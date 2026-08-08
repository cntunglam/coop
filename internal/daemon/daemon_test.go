package daemon

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/cntunglam/coop/internal/script"
)

func sleepSpec(name string, seconds int) *script.Spec {
	return &script.Spec{
		Name:       name,
		Argv:       []string{"sh", "-c", fmt.Sprintf("sleep %d", seconds)},
		Env:        []string{},
		Background: true,
		Display:    fmt.Sprintf("sleep %d", seconds),
	}
}

func TestStartListStop(t *testing.T) {
	m := NewManagerAt(t.TempDir())
	spec := sleepSpec("sleeper", 30)

	meta, err := m.Start(spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if meta.PID <= 0 {
		t.Fatalf("bad pid %d", meta.PID)
	}
	t.Cleanup(func() { _ = m.Stop("sleeper", 2*time.Second) })

	alive, _, err := m.IsRunning("sleeper")
	if err != nil || !alive {
		t.Fatalf("expected alive, got alive=%v err=%v", alive, err)
	}

	list, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "sleeper" || !list[0].Alive {
		t.Fatalf("unexpected list: %+v", list)
	}

	if err := m.Stop("sleeper", 3*time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// After stop, the state dir is gone, so it is no longer known.
	if _, _, err := m.IsRunning("sleeper"); err == nil {
		t.Fatal("expected error for stopped/removed daemon")
	}
}

func TestIdempotentStart(t *testing.T) {
	m := NewManagerAt(t.TempDir())
	spec := sleepSpec("dup", 30)
	if _, err := m.Start(spec); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop("dup", 2*time.Second) })

	_, err := m.Start(spec)
	if err == nil {
		t.Fatal("expected second Start to be refused (idempotency)")
	}
}

func TestWaitReadyPort(t *testing.T) {
	// Open a real listener, record its port, and confirm WaitReady detects it.
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	m := NewManagerAt(t.TempDir())
	meta := &Meta{Name: "x", ReadyPort: port}
	if err := m.WaitReady(meta, 0, 2*time.Second); err != nil {
		t.Fatalf("WaitReady on open port: %v", err)
	}
}

func TestWaitReadyPortTimeout(t *testing.T) {
	// Find a definitely-closed port by opening then closing a listener.
	ln, _ := net.Listen("tcp", "localhost:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	m := NewManagerAt(t.TempDir())
	meta := &Meta{Name: "x", ReadyPort: port}
	if err := m.WaitReady(meta, 0, 600*time.Millisecond); err == nil {
		t.Fatal("expected timeout error on closed port")
	}
}

func TestStopUnknown(t *testing.T) {
	m := NewManagerAt(t.TempDir())
	if err := m.Stop("nope", time.Second); err == nil {
		t.Fatal("expected error stopping unknown daemon")
	}
}
