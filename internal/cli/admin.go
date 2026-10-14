// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mrejas/lynx-cli/internal/api"
	"github.com/mrejas/lynx-cli/internal/output"
	"github.com/mrejas/lynx-cli/internal/resolve"
)

const (
	organizationPath = "/api/v2/organization"
	userPath         = "/api/v2/user"
	rolePath         = "/api/v2/role"
)

func constPath(path string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return path, nil }
}

// metaTargetFor gives a resource the meta subcommands, with the object as
// first argument.
func (a *App) metaTargetFor(r *resource, basePath string) metaTarget {
	return metaTarget{
		noun:     r.noun,
		withRef:  true,
		complete: a.completeResource(r),
		load: func(ctx context.Context, ref string) (*metaObject, error) {
			o, _, _, err := a.findResource(ctx, r, ref, false)
			if err != nil {
				return nil, err
			}
			return &metaObject{
				Path:      fmt.Sprintf("%s/%d", basePath, num(o, "id")),
				Label:     r.label(o),
				Meta:      toMeta(o["meta"]),
				Protected: toMeta(o["protected_meta"]),
			}, nil
		},
	}
}

func toMeta(v any) api.Meta {
	m, _ := v.(map[string]any)
	out := api.Meta{}
	for k, x := range m {
		out[k] = str(x)
	}
	return out
}

// --- Organizations ---

func (a *App) organizationResource() *resource {
	r := &resource{
		noun:       "organization",
		collection: constPath(organizationPath),
		nameKey:    "name",
		columns: []column{
			field("id", "id"),
			field("name", "name"),
			field("parent", "parent"),
			field("email", "email"),
			wideField("phone", "phone"),
			{header: "children", wide: true, value: func(o api.JSONObject) string {
				c, _ := o["children"].([]any)
				return strconv.Itoa(len(c))
			}},
		},
		readOnly: []string{"id", "children"},
		required: []string{"name"},
		// The API cannot filter organizations by meta, so it is done here.
		localFilter: func(cmd *cobra.Command) func(api.JSONObject) (bool, error) {
			var meta []string
			cmd.Flags().StringArrayVar(&meta, "meta", nil, metaFilterHelp)
			return func(o api.JSONObject) (bool, error) {
				pairs, err := metaPairs(meta)
				if err != nil {
					return false, err
				}
				return metaMatches(pairs, toMeta(o["meta"]), toMeta(o["protected_meta"])), nil
			}
		},
		createExample: fmt.Sprintf(`  %[1]s organization create --name "ACME Stockholm" --parent ACME --email info@acme.example`, a.Name),
	}
	r.createFlags = func(cmd *cobra.Command) func(context.Context, api.JSONObject) error {
		var name, parent, email, phone, notes string
		f := cmd.Flags()
		f.StringVar(&name, "name", "", "name of the organization")
		f.StringVar(&parent, "parent", "", "parent organization, ID or name (default: your organization, if you have only one)")
		f.StringVar(&email, "email", "", "contact email")
		f.StringVar(&phone, "phone", "", "contact phone")
		f.StringVar(&notes, "notes", "", "notes")
		_ = cmd.RegisterFlagCompletionFunc("parent", a.completeResource(r))
		return func(ctx context.Context, o api.JSONObject) error {
			for key, v := range map[string]string{"name": name, "email": email, "phone": phone, "notes": notes} {
				if v != "" {
					o[key] = v
				}
			}
			switch {
			case parent != "":
				p, _, _, err := a.findResource(ctx, r, parent, false)
				if err != nil {
					return err
				}
				o["parent"] = num(p, "id")
			case o["parent"] == nil:
				id, err := a.onlyOrganization(ctx)
				if err != nil {
					return usageErrorf("choose a parent organization with --parent")
				}
				o["parent"] = id
			}
			return nil
		}
	}
	return r
}

