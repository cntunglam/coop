package capture

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// zshRun configures one interactive zsh session for runZshWith.
type zshRun struct {
	pre    string              // lines run before the snippet is loaded
	script func(string) string // lines run (given the work dir) before last
	last   string              // final line; defaults to "coop save"
	env    []string            // extra environment
}

// runZsh drives an interactive zsh with the snippet loaded and returns the
// COOP_* environment that the fake `coop` binary saw on `coop save`.
func runZsh(t *testing.T, script func(work string) string) (env, work string) {
	t.Helper()
	env, _, work = runZshWith(t, zshRun{script: script})
	return env, work
}

// runZshWith runs r and returns the COOP_* environment and the arguments
// (one per line) that the fake `coop` binary saw on its last run.
func runZshWith(t *testing.T, r zshRun) (env, args, work string) {
	t.Helper()
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh not installed")
	}
	bin := t.TempDir()
	out := filepath.Join(t.TempDir(), "out")
	// env -0 keeps multi-line values intact (a grep over lines would not).
	fake := "#!/bin/sh\nenv -0 > \"$OUT\"\nprintf '%s\\n' \"$@\" > \"$OUT.args\"\n"
	if err := os.WriteFile(filepath.Join(bin, "coop"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	snippet := filepath.Join(t.TempDir(), "init.zsh")
	if err := os.WriteFile(snippet, []byte(ZshInit()), 0o644); err != nil {
		t.Fatal(err)
	}
	work, err = filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	body := ""
	if r.script != nil {
		body = r.script(work)
	}
	last := r.last
	if last == "" {
		last = "coop save"
	}
	cmd := exec.Command(zsh, "-f", "-i")
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "OUT="+out, "HISTSIZE=100")
	cmd.Env = append(cmd.Env, r.env...)
	cmd.Stdin = strings.NewReader(r.pre + "eval \"$(cat " + snippet + ")\"\ncd " + work + "\nalias k=kubectl\n" + body + last + "\nexit\n")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("zsh: %v\n%s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("fake coop never ran: %v", err)
	}
	var coop strings.Builder
	for _, kv := range strings.Split(string(b), "\x00") {
		if strings.HasPrefix(kv, "COOP_") {
			coop.WriteString(kv + "\n")
		}
	}
	a, err := os.ReadFile(out + ".args")
	if err != nil {
		t.Fatal(err)
	}
	return coop.String(), string(a), work
}

func TestZshInitCapturesPreviousCommand(t *testing.T) {
	env, work := runZsh(t, func(string) string { return "echo 'k get pods'\n" })
	for _, want := range []string{
		"COOP_LAST_CMD=echo 'k get pods'\n",
		"COOP_PWD=" + work + "\n",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("missing %q in:\n%s", want, env)
		}
	}
	if !strings.Contains(env, "k=kubectl") {
		t.Errorf("COOP_ALIASES lacks k=kubectl:\n%s", env)
	}
	if !strings.Contains(env, "COOP_FUNCS=") {
		t.Errorf("COOP_FUNCS not set:\n%s", env)
	}
}

func TestZshInitMultiline(t *testing.T) {
	env, _ := runZsh(t, func(string) string { return "echo a \\\n  b\n" })
	if !strings.Contains(env, "COOP_LAST_CMD=echo a \\\n  b\n") {
		t.Errorf("multi-line command not preserved verbatim:\n%q", env)
	}
}

func TestZshInitSaveTwiceKeepsOriginal(t *testing.T) {
	env, _ := runZsh(t, func(string) string { return "echo original\ncoop save\n" })
	if !strings.Contains(env, "COOP_LAST_CMD=echo original\n") {
		t.Errorf("second coop save lost the original command:\n%s", env)
	}
}

// TestZshInitIgnoresSharedHistory: a line another shell appends to $HISTFILE
// under SHARE_HISTORY is not mistaken for this shell's previous command.
func TestZshInitIgnoresSharedHistory(t *testing.T) {
	hist := filepath.Join(t.TempDir(), "histfile")
	local := `echo local; sh -c 'printf ": 1700000000:0;echo foreign\n" >> "$HISTFILE"'`
	env, _, _ := runZshWith(t, zshRun{
		pre:    "setopt SHARE_HISTORY\n",
		script: func(string) string { return local + "\n" },
		env:    []string{"HISTFILE=" + hist, "SAVEHIST=100"},
	})
	if !strings.Contains(env, "COOP_LAST_CMD="+local+"\n") {
		t.Errorf("want the local command captured, got:\n%s", env)
	}
	if b, _ := os.ReadFile(hist); !strings.Contains(string(b), "echo foreign") {
		t.Errorf("foreign line never reached the history file:\n%s", b)
	}
}

func TestZshInitHistIgnoreSpace(t *testing.T) {
	env, _, _ := runZshWith(t, zshRun{
		pre:    "setopt HIST_IGNORE_SPACE\n",
		script: func(string) string { return " echo secret\n" },
	})
	if !strings.Contains(env, "COOP_LAST_CMD= echo secret\n") {
		t.Errorf("space-prefixed command not captured:\n%s", env)
	}
}

func TestZshInitPassesOtherVerbsThrough(t *testing.T) {
	_, args, _ := runZshWith(t, zshRun{last: "coop list a b"})
	if args != "list\na\nb\n" {
		t.Errorf("args = %q, want list a b", args)
	}
}

// TestZshInitKeepsUserHistoryHook: a zshaddhistory hook defined before the
// snippet still runs, and the snippet still captures.
func TestZshInitKeepsUserHistoryHook(t *testing.T) {
	mark := filepath.Join(t.TempDir(), "mark")
	env, _, _ := runZshWith(t, zshRun{
		pre:    "zshaddhistory() { print -r -- \"${1%$'\\n'}\" >> \"$MARK\"; }\n",
		script: func(string) string { return "echo hooked\n" },
		env:    []string{"MARK=" + mark},
	})
	b, err := os.ReadFile(mark)
	if err != nil || !strings.Contains(string(b), "echo hooked\n") {
		t.Errorf("user hook did not run (err %v):\n%s", err, b)
	}
	if !strings.Contains(env, "COOP_LAST_CMD=echo hooked\n") {
		t.Errorf("command not captured alongside the user hook:\n%s", env)
	}
}
