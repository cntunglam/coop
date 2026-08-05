// Package tui implements rx's K9s-style full-screen terminal interface: a live
// table of running daemons over a catalog of scripts and workflows, with
// keystroke actions to run, stop, restart, and tail logs.
package tui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/caongoctunglam/rx/internal/config"
	"github.com/caongoctunglam/rx/internal/daemon"
	"github.com/caongoctunglam/rx/internal/runner"
	"github.com/caongoctunglam/rx/internal/script"
)

// refreshInterval is how often the daemon table re-polls process state.
const refreshInterval = time.Second

// entryKind distinguishes catalog rows.
type entryKind int

const (
	kindScript entryKind = iota
	kindWorkflow
)

// catalogEntry is one row in the scripts/workflows pane.
type catalogEntry struct {
	name string
	kind entryKind
}

// UI holds the tview application and the rx dependencies it drives.
type UI struct {
	app   *tview.Application
	pages *tview.Pages
	cfg   *config.Config
	dm    *daemon.Manager
	run   *runner.Runner

	daemons *tview.Table
	catalog *tview.Table
	footer  *tview.TextView

	cfgPath string
	entries []catalogEntry // catalog rows, aligned with table data rows
	done    chan struct{}
}

// Run builds and runs the TUI, blocking until the user quits. cfgPath is where
// edits (e.g. new presets) are persisted.
func Run(run *runner.Runner, cfgPath string) error {
	u := &UI{
		app:     tview.NewApplication(),
		cfg:     run.Config(),
		dm:      run.Daemons(),
		run:     run,
		cfgPath: cfgPath,
		done:    make(chan struct{}),
	}
	u.build()
	u.refreshDaemons()
	u.installDebugDump()
	go u.refreshLoop()
	return u.app.Run()
}

// installDebugDump writes all goroutine stacks to a file on SIGUSR1, so an
// intermittent freeze can be diagnosed: `kill -USR1 <rx-pid>` from another
// terminal captures where things are stuck (works even if input is wedged).
func (u *UI) installDebugDump() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGUSR1)
	go func() {
		for range sig {
			buf := make([]byte, 1<<20)
			n := runtime.Stack(buf, true)
			_ = os.WriteFile(debugDumpPath(), buf[:n], 0o644)
		}
	}()
}

// debugDumpPath is where SIGUSR1 goroutine dumps are written.
func debugDumpPath() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		if home, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(home, ".local", "state")
		} else {
			base = os.TempDir()
		}
	}
	dir := filepath.Join(base, "rx")
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "goroutines.txt")
}

func (u *UI) build() {
	u.daemons = tview.NewTable().SetBorders(false).SetSelectable(true, false).SetFixed(1, 0)
	u.daemons.SetBorder(true).SetTitle(" DAEMONS ")
	u.daemons.SetSelectedFunc(func(row, _ int) { u.openLogsRow(row) })
	u.daemons.SetInputCapture(u.daemonKeys)

	u.catalog = tview.NewTable().SetBorders(false).SetSelectable(true, false).SetFixed(1, 0)
	u.catalog.SetBorder(true).SetTitle(" SCRIPTS / WORKFLOWS ")
	u.catalog.SetSelectedFunc(func(row, _ int) { u.activateRow(row) })
	u.catalog.SetInputCapture(u.catalogKeys)
	u.buildCatalog()

	u.footer = tview.NewTextView().SetDynamicColors(true)
	u.setFooter("[::b][↑↓][::-] nav  [::b][tab][::-] switch  [::b][enter][::-] run/logs  [::b]n[::-] new preset  [::b]s[::-] stop  [::b]r[::-] restart  [::b]l[::-] logs  [::b]q[::-] quit")

	main := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(u.daemons, 0, 1, true).
		AddItem(u.catalog, 0, 1, false).
		AddItem(u.footer, 1, 0, false)

	u.pages = tview.NewPages().AddPage("main", main, true, true)
	u.app.SetRoot(u.pages, true).EnableMouse(true)
	u.app.SetInputCapture(u.globalKeys)
	u.app.SetFocus(u.daemons)
}

// ---- key handling ------------------------------------------------------------

