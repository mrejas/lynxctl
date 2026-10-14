// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/mrejas/lynx-cli/internal/api"
)

// setup logs in and selects installation 10.
func setup(t *testing.T) (*fakeServer, *testEnv) {
	t.Helper()
	srv := newFakeServer(t)
	env := newTestEnv(t)
	env.login(srv)
	env.mustRun("", "use", "main street 1")
	return srv, env
}

// createID runs a create command with -q and returns the new ID.
func createID(t *testing.T, env *testEnv, stdin string, args ...string) int64 {
	t.Helper()
	r := env.mustRun(stdin, append(args, "-q")...)
	id, err := strconv.ParseInt(strings.TrimSpace(r.stdout), 10, 64)
	if err != nil {
		t.Fatalf("%v printed %q, want an ID", args, r.stdout)
	}
	return id
}

func TestDevicesAndFunctions(t *testing.T) {
	srv, env := setup(t)

	devID := createID(t, env, "", "device", "create", "--type", "zwave", "--name", "Hall sensor", "--meta", "node=12")
	fnID := createID(t, env, "", "function", "create", "--type", "temperature", "--name", "Kitchen temperature",
		"--device", "hall", "--meta", "topic_read=obj/zwave/12/temperature")
	createID(t, env, "", "fn", "create", "--type", "switch", "--name", "Kitchen lamp")

	fn := srv.object(api.FunctionX, fnID)
	if fn.InstallationID != 10 || fn.Meta["device_id"] != strconv.FormatInt(devID, 10) || fn.Meta["topic_read"] != "obj/zwave/12/temperature" {
		t.Errorf("created function = %+v", fn)
	}

	// Listing and filtering.
	r := env.mustRun("", "function", "list")
	for _, want := range []string{"ID", "NAME", "TOPIC READ", "Kitchen temperature", "obj/zwave/12/temperature", "Kitchen lamp"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("function list lacks %q:\n%s", want, r.stdout)
		}
	}
	if r := env.mustRun("", "fn", "ls", "--type", "switch", "-q"); strings.Count(r.stdout, "\n") != 1 {
		t.Errorf("--type switch -q = %q", r.stdout)
	}
	if r := env.mustRun("", "fn", "ls", "--meta", "name=Kitchen*", "-q"); strings.Count(r.stdout, "\n") != 2 {
		t.Errorf("--meta name=Kitchen* -q = %q", r.stdout)
	}
	want := strconv.FormatInt(fnID, 10) + "\n"
	if r := env.mustRun("", "function", "list", "--device", "hall", "-q"); r.stdout != want {
		t.Errorf("--device hall -q = %q, want %q", r.stdout, want)
	}
	if r := env.mustRun("", "device", "functions", "sensor", "-q"); r.stdout != want {
		t.Errorf("device functions = %q, want %q", r.stdout, want)
	}
	if r := env.mustRun("", "fn", "ls", "-i", "11", "-q"); r.stdout != "" {
		t.Errorf("functions in another installation: %q", r.stdout)
	}

	// References.
	r = env.mustRun("", "function", "get", "temperature")
	if !strings.Contains(r.stdout, "Kitchen temperature") || !strings.Contains(r.stdout, "topic_read:") {
		t.Errorf("function get:\n%s", r.stdout)
	}
	if r := env.run("", "function", "get", "kitchen"); r.code != ExitNotFound || !strings.Contains(r.stderr, "matches 2 functions") {
		t.Errorf("ambiguous get: exit %d, stderr %s", r.code, r.stderr)
	}
	r = env.mustRun("", "function", "get", strconv.FormatInt(fnID, 10), "-o", "json")
	var got api.Object
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil || got.ID != fnID {
		t.Errorf("get by ID as JSON: %v\n%s", err, r.stdout)
	}

	// Meta.
	env.mustRun("", "function", "meta", "set", "kitchen temp", "unit=°C", "format=%.1f °C")
	if r := env.mustRun("", "function", "meta", "get", "kitchen temp", "unit"); r.stdout != "°C\n" {
		t.Errorf("meta get unit = %q", r.stdout)
	}
	r = env.mustRun("", "function", "meta", "get", "kitchen temp")
	if !strings.Contains(r.stdout, "format") || !strings.Contains(r.stdout, "%.1f °C") {
		t.Errorf("meta get:\n%s", r.stdout)
	}
	env.mustRun("", "function", "meta", "set", "kitchen temp", "secret=42", "--protected")
	if fn := srv.object(api.FunctionX, fnID); fn.ProtectedMeta["secret"] != "42" {
		t.Errorf("protected meta = %v", fn.ProtectedMeta)
	}
	// Updating a protected key keeps it protected.
	env.mustRun("", "function", "meta", "set", "kitchen temp", "secret=43")
	if fn := srv.object(api.FunctionX, fnID); fn.ProtectedMeta["secret"] != "43" || fn.Meta["secret"] != "" {
		t.Errorf("after update: meta %v, protected %v", fn.Meta, fn.ProtectedMeta)
	}
	env.mustRun("", "function", "meta", "unset", "kitchen temp", "unit", "secret")
	if fn := srv.object(api.FunctionX, fnID); fn.Meta["unit"] != "" || len(fn.ProtectedMeta) != 0 {
		t.Errorf("after unset: meta %v, protected %v", fn.Meta, fn.ProtectedMeta)
	}
	if r := env.run("", "function", "meta", "get", "kitchen temp", "unit"); r.code != ExitNotFound {
		t.Errorf("meta get of a missing key: exit %d", r.code)
	}
	if r := env.run("", "function", "meta", "set", "kitchen temp", "novalue"); r.code != ExitUsage {
		t.Errorf("meta set without =: exit %d", r.code)
	}

	// Deleting needs an exact name and confirmation.
	if r := env.run("", "function", "delete", "lamp", "--yes"); r.code != ExitNotFound {
		t.Errorf("delete by partial name: exit %d", r.code)
	}
	if r := env.run("", "function", "delete", "kitchen lamp"); r.code != ExitUsage {
		t.Errorf("delete without --yes: exit %d", r.code)
	}
	env.mustRun("", "function", "delete", "Kitchen Lamp", "--yes")
	if n := srv.count(api.FunctionX); n != 1 {
		t.Errorf("%d functions left, want 1", n)
	}

	// Deleting a device without --cascade leaves its functions.
	r = env.mustRun("", "device", "delete", "Hall sensor", "--yes")
	if !strings.Contains(r.stderr, "1 function will still refer to this device") {
		t.Errorf("no note about remaining functions: %s", r.stderr)
	}
	if n := srv.count(api.FunctionX); n != 1 {
		t.Errorf("functions after plain device delete: %d", n)
	}
}

