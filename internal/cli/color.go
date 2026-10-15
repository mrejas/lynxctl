// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"io"
	"os"
)

// colorOn reports whether output to w may use ANSI colors: w is a terminal
// that understands them, and NO_COLOR is not set (https://no-color.org).
func colorOn(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" || !isTerminal(w) {
		return false
	}
	return enableColor(w.(*os.File))
}

// colorize wraps s in an ANSI color if on is set and color is not empty.
func colorize(s, color string, on bool) string {
	if !on || color == "" {
		return s
	}
	return "\x1b[" + color + "m" + s + "\x1b[0m"
}