func (u *UI) globalKeys(ev *tcell.EventKey) *tcell.EventKey {
	if front, _ := u.pages.GetFrontPage(); front != "main" {
		return ev // a modal/log view is up; let it handle keys
	}
	switch ev.Key() {
	case tcell.KeyTab, tcell.KeyBacktab:
		u.togglePane()
		return nil
	}
	switch ev.Rune() {
	case 'q':
		u.quit()
		return nil
	case '?':
		u.showHelp()
		return nil
	}
	return ev
}

func (u *UI) daemonKeys(ev *tcell.EventKey) *tcell.EventKey {
	name, ok := u.selectedDaemon()
	if !ok {
		return ev
	}
	switch ev.Rune() {
	case 's':
		u.stopDaemon(name)
		return nil
	case 'r':
		u.restartDaemon(name)
		return nil
	case 'l':
		u.openLogs(name)
		return nil
	}
	return ev
}

func (u *UI) catalogKeys(ev *tcell.EventKey) *tcell.EventKey {
	if ev.Rune() == 'n' {
		if e, ok := u.selectedEntry(); ok && e.kind == kindScript {
			u.addPresetForm(e.name)
		}
		return nil
	}
	return ev
}

// selectedEntry returns the catalog entry under the cursor.
func (u *UI) selectedEntry() (catalogEntry, bool) {
	row, _ := u.catalog.GetSelection()
	idx := row - 1
	if idx < 0 || idx >= len(u.entries) {
		return catalogEntry{}, false
	}
	return u.entries[idx], true
}

func (u *UI) togglePane() {
	if u.app.GetFocus() == u.daemons {
		u.app.SetFocus(u.catalog)
	} else {
		u.app.SetFocus(u.daemons)
	}
}

// ---- daemon pane -------------------------------------------------------------

func (u *UI) refreshLoop() {
	t := time.NewTicker(refreshInterval)
	defer t.Stop()
	for {
		select {
		case <-u.done:
			return
		case <-t.C:
			// dm.List() does blocking TCP readiness probes; run it here on the
			// ticker goroutine, never on the UI thread, then apply cheap cell
			// updates. This ran on the UI goroutine before and stalled input.
			list, err := u.dm.List()
			u.app.QueueUpdateDraw(func() {
				if err != nil {
					u.setFooter("[red]error listing daemons: " + err.Error())
					return
				}
				u.renderDaemons(list)
			})
		}
	}
}

// scheduleDaemonRefresh fetches daemon statuses off the UI thread (dm.List does
// blocking readiness probes) and then renders them. Safe to call from the UI
// goroutine — the blocking work happens in the spawned goroutine.
func (u *UI) scheduleDaemonRefresh() {
	go func() {
		list, err := u.dm.List()
		u.app.QueueUpdateDraw(func() {
			if err != nil {
				u.setFooter("[red]error listing daemons: " + err.Error())
				return
			}
			u.renderDaemons(list)
		})
	}()
}

// refreshDaemons fetches and renders synchronously. It must only be called off
// the UI event loop (initial load before Run, or tests) because dm.List blocks.
func (u *UI) refreshDaemons() {
	list, err := u.dm.List()
	if err != nil {
		u.setFooter("[red]error listing daemons: " + err.Error())
		return
	}
	u.renderDaemons(list)
}

// renderDaemons updates the daemon table cells. UI-thread only; does no I/O.
func (u *UI) renderDaemons(list []daemon.Status) {
	// Preserve the current selection by name across refreshes.
	selName, _ := u.selectedDaemon()

	u.daemons.Clear()
	headers := []string{"NAME", "PRESET", "PID", "UPTIME", "STATE", "COMMAND"}
	for c, h := range headers {
		u.daemons.SetCell(0, c, headerCell(h))
	}
	if len(list) == 0 {
		u.daemons.SetCell(1, 0, tview.NewTableCell("(no daemons running)").SetTextColor(tcell.ColorGray).SetSelectable(false))
		return
	}
	selRow := 1
	for i, st := range list {
		row := i + 1
		if st.Name == selName {
			selRow = row
		}
		preset := dash(st.Preset)
		pid := "-"
		uptime := "-"
		if st.Alive {
			pid = fmt.Sprintf("%d", st.PID)
			uptime = time.Since(time.Unix(st.StartUnix, 0)).Round(time.Second).String()
		}
		stateText, stateColor := daemonState(st)
		u.daemons.SetCell(row, 0, tview.NewTableCell(st.Name).SetTextColor(tcell.ColorWhite))
		u.daemons.SetCell(row, 1, tview.NewTableCell(preset))
		u.daemons.SetCell(row, 2, tview.NewTableCell(pid))
		u.daemons.SetCell(row, 3, tview.NewTableCell(uptime))
		u.daemons.SetCell(row, 4, tview.NewTableCell(stateText).SetTextColor(stateColor))
		u.daemons.SetCell(row, 5, tview.NewTableCell(truncate(st.Command, 60)).SetTextColor(tcell.ColorGray))
	}
	u.daemons.Select(selRow, 0)
}

