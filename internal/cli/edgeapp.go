// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/mrejas/lynx-cli/internal/api"
	"github.com/mrejas/lynx-cli/internal/output"
)

const edgeAppPath = "/api/v2/edge/app"

func (a *App) edgeAppResource() *resource {
	var org int64
	return &resource{
		noun: "app",
		collection: func(context.Context) (string, error) {
			if org != 0 {
				return fmt.Sprintf("%s/organization/%d", edgeAppPath, org), nil
			}
			return edgeAppPath, nil
		},
		nameKey: "name",
		columns: []column{
			field("id", "id"),
			field("name", "name"),
			field("category", "category"),
			{header: "publisher", value: func(o api.JSONObject) string {
				p, _ := o["publisher"].(map[string]any)
				return str(p["name"])
			}},
			{header: "public", value: func(o api.JSONObject) string { return yesNo(o["public"] == true) }},
			{header: "official", wide: true, value: func(o api.JSONObject) string { return yesNo(o["official"] == true) }},
			wideField("description", "short_description"),
		},
		readOnly: []string{"id", "publisher", "official", "created", "updated"},
		required: []string{"name", "category", "short_description", "description", "publisher"},
		createExample: fmt.Sprintf(`  %[1]s edge-app create --name "Heating control" --category climate \
      --short-description "Keeps the temperature steady" --description-file README.md --tag heating`, a.Name),
		createFlags: func(cmd *cobra.Command) func(context.Context, api.JSONObject) error {
			var name, category, short, description, descriptionFile, sourceURL string
			var tags []string
			var public bool
			var publisher int64
			f := cmd.Flags()
			f.StringVar(&name, "name", "", "name of the app")
			f.StringVar(&category, "category", "", "category")
			f.StringVar(&short, "short-description", "", "one-line description")
			f.StringVar(&description, "description", "", "description (Markdown)")
			f.StringVar(&descriptionFile, "description-file", "", `read the description from a file ("-" for stdin)`)
			f.StringArrayVar(&tags, "tag", nil, "tag (repeatable)")
			f.BoolVar(&public, "public", false, "make the app available to everyone")
			f.Int64Var(&publisher, "org", 0, "publishing organization (default: your organization, if you have only one)")
			f.StringVar(&sourceURL, "source-url", "", "URL of the source code")
			cmd.MarkFlagsMutuallyExclusive("description", "description-file")
			return func(ctx context.Context, o api.JSONObject) error {
				for key, v := range map[string]string{"name": name, "category": category, "short_description": short, "description": description, "source_url": sourceURL} {
					if v != "" {
						o[key] = v
					}
				}
				if descriptionFile != "" {
					data, err := a.readInput(descriptionFile)
					if err != nil {
						return err
					}
					o["description"] = string(data)
				}
				if len(tags) > 0 {
					o["tags"] = tags
				} else if _, ok := o["tags"]; !ok {
					o["tags"] = []string{}
				}
				if cmd.Flags().Changed("public") {
					o["public"] = public
				} else if _, ok := o["public"]; !ok {
					o["public"] = false
				}
				if _, ok := o["publisher"]; !ok || publisher != 0 {
					if publisher == 0 {
						var err error
						if publisher, err = a.onlyOrganization(ctx); err != nil {
							return err
						}
					}
					o["publisher"] = map[string]any{"id": publisher}
				}
				return nil
			}
		},
		listFlags: func(cmd *cobra.Command) func() (url.Values, error) {
			var available bool
			cmd.Flags().Int64Var(&org, "org", 0, "list the apps of this organization instead of those available to you")
			cmd.Flags().BoolVar(&available, "available", false, "with --org: also include apps available to the organization")
			return func() (url.Values, error) {
				if available {
					return url.Values{"available": {"true"}}, nil
				}
				return nil, nil
			}
		},
	}
}

// onlyOrganization returns the user's organization if there is exactly one.
func (a *App) onlyOrganization(ctx context.Context) (int64, error) {
	s, err := a.session()
	if err != nil {
		return 0, err
	}
	me, err := s.Client.Me(ctx)
	if err != nil {
		return 0, err
	}
	if len(me.Organisations) != 1 {
		return 0, usageErrorf("you belong to %d organizations. Choose one with --org", len(me.Organisations))
	}
	return me.Organisations[0], nil
}

