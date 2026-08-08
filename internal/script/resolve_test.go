package script

import (
	"strings"
	"testing"

	"github.com/cntunglam/coop/internal/config"
)

func TestResolveCommand(t *testing.T) {
	s := config.Script{
		Type:      config.TypeCommand,
		Command:   "connect {{.instance}} on {{.port}}",
		ReadyPort: "{{.port}}",
		Presets: map[string]map[string]string{
			"prod": {"instance": "prod-db", "port": "5432"},
		},
	}
	spec, err := Resolve("cloud-sql", s, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := spec.Display, "connect prod-db on 5432"; got != want {
		t.Errorf("Display = %q, want %q", got, want)
	}
	if len(spec.Argv) != 3 || spec.Argv[0] != "sh" || spec.Argv[1] != "-c" {
		t.Errorf("Argv = %v, want [sh -c ...]", spec.Argv)
	}
	if spec.ReadyPort != 5432 {
		t.Errorf("ReadyPort = %d, want 5432", spec.ReadyPort)
	}
	// env should include both lower and upper case forms
	if !hasEnv(spec.Env, "instance=prod-db") || !hasEnv(spec.Env, "INSTANCE=prod-db") {
		t.Errorf("env missing instance vars: %v", presetOnly(spec.Env))
	}
	if !hasEnv(spec.Env, "port=5432") || !hasEnv(spec.Env, "PORT=5432") {
		t.Errorf("env missing port vars: %v", presetOnly(spec.Env))
	}
}

func TestResolveFile(t *testing.T) {
	s := config.Script{
		Type: config.TypeFile,
		File: "/opt/scripts/migrate.sh",
		Presets: map[string]map[string]string{
			"staging": {"env": "staging"},
		},
	}
	spec, err := Resolve("migrate", s, "staging")
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Argv) != 1 || spec.Argv[0] != "/opt/scripts/migrate.sh" {
		t.Errorf("Argv = %v", spec.Argv)
	}
	if !hasEnv(spec.Env, "env=staging") || !hasEnv(spec.Env, "ENV=staging") {
		t.Errorf("env missing: %v", presetOnly(spec.Env))
	}
}

func TestResolveMissingPreset(t *testing.T) {
	s := config.Script{Command: "hi", Presets: map[string]map[string]string{"a": {}}}
	if _, err := Resolve("x", s, ""); err == nil {
		t.Fatal("expected error when preset required")
	}
	if _, err := Resolve("x", s, "nope"); err == nil {
		t.Fatal("expected error for unknown preset")
	}
}

func TestResolveNoPresetsOK(t *testing.T) {
	s := config.Script{Command: "echo hi"}
	spec, err := Resolve("x", s, "")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if spec.Display != "echo hi" {
		t.Errorf("Display = %q", spec.Display)
	}
}

func TestResolveMissingTemplateKey(t *testing.T) {
	s := config.Script{Command: "hi {{.missing}}", Presets: map[string]map[string]string{"a": {"x": "1"}}}
	if _, err := Resolve("x", s, "a"); err == nil {
		t.Fatal("expected error for missing template key")
	}
}

func hasEnv(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

// presetOnly filters out the inherited OS environment for readable failure output.
func presetOnly(env []string) []string {
	var out []string
	for _, e := range env {
		if strings.HasPrefix(e, "instance=") || strings.HasPrefix(e, "INSTANCE=") ||
			strings.HasPrefix(e, "port=") || strings.HasPrefix(e, "PORT=") ||
			strings.HasPrefix(e, "env=") || strings.HasPrefix(e, "ENV=") {
			out = append(out, e)
		}
	}
	return out
}
