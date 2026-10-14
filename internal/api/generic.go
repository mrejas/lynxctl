// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
)

// JSONObject is a JSON object whose schema the client does not model. Numbers
// are int64 when integral and float64 otherwise, so that they survive a
// round trip through YAML unchanged.
type JSONObject = map[string]any

// CallJSON sends in (if not nil) as JSON and returns the decoded response,
// or nil for an empty response.
func (c *Client) CallJSON(ctx context.Context, method, path string, in any) (any, error) {
	req, err := c.jsonRequest(ctx, method, path, in)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, NewError(resp, data)
	}
	return DecodeJSON(data)
}

// DecodeJSON decodes data like CallJSON does.
func DecodeJSON(data []byte) (any, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("unexpected response: %w", err)
	}
	return Normalize(v), nil
}

// Normalize replaces json.Number values with int64 or float64.
func Normalize(v any) any {
	switch v := v.(type) {
	case json.Number:
		if n, err := strconv.ParseInt(string(v), 10, 64); err == nil {
			return n
		}
		f, _ := v.Float64()
		return f
	case map[string]any:
		for k, x := range v {
			v[k] = Normalize(x)
		}
	case []any:
		for i, x := range v {
			v[i] = Normalize(x)
		}
	}
	return v
}

// ListObjectsAt returns the array at path as objects.
func (c *Client) ListObjectsAt(ctx context.Context, path string) ([]JSONObject, error) {
	v, err := c.CallJSON(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	items, _ := v.([]any)
	out := make([]JSONObject, 0, len(items))
	for _, it := range items {
		if o, ok := it.(map[string]any); ok {
			out = append(out, o)
		}
	}
	return out, nil
}

// GetObjectAt returns the object at path.
func (c *Client) GetObjectAt(ctx context.Context, method, path string, in any) (JSONObject, error) {
	v, err := c.CallJSON(ctx, method, path, in)
	if err != nil {
		return nil, err
	}
	o, _ := v.(map[string]any)
	return o, nil
}

// FilePart is one file in a multipart upload.
type FilePart struct {
	Field       string
	Filename    string
	ContentType string
	Data        []byte
}

// Upload sends files as multipart/form-data and returns the response body
// and status code. Non-2xx responses are returned as *Error.
func (c *Client) Upload(ctx context.Context, method, path string, parts []FilePart) ([]byte, int, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, p := range parts {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, p.Field, p.Filename))
		ct := p.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		h.Set("Content-Type", ct)
		pw, err := w.CreatePart(h)
		if err != nil {
			return nil, 0, err
		}
		if _, err := pw.Write(p.Data); err != nil {
			return nil, 0, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, 0, err
	}
	req, err := c.NewRequest(ctx, method, path, bytes.NewReader(body.Bytes()))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, resp.StatusCode, NewError(resp, data)
	}
	return data, resp.StatusCode, nil
}

// Download streams the body of a GET request to w.
func (c *Client) Download(ctx context.Context, path string, w io.Writer) error {
	req, err := c.NewRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	// Downloads are files, not JSON.
	req.Header.Set("Accept", "*/*")
	// Bypass the debug dump, which would read the whole file into memory.
	debug := c.Debug
	c.Debug = nil
	resp, err := c.Do(req)
	c.Debug = debug
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		data, _ := io.ReadAll(resp.Body)
		return NewError(resp, data)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}
