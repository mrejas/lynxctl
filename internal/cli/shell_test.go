// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/term"
)

func TestSplitWords(t *testing.T) {
	tests := []struct {
		line string
		want []string
		open bool
	}{
		{"", nil, false},
		{"  get  kitchen ", []string{"get", "kitchen"}, false},
		{`use "main street"`, []string{"use", "main street"}, false},
		{`meta set x 'unit=°C' # comment`, []string{"meta", "set", "x", "unit=°C"}, false},
		{`a#b "q\"uote" 'back\slash'`, []string{"a#b", `q"uote`, `back\slash`}, false},
		{`file upload C:\tmp\x.png`, []string{"file", "upload", `C:\tmp\x.png`}, false},
		{`hall\ lamp`, []string{"hall lamp"}, false},
		{`use "main st`, []string{"use", "main st"}, true},
		{"# only a comment", nil, false},
	}
	for _, tt := range tests {
		words, open := splitWords(tt.line)
		if got := wordTexts(words); !reflect.DeepEqual(got, tt.want) && (len(got) != 0 || len(tt.want) != 0) || open != tt.open {
			t.Errorf("splitWords(%q) = %q, %v; want %q, %v", tt.line, got, open, tt.want, tt.open)
		}
	}
	words, _ := splitWords(`get "Hall lamp" å`)
	if words[1].start != 4 || words[2].start != 16 {
		t.Errorf("word offsets = %+v", words)
	}
}

func TestQuoteWord(t *testing.T) {
	for _, s := range []string{"plain", "Hall lamp", `it's "x"`, `back\slash`, "#tag", ""} {
		words, open := splitWords("cmd " + quoteWord(s, false))
		if open || len(words) != 2 || words[1].text != s {
			t.Errorf("quoteWord(%q) does not round-trip: %q", s, quoteWord(s, false))
		}
	}
	if got := quoteWord("Hall l", true); got != "'Hall l" {
		t.Errorf("open quote = %q", got)
	}
}

func TestShellScript(t *testing.T) {
	srv, env := valueSetup(t)

	script := `
# Comments and empty lines are ignored.
use "street 2"
iotopenctl use -q
use "main street 1"
get kitchen -q
function get nothing-like-this
`
	r := env.run(script, "shell")
	if r.stdout != "11\n21.46\n" {
		t.Errorf("stdout = %q", r.stdout)
	}
	if r.code != ExitNotFound || strings.Count(r.stderr, "Error:") != 1 {
		t.Errorf("exit %d (want the last command's), stderr:\n%s", r.code, r.stderr)
	}
	if inst := env.config().Profile(srv.host()).Installation; inst == nil || inst.ID != 10 {
		t.Errorf("installation after the script = %+v", inst)
	}

	// Without -e the shell goes on after an error; with it, it stops.
	if r := env.run("function get nothing\nget kitchen -q\n", "shell"); r.code != ExitOK || r.stdout != "21.46\n" {
		t.Errorf("without -e: exit %d, stdout %q", r.code, r.stdout)
	}
	if r := env.run("function get nothing\nget kitchen -q\n", "shell", "-e"); r.code != ExitNotFound || r.stdout != "" {
		t.Errorf("with -e: exit %d, stdout %q", r.code, r.stdout)
	}

	if r := env.run("get kitchen -q\nexit 7\nget kitchen\n", "shell"); r.code != 7 || r.stdout != "21.46\n" {
		t.Errorf("exit 7: exit %d, stdout %q", r.code, r.stdout)
	}
	if r := env.run("use 'unterminated\n", "shell"); r.code != ExitUsage || !strings.Contains(r.stderr, "unterminated quote") {
		t.Errorf("unterminated quote: exit %d, stderr %s", r.code, r.stderr)
	}
	// Destructive commands still need --yes without a terminal.
	if r := env.run("function delete 'Kitchen temperature'\n", "shell"); r.code != ExitUsage {
		t.Errorf("delete without --yes: exit %d, stderr %s", r.code, r.stderr)
	}
	if r := env.run("shell\n", "shell"); r.code != ExitUsage || !strings.Contains(r.stderr, "already in the shell") {
		t.Errorf("nested shell: exit %d, stderr %s", r.code, r.stderr)
	}
	// Global flags given to the shell apply to every command.
	if r := env.run("get kitchen\nget lamp -o table\n", "-o", "json", "shell"); !strings.HasPrefix(r.stdout, "[") || !strings.Contains(r.stdout, "VALUE") {
		t.Errorf("inherited -o json:\n%s", r.stdout)
	}
}

