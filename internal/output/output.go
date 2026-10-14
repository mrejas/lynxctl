// Package output prints data as tables, details, JSON, YAML or CSV.
package output

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"gopkg.in/yaml.v3"
)

// Format is an output format selected with -o.
type Format string

const (
	Table Format = "table"
	Wide  Format = "wide"
	JSON  Format = "json"
	YAML  Format = "yaml"
	CSV   Format = "csv"
)

// Formats lists all valid formats, for help texts and completion.
var Formats = []Format{Table, Wide, JSON, YAML, CSV}

// ParseFormat validates a format name. An empty name means Table.
func ParseFormat(s string) (Format, error) {
	if s == "" {
		return Table, nil
	}
	for _, f := range Formats {
		if strings.EqualFold(s, string(f)) {
			return f, nil
		}
	}
	names := make([]string, len(Formats))
	for i, f := range Formats {
		names[i] = string(f)
	}
	return "", fmt.Errorf("invalid output format %q (valid: %s)", s, strings.Join(names, ", "))
}

// Column is a table column. Wide columns are only shown with -o wide (and
// always in CSV).
type Column struct {
	Header string
	Wide   bool
}

// Rows is tabular data. IDColumn is the column printed by --quiet.
type Rows struct {
	Columns  []Column
	Rows     [][]string
	IDColumn int
}

// Field is one line of a details view.
type Field struct {
	Label string
	Value string
}

// Printer writes data in the selected format.
type Printer struct {
	Out    io.Writer
	Format Format
	// Quiet prints only IDs, one per line.
	Quiet bool
}

// List prints a collection. data is used for JSON and YAML, rows for the
// other formats.
func (p *Printer) List(data any, rows Rows) error {
	if p.Quiet {
		for _, r := range rows.Rows {
			fmt.Fprintln(p.Out, r[rows.IDColumn])
		}
		return nil
	}
	switch p.Format {
	case JSON:
		return WriteJSON(p.Out, data)
	case YAML:
		return WriteYAML(p.Out, data)
	case CSV:
		return p.csv(rows.Columns, rows.Rows)
	}
	return p.table(rows, p.Format == Wide)
}

// Details prints a single object as "Label: value" lines. id is printed by
// --quiet.
func (p *Printer) Details(data any, id string, fields []Field) error {
	if p.Quiet {
		if id != "" {
			fmt.Fprintln(p.Out, id)
		}
		return nil
	}
	switch p.Format {
	case JSON:
		return WriteJSON(p.Out, data)
	case YAML:
		return WriteYAML(p.Out, data)
	case CSV:
		rows := make([][]string, len(fields))
		for i, f := range fields {
			rows[i] = []string{f.Label, f.Value}
		}
		return p.csv([]Column{{Header: "field"}, {Header: "value"}}, rows)
	}
	tw := tabwriter.NewWriter(p.Out, 0, 8, 2, ' ', 0)
	for _, f := range fields {
		fmt.Fprintf(tw, "%s:\t%s\n", f.Label, f.Value)
	}
	return tw.Flush()
}

func (p *Printer) table(rows Rows, wide bool) error {
	tw := tabwriter.NewWriter(p.Out, 0, 8, 3, ' ', 0)
	var visible []int
	for i, c := range rows.Columns {
		if wide || !c.Wide {
			visible = append(visible, i)
		}
	}
	line := make([]string, len(visible))
	for i, c := range visible {
		line[i] = strings.ToUpper(rows.Columns[c].Header)
	}
	fmt.Fprintln(tw, strings.Join(line, "\t"))
	for _, r := range rows.Rows {
		for i, c := range visible {
			line[i] = r[c]
		}
		fmt.Fprintln(tw, strings.Join(line, "\t"))
	}
	return tw.Flush()
}

func (p *Printer) csv(cols []Column, rows [][]string) error {
	w := csv.NewWriter(p.Out)
	header := make([]string, len(cols))
	for i, c := range cols {
		header[i] = strings.ToLower(c.Header)
	}
	if err := w.Write(header); err != nil {
		return err
	}
	if err := w.WriteAll(rows); err != nil {
		return err
	}
	w.Flush()
	return w.Error()
}

// WriteJSON writes v as indented JSON without HTML escaping.
func WriteJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// WriteCompactJSON writes v as JSON on a single line, for streams of
// objects (one per line).
func WriteCompactJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// WriteYAML writes v as YAML. It goes through JSON so that the field names
// and order match the JSON output.
func WriteYAML(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	clearStyle(&doc)
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return err
	}
	return enc.Close()
}

// clearStyle removes the JSON flow style that the YAML decoder records, so
// the output is written in block style.
func clearStyle(n *yaml.Node) {
	n.Style = 0
	for _, c := range n.Content {
		clearStyle(c)
	}
}

// IndentJSON pretty-prints data if it is valid JSON, otherwise it returns
// data unchanged.
func IndentJSON(data []byte) []byte {
	var buf bytes.Buffer
	if err := json.Indent(&buf, data, "", "  "); err != nil {
		return data
	}
	return buf.Bytes()
}
