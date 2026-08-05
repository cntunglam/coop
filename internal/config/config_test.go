package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParseValid(t *testing.T) {
	data := []byte(`
scripts:
  cloud-sql:
    description: "tunnel"
    type: command
    command: "gcloud ... {{.instance}} {{.port}}"
    background: true
    ready_port: "{{.port}}"
    presets:
      prod: { instance: prod-db, port: "5432" }
      staging: { instance: staging-db, port: "5433" }
  migrate:
    type: file
    file: ~/scripts/migrate.sh
    presets:
      staging: { env: staging }
workflows:
  deploy:
    description: "deploy"
    steps:
      - { script: cloud-sql, preset: staging }
      - { script: migrate, preset: staging }
`)
	cfg, err := Parse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Scripts) != 2 || len(cfg.Workflows) != 1 {
		t.Fatalf("wrong counts: %d scripts, %d workflows", len(cfg.Scripts), len(cfg.Workflows))
	}
	if cfg.Scripts["migrate"].ResolvedType() != TypeFile {
		t.Errorf("migrate should be file type")
	}
	if cfg.Scripts["cloud-sql"].ResolvedType() != TypeCommand {
		t.Errorf("cloud-sql should default/be command type")
	}
}

func TestSaveRoundTrip(t *testing.T) {
	cfg, err := Parse([]byte(`
scripts:
  ssh:
    command: "ssh {{.host}}"
  proxy:
    type: file
    file: ~/p.sh
    background: true
    presets:
      a: { x: "1" }
`))
	if err != nil {
		t.Fatal(err)
	}
	// Add a preset in memory, save, reload.
	s := cfg.Scripts["ssh"]
	s.Presets = map[string]map[string]string{"prod": {"host": "h"}}
	cfg.Scripts["ssh"] = s

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Scripts["ssh"].Presets["prod"]["host"] != "h" {
		t.Errorf("preset not round-tripped: %+v", got.Scripts["ssh"])
	}
	// omitempty: command script must not emit an empty file field, and vice versa.
	if got.Scripts["ssh"].File != "" {
		t.Errorf("command script gained file field")
	}
	if got.Scripts["proxy"].Command != "" {
		t.Errorf("file script gained command field")
	}
	if got.Scripts["proxy"].ResolvedType() != TypeFile {
		t.Errorf("proxy type not preserved")
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantSub string
	}{
		{
			name:    "reserved script name",
			yaml:    "scripts:\n  ps:\n    command: echo hi\n",
			wantSub: "reserved",
		},
		{
			name:    "command type missing command",
			yaml:    "scripts:\n  x:\n    type: command\n",
			wantSub: "requires a non-empty 'command'",
		},
		{
			name:    "file type missing file",
			yaml:    "scripts:\n  x:\n    type: file\n",
			wantSub: "requires a non-empty 'file'",
		},
		{
			name:    "unknown type",
			yaml:    "scripts:\n  x:\n    type: bogus\n    command: hi\n",
			wantSub: "unknown type",
		},
		{
			name:    "workflow unknown script",
			yaml:    "workflows:\n  w:\n    steps:\n      - { script: nope }\n",
			wantSub: "unknown script",
		},
		{
			name:    "workflow unknown preset",
			yaml:    "scripts:\n  a:\n    command: hi\n    presets:\n      x: {}\nworkflows:\n  w:\n    steps:\n      - { script: a, preset: y }\n",
			wantSub: "no preset",
		},
		{
			name:    "unknown field",
			yaml:    "scripts:\n  a:\n    command: hi\n    bogusfield: 1\n",
			wantSub: "field",
		},
		{
			name:    "empty workflow",
			yaml:    "workflows:\n  w:\n    steps: []\n",
			wantSub: "no steps",
		},
		{
			name:    "script and workflow name clash",
			yaml:    "scripts:\n  a:\n    command: hi\nworkflows:\n  a:\n    steps:\n      - { script: a }\n",
			wantSub: "both a script and a workflow",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}
