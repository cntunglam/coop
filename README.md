# rx

A small personal CLI for running your own scripts. Register a command once, save
named **presets**, chain scripts into **workflows**, and manage long-running steps
(a proxy, a tunnel) as **daemons** you can `ps` / `logs` / `stop`.

Stop remembering and copy-pasting fiddly command lines. Just:

```sh
rx cloud-sql prod
```

## Install

```sh
go build -o rx ./cmd/rx
# then put it on your PATH, e.g.
mv rx /usr/local/bin/    # or ~/go/bin, ~/.local/bin, etc.
```

Requires Go 1.25+. macOS / Linux (uses POSIX process sessions for daemons).

## Interactive UI (K9s-style)

Run `rx` with no arguments (or `rx ui`) to open a full-screen terminal dashboard:

```
 rx                                              [q]uit  [?]help
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
  state).
- **Keys:** `↑/↓` navigate · `tab` switch pane · `enter` run a script / open a
  daemon's logs · `n` add a new preset to the selected script (a form appears with
  a field per detected `{{.param}}`; saved straight to your config) · `s` stop ·
  `r` restart · `l` logs (live tail, `Esc` to leave) · `q` quit.
- Selecting a **daemon** script starts it and it appears in the top table.
  Selecting a **foreground** script (e.g. `ssh`) suspends the UI, runs it attached
  to your terminal, and restores the UI when it exits. Workflows run the same way.
  While a foreground command runs, **Ctrl-C aborts just that command** and returns
  you to the UI — it does not quit `rx`.

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
  `rx ps`, `rx logs <name>`, `rx stop <name>`.

## Config

A single global file, created with an example on first run:

- `~/.config/rx/config.yaml` (honors `$XDG_CONFIG_HOME`)
- `rx config path` prints it; `rx config edit` opens it in `$EDITOR`.

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
    file: ~/scripts/migrate.sh          # preset values are passed as env vars ($env, $ENV)
    presets:
      staging: { env: staging }

workflows:
  deploy-staging:
    description: "tunnel, then migrate"
    steps:
      - { script: cloud-sql, preset: staging }   # daemon; workflow waits until ready_port is open
      - { script: migrate,   preset: staging }    # runs against the now-ready tunnel
```

### How preset values reach your script

- **`type: command`** — values are substituted into the command via Go templates:
  `{{.instance}}`, `{{.port}}`.
- **`type: file`** — the file is executed directly and each preset value is exported
  as an environment variable, both as-is and upper-cased (`$env` and `$ENV`).
- For consistency, preset values are exported as env vars for **command** scripts too.

### Readiness gates

For a background step that later steps depend on, set either:

- `ready_port: "{{.port}}"` — rx polls the TCP port and only proceeds once it accepts
  connections (30s timeout), or
- `ready_wait: 3` — a fixed delay in seconds.

## Commands

```
rx                       open the interactive TUI (same as `rx ui`)
rx <script> [preset]     run a script (foreground, or start its daemon if background)
rx <workflow>            run a workflow (sequential, stop on first failure)

rx ui                    open the interactive TUI
rx list                  list configured scripts and workflows
rx ps                    list running daemons (pid, uptime, ready state)
rx logs <name> [-f]      show (or follow) a daemon's log
rx stop <name>           stop a daemon (SIGTERM, then SIGKILL after 10s)
rx config path|edit      show or edit the config file
```

## Notes & limitations

- Daemons are tracked by PID. In the rare window where the OS recycles a PID, a
  dead daemon could briefly appear alive; this is a documented trade-off (rx does
  not query per-process start time to disambiguate).
- Per-run parameter overrides are intentionally not supported — pick a preset.
  Add a new preset when you need a new combination.
- The TUI log view reads the whole log file from the start, but only retains the
  last ~5000 lines on screen and batches updates, so a chatty daemon stays
  responsive. The full log is always on disk (`rx logs <name>` / the file under
  `~/.local/state/rx/<name>/log`).
```
