// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mrejas/lynx-cli/internal/api"
	"github.com/mrejas/lynx-cli/internal/mqtt"
)

func TestParseDuration(t *testing.T) {
	tests := map[string]time.Duration{
		"90s":   90 * time.Second,
		"15m":   15 * time.Minute,
		"2h":    2 * time.Hour,
		"7d":    7 * 24 * time.Hour,
		"1d12h": 36 * time.Hour,
		"2w":    14 * 24 * time.Hour,
	}
	for in, want := range tests {
		if got, err := parseDuration(in); err != nil || got != want {
			t.Errorf("parseDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "x", "7x", "d"} {
		if _, err := parseDuration(bad); err == nil {
			t.Errorf("parseDuration(%q) succeeded", bad)
		}
	}
}

func TestParseTime(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.Local)
	tests := map[string]time.Time{
		"now":                       now,
		"2h":                        now.Add(-2 * time.Hour),
		"-7d":                       now.Add(-7 * 24 * time.Hour),
		"2026-10-01":                time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local),
		"2026-10-01 08:30":          time.Date(2026, 10, 1, 8, 30, 0, 0, time.Local),
		"2026-10-01T08:30:15":       time.Date(2026, 10, 1, 8, 30, 15, 0, time.Local),
		"2026-10-01T08:30:00Z":      time.Date(2026, 10, 1, 8, 30, 0, 0, time.UTC),
		"2026-10-01T08:30:00+02:00": time.Date(2026, 10, 1, 6, 30, 0, 0, time.UTC),
		"1760000000":                time.Unix(1760000000, 0),
	}
	for in, want := range tests {
		if got, err := parseTime(in, now); err != nil || !got.Equal(want) {
			t.Errorf("parseTime(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := parseTime("yesterday", now); err == nil {
		t.Error("parseTime(yesterday) succeeded")
	}
}

func TestFormatValue(t *testing.T) {
	tests := []struct {
		value float64
		meta  api.Meta
		want  string
	}{
		{21.456, nil, "21.456"},
		{21.456, api.Meta{"unit": "°C"}, "21.456 °C"},
		{21.456, api.Meta{"format": "%.1f °C", "unit": "ignored"}, "21.5 °C"},
		{55.6, api.Meta{"format": "%d %%"}, "56 %"},
		{3, api.Meta{"format": "%i W"}, "3 W"},
		{3, api.Meta{"format": "%5.2f"}, " 3.00"},
		{3, api.Meta{"format": "no verb", "unit": "W"}, "3 W"},   // unusable format
		{3, api.Meta{"format": "%f and %f", "unit": "W"}, "3 W"}, // two values
		{3, api.Meta{"format": "%x"}, "3"},
	}
	for _, tt := range tests {
		if got := formatValue(tt.value, tt.meta); got != tt.want {
			t.Errorf("formatValue(%v, %v) = %q, want %q", tt.value, tt.meta, got, tt.want)
		}
	}
}

func TestParseSetValue(t *testing.T) {
	tests := map[string]float64{"1": 1, "21.5": 21.5, "21,5": 21.5, "-3": -3, "on": 1, "OFF": 0, "true": 1, "no": 0}
	for in, want := range tests {
		if got, err := parseSetValue(in); err != nil || got != want {
			t.Errorf("parseSetValue(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := parseSetValue("bright"); exitCode(err) != ExitUsage {
		t.Errorf("parseSetValue(bright): %v", err)
	}
}

func TestFullTopic(t *testing.T) {
	for in, want := range map[string]string{
		"obj/zwave/12/temp":      "1010/obj/zwave/12/temp",
		"/obj/zwave/12/temp":     "1010/obj/zwave/12/temp",
		"1010/obj/zwave/12/temp": "1010/obj/zwave/12/temp",
	} {
		if got := fullTopic(1010, in); got != want {
			t.Errorf("fullTopic(%q) = %q, want %q", in, got, want)
		}
	}
}

// valueSetup creates two functions with logged values in installation 10
// (client ID 1010).
func valueSetup(t *testing.T) (*fakeServer, *testEnv) {
	t.Helper()
	srv, env := setup(t)
	createID(t, env, "", "function", "create", "--type", "temperature", "--name", "Kitchen temperature",
		"--meta", "topic_read=obj/zwave/12/temperature", "--meta", "format=%.1f °C")
	createID(t, env, "", "function", "create", "--type", "switch", "--name", "Hall lamp",
		"--meta", "topic_read=obj/zwave/13/switch", "--meta", "topic_write=set/obj/zwave/13/switch")
	createID(t, env, "", "function", "create", "--type", "virtual", "--name", "No topics")
	now := float64(time.Now().Unix())
	srv.addValue(10, "obj/zwave/12/temperature", 20.04, now-7200)
	srv.addValue(10, "obj/zwave/12/temperature", 21.46, now-60)
	srv.addValue(10, "obj/zwave/13/switch", 1, now-30)
	return srv, env
}

func TestGet(t *testing.T) {
	_, env := valueSetup(t)

	r := env.mustRun("", "get")
	for _, want := range []string{"NAME", "Kitchen temperature", "21.5 °C", "Hall lamp"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("get lacks %q:\n%s", want, r.stdout)
		}
	}
	if strings.Contains(r.stdout, "No topics") {
		t.Errorf("get lists a function without topic_read:\n%s", r.stdout)
	}
	if r := env.mustRun("", "get", "kitchen", "-q"); r.stdout != "21.46\n" {
		t.Errorf("get kitchen -q = %q", r.stdout)
	}
	r = env.mustRun("", "get", "kitchen", "lamp", "-o", "json")
	var views []valueView
	if err := json.Unmarshal([]byte(r.stdout), &views); err != nil || len(views) != 2 ||
		views[0].Name != "Kitchen temperature" || *views[0].Value != 21.46 || views[0].Topic != "1010/obj/zwave/12/temperature" {
		t.Errorf("get -o json: %v\n%s", err, r.stdout)
	}
	if r := env.mustRun("", "get", "--topic", "obj/zwave/13/switch", "-q"); r.stdout != "1\n" {
		t.Errorf("get --topic -q = %q", r.stdout)
	}
	if r := env.run("", "get", "no topics"); r.code != ExitError || !strings.Contains(r.stderr, "no topic_read") {
		t.Errorf("get of a function without topic: exit %d, stderr %s", r.code, r.stderr)
	}
}

func TestSet(t *testing.T) {
	srv, env := valueSetup(t)

	r := env.mustRun("", "set", "lamp", "off")
	if !strings.Contains(r.stderr, `Set function "Hall lamp"`) {
		t.Errorf("stderr: %s", r.stderr)
	}
	env.mustRun("", "set", "--topic", "1010/set/obj/x", "21,5", "--msg", "from test")
	if len(srv.published) != 2 {
		t.Fatalf("published %v", srv.published)
	}
	p := srv.published[0]
	if p.Topic != "set/obj/zwave/13/switch" || p.Payload.(map[string]any)["value"] != 0.0 {
		t.Errorf("first publish = %+v", p)
	}
	p = srv.published[1]
	payload := p.Payload.(map[string]any)
	if p.Topic != "set/obj/x" || payload["value"] != 21.5 || payload["msg"] != "from test" {
		t.Errorf("second publish = %+v (the client ID prefix should be stripped)", p)
	}

	if r := env.run("", "set", "kitchen", "1"); r.code != ExitError || !strings.Contains(r.stderr, "no topic_write") {
		t.Errorf("set without topic_write: exit %d, stderr %s", r.code, r.stderr)
	}
	if r := env.run("", "set", "lamp", "bright"); r.code != ExitUsage {
		t.Errorf("invalid value: exit %d", r.code)
	}
	if r := env.run("", "set", "lamp"); r.code != ExitUsage {
		t.Errorf("missing value: exit %d", r.code)
	}
}

func TestLog(t *testing.T) {
	srv, env := valueSetup(t)

	r := env.mustRun("", "log", "kitchen")
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if len(lines) != 3 || !strings.Contains(lines[1], "20.0 °C") || !strings.Contains(lines[2], "21.5 °C") {
		t.Errorf("log kitchen should list two values oldest first:\n%s", r.stdout)
	}
	if strings.Contains(lines[0], "NAME") {
		t.Errorf("log of one function should not have a name column:\n%s", r.stdout)
	}
	q, _ := url.ParseQuery(srv.lastLog)
	if q.Get("topics") != "1010/obj/zwave/12/temperature" || q.Get("order") != "desc" {
		t.Errorf("log query = %s", srv.lastLog)
	}

	r = env.mustRun("", "log")
	if !strings.Contains(r.stdout, "NAME") || !strings.Contains(r.stdout, "Hall lamp") {
		t.Errorf("log of everything:\n%s", r.stdout)
	}

	r = env.mustRun("", "log", "kitchen", "--since", "1h", "-q")
	if r.stdout != "21.46\n" {
		t.Errorf("log --since 1h -q = %q", r.stdout)
	}

	r = env.mustRun("", "log", "kitchen", "--limit", "1", "-q")
	if r.stdout != "21.46\n" || !strings.Contains(r.stderr, "Showing the latest 1 of 2 values") {
		t.Errorf("log --limit 1: stdout %q, stderr %q", r.stdout, r.stderr)
	}

	env.mustRun("", "log", "kitchen", "--from", "2026-10-01", "--to", "2026-10-02 12:00", "--aggr", "avg", "--interval", "1h")
	q, _ = url.ParseQuery(srv.lastLog)
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local).Unix()
	if q.Get("aggr_method") != "avg" || q.Get("aggr_interval") != "1h" || q.Get("from") != strconv.FormatInt(from, 10) {
		t.Errorf("log query = %s", srv.lastLog)
	}

	for _, args := range [][]string{
		{"log", "--since", "1h", "--from", "2026-10-01"},
		{"log", "--from", "2026-10-02", "--to", "2026-10-01"},
		{"log", "--aggr", "average"},
		{"log", "--interval", "1 hour"},
		{"log", "--since", "yesterday"},
	} {
		if r := env.run("", args...); r.code != ExitUsage {
			t.Errorf("%v: exit %d, stderr %s", args, r.code, r.stderr)
		}
	}
}

func TestWatch(t *testing.T) {
	_, env := valueSetup(t)
	var gotOpts mqtt.Options
	var gotTopics []string
	env.subscribe = func(ctx context.Context, opts mqtt.Options, topics []string, handle func(mqtt.Message)) error {
		gotOpts, gotTopics = opts, topics
		handle(mqtt.Message{Topic: "1010/obj/zwave/12/temperature", Payload: []byte(`{"value":22.25,"timestamp":1760000000.5}`)})
		handle(mqtt.Message{Topic: "1010/obj/zwave/13/switch", Payload: []byte(`{"value":0,"msg":"manual"}`)})
		handle(mqtt.Message{Topic: "1010/obj/other", Payload: []byte(`not json`)})
		handle(mqtt.Message{Topic: "1010/obj/zwave/12/temperature", Payload: []byte(`{"value":99}`)})
		<-ctx.Done()
		return nil
	}

	r := env.mustRun("", "watch", "--count", "3")
	if len(gotTopics) != 1 || gotTopics[0] != "1010/obj/#" {
		t.Errorf("subscribed to %v", gotTopics)
	}
	if gotOpts.Broker != "127.0.0.1" || gotOpts.Password != "token-jane" {
		t.Errorf("options = %+v", gotOpts)
	}
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "Kitchen temperature  22.2 °C") ||
		!strings.Contains(lines[1], "Hall lamp            0  (manual)") || !strings.Contains(lines[2], "not json") {
		t.Errorf("watch output:\n%s", r.stdout)
	}
	if strings.Contains(r.stdout, "99") {
		t.Errorf("--count 3 printed a fourth value:\n%s", r.stdout)
	}

	r = env.mustRun("", "watch", "kitchen", "--count", "1", "-o", "json", "--broker", "tcp://localhost:1883")
	var v valueView
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || v.Name != "Kitchen temperature" || *v.Value != 22.25 {
		t.Errorf("watch -o json: %v\n%s", err, r.stdout)
	}
	if gotTopics[0] != "1010/obj/zwave/12/temperature" || gotOpts.Broker != "tcp://localhost:1883" {
		t.Errorf("subscribed to %v on %s", gotTopics, gotOpts.Broker)
	}
	if r := env.run("", "watch", "-o", "yaml"); r.code != ExitUsage {
		t.Errorf("watch -o yaml: exit %d", r.code)
	}
}

func TestBrokerURL(t *testing.T) {
	for in, want := range map[string]string{
		"lynx.iotopen.se":       "ssl://lynx.iotopen.se:8883",
		"lynx.iotopen.se:1883":  "ssl://lynx.iotopen.se:1883",
		"tcp://localhost:1883":  "tcp://localhost:1883",
		"wss://lynx.iotopen.se": "wss://lynx.iotopen.se",
	} {
		if got := mqtt.BrokerURL(in); got != want {
			t.Errorf("BrokerURL(%q) = %q, want %q", in, got, want)
		}
	}
}
