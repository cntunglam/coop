# coop

A small personal CLI for running your own scripts. Register a command once, save
named **presets**, chain scripts into **workflows**, and manage long-running steps
(a proxy, a tunnel) as **daemons** you can `ps` / `logs` / `stop`.

Stop remembering and copy-pasting fiddly command lines. Just:

```sh
coop cloud-sql prod
```

## Install

### Homebrew (recommended)

```sh
brew install cntunglam/tap/coop
```

### Go install

```sh
go install github.com/cntunglam/coop/cmd/coop@latest
```

### From source

Requires Go 1.25+. macOS / Linux (uses POSIX process sessions for daemons).

Clone and build the **stable** version (the `release` branch tracks the latest
tagged release):

```sh
git clone https://github.com/cntunglam/coop.git
cd coop
git checkout release            # stable line — or a specific tag, e.g. `git checkout v0.3.0`

go build -o coop ./cmd/coop         # build the binary
sudo mv coop /usr/local/bin/      # put it on your PATH (or: mv coop ~/go/bin/, ~/.local/bin/)
```

Or, from inside the cloned repo, install straight onto your Go bin
(`$(go env GOPATH)/bin`, so make sure that's on your `PATH`):

```sh
go install ./cmd/coop
```

Verify: `coop list` (first run seeds an example config), or `coop` to open the TUI.

To update later: `git pull` (on `release`), rebuild, and replace the binary.

## Interactive UI (K9s-style)

Run `coop` with no arguments (or `coop ui`) to open a full-screen terminal dashboard:

```
 coop                                              [q]uit  [?]help
 ┌─ DAEMONS ──────────────────────────────────────────────────┐
 │ NAME       PRESET  PID    UPTIME  STATE     COMMAND         │
 │▸cloud-sql  prod    4021   2m14s   ● ready   gcloud ...      │
 └────────────────────────────────────────────────────────────┘
 ┌─ SCRIPTS / WORKFLOWS ──────────────────────────────────────┐
 │ ssh     script    SSH to a saved host · presets: mynode     │
 │ deploy  workflow  3 step(s)                                 │
 └────────────────────────────────────────────────────────────┘
```

- Top pane is a **live** table of running daemons (refreshes every second, colored
  state; columns include `GROUP` and `PORT`, and `COMMAND` flexes to the terminal
  width); below it is the scripts/workflows catalog; the bottom pane is a **console**
  — an activity log plus a command line.
- **Keys:** `↑/↓` navigate · `tab` switch pane · `enter` run a script / open a
  daemon's logs · `n` add a new preset to the selected script · `/` filter the
  catalog · `a` add a script · `E` edit (or delete) the selected script/daemon ·
  `s` stop · `r` restart · `l` logs (live tail, `Esc` to leave) · `e` **command
  mode** · `q` quit.
- **Add/edit form (`a` / `E`)**: name, description, tags, command (or file path
  for file scripts), folder, and "run in background (daemon)" — ticking it reveals
  `ready port` and `ready wait`. Buttons: Save, Make swappable…, Delete (asks
  first), Cancel; `Esc` cancels. If Save fails, the error shows under the buttons.
  Presets and health checks are kept as they are (health is YAML-only).
  Renaming or deleting is refused while a workflow/group uses the script or its
  daemon is running; other edits to a running daemon apply on its next start.
  Workflows and groups are edited with `coop config edit`.
- **Make swappable (`Make swappable…` button, or `Ctrl-P` in the form)** turns
  part of a saved command into a parameter you pick per run. Pick a word of the
  command, name the part (e.g. `context`), and Apply: the word becomes
  `{{.context}}` and today's value is saved as a preset (you name the preset when
  the script has none; otherwise the value is added to every existing preset).
  Press Save to keep it. Part names that would override a vital environment
  variable (`path`, `home`, `pwd`, `shell`, `user`, `lang`, `lc_*`, …) are refused,
  because preset values are exported as env vars. Not available for file scripts,
  or while a workflow/group uses the script.
- **Command mode (`e`)** focuses the console's command line, where you type
  these verbs (a subset of the CLI, plus `start`/`run` and `reload`) to drive your
  daemons/scripts, then `Esc` to return. **Type to autocomplete** — matching verbs
  and names appear as you type; `Tab`/`Enter` fills one in.
  - `list` · `ps` · `logs <daemon>` · `reload` (re-read the config from disk)
  - `stop <daemon|group>` · `restart <daemon|group>` · `start <name> [preset]`
  - `config path|edit` (edit reloads the catalog on save)
  - `<name> [preset]` — run a script, workflow, or group by name
