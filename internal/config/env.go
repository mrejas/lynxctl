package config

import "os"

// envPrefixes lists the accepted prefixes for environment variables, in order
// of precedence. LYNX_ is kept for those who still call the platform Lynx.
var envPrefixes = []string{"IOTOPEN_", "LYNX_"}

// Getenv returns the value of IOTOPEN_<name>, falling back to LYNX_<name>.
// Empty values are treated as unset.
func Getenv(name string) string {
	for _, prefix := range envPrefixes {
		if v := os.Getenv(prefix + name); v != "" {
			return v
		}
	}
	return ""
}
