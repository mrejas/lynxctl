# iotopenctl

A command line interface for the [IoT Open](https://iotopen.se) platform,
formerly known as Lynx. It is also installed as `lynxctl`.

> Work in progress. See [docs/DESIGN.md](docs/DESIGN.md) for the design and
> roadmap.

## Install

Releases provide `.deb`, `.rpm` and `.apk` packages, which install both
`iotopenctl` and `lynxctl` with shell completion, a `.zip` for Windows with
both executables, and `.tar.gz` archives for Linux and macOS.

From a tarball:

```sh
tar xzf iotopenctl_*_linux_amd64.tar.gz
sudo install iotopenctl /usr/local/bin/
sudo ln -s iotopenctl /usr/local/bin/lynxctl    # optional alias
```

## Build

Requires Go 1.26 or later.

```sh
go build -o iotopenctl ./cmd/iotopenctl
ln -s iotopenctl lynxctl    # optional alias
```

Packages are built with [GoReleaser](https://goreleaser.com/):

```sh
goreleaser release --snapshot --clean    # local build into dist/, nothing is published
```

## Shell completion

Packages install completion automatically. Otherwise, for the current shell:

```sh
source <(iotopenctl completion bash)             # bash
iotopenctl completion zsh > "${fpath[1]}/_iotopenctl"   # zsh
iotopenctl completion fish | source              # fish
iotopenctl completion powershell | Out-String | Invoke-Expression   # PowerShell
```

Names of installations, devices and functions are completed from the API.

## Quick start

```sh
iotopenctl login                    # https://lynx.iotopen.se, prompts for password and 2FA code
iotopenctl use "main street"        # select an installation by ID, client ID or (part of) its name
iotopenctl whoami
iotopenctl function list
iotopenctl function get kitchen             # unique part of the name
iotopenctl function meta set kitchen unit=°C
iotopenctl device functions "hall sensor"
iotopenctl get kitchen                      # latest value
iotopenctl set "hall lamp" on
iotopenctl watch                            # live values
iotopenctl log kitchen --since 7d --aggr avg --interval 1h
iotopenctl api 'functionx/{installation}?type=switch'
```

You can be logged in to several servers at once. Each login is a profile,
named after the server, and the last one you logged in to is the current
one. Profiles can be referred to by any unique part of their name:

```sh
iotopenctl login lynx-dev.iotopen.se
iotopenctl profile list
iotopenctl --profile lynx.io whoami     # one-off
iotopenctl profile use dev              # switch
iotopenctl logout --all
```

For scripts and CI, use an API key from the environment instead of logging in:

```sh
export IOTOPEN_URL=https://lynx.iotopen.se
export IOTOPEN_API_KEY=...
iotopenctl -i 42 api 'devicex/{installation}' | jq .
```

Every command has `--help` with examples.

## Exit codes

| Code | Meaning                              |
|------|--------------------------------------|
| 0    | Success                              |
| 1    | Error (API, network, ...)            |
| 2    | Usage error (bad flags or arguments) |
| 3    | Reference not found or ambiguous     |
| 4    | Authentication required or failed    |

## License

Licensed under the GNU General Public License, version 3 or (at your
option) any later version. See [LICENSE](LICENSE).
