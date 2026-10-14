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
iotopenctl api 'functionx/{installation}?type=switch'
```

More servers are handled with profiles:

```sh
iotopenctl login --profile dev --url lynx-dev.iotopen.se
iotopenctl --profile dev whoami
iotopenctl profile use dev
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
