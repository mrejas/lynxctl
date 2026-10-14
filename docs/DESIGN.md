# iotopenctl — Design

A command line interface for the IoT Open platform (formerly known as Lynx).
The guiding principle is that the CLI should feel **obvious**: the most
common tasks are the shortest to type, objects are referred to by name, and
nobody should need to look up an installation ID twice.

## Goals

- Intuitive first, complete second. Every API operation must be reachable
  (see `api` below), but only common workflows get dedicated commands.
- Developer and integrator workflows first: installations, devices,
  functions, metadata and values. Administration (organizations, users,
  roles) comes later.
- Equally pleasant for humans at a terminal and for scripts.
- Linux is a hard requirement. Windows should work equally well. macOS comes
  for free.

## Non-goals (for now)

- Full coverage of every admin endpoint with dedicated commands.
- An interactive TUI.

## Name

| Name         | Role                                         |
|--------------|----------------------------------------------|
| `iotopenctl` | Official name, used in docs and packages     |
| `lynxctl`    | Alias for those of us who still say Lynx     |

- No hyphenated variants. This follows the `*ctl` convention (`kubectl`,
  `systemctl`, `etcdctl`, ...). It also avoids the `tool-plugin` pattern,
  where `foo-bar` usually means "plugin `bar` for `foo`".
- Plain `lynx` is not an option because it collides with the Lynx text web
  browser.
- The program reads `argv[0]` (basename, `.exe` stripped). Help texts, error
  messages and shell completion use whichever name it was invoked as. Any
  name other than the two above falls back to `iotopenctl`.
- Both names share the same configuration, credentials and environment
  variables.
- Packaging:
  - Linux packages and tarballs install `lynxctl` as a symlink to
    `iotopenctl`.
  - On Windows, `lynxctl.exe` is shipped as a copy (or a shim, for
    scoop/winget). Creating symlinks there requires elevated rights.

## Technology

- **Language:** Go. It produces a single static binary, cross-compiles
  trivially and needs no runtime on the target machine.
- **Libraries:**
  - `github.com/spf13/cobra` for commands, help and shell completion (bash,
    zsh, fish, PowerShell).
  - `github.com/zalando/go-keyring` to store secrets in the OS keyring
    (Secret Service on Linux, Credential Manager on Windows, Keychain on
    macOS).
  - `github.com/eclipse/paho.mqtt.golang` for `watch`.
  - `gopkg.in/yaml.v3` for config and YAML output.
  - The official client `github.com/IoTOpen/go-lynx` will be evaluated. If
    it does not cover what we need (for example the `v3beta` log
    endpoints), we use a thin HTTP client of our own instead.
- **Releases:** GoReleaser. It builds `.deb`/`.rpm`, a `.tar.gz` for Linux
  and macOS and a `.zip` for Windows. Distribution through scoop and winget
  is a later step.
- **Module path:** `github.com/mrejas/lynx-cli`.

## Concepts

### Profiles

A profile is one login: a server URL plus credentials and a current
installation. Profiles replace the `.config` file of the old bash scripts.
Many people work with several platform instances, so you can be logged in
to any number of servers at once, as with `docker login`.

```text
iotopenctl login                            # lynx.iotopen.se -> profile "lynx.iotopen.se"
iotopenctl login lynx-dev.iotopen.se        # profile "lynx-dev.iotopen.se"
iotopenctl login --profile customer customer.example.com   # custom name
iotopenctl profile list
iotopenctl profile use dev                  # unique part of "lynx-dev.iotopen.se"
iotopenctl --profile dev installation list  # one-off override
iotopenctl login dev                        # log in again after the session expired
iotopenctl logout dev
iotopenctl logout --all
```

Rules:

- A profile is named after the server's host unless `--profile` is given.
  `--profile` is needed only for a shorter name or for a second account on
  the same server.
- `login <server>` reuses the profile that already uses that server. If
  several profiles use it, the current one wins; otherwise `--profile` is
  required.
- `login <name>`, where the argument has no dot, colon or slash, logs in to
  an existing profile again.
- `login` without arguments logs in to the current profile again, or to
  `https://lynx.iotopen.se` if there is none.
- The profile you log in to becomes the current profile. If that changes
  the current profile, `login` says so and shows how to switch back.
- Everywhere a profile is given (`--profile`, `IOTOPEN_PROFILE`,
  `profile use`, `logout`, ...), an exact name or a unique part of a name
  works. `profile delete` requires the exact name.