func (a *App) newEdgeAppCommand() *cobra.Command {
	r := a.edgeAppResource()
	cmd := groupCommand("edge-app", "Develop, publish and configure edge apps", "edge-apps", "app", "apps")
	cmd.Long = `Edge apps are Lua programs that run on gateways. Apps are referred to by ID,
name or a unique part of the name.

Developing an app:
  edge-app create ...                 register the app
  edge-app upload <app> [dir]         upload app.json and app.lua as a new version
  edge-app publish <app> <version>    give the latest upload a version name

Running an app in the current installation:
  edge-app instance create --app <app> --config key=value ...`
	cmd.AddCommand(
		a.newResourceListCommand(r),
		a.newResourceGetCommand(r),
		a.newResourceCreateCommand(r),
		&cobra.Command{
			Use:               "edit <app>",
			Short:             "Edit an app's details in your editor",
			Args:              usageArgs(cobra.ExactArgs(1)),
			ValidArgsFunction: a.completeResource(r),
			RunE: func(cmd *cobra.Command, args []string) error {
				o, s, _, err := a.findResource(cmd.Context(), r, args[0], false)
				if err != nil {
					return err
				}
				// Apps are updated at the collection path, with the ID in the body.
				return a.editGeneric(cmd.Context(), s, r.label(o), o, r.readOnly, edgeAppPath)
			},
		},
		a.newEdgeAppVersionsCommand(r),
		a.newEdgeAppUploadCommand(r),
		a.newEdgeAppPublishCommand(r),
		a.newEdgeAppDownloadCommand(r),
		a.newEdgeAppOptionsCommand(r),
		a.newEdgeAppInstanceCommand(r),
	)
	return cmd
}

type appVersion struct {
	Name      string  `json:"name,omitempty"`
	Hash      string  `json:"hash"`
	Timestamp float64 `json:"timestamp,omitempty"`
}

// appVersions returns the versions of an app, newest first. With untagged,
// unpublished uploads are included.
func (a *App) appVersions(ctx context.Context, s *session, appID int64, untagged bool) ([]appVersion, error) {
	path := fmt.Sprintf("%s/%d/version", edgeAppPath, appID)
	if untagged {
		path += "?untagged=true"
	}
	v, err := s.Client.CallJSON(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	items, _ := v.([]any)
	var versions []appVersion
	for _, it := range items {
		o, _ := it.(map[string]any)
		versions = append(versions, appVersion{Name: str(o["name"]), Hash: str(o["hash"]), Timestamp: float64(num(o, "timestamp"))})
	}
	sort.SliceStable(versions, func(i, j int) bool { return versions[i].Timestamp > versions[j].Timestamp })
	return versions, nil
}

// latestPublished returns the name of the newest published version.
func (a *App) latestPublished(ctx context.Context, s *session, app api.JSONObject, label string) (string, error) {
	versions, err := a.appVersions(ctx, s, num(app, "id"), false)
	if err != nil {
		return "", err
	}
	for _, v := range versions {
		if v.Name != "" {
			return v.Name, nil
		}
	}
	return "", fmt.Errorf("%s has no published version", label)
}

func (a *App) newEdgeAppVersionsCommand(r *resource) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:               "versions <app>",
		Short:             "List the versions of an app, newest first",
		Args:              usageArgs(cobra.ExactArgs(1)),
		ValidArgsFunction: a.completeResource(r),
		RunE: func(cmd *cobra.Command, args []string) error {
			pr, err := a.printer()
			if err != nil {
				return err
			}
			o, s, _, err := a.findResource(cmd.Context(), r, args[0], false)
			if err != nil {
				return err
			}
			versions, err := a.appVersions(cmd.Context(), s, num(o, "id"), all)
			if err != nil {
				return err
			}
			rows := output.Rows{Columns: []output.Column{{Header: "version"}, {Header: "uploaded"}, {Header: "hash"}}}
			for _, v := range versions {
				name := v.Name
				if name == "" {
					name = "(unpublished)"
				}
				rows.Rows = append(rows.Rows, []string{name, formatTimestamp(v.Timestamp), v.Hash})
			}
			if versions == nil {
				versions = []appVersion{}
			}
			return pr.List(versions, rows)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "include unpublished uploads")
	return cmd
}

