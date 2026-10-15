# lynxctl — Design

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
- A full-screen TUI. The line-oriented `shell` covers interactive use.

## Name

| Name         | Role                                         |
|--------------|----------------------------------------------|
| `lynxctl`    | Official name, used in docs and packages     |
| `iotopenctl` | Alias after the platform's current name      |

- The platform was called Lynx for a long time, and the name lives on in
  its servers (`lynx.iotopen.se`) and among the people who built it. The
  tool keeps that name. Up to v0.1.0 the names were the other way around.

- No hyphenated variants. This follows the `*ctl` convention (`kubectl`,
  `systemctl`, `etcdctl`, ...). It also avoids the `tool-plugin` pattern,
  where `foo-bar` usually means "plugin `bar` for `foo`".
- Plain `lynx` is not an option because it collides with the Lynx text web
  browser.
- The program reads `argv[0]` (basename, `.exe` stripped). Help texts, error
  messages and shell completion use whichever name it was invoked as. Any
  name other than the two above falls back to `lynxctl`.
- Both names share the same configuration, credentials and environment
  variables.
- Packaging:
  - Linux packages and tarballs install `iotopenctl` as a symlink to
    `lynxctl`.
  - On Windows, `iotopenctl.exe` is shipped as a copy (or a shim, for
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
lynxctl login                            # lynx.iotopen.se -> profile "lynx.iotopen.se"
lynxctl login lynx-dev.iotopen.se        # profile "lynx-dev.iotopen.se"
lynxctl login --profile customer customer.example.com   # custom name
lynxctl profile list
lynxctl profile use dev                  # unique part of "lynx-dev.iotopen.se"
lynxctl --profile dev installation list  # one-off override
lynxctl login dev                        # log in again after the session expired
lynxctl logout dev
lynxctl logout --all
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
- Everywhere a profile is given (`--profile`, `LYNX_PROFILE`,
  `profile use`, `logout`, ...), an exact name or a unique part of a name
  works. `profile delete` requires the exact name.

Configuration file (the directory can be overridden with
`LYNX_CONFIG_DIR`):

- Linux: `$XDG_CONFIG_HOME/lynxctl/config.yaml`, which defaults to
  `~/.config/lynxctl/config.yaml`.
- macOS: `~/Library/Application Support/lynxctl/config.yaml`.
- Windows: `%AppData%\lynxctl\config.yaml`.

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
to the config file, created with mode `0600`. Setting `LYNX_NO_KEYRING`
forces the file store.

### Authentication

| Method            | Command                    | Notes                                    |
|-------------------|----------------------------|------------------------------------------|
| Username/password | `lynxctl login [server]` | Prompts for password and, if enabled, the 2FA code. `--password-stdin` for scripts. |
| API key           | `lynxctl login --api-key [server]` | Prompts for the key, or reads it from stdin |
| Environment       | `LYNX_API_KEY=...`      | For CI and scripts. Nothing is stored.   |

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
lynxctl use "Måbärsvägen"       # by ID, client_id or (sub)name
lynxctl use                     # show the current installation
lynxctl function list           # uses the current installation
lynxctl function list -i 42     # override for one command
lynxctl use --clear             # unset it
```

A numeric `-i` (or `LYNX_INSTALLATION`) is always taken as an installation
ID, without an API call. Names are resolved like any other reference. `use`
also accepts a client ID.

Precedence for every setting, from highest to lowest: command line flag,
then environment variable, then profile config.

### Environment variables

| Variable               | Meaning                          |
|------------------------|----------------------------------|
| `LYNX_PROFILE`      | Profile to use                   |
| `LYNX_URL`          | Server URL                       |
| `LYNX_API_KEY`      | API key (bypasses stored creds)  |
| `LYNX_INSTALLATION` | Installation reference           |
| `LYNX_BROKER`       | MQTT broker for `watch`          |
| `LYNX_CACHE_DIR`    | Cache directory for completion   |
| `LYNX_CONFIG_DIR`   | Configuration directory          |
| `LYNX_NO_KEYRING`   | Store secrets in a file, not the keyring |

Every variable is also accepted with the `IOTOPEN_` prefix (for example
`IOTOPEN_API_KEY`). If both are set, `LYNX_` wins.

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
$ lynxctl set lampa 1
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
- A name that looks like a number is still found: if no object has that ID,
  the reference is matched against names. Only when both an ID and a name
  match does the ID win.

## Command overview

Commands follow a noun-verb structure, like `gh`, `docker` and `az`. The most
frequent operations, reading and writing values, are also available as
top-level verbs.

```text
lynxctl
├── login | logout | whoami
├── profile    list | use | show | delete
├── use        [installation]
│
├── get        [function|--topic T]...            # latest value
├── set        <function|--topic T> <value>       # write a value
├── watch      [function|--topic T]...            # live values over MQTT
├── log        [function|--topic T]... [--since|--from|--to] [--aggr --interval]
│
├── installation (inst, i)   list | get | create | edit | delete | meta
├── device       (dev, d)    list | get | create | edit | delete | meta | functions
├── function     (fn, f)     list | get | create | edit | delete | meta
├── schedule     (sched)     list | get | create | edit | delete | enable | disable
├── file                     list | get | upload | replace | download | delete   [--org]
├── edge-app     (app)       list | get | create | edit | versions | upload | publish
│                            | download | options
│   └── instance             list | get | create | edit | delete
├── notification (notif)     send
│   ├── message              list | get | create | edit | delete
│   ├── output               list | get | create | edit | delete
│   └── executor             list | get
├── organization (org)       list | get | create | edit | delete | tree | meta
│                            | force-password-reset
├── user                     list | get | create | edit | delete | meta | password
│   └── token                list | create | delete            [--user, default: me]
├── role                     list | get | create | edit | delete
├── permission               list
│
├── search     [query] [--type T] [--meta k=v]    # global search
├── api        <METHOD> <path> [-f file] [--field k=v]
├── shell      [-e]                               # interactive shell
├── browse     [-e]                               # browse like a file system (experimental)
└── completion bash | zsh | fish | powershell
```

Every command has aliases for the common short forms: `ls` for `list`, and
`rm` for `delete`.

### Values: `get`, `set`, `watch`, `log`

These are the everyday commands and are kept as short as possible.

```text
lynxctl get                                  # all functions with their latest value
lynxctl get "temp kök" "temp ute"
lynxctl get "temp kök" -q                    # just the number
lynxctl set "lampa hall" on                  # numbers, on/off, true/false; "21,5" works too
lynxctl watch                                # everything under obj/# live
lynxctl watch "temp kök" --count 1 -q        # wait for the next value
lynxctl watch --topic 'obj/zwave/#'
lynxctl log "temp kök" --since 7d --aggr avg --interval 1h
lynxctl log "temp kök" --from 2026-10-01 --to 2026-10-02 -o csv
```

| Command | Implementation                                                        |
|---------|-----------------------------------------------------------------------|
| `get`   | Read meta `topic_read` of the functions, call `POST /api/v2/status/{installation}` with the topics (or `GET` for all). |
| `set`   | Read meta `topic_write`, call `POST /api/v2/mqtt/publish/{installation}` with `{"topic": ..., "payload": {"value": ...}}`. No MQTT connection is needed. |
| `watch` | MQTT over TLS on the server's host, port 8883 (override with `--broker` or `LYNX_BROKER`). The token or API key is the password; the username is not checked. |
| `log`   | `GET /api/v3beta/log/{installation}` with `topics`, `from`, `to`, `limit`, `order=desc`, and `aggr_method`/`aggr_interval` only when asked for. |

Topics:

- Status and log responses use full topics, prefixed with the
  installation's client ID (`1010/obj/zwave/12/temperature`). The publish
  endpoint takes topics relative to the installation
  (`obj/zwave/12/temperature`).
- `topic_read`/`topic_write` may be stored in either form; the CLI adds or
  strips the client ID prefix as each endpoint needs. `--topic` arguments are
  relative to the installation.

Presentation:

- Values are formatted like in the frontend: with the C-style `format` meta
  key (`%.1f °C`), else followed by `unit`. `-q` prints raw values, and
  JSON/YAML include both the raw and the formatted value.
- `log` fetches newest first (so `--limit` keeps the latest values) and
  shows them oldest first. `--oldest` fetches oldest first instead, so that
  `--limit` keeps the oldest values. It says so when more values match than were
  shown. The name column is left out when only one function is shown.
- `watch` prints one line per value. With `-o json` it prints one JSON
  object per line. `--count N` stops after N values.
- `get`, `log` and `watch` select functions by name, by `--meta` and
  `--type` (both with `*` wildcards), or both; duplicates are removed.
  `lynxctl get --meta device_id=12` shows all values of a device.
  A selection that matches no function with a `topic_read` is an error
  (exit code 3).
- `get` without arguments lists all functions that have a `topic_read`.
  Naming a function without one is an error, and so is `set` on a function
  without a `topic_write`.

Time arguments (`--since`, `--from`, `--to`) accept:

- Relative durations: `90s`, `15m`, `2h`, `7d`, `2w`, `1d12h`.
- RFC 3339 timestamps (`2026-10-01T12:00:00+02:00`).
- Local dates and times (`2026-10-01`, `"2026-10-01 12:00"`).
- Unix timestamps.

### CRUD and metadata

```text
lynxctl function list --type switch --meta device_id=12
lynxctl function list --device "sensor hall"        # shorthand for --meta device_id=<id>
lynxctl function get "temp kök"
lynxctl function create --type temperature --name "Temp kök" --meta topic_read=obj/zwave/12/temp
lynxctl function create -f function.yaml            # or -f - for stdin
lynxctl function edit "temp kök"                    # opens $EDITOR
lynxctl function delete 1042

lynxctl function meta get "temp kök"                # all keys
lynxctl function meta get "temp kök" unit
lynxctl function meta set "temp kök" unit=°C format="%.1f °C"
lynxctl function meta unset "temp kök" format

lynxctl device functions "sensor hall"
lynxctl device delete 12 --cascade                  # also deletes the device's functions

lynxctl installation get                            # the current installation
lynxctl installation edit summer
lynxctl installation create --name "Main Street 1"  # --org defaults to your only organization
lynxctl installation meta set customer=ACME         # current installation, or -i
```

- `--meta key=value` can be repeated, and values may contain `*` wildcards.
  A key alone (`--meta pinned`) means the key is set, with any value
  (`pinned=*`). For installations, devices, functions and users this maps
  directly to the API's metadata filters. Organizations cannot be filtered
  by the API, so `organization list --meta` filters locally: case-insensitive,
  with `*` matching any text, protected keys included.
- `meta set` uses `PUT .../meta/{key}?create_missing=true`, so setting a key
  works whether or not it already exists.
- `edit` downloads the object and opens it as YAML in `$VISUAL`, then
  `$EDITOR`, falling back to `vi` on Unix and `notepad` on Windows.
  Read-only fields are shown as comments. On save, the object is sent with
  `PUT`. If the file is invalid or the API rejects it, the editor is
  reopened with the error at the top. If nothing changed, nothing is sent.
- `edit` accepts only the editable fields (`type`, `meta`, `protected_meta`
  for devices and functions; `name`, `organization_id`, `notes`, `users`,
  `meta` for installations). Unknown fields are rejected, so typos do not
  disappear silently. Saving without changes cancels; after an error, it
  gives up and reports the error.
- `create` and `edit` accept JSON and YAML. `create -f file` can be combined
  with flags, which are applied on top of the file.
- `meta get <object> <key>` prints only the value, for use in scripts.
  `meta set` keeps a protected key protected unless `--protected=false` is
  given.
- Commands on installations default to the current installation; `meta`
  for installations uses the current installation or `-i`, so its arguments
  are always keys.
- `device delete` without `--cascade` mentions how many functions still
  refer to the device. Deleting the current installation also unselects it.
- Lists are sorted by ID. The function list shows the device and the read
  topic; `-o wide` adds the write topic and the update time.

### Schedules, files, edge apps, notifications and search

These share one implementation for list, get, create, edit and delete:
objects are handled as JSON without a fixed schema, so new fields in the
API work without changes to the CLI. On top of that, each resource has
flags for what is natural to type:

```text
lynxctl schedule create --function "hall lamp" --value on --cron "0 7 * * 1,2,3,4,5"
lynxctl schedule disable 1042

lynxctl file upload floorplan.png manual.pdf      # MIME type guessed
lynxctl file download floorplan                   # saved as ./floorplan.png
lynxctl file download config.json -O - | jq .
lynxctl file list --org 7                         # organization files

lynxctl edge-app create --name "Heating control" --category climate ...
lynxctl edge-app upload heating ./src             # app.json + app.lua, prints the hash
lynxctl edge-app publish heating 1.2.0            # names the newest upload
lynxctl edge-app instance create --app heating --config target=21

lynxctl notification message create --name Alarm --text '{{.payload.message}}'
lynxctl notification output create --name "Mail on-call" --executor mail --message alarm \
    --config recipients=oncall@example.com
lynxctl notification send "mail on-call" -F message="Water leak"

lynxctl search kitchen --type function
```

- `create` takes `-f file` (YAML or JSON), `-F key=value` (typed, dotted
  keys for nesting) and `--raw-field key=value`, like `api`, plus the
  resource's own flags. Flags are applied on top of the file.
- `edit` hides read-only fields (IDs, timestamps, the owning installation)
  and sends them back unchanged.
- References to executors, messages, apps and functions in flags accept
  names, like everywhere else.
- Schedules have no names and are referred to by ID. `--cron` takes the
  five crontab fields; the API's own fields can be set with `-F`.
- `file download` saves under the file's name and refuses to overwrite
  without `--force`; `-O` chooses another path (`-o` is the output format).
  `file replace` checks locally that the new content has the same MIME type,
  which the API requires.
- `edge-app download` writes to stdout only when it is redirected;
  otherwise `-O` is required. `download` and `options` default to the
  newest published version, and `publish` to the newest unpublished upload.
  `instance create` defaults the version to the newest published one and
  the name to the app's name.
- `search` without a query lists everything matching the filters. `--type`
  accepts singular and plural forms.

### Administration

```text
lynxctl org tree                                  # the organization hierarchy
lynxctl org create --name "ACME Malmö"            # --parent defaults to your organization
lynxctl org delete "ACME Malmö" --force           # also deletes everything in it

lynxctl user get                                  # yourself ("me")
lynxctl user create --email eve@example.com --first-name Eve --last-name Adams --role user
lynxctl user password                             # asks for the current and new password
lynxctl user token create --name ci --expires 90d # prints the token once
lynxctl user token create --name ci -q | lynxctl login --profile ci --api-key

lynxctl role create --name Integration --priority 5 --allow installation/list/all --deny 'functionx/+/delete'
lynxctl permission list --allowed
```

- Users are referred to by email address (or a unique part of it), ID, or
  `me`. `user get` and `user token` default to `me`.
- `--org` and `--parent` default to your organization when you belong to
  exactly one.
- `org delete --force` and `org force-password-reset` affect many users and
  objects, and say so in their confirmation question.
- `user password` asks for the new password twice. For your own account it
  also asks for the current one. `--password-stdin` reads the passwords
  from stdin instead, one per line.
- `user token delete` warns when the token is the one the CLI is using.
  Its alias is `revoke`.
- `permission list` uses `/api/v2/permission`; the API documentation spells
  it `perimission`, which the server does not answer to.

### `api`: the escape hatch

Inspired by `gh api`. It makes every endpoint usable from day one, including
endpoints that have no dedicated command yet. It replaces `acurl.sh`.

```text
lynxctl api user/me                                    # GET /api/v2/user/me
lynxctl api POST 'schedule/{installation}' -f schedule.json
lynxctl api PUT 'functionx/{installation}/1042/meta/unit?create_missing=true' -F value=°C
lynxctl api GET /api/v3beta/log/{installation}         # absolute path
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

### `shell`: interactive mode

`lynxctl shell` starts a shell where commands are typed without the
program name:

```text
$ lynxctl shell
lynx.iotopen.se:Main Street 1> fn ls --type switch
lynx.iotopen.se:Main Street 1> use "street 2"
lynx.iotopen.se:Main Street 2> watch hall        # Ctrl-C stops watch, not the shell
```

- The prompt shows the profile and the installation that commands will use.
- Tab completes commands, flags and names through the same machinery as the
  shell completion scripts (`__complete`), so completion behaves the same
  everywhere. Candidates are matched by prefix and, if none match, by
  substring, like references. Without a unique match, the longest common
  prefix is inserted, or the candidates are listed. File names are
  completed where a command takes a file.
- Completed words that need quoting (names with spaces) are quoted as a
  whole. While the word is not finished (a directory, a common prefix, a
  `key=`), the cursor stays before the closing quote, so that typing and
  Tab continue inside it; the line is always correctly quoted.
- Line editing and history come from `golang.org/x/term` (no extra
  dependency). The history is kept in the cache directory (`shell-history`,
  at most 1000 lines, mode 0600). Lines starting with a space are not saved.
- Ctrl-C clears the line, or stops the running command. Ctrl-D, `exit
  [code]` or `quit` leaves the shell.
- Words are split like in a POSIX shell, with '...', "..." and backslash
  escapes. A backslash is literal unless it escapes whitespace, a quote or a
  backslash, so Windows paths can be typed as they are. `#` starts a
  comment. A leading `lynxctl` or `iotopenctl` is ignored, so command lines
  can be pasted as they are.
- Every command runs as if invoked separately: fresh flags, and the
  configuration is reloaded, so `use` and `profile use` take effect at once
  (and also outside the shell). Global flags given to `shell` itself apply
  to every command and can be overridden per command.
- When stdin is not a terminal, commands are read one per line without
  prompts, so `lynxctl shell < script` runs a script. Commands that read
  stdin continue reading from the script. `-e` stops at the first failing
  command. The exit code is that of the last command, or the one given to
  `exit`.

### `browse`: the platform as a file system (experimental)

`lynxctl browse` is a shell like `shell`, where the platform is laid out
as directories:

```text
/                                   top organizations
/<org>/<org>/...                    sub-organizations and installations
/.../<installation>                 devices, and functions without a device
/.../<installation>/<device>        the device's functions
/.../<function>                     the function's topic_* meta keys (files)
```

- `cd [path]`, `ls [-l] [path]...`, `cat [-o format] [path]` and `pwd`.
  `cd` without a path and `~` go to the current installation, which is also
  where the browser starts. `cat` shows an object with the matching `get`
  command (so `-o` works).
- Values: `cat` on a topic shows its log with the defaults of `log` (the
  last 24 hours). `head [-n N]` and `tail [-n N] [-f]` show the oldest and
  latest N values (default 10) of a topic, function, device (its functions)
  or installation; `tail -f` then follows new values with `watch`. Other
  flags (`--since`, `-o`, `-q`, ...) are passed on to `log`. `head` uses
  `log --oldest`, which asks the server for `order=asc`.
- On a terminal, `ls` colors names by kind, as `ls --color` does:
  organizations bold magenta, installations bold blue, devices yellow,
  functions green, meta keys uncolored. The prompt colors each element of
  the path the same way (and `shell` colors the installation in its prompt).
  Directories always end with `/`, and
  `ls -l` names the kind. Symbols (emoji) were rejected: their width varies
  between terminals, which breaks alignment. Colors follow the general
  rule: only when stdout is a terminal and `NO_COLOR` is not set.
- Path elements are names, IDs or unique parts of names, resolved like
  other references. Names containing `/` must be given by ID, and Tab
  completes them as IDs.
- Organizations whose parent is not visible are shown at the top, and so
  are installations whose organization is not visible. Installations are
  taken from both `/installation` and `/installationinfo`. A function
  belongs to the device in its `device_id` meta key; functions without one,
  or with one that matches no device, are listed in the installation.
- Any other command is run as in `shell`, with `-i` set to the installation
  the current directory is in.
- What has been fetched is reused for a minute, and forgotten after any
  command that is passed on to the CLI.

## Output

- The default output is a human-readable table with the most relevant
  columns.
- `-o wide` gives the table with more columns.
- `-o json`, `-o yaml` and `-o csv` give machine-readable output. JSON and
  YAML contain the full API objects.
- `-q` / `--quiet` prints only IDs, one per line, for piping:

  ```text
  lynxctl fn ls --meta device_id=12 -q | xargs -n1 lynxctl fn rm --yes
  ```

- Colors are used only when stdout is a terminal. `NO_COLOR` is respected.
- Progress and informational messages go to stderr, and data goes to
  stdout. That way, piping always gets clean data.

## Errors and safety

- Error messages say what to do next:
  - `Not logged in. Run "lynxctl login [server]".`
  - `No installation selected. Run "lynxctl use <installation>" or pass -i.`
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

- Completion scripts are generated for bash, zsh, fish and PowerShell, for
  both program names (`scripts/completions.sh`).
- Completion is dynamic: profile names, installation names, device and
  function names, meta keys (as `key=` for `meta set`) and function types
  come from the configuration and the API.
- Lists fetched from the API are cached for one minute in the user cache
  directory (`LYNX_CACHE_DIR` overrides it), so pressing Tab repeatedly
  costs one API call. Only names, IDs and types are cached, never secrets.
- Commands that take several functions (`get`, `log`, `watch`) do not
  suggest functions already on the command line.
- Without a login, completion offers nothing instead of printing errors.
- Packages install the completion scripts automatically where the platform
  supports it.

## Packaging and releases

Releases are built by GoReleaser (`.goreleaser.yaml`) when a `v*` tag is
pushed, and published as a draft GitHub release to be checked before it
goes public.

| Artifact | Contents |
|----------|----------|
| `.tar.gz` (Linux, macOS) | `lynxctl`, completions, README, LICENSE |
| `.zip` (Windows) | `lynxctl.exe` and `iotopenctl.exe` (a copy), completions, README, LICENSE |
| `.deb`, `.rpm`, `.apk` | `/usr/bin/lynxctl`, `/usr/bin/iotopenctl` as a symlink, completions for bash, zsh and fish under both names |
| Scoop manifest | Both executables on `PATH` |
| winget manifest | Portable zip with both executables |

- Builds cover Linux, Windows and macOS on amd64 and arm64, statically
  linked (`CGO_ENABLED=0`), with the version set from the tag.
- The Scoop and winget manifests are generated but not uploaded until the
  bucket repository and a winget-pkgs fork exist (`skip_upload`).
- CI runs `gofmt`, `go vet` and the tests with the race detector on Linux,
  Windows and macOS.

## License

GPL-3.0-or-later. Every source file starts with an SPDX identifier.

## Project layout

```text
cmd/lynxctl/        main package (tiny: builds the root command and runs it)
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
2. **Objects (done):**
   - `installation`, `device` and `function` with list, get, create, edit
     and delete
   - `meta`
   - `--meta` and `--type` filters
   - reference resolution
3. **Values (done):** `get`, `set`, `watch`, `log`.
4. **Polish (done):** dynamic completion, GoReleaser packaging (including
   the `iotopenctl` alias), and manifests for scoop and winget.
5. **More resources (done):** schedule, file, search, edge-app, notification.
6. **Admin (done):** organization, user (with tokens and passwords), role,
   permissions.

## Future ideas

- Plugins: an executable named `lynxctl-<name>` on `PATH` becomes
  available as `lynxctl <name>` (as with git and kubectl).
- `lynxctl apply -f dir/` for declarative setup of devices and functions.
- Export and import of an installation's devices and functions.