func daemonState(st daemon.Status) (string, tcell.Color) {
	switch {
	case !st.Alive:
		return "● dead", tcell.ColorRed
	case st.ReadyPort > 0 && st.Ready:
		return "● ready", tcell.ColorGreen
	case st.ReadyPort > 0 && !st.Ready:
		return "● starting", tcell.ColorYellow
	default:
		return "● running", tcell.ColorGreen
	}
}

func (u *UI) selectedDaemon() (string, bool) {
	row, _ := u.daemons.GetSelection()
	if row < 1 {
		return "", false
	}
	cell := u.daemons.GetCell(row, 0)
	if cell == nil {
		return "", false
	}
	name := cell.Text
	if _, ok := u.cfg.Scripts[name]; !ok {
		return "", false // the "(no daemons)" placeholder row
	}
	return name, true
}

func (u *UI) stopDaemon(name string) {
	u.setFooter("[yellow]stopping " + name + "…")
	go func() {
		err := u.dm.Stop(name, 10*time.Second)
		list, lerr := u.dm.List() // off the UI thread
		u.app.QueueUpdateDraw(func() {
			if err != nil {
				u.setFooter("[red]stop " + name + ": " + err.Error())
			} else {
				u.setFooter("[green]stopped " + name)
			}
			if lerr == nil {
				u.renderDaemons(list)
			}
		})
	}()
}

func (u *UI) restartDaemon(name string) {
	u.setFooter("[yellow]restarting " + name + "…")
	go func() {
		err := u.run.RestartDaemon(name)
		list, lerr := u.dm.List() // off the UI thread
		u.app.QueueUpdateDraw(func() {
			if err != nil {
				u.setFooter("[red]restart " + name + ": " + err.Error())
			} else {
				u.setFooter("[green]restarted " + name)
			}
			if lerr == nil {
				u.renderDaemons(list)
			}
		})
	}()
}

// ---- catalog pane ------------------------------------------------------------

func (u *UI) buildCatalog() {
	u.entries = nil
	u.catalog.Clear()
	headers := []string{"NAME", "KIND", "DETAIL"}
	for c, h := range headers {
		u.catalog.SetCell(0, c, headerCell(h))
	}
	row := 1
	for _, name := range sortedScripts(u.cfg.Scripts) {
		s := u.cfg.Scripts[name]
		kind := "script"
		if s.Background {
			kind = "daemon"
		}
		detail := s.Description
		if len(s.Presets) > 0 {
			detail = joinDetail(detail, "presets: "+joinStrings(s.PresetNames()))
		}
		u.catalog.SetCell(row, 0, tview.NewTableCell(name).SetTextColor(tcell.ColorWhite))
		u.catalog.SetCell(row, 1, tview.NewTableCell(kind).SetTextColor(tcell.ColorTeal))
		u.catalog.SetCell(row, 2, tview.NewTableCell(detail).SetTextColor(tcell.ColorGray))
		u.entries = append(u.entries, catalogEntry{name: name, kind: kindScript})
		row++
	}
	for _, name := range sortedWorkflows(u.cfg.Workflows) {
		w := u.cfg.Workflows[name]
		detail := joinDetail(w.Description, fmt.Sprintf("%d step(s)", len(w.Steps)))
		u.catalog.SetCell(row, 0, tview.NewTableCell(name).SetTextColor(tcell.ColorWhite))
		u.catalog.SetCell(row, 1, tview.NewTableCell("workflow").SetTextColor(tcell.ColorPurple))
		u.catalog.SetCell(row, 2, tview.NewTableCell(detail).SetTextColor(tcell.ColorGray))
		u.entries = append(u.entries, catalogEntry{name: name, kind: kindWorkflow})
		row++
	}
	if len(u.entries) == 0 {
		u.catalog.SetCell(1, 0, tview.NewTableCell("(nothing configured — see `rx config edit`)").
			SetTextColor(tcell.ColorGray).SetSelectable(false))
	}
	u.catalog.Select(1, 0)
}

