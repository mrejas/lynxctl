// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
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
			pairs, err := parsePairs(meta, "--meta filter")
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
			rows := output.Rows{Columns: []output.Column{
				{Header: "type"}, {Header: "id"}, {Header: "name"}, {Header: "installation"},
				{Header: "matches", Wide: true},
			}, IDColumn: 1}
			for _, o := range results {
				inst := str(o["installationID"])
				if str(o["type"]) == "installation" {
					inst = "client ID " + str(o["clientID"])
				}
				rows.Rows = append(rows.Rows, []string{str(o["type"]), str(o["id"]), str(o["name"]), inst, formatMatches(o["metaMatches"])})
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
	f.StringArrayVar(&meta, "meta", nil, "only find objects whose meta `key=value` matches (* is a wildcard; repeatable)")
	f.IntVar(&limit, "limit", 0, "maximum number of results (default and maximum: 10000)")
	_ = cmd.RegisterFlagCompletionFunc("type", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return searchTypes, cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
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
