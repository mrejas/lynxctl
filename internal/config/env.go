// SPDX-License-Identifier: GPL-3.0-or-later

package config

import "os"

// envPrefixes lists the accepted prefixes for environment variables, in order
// of precedence. IOTOPEN_ goes with the iotopenctl alias.
var envPrefixes = []string{"LYNX_", "IOTOPEN_"}

// Getenv returns the value of LYNX_<name>, falling back to IOTOPEN_<name>.
// Empty values are treated as unset.
func Getenv(name string) string {
	for _, prefix := range envPrefixes {
		if v := os.Getenv(prefix + name); v != "" {
			return v
		}
	}
	return ""
}
