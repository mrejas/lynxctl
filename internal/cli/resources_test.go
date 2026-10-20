// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSchedules(t *testing.T) {
	srv, env := valueSetup(t)
	const coll = "/api/v2/schedule/10"

	r := env.mustRun("", "schedule", "create", "--function", "lamp", "--value", "on", "--cron", "0 7 * * 1,2,3,4,5")
	if !strings.Contains(r.stderr, "Created schedule.") {
		t.Errorf("stderr: %s", r.stderr)
	}
	id := srv.nextID
	s := srv.doc(coll, id)
	if s["topic"] != "set/obj/zwave/13/switch" || s["value"] != 1.0 || s["minute"] != "0" || s["hour"] != "7" ||
		s["day_of_week"] != "1,2,3,4,5" || s["active"] != true || s["installation_id"] != 10.0 {
		t.Errorf("created schedule = %v", s)
	}

	r = env.mustRun("", "schedule", "list")
	if !strings.Contains(r.stdout, "0 7 * * 1,2,3,4,5") || !strings.Contains(r.stdout, "yes") {
		t.Errorf("schedule list:\n%s", r.stdout)
	}

	env.mustRun("", "schedule", "disable", itoa(id))
	if srv.doc(coll, id)["active"] != false {
		t.Errorf("after disable: %v", srv.doc(coll, id))
	}
	env.mustRun("", "schedule", "enable", itoa(id))
	if srv.doc(coll, id)["active"] != true {
		t.Errorf("after enable: %v", srv.doc(coll, id))
	}

	var seen []string
	env.editor = editWith(&seen, func(s string) string { return strings.Replace(s, "value: 1", "value: 0", 1) })
	env.mustRun("", "schedule", "edit", itoa(id))
	if got := srv.doc(coll, id); got["value"] != 0.0 || got["installation_id"] != 10.0 {
		t.Errorf("after edit: %v", got)
	}
	if strings.Contains(seen[0], "\ninstallation_id:") || !strings.Contains(seen[0], "# Read-only: id") {
		t.Errorf("editor showed read-only fields as editable:\n%s", seen[0])
	}

	for _, args := range [][]string{
		{"schedule", "create", "--topic", "x", "--value", "1", "--cron", "0 7 * *"},
		{"schedule", "create", "--cron", "0 7 * * *"},
		{"schedule", "create", "--function", "lamp", "--topic", "x", "--value", "1"},
	} {
		if r := env.run("", args...); r.code != ExitUsage {
			t.Errorf("%v: exit %d, stderr %s", args, r.code, r.stderr)
		}
	}
	if r := env.run("", "schedule", "get", "lamp"); r.code != ExitNotFound {
		t.Errorf("schedules have no names, but get lamp: exit %d", r.code)
	}

	env.mustRun("", "schedule", "delete", itoa(id), "--yes")
	if srv.doc(coll, id) != nil {
		t.Error("schedule not deleted")
	}
}

func itoa(n int64) string {
	data, _ := json.Marshal(n)
	return string(data)
}

func TestNotifications(t *testing.T) {
	srv, env := setup(t)

	if r := env.mustRun("", "notification", "executor", "list", "-q"); r.stdout != "1\n2\n" {
		t.Errorf("executor list -q = %q", r.stdout)
	}
	msgID := createID(t, env, "", "notification", "message", "create", "--name", "Alarm", "--text", "{{.installation.Name}}: {{.payload.message}}")
	outID := createID(t, env, "", "notif", "output", "create", "--name", "Mail on-call", "--executor", "mail", "--message", "alarm",
		"--config", "recipients=oncall@example.com", "--config", "subject=Alarm")

	out := srv.doc("/api/v2/notification/10/output", outID)
	cfg, _ := out["config"].(map[string]any)
	if out["notification_output_executor_id"] != 1.0 || out["notification_message_id"] != float64(msgID) ||
		cfg["recipients"] != "oncall@example.com" || out["installation_id"] != 10.0 {
		t.Errorf("created output = %v", out)
	}

	r := env.mustRun("", "notification", "output", "list")
	if !strings.Contains(r.stdout, "Mail on-call") {
		t.Errorf("output list:\n%s", r.stdout)
	}
	r = env.mustRun("", "notification", "message", "get", "alarm")
	if !strings.Contains(r.stdout, "{{.installation.Name}}") {
		t.Errorf("message get:\n%s", r.stdout)
	}

	env.mustRun("", "notification", "send", "on-call", "-F", "message=Water leak", "-F", "level=3")
	if len(srv.sent) != 1 || !strings.HasPrefix(srv.sent[0], itoa(outID)+" ") {
		t.Fatalf("sent = %v", srv.sent)
	}
	var payload map[string]any
	json.Unmarshal([]byte(strings.SplitN(srv.sent[0], " ", 2)[1]), &payload)
	if payload["message"] != "Water leak" || payload["level"] != 3.0 {
		t.Errorf("payload = %v", payload)
	}

	if r := env.run("", "notification", "output", "create", "--name", "x"); r.code != ExitUsage {
		t.Errorf("output without executor: exit %d", r.code)
	}
	if r := env.run("", "notification", "send", "on-call", "-f", "-", "-F", "a=b"); r.code != ExitUsage {
		t.Errorf("send with -f and -F: exit %d", r.code)
	}
}

