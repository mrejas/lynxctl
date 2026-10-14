package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Error is a non-2xx response from the API.
type Error struct {
	StatusCode int
	Message    string
	Body       []byte
}

// NewError builds an Error from a response and its already read body.
func NewError(resp *http.Response, body []byte) *Error {
	e := &Error{StatusCode: resp.StatusCode, Body: body}
	var msg struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(body, &msg) == nil {
		e.Message = msg.Message
		if e.Message == "" {
			e.Message = msg.Error
		}
	}
	return e
}

func (e *Error) Error() string {
	status := fmt.Sprintf("%d %s", e.StatusCode, http.StatusText(e.StatusCode))
	if e.Message == "" || strings.EqualFold(e.Message, http.StatusText(e.StatusCode)) {
		return status
	}
	return status + ": " + e.Message
}

// IsStatus reports whether err is an API error with the given status code.
func IsStatus(err error, code int) bool {
	var e *Error
	return errors.As(err, &e) && e.StatusCode == code
}
