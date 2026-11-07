package edit

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cntunglam/coop/internal/config"
	"github.com/cntunglam/coop/internal/daemon"
	"github.com/cntunglam/coop/internal/script"
)

const baseYAML = `scripts:
  alpha:
    command: echo alpha
  bg:
    command: sleep 30
    background: true
workflows:
  flow:
    steps:
      - script: alpha
`

func newEditor(t *testing.T, yaml string) *Editor {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Editor{Path: p, Daemons: daemon.NewManagerAt(t.TempDir())}
}

func readFile(t *testing.T, e *Editor) string {
	t.Helper()
	b, err := os.ReadFile(e.Path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func startBG(t *testing.T, e *Editor) {
	t.Helper()
	spec, err := script.Resolve("bg", config.Script{Command: "sleep 30", Background: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Daemons.Start(spec); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Daemons.Stop("bg", 5*time.Second) })
}

func TestPutCreate(t *testing.T) {
	e := newEditor(t, baseYAML)
	cfg, err := e.Put("", "orders-logs", config.Script{Literal: true, Command: "x"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Scripts["orders-logs"]; !ok {
		t.Fatal("returned config missing script")
	}
	loaded, err := config.Load(e.Path)
	if err != nil {
		t.Fatal(err)
	}
	if s := loaded.Scripts["orders-logs"]; !s.Literal || s.Command != "x" {
		t.Fatalf("got %+v", s)
	}
}

func TestPutCreateRejects(t *testing.T) {
	e := newEditor(t, baseYAML)
	before := readFile(t, e)
	for _, name := range []string{"ps", "alpha", "flow", "a b"} {
		if _, err := e.Put("", name, config.Script{Command: "x"}, nil); err == nil {
			t.Errorf("name %q: expected error", name)
		}
	}
	if readFile(t, e) != before {
		t.Fatal("file changed")
	}
}

func TestPutKeepsExternalWrites(t *testing.T) {
	e := newEditor(t, baseYAML)
	cfg, _ := config.Load(e.Path)
	base := cfg.Scripts["alpha"]
	cfg.Scripts["other"] = config.Script{Command: "echo other"}
	if err := config.Save(e.Path, cfg); err != nil {
		t.Fatal(err)
	}
	mod := base
	mod.Description = "modified"
	out, err := e.Put("alpha", "alpha", mod, &base)
	if err != nil {
		t.Fatal(err)
	}
	if out.Scripts["alpha"].Description != "modified" {
		t.Fatal("alpha not modified")
	}
	if _, ok := out.Scripts["other"]; !ok {
		t.Fatal("external write lost")
	}
}

func TestPutDetectsChange(t *testing.T) {
	e := newEditor(t, baseYAML)
	cfg, _ := config.Load(e.Path)
	base := cfg.Scripts["alpha"]
	cfg.Scripts["alpha"] = config.Script{Command: "echo changed"}
	if err := config.Save(e.Path, cfg); err != nil {
		t.Fatal(err)
	}
	_, err := e.Put("alpha", "alpha", config.Script{Command: "mine"}, &base)
	if !errors.Is(err, ErrChanged) {
		t.Fatalf("got %v", err)
	}
}

func TestPutRenameRefusedWhenReferenced(t *testing.T) {
	e := newEditor(t, baseYAML)
	before := readFile(t, e)
	_, err := e.Put("alpha", "beta", config.Script{Command: "echo alpha"}, nil)
	if err == nil || !strings.Contains(err.Error(), `workflow "flow"`) {
		t.Fatalf("got %v", err)
	}
	if readFile(t, e) != before {
		t.Fatal("file changed")
	}
}

func TestPutRenameRefusedWhenRunning(t *testing.T) {
	e := newEditor(t, baseYAML)
	startBG(t, e)
	before := readFile(t, e)
	_, err := e.Put("bg", "bg2", config.Script{Command: "sleep 30", Background: true}, nil)
	if err == nil || !strings.Contains(err.Error(), "stop it first") {
		t.Fatalf("got %v", err)
	}
	if readFile(t, e) != before {
		t.Fatal("file changed")
	}
}

func TestPutRenameOK(t *testing.T) {
	e := newEditor(t, baseYAML)
	out, err := e.Put("bg", "bg2", config.Script{Command: "sleep 30", Background: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out.Scripts["bg"]; ok {
		t.Fatal("old name remains")
	}
	if _, ok := out.Scripts["bg2"]; !ok {
		t.Fatal("new name missing")
	}
}

func TestPutMissingEdit(t *testing.T) {
	e := newEditor(t, baseYAML)
	_, err := e.Put("nope", "nope", config.Script{Command: "x"}, nil)
	if err == nil || err.Error() != `no script "nope"` {
		t.Fatalf("got %v", err)
	}
}

func TestReferencedScriptCannotGainParams(t *testing.T) {
	e := newEditor(t, baseYAML)
	before := readFile(t, e)
	_, err := e.Put("alpha", "alpha", config.Script{Command: "echo {{.who}}"}, nil)
	if err == nil || !strings.Contains(err.Error(), "can't add swappable parts") {
		t.Fatalf("got %v", err)
	}
	if readFile(t, e) != before {
		t.Fatal("file changed")
	}
}

func TestDeleteRules(t *testing.T) {
	yaml := baseYAML + "  \n"
	e := newEditor(t, yaml)
	if _, err := e.Delete("alpha"); err == nil || !strings.Contains(err.Error(), `workflow "flow"`) {
		t.Fatalf("referenced: got %v", err)
	}
	startBG(t, e)
	if _, err := e.Delete("bg"); err == nil || !strings.Contains(err.Error(), "stop it first") {
		t.Fatalf("running: got %v", err)
	}
	if _, err := e.Delete("nope"); err == nil || err.Error() != `no script "nope"` {
		t.Fatalf("missing: got %v", err)
	}
	if _, err := e.Put("", "free", config.Script{Command: "x"}, nil); err != nil {
		t.Fatal(err)
	}
	out, err := e.Delete("free")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out.Scripts["free"]; ok {
		t.Fatal("still present")
	}
	loaded, _ := config.Load(e.Path)
	if _, ok := loaded.Scripts["free"]; ok {
		t.Fatal("still on disk")
	}
}

func TestAddPreset(t *testing.T) {
	e := newEditor(t, "scripts:\n  t:\n    command: echo {{.who}}\nworkflows: {}\n")
	out, err := e.AddPreset("t", "p1", map[string]string{"who": "me"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Scripts["t"].Presets["p1"]["who"] != "me" {
		t.Fatal("preset missing")
	}
	if _, err := e.AddPreset("t", "p1", nil); err == nil || err.Error() != `preset "p1" already exists` {
		t.Fatalf("got %v", err)
	}
}

func TestAddPresetRejectsReservedKey(t *testing.T) {
	e := newEditor(t, "scripts:\n  t:\n    command: ls {{.dir}}\nworkflows: {}\n")
	before := readFile(t, e)
	_, err := e.AddPreset("t", "p", map[string]string{"dir": "/tmp", "path": "/tmp"})
	want := `"path" can't be used as a preset key — it would override $PATH`
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
	if after := readFile(t, e); after != before {
		t.Errorf("file changed:\n%s", after)
	}
}

func TestAddPresetLiteralRefused(t *testing.T) {
	e := newEditor(t, "scripts:\n  t:\n    command: echo hi\n    literal: true\nworkflows: {}\n")
	_, err := e.AddPreset("t", "p", map[string]string{"a": "b"})
	if err == nil || !strings.Contains(err.Error(), "is a literal command; use Make swappable in the edit form") {
		t.Fatalf("got %v", err)
	}
}

func TestWritesRefusedOnBrokenConfig(t *testing.T) {
	e := newEditor(t, "scripts: [")
	before := readFile(t, e)
	if _, err := e.Put("", "x", config.Script{Command: "x"}, nil); err == nil {
		t.Error("Put: expected error")
	}
	if _, err := e.Delete("x"); err == nil {
		t.Error("Delete: expected error")
	}
	if _, err := e.AddPreset("x", "p", nil); err == nil {
		t.Error("AddPreset: expected error")
	}
	if readFile(t, e) != before {
		t.Fatal("file changed")
	}
}

func TestPutRoundTripsSpecialChars(t *testing.T) {
	e := newEditor(t, baseYAML)
	cmd := `printf '%s\n' "$HOME" '{{.Names}}' | grep -v '!' && echo "a\"b"`
	if _, err := e.Put("", "special", config.Script{Literal: true, Command: cmd}, nil); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(e.Path)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := script.Resolve("special", cfg.Scripts["special"], "")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Argv[2] != cmd {
		t.Fatalf("got %q", spec.Argv[2])
	}
}

func TestPutTwiceWithReturnedBase(t *testing.T) {
	e := newEditor(t, baseYAML)
	s := config.Script{Command: "echo hi", Presets: map[string]map[string]string{}, Tags: []string{}}
	ret, err := e.Put("", "twice", s, nil)
	if err != nil {
		t.Fatal(err)
	}
	base := ret.Scripts["twice"]
	mod := base
	mod.Description = "d"
	ret, err = e.Put("twice", "twice", mod, &base)
	if err != nil {
		t.Fatal(err)
	}
	base = ret.Scripts["twice"]
	if _, err := e.Put("twice", "twice", base, &base); err != nil {
		t.Fatal(err)
	}
}

func TestPutBaseNilVsEmptyEqual(t *testing.T) {
	e := newEditor(t, baseYAML)
	base := config.Script{Command: "echo alpha", Presets: map[string]map[string]string{}, Tags: []string{}}
	if _, err := e.Put("alpha", "alpha", config.Script{Command: "echo alpha", Description: "x"}, &base); err != nil {
		t.Fatalf("got %v", err)
	}
}
