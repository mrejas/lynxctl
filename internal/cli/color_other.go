// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !windows

package cli

import "os"

func enableColor(*os.File) bool { return true }