func TestEdgeApps(t *testing.T) {
	srv, env := setup(t)
	appID := createID(t, env, "", "edge-app", "create", "--name", "Heating control", "--category", "climate",
		"--short-description", "Keeps it warm", "--description", "# Heating", "--tag", "heating")
	app := srv.doc("/api/v2/edge/app", appID)
	if p, _ := app["publisher"].(map[string]any); p["id"] != 2.0 || app["public"] != false {
		t.Errorf("created app = %v (publisher should default to the user's only organization)", app)
	}

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "app.lua"), []byte("print('v1')\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "app.json"), []byte(`{"name":"heating"}`), 0o644)
	r := env.mustRun("", "edge-app", "upload", "heating", dir)
	hash := strings.TrimSpace(r.stdout)
	if hash == "" || !strings.Contains(r.stderr, "edge-app publish 'Heating control' <version>") {
		t.Errorf("upload: stdout %q, stderr %q", r.stdout, r.stderr)
	}
	if r := env.mustRun("", "edge-app", "upload", "heating", dir); !strings.Contains(r.stderr, "Nothing uploaded") {
		t.Errorf("identical upload: %s", r.stderr)
	}
	if r := env.run("", "edge-app", "upload", "heating", t.TempDir()); r.code != ExitUsage {
		t.Errorf("upload of an empty directory: exit %d", r.code)
	}

	if r := env.run("", "edge-app", "options", "heating"); r.code != ExitError || !strings.Contains(r.stderr, "no published version") {
		t.Errorf("options before publishing: exit %d, stderr %s", r.code, r.stderr)
	}
	env.mustRun("", "edge-app", "publish", "heating", "1.0.0")
	if r := env.run("", "edge-app", "publish", "heating", "1.0.1"); r.code != ExitError || !strings.Contains(r.stderr, "no unpublished upload") {
		t.Errorf("publishing twice: exit %d, stderr %s", r.code, r.stderr)
	}
	r = env.mustRun("", "edge-app", "versions", "heating")
	if !strings.Contains(r.stdout, "1.0.0") || !strings.Contains(r.stdout, hash) {
		t.Errorf("versions:\n%s", r.stdout)
	}

	out := filepath.Join(t.TempDir(), "heating.lua")
	env.mustRun("", "edge-app", "download", "heating", "-O", out)
	if data, _ := os.ReadFile(out); !strings.Contains(string(data), "print('v1')") {
		t.Errorf("downloaded %q", data)
	}
	if r := env.mustRun("", "edge-app", "download", "heating"); !strings.Contains(r.stdout, "print('v1')") {
		t.Errorf("download to stdout: %q", r.stdout)
	}
	if r := env.mustRun("", "edge-app", "options", "heating"); !strings.Contains(r.stdout, "app.json for 1.0.0") {
		t.Errorf("options: %s", r.stdout)
	}

	instID := createID(t, env, "", "edge-app", "instance", "create", "--app", "heating", "--config", "target=21", "--config", "sensor.id=1042")
	inst := srv.doc("/api/v2/edge/app/configured/10", instID)
	cfg, _ := inst["config"].(map[string]any)
	sensor, _ := cfg["sensor"].(map[string]any)
	if inst["app_id"] != float64(appID) || inst["version"] != "1.0.0" || inst["name"] != "Heating control" ||
		cfg["target"] != 21.0 || sensor["id"] != 1042.0 {
		t.Errorf("created instance = %v", inst)
	}
	if r := env.mustRun("", "app", "instance", "list", "-q"); r.stdout != itoa(instID)+"\n" {
		t.Errorf("instance list -q = %q", r.stdout)
	}

	var seen []string
	env.editor = editWith(&seen, func(s string) string { return strings.Replace(s, "category: climate", "category: heating", 1) })
	env.mustRun("", "edge-app", "edit", "heating")
	if got := srv.doc("/api/v2/edge/app", appID); got["category"] != "heating" || got["publisher"] == nil {
		t.Errorf("after edit: %v", got)
	}
}

