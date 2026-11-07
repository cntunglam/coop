package capture

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runZsh drives an interactive zsh with the snippet loaded and returns the
// COOP_* environment that the fake `coop` binary saw on `coop save`.
func runZsh(t *testing.T, script func(work string) string) (env, work string) {
	t.Helper()
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh not installed")
	}
	bin := t.TempDir()
	out := filepath.Join(t.TempDir(), "out")
	// env -0 keeps multi-line values intact (a grep over lines would not).
	fake := "#!/bin/sh\nenv -0 > \"$OUT\"\n"
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
	cmd := exec.Command(zsh, "-f", "-i")
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "OUT="+out, "HISTSIZE=100")
	cmd.Stdin = strings.NewReader("eval \"$(cat " + snippet + ")\"\ncd " + work + "\nalias k=kubectl\n" + script(work) + "coop save\nexit\n")
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
	return coop.String(), work
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
