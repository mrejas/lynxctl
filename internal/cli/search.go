// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mrejas/lynx-cli/internal/api"
	"github.com/mrejas/lynx-cli/internal/output"
)

var searchTypes = []string{"installation", "device", "function"}

func (a *App) newSearchCommand() *cobra.Command {
	var types, meta []string
	var limit int
	cmd := &cobra.Command{
		Use:   "search [query]",
		Short: "Search installations, devices and functions everywhere",
		Long: `Search all installations you can access, and their devices and functions,
by name, description, metadata or ID. * is a wildcard. Without a query,
everything matching the filters is listed.`,
		Example: fmt.Sprintf(`  %[1]s search kitchen
  %[1]s search 'temp*' --type function
  %[1]s search --meta location=stockholm --type installation`, a.Name),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			pr, err := a.printer()
			if err != nil {
				return err
			}
			q := url.Values{}
			query := strings.Join(args, " ")
			if query == "" {
				query = "*"
			}
			q.Set("q", query)
			var selected []string
			for _, t := range types {
				for _, part := range strings.Split(t, ",") {
					part = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(part)), "s")
					if !slices.Contains(searchTypes, part) {
						return usageErrorf("invalid --type %q (valid: %s)", t, strings.Join(searchTypes, ", "))
					}
					selected = append(selected, part)
				}
			}
			if len(selected) > 0 {
				q.Set("types", strings.Join(selected, ","))
			}
			pairs, err := metaPairs(meta)
			if err != nil {
				return err
			}
			for _, p := range pairs {
				q.Add("metadata."+p[0], p[1])
			}
			if limit > 0 {
				q.Set("limit", strconv.Itoa(limit))
			}

			s, err := a.session()
			if err != nil {
				return err
			}
			v, err := s.Client.CallJSON(ctx, http.MethodGet, "/api/v3beta/search?"+q.Encode(), nil)
			if err != nil {
				return err
			}
			res, _ := v.(map[string]any)
			items, _ := res["results"].([]any)
			results := make([]api.JSONObject, 0, len(items))
			for _, it := range items {
				if o, ok := it.(map[string]any); ok {
					results = append(results, o)
				}
			}
			names := a.installationNames(ctx, s, results)
			rows := output.Rows{Columns: []output.Column{
				{Header: "type"}, {Header: "id"}, {Header: "name"}, {Header: "installation"},
				{Header: "matches", Wide: true},
			}, IDColumn: 1}
			for _, o := range results {
				var inst string
				if str(o["type"]) == "installation" {
					inst = "client ID " + strOf(o, "client_id", "clientID")
				} else if id := strOf(o, "installation_id", "installationID"); names[id] != "" {
					inst = names[id] + " (" + id + ")"
				} else {
					inst = id
				}
				rows.Rows = append(rows.Rows, []string{str(o["type"]), str(o["id"]), str(o["name"]), inst, formatMatches(firstOf(o, "meta_matches", "metaMatches"))})
			}
			if err := pr.List(results, rows); err != nil {
				return err
			}
			if total := int(num(res, "total")); total > len(results) {
				a.info("Showing %d of %d results. Use --limit or a narrower query.", len(results), total)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringArrayVar(&types, "type", nil, "only find these types: installation, device, function (repeatable or comma-separated)")
	f.StringArrayVar(&meta, "meta", nil, "only find objects whose meta `key=value` matches; * is a wildcard (repeatable)")
	f.IntVar(&limit, "limit", 0, "maximum number of results (default and maximum: 10000)")
	_ = cmd.RegisterFlagCompletionFunc("type", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return searchTypes, cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
}

// The server answers in snake_case like the rest of the API, while the
// docs show camelCase; both are accepted.

// firstOf returns the first of keys that o has.
func firstOf(o api.JSONObject, keys ...string) any {
	for _, k := range keys {
		if v, ok := o[k]; ok && v != nil {
			return v
		}
	}
	return nil
}

// strOf returns the first of keys that o has, as a string.
func strOf(o api.JSONObject, keys ...string) string {
	return str(firstOf(o, keys...))
}

// installationNames maps the installation IDs of device and function
// results to names. Lookup errors only leave the names out.
func (a *App) installationNames(ctx context.Context, s *session, results []api.JSONObject) map[string]string {
	names := map[string]string{}
	need := false
	for _, o := range results {
		need = need || str(o["type"]) != "installation"
	}
	if !need {
		return names
	}
	infos, err := s.Client.InstallationInfos(ctx)
	if err != nil {
		return names
	}
	if all, err := s.Client.ListInstallations(ctx, nil); err == nil {
		infos = mergeInstallations(infos, all)
	}
	for _, i := range infos {
		names[strconv.FormatInt(i.ID, 10)] = i.Name
	}
	return names
}

func formatMatches(v any) string {
	m, _ := v.(map[string]any)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + str(m[k])
	}
	return strings.Join(parts, ", ")
}