Configuration file (the directory can be overridden with
`IOTOPEN_CONFIG_DIR`):

- Linux: `$XDG_CONFIG_HOME/iotopenctl/config.yaml`, which defaults to
  `~/.config/iotopenctl/config.yaml`.
- macOS: `~/Library/Application Support/iotopenctl/config.yaml`.
- Windows: `%AppData%\iotopenctl\config.yaml`.

```yaml
current-profile: lynx.iotopen.se
profiles:
  lynx.iotopen.se:
    url: https://lynx.iotopen.se
    username: jane.doe@example.com
    auth: password            # or api-key
    installation:             # current installation, see "Current installation"
      id: 42
      name: Main Street 1     # cached for display
      client-id: 1042
  lynx-dev.iotopen.se:
    url: https://lynx-dev.iotopen.se
    username: jane.doe@example.com
    auth: api-key
```

Secrets (session tokens and API keys) are never written to `config.yaml`.
They are stored in the OS keyring, keyed by profile name. If no keyring is
available (headless Linux, containers), they go in `credentials.yaml` next
to the config file, created with mode `0600`. Setting `IOTOPEN_NO_KEYRING`
forces the file store.

### Authentication

| Method            | Command                    | Notes                                    |
|-------------------|----------------------------|------------------------------------------|
| Username/password | `iotopenctl login [server]` | Prompts for password and, if enabled, the 2FA code. `--password-stdin` for scripts. |
| API key           | `iotopenctl login --api-key [server]` | Prompts for the key, or reads it from stdin |
| Environment       | `IOTOPEN_API_KEY=...`      | For CI and scripts. Nothing is stored.   |

- `login` with username and password calls `POST /api/v2/auth` using basic
  auth. If the response asks for a challenge, the user is prompted for the
  2FA code, which is sent with `PUT /api/v2/auth`. The resulting token is
  then stored for the profile.
- Logging in again to a profile invalidates its previous session token.
- `logout` calls `DELETE /api/v2/auth` and removes the stored token. An API
  key is only removed locally, because calling `DELETE /api/v2/auth` with it
  would revoke it on the server. The profile itself is kept.
- `whoami` calls `GET /api/v2/user/me` and shows the user, profile and
  server.
- Passwords are never accepted as command line arguments, because those end
  up in shell history and process lists. Use the prompt or `--password-stdin`.

### Current installation

Almost every API call needs an installation ID. The CLI therefore keeps a
*current installation* per profile:

```text
iotopenctl use "Måbärsvägen"       # by ID, client_id or (sub)name
iotopenctl use                     # show the current installation
iotopenctl function list           # uses the current installation
iotopenctl function list -i 42     # override for one command
iotopenctl use --clear             # unset it
```

A numeric `-i` (or `IOTOPEN_INSTALLATION`) is always taken as an installation
ID, without an API call. Names are resolved like any other reference. `use`
also accepts a client ID.

Precedence for every setting, from highest to lowest: command line flag,
then environment variable, then profile config.

### Environment variables

| Variable               | Meaning                          |
|------------------------|----------------------------------|
| `IOTOPEN_PROFILE`      | Profile to use                   |
| `IOTOPEN_URL`          | Server URL                       |
| `IOTOPEN_API_KEY`      | API key (bypasses stored creds)  |
| `IOTOPEN_INSTALLATION` | Installation reference           |
| `IOTOPEN_CONFIG_DIR`   | Configuration directory          |
| `IOTOPEN_NO_KEYRING`   | Store secrets in a file, not the keyring |

Every variable is also accepted with the `LYNX_` prefix (for example
`LYNX_API_KEY`). If both are set, `IOTOPEN_` wins.

### Object references

Wherever a command takes an installation, device or function, the user can
give an ID or a name. A reference is resolved within the current
installation (or across all accessible installations, for installations
themselves), case-insensitively:

1. Exact ID, if the argument is numeric.
2. Exact name. The name is the `name` field for installations and the `name`
   meta key for devices and functions.
3. Unique substring of the name.

If more than one object matches, the command fails and lists the
candidates:

```text
$ iotopenctl set lampa 1
Error: "lampa" matches 3 functions:
  1042  Lampa hall
  1043  Lampa kök
  1051  Lampa altan
Use a more specific name or the ID.
```

Rules:

