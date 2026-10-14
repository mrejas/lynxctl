// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mrejas/lynx-cli/internal/api"
)

// Generic documents, keyed by collection path, then ID.
type docStore map[string]map[int64]api.JSONObject

// fakeFile is a stored file with its content.
type fakeFile struct {
	meta api.JSONObject
	data []byte
}

// registerMore adds the endpoints of schedules, notifications, edge apps,
// files and search.
func (f *fakeServer) registerMore(mux *http.ServeMux) {
	f.docs = docStore{}
	f.files = map[int64]*fakeFile{}
	f.appVersions = map[int64][]map[string]any{}
	f.appCode = map[string][]byte{}
	f.docs["/api/v2/notification/10/executor"] = map[int64]api.JSONObject{
		1: {"id": int64(1), "name": "Mail", "type": "mail", "organization_id": int64(2), "config": map[string]any{}},
		2: {"id": int64(2), "name": "SMS", "type": "sms", "organization_id": int64(2), "config": map[string]any{}},
	}

	for _, pattern := range []string{
		"/api/v2/schedule/{inst}",
		"/api/v2/notification/{inst}/message",
		"/api/v2/notification/{inst}/output",
		"/api/v2/notification/{inst}/executor",
	} {
		mux.HandleFunc(pattern, f.authed(f.collectionHandler))
		mux.HandleFunc(pattern+"/{id}", f.authed(f.itemHandler))
	}
	mux.HandleFunc("POST /api/v2/notification/{inst}/output/{id}/send", f.authed(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.sent = append(f.sent, r.PathValue("id")+" "+string(body))
		writeJSON(w, map[string]string{"message": "sent"})
	}))

	// Edge apps are routed by hand: "/edge/app/{id}/version" and
	// "/edge/app/configured/{inst}" overlap as ServeMux patterns.
	mux.HandleFunc("/api/v2/edge/app", f.authed(f.edgeApps))
	mux.HandleFunc("/api/v2/edge/app/", f.authed(f.edgeApps))

	mux.HandleFunc("/api/v2/file/{owner}/{oid}", f.authed(f.fileCollection))
	mux.HandleFunc("/api/v2/file/{owner}/{oid}/{id}", f.authed(f.fileItem))
	mux.HandleFunc("GET /api/v2/file/download/{hash}", f.authed(func(w http.ResponseWriter, r *http.Request) {
		for _, file := range f.files {
			if file.meta["hash"] == r.PathValue("hash") {
				w.Header().Set("Content-Type", str(file.meta["mime"]))
				w.Write(file.data)
				return
			}
		}
		notFound(w)
	}))

	mux.HandleFunc("GET /api/v3beta/search", f.authed(f.search))

	// Administration.
	f.docs["/api/v2/organization"] = map[int64]api.JSONObject{
		2: {"id": int64(2), "name": "ACME", "parent": int64(0), "meta": map[string]any{}},
		3: {"id": int64(3), "name": "ACME Stockholm", "parent": int64(2), "meta": map[string]any{}},
		4: {"id": int64(4), "name": "ACME Göteborg", "parent": int64(2), "meta": map[string]any{}},
		5: {"id": int64(5), "name": "Stockholm South", "parent": int64(3), "meta": map[string]any{}},
	}
	f.docs["/api/v2/role"] = map[int64]api.JSONObject{
		1: {"id": int64(1), "name": "Admin", "priority": int64(100), "permissions": map[string]any{"#": true}},
		2: {"id": int64(2), "name": "User", "priority": int64(10), "permissions": map[string]any{"installation/list/all": true, "functionx/+/delete": false}},
	}
	f.docs["/api/v2/user"] = map[int64]api.JSONObject{
		7: {"id": int64(7), "email": "jane@example.com", "first_name": "Jane", "last_name": "Doe", "role": int64(1), "organisations": []any{int64(2)}, "meta": map[string]any{}},
		8: {"id": int64(8), "email": "bob@example.com", "first_name": "Bob", "last_name": "Smith", "role": int64(2), "organisations": []any{int64(3)}, "meta": map[string]any{}},
	}
	f.tokens = map[int64]api.JSONObject{
		50: {"id": int64(50), "name": "laptop", "type": "user_created", "expires": int64(0), "current": true},
	}
	for _, base := range []string{"/api/v2/organization", "/api/v2/user", "/api/v2/role"} {
		mux.HandleFunc(base, f.authed(f.collectionHandler))
		mux.HandleFunc(base+"/{id}", f.authed(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodDelete {
				f.lastQuery = r.URL.RawQuery
			}
			f.itemHandler(w, r)
		}))
	}
	for _, base := range []string{"/api/v2/organization", "/api/v2/user"} {
		coll := base
		mux.HandleFunc(base+"/{id}/meta/{key}", f.authed(func(w http.ResponseWriter, r *http.Request) {
			o, ok := f.docs[coll][pathID(r, "id")]
			if !ok {
				notFound(w)
				return
			}
			meta := toMeta(o["meta"])
			f.handleMeta(w, r, meta, nil)
			m := map[string]any{}
			for k, v := range meta {
				m[k] = v
			}
			o["meta"] = m
		}))
	}
	mux.HandleFunc("POST /api/v2/organization/{id}/force_password_reset", f.authed(func(w http.ResponseWriter, r *http.Request) {
		f.passwordResets = append(f.passwordResets, pathID(r, "id"))
		writeJSON(w, f.docs["/api/v2/organization"][pathID(r, "id")])
	}))
	mux.HandleFunc("PUT /api/v2/user/{id}/password", f.authed(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if !readJSON(w, r, &body) {
			return
		}
		if pathID(r, "id") == 7 && body["current_password"] != "secret" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"message":"wrong current password"}`))
			return
		}
		f.passwords = append(f.passwords, r.PathValue("id")+":"+body["new_password"])
		writeJSON(w, map[string]string{"message": "ok"})
	}))
	mux.HandleFunc("/api/v2/user/{id}/security/token", f.authed(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			list := []api.JSONObject{}
			for _, t := range f.tokens {
				list = append(list, t)
			}
			sort.Slice(list, func(i, j int) bool { return num(list[i], "id") < num(list[j], "id") })
			writeJSON(w, list)
			return
		}
		var t api.JSONObject
		if !readJSON(w, r, &t) {
			return
		}
		t["id"] = f.newID()
		t["type"] = "user_created"
		f.tokens[num(t, "id")] = t
		created := api.JSONObject{"token": "secret-token-value"}
		for k, v := range t {
			created[k] = v
		}
		writeJSON(w, created)
	}))
	mux.HandleFunc("DELETE /api/v2/user/{id}/security/token/{tid}", f.authed(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := f.tokens[pathID(r, "tid")]; !ok {
			notFound(w)
			return
		}
		delete(f.tokens, pathID(r, "tid"))
		writeJSON(w, map[string]string{"message": "deleted"})
	}))
	mux.HandleFunc("GET /api/v2/permission", f.authed(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]bool{"installation/list/all": true, "functionx/+/add": true, "functionx/zwave/add": false})
	}))
}

func (f *fakeServer) collectionHandler(w http.ResponseWriter, r *http.Request) {
	coll := r.URL.Path
	switch r.Method {
	case http.MethodGet:
		list := []api.JSONObject{}
		for _, o := range f.docs[coll] {
			if exec := r.URL.Query().Get("executor"); exec != "" && o["executor"] != exec {
				continue
			}
			list = append(list, o)
		}
		sort.Slice(list, func(i, j int) bool { return num(list[i], "id") < num(list[j], "id") })
		writeJSON(w, list)
	case http.MethodPost:
		var o api.JSONObject
		if !readJSON(w, r, &o) {
			return
		}
		o["id"] = f.newID()
		if f.docs[coll] == nil {
			f.docs[coll] = map[int64]api.JSONObject{}
		}
		f.docs[coll][num(o, "id")] = o
		if strings.HasPrefix(coll, "/api/v2/schedule/") {
			// Like the real API, creating a schedule returns no body.
			return
		}
		writeJSON(w, o)
	}
}

func (f *fakeServer) itemHandler(w http.ResponseWriter, r *http.Request) {
	coll := r.URL.Path[:strings.LastIndex(r.URL.Path, "/")]
	o, ok := f.docs[coll][pathID(r, "id")]
	if !ok {
		notFound(w)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, o)
	case http.MethodPut:
		var updated api.JSONObject
		if !readJSON(w, r, &updated) {
			return
		}
		updated["id"] = o["id"]
		f.docs[coll][num(o, "id")] = updated
		writeJSON(w, updated)
	case http.MethodDelete:
		delete(f.docs[coll], num(o, "id"))
		writeJSON(w, map[string]string{"message": "deleted"})
	}
}

// doc returns a stored document, for assertions.
func (f *fakeServer) doc(coll string, id int64) api.JSONObject {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.docs[coll][id]
}

func (f *fakeServer) edgeApps(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v2/edge/app"), "/")
	parts := strings.Split(rest, "/")
	apps := "/api/v2/edge/app"
	switch {
	case rest == "" && r.Method == http.MethodPut:
		var o api.JSONObject
		if !readJSON(w, r, &o) {
			return
		}
		if _, ok := f.docs[apps][num(o, "id")]; !ok {
			notFound(w)
			return
		}
		f.docs[apps][num(o, "id")] = o
		writeJSON(w, o)
	case rest == "":
		f.collectionHandler(w, r)
	case parts[0] == "configured":
		r.URL.Path = "/api/v2/edge/app/configured/" + parts[1]
		if len(parts) == 2 {
			f.collectionHandler(w, r)
			return
		}
		r.URL.Path += "/" + parts[2]
		r.SetPathValue("id", parts[2])
		f.itemHandler(w, r)
	case len(parts) == 1:
		r.SetPathValue("id", parts[0])
		f.itemHandler(w, r)
	default:
		id, _ := strconv.ParseInt(parts[0], 10, 64)
		if _, ok := f.docs[apps][id]; !ok {
			notFound(w)
			return
		}
		f.appAction(w, r, id, parts[1])
	}
}

func (f *fakeServer) appAction(w http.ResponseWriter, r *http.Request, id int64, action string) {
	switch action {
	case "version":
		if r.Method == http.MethodGet {
			list := []map[string]any{}
			for _, v := range f.appVersions[id] {
				if v["name"] != "" || r.URL.Query().Get("untagged") == "true" {
					list = append(list, v)
				}
			}
			writeJSON(w, list)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var content []byte
		for _, field := range []string{"app_json", "app_lua"} {
			if file, _, err := r.FormFile(field); err == nil {
				data, _ := io.ReadAll(file)
				content = append(content, data...)
			}
		}
		sum := sha256.Sum256(content)
		hash := hex.EncodeToString(sum[:8])
		if versions := f.appVersions[id]; len(versions) > 0 && versions[len(versions)-1]["hash"] == hash {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		f.appVersions[id] = append(f.appVersions[id], map[string]any{"name": "", "hash": hash, "timestamp": time.Now().UnixNano() / 1e3})
		f.appCode[hash] = content
		writeJSON(w, map[string]string{"hash": hash})
	case "publish":
		var v map[string]any
		if !readJSON(w, r, &v) {
			return
		}
		for _, existing := range f.appVersions[id] {
			if existing["hash"] == v["hash"] {
				existing["name"] = v["name"]
				writeJSON(w, existing)
				return
			}
		}
		notFound(w)
	case "download", "configure":
		for _, v := range f.appVersions[id] {
			if v["name"] == r.URL.Query().Get("version") {
				if action == "configure" {
					writeJSON(w, map[string]any{"name": "app.json for " + str(v["name"])})
				} else {
					w.Write(f.appCode[str(v["hash"])])
				}
				return
			}
		}
		notFound(w)
	default:
		notFound(w)
	}
}

func (f *fakeServer) fileCollection(w http.ResponseWriter, r *http.Request) {
	owner, oid := r.PathValue("owner"), pathID(r, "oid")
	switch r.Method {
	case http.MethodGet:
		list := []api.JSONObject{}
		for _, file := range f.files {
			if file.meta["owner"] == owner && num(file.meta, "owner_id") == oid {
				list = append(list, file.meta)
			}
		}
		sort.Slice(list, func(i, j int) bool { return num(list[i], "id") < num(list[j], "id") })
		writeJSON(w, list)
	case http.MethodPost:
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		created := []api.JSONObject{}
		for _, fh := range r.MultipartForm.File["filename"] {
			file, _ := fh.Open()
			data, _ := io.ReadAll(file)
			sum := sha256.Sum256(data)
			meta := api.JSONObject{
				"id": f.newID(), "name": fh.Filename, "mime": fh.Header.Get("Content-Type"),
				"hash": hex.EncodeToString(sum[:]), "owner": owner, "owner_id": oid, "updated": int64(1700000000),
			}
			f.files[num(meta, "id")] = &fakeFile{meta: meta, data: data}
			created = append(created, meta)
		}
		writeJSON(w, created)
	}
}

func (f *fakeServer) fileItem(w http.ResponseWriter, r *http.Request) {
	file, ok := f.files[pathID(r, "id")]
	if !ok || file.meta["owner"] != r.PathValue("owner") {
		notFound(w)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, file.meta)
	case http.MethodPut:
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		part, fh, err := r.FormFile("filename")
		if err != nil || fh.Header.Get("Content-Type") != file.meta["mime"] {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"message":"content type must not change"}`))
			return
		}
		file.data, _ = io.ReadAll(part)
		sum := sha256.Sum256(file.data)
		file.meta["hash"] = hex.EncodeToString(sum[:])
		writeJSON(w, file.meta)
	case http.MethodDelete:
		delete(f.files, num(file.meta, "id"))
		writeJSON(w, map[string]string{"message": "deleted"})
	}
}

