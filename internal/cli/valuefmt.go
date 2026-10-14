package cli

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mrejas/lynx-cli/internal/api"
)

// parseDuration extends time.ParseDuration with days ("7d") and weeks
// ("2w"), and allows combinations such as "1d12h".
func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	var total time.Duration
	for _, unit := range []struct {
		suffix string
		d      time.Duration
	}{{"w", 7 * 24 * time.Hour}, {"d", 24 * time.Hour}} {
		if i := strings.Index(s, unit.suffix); i > 0 {
			n, err := strconv.Atoi(s[:i])
			if err != nil {
				return 0, fmt.Errorf("invalid duration %q", s)
			}
			total += time.Duration(n) * unit.d
			s = s[i+1:]
		}
	}
	if s == "" {
		return total, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q (use e.g. 90s, 15m, 2h, 7d)", s)
	}
	return total + d, nil
}

var timeLayouts = []string{
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02",
}

// parseTime accepts "now", a duration meaning "that long ago" ("2h", "7d"),
// RFC 3339, a local date or date and time, or a Unix timestamp.
func parseTime(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "now" {
		return now, nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0), nil
	}
	if d, err := parseDuration(strings.TrimPrefix(s, "-")); err == nil {
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	for _, layout := range timeLayouts {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time %q (use e.g. 2h, 7d, 2026-10-01, \"2026-10-01 12:00\" or RFC 3339)", s)
}

func formatTimestamp(ts float64) string {
	if ts == 0 {
		return ""
	}
	sec, frac := math.Modf(ts)
	return time.Unix(int64(sec), int64(frac*1e9)).Local().Format("2006-01-02 15:04:05")
}

func rfc3339(ts float64) string {
	if ts == 0 {
		return ""
	}
	sec, frac := math.Modf(ts)
	return time.Unix(int64(sec), int64(frac*1e9)).Format(time.RFC3339Nano)
}

func rawValue(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// formatValue presents a value as the IoT Open frontend does: with the
// C-style "format" meta key if set, otherwise followed by "unit".
func formatValue(v float64, meta api.Meta) string {
	if f := meta["format"]; f != "" {
		if out, ok := cFormat(f, v); ok {
			return out
		}
	}
	if u := meta["unit"]; u != "" {
		return rawValue(v) + " " + u
	}
	return rawValue(v)
}

var formatVerb = regexp.MustCompile(`%[-+ #0]*[0-9]*(?:\.[0-9]+)?[a-zA-Z%]`)

// cFormat applies a printf-style format with a single value. It reports
// false for formats it does not understand.
func cFormat(format string, v float64) (string, bool) {
	used, ok := false, true
	out := formatVerb.ReplaceAllStringFunc(format, func(verb string) string {
		switch verb[len(verb)-1] {
		case '%':
			return "%"
		case 'f', 'F', 'e', 'E', 'g', 'G':
			ok, used = ok && !used, true
			return fmt.Sprintf(verb, v)
		case 'd', 'i', 'u':
			ok, used = ok && !used, true
			return fmt.Sprintf(verb[:len(verb)-1]+"d", int64(math.Round(v)))
		case 's':
			ok, used = ok && !used, true
			return fmt.Sprintf(verb, rawValue(v))
		}
		ok = false
		return verb
	})
	return out, ok && used
}

// parseSetValue accepts numbers (with "." or "," as decimal separator) and
// on/off, true/false, yes/no.
func parseSetValue(s string) (float64, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "true", "yes":
		return 1, nil
	case "off", "false", "no":
		return 0, nil
	}
	v, err := strconv.ParseFloat(strings.Replace(strings.TrimSpace(s), ",", ".", 1), 64)
	if err != nil {
		return 0, usageErrorf("invalid value %q: use a number, on/off or true/false", s)
	}
	return v, nil
}
