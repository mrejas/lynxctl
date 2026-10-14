// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"strings"
	"testing"
	"time"
)

func TestOrganizations(t *testing.T) {
	srv, env := setup(t)
	const coll = "/api/v2/organization"

	r := env.mustRun("", "organization", "tree")
	want := "ACME (ID 2)\n├── ACME Stockholm (ID 3)\n│   └── Stockholm South (ID 5)\n└── ACME Göteborg (ID 4)\n"
	if r.stdout != want {
		t.Errorf("tree:\n%s\nwant:\n%s", r.stdout, want)
	}
	if r := env.mustRun("", "org", "tree", "-q"); r.stdout != "2\n3\n4\n5\n" {
		t.Errorf("tree -q = %q", r.stdout)
	}

	// The parent defaults to the user's only organization.
	id := createID(t, env, "", "org", "create", "--name", "ACME Malmö", "--email", "malmo@acme.example")
	if o := srv.doc(coll, id); o["parent"] != 2.0 || o["email"] != "malmo@acme.example" {
		t.Errorf("created %v", o)
	}
	id = createID(t, env, "", "org", "create", "--name", "Malmö North", "--parent", "malmö")
	if o := srv.doc(coll, id); o["parent"] != float64(id-1) {
		t.Errorf("created with --parent: %v", o)
	}

	env.mustRun("", "org", "meta", "set", "malmö north", "region=south")
	if r := env.mustRun("", "org", "meta", "get", "malmö north", "region"); r.stdout != "south\n" {
		t.Errorf("meta get = %q", r.stdout)
	}

	if r := env.run("", "org", "delete", "north", "--yes"); r.code != ExitNotFound {
		t.Errorf("delete by partial name: exit %d", r.code)
	}
	env.mustRun("", "org", "delete", "Malmö North", "--yes", "--force")
	if srv.doc(coll, id) != nil || srv.lastQuery != "force=true" {
		t.Errorf("delete --force: doc %v, query %q", srv.doc(coll, id), srv.lastQuery)
	}

	env.mustRun("", "org", "force-password-reset", "ACME Stockholm", "--yes")
	if len(srv.passwordResets) != 1 || srv.passwordResets[0] != 3 {
		t.Errorf("password resets = %v", srv.passwordResets)
	}
	if r := env.run("", "org", "force-password-reset", "ACME Stockholm"); r.code != ExitUsage {
		t.Errorf("force-password-reset without --yes: exit %d", r.code)
	}
}

func TestUsers(t *testing.T) {
	srv, env := setup(t)

	if r := env.mustRun("", "user", "get", "-q"); r.stdout != "7\n" {
		t.Errorf("user get (me) -q = %q", r.stdout)
	}
	r := env.mustRun("", "user", "list")
	if !strings.Contains(r.stdout, "Bob Smith") || !strings.Contains(r.stdout, "jane@example.com") {
		t.Errorf("user list:\n%s", r.stdout)
	}
	if r := env.mustRun("", "user", "get", "bob", "-q"); r.stdout != "8\n" {
		t.Errorf("user get bob -q = %q", r.stdout)
	}

	id := createID(t, env, "hunter2\n", "user", "create", "--email", "eve@example.com", "--first-name", "Eve",
		"--last-name", "Adams", "--role", "user", "--password-stdin")
	u := srv.doc("/api/v2/user", id)
	orgs, _ := u["organisations"].([]any)
	if u["role"] != 2.0 || len(orgs) != 1 || orgs[0] != 2.0 || u["password"] != "hunter2" || u["address"] == nil {
		t.Errorf("created user = %v", u)
	}
	if r := env.run("", "user", "create", "--email", "x@example.com"); r.code != ExitUsage {
		t.Errorf("create without names: exit %d", r.code)
	}

	env.mustRun("", "user", "meta", "set", "eve", "team=blue")
	if r := env.mustRun("", "user", "meta", "get", "eve", "team"); r.stdout != "blue\n" {
		t.Errorf("user meta get = %q", r.stdout)
	}

	// Own password: the current one is required; others' only the new one.
	env.mustRun("secret\nnew-pass\n", "user", "password", "--password-stdin")
	env.mustRun("bobs-new\n", "user", "password", "bob@example.com", "--password-stdin")
	if strings.Join(srv.passwords, ",") != "7:new-pass,8:bobs-new" {
		t.Errorf("passwords = %v", srv.passwords)
	}
	if r := env.run("wrong\nx\n", "user", "password", "--password-stdin"); r.code != ExitError || !strings.Contains(r.stderr, "wrong current password") {
		t.Errorf("wrong current password: exit %d, stderr %s", r.code, r.stderr)
	}
	if r := env.run("", "user", "password"); r.code != ExitUsage {
		t.Errorf("password without terminal: exit %d", r.code)
	}

	env.mustRun("", "user", "delete", "eve@example.com", "--yes")
	if srv.doc("/api/v2/user", id) != nil {
		t.Error("user not deleted")
	}
}

