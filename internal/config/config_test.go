package config

import (
	"os"
	"path/filepath"
	"reflect"
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
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"tags:", "literal:", "dir:"} {
		if strings.Contains(string(raw), f) {
			t.Errorf("unset field %s was marshaled", f)
		}
	}
	if got.Scripts["proxy"].ResolvedType() != TypeFile {
		t.Errorf("proxy type not preserved")
	}
}

func TestParseGroups(t *testing.T) {
	cfg, err := Parse([]byte(`
scripts:
  docker:
    command: "docker compose up"
    background: true
  devserver:
    command: "pnpm dev"
    background: true
groups:
  stack:
    description: "dev stack"
    auto_restart: true
    members:
      - { script: docker }
      - { script: devserver }
`))
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	g, ok := cfg.Groups["stack"]
	if !ok {
		t.Fatal("group stack missing")
	}
	if !g.AutoRestart || len(g.Members) != 2 {
		t.Errorf("group parsed wrong: %+v", g)
	}
	if got := cfg.GroupNames(); len(got) != 1 || got[0] != "stack" {
		t.Errorf("GroupNames = %v", got)
	}
}

func TestGroupErrors(t *testing.T) {
	cases := []struct{ name, yaml, wantSub string }{
		{
			"member not background",
			"scripts:\n  x:\n    command: echo hi\ngroups:\n  g:\n    members:\n      - { script: x }\n",
			"not a background",
		},
		{
			"unknown member",
			"groups:\n  g:\n    members:\n      - { script: nope }\n",
			"unknown script",
		},
		{
			"empty group",
			"groups:\n  g:\n    members: []\n",
			"no members",
		},
		{
			"reserved group name",
			"groups:\n  serve:\n    members:\n      - { script: x }\n",
			"reserved",
		},
		{
			"group clashes with script",
			"scripts:\n  dup:\n    command: hi\n    background: true\ngroups:\n  dup:\n    members:\n      - { script: dup }\n",
			"both a script and a group",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("want error containing %q, got %v", tc.wantSub, err)
			}
		})
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

func TestHealthTCPValid(t *testing.T) {
	cfg, err := Parse([]byte(`
scripts:
  api:
    command: "run"
    background: true
    health:
      tcp: "{{.port}}"
      interval: 5
    presets:
      p: { port: "8080" }
`))
	if err != nil {
		t.Fatal(err)
	}
	h := cfg.Scripts["api"].Health
	if h == nil || h.TCP != "{{.port}}" || h.Interval != 5 {
		t.Errorf("health not parsed: %+v", h)
	}
}

func TestHealthRejectsTwoProbes(t *testing.T) {
	_, err := Parse([]byte(`
scripts:
  api:
    command: "run"
    background: true
    health:
      tcp: "8080"
      command: "curl -f x"
`))
	if err == nil {
		t.Error("two probes should be rejected")
	}
}

func TestHealthRejectsZeroProbes(t *testing.T) {
	_, err := Parse([]byte(`
scripts:
  api:
    command: "run"
    background: true
    health:
      interval: 5
`))
	if err == nil {
		t.Error("zero probes should be rejected")
	}
}

func TestHealthRequiresBackground(t *testing.T) {
	_, err := Parse([]byte(`
scripts:
  api:
    command: "run"
    health:
      tcp: "8080"
`))
	if err == nil {
		t.Error("health on a non-background script should be rejected")
	}
}

func TestHealthRejectsNegativeTiming(t *testing.T) {
	_, err := Parse([]byte(`
scripts:
  api:
    command: "run"
    background: true
    health:
      tcp: "8080"
      interval: -1
`))
	if err == nil {
		t.Error("negative interval should be rejected")
	}
}

func TestHealthAllowsUnlimitedRestarts(t *testing.T) {
	if _, err := Parse([]byte(`
scripts:
  api:
    command: "run"
    background: true
    health:
      tcp: "8080"
      max_restarts: -1
`)); err != nil {
		t.Errorf("max_restarts -1 (unlimited) should be valid: %v", err)
	}
}

func TestScriptNewFields(t *testing.T) {
	cfg, err := Parse([]byte("scripts:\n  orders-logs:\n    tags: [prod, k8s]\n    literal: true\n    dir: ~/work\n    command: \"docker ps --format '{{.Names}}'\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.Scripts["orders-logs"]
	if !reflect.DeepEqual(s.Tags, []string{"prod", "k8s"}) || !s.Literal || s.Dir != "~/work" {
		t.Errorf("fields not parsed: %+v", s)
	}
}

func TestLiteralRejected(t *testing.T) {
	for _, y := range []string{
		"scripts:\n  a:\n    literal: true\n    command: x\n    presets:\n      p: { k: v }\n",
		"scripts:\n  a:\n    literal: true\n    type: file\n    file: x.sh\n",
	} {
		if _, err := Parse([]byte(y)); err == nil || !strings.Contains(err.Error(), "literal") {
			t.Errorf("want literal error for %q, got %v", y, err)
		}
	}
}

func TestNewReservedNames(t *testing.T) {
	for _, n := range []string{"save", "find", "edit", "rm", "init"} {
		_, err := Parse([]byte("scripts:\n  " + n + ":\n    command: x\n"))
		if err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Errorf("%s: want reserved error, got %v", n, err)
		}
	}
}

func TestValidName(t *testing.T) {
	for _, n := range []string{"orders-logs", "a1", "kubectl_logs", "9lives"} {
		if err := ValidName(n); err != nil {
			t.Errorf("%q: unexpected %v", n, err)
		}
	}
	for _, n := range []string{"", "-x", "_x", "a b", "a/b", "ünï"} {
		if ValidName(n) == nil {
			t.Errorf("%q: want error", n)
		}
	}
}

func TestReferences(t *testing.T) {
	cfg, err := Parse([]byte(`
scripts:
  alpha:
    command: a
  proxy:
    command: p
    background: true
workflows:
  flow:
    steps:
      - script: alpha
      - script: alpha
groups:
  g:
    members:
      - script: proxy
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.References("alpha"); !reflect.DeepEqual(got, []string{`workflow "flow"`}) {
		t.Errorf("alpha refs = %v", got)
	}
	if got := cfg.References("proxy"); !reflect.DeepEqual(got, []string{`group "g"`}) {
		t.Errorf("proxy refs = %v", got)
	}
	if len(cfg.References("nope")) != 0 {
		t.Error("nope should have no refs")
	}
	for _, n := range []string{"alpha", "flow", "g", "save"} {
		if !cfg.NameTaken(n) {
			t.Errorf("%q should be taken", n)
		}
	}
	if cfg.NameTaken("fresh") {
		t.Error("fresh should be free")
	}
}
