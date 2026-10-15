// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package cli

import (
	"os"

	"golang.org/x/sys/windows"
)

// enableColor turns on ANSI escape sequences in the Windows console. Old
// consoles that cannot do it get no colors.
func enableColor(f *os.File) bool {
	h := windows.Handle(f.Fd())
	var mode uint32
	if windows.GetConsoleMode(h, &mode) != nil {
		return false
	}
	return windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