func (a *App) newOrganizationCommand() *cobra.Command {
	r := a.organizationResource()
	cmd := groupCommand("organization", "Manage organizations", "organizations", "org", "orgs")
	cmd.Long = `Manage organizations. Organizations form a tree; see it with
"organization tree". They are referred to by ID, name or a unique part of
the name. Deleting requires the ID or the exact name.`
	cmd.AddCommand(
		a.newResourceListCommand(r),
		a.newResourceGetCommand(r),
		a.newResourceCreateCommand(r),
		a.newResourceEditCommand(r),
		a.newOrganizationDeleteCommand(r),
		a.newOrganizationTreeCommand(r),
		a.newOrganizationPasswordResetCommand(r),
		a.newMetaCommand(a.metaTargetFor(r, organizationPath)),
	)
	return cmd
}

func (a *App) newOrganizationDeleteCommand(r *resource) *cobra.Command {
	var yes, force bool
	cmd := &cobra.Command{
		Use:     "delete <organization>",
		Aliases: []string{"rm"},
		Short:   "Delete an organization (ID or exact name required)",
		Long: `Delete an organization. Only an empty organization can be deleted, unless
--force is given: then its installations, devices, functions, files,
notifications and users are deleted first. Organizations with
sub-organizations cannot be deleted.`,
		Args:              usageArgs(cobra.ExactArgs(1)),
		ValidArgsFunction: a.completeResource(r),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			o, s, _, err := a.findResource(ctx, r, args[0], true)
			if err != nil {
				return err
			}
			question := fmt.Sprintf("Delete %s?", r.label(o))
			path := fmt.Sprintf("%s/%d", organizationPath, num(o, "id"))
			if force {
				question = fmt.Sprintf("Delete %s and ALL its installations, devices, functions, files, notifications and users?", r.label(o))
				path += "?force=true"
			}
			if err := a.confirm(question, yes); err != nil {
				return err
			}
			_, err = s.Client.CallJSON(ctx, http.MethodDelete, path, nil)
			if api.IsStatus(err, http.StatusConflict) {
				return fmt.Errorf("%s has sub-organizations; delete them first (%w)", r.label(o), err)
			}
			if err != nil {
				return err
			}
			a.info("Deleted %s.", r.label(o))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	cmd.Flags().BoolVar(&force, "force", false, "also delete everything in the organization")
	return cmd
}

func (a *App) newOrganizationTreeCommand(r *resource) *cobra.Command {
	return &cobra.Command{
		Use:   "tree",
		Short: "Show the organizations as a tree",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			list, _, _, err := a.listResource(cmd.Context(), r, nil)
			if err != nil {
				return err
			}
			if a.outputFlag != string(output.Table) || a.quiet {
				pr, err := a.printer()
				if err != nil {
					return err
				}
				return pr.List(list, r.rows(list))
			}
			ids := map[int64]bool{}
			children := map[int64][]api.JSONObject{}
			for _, o := range list {
				ids[num(o, "id")] = true
			}
			var roots []api.JSONObject
			for _, o := range list {
				if p := num(o, "parent"); p != 0 && ids[p] && p != num(o, "id") {
					children[p] = append(children[p], o)
				} else {
					roots = append(roots, o)
				}
			}
			var walk func(o api.JSONObject, prefix string, last, root bool)
			walk = func(o api.JSONObject, prefix string, last, root bool) {
				branch, next := "├── ", "│   "
				if last {
					branch, next = "└── ", "    "
				}
				if root {
					branch, next = "", ""
				}
				fmt.Fprintf(a.Out, "%s%s%s (ID %d)\n", prefix, branch, str(o["name"]), num(o, "id"))
				kids := children[num(o, "id")]
				for i, c := range kids {
					walk(c, prefix+next, i == len(kids)-1, false)
				}
			}
			for _, o := range roots {
				walk(o, "", true, true)
			}
			return nil
		},
	}
}

