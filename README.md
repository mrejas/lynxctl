# iotopenctl

A command line interface for the [IoT Open](https://iotopen.se) platform,
formerly known as Lynx. It is also installed as `lynxctl`.

> Work in progress. See [docs/DESIGN.md](docs/DESIGN.md) for the design and
> roadmap.

## Build

Requires Go 1.26 or later.

```sh
go build -o iotopenctl ./cmd/iotopenctl
ln -s iotopenctl lynxctl    # optional alias
```

## Quick start

```sh
iotopenctl login                    # https://lynx.iotopen.se, prompts for password and 2FA code
iotopenctl use "main street"        # select an installation by ID, client ID or (part of) its name
iotopenctl whoami
iotopenctl function list
iotopenctl function get kitchen             # unique part of the name
iotopenctl function meta set kitchen unit=°C
iotopenctl device functions "hall sensor"
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

Every command has `--help` with examples. Shell completion is available with
`iotopenctl completion bash|zsh|fish|powershell`.

## Exit codes

| Code | Meaning                              |
|------|--------------------------------------|
| 0    | Success                              |
| 1    | Error (API, network, ...)            |
| 2    | Usage error (bad flags or arguments) |
| 3    | Reference not found or ambiguous     |
| 4    | Authentication required or failed    |