func (a *App) newEdgeAppUploadCommand(r *resource) *cobra.Command {
	return &cobra.Command{
		Use:   "upload <app> [directory]",
		Short: "Upload app.json and app.lua as a new version",
		Long: `Upload app.json and app.lua from a directory (default: the current one) as a
new, unpublished version of the app, and print its hash. Publish it with
"edge-app publish". Nothing is uploaded if the files are identical to the
latest version.`,
		Example: fmt.Sprintf(`  %[1]s edge-app upload "heating control" ./heating
  %[1]s edge-app publish "heating control" 1.2.0`, a.Name),
		Args:              usageArgs(cobra.RangeArgs(1, 2)),
		ValidArgsFunction: a.completeResource(r),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			dir := "."
			if len(args) == 2 {
				dir = args[1]
			}
			var parts []api.FilePart
			for _, f := range []struct{ name, field, mime string }{
				{"app.json", "app_json", "application/json"},
				{"app.lua", "app_lua", "text/x-lua"},
			} {
				data, err := os.ReadFile(filepath.Join(dir, f.name))
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				if err != nil {
					return err
				}
				parts = append(parts, api.FilePart{Field: f.field, Filename: f.name, ContentType: f.mime, Data: data})
			}
			if len(parts) == 0 {
				return usageErrorf("neither app.json nor app.lua found in %s", dir)
			}
			o, s, _, err := a.findResource(ctx, r, args[0], false)
			if err != nil {
				return err
			}
			body, status, err := s.Client.Upload(ctx, http.MethodPost, fmt.Sprintf("%s/%d/version", edgeAppPath, num(o, "id")), parts)
			if err != nil {
				return err
			}
			if status == http.StatusNoContent {
				a.info("Nothing uploaded: the files are identical to the latest version of %s.", r.label(o))
				return nil
			}
			v, err := api.DecodeJSON(body)
			if err != nil {
				return err
			}
			res, _ := v.(map[string]any)
			hash := str(res["hash"])
			fmt.Fprintln(a.Out, hash)
			a.info("Uploaded a new version of %s. Publish it with %q.", r.label(o), a.Name+" edge-app publish "+shellQuote(r.name(o))+" <version>")
			return nil
		},
	}
}

// shellQuote quotes a name for a shell command line if needed.
func shellQuote(s string) string {
	for _, c := range s {
		if !(c == '-' || c == '_' || c == '.' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return "'" + s + "'"
		}
	}
	return s
}

func (a *App) newEdgeAppPublishCommand(r *resource) *cobra.Command {
	var hash string
	cmd := &cobra.Command{
		Use:   "publish <app> <version>",
		Short: "Publish the latest upload under a version name",
		Long: `Give an uploaded version a name, which publishes it. By default the newest
unpublished upload is published; use --hash to pick another.`,
		Example:           fmt.Sprintf(`  %[1]s edge-app publish "heating control" 1.2.0`, a.Name),
		Args:              usageArgs(cobra.ExactArgs(2)),
		ValidArgsFunction: a.completeResource(r),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			o, s, _, err := a.findResource(ctx, r, args[0], false)
			if err != nil {
				return err
			}
			id := num(o, "id")
			if hash == "" {
				versions, err := a.appVersions(ctx, s, id, true)
				if err != nil {
					return err
				}
				for _, v := range versions {
					if v.Name == "" {
						hash = v.Hash
						break
					}
				}
				if hash == "" {
					return fmt.Errorf("%s has no unpublished upload. Upload one with %q", r.label(o), a.Name+" edge-app upload")
				}
			}
			body := appVersion{Name: args[1], Hash: hash}
			if _, err := s.Client.CallJSON(ctx, http.MethodPost, fmt.Sprintf("%s/%d/publish", edgeAppPath, id), body); err != nil {
				return err
			}
			a.info("Published %s version %s (%s).", r.label(o), args[1], hash)
			return nil
		},
	}
	cmd.Flags().StringVar(&hash, "hash", "", "hash of the upload to publish (default: the newest unpublished)")
	return cmd
}

func (a *App) newEdgeAppDownloadCommand(r *resource) *cobra.Command {
	var version, out string
	var force bool
	cmd := &cobra.Command{
		Use:   "download <app>",
		Short: "Download the code of an app version",
		Long: `Download the code of a published version (default: the newest). Save it
with -O, or redirect stdout.`,
		Example: fmt.Sprintf(`  %[1]s edge-app download "heating control" -O heating.lua
  %[1]s edge-app download "heating control" --version 1.1.0 > old.lua`, a.Name),
		Args:              usageArgs(cobra.ExactArgs(1)),
		ValidArgsFunction: a.completeResource(r),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if out == "" && a.stdoutIsTerminal() {
				return usageErrorf("use -O <file> to save the code, or redirect stdout")
			}
			o, s, _, err := a.findResource(ctx, r, args[0], false)
			if err != nil {
				return err
			}
			if version == "" {
				if version, err = a.latestPublished(ctx, s, o, r.label(o)); err != nil {
					return err
				}
			}
			path := fmt.Sprintf("%s/%d/download?version=%s", edgeAppPath, num(o, "id"), url.QueryEscape(version))
			if out == "" || out == "-" {
				return s.Client.Download(ctx, path, a.Out)
			}
			return a.downloadTo(ctx, s, path, out, force, fmt.Sprintf("%s version %s", r.label(o), version))
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "version to download (default: the newest published)")
	cmd.Flags().StringVarP(&out, "output-file", "O", "", `where to save the code ("-" for stdout)`)
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing file")
	return cmd
}

