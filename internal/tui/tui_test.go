package tui

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/caongoctunglam/rx/internal/config"
	"github.com/caongoctunglam/rx/internal/daemon"
	"github.com/caongoctunglam/rx/internal/runner"
)

func testUI(t *testing.T, yaml string) *UI {
	t.Helper()
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	dm := daemon.NewManagerAt(t.TempDir())
	run := runner.New(cfg, dm)
	u := &UI{
		app:     tview.NewApplication(),
		cfg:     cfg,
		dm:      dm,
		run:     run,
		cfgPath: cfgPath,
		done:    make(chan struct{}),
	}
	u.build()
	return u
}

func TestBuildCatalog(t *testing.T) {
	u := testUI(t, `
scripts:
  alpha:
    command: "echo a"
  proxy:
    command: "echo p"
    background: true
    presets:
      prod: { x: "1" }
workflows:
  flow:
    steps:
      - { script: alpha }
`)
	if len(u.entries) != 3 {
		t.Fatalf("expected 3 catalog entries, got %d: %+v", len(u.entries), u.entries)
	}
	// Scripts sorted first (alpha, proxy), then workflows (flow).
	want := []struct {
		name string
		kind entryKind
	}{
		{"alpha", kindScript},
		{"proxy", kindScript},
		{"flow", kindWorkflow},
	}
	for i, w := range want {
		if u.entries[i].name != w.name || u.entries[i].kind != w.kind {
			t.Errorf("entry %d = %+v, want %+v", i, u.entries[i], w)
		}
	}
	// The daemon script should be tagged "daemon" in its KIND cell.
	if got := u.catalog.GetCell(2, 1).Text; got != "daemon" {
		t.Errorf("proxy KIND cell = %q, want daemon", got)
	}
}

func TestDaemonStateMapping(t *testing.T) {
	cases := []struct {
		st        daemon.Status
		wantText  string
		wantColor tcell.Color
	}{
		{daemon.Status{Alive: false}, "● dead", tcell.ColorRed},
		{daemon.Status{Alive: true, Meta: daemon.Meta{ReadyPort: 1}, Ready: true}, "● ready", tcell.ColorGreen},
		{daemon.Status{Alive: true, Meta: daemon.Meta{ReadyPort: 1}, Ready: false}, "● starting", tcell.ColorYellow},
		{daemon.Status{Alive: true}, "● running", tcell.ColorGreen},
	}
	for _, c := range cases {
		text, color := daemonState(c.st)
		if text != c.wantText || color != c.wantColor {
			t.Errorf("daemonState(%+v) = %q/%v, want %q/%v", c.st, text, color, c.wantText, c.wantColor)
		}
	}
}

func TestEmptyDaemonsPlaceholder(t *testing.T) {
	u := testUI(t, "scripts:\n  a:\n    command: echo hi\n")
	u.refreshDaemons()
	if got := u.daemons.GetCell(1, 0).Text; got != "(no daemons running)" {
		t.Errorf("expected placeholder row, got %q", got)
	}
	// With no live daemon, selectedDaemon must report none.
	if name, ok := u.selectedDaemon(); ok {
		t.Errorf("selectedDaemon should be false on placeholder, got %q", name)
	}
}