func (a *App) newOrganizationPasswordResetCommand(r *resource) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:               "force-password-reset <organization>",
		Short:             "Make all users of an organization and its sub-organizations reset their password",
		Args:              usageArgs(cobra.ExactArgs(1)),
		ValidArgsFunction: a.completeResource(r),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			o, s, _, err := a.findResource(ctx, r, args[0], true)
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Force all users of %s and its sub-organizations to reset their password?", r.label(o)), yes); err != nil {
				return err
			}
			if _, err := s.Client.CallJSON(ctx, http.MethodPost, fmt.Sprintf("%s/%d/force_password_reset", organizationPath, num(o, "id")), nil); err != nil {
				return err
			}
			a.info("All users of %s must now reset their password.", r.label(o))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

// --- Users ---

func fullName(o api.JSONObject) string {
	return strings.TrimSpace(str(o["first_name"]) + " " + str(o["last_name"]))
}

func (a *App) userResource() *resource {
	roles := a.roleResource()
	r := &resource{
		noun:       "user",
		collection: constPath(userPath),
		nameKey:    "email",
		self:       "me",
		defaultRef: "me",
		columns: []column{
			field("id", "id"),
			field("email", "email"),
			{header: "name", value: fullName},
			field("role", "role"),
			field("organizations", "organisations"),
			wideField("mobile", "mobile"),
			wideField("installations", "assigned_installations"),
		},
		readOnly: []string{"id", "assigned_installations"},
		required: []string{"email", "first_name", "last_name", "role", "organisations"},
		createExample: fmt.Sprintf(`  %[1]s user create --email jane@example.com --first-name Jane --last-name Doe --role User
  echo "$PASSWORD" | %[1]s user create --email bot@example.com --first-name Bot --last-name Account \
      --role Integration --org 7 --password-stdin`, a.Name),
		listFlags: func(cmd *cobra.Command) func() (url.Values, error) {
			var org int64
			var meta []string
			cmd.Flags().Int64Var(&org, "org", 0, "only show users of this organization")
			cmd.Flags().StringArrayVar(&meta, "meta", nil, metaFilterHelp)
			return func() (url.Values, error) {
				q, err := metaFilter(meta, "")
				if err != nil {
					return nil, err
				}
				if org != 0 {
					q.Set("organization_id", strconv.FormatInt(org, 10))
				}
				return q, nil
			}
		},
	}
	r.createFlags = func(cmd *cobra.Command) func(context.Context, api.JSONObject) error {
		var email, first, last, role, mobile string
		var orgs []int64
		var passwordStdin, smsLogin bool
		f := cmd.Flags()
		f.StringVar(&email, "email", "", "email address (also the username)")
		f.StringVar(&first, "first-name", "", "first name")
		f.StringVar(&last, "last-name", "", "last name")
		f.StringVar(&role, "role", "", "role, ID or name")
		f.Int64SliceVar(&orgs, "org", nil, "organization ID (repeatable; default: your organization, if you have only one)")
		f.StringVar(&mobile, "mobile", "", "mobile number, for SMS login")
		f.BoolVar(&smsLogin, "sms-login", false, "require a code sent by SMS when logging in")
		f.BoolVar(&passwordStdin, "password-stdin", false, "read the new user's password from stdin")
		_ = cmd.RegisterFlagCompletionFunc("role", a.completeResource(roles))
		return func(ctx context.Context, o api.JSONObject) error {
			for key, v := range map[string]string{"email": email, "first_name": first, "last_name": last, "mobile": mobile} {
				if v != "" {
					o[key] = v
				}
			}
			if smsLogin {
				o["sms_login"] = true
			}
			if role != "" {
				found, _, _, err := a.findResource(ctx, roles, role, false)
				if err != nil {
					return err
				}
				o["role"] = num(found, "id")
			}
			switch {
			case len(orgs) > 0:
				o["organisations"] = orgs
			case o["organisations"] == nil:
				id, err := a.onlyOrganization(ctx)
				if err != nil {
					return usageErrorf("choose the user's organization with --org")
				}
				o["organisations"] = []int64{id}
			}
			if o["address"] == nil {
				o["address"] = map[string]any{"address": "", "city": "", "country": "", "zip": ""}
			}
			if passwordStdin {
				pw, err := a.readLine()
				if err != nil {
					return err
				}
				o["password"] = pw
			}
			return nil
		}
	}
	return r
}

