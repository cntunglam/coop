package cli

import (
	"os"
	"path/filepath"
)

// seedConfig is written on first run so a new user has a working, self-documenting
// starting point that demonstrates every config type.
const seedConfig = `# coop configuration — full example. Edit with ` + "`coop config edit`" + `.
#
# Four kinds of things you can define:
#   scripts   — a named command you run by name (` + "`coop hello world`" + `)
#   workflows — an ordered sequence of steps, run once (stop on first failure)
#   groups    — a namespace: background scripts brought up / down together
#
# A script is either:
#   type: command  (default) — inline shell; {{.name}} injects preset values
#   type: file               — path to a file; preset values become env vars ($name and $NAME)
# Add ` + "`background: true`" + ` to run a script as a managed daemon (coop ps / logs / stop).

scripts:
  # 1) command script with presets — runnable now: ` + "`coop hello world`" + `
  hello:
    description: "example greeting"
    command: "echo hello {{.who}}"        # {{.who}} comes from the chosen preset
    presets:
      world: { who: world }
      team:  { who: team }

  # 2) background daemon — runnable now: ` + "`coop sleeper`" + ` then ` + "`coop ps`" + ` / ` + "`coop logs sleeper`" + `
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

  # 4) background daemon with a readiness gate + ongoing health check.
  # cloud-sql:
  #   description: "Cloud SQL IAP tunnel"
  #   command: "gcloud compute start-iap-tunnel {{.instance}} 5432 --local-host-port=localhost:{{.port}}"
  #   background: true
  #   ready_port: "{{.port}}"             # "ready" once this port accepts connections (30s timeout)
  #   health:                             # ` + "`coop serve`" + ` restarts it if this stops passing
  #     tcp: "{{.port}}"                  #   probe: tcp | command: "curl -f ..." | http: "http://..."
  #     interval: 10                      #   seconds between probes (default 10)
  #     failures: 3                       #   consecutive fails before restart (default 3)
  #     max_restarts: 5                   #   cap per restart_window; -1 = unlimited (default 5)
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
# namespace for one project). ` + "`coop <group>`" + ` starts members in order,
# ` + "`coop stop <group>`" + ` stops them, ` + "`coop serve <group>`" + ` supervises them headlessly.
groups:
  # dev-stack:
  #   description: "everything for one project"
  #   auto_restart: true                   # coop serve restarts crashed members
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