func (u *UI) activateRow(row int) {
	idx := row - 1
	if idx < 0 || idx >= len(u.entries) {
		return
	}
	e := u.entries[idx]
	if e.kind == kindWorkflow {
		u.launchWorkflow(e.name)
		return
	}
	s := u.cfg.Scripts[e.name]
	if len(s.Presets) > 0 {
		u.pickPreset(e.name, s, func(preset string) { u.launchScript(e.name, preset) })
		return
	}
	u.launchScript(e.name, "")
}

func (u *UI) launchScript(name, preset string) {
	s := u.cfg.Scripts[name]
	if s.Background {
		u.setFooter("[yellow]starting " + name + "…")
		go func() {
			err := u.run.RunScript(name, preset)
			list, lerr := u.dm.List() // off the UI thread
			u.app.QueueUpdateDraw(func() {
				if err != nil {
					u.setFooter("[red]" + err.Error())
				} else {
					u.setFooter("[green]started " + name)
					u.app.SetFocus(u.daemons)
				}
				if lerr == nil {
					u.renderDaemons(list)
				}
			})
		}()
		return
	}
	// Foreground: suspend the TUI, run attached, then restore (K9s-style).
	u.suspendRun(func() error { return u.run.RunScript(name, preset) })
}

func (u *UI) launchWorkflow(name string) {
	u.suspendRun(func() error { return u.run.RunWorkflow(name) })
}

// suspendRun drops out of the full-screen UI, runs fn attached to the real
// terminal, waits for the user to acknowledge, then restores the UI.
//
// The child process shares rx's process group and controlling terminal, so a
// Ctrl-C typed to abort a stuck command (e.g. ssh to an unreachable host) would
// otherwise also SIGINT rx and kill the whole app. We catch and swallow SIGINT
// in rx for the duration: the child inherits the default handler (os/exec resets
// caught signals to SIG_DFL in the child) and dies on Ctrl-C, while rx survives
// and returns to the UI.
func (u *UI) suspendRun(fn func() error) {
	u.app.Suspend(func() {
		sig := make(chan os.Signal, 1)
		stop := make(chan struct{})
		signal.Notify(sig, os.Interrupt)
		go func() {
			for {
				select {
				case <-sig: // swallow: let the child handle Ctrl-C; rx must live
				case <-stop:
					return
				}
			}
		}()
		defer func() {
			signal.Stop(sig)
			close(stop)
		}()

		fmt.Print("\033[2J\033[H") // clear screen
		if err := fn(); err != nil {
			fmt.Printf("\n[error] %v\n", err)
		}
		fmt.Print("\n[Ctrl-C aborts the command; press Enter to return to rx]")
		// ReadString drains a full line rather than Scanln, which can return
		// immediately on a stray buffered newline and skip the pause.
		bufio.NewReader(os.Stdin).ReadString('\n')
	})
	// tcell's screen.Resume() (inside Suspend) already restores and redraws the
	// terminal. Do NOT call app.Sync()/Draw() here: this runs on the UI goroutine,
	// and those block on the update channel that only the UI goroutine drains —
	// a latent deadlock. Refresh off-thread instead; its queued render repaints.
	u.scheduleDaemonRefresh()
}

// ---- preset picker -----------------------------------------------------------

