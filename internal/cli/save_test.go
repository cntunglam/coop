package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cntunglam/coop/internal/capture"
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

// TestSaveNoPreviousCommand: with the hook installed but nothing recorded in
// this tab, point at --recent instead of the install hint.
func TestSaveNoPreviousCommand(t *testing.T) {
	ed, home := saveEnv(t)
	for _, c := range []string{"", "  \n"} {
		_, err := save(ed, home, "", saveInput{Command: c, Hooked: true})
		want := "no previous command in this tab — try coop save --recent"
		if err == nil || err.Error() != want {
			t.Errorf("command %q: err = %v, want %q", c, err, want)
		}
	}
}

// TestSaveCmdHookDetection: an empty-but-present COOP_LAST_CMD means the hook
// is installed; an absent one keeps the install hint.
func TestSaveCmdHookDetection(t *testing.T) {
	ed, _ := saveEnv(t)
	run := func() error {
		cmd := (&app{ed: ed}).cmdSave()
		cmd.SetArgs(nil)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		return cmd.Execute()
	}
	t.Setenv("COOP_LAST_CMD", "")
	if err := run(); err == nil || !strings.Contains(err.Error(), "try coop save --recent") {
		t.Errorf("hooked: err = %v", err)
	}
	os.Unsetenv("COOP_LAST_CMD")
	if err := run(); err == nil || !strings.Contains(err.Error(), `eval "$(coop init zsh)"`) {
		t.Errorf("no hook: err = %v", err)
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

func TestSaveRefusesCoopSave(t *testing.T) {
	ed, home := saveEnv(t)
	for _, c := range []string{"coop save", "  coop save --name x"} {
		_, err := save(ed, home, "x\n\n", saveInput{Command: c, Dir: home})
		want := "previous command was coop save — run the command you want to save first, then coop save"
		if err == nil || err.Error() != want {
			t.Errorf("command %q: err = %v, want %q", c, err, want)
		}
	}
	if _, err := save(ed, home, "x\n\n", saveInput{Command: "coop saved-thing", Dir: home}); err != nil {
		t.Errorf("coop saved-thing refused: %v", err)
	}
}

// TestSaveNoWarningForCoopWrapper: the snippet's own coop wrapper and
// __coop_* helpers are zsh functions but must not trigger the warning.
func TestSaveNoWarningForCoopWrapper(t *testing.T) {
	ed, home := saveEnv(t)
	out, err := save(ed, home, "\n\n", saveInput{Command: "coop list && __coop_addhistory x && mk", Funcs: "coop __coop_addhistory mk", Dir: home})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, `"coop" is a zsh function`) || strings.Contains(out, `"__coop_addhistory" is a zsh function`) {
		t.Errorf("false warning: %q", out)
	}
	if !strings.Contains(out, `"mk" is a zsh function`) {
		t.Errorf("real function not warned: %q", out)
	}
}

// fakePick returns a picker that records the rows it was shown and picks
// index i (or cancels when i < 0).
func fakePick(i int, shown *[]capture.Recent) func([]capture.Recent) (capture.Recent, bool, error) {
	return func(rows []capture.Recent) (capture.Recent, bool, error) {
		*shown = rows
		if i < 0 {
			return capture.Recent{}, false, nil
		}
		return rows[i], true, nil
	}
}

func TestSaveRecentPicked(t *testing.T) {
	ed, home := saveEnv(t)
	rows := []capture.Recent{
		{PID: 1, When: time.Now(), Dir: "/srv", Command: "echo other"},
		{PID: 2, When: time.Now(), Dir: home + "/work", Command: "k get \\\n  pods"},
	}
	var shown []capture.Recent
	si := saveInput{Command: "echo this-tab", Dir: home, Aliases: "alias k=kubectl"}
	var out bytes.Buffer
	if err := runSaveRecent(strings.NewReader("pods\n\n"), &out, ed, home, si, rows, fakePick(1, &shown)); err != nil {
		t.Fatal(err)
	}
	if len(shown) != 2 {
		t.Errorf("picker shown %d rows", len(shown))
	}
	if !strings.Contains(out.String(), "  kubectl get \\\n  pods\n") {
		t.Errorf("output = %q", out.String())
	}
	s := load(t, ed).Scripts["pods"]
	if !s.Literal || s.Command != "kubectl get \\\n  pods" || s.Dir != "~/work" {
		t.Errorf("script = %+v", s)
	}
}

func TestSaveRecentNoRows(t *testing.T) {
	ed, home := saveEnv(t)
	before, _ := os.ReadFile(ed.Path)
	var out bytes.Buffer
	var shown []capture.Recent
	if err := runSaveRecent(strings.NewReader(""), &out, ed, home, saveInput{}, nil, fakePick(0, &shown)); err != nil {
		t.Fatal(err)
	}
	if out.String() != "no other open tabs have a command yet\n" {
		t.Errorf("output = %q", out.String())
	}
	if shown != nil {
		t.Error("picker opened with no rows")
	}
	if after, _ := os.ReadFile(ed.Path); string(after) != string(before) {
		t.Error("config changed")
	}
}

func TestSaveRecentCancel(t *testing.T) {
	ed, home := saveEnv(t)
	before, _ := os.ReadFile(ed.Path)
	rows := []capture.Recent{{PID: 1, When: time.Now(), Dir: home, Command: "echo x"}}
	var out bytes.Buffer
	var shown []capture.Recent
	if err := runSaveRecent(strings.NewReader("x\n\n"), &out, ed, home, saveInput{}, rows, fakePick(-1, &shown)); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("output = %q", out.String())
	}
	if after, _ := os.ReadFile(ed.Path); string(after) != string(before) {
		t.Error("config changed on cancel")
	}
}

func TestSaveRecentWithCommandErrors(t *testing.T) {
	ed, _ := saveEnv(t)
	cmd := (&app{ed: ed}).cmdSave()
	cmd.SetArgs([]string{"--recent", "--", "echo", "hi"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil || err.Error() != "--recent can't be combined with a command" {
		t.Fatalf("err = %v", err)
	}
}

// TestShellPID: without a usable COOP_SHELL_PID the excluded tab is coop's
// parent, the shell that ran it.
func TestShellPID(t *testing.T) {
	for in, want := range map[string]int{"4242": 4242, "": os.Getppid(), "x": os.Getppid(), "0": os.Getppid(), "-3": os.Getppid()} {
		if got := shellPID(in); got != want {
			t.Errorf("shellPID(%q) = %d, want %d", in, got, want)
		}
	}
}
