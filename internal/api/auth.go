// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"net/http"
)

// LoginRequest is the body sent with a password login. The server uses it
// to label the session.
type LoginRequest struct {
	DeviceID   string `json:"device_id,omitempty"`
	AppVersion string `json:"app_version,omitempty"`
}

// LoginResponse is returned by a password login.
type LoginResponse struct {
	Token string `json:"token"`
	// NextStep is set when the user has two-factor authentication enabled.
	// The token is then only valid for the challenge call. The API docs
	// spell it "nextstep" but servers have been seen using "next_step", so
	// both are accepted.
	NextStep    string `json:"nextstep"`
	NextStepAlt string `json:"next_step"`
}

// NeedsChallenge reports whether a two-factor code must be sent before the
// token can be used.
func (r *LoginResponse) NeedsChallenge() bool {
	return r.NextStep != "" || r.NextStepAlt != ""
}

// Login performs a password login. The client's APIKey is not used.
func (c *Client) Login(ctx context.Context, username, password string, body LoginRequest) (*LoginResponse, error) {
	req, err := c.jsonRequest(ctx, http.MethodPost, "/api/v2/auth", body)
	if err != nil {
		return nil, err
	}
	req.Header.Del("X-API-Key")
	req.SetBasicAuth(username, password)
	var resp LoginResponse
	if err := c.send(req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Challenge sends a two-factor code. The client's APIKey must be the token
// returned by Login. It returns the final session token.
func (c *Client) Challenge(ctx context.Context, code string) (string, error) {
	var resp struct {
		Token string `json:"token"`
	}
	err := c.call(ctx, http.MethodPut, "/api/v2/auth", map[string]string{"challenge": code}, &resp)
	return resp.Token, err
}

// Logout invalidates the session token used by the client.
func (c *Client) Logout(ctx context.Context) error {
	return c.call(ctx, http.MethodDelete, "/api/v2/auth", nil, nil)
}