- `delete` accepts only an exact ID or exact name, never a substring. If
  another object is added later, a substring in an old script could
  otherwise start matching it.
- A name that looks like a number can be referenced with `--name <name>`.
  For example, `--name 21` matches a function named "21" instead of ID 21.

## Command overview

Commands follow a noun-verb structure, like `gh`, `docker` and `az`. The most
frequent operations, reading and writing values, are also available as
top-level verbs.

```text
iotopenctl
├── login | logout | whoami
├── profile    list | use | show | delete
├── use        [installation]
│
├── get        <function>                         # latest value
├── set        <function> <value>                 # write a value
├── watch      <function|--topic T>...            # live values over MQTT
├── log        <function>... [--since|--from|--to] [--aggr]
│
├── installation (inst, i)   list | get | create | edit | delete | meta
├── device       (dev, d)    list | get | create | edit | delete | meta | functions
├── function     (fn, f)     list | get | create | edit | delete | meta
├── schedule                 list | get | create | edit | delete
├── file                     list | get | upload | download | delete
├── edge-app                 list | get | ... (later)
├── notification             ... (later)
├── organization | user | role ... (later, admin)
│
├── search     <query>                            # global search
├── api        <METHOD> <path> [-f file] [--field k=v]
└── completion bash | zsh | fish | powershell
```

Every command has aliases for the common short forms: `ls` for `list`, and
`rm` for `delete`.

### Values: `get`, `set`, `watch`, `log`

These are the everyday commands and are kept as short as possible.

```text
iotopenctl get "temp kök"
iotopenctl set "lampa hall" 1
iotopenctl watch "temp kök" "temp ute"
iotopenctl watch --topic '+/obj/#'
iotopenctl log "temp kök" --since 24h --aggr 1h:avg
```

| Command | Implementation                                                        |
|---------|-----------------------------------------------------------------------|
| `get`   | Resolve the function, read meta `topic_read`, call `GET /api/v2/status/{installation}?topics=...` |
| `set`   | Resolve the function, read meta `topic_write`, call `POST /api/v2/mqtt/publish/{installation}` with `{"value": ...}`. No MQTT connection is needed. |
| `watch` | MQTT over TLS (port 8883). The token or API key is the password and the username is arbitrary. Subscribes to the function's `topic_read`. |
| `log`   | `GET /api/v3beta/log/{installation}` with `topics`, `from`, `to`, `limit`, `order`, `aggr_method`, `aggr_interval` |

To verify against the API:

- Whether `topic_read` and `topic_write` are relative topics (prefixed with
  `client_id` on the broker) or full topics.
- The exact request body of `/api/v2/mqtt/publish`.

Time arguments (`--since`, `--from`, `--to`) accept:

- Relative durations: `90s`, `15m`, `2h`, `7d`.
- RFC 3339 timestamps (`2026-10-01T12:00:00+02:00`).
- Plain dates (`2026-10-01`, interpreted in local time).
- Unix timestamps.

### CRUD and metadata

```text
iotopenctl function list --type switch --meta device_id=12
iotopenctl function list --device "sensor hall"        # shorthand for --meta device_id=<id>
iotopenctl function get "temp kök"
iotopenctl function create --type temperature --name "Temp kök" --meta topic_read=obj/zwave/12/temp
iotopenctl function create -f function.yaml            # or -f - for stdin
iotopenctl function edit "temp kök"                    # opens $EDITOR
iotopenctl function delete 1042

iotopenctl function meta get "temp kök"                # all keys
iotopenctl function meta get "temp kök" unit
iotopenctl function meta set "temp kök" unit=°C format="%.1f °C"
iotopenctl function meta unset "temp kök" format

iotopenctl device functions "sensor hall"
iotopenctl device delete 12 --cascade                  # also deletes the device's functions
```

- `--meta key=value` can be repeated, and values may contain `*` wildcards.
  This maps directly to the API's metadata filters.
- `meta set` uses `PUT .../meta/{key}?create_missing=true`, so setting a key
  works whether or not it already exists.
- `edit` downloads the object and opens it as YAML in `$VISUAL`, then
  `$EDITOR`, falling back to `vi` on Unix and `notepad` on Windows.
  Read-only fields are shown as comments. On save, the object is sent with
  `PUT`. If the file is invalid or the API rejects it, the editor is
  reopened with the error at the top. If nothing changed, nothing is sent.
- `create` and `edit` accept JSON and YAML.