// TestRunAndQuit runs the full application against a simulation screen and quits
// after the first draw, verifying the UI constructs and tears down without panic.
func TestRunAndQuit(t *testing.T) {
	u := testUI(t, "scripts:\n  a:\n    command: echo hi\n")
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	u.app.SetScreen(screen)

	var once sync.Once
	u.app.SetAfterDrawFunc(func(tcell.Screen) {
		once.Do(func() { go u.quit() })
	})
	go u.refreshLoop()

	done := make(chan error, 1)
	go func() { done <- u.app.Run() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("app.Run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		u.app.Stop()
		t.Fatal("app did not quit within timeout")
	}
}

// TestStartAndStopDaemonViaKeys drives the real event loop against a simulation
// screen: it focuses the catalog, presses Enter on a preset-less background
// script (which starts a daemon), then presses `s` in the daemon pane to stop it.
// This exercises activateRow→launchScript→daemon start and daemonKeys→stopDaemon.
func TestStartAndStopDaemonViaKeys(t *testing.T) {
	u := testUI(t, "scripts:\n  bg:\n    command: \"sleep 30\"\n    background: true\n")
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	u.app.SetScreen(screen)
	t.Cleanup(func() { _ = u.dm.Stop("bg", 2*time.Second) })

	go u.refreshLoop()
	go func() { _ = u.app.Run() }()
	defer u.app.Stop()

	// Give the event loop a moment to start before injecting events.
	time.Sleep(200 * time.Millisecond)

	// Focus the catalog and select the "bg" row, then press Enter to start it.
	u.app.QueueUpdateDraw(func() {
		u.app.SetFocus(u.catalog)
		u.catalog.Select(1, 0)
	})
	u.app.QueueEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	waitFor(t, 8*time.Second, func() bool {
		alive, _, _ := u.dm.IsRunning("bg")
		return alive
	})
	if alive, _, _ := u.dm.IsRunning("bg"); !alive {
		t.Fatal("daemon 'bg' did not start after Enter")
	}

	// Refresh, focus, and select the daemon in one queued update so row 1 is the
	// live "bg" row (not the placeholder) before we inject 's'. Queued events run
	// in order, so the 's' below is processed after this update.
	done := make(chan struct{})
	u.app.QueueUpdateDraw(func() {
		u.refreshDaemons()
		u.app.SetFocus(u.daemons)
		u.daemons.Select(1, 0)
		close(done)
	})
	<-done
	u.app.QueueEvent(tcell.NewEventKey(tcell.KeyRune, 's', tcell.ModNone))

	waitFor(t, 8*time.Second, func() bool {
		alive, _, _ := u.dm.IsRunning("bg")
		return !alive
	})
	if alive, _, _ := u.dm.IsRunning("bg"); alive {
		t.Fatal("daemon 'bg' still running after 's'")
	}
}

// waitFor polls cond until true or the timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestSavePresetPersists(t *testing.T) {
	u := testUI(t, "scripts:\n  ssh:\n    command: \"ssh {{.user}}@{{.host}} -p {{.port}}\"\n")
	err := u.savePreset("ssh", "prod", map[string]string{"user": "root", "host": "h.example.com", "port": "22"})
	if err != nil {
		t.Fatalf("savePreset: %v", err)
	}
	// In-memory config updated.
	if _, ok := u.cfg.Scripts["ssh"].Presets["prod"]; !ok {
		t.Fatal("preset not present in memory")
	}
	// File on disk updated and reloadable.
	reloaded, err := config.Load(u.cfgPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := reloaded.Scripts["ssh"].Presets["prod"]
	if got["user"] != "root" || got["host"] != "h.example.com" || got["port"] != "22" {
		t.Errorf("persisted preset wrong: %+v", got)
	}
	// A command-type script must not have gained a stray `file:` key (omitempty).
	if reloaded.Scripts["ssh"].File != "" {
		t.Errorf("command script gained a file field on rewrite")
	}
}

// TestNKeyOpensPresetForm drives the event loop: focus the catalog, select a
// script, press 'n', and verify the add-preset form page is shown.
func TestNKeyOpensPresetForm(t *testing.T) {
	u := testUI(t, "scripts:\n  ssh:\n    command: \"ssh {{.user}}@{{.host}}\"\n")
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	u.app.SetScreen(screen)

	go u.refreshLoop()
	go func() { _ = u.app.Run() }()
	defer u.app.Stop()
	time.Sleep(200 * time.Millisecond)

	ready := make(chan struct{})
	u.app.QueueUpdateDraw(func() {
		u.app.SetFocus(u.catalog)
		u.catalog.Select(1, 0) // the "ssh" row
		close(ready)
	})
	<-ready
	u.app.QueueEvent(tcell.NewEventKey(tcell.KeyRune, 'n', tcell.ModNone))

	got := make(chan string, 1)
	waitFor(t, 3*time.Second, func() bool {
		done := make(chan string, 1)
		u.app.QueueUpdateDraw(func() { front, _ := u.pages.GetFrontPage(); done <- front })
		front := <-done
		if front == presetFormPage {
			got <- front
			return true
		}
		return false
	})
	select {
	case <-got:
	default:
		t.Fatal("pressing 'n' did not open the preset form")
	}
}

// TestPumpReaderBatches is the deterministic regression for the log-view freeze.
// The freeze was caused by one UI flush (full redraw) per 4KB read; on a chatty
// daemon that floods the event loop and starves input. pumpReader must coalesce a
// large input into very few flushes while preserving every byte in order.
func TestPumpReaderBatches(t *testing.T) {
	// 5MB of data available all at once (like opening a big existing log).
	data := bytes.Repeat([]byte("spam line padding padding padding\n"), 150000)

	var flushes int
	var got bytes.Buffer
	flush := func(chunk []byte) {
		flushes++
		got.Write(chunk)
	}
	stop := make(chan struct{})
	// interval 100ms; the whole 5MB is read in well under 100ms, so batching
	// should collapse it to a single flush at EOF.
	pumpReader(bytes.NewReader(data), stop, 100*time.Millisecond, flush, func() bool { return false })

	if got.Len() != len(data) {
		t.Fatalf("data not preserved: got %d bytes, want %d", got.Len(), len(data))
	}
	if !bytes.Equal(got.Bytes(), data) {
		t.Fatal("data corrupted/reordered")
	}
	// The bug produced ~len(data)/32KB (~160) flushes; batched should be a handful.
	if flushes > 3 {
		t.Fatalf("expected batched output (≤3 flushes) for 5MB, got %d — throttling is not working", flushes)
	}
}

// TestPumpReaderStopsOnStop verifies the pump exits promptly when stop closes,
// even mid-follow, and flushes any buffered bytes first.
func TestPumpReaderStopsOnStop(t *testing.T) {
	pr, pw := io.Pipe()
	stop := make(chan struct{})
	var got bytes.Buffer
	done := make(chan struct{})
	go func() {
		pumpReader(pr, stop, 10*time.Millisecond, func(c []byte) { got.Write(c) }, func() bool { return true })
		close(done)
	}()
	pw.Write([]byte("hello\n"))
	time.Sleep(50 * time.Millisecond) // allow a flush
	close(stop)
	pw.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pumpReader did not stop when stop closed")
	}
	if got.String() != "hello\n" {
		t.Errorf("buffered bytes not flushed: %q", got.String())
	}
}

// TestRenderDaemonsPure verifies the render path takes a pre-fetched status list
// and does not fetch (dm.List, with its blocking readiness probes, must run off
// the UI thread). It renders a synthetic list and checks the cells.
func TestRenderDaemonsPure(t *testing.T) {
	u := testUI(t, "scripts:\n  proxy:\n    command: echo hi\n    background: true\n")
	list := []daemon.Status{
		{Meta: daemon.Meta{Name: "proxy", Preset: "prod", PID: 1234, StartUnix: 1, ReadyPort: 5432}, Alive: true, Ready: true},
	}
	u.renderDaemons(list)
	if got := u.daemons.GetCell(1, 0).Text; got != "proxy" {
		t.Errorf("name cell = %q, want proxy", got)
	}
	if got := u.daemons.GetCell(1, 1).Text; got != "prod" {
		t.Errorf("preset cell = %q, want prod", got)
	}
	if got := u.daemons.GetCell(1, 4).Text; got != "● ready" {
		t.Errorf("state cell = %q, want ● ready", got)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("truncate short = %q", got)
	}
	if got := truncate("hello world", 5); got != "hell…" {
		t.Errorf("truncate long = %q", got)
	}
}
