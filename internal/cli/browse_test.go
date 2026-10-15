// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"io"
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
