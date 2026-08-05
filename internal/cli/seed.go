package cli

import (
	"os"
	"path/filepath"
)

// seedConfig is written on first run so a new user has a working, self-documenting
// starting point that demonstrates every config type.
const seedConfig = `# rx configuration — full example. Edit with ` + "`rx config edit`" + `.
#
# Four kinds of things you can define:
#   scripts   — a named command you run by name (` + "`rx hello world`" + `)
#   workflows — an ordered sequence of steps, run once (stop on first failure)
#   groups    — a namespace: background scripts brought up / down together
#
# A script is either:
#   type: command  (default) — inline shell; {{.name}} injects preset values
#   type: file               — path to a file; preset values become env vars ($name and $NAME)
# Add ` + "`background: true`" + ` to run a script as a managed daemon (rx ps / logs / stop).

scripts:
  # 1) command script with presets — runnable now: ` + "`rx hello world`" + `
  hello:
    description: "example greeting"
    command: "echo hello {{.who}}"        # {{.who}} comes from the chosen preset
    presets:
      world: { who: world }
      team:  { who: team }

  # 2) background daemon — runnable now: ` + "`rx sleeper`" + ` then ` + "`rx ps`" + ` / ` + "`rx logs sleeper`" + `
  sleeper:
    description: "example daemon (logs a tick every 5s)"
    command: "while true; do echo tick; sleep 5; done"
    background: true

  # 3) file-type script — runs a file directly; preset values arrive as env vars.
  # migrate:
  #   description: "run a migration script"
  #   type: file
  #   file: ~/scripts/migrate.sh          # the script reads $env / $ENV
  #   presets:
  #     staging: { env: staging }

  # 4) background daemon with a readiness gate (waits until the TCP port is open).
  # cloud-sql:
  #   description: "Cloud SQL IAP tunnel"
  #   command: "gcloud compute start-iap-tunnel {{.instance}} 5432 --local-host-port=localhost:{{.port}}"
  #   background: true
  #   ready_port: "{{.port}}"             # "ready" once this port accepts connections (30s timeout)
  #   # ready_wait: 3                      # alternative: a fixed N-second delay
  #   presets:
  #     prod:    { instance: prod-db,    port: "5432" }
  #     staging: { instance: staging-db, port: "5433" }

# workflows: ordered steps, run once, stopping on the first failure. A background
# step starts its daemon and (if it has a readiness gate) the workflow waits for
# it before running the next step.
workflows:
  # deploy-staging:
  #   description: "tunnel, then migrate against it"
  #   steps:
  #     - { script: cloud-sql, preset: staging }   # daemon; waits for ready_port
  #     - { script: migrate,   preset: staging }    # runs once the tunnel is ready

# groups: a namespace of background scripts brought up / down together (like a k8s
# namespace for one project). ` + "`rx <group>`" + ` starts members in order,
# ` + "`rx stop <group>`" + ` stops them, ` + "`rx serve <group>`" + ` supervises them headlessly.
groups:
  # dev-stack:
  #   description: "everything for one project"
  #   auto_restart: true                   # rx serve restarts crashed members
  #   members:                             # ordered — start in order, stop in reverse
  #     - { script: cloud-sql, preset: staging }
  #     - { script: sleeper }
`

// ensureConfig creates the config directory and a seed file if none exists.
func ensureConfig(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil // already present
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(seedConfig), 0o644)
}
