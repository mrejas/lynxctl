// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"io"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/mrejas/lynx-cli/internal/mqtt"
)

func browseSetup(t *testing.T) *testEnv {
	t.Helper()
	_, env := valueSetup(t)
	dev := createID(t, env, "", "device", "create", "--type", "zwave", "--name", "Hall sensor")
	env.mustRun("", "function", "meta", "set", "hall lamp", "device_id="+strconv.FormatInt(dev, 10))
	return env
}

func TestBrowse(t *testing.T) {
	env := browseSetup(t)

	steps := []struct{ line, want string }{
		{"pwd", "/ACME/Main Street 1\n"},
		{"ls", "Hall sensor/\nKitchen temperature/\nNo topics/\n"},
		{"ls hall", "Hall lamp/\n"},
		{"cd /", ""},
		{"ls", "ACME/\n"},
		{"ls acme", "ACME Göteborg/\nACME Stockholm/\nMain Street 1/\nMain Street 2/\nSummer house/\n"},
		{"cd acme/stockholm/south", ""},
		{"pwd", "/ACME/ACME Stockholm/Stockholm South\n"},
		{"cd ../../summer", ""},
		{"pwd", "/ACME/Summer house\n"},
		{"cd", ""},
		{"cd 'hall sensor'/lamp", ""},
		{"ls", "topic_read\ntopic_write\n"},
		{"cat topic_read -q", "1\n"},
		{"cd ~/kitchen", ""},
		{"pwd", "/ACME/Main Street 1/Kitchen temperature\n"},
		// Other commands run in the installation browsed.
		{"cd /acme/summer", ""},
		{"function list -q", ""},
		{"cd ~", ""},
		{"get kitchen -q", "21.46\n"},
	}
	var script strings.Builder
	var want strings.Builder
	for _, s := range steps {
		script.WriteString(s.line + "\n")
		want.WriteString(s.want)
	}
	r := env.run(script.String(), "browse", "-e")
	if r.code != ExitOK || r.stdout != want.String() {
		t.Errorf("exit %d\nstdout:\n%s\nwant:\n%s\nstderr:\n%s", r.code, r.stdout, want.String(), r.stderr)
	}

	r = env.mustRun("ls -l\ncat kitchen -o json\n", "browse")
	for _, want := range []string{"KIND", "device", "Hall sensor/", "function", "temperature", `"name": "Kitchen temperature"`} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("ls -l and cat lack %q:\n%s", want, r.stdout)
		}
	}
	r = env.mustRun("cat\ncat /acme\n", "browse")
	for _, want := range []string{"Main Street 1", "ACME"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("cat lacks %q:\n%s", want, r.stdout)
		}
	}

	for _, tt := range []struct {
		line string
		code int
		want string
	}{
		{"cd nowhere", ExitNotFound, `no item matches "nowhere" (in /ACME/Main Street 1)`},
		{"cd /acme/main", ExitNotFound, "matches 2 items"},
		{"cd kitchen/topic_read", ExitError, "is not a directory"},
		{"ls -x", ExitUsage, "unknown flag"},
	} {
		r := env.run(tt.line+"\n", "browse")
		if r.code != tt.code || !strings.Contains(r.stderr, tt.want) {
			t.Errorf("%s: exit %d, stderr %s", tt.line, r.code, r.stderr)
		}
	}
}

func TestBrowseValues(t *testing.T) {
	env := browseSetup(t)

	steps := []struct{ line, want string }{
		{"cd kitchen", ""},
		{"cat topic_read -q", "20.04\n21.46\n"},
		{"tail -n 1 topic_read -q", "21.46\n"},
		{"tail -n1 -q", "21.46\n"},
		{"head -n 1 topic_read -q", "20.04\n"},
		{"head --since 1h -q", "21.46\n"},
		{"tail -q ~/hall", "1\n"},
	}
	var script, want strings.Builder
	for _, s := range steps {
		script.WriteString(s.line + "\n")
		want.WriteString(s.want)
	}
	r := env.run(script.String(), "browse", "-e")
	if r.code != ExitOK || r.stdout != want.String() {
		t.Errorf("exit %d\nstdout:\n%s\nwant:\n%s\nstderr:\n%s", r.code, r.stdout, want.String(), r.stderr)
	}

	// tail -f prints the latest values and then follows new ones.
	env.subscribe = func(ctx context.Context, opts mqtt.Options, topics []string, handle func(mqtt.Message)) error {
		if len(topics) != 1 || topics[0] != "1010/obj/zwave/12/temperature" {
			t.Errorf("tail -f subscribed to %v", topics)
		}
		handle(mqtt.Message{Topic: "1010/obj/zwave/12/temperature", Payload: []byte(`{"value":22.5}`)})
		return nil
	}
	r = env.run("tail -f -n 1 -q kitchen/topic_read\n", "browse")
	if r.code != ExitOK || r.stdout != "21.46\n22.5\n" {
		t.Errorf("tail -f: exit %d, stdout %q, stderr %s", r.code, r.stdout, r.stderr)
	}

	for _, tt := range []struct {
		line string
		code int
		want string
	}{
		{"tail -n x", ExitUsage, "invalid number of lines"},
		{"tail -n", ExitUsage, "usage: tail"},
		{"head /", ExitUsage, "/ has no values"},
		{"cat /", ExitUsage, "/ is not an object"},
		{"tail a b", ExitUsage, "takes one path"},
	} {
		r := env.run(tt.line+"\n", "browse")
		if r.code != tt.code || !strings.Contains(r.stderr, tt.want) {
			t.Errorf("%s: exit %d, stderr %s", tt.line, r.code, r.stderr)
		}
	}
}