func (u *UI) pickPreset(name string, s config.Script, onPick func(string)) {
	list := tview.NewList().ShowSecondaryText(false)
	list.SetBorder(true).SetTitle(fmt.Sprintf(" preset for %q ", name))
	for _, p := range s.PresetNames() {
		p := p
		list.AddItem(p, "", 0, func() {
			u.closeModal("preset")
			onPick(p)
		})
	}
	list.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEscape {
			u.closeModal("preset")
			return nil
		}
		return ev
	})
	u.showModal("preset", list, 40, len(s.Presets)+2)
}

// ---- add-preset form ---------------------------------------------------------

const presetFormPage = "preset-form"

// addPresetForm opens a form to add a new preset to a script. Fields are the
// script's detected parameters (from its {{.template}} vars and existing preset
// keys) plus a preset-name field. On save it persists to the config file.
func (u *UI) addPresetForm(scriptName string) {
	s := u.cfg.Scripts[scriptName]
	params := script.ParamNames(s)

	form := tview.NewForm()
	form.SetBorder(true).SetTitle(fmt.Sprintf(" new preset for %q  (Esc cancels) ", scriptName))
	form.AddInputField("preset name", "", 32, nil, nil)
	for _, p := range params {
		form.AddInputField(p, "", 32, nil, nil)
	}
	field := func(label string) string {
		return strings.TrimSpace(form.GetFormItemByLabel(label).(*tview.InputField).GetText())
	}
	form.AddButton("Save", func() {
		presetName := field("preset name")
		if presetName == "" {
			u.setFooter("[red]preset name is required")
			return
		}
		if _, exists := s.Presets[presetName]; exists {
			u.setFooter("[red]preset " + presetName + " already exists")
			return
		}
		values := map[string]string{}
		for _, p := range params {
			values[p] = field(p)
		}
		if err := u.savePreset(scriptName, presetName, values); err != nil {
			u.setFooter("[red]" + err.Error())
			return
		}
		u.closeModal(presetFormPage)
		u.setFooter("[green]added preset " + presetName + " to " + scriptName)
	})
	form.AddButton("Cancel", func() { u.closeModal(presetFormPage) })
	form.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEscape {
			u.closeModal(presetFormPage)
			return nil
		}
		return ev
	})

	u.showModal(presetFormPage, form, 52, len(params)+7)
}

// savePreset adds the preset in memory and persists the config, rolling back the
// in-memory change if the write fails so disk and UI stay consistent.
func (u *UI) savePreset(scriptName, presetName string, values map[string]string) error {
	s := u.cfg.Scripts[scriptName]
	if s.Presets == nil {
		s.Presets = map[string]map[string]string{}
	}
	s.Presets[presetName] = values
	u.cfg.Scripts[scriptName] = s
	if err := config.Save(u.cfgPath, u.cfg); err != nil {
		delete(s.Presets, presetName)
		return err
	}
	u.buildCatalog()
	return nil
}

// ---- logs view ---------------------------------------------------------------

func (u *UI) openLogsRow(row int) {
	if name, ok := u.selectedDaemonAt(row); ok {
		u.openLogs(name)
	}
}

func (u *UI) selectedDaemonAt(row int) (string, bool) {
	if row < 1 {
		return "", false
	}
	cell := u.daemons.GetCell(row, 0)
	if cell == nil {
		return "", false
	}
	if _, ok := u.cfg.Scripts[cell.Text]; !ok {
		return "", false
	}
	return cell.Text, true
}

func (u *UI) openLogs(name string) {
	path, err := u.dm.LogPath(name)
	if err != nil {
		u.setFooter("[red]" + err.Error())
		return
	}
	tv := tview.NewTextView().SetDynamicColors(true).SetScrollable(true)
	// Cap retained lines so redraw cost stays bounded no matter how much a chatty
	// daemon logs; without this, an ever-growing buffer makes each redraw slower
	// until the event loop is starved and no key (not even 'q') is processed.
	tv.SetMaxLines(maxLogLines)
	tv.SetBorder(true).SetTitle(fmt.Sprintf(" logs: %s  ([::b]Esc[::-] back) ", name))
	stop := make(chan struct{})
	tv.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEscape || ev.Rune() == 'q' {
			close(stop)
			u.pages.RemovePage("logs")
			u.app.SetFocus(u.daemons)
			return nil
		}
		return ev
	})
	u.pages.AddPage("logs", tv, true, true)
	u.app.SetFocus(tv)
	go u.tail(path, tv, stop)
}

