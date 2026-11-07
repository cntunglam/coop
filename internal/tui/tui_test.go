package tui

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/cntunglam/coop/internal/catalog"
	"github.com/cntunglam/coop/internal/config"
	"github.com/cntunglam/coop/internal/daemon"
	"github.com/cntunglam/coop/internal/edit"
	"github.com/cntunglam/coop/internal/runner"
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
		ed:      &edit.Editor{Path: cfgPath, Daemons: dm},
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
		kind catalog.Kind
	}{
		{"alpha", catalog.KindScript},
		{"proxy", catalog.KindDaemon},
		{"flow", catalog.KindWorkflow},
	}
	for i, w := range want {
		if u.entries[i].Name != w.name || u.entries[i].Kind != w.kind {
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
		{Meta: daemon.Meta{Name: "proxy", Group: "gamehub", Preset: "prod", PID: 1234, StartUnix: 1, ReadyPort: 5432}, Alive: true, Ready: true},
	}
	u.renderDaemons(list)
	// Columns: NAME, GROUP, PRESET, PORT, PID, UPTIME, STATE, COMMAND.
	if got := u.daemons.GetCell(1, 0).Text; got != "proxy" {
		t.Errorf("name cell = %q, want proxy", got)
	}
	if got := u.daemons.GetCell(1, 1).Text; got != "gamehub" {
		t.Errorf("group cell = %q, want gamehub", got)
	}
	if got := u.daemons.GetCell(1, 2).Text; got != "prod" {
		t.Errorf("preset cell = %q, want prod", got)
	}
	if got := u.daemons.GetCell(1, 3).Text; got != "5432" {
		t.Errorf("port cell = %q, want 5432", got)
	}
	if got := u.daemons.GetCell(1, 6).Text; got != "● ready" {
		t.Errorf("state cell = %q, want ● ready", got)
	}
}

func TestRenderDaemonsHealthColumn(t *testing.T) {
	u := testUI(t, "scripts:\n  svc:\n    command: echo hi\n    background: true\n")
	now := time.Now().Unix()
	list := []daemon.Status{
		{Meta: daemon.Meta{
			Name: "svc", PID: 1, StartUnix: 1,
			Health: &daemon.HealthState{Configured: true, State: "healthy", Interval: 10, LastCheck: now},
		}, Alive: true},
	}
	u.renderDaemons(list)
	// Columns now: NAME, GROUP, PRESET, PORT, PID, UPTIME, STATE, HEALTH, COMMAND.
	if got := u.daemons.GetCell(0, 7).Text; got != "HEALTH" {
		t.Errorf("header col 7 = %q, want HEALTH", got)
	}
	if got := u.daemons.GetCell(1, 7).Text; got != "healthy" {
		t.Errorf("health cell = %q, want healthy", got)
	}
}

