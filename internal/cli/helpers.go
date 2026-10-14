package cli

import (
	"bytes"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/mrejas/lynx-cli/internal/api"
	"github.com/mrejas/lynx-cli/internal/output"
)

// parsePairs parses repeated key=value arguments.
func parsePairs(pairs []string, what string) ([][2]string, error) {
	out := make([][2]string, 0, len(pairs))
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, usageErrorf("invalid %s %q, expected key=value", what, p)
		}
		out = append(out, [2]string{strings.TrimSpace(k), v})
	}
	return out, nil
}

// metaFilter builds the query for list endpoints from --meta and --type.
func metaFilter(metaFlags []string, typ string) (url.Values, error) {
	pairs, err := parsePairs(metaFlags, "--meta filter")
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	for _, p := range pairs {
		q.Add(p[0], p[1])
	}
	if typ != "" {
		q.Set("type", typ)
	}
	return q, nil
}

// readInput reads a file, or stdin for "-".
func (a *App) readInput(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(a.reader())
	}
	return os.ReadFile(path)
}

// decodeStrict parses YAML (or JSON, which is valid YAML) and rejects
// unknown fields, so that typos do not silently disappear.
func decodeStrict(data []byte, v any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil && err != io.EOF {
		return fmt.Errorf("invalid input: %w", err)
	}
	return nil
}

func formatTime(unix float64) string {
	if unix == 0 {
		return ""
	}
	return time.Unix(int64(unix), 0).Local().Format("2006-01-02 15:04")
}

// metaFields renders meta keys as indented lines of a details view.
func metaFields(title string, m api.Meta) []output.Field {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fields := []output.Field{{Label: title}}
	for _, k := range keys {
		fields = append(fields, output.Field{Label: "  " + k, Value: m[k]})
	}
	return fields
}

// quoted gives `"Kitchen" (ID 1042)`, or `ID 1042` for unnamed objects.
func quoted(name string, id int64) string {
	if name == "" {
		return fmt.Sprintf("ID %d", id)
	}
	return fmt.Sprintf("%q (ID %d)", name, id)
}
