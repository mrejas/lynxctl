package resolve

import (
	"errors"
	"strings"
	"testing"
)

type item struct {
	id, clientID int64
	name         string
}

var items = []item{
	{10, 1010, "Main Street 1"},
	{11, 1011, "Main Street 2"},
	{12, 1012, "Summer house"},
	{13, 0, "21"},
	{14, 0, "Kök"},
	{15, 0, "Kökslampa"},
}

func key(i item) Key { return Key{ID: i.id, AltID: i.clientID, Name: i.name} }

func TestFind(t *testing.T) {
	tests := []struct {
		ref    string
		opts   Options
		wantID int64
		// wantErr is "notfound", "ambiguous" or "".
		wantErr string
	}{
		{ref: "11", wantID: 11},
		{ref: "1012", wantID: 12},             // client ID
		{ref: "summer HOUSE", wantID: 12},     // exact name, case-insensitive
		{ref: "summer", wantID: 12},           // unique substring
		{ref: "  summer ", wantID: 12},        // surrounding space ignored
		{ref: "street 2", wantID: 11},         // substring
		{ref: "street", wantErr: "ambiguous"}, // two matches
		{ref: "kök", wantID: 14},              // exact match wins over substring
		{ref: "KÖKS", wantID: 15},             // non-ASCII case folding
		{ref: "13", wantID: 13},
		{ref: "21", opts: Options{NameOnly: true}, wantID: 13},
		{ref: "10", opts: Options{NameOnly: true}, wantErr: "notfound"},
		{ref: "nothing", wantErr: "notfound"},
		{ref: "summer", opts: Options{Exact: true}, wantErr: "notfound"},
		{ref: "summer house", opts: Options{Exact: true}, wantID: 12},
	}
	for _, tt := range tests {
		got, err := Find("installation", tt.ref, items, key, tt.opts)
		var nf *NotFoundError
		var amb *AmbiguousError
		switch tt.wantErr {
		case "":
			if err != nil {
				t.Errorf("Find(%q, %+v): unexpected error %v", tt.ref, tt.opts, err)
			} else if got.id != tt.wantID {
				t.Errorf("Find(%q, %+v) = %d, want %d", tt.ref, tt.opts, got.id, tt.wantID)
			}
		case "notfound":
			if !errors.As(err, &nf) {
				t.Errorf("Find(%q, %+v): got %v (%v), want NotFoundError", tt.ref, tt.opts, got, err)
			}
		case "ambiguous":
			if !errors.As(err, &amb) {
				t.Errorf("Find(%q, %+v): got %v (%v), want AmbiguousError", tt.ref, tt.opts, got, err)
			}
		}
	}
}

func TestNumericReferenceFallsBackToName(t *testing.T) {
	// "21" is not an ID, but it is an exact name.
	got, err := Find("function", "21", []item{{13, 0, "21"}, {14, 0, "Room 210"}}, key, Options{})
	if err != nil || got.id != 13 {
		t.Fatalf("got %v, %v; want ID 13", got, err)
	}
}

func TestAmbiguousErrorListsCandidates(t *testing.T) {
	_, err := Find("function", "street", items, key, Options{})
	msg := err.Error()
	for _, want := range []string{`"street" matches 2 functions:`, "10  Main Street 1", "11  Main Street 2", "Use a more specific name or the ID."} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message %q does not contain %q", msg, want)
		}
	}
}
