package script

import (
	"os"
	"path/filepath"
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
	s := config.Script{Command: "hi", Presets: map[string]map[string]string{"a": {}, "b": {}}}
	if _, err := Resolve("x", s, ""); err == nil || !strings.Contains(err.Error(), "requires a preset") {
		t.Fatalf("expected requires-a-preset error, got %v", err)
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

func TestResolveHealthTCPTemplating(t *testing.T) {
	s := config.Script{
		Command:    "run",
		Background: true,
		Health:     &config.Health{TCP: "{{.port}}"},
		Presets:    map[string]map[string]string{"p": {"port": "8080"}},
	}
	spec, err := Resolve("api", s, "p")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Health == nil {
		t.Fatal("Spec.Health is nil")
	}
	if spec.Health.Kind != "tcp" || spec.Health.Target != "8080" {
		t.Errorf("kind/target = %q/%q, want tcp/8080", spec.Health.Kind, spec.Health.Target)
	}
	// defaults filled
	if spec.Health.Interval != 10 || spec.Health.Timeout != 3 || spec.Health.Failures != 3 ||
		spec.Health.MaxRestarts != 5 || spec.Health.RestartWindow != 60 {
		t.Errorf("defaults not filled: %+v", spec.Health)
	}
}

func TestResolveHealthCommandCarriesEnv(t *testing.T) {
	s := config.Script{
		Command:    "run",
		Background: true,
		Health:     &config.Health{Command: "check", Interval: 2},
		Presets:    map[string]map[string]string{"p": {"port": "9000"}},
	}
	spec, err := Resolve("api", s, "p")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Health.Kind != "command" || spec.Health.Interval != 2 {
		t.Errorf("kind/interval = %q/%d", spec.Health.Kind, spec.Health.Interval)
	}
	var hasPort bool
	for _, e := range spec.Health.Env {
		if e == "PORT=9000" {
			hasPort = true
		}
	}
	if !hasPort {
		t.Errorf("command probe env missing PORT=9000: %v", spec.Health.Env)
	}
}

func TestResolveHealthUnlimitedPreserved(t *testing.T) {
	s := config.Script{
		Command:    "run",
		Background: true,
		Health:     &config.Health{HTTP: "http://x/health", MaxRestarts: -1},
	}
	spec, err := Resolve("api", s, "")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Health.MaxRestarts != -1 {
		t.Errorf("max_restarts = %d, want -1", spec.Health.MaxRestarts)
	}
}

func TestResolveNoHealth(t *testing.T) {
	s := config.Script{Command: "run", Background: true}
	spec, _ := Resolve("api", s, "")
	if spec.Health != nil {
		t.Error("Spec.Health should be nil when no health block")
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

func TestResolveLiteralSkipsTemplate(t *testing.T) {
	cmd := "docker ps --format '{{.Names}}'"
	spec, err := Resolve("x", config.Script{Literal: true, Command: cmd}, "")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Argv[2] != cmd || spec.Display != cmd {
		t.Fatalf("argv=%v display=%q", spec.Argv, spec.Display)
	}
}

func TestResolveSinglePresetDefault(t *testing.T) {
	s := config.Script{Command: "echo {{.x}}", Presets: map[string]map[string]string{"prod": {"x": "1"}}}
	spec, err := Resolve("x", s, "")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Argv[2] != "echo 1" || spec.Preset != "prod" {
		t.Fatalf("argv=%v preset=%q", spec.Argv, spec.Preset)
	}
}

func TestResolveDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.Mkdir(filepath.Join(home, "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	spec, err := Resolve("x", config.Script{Command: "pwd", Dir: "~/work"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Dir != filepath.Join(home, "work") {
		t.Fatalf("Dir = %q", spec.Dir)
	}
	_, err = Resolve("x", config.Script{Command: "pwd", Dir: "~/gone"}, "")
	if err == nil || !strings.Contains(err.Error(), "does not exist") || !strings.Contains(err.Error(), "~/gone") {
		t.Fatalf("expected does-not-exist error naming ~/gone, got %v", err)
	}
}