### `api`: the escape hatch

Inspired by `gh api`. It makes every endpoint usable from day one, including
endpoints that have no dedicated command yet. It replaces `acurl.sh`.

```text
iotopenctl api user/me                                    # GET /api/v2/user/me
iotopenctl api POST 'schedule/{installation}' -f schedule.json
iotopenctl api PUT 'functionx/{installation}/1042/meta/unit?create_missing=true' -F value=°C
iotopenctl api GET /api/v3beta/log/{installation}         # absolute path
```

- `METHOD` defaults to `GET`, or to `POST` when a body is given.
- A path without a leading `/` is relative to `/api/v2/`.
- `{installation}` and `{client_id}` in the path are replaced with the ID and
  client ID of the current installation (or the one given with `-i`).
- The body comes from `-f/--input <file>` (`-` for stdin), or is built from
  `-F/--field key=value` (typed: numbers, booleans, null, arrays and objects
  are sent as JSON) and `--raw-field key=value` (always a string). Dots in
  keys create nested objects.
- `-H/--header 'Name: value'` adds headers, and `--include` prints the status
  line and response headers.
- Authentication, base URL and profile are handled as for every other
  command.
- The response body is printed as-is. JSON is pretty-printed when stdout is
  a terminal.

## Output

- The default output is a human-readable table with the most relevant
  columns.
- `-o wide` gives the table with more columns.
- `-o json`, `-o yaml` and `-o csv` give machine-readable output. JSON and
  YAML contain the full API objects.
- `-q` / `--quiet` prints only IDs, one per line, for piping:

  ```text
  iotopenctl fn ls --meta device_id=12 -q | xargs -n1 iotopenctl fn rm --yes
  ```

- Colors are used only when stdout is a terminal. `NO_COLOR` is respected.
- Progress and informational messages go to stderr, and data goes to
  stdout. That way, piping always gets clean data.

## Errors and safety

- Error messages say what to do next:
  - `Not logged in. Run "iotopenctl login [server]".`
  - `No installation selected. Run "iotopenctl use <installation>" or pass -i.`
- HTTP errors include the status code and the API's message. `--debug`
  prints the full requests and responses, with secrets redacted.
- Destructive commands (`delete`, `device delete --cascade`) ask for
  confirmation when stdin is a terminal. `--yes` skips the question. When
  stdin is not a terminal, `--yes` is required.
- Exit codes:

  | Code | Meaning                                     |
  |------|---------------------------------------------|
  | 0    | Success                                     |
  | 1    | Error (API, network, ...)                   |
  | 2    | Usage error (bad flags or arguments)        |
  | 3    | Reference not found or ambiguous            |
  | 4    | Authentication required or failed           |

## Shell completion

- Completion scripts are generated for bash, zsh, fish and PowerShell.
- Completion is dynamic: profile names, installation names, device and
  function names, meta keys and function types are fetched from the API.
  Lookups are cached briefly so pressing Tab stays fast.
- Packages install the completion scripts automatically where the platform
  supports it.

## Project layout

```text
cmd/iotopenctl/        main package (tiny: builds the root command and runs it)
internal/cli/          cobra commands, one file per noun
internal/api/          HTTP client, auth, typed API models
internal/mqtt/         MQTT client for watch
internal/config/       profiles, config file, keyring and credential storage
internal/resolve/      ID/name/substring resolution of references
internal/output/       table, json, yaml, csv and quiet printers
docs/                  design and user documentation
```

## Roadmap

1. **Foundation (done):** project skeleton, profiles and config, `login` (including
   2FA), `logout`, `whoami`, `api`, `use`, and output formats.
2. **Objects:**
   - `installation`, `device` and `function` with list, get, create, edit
     and delete
   - `meta`
   - `--meta` and `--type` filters
   - reference resolution
3. **Values:** `get`, `set`, `watch`, `log`.
4. **Polish:** dynamic completion, GoReleaser packaging (including the
   `lynxctl` alias), and installation through scoop and winget.
5. **More resources:** schedule, file, search, edge-app, notification.
6. **Admin:** organization, user, role, permissions.

## Future ideas

- Plugins: an executable named `iotopenctl-<name>` on `PATH` becomes
  available as `iotopenctl <name>` (as with git and kubectl).
- `iotopenctl apply -f dir/` for declarative setup of devices and functions.
- Export and import of an installation's devices and functions.