func (a *App) newUserCommand() *cobra.Command {
	r := a.userResource()
	cmd := groupCommand("user", "Manage users and your own account", "users")
	cmd.Long = `Manage users. Users are referred to by ID, email address or a unique part
of it, or "me" for yourself. Deleting requires the ID or the exact address.`
	cmd.AddCommand(append(a.resourceCommands(r),
		a.newMetaCommand(a.metaTargetFor(r, userPath)),
		a.newPasswordCommand(r),
		a.newTokenCommand(r),
	)...)
	return cmd
}

func (a *App) newPasswordCommand(r *resource) *cobra.Command {
	var passwordStdin bool
	cmd := &cobra.Command{
		Use:   "password [user]",
		Short: "Change a password (default: your own)",
		Long: `Change your own password, or another user's if you are allowed to. For
your own account the current password is asked for as well.

With --password-stdin, the passwords are read from stdin, one per line: the
current password first (only for your own account), then the new one.`,
		Args:              usageArgs(cobra.MaximumNArgs(1)),
		ValidArgsFunction: a.completeResource(r),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			ref := firstArg(args)
			if ref == "" {
				ref = "me"
			}
			o, s, _, err := a.findResource(ctx, r, ref, true)
			if err != nil {
				return err
			}
			me, err := s.Client.Me(ctx)
			if err != nil {
				return err
			}
			self := num(o, "id") == me.ID
			if !passwordStdin && !a.stdinIsTerminal() {
				return usageErrorf("cannot prompt for passwords without a terminal. Use --password-stdin")
			}
			read := a.promptSecret
			if passwordStdin {
				read = func(string) (string, error) { return a.readLine() }
			}
			body := map[string]string{}
			if self {
				if body["current_password"], err = read("Current password: "); err != nil {
					return err
				}
			}
			if body["new_password"], err = read(fmt.Sprintf("New password for %s: ", str(o["email"]))); err != nil {
				return err
			}
			if !passwordStdin {
				again, err := read("Repeat the new password: ")
				if err != nil {
					return err
				}
				if again != body["new_password"] {
					return usageErrorf("the passwords do not match")
				}
			}
			if body["new_password"] == "" {
				return usageErrorf("the new password must not be empty")
			}
			if _, err := s.Client.CallJSON(ctx, http.MethodPut, fmt.Sprintf("%s/%d/password", userPath, num(o, "id")), body); err != nil {
				return err
			}
			a.info("Changed the password of %s.", r.label(o))
			return nil
		},
	}
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the passwords from stdin")
	return cmd
}

// --- API tokens ---

func (a *App) newTokenCommand(users *resource) *cobra.Command {
	var userRef string
	// tokenPath resolves --user (default: you) to the token collection.
	tokenPath := func(ctx context.Context) (string, error) {
		o, _, _, err := a.findResource(ctx, users, userRef, false)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s/%d/security/token", userPath, num(o, "id")), nil
	}
	r := &resource{
		noun:       "token",
		collection: tokenPath,
		nameKey:    "name",
		columns: []column{
			field("id", "id"),
			field("name", "name"),
			{header: "expires", value: func(o api.JSONObject) string {
				if num(o, "expires") == 0 {
					return "never"
				}
				return formatTimestamp(float64(num(o, "expires")))
			}},
			{header: "last used", value: func(o api.JSONObject) string { return formatTimestamp(float64(num(o, "last_used"))) }},
			{header: "current", value: func(o api.JSONObject) string {
				if o["current"] == true {
					return "*"
				}
				return ""
			}},
			field("type", "type"),
			{header: "created", wide: true, value: func(o api.JSONObject) string { return formatTimestamp(float64(num(o, "created"))) }},
			wideField("permissions", "permissions"),
		},
	}
	cmd := groupCommand("token", "Manage API tokens (default: your own)", "tokens")
	cmd.Long = `API tokens are API keys for scripts and integrations. A new token is shown
only once, when it is created. Log in with one using "login --api-key".`
	cmd.PersistentFlags().StringVar(&userRef, "user", "me", "user whose tokens to manage")
	_ = cmd.RegisterFlagCompletionFunc("user", a.completeResource(users))
	cmd.AddCommand(
		a.newResourceListCommand(r),
		a.newTokenCreateCommand(r),
		a.newTokenDeleteCommand(r),
	)
	return cmd
}

