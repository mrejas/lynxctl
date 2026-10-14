// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// Status is a reported value of a topic.
type Status struct {
	ClientID       int64   `json:"client_id"`
	InstallationID int64   `json:"installation_id"`
	Timestamp      float64 `json:"timestamp"`
	Value          float64 `json:"value"`
	Msg            string  `json:"msg,omitempty"`
	// Topic includes the client ID prefix, e.g. "1010/obj/zwave/12/temperature".
	Topic string `json:"topic"`
}

// Status returns the latest value of the given topics (with client ID
// prefix), or of all topics in the installation if none are given.
func (c *Client) Status(ctx context.Context, installationID int64, topics []string) ([]Status, error) {
	var list []Status
	path := fmt.Sprintf("/api/v2/status/%d", installationID)
	var err error
	if len(topics) == 0 {
		err = c.call(ctx, http.MethodGet, path, nil, &list)
	} else {
		// The POST variant takes the topics in the body, which avoids long URLs.
		err = c.call(ctx, http.MethodPost, path, topics, &list)
	}
	return list, err
}

// LogQuery selects entries from the value log.
type LogQuery struct {
	Topics []string
	From   int64 // Unix time; zero means the server default (24 hours ago)
	To     int64 // Unix time; zero means now
	Limit  int   // zero means the server default (10 000)
	Offset int
	// Order is "asc" or "desc" (the server default).
	Order string
	// AggrMethod and AggrInterval are only sent when set.
	AggrMethod   string
	AggrInterval string
}

// LogPage is one page of log entries.
type LogPage struct {
	Count int      `json:"count"`
	Total int      `json:"total"`
	Last  float64  `json:"last"`
	Data  []Status `json:"data"`
}

// Log reads the value log (API v3).
func (c *Client) Log(ctx context.Context, installationID int64, q LogQuery) (*LogPage, error) {
	v := url.Values{}
	for _, t := range q.Topics {
		v.Add("topics", t)
	}
	setInt := func(name string, n int64) {
		if n != 0 {
			v.Set(name, strconv.FormatInt(n, 10))
		}
	}
	setInt("from", q.From)
	setInt("to", q.To)
	setInt("limit", int64(q.Limit))
	setInt("offset", int64(q.Offset))
	if q.Order != "" {
		v.Set("order", q.Order)
	}
	if q.AggrMethod != "" {
		v.Set("aggr_method", q.AggrMethod)
	}
	if q.AggrInterval != "" {
		v.Set("aggr_interval", q.AggrInterval)
	}
	var page LogPage
	path := withQuery(fmt.Sprintf("/api/v3beta/log/%d", installationID), v)
	if err := c.call(ctx, http.MethodGet, path, nil, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

// Publish is a message to publish on the installation's MQTT broker.
type Publish struct {
	// Topic is relative to the installation, e.g. "set/obj/zwave/12/switch".
	Topic    string `json:"topic"`
	QoS      int    `json:"qos,omitempty"`
	Retained bool   `json:"retained,omitempty"`
	Payload  any    `json:"payload"`
}

// Publish sends a message via the REST API, so no MQTT connection is needed.
func (c *Client) Publish(ctx context.Context, installationID int64, msg Publish) error {
	return c.call(ctx, http.MethodPost, fmt.Sprintf("/api/v2/mqtt/publish/%d", installationID), msg, nil)
}
