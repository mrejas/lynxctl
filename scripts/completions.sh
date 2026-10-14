#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Generates shell completion scripts for both names of the program into
# completions/. The scripts differ only in the program name, which the
# program takes from how it is invoked, so it is built once and run under
# both names.
set -eu

out=completions
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

go build -o "$tmp/iotopenctl" ./cmd/iotopenctl
cp "$tmp/iotopenctl" "$tmp/lynxctl"

rm -rf "$out"
mkdir -p "$out"
for name in iotopenctl lynxctl; do
	"$tmp/$name" completion bash >"$out/$name.bash"
	"$tmp/$name" completion zsh >"$out/_$name"
	"$tmp/$name" completion fish >"$out/$name.fish"
	"$tmp/$name" completion powershell >"$out/$name.ps1"
done