// parseFuture accepts a duration from now ("90d") or a time.
func parseFuture(s string, now time.Time) (time.Time, error) {
	if d, err := parseDuration(s); err == nil {
		return now.Add(d), nil
	}
	return parseTime(s, now)
}

func (a *App) newTokenCreateCommand(r *resource) *cobra.Command {
	var name, expires string
	var permissions []string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create an API token and print it",
		Long: `Create an API token. The token is printed to stdout; it cannot be shown
again later. Without --permission it has the permissions of the user.`,
		Example: fmt.Sprintf(`  %[1]s user token create --name ci --expires 90d
  %[1]s user token create --name dashboard --permission installation/list/all
  %[1]s user token create --name ci -q | %[1]s login --profile ci --api-key`, a.Name),
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if name == "" {
				return usageErrorf("--name is required")
			}
			body := map[string]any{"name": name}
			if expires != "" {
				t, err := parseFuture(expires, time.Now())
				if err != nil {
					return usageError(err)
				}
				if !t.After(time.Now()) {
					return usageErrorf("--expires must be in the future")
				}
				body["expires"] = t.Unix()
			}
			if len(permissions) > 0 {
				body["permissions"] = permissions
			}
			s, err := a.session()
			if err != nil {
				return err
			}
			path, err := r.collection(ctx)
			if err != nil {
				return err
			}
			created, err := s.Client.GetObjectAt(ctx, http.MethodPost, path, body)
			if err != nil {
				return err
			}
			token := str(created["token"])
			switch {
			case a.outputFlag == string(output.JSON) || a.outputFlag == string(output.YAML):
				pr, err := a.printer()
				if err != nil {
					return err
				}
				if err := pr.Details(created, token, nil); err != nil {
					return err
				}
			default:
				fmt.Fprintln(a.Out, token)
			}
			a.info("Created %s. Store it now; it cannot be shown again.", r.label(created))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "name of the token (required)")
	f.StringVar(&expires, "expires", "", "when the token expires: a duration from now (90d) or a time (default: never)")
	f.StringArrayVar(&permissions, "permission", nil, "limit the token to this permission (repeatable)")
	return cmd
}

func (a *App) newTokenDeleteCommand(r *resource) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:               "delete <token>",
		Aliases:           []string{"rm", "revoke"},
		Short:             "Delete (revoke) an API token (ID or exact name required)",
		Args:              usageArgs(cobra.ExactArgs(1)),
		ValidArgsFunction: a.completeResource(r),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			list, s, path, err := a.listResource(ctx, r, nil)
			if err != nil {
				return err
			}
			o, err := resolve.Find("token", args[0], list, func(o api.JSONObject) resolve.Key {
				return resolve.Key{ID: num(o, "id"), Name: str(o["name"])}
			}, resolve.Options{Exact: true})
			if err != nil {
				return err
			}
			question := fmt.Sprintf("Delete %s?", r.label(o))
			if o["current"] == true {
				question = fmt.Sprintf("Delete %s? It is the token this command is using, so you will be logged out.", r.label(o))
			}
			if err := a.confirm(question, yes); err != nil {
				return err
			}
			if _, err := s.Client.CallJSON(ctx, http.MethodDelete, fmt.Sprintf("%s/%d", path, num(o, "id")), nil); err != nil {
				return err
			}
			a.info("Deleted %s.", r.label(o))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