func TestShellPrompt(t *testing.T) {
	srv, env := setup(t)
	sh := &shell{app: env.app("", io.Discard, io.Discard)}
	if got, want := sh.prompt(), srv.host()+":Main Street 1> "; got != want {
		t.Errorf("prompt = %q, want %q", got, want)
	}
	env.mustRun("", "use", "--clear")
	if got, want := sh.prompt(), srv.host()+"> "; got != want {
		t.Errorf("prompt = %q, want %q", got, want)
	}
	sh.app.installationFlag = "42"
	if got, want := sh.prompt(), srv.host()+":42> "; got != want {
		t.Errorf("prompt with -i = %q, want %q", got, want)
	}
}

func TestShellCompletion(t *testing.T) {
	_, env := valueSetup(t)
	var listed strings.Builder
	tm := term.NewTerminal(struct {
		io.Reader
		io.Writer
	}{strings.NewReader(""), &listed}, "")
	sh := &shell{app: env.app("", io.Discard, io.Discard)}

	tests := []struct {
		line string
		pos  int // -1 for the end of the line
		want string
	}{
		{"fun", -1, "function "},
		{"function g", -1, "function get "},
		{"function get kit", -1, "function get 'Kitchen temperature' "},
		{"function get TEMP", -1, "function get 'Kitchen temperature' "}, // unique part
		{"iotopenctl get hall", -1, "iotopenctl get 'Hall lamp' "},
		{"get 'hall l", -1, "get 'Hall lamp' "},
		{"use main", -1, "use 'Main Street "}, // common prefix, quote left open
		{"get kit and more", 7, "get 'Kitchen temperature'  and more"},
		{"function get kitchen x", -1, ""}, // takes one argument
		{"exit", -1, ""},
	}
	for _, tt := range tests {
		pos := tt.pos
		if pos < 0 {
			pos = len(tt.line)
		}
		got, newPos, ok := sh.complete(tm, tt.line, pos)
		if tt.want == "" {
			if ok {
				t.Errorf("complete(%q) = %q, want nothing", tt.line, got)
			}
			continue
		}
		if !ok || got != tt.want {
			t.Errorf("complete(%q) = %q, %v; want %q", tt.line, got, ok, tt.want)
		}
		if tt.pos < 0 && newPos != len(got) {
			t.Errorf("complete(%q) moved the cursor to %d, want the end", tt.line, newPos)
		}
	}

	// No unique completion and no common prefix: the candidates are listed.
	listed.Reset()
	if _, _, ok := sh.complete(tm, "get ", 4); ok {
		t.Error("ambiguous completion changed the line")
	}
	for _, want := range []string{"Kitchen temperature", "ID 1001, temperature", "Hall lamp"} {
		if !strings.Contains(listed.String(), want) {
			t.Errorf("listing lacks %q:\n%s", want, listed.String())
		}
	}

	// Files are completed where the command takes a file.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "floorplan.png"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	line := "file upload " + filepath.Join(dir, "floor")
	if got, _, ok := sh.complete(tm, line, len(line)); !ok || !strings.HasSuffix(got, "floorplan.png ") {
		t.Errorf("file completion = %q, %v", got, ok)
	}
}

func TestShellHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	h := loadHistory(path)
	for _, line := range []string{"get kitchen", "get kitchen", " secret", "", "use main"} {
		h.Add(line)
		h.commit()
	}
	h.Add("interrupted")
	h.discard()
	if h.Len() != 2 || h.At(0) != "use main" || h.At(1) != "get kitchen" {
		t.Errorf("history in memory = %q", h.lines)
	}
	if h := loadHistory(path); !reflect.DeepEqual(h.lines, []string{"get kitchen", "use main"}) {
		t.Errorf("history on disk = %q", h.lines)
	}

	for i := 0; i < historySize+5; i++ {
		h.Add(strings.Repeat("x", i%7+1) + string(rune('a'+i%26)))
		h.commit()
	}
	if h := loadHistory(path); h.Len() != historySize {
		t.Errorf("history has %d lines, want %d", h.Len(), historySize)
	}
}