- Selecting a **daemon** script starts it and it appears in the top table.
  Selecting a **foreground** script (e.g. `ssh`) suspends the UI, runs it attached
  to your terminal, and restores the UI when it exits. Workflows run the same way.
  While a foreground command runs, **Ctrl-C aborts just that command** and returns
  you to the UI — it does not quit `coop`.

The CLI subcommands below all still work directly without opening the UI.

## Concepts

- **Script** — a named thing you run. Either an inline shell command
  (`type: command`, the default) or a pointer to a script file (`type: file`).
- **Preset** — a named bundle of parameter values for a script (`prod`, `staging`).
  You pick a preset by name; minimal typing.
- **Workflow** — an ordered list of steps run one-by-one, stopping at the first
  failure.
- **Daemon** — a script marked `background: true`. It keeps running after the
  command returns, detached from your terminal, tracked by PID. Manage it with
  `coop ps`, `coop logs <name>`, `coop stop <name>`.
- **Group** — a namespace (like a k8s namespace): a named bundle of background
  scripts brought up and torn down together, e.g. a whole dev project (docker +
  dev server + a proxy). `coop <group>` starts all members in order; `coop stop
  <group>` stops them; `coop serve <group>` supervises them headlessly on a server.

## Config

A single global file, created with an example on first run:

- `~/.config/coop/config.yaml` (honors `$XDG_CONFIG_HOME`)
- `coop config path` prints it; `coop config edit` opens it in `$EDITOR`.

```yaml
scripts:
  hello:
    description: "example greeting"
    command: "echo hello {{.who}}"      # {{.name}} injects preset values
    presets:
      world: { who: world }

  cloud-sql:
    description: "Cloud SQL IAP tunnel"
    command: "gcloud compute start-iap-tunnel {{.instance}} 5432 --local-host-port=localhost:{{.port}}"
    background: true                     # managed daemon
    ready_port: "{{.port}}"             # "ready" once this TCP port accepts a connection
    presets:
      prod:    { instance: prod-db,    port: "5432" }
      staging: { instance: staging-db, port: "5433" }

  migrate:
    type: file
    file: ~/scripts/migrate.sh          # preset values are passed as env vars ($stage, $STAGE)
    presets:
      staging: { stage: staging }

  orders-logs:                           # what `coop save` writes
    command: "kubectl logs -f deploy/orders --context gke_prod"
    literal: true                        # run exactly as stored ({{ }} is not templated)
    dir: ~/work/orders                   # working folder for the run (~ is expanded)
    tags: [prod, k8s]                    # matched by `coop find` and the TUI `/` filter

workflows:
  deploy-staging:
    description: "tunnel, then migrate"
    steps:
      - { script: cloud-sql, preset: staging }   # daemon; workflow waits until ready_port is open
      - { script: migrate,   preset: staging }    # runs against the now-ready tunnel

groups:
  gamehub:                        # a namespace: all the services for one project
    description: "gamification-hub dev stack"
    auto_restart: true            # `coop serve` restarts crashed members
    members:                      # ordered background scripts (start in order, stop in reverse)
      - { script: docker }
      - { script: gamehub-dev }
      - { script: cloud-sql, preset: staging }
```

- `literal: true` is managed by coop: `coop save` and the add form set it, and
  Make swappable clears it when it adds a parameter. A literal command runs exactly
  as stored, so it can't have presets (and can't be a file script).
- `dir` sets the working folder for foreground and background runs.
- `tags` are free-form labels for searching.
- A script with exactly one preset uses it when you don't name one
  (`coop hello` runs the `world` preset above).

### How preset values reach your script

- **`type: command`** — values are substituted into the command via Go templates:
  `{{.instance}}`, `{{.port}}`.
- **`type: file`** — the file is executed directly and each preset value is exported
  as an environment variable, both as-is and upper-cased (`$stage` and `$STAGE`).
- For consistency, preset values are exported as env vars for **command** scripts too.

### Readiness gates

For a background step that later steps depend on, set either:

- `ready_port: "{{.port}}"` — coop polls the TCP port and only proceeds once it accepts
  connections (30s timeout), or
- `ready_wait: 3` — a fixed delay in seconds.

### Health checks (auto-restart under `coop serve`)

A background script can declare a `health` probe. When you run the script's group
under `coop serve`, the supervisor probes each member on an interval and restarts
it if it stops passing — catching hung-but-alive processes that a bare PID check
misses. Restarts are bounded so a genuinely broken service can't crash-loop.