// --- Roles and permissions ---

func (a *App) roleResource() *resource {
	return &resource{
		noun:       "role",
		collection: constPath(rolePath),
		nameKey:    "name",
		columns: []column{
			field("id", "id"),
			field("name", "name"),
			field("priority", "priority"),
			{header: "permissions", value: func(o api.JSONObject) string {
				p, _ := o["permissions"].(map[string]any)
				allowed := 0
				for _, v := range p {
					if v == true {
						allowed++
					}
				}
				return fmt.Sprintf("%d allowed", allowed)
			}},
		},
		readOnly: []string{"id"},
		required: []string{"name", "priority", "permissions"},
		createExample: fmt.Sprintf(`  %[1]s role create --name Integration --priority 10 \
      --allow installation/list/all --allow 'functionx/#' --deny 'functionx/+/delete'`, a.Name),
		createFlags: func(cmd *cobra.Command) func(context.Context, api.JSONObject) error {
			var name string
			var priority int
			var allow, deny []string
			f := cmd.Flags()
			f.StringVar(&name, "name", "", "name of the role")
			f.IntVar(&priority, "priority", 0, "priority: users can only assign roles up to their own role's priority")
			f.StringArrayVar(&allow, "allow", nil, "allow a permission (repeatable)")
			f.StringArrayVar(&deny, "deny", nil, "deny a permission (repeatable)")
			return func(_ context.Context, o api.JSONObject) error {
				if name != "" {
					o["name"] = name
				}
				if cmd.Flags().Changed("priority") || o["priority"] == nil {
					o["priority"] = priority
				}
				perms, _ := o["permissions"].(map[string]any)
				if perms == nil {
					perms = map[string]any{}
				}
				for _, p := range allow {
					perms[p] = true
				}
				for _, p := range deny {
					perms[p] = false
				}
				o["permissions"] = perms
				return nil
			}
		},
	}
}

func (a *App) newRoleCommand() *cobra.Command {
	r := a.roleResource()
	cmd := groupCommand("role", "Manage roles", "roles")
	cmd.Long = `A role is a set of permissions given to users. Permissions are MQTT-style
patterns where + matches one level and # the rest, e.g. "functionx/+/add".`
	cmd.AddCommand(a.resourceCommands(r)...)
	return cmd
}

func (a *App) newPermissionCommand() *cobra.Command {
	var allowedOnly bool
	cmd := groupCommand("permission", "Show your permissions", "permissions", "perm")
	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List your permissions",
		Example: fmt.Sprintf(`  %[1]s permission list --allowed
  %[1]s permission list -o json | jq 'with_entries(select(.key | startswith("functionx")))'`, a.Name),
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			pr, err := a.printer()
			if err != nil {
				return err
			}
			s, err := a.session()
			if err != nil {
				return err
			}
			// The API documentation spells this path "perimission"; the server
			// uses "permission".
			v, err := s.Client.CallJSON(cmd.Context(), http.MethodGet, "/api/v2/permission", nil)
			if err != nil {
				return err
			}
			perms, _ := v.(map[string]any)
			if perms == nil {
				return errors.New("unexpected response: no permissions")
			}
			keys := make([]string, 0, len(perms))
			shown := map[string]any{}
			for k, allowed := range perms {
				if allowedOnly && allowed != true {
					continue
				}
				keys = append(keys, k)
				shown[k] = allowed
			}
			sort.Strings(keys)
			rows := output.Rows{Columns: []output.Column{{Header: "permission"}, {Header: "allowed"}}}
			for _, k := range keys {
				rows.Rows = append(rows.Rows, []string{k, yesNo(perms[k] == true)})
			}
			return pr.List(shown, rows)
		},
	}
	list.Flags().BoolVar(&allowedOnly, "allowed", false, "only show permissions that are allowed")
	cmd.AddCommand(list)
	return cmd
}