// TestEmptyDaemonTableNoFreeze reproduces the reported freeze: an empty daemon
// table (header + "(no daemons)" placeholder, both non-selectable) that stays
// SetSelectable(true) makes tview's Table.InputHandler spin forever on a movement
// key. The fix disables selection while empty, so Down is a harmless scroll and
// the UI stays responsive.
func TestEmptyDaemonTableNoFreeze(t *testing.T) {
	u := testUI(t, "scripts:\n  hello:\n    command: echo hi\n") // no daemons
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	u.app.SetScreen(screen)
	go u.refreshLoop()
	go func() { _ = u.app.Run() }()
	defer u.app.Stop()
	time.Sleep(200 * time.Millisecond)

	// Render the empty table (header + placeholder → rowCount 2) and focus it.
	rendered := make(chan struct{})
	u.app.QueueUpdateDraw(func() {
		u.renderDaemons(nil)
		u.app.SetFocus(u.daemons)
		close(rendered)
	})
	<-rendered

	// Movement keys on the empty table must not wedge the input handler.
	u.app.QueueEvent(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	u.app.QueueEvent(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	u.app.QueueEvent(tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone))

	// If the input handler is spinning, this queued update never runs.
	responded := make(chan struct{})
	go func() { u.app.QueueUpdateDraw(func() { close(responded) }) }()
	select {
	case <-responded:
	case <-time.After(3 * time.Second):
		t.Fatal("UI frozen after Down/Up on empty daemon table (tview InputHandler infinite loop)")
	}
}

// TestTUISilencesRunnerStatus guards against the screen-corruption bug: the TUI
// must not let the runner write progress messages to the terminal (os.Stderr),
// which would land on top of the live tview screen when starting a daemon.
func TestTUISilencesRunnerStatus(t *testing.T) {
	u := testUI(t, "scripts:\n  proxy:\n    command: echo hi\n    background: true\n")
	if u.run.Status != io.Discard {
		t.Fatalf("TUI must redirect runner Status to io.Discard, got %T", u.run.Status)
	}
}

func TestExecuteUnknownCommand(t *testing.T) {
	u := testUI(t, "scripts:\n  hello:\n    command: echo hi\n")
	u.executeCommand("nonexistent-thing")
	if !strings.Contains(u.consoleLog.GetText(true), "unknown script, workflow, or group") {
		t.Errorf("expected unknown-command message in log, got: %q", u.consoleLog.GetText(true))
	}
}

func TestEKeyEntersCommandMode(t *testing.T) {
	u := testUI(t, "scripts:\n  hello:\n    command: echo hi\n")
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	u.app.SetScreen(screen)
	go u.refreshLoop()
	go func() { _ = u.app.Run() }()
	defer u.app.Stop()
	time.Sleep(200 * time.Millisecond)

	u.app.QueueEvent(tcell.NewEventKey(tcell.KeyRune, 'e', tcell.ModNone))

	focused := false
	waitFor(t, 3*time.Second, func() bool {
		res := make(chan bool, 1)
		u.app.QueueUpdateDraw(func() { res <- u.app.GetFocus() == u.consoleInput })
		select {
		case focused = <-res:
		case <-time.After(200 * time.Millisecond):
		}
		return focused
	})
	if !focused {
		t.Fatal("pressing 'e' did not focus the command input")
	}
}

// TestCommandStopsDaemon runs a daemon, then stops it by typing `stop <name>` in
// the command box, exercising executeCommand → stopDaemon.
func TestCommandStopsDaemon(t *testing.T) {
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
	time.Sleep(200 * time.Millisecond)

	if err := u.run.RunScript("bg", ""); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { a, _, _ := u.dm.IsRunning("bg"); return a })

	// Type "stop bg" and submit via the command box.
	done := make(chan struct{})
	u.app.QueueUpdateDraw(func() {
		u.consoleInput.SetText("stop bg")
		u.onCommandDone(tcell.KeyEnter)
		close(done)
	})
	<-done

	waitFor(t, 8*time.Second, func() bool { a, _, _ := u.dm.IsRunning("bg"); return !a })
	if a, _, _ := u.dm.IsRunning("bg"); a {
		t.Fatal("daemon still running after `stop bg` command")
	}
}

func TestCommandListLogsEntries(t *testing.T) {
	u := testUI(t, "scripts:\n  alpha:\n    command: echo a\nworkflows:\n  flow:\n    steps:\n      - { script: alpha }\n")
	u.executeCommand("list")
	log := u.consoleLog.GetText(true)
	if !strings.Contains(log, "alpha") || !strings.Contains(log, "flow") {
		t.Errorf("list should log both entries, got: %q", log)
	}
}

