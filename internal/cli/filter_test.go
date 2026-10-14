// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/mrejas/lynx-cli/internal/mqtt"
)

func TestGlobMatch(t *testing.T) {
	tests := []struct {
		pattern, s string
		want       bool
	}{
		{"Kitchen*", "kitchen temperature", true},
		{"obj/*/12/*", "obj/zwave/12/temperature", true},
		{"*", "", true},
		{"*", "anything/with/slashes", true},
		{"12", "12", true},
		{"12", "120", false},
		{"a.b", "axb", false}, // regexp characters are literal
		{"%.1f*", "%.1f °C", true},
	}
	for _, tt := range tests {
		if got := globMatch(tt.pattern, tt.s); got != tt.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", tt.pattern, tt.s, got, tt.want)
		}
	}
}

func TestMetaPairs(t *testing.T) {
	got, err := metaPairs([]string{"pinned", "name=Kitchen*", "empty="})
	if err != nil || len(got) != 3 || got[0] != [2]string{"pinned", "*"} || got[1] != [2]string{"name", "Kitchen*"} || got[2] != [2]string{"empty", ""} {
		t.Errorf("metaPairs = %v, %v", got, err)
	}
	if _, err := metaPairs([]string{" "}); exitCode(err) != ExitUsage {
		t.Errorf("empty filter: %v", err)
	}
}

func TestSelectFunctionsByMeta(t *testing.T) {
	srv, env := valueSetup(t)

	// A key alone means "has the key".
	if r := env.mustRun("", "function", "list", "--meta", "topic_write", "-q"); strings.Count(r.stdout, "\n") != 1 {
		t.Errorf("list --meta topic_write -q = %q", r.stdout)
	}

	if r := env.mustRun("", "get", "--meta", "format", "-q"); r.stdout != "21.46\n" {
		t.Errorf("get --meta format -q = %q", r.stdout)
	}
	if r := env.mustRun("", "get", "--type", "switch", "-q"); r.stdout != "1\n" {
		t.Errorf("get --type switch -q = %q", r.stdout)
	}
	if r := env.mustRun("", "get", "--meta", "topic_read=obj/zwave/*", "-q"); r.stdout != "21.46\n1\n" {
		t.Errorf("get --meta topic_read=obj/zwave/* -q = %q", r.stdout)
	}
	// Named functions and selected ones are combined without duplicates.
	if r := env.mustRun("", "get", "kitchen", "--meta", "topic_read=obj/zwave/*", "-q"); r.stdout != "21.46\n1\n" {
		t.Errorf("get kitchen --meta ... -q = %q", r.stdout)
	}
	if r := env.run("", "get", "--meta", "nosuchkey"); r.code != ExitNotFound {
		t.Errorf("get with a filter matching nothing: exit %d, stderr %s", r.code, r.stderr)
	}

	env.mustRun("", "log", "--type", "temp*")
	q, _ := url.ParseQuery(srv.lastLog)
	if strings.Join(q["topics"], ",") != "1010/obj/zwave/12/temperature" {
		t.Errorf("log --type temp* asked for topics %v", q["topics"])
	}

	var topics []string
	env.subscribe = func(ctx context.Context, _ mqtt.Options, t []string, _ func(mqtt.Message)) error {
		topics = t
		return nil
	}
	env.mustRun("", "watch", "--meta", "topic_write")
	if strings.Join(topics, ",") != "1010/obj/zwave/13/switch" {
		t.Errorf("watch --meta topic_write subscribed to %v", topics)
	}
}

func TestOrganizationMetaFilter(t *testing.T) {
	_, env := setup(t)
	env.mustRun("", "org", "meta", "set", "stockholm south", "region=south")
	env.mustRun("", "org", "meta", "set", "göteborg", "region=west")

	if r := env.mustRun("", "org", "list", "--meta", "region=SOUTH", "-q"); r.stdout != "5\n" {
		t.Errorf("org list --meta region=SOUTH -q = %q", r.stdout)
	}
	if r := env.mustRun("", "org", "list", "--meta", "region", "-q"); r.stdout != "4\n5\n" {
		t.Errorf("org list --meta region -q = %q", r.stdout)
	}
	if r := env.mustRun("", "org", "list", "--meta", "region=*st", "-q"); r.stdout != "4\n" {
		t.Errorf("org list --meta region=*st -q = %q", r.stdout)
	}
}
