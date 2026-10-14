package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeURL(t *testing.T) {
	tests := map[string]string{
		"lynx.iotopen.se":               "https://lynx.iotopen.se",
		"https://lynx-dev.iotopen.se/":  "https://lynx-dev.iotopen.se",
		" http://localhost:8080 ":       "http://localhost:8080",
		"https://example.com/sub/path/": "https://example.com/sub/path",
	}
	for in, want := range tests {
		if got, err := NormalizeURL(in); err != nil || got != want {
			t.Errorf("NormalizeURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ftp://example.com", "https://"} {
		if _, err := NormalizeURL(bad); err == nil {
			t.Errorf("NormalizeURL(%q) succeeded", bad)
		}
	}
}

func TestErrorMessages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/unauthorized":
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"message":"Unauthorized"}`))
		case "/bad":
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"q is required"}`))
		}
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL}

	err := c.call(context.Background(), http.MethodGet, "/unauthorized", nil, nil)
	if !IsStatus(err, http.StatusUnauthorized) || err.Error() != "401 Unauthorized" {
		t.Errorf("401 error = %v", err)
	}
	err = c.call(context.Background(), http.MethodGet, "/bad", nil, nil)
	if err == nil || err.Error() != "400 Bad Request: q is required" {
		t.Errorf("400 error = %v", err)
	}
}

func TestDebugRedactsSecrets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"token": "super-secret-token"}`))
	}))
	defer srv.Close()
	var debug bytes.Buffer
	c := &Client{BaseURL: srv.URL, APIKey: "my-api-key", Debug: &debug}
	if _, err := c.Login(context.Background(), "jane", "hunter2", LoginRequest{DeviceID: "test"}); err != nil {
		t.Fatal(err)
	}
	out := debug.String()
	for _, secret := range []string{"super-secret-token", "my-api-key", "hunter2", "amFuZTpodW50ZXIy"} {
		if strings.Contains(out, secret) {
			t.Errorf("debug output contains secret %q:\n%s", secret, out)
		}
	}
	if !strings.Contains(out, "> POST "+srv.URL+"/api/v2/auth") || !strings.Contains(out, `"device_id":"test"`) {
		t.Errorf("debug output lacks request details:\n%s", out)
	}
}