// TestReloadConfigSharesPointer verifies `config edit`'s reload updates the config
// in place, so the runner (which holds the same pointer) sees new scripts.
func TestReloadConfigSharesPointer(t *testing.T) {
	u := testUI(t, "scripts:\n  alpha:\n    command: echo a\n")
	// Overwrite the config file with an added script, then reload.
	newYAML := "scripts:\n  alpha:\n    command: echo a\n  beta:\n    command: echo b\n"
	if err := os.WriteFile(u.cfgPath, []byte(newYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := u.reloadConfig(); err != nil {
		t.Fatalf("reloadConfig: %v", err)
	}
	if _, ok := u.cfg.Scripts["beta"]; !ok {
		t.Error("reloaded config missing new script in u.cfg")
	}
	// The runner shares the same *config.Config, so it must see it too.
	if _, ok := u.run.Config().Scripts["beta"]; !ok {
		t.Error("runner's config did not see the reloaded script")
	}
}

func TestSuggest(t *testing.T) {
	u := testUI(t, `
scripts:
  hello:
    command: "echo hi {{.who}}"
    presets:
      world: { who: world }
      team:  { who: team }
  proxy:
    command: echo p
    background: true
    presets:
      local: { port: "8899" }
groups:
  stack:
    members:
      - { script: proxy, preset: local }
`)
	// First token: verb prefix.
	if got := u.suggest("sto"); !contains(got, "stop") {
		t.Errorf("suggest(sto) = %v, want to include stop", got)
	}
	// First token: name prefix.
	if got := u.suggest("pro"); !contains(got, "proxy") {
		t.Errorf("suggest(pro) = %v, want to include proxy", got)
	}
	// After a bare script name → ONLY that script's presets (the reported bug).
	got := u.suggest("hello ")
	if !contains(got, "hello world") || !contains(got, "hello team") {
		t.Errorf("suggest(hello ) = %v, want hello world + hello team", got)
	}
	if contains(got, "hello proxy") || contains(got, "hello stack") {
		t.Errorf("suggest(hello ) must not offer other names: %v", got)
	}
	// Partial preset after a name.
	if got := u.suggest("hello wo"); !contains(got, "hello world") || contains(got, "hello team") {
		t.Errorf("suggest(hello wo) = %v, want only hello world", got)
	}
	// After `stop `: only background scripts + groups (not the foreground `hello`).
	got = u.suggest("stop ")
	if !contains(got, "stop proxy") || !contains(got, "stop stack") {
		t.Errorf("suggest(stop ) = %v, want stop proxy + stop stack", got)
	}
	if contains(got, "stop hello") {
		t.Errorf("suggest(stop ) should not offer foreground script hello: %v", got)
	}
	// `start <script> ` → the script's presets.
	if got := u.suggest("start hello "); !contains(got, "start hello world") {
		t.Errorf("suggest(start hello ) = %v, want start hello world", got)
	}
}

func TestDaemonPortFallback(t *testing.T) {
	u := testUI(t, "scripts:\n  proxy:\n    command: echo p\n    background: true\n    presets:\n      local: { port: \"8899\" }\n")
	// ready_port takes precedence.
	if got := u.daemonPort(daemon.Status{Meta: daemon.Meta{ReadyPort: 5432}}); got != "5432" {
		t.Errorf("port from ready_port = %q, want 5432", got)
	}
	// Falls back to the preset's port value.
	if got := u.daemonPort(daemon.Status{Meta: daemon.Meta{Name: "proxy", Preset: "local"}}); got != "8899" {
		t.Errorf("port from preset = %q, want 8899", got)
	}
	// Neither → dash.
	if got := u.daemonPort(daemon.Status{Meta: daemon.Meta{Name: "proxy"}}); got != "-" {
		t.Errorf("port with none = %q, want -", got)
	}
}

func TestReloadVerb(t *testing.T) {
	u := testUI(t, "scripts:\n  a:\n    command: echo a\n")
	// Add a script to the file, then reload via the command box.
	if err := os.WriteFile(u.cfgPath, []byte("scripts:\n  a:\n    command: echo a\n  b:\n    command: echo b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	u.executeCommand("reload")
	if _, ok := u.cfg.Scripts["b"]; !ok {
		t.Error("reload did not pick up the new script")
	}
	if !strings.Contains(u.consoleLog.GetText(true), "config reloaded") {
		t.Errorf("reload should log success, got: %q", u.consoleLog.GetText(true))
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("truncate short = %q", got)
	}
	if got := truncate("hello world", 5); got != "hell…" {
		t.Errorf("truncate long = %q", got)
	}
}

// startSim runs the app on a simulation screen and returns a helper that runs
// fn on the UI goroutine and waits for it.
func startSim(t *testing.T, u *UI) func(fn func()) {
	t.Helper()
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	u.app.SetScreen(screen)
	go u.refreshLoop()
	go func() { _ = u.app.Run() }()
	t.Cleanup(u.app.Stop)
	time.Sleep(200 * time.Millisecond)
	return func(fn func()) {
		done := make(chan struct{})
		u.app.QueueUpdateDraw(func() { fn(); close(done) })
		<-done
	}
}

func TestCatalogShowsTags(t *testing.T) {
	u := testUI(t, "scripts:\n  a:\n    command: echo a\n    tags: [prod, k8s]\n")
	if got := u.catalog.GetCell(1, 2).Text; !strings.Contains(got, "tags: prod, k8s") {
		t.Errorf("DETAIL = %q, want tags", got)
	}
}

func TestSlashFiltersCatalog(t *testing.T) {
	u := testUI(t, "scripts:\n  orders-logs:\n    command: echo o\n  other:\n    command: echo x\n")
	do := startSim(t, u)
	do(func() { u.app.SetFocus(u.catalog) })
	for _, r := range "/ord" {
		u.app.QueueEvent(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	names := func() (out []string) {
		do(func() {
			for _, e := range u.entries {
				out = append(out, e.Name)
			}
		})
		return
	}
	waitFor(t, 3*time.Second, func() bool { n := names(); return len(n) == 1 && n[0] == "orders-logs" })
	u.app.QueueEvent(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	waitFor(t, 3*time.Second, func() bool { return len(names()) == 2 })
	do(func() {
		if u.filter != "" {
			t.Errorf("filter = %q after Esc", u.filter)
		}
		if u.app.GetFocus() != u.catalog {
			t.Errorf("focus not returned to catalog")
		}
	})
}

func TestFilterNoMatchesPlaceholder(t *testing.T) {
	u := testUI(t, "scripts:\n  a:\n    command: echo a\n")
	u.filter = "zzz"
	u.buildCatalog()
	if len(u.entries) != 0 || u.catalog.GetCell(1, 0).Text != "(no matches)" {
		t.Errorf("entries=%v cell=%q", u.entries, u.catalog.GetCell(1, 0).Text)
	}
	if u.catalog.GetCell(1, 0).NotSelectable != true {
		t.Error("placeholder must not be selectable")
	}
}

func TestSavePresetKeepsExternalWrites(t *testing.T) {
	u := testUI(t, "scripts:\n  ssh:\n    command: \"ssh {{.host}}\"\n")
	f, err := os.OpenFile(u.cfgPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("  other:\n    command: echo other\n")
	f.Close()
	if err := u.savePreset("ssh", "prod", map[string]string{"host": "h"}); err != nil {
		t.Fatal(err)
	}
	disk, err := config.Load(u.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []*config.Config{disk, u.cfg} {
		if _, ok := c.Scripts["other"]; !ok {
			t.Error("external script lost")
		}
		if _, ok := c.Scripts["ssh"].Presets["prod"]; !ok {
			t.Error("new preset missing")
		}
	}
}

func TestNKeyBlockedOnLiteral(t *testing.T) {
	u := testUI(t, "scripts:\n  lit:\n    command: echo hi\n    literal: true\n")
	do := startSim(t, u)
	do(func() { u.app.SetFocus(u.catalog); u.catalog.Select(1, 0) })
	u.app.QueueEvent(tcell.NewEventKey(tcell.KeyRune, 'n', tcell.ModNone))
	var log string
	waitFor(t, 3*time.Second, func() bool {
		do(func() { log = u.consoleLog.GetText(true) })
		return strings.Contains(log, "is a literal command")
	})
	do(func() {
		if front, _ := u.pages.GetFrontPage(); front != "main" {
			t.Errorf("front page = %q, want main", front)
		}
	})
}
