// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/mrejas/lynx-cli/internal/api"
)

// fakeServer is a small in-memory stand-in for the IoT Open API.
type fakeServer struct {
	*httptest.Server

	mu            sync.Mutex
	validKeys     map[string]bool
	loggedOut     []string
	installations map[int64]*api.Installation
	// hiddenInfo lists installations left out of /installationinfo, as the
	// real server does for some installations.
	hiddenInfo map[int64]bool
	objects    map[api.ObjectKind]map[int64]*api.Object
	nextID     int64

	// Values: the full log, and what was published.
	values    []api.Status
	published []api.Publish
	lastLog   string // raw query of the last log request

	// Endpoints of fakeserver_more_test.go.
	docs        docStore
	files       map[int64]*fakeFile
	appVersions map[int64][]map[string]any
	appCode     map[string][]byte
	sent        []string

	tokens         map[int64]api.JSONObject
	passwordResets []int64
	passwords      []string

	// The last request to /api/v2/echo/, for testing the api command.
	lastPath   string
	lastQuery  string
	lastMethod string
	lastBody   string
}

func newFakeServer(t *testing.T) *fakeServer {
	f := &fakeServer{
		validKeys: map[string]bool{"api-key-1": true},
		installations: map[int64]*api.Installation{
			10: {ID: 10, Name: "Main Street 1", ClientID: 1010, OrganizationID: 2, Users: []int64{7}, Meta: api.Meta{}},
			11: {ID: 11, Name: "Main Street 2", ClientID: 1011, OrganizationID: 2, Users: []int64{7}, Meta: api.Meta{}},
			12: {ID: 12, Name: "Summer house", ClientID: 1012, OrganizationID: 2, Users: []int64{7}, Meta: api.Meta{}},
		},
		objects: map[api.ObjectKind]map[int64]*api.Object{api.DeviceX: {}, api.FunctionX: {}},
		nextID:  1000,
	}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/v2/auth", func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		switch {
		case user == "jane@example.com" && pass == "secret":
			f.issue(w, "token-jane", "")
		case user == "twofa@example.com" && pass == "secret":
			f.issue(w, "token-challenge", "smslogin")
		default:
			unauthorized(w)
		}
	})
	mux.HandleFunc("PUT /api/v2/auth", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Challenge string }
		json.NewDecoder(r.Body).Decode(&body)
		if r.Header.Get("X-API-Key") != "token-challenge" || body.Challenge != "123456" {
			unauthorized(w)
			return
		}
		f.issue(w, "token-2fa", "")
	})
	mux.HandleFunc("DELETE /api/v2/auth", func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-API-Key")
		f.loggedOut = append(f.loggedOut, key)
		delete(f.validKeys, key)
	})
	mux.HandleFunc("GET /api/v2/user/me", f.authed(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":7,"email":"jane@example.com","first_name":"Jane","last_name":"Doe","role":1,"organisations":[2]}`))
	}))
	mux.HandleFunc("/api/v2/echo/", f.authed(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.lastMethod, f.lastPath, f.lastQuery, f.lastBody = r.Method, r.URL.Path, r.URL.RawQuery, string(body)
		w.Write([]byte(`[{"id":1042,"type":"switch"}]`))
	}))

	// Installations.
	mux.HandleFunc("GET /api/v2/installationinfo", f.authed(func(w http.ResponseWriter, r *http.Request) {
		var list []api.InstallationInfo
		for _, inst := range f.sortedInstallations() {
			if f.hiddenInfo[inst.ID] {
				continue
			}
			list = append(list, api.InstallationInfo{ID: inst.ID, Name: inst.Name, ClientID: inst.ClientID, OrganizationID: inst.OrganizationID})
		}
		writeJSON(w, list)
	}))
	mux.HandleFunc("GET /api/v2/installation", f.authed(func(w http.ResponseWriter, r *http.Request) {
		list := []*api.Installation{}
		for _, inst := range f.sortedInstallations() {
			if fakeMetaMatches(inst.Meta, r.URL.Query()) {
				list = append(list, inst)
			}
		}
		writeJSON(w, list)
	}))
	mux.HandleFunc("POST /api/v2/installation", f.authed(func(w http.ResponseWriter, r *http.Request) {
		var inst api.Installation
		if !readJSON(w, r, &inst) {
			return
		}
		inst.ID = f.newID()
		inst.ClientID = inst.ID + 1000
		f.installations[inst.ID] = &inst
		writeJSON(w, inst)
	}))
	mux.HandleFunc("/api/v2/installation/{id}", f.authed(func(w http.ResponseWriter, r *http.Request) {
		inst, ok := f.installations[pathID(r, "id")]
		if !ok {
			notFound(w)
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, inst)
		case http.MethodPut:
			var updated api.Installation
			if !readJSON(w, r, &updated) {
				return
			}
			updated.ID, updated.ClientID = inst.ID, inst.ClientID
			f.installations[inst.ID] = &updated
			writeJSON(w, updated)
		case http.MethodDelete:
			delete(f.installations, inst.ID)
			writeJSON(w, map[string]string{"message": "Deleted"})
		}
	}))
	mux.HandleFunc("/api/v2/installation/{id}/meta/{key}", f.authed(func(w http.ResponseWriter, r *http.Request) {
		inst, ok := f.installations[pathID(r, "id")]
		if !ok {
			notFound(w)
			return
		}
		f.handleMeta(w, r, inst.Meta, nil)
	}))

	// Devices and functions.
	for _, kind := range []api.ObjectKind{api.DeviceX, api.FunctionX} {
		base := "/api/v2/" + string(kind) + "/{inst}"
		mux.HandleFunc("GET "+base, f.authed(func(w http.ResponseWriter, r *http.Request) {
			inst := pathID(r, "inst")
			q := r.URL.Query()
			list := []*api.Object{}
			for _, o := range f.sortedObjects(kind) {
				if o.InstallationID != inst || (q.Get("type") != "" && q.Get("type") != o.Type) {
					continue
				}
				if fakeMetaMatches(o.Meta, q) {
					list = append(list, o)
				}
			}
			writeJSON(w, list)
		}))
		mux.HandleFunc("POST "+base, f.authed(func(w http.ResponseWriter, r *http.Request) {
			var o api.Object
			if !readJSON(w, r, &o) {
				return
			}
			o.ID = f.newID()
			o.InstallationID = pathID(r, "inst")
			o.Created, o.Updated = 1700000000, 1700000000
			f.objects[kind][o.ID] = &o
			writeJSON(w, o)
		}))
		mux.HandleFunc(base+"/{id}", f.authed(func(w http.ResponseWriter, r *http.Request) {
			o, ok := f.objects[kind][pathID(r, "id")]
			if !ok || o.InstallationID != pathID(r, "inst") {
				notFound(w)
				return
			}
			switch r.Method {
			case http.MethodGet:
				writeJSON(w, o)
			case http.MethodPut:
				var updated api.Object
				if !readJSON(w, r, &updated) {
					return
				}
				if updated.Type == "invalid" {
					w.WriteHeader(http.StatusBadRequest)
					w.Write([]byte(`{"message":"invalid type"}`))
					return
				}
				updated.ID, updated.InstallationID, updated.Created = o.ID, o.InstallationID, o.Created
				f.objects[kind][o.ID] = &updated
				writeJSON(w, updated)
			case http.MethodDelete:
				delete(f.objects[kind], o.ID)
				writeJSON(w, map[string]string{"message": "Deleted"})
			}
		}))
		mux.HandleFunc(base+"/{id}/meta/{key}", f.authed(func(w http.ResponseWriter, r *http.Request) {
			o, ok := f.objects[kind][pathID(r, "id")]
			if !ok {
				notFound(w)
				return
			}
			if o.Meta == nil {
				o.Meta = api.Meta{}
			}
			if o.ProtectedMeta == nil {
				o.ProtectedMeta = api.Meta{}
			}
			f.handleMeta(w, r, o.Meta, o.ProtectedMeta)
		}))
	}

	// Values.
	mux.HandleFunc("/api/v2/status/{inst}", f.authed(func(w http.ResponseWriter, r *http.Request) {
		var topics []string
		if r.Method == http.MethodPost && !readJSON(w, r, &topics) {
			return
		}
		latest := map[string]api.Status{}
		for _, v := range f.values {
			if v.InstallationID == pathID(r, "inst") && (len(topics) == 0 || slices.Contains(topics, v.Topic)) {
				if cur, ok := latest[v.Topic]; !ok || v.Timestamp > cur.Timestamp {
					latest[v.Topic] = v
				}
			}
		}
		list := []api.Status{}
		for _, v := range latest {
			list = append(list, v)
		}
		writeJSON(w, list)
	}))
	mux.HandleFunc("GET /api/v3beta/log/{inst}", f.authed(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f.lastLog = r.URL.RawQuery
		from, _ := strconv.ParseFloat(q.Get("from"), 64)
		to, _ := strconv.ParseFloat(q.Get("to"), 64)
		limit, _ := strconv.Atoi(q.Get("limit"))
		if limit == 0 {
			limit = 10000
		}
		var matching []api.Status
		for _, v := range f.values {
			if v.InstallationID != pathID(r, "inst") || (len(q["topics"]) > 0 && !slices.Contains(q["topics"], v.Topic)) ||
				(from != 0 && v.Timestamp < from) || (to != 0 && v.Timestamp > to) {
				continue
			}
			matching = append(matching, v)
		}
		sort.Slice(matching, func(i, j int) bool { return matching[i].Timestamp > matching[j].Timestamp })
		page := api.LogPage{Total: len(matching), Data: matching[:min(limit, len(matching))]}
		page.Count = len(page.Data)
		writeJSON(w, page)
	}))
	mux.HandleFunc("POST /api/v2/mqtt/publish/{inst}", f.authed(func(w http.ResponseWriter, r *http.Request) {
		var p api.Publish
		if !readJSON(w, r, &p) {
			return
		}
		f.published = append(f.published, p)
		writeJSON(w, map[string]string{"message": "ok"})
	}))

	f.registerMore(mux)
	f.Server = httptest.NewServer(f.locked(mux))
	t.Cleanup(f.Close)
	return f
}

