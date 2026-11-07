package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cntunglam/coop/internal/config"
	"github.com/cntunglam/coop/internal/edit"
)

func saveEnv(t *testing.T) (*edit.Editor, string) {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(filepath.Join(home, "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("scripts:\n  alpha:\n    command: echo a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &edit.Editor{Path: path}, home
}

func load(t *testing.T, ed *edit.Editor) *config.Config {
	t.Helper()
	cfg, err := config.Load(ed.Path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func save(ed *edit.Editor, home, input string, si saveInput) (string, error) {
	var out bytes.Buffer
	err := runSave(strings.NewReader(input), &out, ed, home, si)
	return out.String(), err
}

func TestSaveHappyPath(t *testing.T) {
	ed, home := saveEnv(t)
	si := saveInput{Command: "k logs -f api-7f9c2", Aliases: "alias k=kubectl", Dir: home + "/work"}
	out, err := save(ed, home, "orders-logs\nprod k8s\n", si)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(strings.TrimSpace(out), `saved "orders-logs" — run it with: coop orders-logs`) {
		t.Errorf("output = %q", out)
	}
	s := load(t, ed).Scripts["orders-logs"]
	if !s.Literal || s.Command != "kubectl logs -f api-7f9c2" || s.Dir != "~/work" ||
		!reflect.DeepEqual(s.Tags, []string{"prod", "k8s"}) {
		t.Errorf("script = %+v", s)
	}
}

func TestSaveDefaultName(t *testing.T) {
	ed, home := saveEnv(t)
	_, err := save(ed, home, "\n\n", saveInput{Command: "kubectl --context c -n p logs x", Dir: home})
	if err != nil {
		t.Fatal(err)
	}
	s, ok := load(t, ed).Scripts["kubectl-logs"]
	if !ok || len(s.Tags) != 0 {
		t.Errorf("script = %+v ok=%v", s, ok)
	}
}

func TestSaveReprompts(t *testing.T) {
	ed, home := saveEnv(t)
	out, err := save(ed, home, "ps\nalpha\nmy-ps\n\n", saveInput{Command: "echo hi", Dir: home})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "reserved") || !strings.Contains(out, "taken") {
		t.Errorf("output = %q", out)
	}
	if _, ok := load(t, ed).Scripts["my-ps"]; !ok {
		t.Error("my-ps not saved")
	}
}

func TestSaveNonInteractive(t *testing.T) {
	ed, home := saveEnv(t)
	out, err := save(ed, home, "", saveInput{Command: "echo hi", Dir: home, Name: "hi", NameSet: true, Tags: "x", TagsSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "name [") {
		t.Errorf("prompted: %q", out)
	}
	if !reflect.DeepEqual(load(t, ed).Scripts["hi"].Tags, []string{"x"}) {
		t.Error("tags not saved")
	}
	if _, err := save(ed, home, "", saveInput{Command: "echo", Dir: home, Name: "alpha", NameSet: true, TagsSet: true}); err == nil {
		t.Error("taken --name should error")
	}
}

func TestSaveNoCommand(t *testing.T) {
	ed, home := saveEnv(t)
	for _, c := range []string{"", "  \n"} {
		_, err := save(ed, home, "", saveInput{Command: c})
		if err == nil || !strings.Contains(err.Error(), `eval "$(coop init zsh)"`) {
			t.Errorf("command %q: err = %v", c, err)
		}
	}
}

func TestSaveFunctionWarning(t *testing.T) {
	ed, home := saveEnv(t)
	out, err := save(ed, home, "\n\n", saveInput{Command: "mk build", Funcs: "mk other", Dir: home})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"mk" is a zsh function`) {
		t.Errorf("output = %q", out)
	}
}

func TestSaveMultiline(t *testing.T) {
	ed, home := saveEnv(t)
	cmd := "kubectl get \\\n  pods"
	if _, err := save(ed, home, "ml\n\n", saveInput{Command: cmd, Dir: home}); err != nil {
		t.Fatal(err)
	}
	if got := load(t, ed).Scripts["ml"].Command; got != cmd {
		t.Errorf("command = %q, want %q", got, cmd)
	}
}

func TestRm(t *testing.T) {
	ed, _ := saveEnv(t)
	var out bytes.Buffer
	if err := runRm(strings.NewReader("\n"), &out, ed, "alpha"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "cancelled") {
		t.Errorf("output = %q", out.String())
	}
	if _, ok := load(t, ed).Scripts["alpha"]; !ok {
		t.Fatal("alpha deleted on cancel")
	}
	out.Reset()
	if err := runRm(strings.NewReader("Y\n"), &out, ed, "alpha"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `deleted "alpha"`) {
		t.Errorf("output = %q", out.String())
	}
	if _, ok := load(t, ed).Scripts["alpha"]; ok {
		t.Error("alpha still present")
	}
}