// maxLogLines bounds how many trailing lines the log view retains, and
// logFlushInterval throttles how often accumulated output is pushed to the UI.
// Together they keep a high-volume log from starving the tview event loop.
const (
	maxLogLines      = 5000
	logFlushInterval = 100 * time.Millisecond
)

// tail follows a growing log file, batching new bytes into the text view.
func (u *UI) tail(path string, tv *tview.TextView, stop <-chan struct{}) {
	f, err := openFollow(path)
	if err != nil {
		u.app.QueueUpdateDraw(func() { fmt.Fprintf(tv, "[red]cannot open log: %v\n", err) })
		return
	}
	defer f.Close()

	flush := func(chunk []byte) {
		u.app.QueueUpdateDraw(func() {
			tv.Write(chunk)
			tv.ScrollToEnd()
		})
	}
	// On EOF (caught up to the end of the file) wait briefly for more, unless asked to stop.
	onEOF := func() bool {
		select {
		case <-stop:
			return false
		case <-time.After(250 * time.Millisecond):
			return true
		}
	}
	pumpReader(f, stop, logFlushInterval, flush, onEOF)
}

// pumpReader reads r and delivers its bytes to flush, but coalesces reads so
// flush fires at most once per interval (plus once whenever the reader catches
// up or stop closes). This is what keeps a high-volume log from flooding the UI
// with per-chunk redraws and starving input handling. onEOF is called when the
// reader returns an error (typically io.EOF for a followed file); returning false
// ends the pump, true continues following.
func pumpReader(r io.Reader, stop <-chan struct{}, interval time.Duration, flush func([]byte), onEOF func() bool) {
	buf := make([]byte, 32*1024)
	var pending []byte
	last := time.Now()
	emit := func() {
		if len(pending) == 0 {
			return
		}
		flush(pending)
		pending = nil
		last = time.Now()
	}
	for {
		select {
		case <-stop:
			emit()
			return
		default:
		}
		n, err := r.Read(buf)
		if n > 0 {
			pending = append(pending, buf[:n]...)
			if time.Since(last) >= interval {
				emit()
			}
		}
		if err != nil {
			emit()
			if !onEOF() {
				return
			}
		}
	}
}

// ---- modals & chrome ---------------------------------------------------------

func (u *UI) showModal(page string, content tview.Primitive, width, height int) {
	modal := tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(content, height, 0, true).
			AddItem(nil, 0, 1, false), width, 0, true).
		AddItem(nil, 0, 1, false)
	u.pages.AddPage(page, modal, true, true)
	u.app.SetFocus(content)
}

func (u *UI) closeModal(page string) {
	u.pages.RemovePage(page)
	u.app.SetFocus(u.catalog)
}

func (u *UI) showHelp() {
	modal := tview.NewModal().
		SetText("rx — keys\n\n↑/↓  navigate\ntab   switch pane\nenter run script / open daemon logs\ns     stop daemon\nr     restart daemon\nl     daemon logs (Esc to leave)\nq     quit").
		AddButtons([]string{"OK"}).
		SetDoneFunc(func(int, string) { u.pages.RemovePage("help"); u.app.SetFocus(u.daemons) })
	u.pages.AddPage("help", modal, true, true)
	u.app.SetFocus(modal)
}

func (u *UI) setFooter(s string) { u.footer.SetText(" " + s) }

func (u *UI) quit() {
	close(u.done)
	u.app.Stop()
}

// ---- small helpers -----------------------------------------------------------

// openFollow opens a log file for reading from the beginning; the tailer then
// follows it as it grows.
func openFollow(path string) (*os.File, error) {
	return os.Open(path)
}

func headerCell(s string) *tview.TableCell {
	return tview.NewTableCell(s).SetTextColor(tcell.ColorYellow).SetSelectable(false).SetAttributes(tcell.AttrBold)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func joinDetail(a, b string) string {
	if a == "" {
		return b
	}
	return a + " · " + b
}

func joinStrings(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

func sortedScripts(m map[string]config.Script) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedWorkflows(m map[string]config.Workflow) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