func TestBrowseCompletion(t *testing.T) {
	env := browseSetup(t)
	a := env.app("", io.Discard, io.Discard)
	sh := &shell{app: a}
	b := &browser{sh: sh}
	sh.mode = b
	b.cwd = b.home(t.Context())

	// | marks the cursor. Directories are quoted as a whole with the cursor
	// before the closing quote, so that Tab can go on inside it.
	for _, tt := range []struct{ line, want string }{
		{"cd ha", "cd 'Hall sensor/|'"},
		{"cd 'Hall sensor/la|'", "cd 'Hall sensor/Hall lamp/|'"},
		{"cd /acme/stock", "cd '/acme/ACME Stockholm/|'"},
		{"cd /acme/göte", "cd '/acme/ACME Göteborg/|'"},
		{"ls hall\\ sensor/", "ls 'hall sensor/Hall lamp/|'"},
		{"cat kitchen/topic", "cat kitchen/topic_read |"},
		{"cat 'Hall sensor/Hall lamp/topic_r|'", "cat 'Hall sensor/Hall lamp/topic_read' |"},
		{"pw", "pwd |"},
		{"find -type d", "find -type dev |"},
		{"find -ma", "find -maxdepth |"},
		{"find ha", "find 'Hall sensor/|'"},
		{"get kit", "get 'Kitchen temperature' |"},
	} {
		checkCompletion(t, sh, nil, tt.line, tt.want)
	}
}

func TestBrowseColors(t *testing.T) {
	b := &browser{color: true}
	for _, tt := range []struct {
		n    node
		want string
	}{
		{node{kind: orgNode, name: "ACME"}, "\x1b[1;35mACME/\x1b[0m"},
		{node{kind: installationNode, name: "Main"}, "\x1b[1;34mMain/\x1b[0m"},
		{node{kind: deviceNode, name: "Hall sensor"}, "\x1b[33mHall sensor/\x1b[0m"},
		{node{kind: functionNode, id: 7}, "\x1b[32m7/\x1b[0m"},
		{node{kind: metaNode, name: "topic_read"}, "topic_read"},
	} {
		if got := b.listName(tt.n); got != tt.want {
			t.Errorf("listName(%+v) = %q, want %q", tt.n, got, tt.want)
		}
	}
	b.color = false
	if got := b.listName(node{kind: orgNode, name: "ACME"}); got != "ACME/" {
		t.Errorf("without colors: %q", got)
	}
}

func TestBrowsePrompt(t *testing.T) {
	sh := &shell{app: &App{Name: "lynxctl"}, color: true}
	b := &browser{sh: sh}
	sh.mode = b
	b.cwd = []node{{kind: rootNode}, {kind: orgNode, name: "ACME"}, {kind: installationNode, name: "Main"}}
	t.Setenv("IOTOPEN_CONFIG_DIR", t.TempDir())
	if got, want := b.prompt(), "/\x1b[1;35mACME\x1b[0m/\x1b[1;34mMain\x1b[0m> "; got != want {
		t.Errorf("prompt = %q, want %q", got, want)
	}
	b.cwd = append(b.cwd, node{kind: deviceNode, name: "A rather long device name"}, node{kind: functionNode, name: "Temperature"})
	sh.color = false
	if got, want := b.prompt(), ".../A rather long device name/Temperature> "; got != want {
		t.Errorf("long prompt = %q, want %q", got, want)
	}
	b.cwd = b.cwd[:1]
	if got := b.prompt(); got != "/> " {
		t.Errorf("root prompt = %q", got)
	}
}