func (a *App) newEdgeAppOptionsCommand(r *resource) *cobra.Command {
	var version string
	cmd := &cobra.Command{
		Use:               "options <app>",
		Short:             "Show the configuration options (app.json) of an app version",
		Args:              usageArgs(cobra.ExactArgs(1)),
		ValidArgsFunction: a.completeResource(r),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			o, s, _, err := a.findResource(ctx, r, args[0], false)
			if err != nil {
				return err
			}
			if version == "" {
				if version, err = a.latestPublished(ctx, s, o, r.label(o)); err != nil {
					return err
				}
			}
			v, err := s.Client.CallJSON(ctx, http.MethodGet, fmt.Sprintf("%s/%d/configure?version=%s", edgeAppPath, num(o, "id"), url.QueryEscape(version)), nil)
			if err != nil {
				return err
			}
			if a.outputFlag == string(output.YAML) {
				return output.WriteYAML(a.Out, v)
			}
			return output.WriteJSON(a.Out, v)
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "version (default: the newest published)")
	return cmd
}

func (a *App) newEdgeAppInstanceCommand(apps *resource) *cobra.Command {
	r := &resource{
		noun:       "instance",
		collection: a.installationPath(edgeAppPath + "/configured/%d"),
		nameKey:    "name",
		columns: []column{
			field("id", "id"),
			field("name", "name"),
			field("app", "app_id"),
			field("version", "version"),
			{header: "updated", wide: true, value: func(o api.JSONObject) string { return formatTimestamp(float64(num(o, "updated"))) }},
		},
		readOnly: []string{"id", "installation_id", "created", "updated"},
		required: []string{"app_id", "version", "name"},
		createExample: fmt.Sprintf(`  %[1]s edge-app instance create --app "heating control" --config target=21 --config sensor=1042
  %[1]s edge-app instance create --app 12 --version 1.1.0 --name "Heating upstairs" -f config.yaml`, a.Name),
		createFlags: func(cmd *cobra.Command) func(context.Context, api.JSONObject) error {
			var app, version, name string
			var config []string
			f := cmd.Flags()
			f.StringVar(&app, "app", "", "app to run (ID or name)")
			f.StringVar(&version, "version", "", "app version (default: the newest published)")
			f.StringVar(&name, "name", "", "name of the instance (default: the app's name)")
			f.StringArrayVar(&config, "config", nil, "configuration value `key=value` (typed; dots create nested objects; repeatable)")
			_ = cmd.RegisterFlagCompletionFunc("app", a.completeResource(apps))
			return func(ctx context.Context, o api.JSONObject) error {
				cfg, err := buildFields(config, nil)
				if err != nil {
					return err
				}
				sc, err := a.scope(ctx)
				if err != nil {
					return err
				}
				o["installation_id"] = sc.installation
				existing, _ := o["config"].(map[string]any)
				if existing == nil {
					existing = map[string]any{}
				}
				mergeObjects(existing, cfg)
				o["config"] = existing
				if app != "" {
					found, s, _, err := a.findResource(ctx, apps, app, false)
					if err != nil {
						return err
					}
					o["app_id"] = num(found, "id")
					if name == "" && o["name"] == nil {
						name = apps.name(found)
					}
					if version == "" && o["version"] == nil {
						if version, err = a.latestPublished(ctx, s, found, apps.label(found)); err != nil {
							return err
						}
					}
				}
				if name != "" {
					o["name"] = name
				}
				if version != "" {
					o["version"] = version
				}
				return nil
			}
		},
	}
	cmd := groupCommand("instance", "Manage apps running in the current installation", "instances", "inst")
	cmd.AddCommand(a.resourceCommands(r)...)
	return cmd
}
