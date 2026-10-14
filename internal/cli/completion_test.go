// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// complete runs cobra's hidden completion command and returns the
// candidates and the directive.
func complete(t *testing.T, env *testEnv, args ...string) ([]string, cobra.ShellCompDirective) {
	t.Helper()
	r := env.mustRun("", append([]string{cobra.ShellCompRequestCmd}, args...)...)
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	last := lines[len(lines)-1]
	var directive int
	if _, err := fmt.Sscanf(last, ":%d", &directive); err != nil {
		t.Fatalf("no directive in completion output:\n%s", r.stdout)
	}
	return lines[:len(lines)-1], cobra.ShellCompDirective(directive)
}

func TestCompletion(t *testing.T) {
	_, env := valueSetup(t)
	createID(t, env, "", "device", "create", "--type", "zwave", "--name", "Hall sensor")

	got, dir := complete(t, env, "function", "get", "")
	want := []string{"Kitchen temperature\tID 1001, temperature", "Hall lamp\tID 1002, switch", "No topics\tID 1003, virtual"}
	if strings.Join(got, "|") != strings.Join(want, "|") || dir != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("function get completion = %q, %v", got, dir)
	}
	if got, _ := complete(t, env, "function", "get", "kitchen", ""); len(got) != 0 {
		t.Errorf("function get takes one argument, but got %q", got)
	}
	// Commands taking several functions leave out those already given.
	if got, _ := complete(t, env, "get", "Hall lamp", ""); len(got) != 2 || strings.Contains(strings.Join(got, "|"), "Hall lamp") {
		t.Errorf("get completion after one function = %q", got)
	}
	if got, _ := complete(t, env, "device", "functions", ""); len(got) != 1 || !strings.HasPrefix(got[0], "Hall sensor\t") {
		t.Errorf("device completion = %q", got)
	}
	if got, _ := complete(t, env, "function", "list", "--type", ""); strings.Join(got, ",") != "switch,temperature,virtual" {
		t.Errorf("--type completion = %q", got)
	}
	if got, _ := complete(t, env, "use", ""); len(got) != 3 || !strings.HasPrefix(got[0], "Main Street 1\t") {
		t.Errorf("installation completion = %q", got)
	}

	// Meta keys: as "key=" without a trailing space for set, plain for get.
	got, dir = complete(t, env, "function", "meta", "set", "kitchen", "")
	if len(got) != 3 || !strings.HasPrefix(got[0], "format=\t") || dir != cobra.ShellCompDirectiveNoFileComp|cobra.ShellCompDirectiveNoSpace {
		t.Errorf("meta set completion = %q, %v", got, dir)
	}
	if got, _ := complete(t, env, "function", "meta", "unset", "kitchen", "format", ""); len(got) != 2 {
		t.Errorf("meta unset completion after one key = %q", got)
	}
	if got, _ := complete(t, env, "function", "meta", "get", "kitchen", ""); len(got) != 3 || !strings.HasPrefix(got[1], "name\tKitchen temperature") {
		t.Errorf("meta get completion = %q", got)
	}

	// Lists are cached: a function created now does not show up until the
	// cache expires, and the cache is a file in the cache directory.
	createID(t, env, "", "function", "create", "--type", "switch", "--name", "New lamp")
	if got, _ := complete(t, env, "function", "get", ""); len(got) != 3 {
		t.Errorf("completion was not cached: %q", got)
	}
	files, _ := filepath.Glob(filepath.Join(env.dir, "cache", "completion-*.json"))
	if len(files) == 0 {
		t.Error("no cache files written")
	}
	for _, f := range files {
		os.Remove(f)
	}
	if got, _ := complete(t, env, "function", "get", ""); len(got) != 4 {
		t.Errorf("completion after clearing the cache = %q", got)
	}

	// Without a login, completion fails quietly instead of printing errors.
	env2 := newTestEnv(t)
	if got, dir := complete(t, env2, "function", "get", ""); len(got) != 0 || dir != cobra.ShellCompDirectiveError {
		t.Errorf("completion without login = %q, %v", got, dir)
	}
}