func (f *fakeServer) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	needle := strings.ToLower(strings.Trim(q.Get("q"), "*"))
	types := q.Get("types")
	want := func(t string) bool { return types == "" || strings.Contains(types, t) }
	results := []map[string]any{}
	match := func(name string, meta api.Meta) bool {
		for key, values := range q {
			if strings.HasPrefix(key, "metadata.") && !strings.EqualFold(meta[strings.TrimPrefix(key, "metadata.")], values[0]) {
				return false
			}
		}
		return strings.Contains(strings.ToLower(name), needle)
	}
	if want("installation") {
		for _, inst := range f.sortedInstallations() {
			if match(inst.Name, inst.Meta) {
				results = append(results, map[string]any{"type": "installation", "id": inst.ID, "name": inst.Name, "clientID": strconv.FormatInt(inst.ClientID, 10)})
			}
		}
	}
	for kind, t := range map[api.ObjectKind]string{api.DeviceX: "device", api.FunctionX: "function"} {
		if !want(t) {
			continue
		}
		for _, o := range f.sortedObjects(kind) {
			if match(o.Name(), o.Meta) {
				results = append(results, map[string]any{"type": t, "id": o.ID, "name": o.Name(), "installationID": o.InstallationID})
			}
		}
	}
	data, _ := json.Marshal(map[string]any{"total": len(results), "limit": 10000, "offset": 0, "results": results})
	w.Write(data)
}