func TestDeviceDeleteCascade(t *testing.T) {
	srv, env := setup(t)
	createID(t, env, "", "device", "create", "--type", "zwave", "--name", "Hall sensor")
	createID(t, env, "", "function", "create", "--type", "temperature", "--name", "Hall temperature", "--device", "hall")
	createID(t, env, "", "function", "create", "--type", "humidity", "--name", "Hall humidity", "--device", "hall")
	createID(t, env, "", "function", "create", "--type", "switch", "--name", "Unrelated")

	r := env.mustRun("", "device", "rm", "hall sensor", "--cascade", "--yes")
	if !strings.Contains(r.stderr, `Deleted function "Hall temperature"`) || !strings.Contains(r.stderr, `Deleted device "Hall sensor"`) {
		t.Errorf("stderr: %s", r.stderr)
	}
	if srv.count(api.DeviceX) != 0 || srv.count(api.FunctionX) != 1 {
		t.Errorf("left: %d devices, %d functions", srv.count(api.DeviceX), srv.count(api.FunctionX))
	}
}

func TestCreateFromFile(t *testing.T) {
	srv, env := setup(t)
	yaml := `type: temperature
meta:
  name: From file
  device_id: 12
  topic_read: obj/x
`
	// Flags are applied on top of the file.
	id := createID(t, env, yaml, "function", "create", "-f", "-", "--name", "Overridden", "--meta", "unit=°C")
	fn := srv.object(api.FunctionX, id)
	if fn.Type != "temperature" || fn.Meta["name"] != "Overridden" || fn.Meta["device_id"] != "12" || fn.Meta["unit"] != "°C" {
		t.Errorf("created %+v", fn)
	}

	if r := env.run("typo: x\n", "function", "create", "-f", "-"); r.code != ExitUsage || !strings.Contains(r.stderr, "typo") {
		t.Errorf("unknown field: exit %d, stderr %s", r.code, r.stderr)
	}
	if r := env.run("", "function", "create", "--name", "No type"); r.code != ExitUsage || !strings.Contains(r.stderr, "type is required") {
		t.Errorf("missing type: exit %d, stderr %s", r.code, r.stderr)
	}
}

// editWith returns an editor that applies the replacements in turn, one per
// invocation, and records what it was shown.
func editWith(seen *[]string, edits ...func(string) string) func(string) error {
	return func(path string) error {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		*seen = append(*seen, string(data))
		if len(edits) == 0 {
			return nil
		}
		next := edits[0]
		edits = edits[1:]
		return os.WriteFile(path, []byte(next(string(data))), 0o600)
	}
}

