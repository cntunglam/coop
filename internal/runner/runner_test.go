package runner

import (
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cntunglam/coop/internal/config"
	"github.com/cntunglam/coop/internal/daemon"
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

// eventually polls cond until true or the timeout elapses.
func eventually(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func mustParseCfg(t *testing.T, yaml string) *config.Config {
	t.Helper()
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	return cfg
}

func TestSuperviseRestartsUnhealthy(t *testing.T) {
	dm := daemon.NewManagerAt(t.TempDir())
	cfg := mustParseCfg(t, `
scripts:
  svc:
    command: "exec sleep 30"
    background: true
    health:
      command: "false"
      interval: 1
      timeout: 1
      failures: 1
      max_restarts: 10
groups:
  g:
    members:
      - { script: svc }
`)
	r := New(cfg, dm)
	r.Status = io.Discard
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- r.Supervise("g", stop) }()
	t.Cleanup(func() {
		close(stop)
		<-done
		_ = dm.Stop("svc", 2*time.Second)
	})

	var pid0 int
	if !eventually(t, 4*time.Second, func() bool {
		m, err := dm.Get("svc")
		if err != nil {
			return false
		}
		pid0 = m.PID
		return pid0 > 0
	}) {
		t.Fatal("daemon never started")
	}
	if !eventually(t, 8*time.Second, func() bool {
		m, err := dm.Get("svc")
		return err == nil && m.PID != pid0
	}) {
		t.Error("unhealthy daemon was not restarted (PID unchanged)")
	}
}

func TestSuperviseGivesUpAfterMaxRestarts(t *testing.T) {
	dm := daemon.NewManagerAt(t.TempDir())
	cfg := mustParseCfg(t, `
scripts:
  svc:
    command: "exec sleep 30"
    background: true
    health:
      command: "false"
      interval: 1
      timeout: 1
      failures: 1
      max_restarts: 2
      restart_window: 60
groups:
  g:
    members:
      - { script: svc }
`)
	r := New(cfg, dm)
	r.Status = io.Discard
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- r.Supervise("g", stop) }()
	t.Cleanup(func() {
		close(stop)
		<-done
		_ = dm.Stop("svc", 2*time.Second)
	})

	if !eventually(t, 12*time.Second, func() bool {
		m, err := dm.Get("svc")
		return err == nil && m.Health != nil && m.Health.State == daemon.HealthFailed
	}) {
		t.Error("member should be marked failed after exceeding max_restarts")
	}
}

func TestSuperviseHealthyNoRestart(t *testing.T) {
	dm := daemon.NewManagerAt(t.TempDir())
	cfg := mustParseCfg(t, `
scripts:
  svc:
    command: "exec sleep 30"
    background: true
    health:
      command: "true"
      interval: 1
      timeout: 1
      failures: 1
groups:
  g:
    members:
      - { script: svc }
`)
	r := New(cfg, dm)
	r.Status = io.Discard
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- r.Supervise("g", stop) }()
	t.Cleanup(func() {
		close(stop)
		<-done
		_ = dm.Stop("svc", 2*time.Second)
	})

	var pid0 int
	eventually(t, 4*time.Second, func() bool {
		m, err := dm.Get("svc")
		if err != nil {
			return false
		}
		pid0 = m.PID
		return pid0 > 0
	})
	// Wait for at least one healthy probe.
	if !eventually(t, 4*time.Second, func() bool {
		m, err := dm.Get("svc")
		return err == nil && m.Health != nil && m.Health.State == daemon.HealthHealthy
	}) {
		t.Fatal("member never reported healthy")
	}
	// Give it more cycles; PID must not change (no restart).
	time.Sleep(3 * time.Second)
	m, err := dm.Get("svc")
	if err != nil || m.PID != pid0 {
		t.Errorf("healthy member should not restart: pid0=%d now=%v err=%v", pid0, m, err)
	}
}

// TestSuperviseBadHealthProbeAbandonsMember covers a runtime-only failure mode:
// a templated tcp target (e.g. "{{.host}}") passes config validation (it's a
// non-empty template string) but renders to a non-numeric value, so
// health.Build fails at Supervise time. Before the fix, ms.probe stayed nil in
// that case, which made the member look "legacy" (no health block) and gave it
// UNCAPPED dead-PID auto-restarts, bypassing the crash-loop cap entirely. The
// fix marks the member failed instead, so it is never touched again.
func TestSuperviseBadHealthProbeAbandonsMember(t *testing.T) {
	dm := daemon.NewManagerAt(t.TempDir())
	cfg := mustParseCfg(t, `
scripts:
  svc:
    command: "exec sleep 1"
    background: true
    presets:
      p: { host: localhost }
    health:
      tcp: "{{.host}}"
      interval: 1
      timeout: 1
      failures: 1
groups:
  g:
    auto_restart: true
    members:
      - { script: svc, preset: p }
`)
	r := New(cfg, dm)
	r.Status = io.Discard
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- r.Supervise("g", stop) }()
	t.Cleanup(func() {
		close(stop)
		<-done
		_ = dm.Stop("svc", 2*time.Second)
	})

	// Capture the baseline PID from StartGroup's initial launch, independent of
	// whether the failed-marking assertion below passes — this is the anchor
	// for proving "never restarted" even if that assertion were to regress.
	var pid0 int
	if !eventually(t, 4*time.Second, func() bool {
		m, err := dm.Get("svc")
		if err != nil || m.PID == 0 {
			return false
		}
		pid0 = m.PID
		return true
	}) {
		t.Fatal("daemon never started")
	}

	// The bad probe should be detected quickly and the member marked failed.
	// Non-fatal: even if this regresses, the restart-suppression check below
	// should still independently catch an uncapped-legacy-restart regression.
	if !eventually(t, 4*time.Second, func() bool {
		m, err := dm.Get("svc")
		return err == nil && m.Health != nil && m.Health.State == daemon.HealthFailed
	}) {
		t.Error("member with an unbuildable health probe should be marked failed")
	}

	// The script dies on its own after ~1s. If it were (wrongly) treated as a
	// legacy auto-restart member, the supervisor's once-a-second loop would
	// bring it back up with a fresh PID via dm.Start. Wait well past that and
	// confirm both that it is not alive AND that its PID never changed — PID
	// equality is what actually discriminates "abandoned" from "restarted,
	// and its short-lived replacement happened to be dead again when we
	// looked" (IsRunning alone can't tell those apart at this timing).
	time.Sleep(3 * time.Second)
	m1, err := dm.Get("svc")
	if err != nil {
		t.Fatalf("dm.Get after wait: %v", err)
	}
	if m1.PID != pid0 {
		t.Errorf("failed member must not be restarted: pid was %d, now %d (proves uncapped legacy restart path was taken)", pid0, m1.PID)
	}
	if alive, _, _ := dm.IsRunning("svc"); alive {
		t.Error("failed member must not be restarted (process should have died naturally and stayed down)")
	}
}
