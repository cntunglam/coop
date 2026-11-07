package capture

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	// A private XDG_STATE_HOME keeps shell records out of the real state dir;
	// r.env may override it (exec keeps the last duplicate).
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "OUT="+out, "HISTSIZE=100",
		"XDG_STATE_HOME="+t.TempDir())
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

// shellRecords checks every per-shell record file under state is 0600 and
// returns their names. (Contents end up as the harness's final "exit".)
func shellRecords(t *testing.T, state string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(state, "coop", "shells"))
	if err != nil {
		t.Fatalf("shells dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s mode = %o, want 600", e.Name(), perm)
		}
		names = append(names, e.Name())
	}
	return names
}

// runRecorded runs script in a zsh with XDG_STATE_HOME=state and returns the
// shell's record as it stood right after the final `coop save` line, split
// into its NUL-separated fields, plus the shell's PID and work dir.
func runRecorded(t *testing.T, state, script string) (fields []string, pid, work string) {
	t.Helper()
	snap := filepath.Join(t.TempDir(), "record")
	env, _, work := runZshWith(t, zshRun{
		pre:    "umask 022\n",
		script: func(string) string { return script },
		last:   `coop save; cp "$XDG_STATE_HOME/coop/shells/$$" ` + snap,
		env:    []string{"XDG_STATE_HOME=" + state},
	})
	for _, line := range strings.Split(env, "\n") {
		if v, ok := strings.CutPrefix(line, "COOP_SHELL_PID="); ok {
			pid = v
		}
	}
	if pid == "" {
		t.Fatalf("COOP_SHELL_PID not passed:\n%s", env)
	}
	b, err := os.ReadFile(snap)
	if err != nil {
		t.Fatalf("no record: %v", err)
	}
	fields = strings.Split(string(b), "\x00")
	if len(fields) != 4 || fields[3] != "" {
		t.Fatalf("record = %q", b)
	}
	if _, err := strconv.ParseInt(fields[0], 10, 64); err != nil {
		t.Errorf("epoch = %q", fields[0])
	}
	return fields, pid, work
}

// TestZshInitWritesShellRecord: each recorded line also lands in this shell's
// record file (0600; epoch, PWD and the exact command, NUL-separated), and a
// `coop save` line does not overwrite it.
func TestZshInitWritesShellRecord(t *testing.T) {
	state := t.TempDir()
	multi := "echo a \\\n  b"
	f, pid, work := runRecorded(t, state, "echo first\n"+multi+"\n")
	if f[1] != work || f[2] != multi {
		t.Errorf("pwd, cmd = %q, %q; want %q, %q", f[1], f[2], work, multi)
	}
	if names := shellRecords(t, state); len(names) != 1 || names[0] != pid {
		t.Errorf("record files = %q, want [%s]", names, pid)
	}
}

func TestZshInitShellRecordPerShell(t *testing.T) {
	state := t.TempDir()
	f1, pid1, _ := runRecorded(t, state, "echo one\n")
	f2, pid2, _ := runRecorded(t, state, "echo two\n")
	if f1[2] != "echo one" || f2[2] != "echo two" {
		t.Errorf("commands = %q, %q", f1[2], f2[2])
	}
	names := shellRecords(t, state)
	if len(names) != 2 || pid1 == pid2 {
		t.Errorf("record files = %q (pids %s, %s), want two", names, pid1, pid2)
	}
}

// TestZshInitKeepsUmask: writing the record must not change the shell's umask.
func TestZshInitKeepsUmask(t *testing.T) {
	state := t.TempDir()
	out := filepath.Join(t.TempDir(), "umask")
	runZshWith(t, zshRun{
		pre:    "umask 022\n",
		script: func(string) string { return "echo x\numask > " + out + "\n" },
		env:    []string{"XDG_STATE_HOME=" + state},
	})
	if b, _ := os.ReadFile(out); strings.TrimSpace(string(b)) != "022" {
		t.Errorf("umask after hook = %q, want 022", b)
	}
}
