package runner

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/caongoctunglam/rx/internal/config"
	"github.com/caongoctunglam/rx/internal/daemon"
)

func newTestRunner(t *testing.T, cfg *config.Config) (*Runner, *bytes.Buffer) {
	t.Helper()
	dm := daemon.NewManagerAt(t.TempDir())
	r := New(cfg, dm)
	var out bytes.Buffer
	r.Stdout = &out
	r.Stderr = &out
	r.Status = &out
	return r, &out
}

func TestForegroundScriptSuccess(t *testing.T) {
	cfg, err := config.Parse([]byte(`
scripts:
  greet:
    command: "echo hello {{.who}}"
    presets:
      world: { who: world }
`))
	if err != nil {
		t.Fatal(err)
	}
	r, out := newTestRunner(t, cfg)
	if err := r.RunScript("greet", "world"); err != nil {
		t.Fatalf("RunScript: %v", err)
	}
	if !strings.Contains(out.String(), "hello world") {
		t.Fatalf("output missing greeting: %q", out.String())
	}
}

func TestForegroundScriptFailure(t *testing.T) {
	cfg, _ := config.Parse([]byte(`
scripts:
  boom:
    command: "exit 3"
`))
	r, _ := newTestRunner(t, cfg)
	if err := r.RunScript("boom", ""); err == nil {
		t.Fatal("expected non-zero exit to surface as error")
	}
}

func TestWorkflowStopsOnFailure(t *testing.T) {
	dir := t.TempDir()
	markerA := filepath.Join(dir, "a")
	markerC := filepath.Join(dir, "c")
	cfg, err := config.Parse([]byte(`
scripts:
  stepA:
    command: "touch ` + markerA + `"
  stepB:
    command: "exit 1"
  stepC:
    command: "touch ` + markerC + `"
workflows:
  flow:
    steps:
      - { script: stepA }
      - { script: stepB }
      - { script: stepC }
`))
	if err != nil {
		t.Fatal(err)
	}
	r, _ := newTestRunner(t, cfg)
	err = r.RunWorkflow("flow")
	if err == nil {
		t.Fatal("expected workflow to fail at stepB")
	}
	if !strings.Contains(err.Error(), "step 2") {
		t.Errorf("error should name step 2: %v", err)
	}
	if _, e := os.Stat(markerA); e != nil {
		t.Errorf("stepA should have run: %v", e)
	}
	if _, e := os.Stat(markerC); e == nil {
		t.Errorf("stepC must NOT have run after stepB failed")
	}
}

func TestBackgroundStepWaitsForPort(t *testing.T) {
	// nc-style listener via sh: use a tiny python http server on a fixed port is
	// flaky in CI; instead pick a free port by binding, closing, and letting the
	// script re-bind it. To keep this hermetic we use `sh` to open the port with
	// a bash-less approach: python3 is broadly available on darwin.
	port := freePort(t)
	cfg, err := config.Parse([]byte(`
scripts:
  server:
    command: "exec python3 -m http.server ` + itoa(port) + ` --bind 127.0.0.1"
    background: true
    ready_port: "` + itoa(port) + `"
`))
	if err != nil {
		t.Fatal(err)
	}
	r, out := newTestRunner(t, cfg)
	t.Cleanup(func() { _ = r.dm.Stop("server", 2*time.Second) })

	done := make(chan error, 1)
	go func() { done <- r.RunScript("server", "") }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("background step failed: %v (output: %s)", err, out.String())
		}
	case <-time.After(ReadyTimeout + 5*time.Second):
		t.Fatal("background step did not return in time")
	}
	if !strings.Contains(out.String(), "ready.") {
		t.Errorf("expected readiness confirmation, got: %s", out.String())
	}
}

func TestWorkflowRerunToleratesRunningDaemon(t *testing.T) {
	port := freePort(t)
	cfg, err := config.Parse([]byte(`
scripts:
  server:
    command: "exec python3 -m http.server ` + itoa(port) + ` --bind 127.0.0.1"
    background: true
    ready_port: "` + itoa(port) + `"
  hit:
    command: "echo hit-ok"
workflows:
  flow:
    steps:
      - { script: server }
      - { script: hit }
`))
	if err != nil {
		t.Fatal(err)
	}
	r, _ := newTestRunner(t, cfg)
	t.Cleanup(func() { _ = r.dm.Stop("server", 2*time.Second) })

	// First run starts the daemon and completes.
	if err := runWithTimeout(t, func() error { return r.RunWorkflow("flow") }); err != nil {
		t.Fatalf("first run: %v", err)
	}
	// Second run WITHOUT stopping the daemon must NOT fail on the already-running
	// background step — it should be tolerated and the workflow complete again.
	if err := runWithTimeout(t, func() error { return r.RunWorkflow("flow") }); err != nil {
		t.Fatalf("second run should tolerate the running daemon, got: %v", err)
	}
}

func TestGroupStartStop(t *testing.T) {
	cfg, err := config.Parse([]byte(`
scripts:
  a:
    command: "exec sleep 30"
    background: true
  b:
    command: "exec sleep 30"
    background: true
groups:
  stack:
    members:
      - { script: a }
      - { script: b }
`))
	if err != nil {
		t.Fatal(err)
	}
	r, _ := newTestRunner(t, cfg)
	t.Cleanup(func() { _ = r.StopGroup("stack") })

	if err := r.StartGroup("stack"); err != nil {
		t.Fatalf("StartGroup: %v", err)
	}
	for _, name := range []string{"a", "b"} {
		alive, meta, _ := r.dm.IsRunning(name)
		if !alive {
			t.Fatalf("member %s not running after StartGroup", name)
		}
		if meta.Group != "stack" {
			t.Errorf("member %s group = %q, want stack", name, meta.Group)
		}
	}

	// Re-running the group tolerates already-running members.
	if err := r.StartGroup("stack"); err != nil {
		t.Fatalf("re-StartGroup should tolerate running members: %v", err)
	}

	if err := r.StopGroup("stack"); err != nil {
		t.Fatalf("StopGroup: %v", err)
	}
	for _, name := range []string{"a", "b"} {
		if alive, _, _ := r.dm.IsRunning(name); alive {
			t.Errorf("member %s still running after StopGroup", name)
		}
	}
}

// runWithTimeout runs fn and fails the test if it does not return promptly.
func runWithTimeout(t *testing.T, fn func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(ReadyTimeout + 5*time.Second):
		t.Fatal("timed out")
		return nil
	}
}

// freePort returns a currently-free TCP port.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func itoa(i int) string { return strconv.Itoa(i) }
