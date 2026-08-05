package tui

import (
	"bufio"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestHelperSignal is not a real test: when RX_SIG_HELPER=1 it runs the helper
// process for TestSuspendSignalIsolation. It mirrors suspendRun's signal setup —
// catch and drain SIGINT — then runs a child and waits for it. If SIGINT reaches
// the group, the child (default disposition) must die and this parent must live.
func TestHelperSignal(t *testing.T) {
	if os.Getenv("RX_SIG_HELPER") != "1" {
		return
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() {
		for range sig {
		} // swallow, like rx during a suspended command
	}()

	cmd := exec.Command("sleep", "10")
	if err := cmd.Start(); err != nil {
		os.Stdout.WriteString("STARTERR " + err.Error() + "\n")
		os.Exit(2)
	}
	os.Stdout.WriteString("READY\n") // child + handler are up
	os.Stdout.Sync()

	err := cmd.Wait() // returns once the child is killed by the group SIGINT
	os.Stdout.WriteString("CHILDEXIT " + errString(err) + "\n")
	os.Stdout.WriteString("PARENTALIVE\n")
	os.Stdout.Sync()
	os.Exit(0)
}

func errString(err error) string {
	if err == nil {
		return "nil"
	}
	return err.Error()
}

// TestSuspendSignalIsolation verifies the mechanism suspendRun relies on: a
// Ctrl-C (SIGINT) delivered to the foreground process group kills the child but
// not the parent that catches and drains SIGINT — so rx survives and returns to
// the UI while the stuck command dies.
func TestSuspendSignalIsolation(t *testing.T) {
	helper := exec.Command(os.Args[0], "-test.run=TestHelperSignal", "-test.v")
	helper.Env = append(os.Environ(), "RX_SIG_HELPER=1")
	// Put the helper in its own process group so we can signal the whole group
	// (helper + its child) without hitting the test runner.
	helper.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	pgid := helper.Process.Pid // group leader id == its pid

	lines := make(chan string, 8)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()

	// Wait for the helper to signal it is ready (child started, handler installed).
	if !waitLine(t, lines, "READY", 5*time.Second) {
		t.Fatal("helper never reported READY")
	}

	// Simulate a terminal Ctrl-C to the whole foreground group.
	if err := syscall.Kill(-pgid, syscall.SIGINT); err != nil {
		t.Fatalf("kill group: %v", err)
	}

	// The parent must survive and report PARENTALIVE, and exit 0.
	if !waitLine(t, lines, "PARENTALIVE", 5*time.Second) {
		_ = helper.Process.Kill()
		t.Fatal("parent did not survive SIGINT (never printed PARENTALIVE)")
	}
	if err := helper.Wait(); err != nil {
		t.Fatalf("helper should exit 0 after surviving SIGINT, got: %v", err)
	}
}

func waitLine(t *testing.T, lines <-chan string, want string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return false
			}
			t.Logf("helper: %s", line)
			if strings.HasPrefix(line, want) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}
