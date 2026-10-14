// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
)

// maxDebugBody limits how much of a body is printed in debug output.
const maxDebugBody = 64 * 1024

var (
	redactedHeaders = map[string]bool{"Authorization": true, "X-Api-Key": true}
	secretFields    = regexp.MustCompile(`("(?:token|password|challenge)"\s*:\s*)"[^"]*"`)
)

func redactBody(body []byte) []byte {
	return secretFields.ReplaceAll(body, []byte(`$1"REDACTED"`))
}

func dumpHeaders(w io.Writer, prefix string, h http.Header) {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range h[k] {
			if redactedHeaders[http.CanonicalHeaderKey(k)] {
				v = "REDACTED"
			}
			fmt.Fprintf(w, "%s%s: %s\n", prefix, k, v)
		}
	}
}

func dumpBody(w io.Writer, prefix string, body []byte) {
	if len(body) == 0 {
		return
	}
	truncated := len(body) > maxDebugBody
	if truncated {
		body = body[:maxDebugBody]
	}
	for _, line := range bytes.Split(redactBody(body), []byte("\n")) {
		fmt.Fprintf(w, "%s%s\n", prefix, line)
	}
	if truncated {
		fmt.Fprintf(w, "%s... (truncated)\n", prefix)
	}
}

func dumpRequest(w io.Writer, req *http.Request) {
	fmt.Fprintf(w, "> %s %s\n", req.Method, req.URL)
	dumpHeaders(w, "> ", req.Header)
	if req.GetBody != nil {
		if body, err := req.GetBody(); err == nil {
			data, _ := io.ReadAll(body)
			body.Close()
			dumpBody(w, "> ", data)
		}
	}
}

func dumpResponse(w io.Writer, resp *http.Response) {
	fmt.Fprintf(w, "< %s\n", resp.Status)
	dumpHeaders(w, "< ", resp.Header)
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(data))
	if err != nil {
		fmt.Fprintf(w, "< (error reading body: %v)\n", err)
		return
	}
	dumpBody(w, "< ", data)
	fmt.Fprintln(w)
}
