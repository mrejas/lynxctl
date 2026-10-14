// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/mrejas/lynx-cli/internal/api"
	"github.com/mrejas/lynx-cli/internal/resolve"
)

// Exit codes. See docs/DESIGN.md.
const (
	ExitOK       = 0
	ExitError    = 1
	ExitUsage    = 2
	ExitNotFound = 3
	ExitAuth     = 4
)

// exitError attaches an exit code to an error.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func usageError(err error) error {
	return &exitError{code: ExitUsage, err: err}
}

func usageErrorf(format string, args ...any) error {
	return usageError(fmt.Errorf(format, args...))
}

func authErrorf(format string, args ...any) error {
	return &exitError{code: ExitAuth, err: fmt.Errorf(format, args...)}
}

// exitCode maps an error to the process exit code.
func exitCode(err error) int {
	var ee *exitError
	var nf *resolve.NotFoundError
	var amb *resolve.AmbiguousError
	switch {
	case err == nil:
		return ExitOK
	case errors.As(err, &ee):
		return ee.code
	case errors.As(err, &nf), errors.As(err, &amb):
		return ExitNotFound
	case api.IsStatus(err, http.StatusUnauthorized):
		return ExitAuth
	case strings.HasPrefix(err.Error(), "unknown command"),
		strings.HasPrefix(err.Error(), "unknown flag"),
		strings.HasPrefix(err.Error(), "unknown shorthand flag"),
		strings.HasPrefix(err.Error(), "if any flags in the group"):
		// Errors produced by cobra itself before any command runs.
		return ExitUsage
	}
	return ExitError
}

// errorMessage formats err for the user, adding a hint where one helps.
func (a *App) errorMessage(err error) string {
	msg := err.Error()
	if api.IsStatus(err, http.StatusUnauthorized) {
		msg += fmt.Sprintf("\nYour session may have expired or the credentials are invalid. Run %q.", a.Name+" login")
	}
	return msg
}
