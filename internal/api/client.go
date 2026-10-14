// SPDX-License-Identifier: GPL-3.0-or-later

// Package api is a small client for the IoT Open REST API.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to one IoT Open server.
type Client struct {
	BaseURL    string
	APIKey     string
	UserAgent  string
	HTTPClient *http.Client
	// Debug, when set, receives a dump of every request and response with
	// secrets redacted.
	Debug io.Writer
}

// NormalizeURL turns user input such as "lynx-dev.iotopen.se/" into
// "https://lynx-dev.iotopen.se".
func NormalizeURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("empty server URL")
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("invalid server URL %q", raw)
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// NewRequest builds a request for path (which must start with "/") with
// authentication and standard headers set.
func (c *Client) NewRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if c.APIKey != "" {
		req.Header.Set("X-API-Key", c.APIKey)
	}
	return req, nil
}

// Do sends a request. It does not inspect the status code.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	hc := c.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	if c.Debug != nil {
		dumpRequest(c.Debug, req)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	if c.Debug != nil {
		dumpResponse(c.Debug, resp)
	}
	return resp, nil
}

// call sends in (if not nil) as JSON and decodes the response into out (if
// not nil). Non-2xx responses are returned as *Error.
func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	req, err := c.jsonRequest(ctx, method, path, in)
	if err != nil {
		return err
	}
	return c.send(req, out)
}

func (c *Client) jsonRequest(ctx context.Context, method, path string, in any) (*http.Request, error) {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}
	return c.NewRequest(ctx, method, path, body)
}

func (c *Client) send(req *http.Request, out any) error {
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return NewError(resp, data)
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("unexpected response from %s %s: %w", req.Method, req.URL.Path, err)
	}
	return nil
}