func TestEditFunction(t *testing.T) {
	srv, env := setup(t)
	id := createID(t, env, "", "function", "create", "--type", "temperature", "--name", "Kitchen")

	var seen []string
	env.editor = editWith(&seen,
		// First attempt is rejected by the server ...
		func(s string) string { return strings.Replace(s, "type: temperature", "type: invalid", 1) },
		// ... so the editor opens again with the error, and this one works.
		func(s string) string {
			s = strings.Replace(s, "type: invalid", "type: humidity", 1)
			return strings.Replace(s, "name: Kitchen", "name: Kitchen humidity\n    unit: '%'", 1)
		},
	)
	r := env.mustRun("", "function", "edit", "kitchen")
	if len(seen) != 2 || !strings.Contains(seen[1], "# Error: 400 Bad Request: invalid type") {
		t.Fatalf("editor was shown:\n%s", strings.Join(seen, "\n---\n"))
	}
	if strings.Count(seen[1], "# Error:") != 1 || !strings.Contains(seen[0], "# Editing function \"Kitchen\"") {
		t.Errorf("editor content:\n%s", strings.Join(seen, "\n---\n"))
	}
	fn := srv.object(api.FunctionX, id)
	if fn.Type != "humidity" || fn.Meta["name"] != "Kitchen humidity" || fn.Meta["unit"] != "%" {
		t.Errorf("after edit: %+v", fn)
	}
	if !strings.Contains(r.stderr, "Updated function") {
		t.Errorf("stderr: %s", r.stderr)
	}

	// Saving without changes cancels.
	seen = nil
	env.editor = editWith(&seen)
	if r := env.mustRun("", "function", "edit", "kitchen"); !strings.Contains(r.stderr, "No changes made") {
		t.Errorf("unchanged edit: %s", r.stderr)
	}

	// Giving up after an error reports the error.
	env.editor = editWith(&seen, func(s string) string { return strings.Replace(s, "type: humidity", "type: invalid", 1) })
	if r := env.run("", "function", "edit", "kitchen"); r.code != ExitError || !strings.Contains(r.stderr, "invalid type") {
		t.Errorf("abandoned edit: exit %d, stderr %s", r.code, r.stderr)
	}
}

func TestInstallations(t *testing.T) {
	srv, env := setup(t)

	r := env.mustRun("", "installation", "list")
	if !strings.Contains(r.stdout, "*         10   Main Street 1") {
		t.Errorf("installation list does not mark the current one:\n%s", r.stdout)
	}
	if r := env.mustRun("", "inst", "get", "-q"); r.stdout != "10\n" {
		t.Errorf("get without argument = %q", r.stdout)
	}
	if r := env.mustRun("", "inst", "get", "summer", "-q"); r.stdout != "12\n" {
		t.Errorf("get summer = %q", r.stdout)
	}

	// Meta works on the current installation, or the one given with -i.
	env.mustRun("", "installation", "meta", "set", "customer=ACME")
	env.mustRun("", "installation", "meta", "set", "-i", "summer", "customer=Other")
	if r := env.mustRun("", "installation", "meta", "get", "customer"); r.stdout != "ACME\n" {
		t.Errorf("meta get customer = %q", r.stdout)
	}
	if r := env.mustRun("", "inst", "ls", "--meta", "customer=AC*", "-q"); r.stdout != "10\n" {
		t.Errorf("list --meta = %q", r.stdout)
	}

	// Create; the organization defaults to the user's only one.
	id := createID(t, env, "", "installation", "create", "--name", "New site", "--meta", "customer=New")
	srv.mu.Lock()
	created := *srv.installations[id]
	srv.mu.Unlock()
	if created.OrganizationID != 2 || created.Meta["customer"] != "New" || created.Users == nil {
		t.Errorf("created %+v", created)
	}

	var seen []string
	env.editor = editWith(&seen, func(s string) string { return strings.Replace(s, "name: New site", "name: Renamed site", 1) })
	env.mustRun("", "installation", "edit", "new site")
	srv.mu.Lock()
	name := srv.installations[id].Name
	srv.mu.Unlock()
	if name != "Renamed site" {
		t.Errorf("name after edit = %q", name)
	}

	// Deleting the current installation also unselects it.
	env.mustRun("", "use", "renamed")
	if r := env.run("", "installation", "delete", "renamed", "--yes"); r.code != ExitNotFound {
		t.Errorf("delete by partial name: exit %d", r.code)
	}
	env.mustRun("", "installation", "delete", "Renamed site", "--yes")
	if inst := env.config().Profile(srv.host()).Installation; inst != nil {
		t.Errorf("deleted installation still selected: %+v", inst)
	}
}
