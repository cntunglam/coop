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

func TestSetHealthPersists(t *testing.T) {
	m := NewManagerAt(t.TempDir())
	spec := sleepSpec("svc", 30)
	spec.Health = &script.HealthSpec{Kind: "command", Target: "true", Interval: 10}
	if _, err := m.Start(spec); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop("svc", 2*time.Second) })

	meta, _ := m.Get("svc")
	if meta.Health == nil || !meta.Health.Configured || meta.Health.State != HealthUnknown {
		t.Fatalf("Start should init health unknown, got %+v", meta.Health)
	}
	if err := m.SetHealth("svc", HealthHealthy); err != nil {
		t.Fatal(err)
	}
	meta, _ = m.Get("svc")
	if meta.Health.State != HealthHealthy || meta.Health.LastCheck == 0 {
		t.Errorf("SetHealth not persisted: %+v", meta.Health)
	}
}

func TestHealthDisplayNotMonitored(t *testing.T) {
	// Configured but never probed -> not monitored.
	s := Status{Meta: Meta{Health: &HealthState{Configured: true, State: HealthUnknown, Interval: 10}}}
	if got := s.HealthDisplay(); got != "" {
		t.Errorf("unprobed = %q, want empty", got)
	}
	// No health at all -> empty.
	if got := (Status{}).HealthDisplay(); got != "" {
		t.Errorf("no health = %q, want empty", got)
	}
}

func TestHealthDisplayFreshVsStale(t *testing.T) {
	now := time.Now().Unix()
	fresh := Status{Meta: Meta{Health: &HealthState{
		Configured: true, State: HealthHealthy, Interval: 10, LastCheck: now,
	}}}
	if got := fresh.HealthDisplay(); got != HealthHealthy {
		t.Errorf("fresh = %q, want healthy", got)
	}
	stale := Status{Meta: Meta{Health: &HealthState{
		Configured: true, State: HealthHealthy, Interval: 10, LastCheck: now - 100,
	}}}
	if got := stale.HealthDisplay(); got != "" {
		t.Errorf("stale = %q, want empty (not monitored)", got)
	}
}

func TestHealthDisplayFailedNeverBlanksAfterStaleness(t *testing.T) {
	// A failed member stops being probed (the supervisor gives up on it), so
	// LastCheck freezes and would normally exceed the 3*Interval staleness
	// window within ~30s. HealthDisplay must keep reporting "failed" anyway —
	// it is a terminal state, not a "nobody is watching this" signal.
	now := time.Now().Unix()
	s := Status{Meta: Meta{Health: &HealthState{
		Configured: true, State: HealthFailed, Interval: 10, LastCheck: now - 100,
	}}}
	if got := s.HealthDisplay(); got != HealthFailed {
		t.Errorf("stale failed = %q, want %q (failed must always be shown)", got, HealthFailed)
	}
}
