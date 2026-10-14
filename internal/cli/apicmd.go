package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mrejas/lynx-cli/internal/api"
	"github.com/mrejas/lynx-cli/internal/output"
)

var httpMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}

type apiOptions struct {
	input     string
	fields    []string
	rawFields []string
	headers   []string
	include   bool
}

func (a *App) newAPICommand() *cobra.Command {
	var opts apiOptions
	cmd := &cobra.Command{
		Use:   "api [METHOD] <path>",
		Short: "Call any API endpoint",
		Long: `Make an authenticated request to any endpoint of the REST API and print the
response. METHOD defaults to GET, or POST when a body is given.

A path that does not start with "/" is relative to /api/v2/. The placeholders
{installation} and {client_id} are replaced with the ID and client ID of the
current installation (or the one given with -i).

Fields given with --field are sent as a JSON object. Dots in the key create
nested objects. Values that look like numbers, booleans, null, arrays or
objects are sent as such; use --raw-field to always send a string.

The API reference is at https://lynx.iotopen.se/api-doc/`,
		Example: fmt.Sprintf(`  %[1]s api user/me
  %[1]s api GET 'functionx/{installation}?type=switch'
  %[1]s api POST 'schedule/{installation}' -f schedule.json
  %[1]s api PUT 'functionx/{installation}/1042/meta/unit?create_missing=true' -F value=°C
  %[1]s api DELETE /api/v2/devicex/42/12`, a.Name),
		Args: usageArgs(cobra.RangeArgs(1, 2)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.callAPI(cmd.Context(), args, opts)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&opts.input, "input", "f", "", `file with the request body ("-" for stdin)`)
	f.StringArrayVarP(&opts.fields, "field", "F", nil, "add a typed JSON field `key=value` to the body")
	f.StringArrayVar(&opts.rawFields, "raw-field", nil, "add a string JSON field `key=value` to the body")
	f.StringArrayVarP(&opts.headers, "header", "H", nil, "add an HTTP header `Name: value`")
	f.BoolVar(&opts.include, "include", false, "print the HTTP status line and response headers")
	return cmd
}

func (a *App) callAPI(ctx context.Context, args []string, opts apiOptions) error {
	method, path, methodGiven, err := parseAPIArgs(args)
	if err != nil {
		return err
	}
	body, err := a.apiBody(opts)
	if err != nil {
		return err
	}
	if body != nil && !methodGiven {
		method = http.MethodPost
	}

	s, err := a.session()
	if err != nil {
		return err
	}
	if path, err = a.expandPlaceholders(ctx, s, path); err != nil {
		return err
	}

	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := s.Client.NewRequest(ctx, method, path, bodyReader)
	if err != nil {
		return err
	}
	for _, h := range opts.headers {
		name, value, ok := strings.Cut(h, ":")
		if !ok || strings.TrimSpace(name) == "" {
			return usageErrorf("invalid header %q, expected \"Name: value\"", h)
		}
		req.Header.Set(strings.TrimSpace(name), strings.TrimSpace(value))
	}

	resp, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if opts.include {
		fmt.Fprintf(a.Out, "%s %s\n", resp.Proto, resp.Status)
		names := make([]string, 0, len(resp.Header))
		for name := range resp.Header {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			for _, v := range resp.Header[name] {
				fmt.Fprintf(a.Out, "%s: %s\n", name, v)
			}
		}
		fmt.Fprintln(a.Out)
	}
	if a.stdoutIsTerminal() {
		data = output.IndentJSON(data)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			data = append(data, '\n')
		}
	}
	if _, err := a.Out.Write(data); err != nil {
		return err
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return api.NewError(resp, data)
	}
	return nil
}

// parseAPIArgs splits "[METHOD] <path>" and turns a relative path into an
// /api/v2/ path.
func parseAPIArgs(args []string) (method, path string, methodGiven bool, err error) {
	method, path = http.MethodGet, args[0]
	if len(args) == 2 {
		method, path, methodGiven = strings.ToUpper(args[0]), args[1], true
		known := false
		for _, m := range httpMethods {
			known = known || m == method
		}
		if !known {
			return "", "", false, usageErrorf("invalid HTTP method %q (valid: %s)", args[0], strings.Join(httpMethods, ", "))
		}
	} else {
		for _, m := range httpMethods {
			if strings.EqualFold(path, m) {
				return "", "", false, usageErrorf("missing path after %s", m)
			}
		}
	}
	if !strings.HasPrefix(path, "/") {
		path = "/api/v2/" + path
	}
	return method, path, methodGiven, nil
}

func (a *App) apiBody(opts apiOptions) ([]byte, error) {
	hasFields := len(opts.fields) > 0 || len(opts.rawFields) > 0
	switch {
	case opts.input != "" && hasFields:
		return nil, usageErrorf("--input cannot be combined with --field or --raw-field")
	case opts.input == "-":
		return io.ReadAll(a.reader())
	case opts.input != "":
		return os.ReadFile(opts.input)
	case hasFields:
		obj, err := buildFields(opts.fields, opts.rawFields)
		if err != nil {
			return nil, err
		}
		return json.Marshal(obj)
	}
	return nil, nil
}

func (a *App) expandPlaceholders(ctx context.Context, s *session, path string) (string, error) {
	needsID := strings.Contains(path, "{installation}")
	needsClientID := strings.Contains(path, "{client_id}")
	if !needsID && !needsClientID {
		return path, nil
	}
	inst, err := a.installation(ctx, s)
	if err != nil {
		return "", err
	}
	if needsClientID && inst.ClientID == 0 {
		// A numeric -i gives only the ID; look up the client ID.
		if inst, err = a.resolveInstallation(ctx, s, fmt.Sprint(inst.ID)); err != nil {
			return "", err
		}
	}
	path = strings.ReplaceAll(path, "{installation}", fmt.Sprint(inst.ID))
	path = strings.ReplaceAll(path, "{client_id}", fmt.Sprint(inst.ClientID))
	return path, nil
}

// buildFields turns key=value pairs into a (possibly nested) JSON object.
func buildFields(typed, raw []string) (map[string]any, error) {
	obj := map[string]any{}
	add := func(field string, parse func(string) any) error {
		key, value, ok := strings.Cut(field, "=")
		if !ok || key == "" {
			return usageErrorf("invalid field %q, expected key=value", field)
		}
		return setNested(obj, strings.Split(key, "."), parse(value), key)
	}
	for _, f := range typed {
		if err := add(f, typedValue); err != nil {
			return nil, err
		}
	}
	for _, f := range raw {
		if err := add(f, func(s string) any { return s }); err != nil {
			return nil, err
		}
	}
	return obj, nil
}

func setNested(obj map[string]any, path []string, value any, key string) error {
	for _, part := range path[:len(path)-1] {
		next, exists := obj[part]
		if !exists {
			child := map[string]any{}
			obj[part] = child
			obj = child
			continue
		}
		child, ok := next.(map[string]any)
		if !ok {
			return usageErrorf("field %q conflicts with an earlier field", key)
		}
		obj = child
	}
	obj[path[len(path)-1]] = value
	return nil
}

// typedValue interprets numbers, booleans, null, arrays and objects as JSON;
// anything else is a string.
func typedValue(s string) any {
	trimmed := strings.TrimSpace(s)
	switch trimmed {
	case "true":
		return true
	case "false":
		return false
	case "null":
		return nil
	}
	if trimmed != "" && json.Valid([]byte(trimmed)) && strings.ContainsAny(trimmed[:1], "-0123456789[{") {
		return json.RawMessage(trimmed)
	}
	return s
}
