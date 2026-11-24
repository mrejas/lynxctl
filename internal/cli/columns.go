// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"errors"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mrejas/lynxctl/internal/api"
	"github.com/mrejas/lynxctl/internal/output"
)

// listColumn is a table column of a list of T.
type listColumn[T any] struct {
	header string
	value  func(T) string
	// wide columns are only shown with -o wide (and in CSV).
	wide bool
	// extra columns are only shown when chosen with --columns.
	extra bool
}

// columnRows builds the default table: all columns but the extra ones.
func columnRows[T any](cols []listColumn[T], list []T, idColumn int) output.Rows {
	var shown []listColumn[T]
	for _, c := range cols {
		if !c.extra {
			shown = append(shown, c)
		}
	}
	rows := output.Rows{IDColumn: idColumn}
	for _, c := range shown {
		rows.Columns = append(rows.Columns, output.Column{Header: c.header, Wide: c.wide})
	}
	for _, o := range list {
		row := make([]string, len(shown))
		for i, c := range shown {
			row[i] = c.value(o)
		}
		rows.Rows = append(rows.Rows, row)
	}
	return rows
}

// metaFunc returns the meta and protected meta of an object.
type metaFunc[T any] func(T) (meta, protected api.Meta)

// pickColumns resolves --columns. A name is a column header (spaces may be
// written as underscores); any other name is a meta key, and "meta.<key>"
// is always a meta key. A leading "+" starts from the default (non-wide)
// columns. The chosen columns are all shown, also without -o wide.
func pickColumns[T any](cols []listColumn[T], names []string, meta metaFunc[T]) ([]listColumn[T], error) {
	var out []listColumn[T]
	if len(names) > 0 && strings.HasPrefix(names[0], "+") {
		for _, c := range cols {
			if !c.wide && !c.extra {
				out = append(out, c)
			}
		}
	}
	for _, n := range names {
		n = strings.TrimSpace(strings.TrimPrefix(n, "+"))
		if n == "" {
			return nil, usageError(errors.New("--columns: empty column name"))
		}
		out = append(out, findColumn(cols, n, meta))
	}
	return out, nil
}

func findColumn[T any](cols []listColumn[T], name string, meta metaFunc[T]) listColumn[T] {
	if key, ok := strings.CutPrefix(name, "meta."); ok {
		return metaColumn(key, meta)
	}
	for _, c := range cols {
		if strings.EqualFold(columnName(c.header), name) {
			c.wide, c.extra = false, false
			return c
		}
	}
	return metaColumn(name, meta)
}

func metaColumn[T any](key string, meta metaFunc[T]) listColumn[T] {
	return listColumn[T]{header: key, value: func(o T) string {
		m, p := meta(o)
		if v, ok := m[key]; ok {
			return v
		}
		return p[key]
	}}
}

// columnName is how a header is written in --columns.
func columnName(header string) string {
	return strings.ReplaceAll(header, " ", "_")
}

// listRows gives the rows of a list: the default columns, or the ones
// chosen with --columns. -q always prints the default ID column.
func listRows[T any](pr *output.Printer, cols []listColumn[T], list []T, idColumn int, chosen []string, meta metaFunc[T]) (output.Rows, error) {
	if len(chosen) == 0 || pr.Quiet {
		return columnRows(cols, list, idColumn), nil
	}
	picked, err := pickColumns(cols, chosen, meta)
	if err != nil {
		return output.Rows{}, err
	}
	return columnRows(picked, list, 0), nil
}

// addColumnsFlag registers --columns on a list command with cols.
func addColumnsFlag[T any](cmd *cobra.Command, chosen *[]string, cols []listColumn[T]) {
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = columnName(c.header)
	}
	cmd.Flags().StringSliceVarP(chosen, "columns", "c", nil,
		"columns to show, comma-separated (repeatable): "+strings.Join(names, ", ")+
			", or any meta `key`; a leading + adds to the default columns")
	_ = cmd.RegisterFlagCompletionFunc("columns", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return names, cobra.ShellCompDirectiveNoFileComp
	})
}