func TestBrowseRemove(t *testing.T) {
	env := browseSetup(t)

	for _, tt := range []struct {
		line string
		code int
		want string
	}{
		{"rm no", ExitNotFound, `no item with ID or exact name "no"`},
		{"rm 'Hall sensor'", ExitUsage, "is not empty (1 item). Use rm -r"},
		{"rm 'No topics'", ExitUsage, "--yes"}, // no terminal to ask on
		{"rm -x kitchen", ExitUsage, "unknown flag -x"},
		{"rm", ExitUsage, "usage: rm"},
		{"rm /", ExitUsage, "/ cannot be removed"},
	} {
		r := env.run(tt.line+"\n", "browse")
		if r.code != tt.code || !strings.Contains(r.stderr, tt.want) {
			t.Errorf("%s: exit %d, stderr %s", tt.line, r.code, r.stderr)
		}
	}

	steps := []struct{ line, want string }{
		{"rm -f 'No topics'", ""},
		{"ls", "Hall sensor/\nKitchen temperature/\n"},
		{"rm 'Kitchen temperature/topic_read'", ""}, // meta keys need no -f
		{"ls 'Kitchen temperature'", ""},
		{"cd 'Hall sensor'", ""},
		{"rm -rf .", ""}, // the device and its function; leaves the directory
		{"pwd", "/ACME/Main Street 1\n"},
		{"ls", "Kitchen temperature/\n"},
	}
	var script, want strings.Builder
	for _, s := range steps {
		script.WriteString(s.line + "\n")
		want.WriteString(s.want)
	}
	r := env.run(script.String(), "browse", "-e")
	if r.code != ExitOK || r.stdout != want.String() {
		t.Errorf("exit %d\nstdout:\n%s\nwant:\n%s\nstderr:\n%s", r.code, r.stdout, want.String(), r.stderr)
	}
	if r := env.mustRun("", "function", "list", "-q"); r.stdout != "1001\n" {
		t.Errorf("functions left: %q", r.stdout)
	}
	if r := env.mustRun("", "device", "list", "-q"); r.stdout != "" {
		t.Errorf("devices left: %q", r.stdout)
	}
}

func TestBrowseEdit(t *testing.T) {
	env := browseSetup(t)
	var seen []string
	env.editor = editWith(&seen,
		func(s string) string { return strings.Replace(s, "name: Kitchen temperature", "name: Kitchen", 1) },
		func(s string) string { return strings.Replace(s, "type: zwave", "type: zigbee", 1) })

	env.mustRun("cd kitchen\nvi topic_read\nedit ~/hall\n", "browse", "-e")
	if len(seen) != 2 || !strings.Contains(seen[0], "topic_read: obj/zwave/12/temperature") || !strings.Contains(seen[1], "type: zwave") {
		t.Fatalf("editor saw:\n%q", seen)
	}
	if r := env.mustRun("", "function", "get", "1001", "-o", "json"); !strings.Contains(r.stdout, `"name": "Kitchen"`) {
		t.Errorf("function after edit:\n%s", r.stdout)
	}
	if r := env.mustRun("", "device", "list"); !strings.Contains(r.stdout, "zigbee") {
		t.Errorf("device after edit:\n%s", r.stdout)
	}
	if r := env.run("edit /\n", "browse"); r.code != ExitUsage || !strings.Contains(r.stderr, "cannot be edited") {
		t.Errorf("edit /: exit %d, stderr %s", r.code, r.stderr)
	}
}

func TestEditorOverride(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses the true and false commands")
	}
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "true")
	a := &App{editor: []string{"false"}}
	// "false" fails, so the override is what ran.
	if err := a.runEditor(t.TempDir() + "/x"); err == nil || !strings.Contains(err.Error(), `"false"`) {
		t.Errorf("runEditor with override: %v", err)
	}
	a.editor = nil
	if err := a.runEditor(t.TempDir() + "/x"); err != nil {
		t.Errorf("runEditor with $EDITOR=true: %v", err)
	}
}

func TestBrowseFind(t *testing.T) {
	env := browseSetup(t)

	for _, tt := range []struct{ line, want string }{
		{"find", `.
./Hall sensor
./Hall sensor/Hall lamp
./Hall sensor/Hall lamp/topic_read
./Hall sensor/Hall lamp/topic_write
./Kitchen temperature
./Kitchen temperature/topic_read
./No topics
`},
		{"find / -type fn", `/ACME/Main Street 1/Hall sensor/Hall lamp
/ACME/Main Street 1/Kitchen temperature
/ACME/Main Street 1/No topics
`},
		{"find / -type inst -name '*street*'", "/ACME/Main Street 1\n/ACME/Main Street 2\n"},
		{"find -type dev,fn -meta topic_read", "./Hall sensor/Hall lamp\n./Kitchen temperature\n"},
		{"find / -maxdepth 1", "/\n/ACME\n"},
		{"find /acme/stock 'hall sensor' -type org,dev", "/acme/stock\n/acme/stock/Stockholm South\nhall sensor\n"},
		{"find -type meta -name topic_w*", "./Hall sensor/Hall lamp/topic_write\n"},
	} {
		r := env.run(tt.line+"\n", "browse")
		if r.code != ExitOK || r.stdout != tt.want {
			t.Errorf("%s: exit %d\nstdout:\n%s\nwant:\n%s\nstderr: %s", tt.line, r.code, r.stdout, tt.want, r.stderr)
		}
	}

	for _, tt := range []struct {
		line string
		code int
		want string
	}{
		{"find -type x", ExitUsage, `invalid -type "x"`},
		{"find -name", ExitUsage, "-name needs a value"},
		{"find -depth 2", ExitUsage, "unknown flag -depth"},
		{"find -maxdepth -1", ExitUsage, "invalid -maxdepth"},
		{"find nowhere", ExitNotFound, `no item matches "nowhere"`},
	} {
		r := env.run(tt.line+"\n", "browse")
		if r.code != tt.code || !strings.Contains(r.stderr, tt.want) {
			t.Errorf("%s: exit %d, stderr %s", tt.line, r.code, r.stderr)
		}
	}
}
