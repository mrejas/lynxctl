# lynxctl

A command line interface for the [IoT Open](https://iotopen.se) platform,
formerly known as Lynx. It is also installed as `iotopenctl`.

> Up to v0.1.0 the program was called `iotopenctl`, with `lynxctl` as the
> alias. The configuration and stored credentials are moved to the new name
> automatically, and the `lynxctl` package replaces the `iotopenctl` package.

> Work in progress. See [docs/DESIGN.md](docs/DESIGN.md) for the design and
> roadmap.

## Install

Releases provide `.deb`, `.rpm` and `.apk` packages, which install both
`lynxctl` and `iotopenctl` with shell completion, a `.zip` for Windows with
both executables, and `.tar.gz` archives for Linux and macOS.

From a tarball:

```sh
tar xzf lynxctl_*_linux_amd64.tar.gz
sudo install lynxctl /usr/local/bin/
sudo ln -s lynxctl /usr/local/bin/iotopenctl    # optional alias
```

## Build

Requires Go 1.26 or later.

```sh
go build -o lynxctl ./cmd/lynxctl
ln -s lynxctl iotopenctl    # optional alias
```

Packages are built with [GoReleaser](https://goreleaser.com/):

```sh
goreleaser release --snapshot --clean    # local build into dist/, nothing is published
```

## Shell completion

Packages install completion automatically. Otherwise, for the current shell:

```sh
source <(lynxctl completion bash)             # bash
lynxctl completion zsh > "${fpath[1]}/_lynxctl"   # zsh
lynxctl completion fish | source              # fish
lynxctl completion powershell | Out-String | Invoke-Expression   # PowerShell
```

Names of installations, devices and functions are completed from the API.

## Quick start

```sh
lynxctl login                    # https://lynx.iotopen.se, prompts for password and 2FA code
lynxctl use "main street"        # select an installation by ID, client ID or (part of) its name
lynxctl whoami
lynxctl function list
lynxctl function get kitchen             # unique part of the name
lynxctl function meta set kitchen unit=°C
lynxctl device functions "hall sensor"
lynxctl get kitchen                      # latest value
lynxctl set "hall lamp" on
lynxctl watch                            # live values
lynxctl log kitchen --since 7d --aggr avg --interval 1h
lynxctl schedule create --function "hall lamp" --value on --cron "0 7 * * 1,2,3,4,5"
lynxctl file upload floorplan.png
lynxctl search kitchen
lynxctl api 'functionx/{installation}?type=switch'
```

For interactive work, `lynxctl shell` starts a shell where commands are
typed without the program name, with Tab completion, history and a prompt
that shows the current profile and installation:

```text
$ lynxctl shell
lynx.iotopen.se:Main Street 1> fn get kit<Tab>
lynx.iotopen.se:Main Street 1> fn get 'Kitchen temperature'
```

`lynxctl browse` (experimental) lays out organizations, installations,
devices and functions as directories, to explore with `cd`, `ls` and `cat`.

You can be logged in to several servers at once. Each login is a profile,
named after the server, and the last one you logged in to is the current
one. Profiles can be referred to by any unique part of their name:

```sh
lynxctl login lynx-dev.iotopen.se
lynxctl profile list
lynxctl --profile lynx.io whoami     # one-off
lynxctl profile use dev              # switch
lynxctl logout --all
```

For scripts and CI, use an API key from the environment instead of logging in:

```sh
export LYNX_URL=https://lynx.iotopen.se
export LYNX_API_KEY=...
lynxctl -i 42 api 'devicex/{installation}' | jq .
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