func TestFiles(t *testing.T) {
	srv, env := setup(t)
	dir := t.TempDir()
	png := filepath.Join(dir, "floorplan.png")
	pdf := filepath.Join(dir, "manual.pdf")
	os.WriteFile(png, []byte("\x89PNG\r\n\x1a\nfake"), 0o644)
	os.WriteFile(pdf, []byte("%PDF-1.4 fake"), 0o644)

	r := env.mustRun("", "file", "upload", png, pdf)
	if !strings.Contains(r.stdout, "floorplan.png") || !strings.Contains(r.stdout, "image/png") || !strings.Contains(r.stdout, "application/pdf") {
		t.Errorf("upload:\n%s", r.stdout)
	}
	if r := env.mustRun("", "file", "list", "--mime", "image/*", "-q"); strings.Count(r.stdout, "\n") != 2 {
		// The fake server ignores --mime; this checks the flag is accepted.
		t.Errorf("file list --mime = %q", r.stdout)
	}

	// Download by a part of the name, into the current directory by default.
	work := t.TempDir()
	wd, _ := os.Getwd()
	os.Chdir(work)
	defer os.Chdir(wd)
	env.mustRun("", "file", "download", "floor")
	if data, _ := os.ReadFile(filepath.Join(work, "floorplan.png")); string(data) != "\x89PNG\r\n\x1a\nfake" {
		t.Errorf("downloaded %q", data)
	}
	if r := env.run("", "file", "download", "floor"); r.code != ExitUsage || !strings.Contains(r.stderr, "--force") {
		t.Errorf("download over an existing file: exit %d, stderr %s", r.code, r.stderr)
	}
	env.mustRun("", "file", "download", "floor", "--force")
	if r := env.mustRun("", "file", "download", "manual", "-O", "-"); r.stdout != "%PDF-1.4 fake" {
		t.Errorf("download to stdout = %q", r.stdout)
	}

	newPNG := filepath.Join(dir, "v2.png")
	os.WriteFile(newPNG, []byte("\x89PNG\r\n\x1a\nversion 2"), 0o644)
	if r := env.run("", "file", "replace", "floorplan", pdf); r.code != ExitUsage || !strings.Contains(r.stderr, "same type") {
		t.Errorf("replace with another type: exit %d, stderr %s", r.code, r.stderr)
	}
	env.mustRun("", "file", "replace", "floorplan", newPNG)
	if r := env.mustRun("", "file", "download", "floorplan", "-O", "-"); !strings.Contains(r.stdout, "version 2") {
		t.Errorf("content after replace = %q", r.stdout)
	}

	env.mustRun("", "file", "delete", "manual.pdf", "--yes")
	if len(srv.files) != 1 {
		t.Errorf("%d files left", len(srv.files))
	}

	// Organization files are separate.
	env.mustRun("", "file", "upload", "--org", "2", pdf)
	if r := env.mustRun("", "file", "list", "--org", "2", "-q"); strings.Count(r.stdout, "\n") != 1 {
		t.Errorf("organization files = %q", r.stdout)
	}
	if r := env.mustRun("", "file", "list", "-q"); strings.Count(r.stdout, "\n") != 1 {
		t.Errorf("installation files = %q", r.stdout)
	}
}

func TestSearch(t *testing.T) {
	_, env := valueSetup(t)
	r := env.mustRun("", "search", "kitchen", "-o", "wide")
	for _, want := range []string{"function", "Kitchen temperature", "Main Street 1 (10)", "name=Kitchen temperature"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("search kitchen lacks %q:\n%s", want, r.stdout)
		}
	}
	if r := env.mustRun("", "search", "main", "--type", "installation"); !strings.Contains(r.stdout, "client ID 1010") {
		t.Errorf("search main --type installation:\n%s", r.stdout)
	}
	if r := env.mustRun("", "search", "main", "--type", "installations", "-q"); r.stdout != "10\n11\n" {
		t.Errorf("search main --type installations -q = %q", r.stdout)
	}
	if r := env.mustRun("", "search", "--type", "function,device", "-q"); strings.Count(r.stdout, "\n") != 3 {
		t.Errorf("search everything of two types = %q", r.stdout)
	}
	if r := env.run("", "search", "x", "--type", "gateway"); r.code != ExitUsage {
		t.Errorf("invalid --type: exit %d", r.code)
	}
}
