# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`coop` is a personal CLI + K9s-style TUI for running saved scripts. You register scripts once
in `~/.config/coop/config.yaml`, give them named **presets** (parameter bundles), chain them
into sequential **workflows**, and manage long-running background scripts as **daemons**
(`ps`/`logs`/`stop`/`restart`). Module path: `github.com/cntunglam/coop`. Go 1.25+, macOS/Linux.

## Commands

```sh
go build -o coop ./cmd/coop      # build the binary (entrypoint is cmd/coop/main.go)
go build ./...               # verify everything compiles (no binary emitted)
go test ./...                # full test suite
go test ./internal/tui/ -run TestStartAndStopDaemonViaKeys -v   # single test
go vet ./...
gofmt -w internal/ cmd/      # format (CI-style check: gofmt is the source of truth)
```

There is no lint config beyond `go vet` + `gofmt`. Tests spawn real child processes
(`sleep`, `python3 -m http.server`) and use `tcell.NewSimulationScreen` to drive the TUI
headlessly — they need those binaries on PATH.

## Architecture

Data flows in one direction through small packages; the CLI and TUI are two front-ends over
the same core:

```
config ──► script.Resolve ──► daemon.Manager  (background)
                          └──► runner          (foreground + workflow orchestration)
cli / tui ──► runner ──► {script.Resolve, daemon.Manager}
```

- **`internal/config`** — YAML schema + `Load`/`Parse`/`Save`. `Parse` uses
  `KnownFields(true)` (typos are errors) and validates cross-references (workflow steps →
  existing scripts/presets, reserved-name collisions). `Save` marshals, **re-parses to prove
  validity, then writes** — it will never leave an unloadable config on disk. Struct fields use
  `omitempty` so rewrites stay clean; comments are NOT preserved across `Save`.
- **`internal/script`** — `Resolve(name, script, preset)` turns a script+preset into an exec
  `Spec` (argv + env). `command`-type scripts template `{{.field}}` into a `sh -c` string;
  `file`-type scripts exec the file directly. **Preset values are exported as env vars
  (`$foo` and `$FOO`) for BOTH types** — that's the one consistent contract. `ParamNames`
  discovers a script's params (template vars ∪ existing preset keys) for the TUI's add-preset form.
- **`internal/daemon`** — supervises background scripts. Highest-risk code; read it first.
  Spawns detached via `SysProcAttr{Setsid: true}` (own session, survives `coop` exiting →
  reparents to launchd), logs to a file under `~/.local/state/coop/<name>/`, tracks PID in
  `meta.json`. Two subtle invariants: (1) `Start` is **idempotent** — refuses a second daemon
  of the same name; (2) it launches `go cmd.Wait()` to **reap the child** so a dead daemon
  isn't left a zombie while the long-running TUI stays alive (a zombie reads as "alive" via
  `kill(pid,0)` and would make `Stop` hang its full timeout). Readiness gating: `ready_port`
  TCP-probe or `ready_wait` seconds.
- **`internal/runner`** — foreground exec and sequential workflow execution (stop on first
  failure). Key rule: a background step **inside a workflow** tolerates an already-running
  daemon (workflows stay re-runnable), but a **standalone** `coop <script>` refuses to
  double-start (`tolerateRunning` flag threads this through `runStep`).
- **`internal/cli`** — cobra wiring. Scripts and workflows become **dynamic subcommands** built
  from config at startup; a fixed set of reserved verbs (`ps`/`logs`/`stop`/`list`/`config`/
  `ui`, plus `help`/`completion`) is protected — config validation rejects any script/workflow
  that collides with them. Bare `coop` (no args) launches the TUI. First run seeds an example config.
- **`internal/tui`** — `tview`/`tcell` full-screen UI (the toolkit K9s uses). Read-only over
  config except the add-preset form. Reuses `runner`/`daemon`; does not reimplement execution.
  **Foreground scripts use `app.Suspend`** to run attached to the real terminal, then restore.
  During suspend, `coop` catches and swallows SIGINT so Ctrl-C aborts only the child (which
  inherits the default handler and dies) and returns to the UI instead of killing `coop`.

## Conventions specific to this repo

- **Presets only, no per-run overrides** — deliberately narrowed. Don't add ad-hoc override
  flags; add a preset instead.
- Platform-specific spawning lives in `internal/daemon/spawn_unix.go` (`//go:build unix`).
- When touching the daemon lifecycle or the TUI suspend/signal paths, prefer the existing
  subprocess/simulation-screen test patterns (`TestSuspendSignalIsolation`,
  `TestStartAndStopDaemonViaKeys`) — these behaviors can't be verified by ordinary unit tests.
