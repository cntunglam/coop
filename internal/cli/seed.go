package cli

import (
	"os"
	"path/filepath"
)

// seedConfig is written on first run so a new user has a working, self-documenting
// starting point.
const seedConfig = `# rx configuration — https://github.com/caongoctunglam/rx
#
# scripts: named commands you run by name, e.g. "rx hello world".
#   type: command  -> inline shell (default); use {{.name}} to inject preset values
#   type: file     -> path to a script file; preset values are passed as env vars
#   background: true marks a long-lived daemon (manage with rx ps / logs / stop)
#   ready_port / ready_wait gate readiness so workflow steps can run against it
#
# workflows: ordered steps run one-by-one, stopping at the first failure.

scripts:
  hello:
    description: "example greeting"
    command: "echo hello {{.who}}"
    presets:
      world: { who: world }
      team: { who: team }

  # Example: a Cloud SQL proxy exposed as a managed daemon.
  # cloud-sql:
  #   description: "Cloud SQL IAP tunnel"
  #   command: "gcloud compute start-iap-tunnel {{.instance}} 5432 --local-host-port=localhost:{{.port}}"
  #   background: true
  #   ready_port: "{{.port}}"
  #   presets:
  #     prod:    { instance: prod-db,    port: "5432" }
  #     staging: { instance: staging-db, port: "5433" }

workflows: {}
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