func TestTokens(t *testing.T) {
	srv, env := setup(t)

	r := env.mustRun("", "user", "token", "create", "--name", "ci", "--expires", "90d", "--permission", "installation/list/all")
	if r.stdout != "secret-token-value\n" || !strings.Contains(r.stderr, "cannot be shown again") {
		t.Errorf("token create: stdout %q, stderr %q", r.stdout, r.stderr)
	}
	var created int64
	for id, tok := range srv.tokens {
		if tok["name"] == "ci" {
			created = id
			expires := time.Unix(num(tok, "expires"), 0)
			if d := time.Until(expires); d < 89*24*time.Hour || d > 91*24*time.Hour {
				t.Errorf("expires in %v, want about 90 days", d)
			}
		}
	}
	if created == 0 {
		t.Fatalf("token not stored: %v", srv.tokens)
	}

	r = env.mustRun("", "user", "token", "list")
	if !strings.Contains(r.stdout, "laptop") || !strings.Contains(r.stdout, "never") || !strings.Contains(r.stdout, "ci") {
		t.Errorf("token list:\n%s", r.stdout)
	}
	if r := env.mustRun("", "user", "token", "list", "--user", "bob", "-q"); r.stdout == "" {
		t.Error("token list --user bob printed nothing")
	}

	// The token in use gets an extra warning; without a terminal --yes is needed.
	if r := env.run("", "user", "token", "delete", "laptop"); r.code != ExitUsage || !strings.Contains(r.stderr, "you will be logged out") {
		t.Errorf("deleting the current token: exit %d, stderr %s", r.code, r.stderr)
	}
	env.mustRun("", "user", "token", "revoke", "ci", "--yes")
	if srv.tokens[created] != nil {
		t.Error("token not deleted")
	}

	for _, args := range [][]string{
		{"user", "token", "create"},
		{"user", "token", "create", "--name", "x", "--expires", "-1h"},
	} {
		if r := env.run("", args...); r.code != ExitUsage {
			t.Errorf("%v: exit %d, stderr %s", args, r.code, r.stderr)
		}
	}
}

func TestRolesAndPermissions(t *testing.T) {
	srv, env := setup(t)

	r := env.mustRun("", "role", "list")
	if !strings.Contains(r.stdout, "Admin") || !strings.Contains(r.stdout, "1 allowed") {
		t.Errorf("role list:\n%s", r.stdout)
	}
	id := createID(t, env, "", "role", "create", "--name", "Integration", "--priority", "5",
		"--allow", "installation/list/all", "--deny", "functionx/+/delete")
	role := srv.doc("/api/v2/role", id)
	perms, _ := role["permissions"].(map[string]any)
	if role["priority"] != 5.0 || perms["installation/list/all"] != true || perms["functionx/+/delete"] != false {
		t.Errorf("created role = %v", role)
	}
	r = env.mustRun("", "role", "get", "integration")
	if !strings.Contains(r.stdout, "installation/list/all:") {
		t.Errorf("role get:\n%s", r.stdout)
	}

	r = env.mustRun("", "permission", "list")
	if !strings.Contains(r.stdout, "functionx/zwave/add") || !strings.Contains(r.stdout, "no") {
		t.Errorf("permission list:\n%s", r.stdout)
	}
	if r := env.mustRun("", "permissions", "list", "--allowed", "-q"); r.stdout != "functionx/+/add\ninstallation/list/all\n" {
		t.Errorf("permission list --allowed -q = %q", r.stdout)
	}
}
