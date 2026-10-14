package api

import (
	"context"
	"net/http"
)

// User is a user account.
type User struct {
	ID                    int64             `json:"id"`
	Email                 string            `json:"email"`
	FirstName             string            `json:"first_name"`
	LastName              string            `json:"last_name"`
	Role                  int64             `json:"role"`
	SMSLogin              bool              `json:"sms_login"`
	Mobile                string            `json:"mobile,omitempty"`
	Note                  string            `json:"note,omitempty"`
	Organisations         []int64           `json:"organisations"`
	AssignedInstallations []int64           `json:"assigned_installations,omitempty"`
	Address               *Address          `json:"address,omitempty"`
	Meta                  map[string]string `json:"meta,omitempty"`
	ProtectedMeta         map[string]string `json:"protected_meta,omitempty"`
	ExpireAt              int64             `json:"expire_at"`
}

// Address is a postal address.
type Address struct {
	Address string `json:"address"`
	City    string `json:"city"`
	Country string `json:"country"`
	Zip     string `json:"zip"`
}

// FullName returns "First Last", or an empty string if neither is set.
func (u *User) FullName() string {
	switch {
	case u.FirstName == "":
		return u.LastName
	case u.LastName == "":
		return u.FirstName
	}
	return u.FirstName + " " + u.LastName
}

// InstallationInfo is the summary of an installation that the user can access.
type InstallationInfo struct {
	ID             int64    `json:"id"`
	Name           string   `json:"name"`
	ClientID       int64    `json:"client_id"`
	OrganizationID int64    `json:"organization_id"`
	Capabilities   []string `json:"capabilities"`
	Assigned       bool     `json:"assigned"`
}

// Me returns the authenticated user.
func (c *Client) Me(ctx context.Context) (*User, error) {
	var u User
	if err := c.call(ctx, http.MethodGet, "/api/v2/user/me", nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// InstallationInfos lists the installations the user can access.
func (c *Client) InstallationInfos(ctx context.Context) ([]InstallationInfo, error) {
	var list []InstallationInfo
	if err := c.call(ctx, http.MethodGet, "/api/v2/installationinfo", nil, &list); err != nil {
		return nil, err
	}
	return list, nil
}
