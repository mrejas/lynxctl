package output

import (
	"bytes"
	"strings"
	"testing"
)

var rows = Rows{
	Columns:  []Column{{Header: "id"}, {Header: "name"}, {Header: "extra", Wide: true}},
	Rows:     [][]string{{"1", "Kök", "x"}, {"22", "Hall", "y"}},
	IDColumn: 0,
}

func render(t *testing.T, p Printer, data any) string {
	t.Helper()
	var buf bytes.Buffer
	p.Out = &buf
	if err := p.List(data, rows); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestTableHidesWideColumns(t *testing.T) {
	got := render(t, Printer{Format: Table}, nil)
	want := "ID   NAME\n1    Kök\n22   Hall\n"
	if got != want {
		t.Errorf("table output:\n%q\nwant:\n%q", got, want)
	}
	if wide := render(t, Printer{Format: Wide}, nil); !strings.Contains(wide, "EXTRA") {
		t.Errorf("wide output lacks the wide column:\n%s", wide)
	}
}

func TestQuietPrintsIDs(t *testing.T) {
	if got := render(t, Printer{Format: JSON, Quiet: true}, nil); got != "1\n22\n" {
		t.Errorf("quiet output = %q", got)
	}
}

func TestCSVIncludesAllColumns(t *testing.T) {
	got := render(t, Printer{Format: CSV}, nil)
	want := "id,name,extra\n1,Kök,x\n22,Hall,y\n"
	if got != want {
		t.Errorf("csv output = %q, want %q", got, want)
	}
}

func TestYAMLKeepsOrderAndQuotesAmbiguousStrings(t *testing.T) {
	data := struct {
		Name  string `json:"name"`
		Code  string `json:"code"`
		Flag  string `json:"flag"`
		Count int    `json:"count"`
		HTML  string `json:"html"`
	}{"Kök", "123", "true", 5, "<b>"}
	var buf bytes.Buffer
	if err := WriteYAML(&buf, data); err != nil {
		t.Fatal(err)
	}
	want := "name: Kök\ncode: \"123\"\nflag: \"true\"\ncount: 5\nhtml: <b>\n"
	if buf.String() != want {
		t.Errorf("yaml output:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestParseFormat(t *testing.T) {
	if f, err := ParseFormat("JSON"); err != nil || f != JSON {
		t.Errorf("ParseFormat(JSON) = %v, %v", f, err)
	}
	if f, err := ParseFormat(""); err != nil || f != Table {
		t.Errorf("ParseFormat(\"\") = %v, %v", f, err)
	}
	if _, err := ParseFormat("xml"); err == nil {
		t.Error("ParseFormat(xml) succeeded")
	}
}