```yaml
scripts:
  api:
    command: "exec myserver --port {{.port}}"
    background: true
    health:
      tcp: "{{.port}}"        # exactly one of: tcp | command | http
      # command: "curl -fs localhost:{{.port}}/healthz"
      # http: "http://localhost:{{.port}}/healthz"
      interval: 10            # seconds between probes        (default 10)
      timeout: 3              # per-probe timeout seconds      (default 3)
      failures: 3             # consecutive fails -> restart   (default 3)
      max_restarts: 5         # cap per window; -1 = unlimited (default 5)
      restart_window: 60      # rolling window seconds         (default 60)
    presets:
      prod: { port: "8080" }
```

- The probe is a **TCP connect**, a **command** (exit 0 = healthy), or an **HTTP GET**
  (2xx = healthy). Probe strings are templated with preset values; the command probe
  runs with the daemon's preset env vars.
- Actuation happens **only** under `coop serve`. `coop ps` and the TUI show a `HEALTH`
  column (read-only); daemons not under an active `serve` show `-` (not monitored).
- After `max_restarts` restarts within `restart_window`, the member is marked
  `failed` and left alone (check `coop logs <name>`).

## Commands

```
coop                       open the interactive TUI (same as `coop ui`)
coop <script> [preset]     run a script (foreground, or start its daemon if background)
coop <workflow>            run a workflow (sequential, stop on first failure)
coop <group>               bring a group's daemons up (in order)

coop ui                    open the interactive TUI
coop list                  list configured scripts, workflows, and groups
coop ps                    list running daemons (name, group, pid, uptime, state)
coop logs <name> [-f]      show (or follow) a daemon's log
coop stop <name>           stop a daemon — or a whole group if <name> is a group
coop serve <group>         bring a group up and supervise it headlessly (no TUI);
                         with `auto_restart: true`, restarts crashed members.
                         Ctrl-C / SIGTERM stops the group. Good under systemd.
coop config path|edit      show or edit the config file

coop save [-- cmd…]        save the last shell command (or the given one) as a script
                         [--name N] [--tags a,b] skip the prompts
coop save --recent         pick another open zsh tab's last command and save that
coop init zsh              print the zsh integration snippet
coop edit <name>           edit a script in coop's form (full-screen)
coop rm <name>             delete a saved script (asks first)
coop find [words…]         search scripts, workflows, and groups, then run your pick
```

### Saving commands

Add this line to `~/.zshrc`:

```sh
eval "$(coop init zsh)"
```

Then, after running any command, type `coop save` and press Enter twice: the first
Enter accepts the suggested name, the second skips tags. Aliases are expanded, the
current directory is recorded, and the command is stored literally (no templating).
Run it later with `coop <name>`; remove it with `coop rm <name>`.

Ran the command in a different tab? `coop save --recent` lists the last command of
every other open zsh tab, newest first, with how long ago it ran and its folder.
Type to filter (words match the command or folder), ↑/↓ to move, Enter to pick,
Esc to cancel. The picked command and its folder are saved; aliases are expanded
with the current tab's. Each tab keeps its record in
`${XDG_STATE_HOME:-~/.local/state}/coop/shells/<pid>` (mode 0600); records of
closed tabs are cleaned up the next time `--recent` runs.
Can't remember the name? `coop find [words…]` lists scripts, workflows, and groups
where every word appears (case-insensitive substring) in the name, tags,
description, or command; entries whose name or tags match rank first. Pick one to
run it (scripts with several presets ask which one).

### Upgrading

`save`, `find`, `edit`, `rm`, and `init` are now coop commands, so a script,
workflow, or group with one of those names fails config validation. Rename it with
`coop config edit`.

Tabs opened before upgrading still run the old snippet, so `coop save --recent`
won't list them. Open a new tab, or re-run `eval "$(coop init zsh)"` in the old one.

### Running on a VPS

`coop serve <group>` is the production primitive: it starts the group and stays in
the foreground supervising it, so it drops straight into a systemd unit:

```ini
[Service]
ExecStart=/usr/local/bin/coop serve gamehub
Restart=always
```

Because daemons are tracked in a shared state dir, launching `coop` (the TUI) or
`coop ps` at any time — even from a separate SSH session — shows everything that's
currently running, including groups started by `coop serve`.

## Notes & limitations

- Daemons are tracked by PID. In the rare window where the OS recycles a PID, a
  dead daemon could briefly appear alive; this is a documented trade-off (coop does
  not query per-process start time to disambiguate).
- Per-run parameter overrides are intentionally not supported — pick a preset.
  Add a new preset when you need a new combination.
- The TUI log view reads the whole log file from the start, but only retains the
  last ~5000 lines on screen and batches updates, so a chatty daemon stays
  responsive. The full log is always on disk (`coop logs <name>` / the file under
  `~/.local/state/coop/<name>/log`).
```
