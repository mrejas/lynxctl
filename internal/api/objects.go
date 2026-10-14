// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// Meta is a key/value store attached to most objects. Values are strings in
// the API, but some servers return numbers, so any JSON scalar is accepted
// and converted to a string.
type Meta map[string]string

func (m *Meta) UnmarshalJSON(data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == nil {
		*m = nil
		return nil
	}
	out := make(Meta, len(raw))
	for k, v := range raw {
		switch v := v.(type) {
		case string:
			out[k] = v
		case nil:
			out[k] = ""
		case float64:
			out[k] = strconv.FormatFloat(v, 'f', -1, 64)
		default:
			b, _ := json.Marshal(v)
			out[k] = string(b)
		}
	}
	*m = out
	return nil
}

// ObjectKind selects between the two object types that share the same
// model and endpoints: DeviceX and FunctionX.
type ObjectKind string

const (
	DeviceX   ObjectKind = "devicex"
	FunctionX ObjectKind = "functionx"
)

// Object is a DeviceX or FunctionX.
type Object struct {
	ID             int64   `json:"id,omitempty"`
	InstallationID int64   `json:"installation_id"`
	Type           string  `json:"type"`
	Meta           Meta    `json:"meta"`
	ProtectedMeta  Meta    `json:"protected_meta,omitempty"`
	Created        float64 `json:"created,omitempty"`
	Updated        float64 `json:"updated,omitempty"`
}

// Name returns the "name" meta key, which is what users know objects by.
func (o *Object) Name() string {
	return o.Meta["name"]
}

// MetaValue is one meta key as returned and accepted by the meta endpoints.
type MetaValue struct {
	Value     string `json:"value"`
	Protected bool   `json:"protected"`
}

func objectsPath(kind ObjectKind, installationID int64) string {
	return fmt.Sprintf("/api/v2/%s/%d", kind, installationID)
}

// ObjectPath is the API path of one object; it is also the base of its meta
// endpoints.
func ObjectPath(kind ObjectKind, installationID, id int64) string {
	return fmt.Sprintf("%s/%d", objectsPath(kind, installationID), id)
}

func withQuery(path string, query url.Values) string {
	if len(query) == 0 {
		return path
	}
	return path + "?" + query.Encode()
}

// ListObjects lists devices or functions in an installation. filter holds
// meta filters (key=value, with * wildcards) and "type".
func (c *Client) ListObjects(ctx context.Context, kind ObjectKind, installationID int64, filter url.Values) ([]Object, error) {
	var list []Object
	err := c.call(ctx, http.MethodGet, withQuery(objectsPath(kind, installationID), filter), nil, &list)
	return list, err
}

func (c *Client) GetObject(ctx context.Context, kind ObjectKind, installationID, id int64) (*Object, error) {
	var o Object
	if err := c.call(ctx, http.MethodGet, ObjectPath(kind, installationID, id), nil, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

func (c *Client) CreateObject(ctx context.Context, kind ObjectKind, o *Object) (*Object, error) {
	var created Object
	if err := c.call(ctx, http.MethodPost, objectsPath(kind, o.InstallationID), o, &created); err != nil {
		return nil, err
	}
	return &created, nil
}

func (c *Client) UpdateObject(ctx context.Context, kind ObjectKind, o *Object) (*Object, error) {
	var updated Object
	if err := c.call(ctx, http.MethodPut, ObjectPath(kind, o.InstallationID, o.ID), o, &updated); err != nil {
		return nil, err
	}
	return &updated, nil
}

func (c *Client) DeleteObject(ctx context.Context, kind ObjectKind, installationID, id int64) error {
	return c.call(ctx, http.MethodDelete, ObjectPath(kind, installationID, id), nil, nil)
}

// Installation is a full installation object.
type Installation struct {
	ID             int64   `json:"id,omitempty"`
	Name           string  `json:"name"`
	ClientID       int64   `json:"client_id,omitempty"`
	Created        float64 `json:"created,omitempty"`
	OrganizationID int64   `json:"organization_id"`
	Notes          string  `json:"notes"`
	Users          []int64 `json:"users"`
	Meta           Meta    `json:"meta"`
}

// InstallationPath is the API path of an installation; it is also the base
// of its meta endpoints.
func InstallationPath(id int64) string {
	return fmt.Sprintf("/api/v2/installation/%d", id)
}

// ListInstallations lists installations, optionally filtered by meta.
func (c *Client) ListInstallations(ctx context.Context, filter url.Values) ([]Installation, error) {
	var list []Installation
	err := c.call(ctx, http.MethodGet, withQuery("/api/v2/installation", filter), nil, &list)
	return list, err
}

func (c *Client) GetInstallation(ctx context.Context, id int64) (*Installation, error) {
	var inst Installation
	if err := c.call(ctx, http.MethodGet, InstallationPath(id), nil, &inst); err != nil {
		return nil, err
	}
	return &inst, nil
}

func (c *Client) CreateInstallation(ctx context.Context, inst *Installation) (*Installation, error) {
	var created Installation
	if err := c.call(ctx, http.MethodPost, "/api/v2/installation", inst, &created); err != nil {
		return nil, err
	}
	return &created, nil
}

func (c *Client) UpdateInstallation(ctx context.Context, inst *Installation) (*Installation, error) {
	var updated Installation
	if err := c.call(ctx, http.MethodPut, InstallationPath(inst.ID), inst, &updated); err != nil {
		return nil, err
	}
	return &updated, nil
}

func (c *Client) DeleteInstallation(ctx context.Context, id int64) error {
	return c.call(ctx, http.MethodDelete, InstallationPath(id), nil, nil)
}

// GetMeta reads one meta key of the object at path (see ObjectPath and
// InstallationPath).
func (c *Client) GetMeta(ctx context.Context, path, key string) (*MetaValue, error) {
	var v MetaValue
	if err := c.call(ctx, http.MethodGet, path+"/meta/"+url.PathEscape(key), nil, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// SetMeta creates or updates one meta key.
func (c *Client) SetMeta(ctx context.Context, path, key string, v MetaValue) error {
	return c.call(ctx, http.MethodPut, path+"/meta/"+url.PathEscape(key)+"?create_missing=true", v, nil)
}

// DeleteMeta removes one meta key.
func (c *Client) DeleteMeta(ctx context.Context, path, key string) error {
	return c.call(ctx, http.MethodDelete, path+"/meta/"+url.PathEscape(key), nil, nil)
}
