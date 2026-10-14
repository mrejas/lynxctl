// SPDX-License-Identifier: GPL-3.0-or-later

// Command iotopenctl is a command line interface for the IoT Open platform.
// It is also shipped as lynxctl.
package main

import (
	"os"

	"github.com/mrejas/lynx-cli/internal/cli"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(cli.Run(os.Args, version, os.Stdin, os.Stdout, os.Stderr))
}