// locked serializes all requests so handlers can use the maps freely.
func (f *fakeServer) locked(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		h.ServeHTTP(w, r)
	})
}

func (f *fakeServer) handleMeta(w http.ResponseWriter, r *http.Request, meta, protected api.Meta) {
	key := r.PathValue("key")
	switch r.Method {
	case http.MethodPut:
		var v api.MetaValue
		if !readJSON(w, r, &v) {
			return
		}
		_, exists := meta[key]
		_, existsProtected := protected[key]
		if !exists && !existsProtected && r.URL.Query().Get("create_missing") != "true" {
			notFound(w)
			return
		}
		delete(meta, key)
		delete(protected, key)
		if v.Protected && protected != nil {
			protected[key] = v.Value
		} else {
			meta[key] = v.Value
		}
		writeJSON(w, v)
	case http.MethodDelete:
		_, exists := meta[key]
		_, existsProtected := protected[key]
		if !exists && !existsProtected {
			notFound(w)
			return
		}
		delete(meta, key)
		delete(protected, key)
		writeJSON(w, map[string]string{"message": "Deleted"})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// host is the profile name a login to this server gets by default.
func (f *fakeServer) host() string {
	return strings.TrimPrefix(f.URL, "http://")
}

func (f *fakeServer) newID() int64 {
	f.nextID++
	return f.nextID
}

func (f *fakeServer) sortedInstallations() []*api.Installation {
	list := make([]*api.Installation, 0, len(f.installations))
	for _, inst := range f.installations {
		list = append(list, inst)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list
}

func (f *fakeServer) sortedObjects(kind api.ObjectKind) []*api.Object {
	list := make([]*api.Object, 0, len(f.objects[kind]))
	for _, o := range f.objects[kind] {
		list = append(list, o)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list
}

// addValue records a reported value of a (relative) topic.
func (f *fakeServer) addValue(installationID int64, topic string, value, timestamp float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cid := f.installations[installationID].ClientID
	f.values = append(f.values, api.Status{
		ClientID: cid, InstallationID: installationID, Timestamp: timestamp, Value: value,
		Topic: strconv.FormatInt(cid, 10) + "/" + topic,
	})
}

// object returns a stored object, for assertions in tests.
func (f *fakeServer) object(kind api.ObjectKind, id int64) *api.Object {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.objects[kind][id]
}

func (f *fakeServer) count(kind api.ObjectKind) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.objects[kind])
}

func (f *fakeServer) issue(w http.ResponseWriter, token, nextStep string) {
	f.validKeys[token] = true
	json.NewEncoder(w).Encode(map[string]string{"token": token, "nextstep": nextStep})
}

func (f *fakeServer) authed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !f.validKeys[r.Header.Get("X-API-Key")] {
			unauthorized(w)
			return
		}
		h(w, r)
	}
}

// fakeMetaMatches applies the API's meta filters: every query parameter
// except "type" must match the meta key of the same name; * is a wildcard.
func fakeMetaMatches(meta api.Meta, q map[string][]string) bool {
	for key, values := range q {
		if key == "type" {
			continue
		}
		v, ok := meta[key]
		for _, want := range values {
			if !ok || !globMatch(want, v) {
				return false
			}
		}
	}
	return true
}

func pathID(r *http.Request, name string) int64 {
	id, _ := strconv.ParseInt(r.PathValue(name), 10, 64)
	return id
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"message":%q}`, err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	json.NewEncoder(w).Encode(v)
}

func notFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	w.Write([]byte(`{"message":"Not Found"}`))
}

func unauthorized(w http.ResponseWriter) {
	w.WriteHeader(http.StatusUnauthorized)
	w.Write([]byte(`{"message":"Unauthorized"}`))
}
