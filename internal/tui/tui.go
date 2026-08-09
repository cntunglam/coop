// Package tui implements coop's K9s-style full-screen terminal interface: a live
// table of running daemons over a catalog of scripts and workflows, with
// keystroke actions to run, stop, restart, and tail logs.
package tui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/cntunglam/coop/internal/config"
	"github.com/cntunglam/coop/internal/daemon"
	"github.com/cntunglam/coop/internal/runner"
	"github.com/cntunglam/coop/internal/script"
)

// refreshInterval is how often the daemon table re-polls process state.
const refreshInterval = time.Second

// entryKind distinguishes catalog rows.
type entryKind int

const (
	kindScript entryKind = iota
	kindWorkflow
	kindGroup
)

// catalogEntry is one row in the scripts/workflows pane.
type catalogEntry struct {
	name string
	kind entryKind
}

// UI holds the tview application and the coop dependencies it drives.
type UI struct {
	app   *tview.Application
	pages *tview.Pages
	cfg   *config.Config
	dm    *daemon.Manager
	run   *runner.Runner

	daemons *tview.Table
	catalog *tview.Table
	// console is the bottom pane: a scrollable coop activity log plus a command
	// input line (press `e` to type coop control verbs, Esc to return).
	consoleLog   *tview.TextView
	consoleInput *tview.InputField

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
// intermittent freeze can be diagnosed: `kill -USR1 <coop-pid>` from another
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
	dir := filepath.Join(base, "coop")
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "goroutines.txt")
}

func (u *UI) build() {
	// The TUI owns the terminal. The runner's progress messages (Status) default
	// to os.Stderr; if left there, starting a background daemon writes
	// "→ started daemon…/waiting for readiness…" straight onto the live screen,
	// desyncing tview (garbled until a resize forces a full repaint). Discard
	// them — the footer already reports status, and foreground commands still
	// write their own output via Stdout/Stderr while the UI is suspended.
	u.run.Status = io.Discard

	u.daemons = tview.NewTable().SetBorders(false).SetSelectable(true, false).SetFixed(1, 0)
	u.daemons.SetBorder(true).SetTitle(" DAEMONS ")
	u.daemons.SetSelectedFunc(func(row, _ int) { u.openLogsRow(row) })
	u.daemons.SetInputCapture(u.daemonKeys)

	u.catalog = tview.NewTable().SetBorders(false).SetSelectable(true, false).SetFixed(1, 0)
	u.catalog.SetBorder(true).SetTitle(" SCRIPTS / WORKFLOWS ")
	u.catalog.SetSelectedFunc(func(row, _ int) { u.activateRow(row) })
	u.catalog.SetInputCapture(u.catalogKeys)
	u.buildCatalog()

	// Bottom console: coop activity log + command input (coop control verbs).
	u.consoleLog = tview.NewTextView().SetDynamicColors(true).SetScrollable(true)
	u.consoleLog.SetMaxLines(2000)
	u.consoleLog.SetBorder(true).SetTitle(" LOG  ([::b]e[::-] command  ·  [::b]?[::-] help) ")
	u.consoleInput = tview.NewInputField().SetLabel("coop › ").SetFieldWidth(0)
	// High-contrast command bar: bright text on a distinct dark-blue field so
	// what you type is clearly readable against the panels.
	u.consoleInput.
		SetFieldBackgroundColor(tcell.NewRGBColor(0, 60, 110)).
		SetFieldTextColor(tcell.ColorWhite).
		SetLabelColor(tcell.ColorYellow)
	u.consoleInput.SetDoneFunc(u.onCommandDone)
	u.consoleInput.SetAutocompleteFunc(u.suggest)

	console := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(u.consoleLog, 0, 1, false).
		AddItem(u.consoleInput, 1, 0, false)

	main := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(u.daemons, 0, 2, true).
		AddItem(u.catalog, 0, 2, false).
		AddItem(console, 10, 0, false)

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
	// While typing a command, let every key reach the input field (so 'q', 'e',
	// etc. are typed, not treated as shortcuts). Esc/Enter are handled there.
	if u.app.GetFocus() == u.consoleInput {
		return ev
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
	case 'e':
		u.enterCommandMode()
		return nil
	case '?':
		u.showHelp()
		return nil
	}
	return ev
}

// enterCommandMode focuses the command input line.
func (u *UI) enterCommandMode() {
	u.consoleInput.SetLabel("coop › ")
	u.app.SetFocus(u.consoleInput)
}

// onCommandDone handles Enter (run the typed command) and Esc (return to the
// tables) in the command input.
func (u *UI) onCommandDone(key tcell.Key) {
	switch key {
	case tcell.KeyEscape:
		u.consoleInput.SetText("")
		u.app.SetFocus(u.daemons)
	case tcell.KeyEnter:
		line := strings.TrimSpace(u.consoleInput.GetText())
		u.consoleInput.SetText("")
		if line != "" {
			u.executeCommand(line)
		}
	}
}

// executeCommand runs an coop control verb typed in the command box. Grammar
// mirrors the CLI:
//
//	list                     list configured scripts/workflows
//	ps                       list running daemons
//	logs <daemon>            open a daemon's log view
//	stop <daemon>            stop a running daemon
//	restart <daemon>         restart a running daemon
//	start|run <name> [preset] run a script/workflow
//	config path|edit         show/edit the config (edit reloads on save)
//	<name> [preset]          same as run
func (u *UI) executeCommand(line string) {
	fields := strings.Fields(line)
	verb := fields[0]
	u.logActivity("[white]coop › " + line)
	switch verb {
	case "list":
		u.cmdList()
	case "ps":
		u.cmdPs()
	case "logs":
		if len(fields) < 2 {
			u.logActivity("[red]usage: logs <daemon>")
			return
		}
		u.openLogs(fields[1])
	case "stop":
		if len(fields) < 2 {
			u.logActivity("[red]usage: stop <daemon|group>")
			return
		}
		if _, ok := u.cfg.Groups[fields[1]]; ok {
			u.stopGroup(fields[1])
		} else {
			u.stopDaemon(fields[1])
		}
	case "restart":
		if len(fields) < 2 {
			u.logActivity("[red]usage: restart <daemon|group>")
			return
		}
		if _, ok := u.cfg.Groups[fields[1]]; ok {
			u.restartGroup(fields[1])
		} else {
			u.restartDaemon(fields[1])
		}
	case "start", "run":
		if len(fields) < 2 {
			u.logActivity("[red]usage: " + verb + " <name> [preset]")
			return
		}
		u.runByName(fields[1], argAt(fields, 2))
	case "config":
		u.cmdConfig(argAt(fields, 1))
	case "reload":
		if err := u.reloadConfig(); err != nil {
			u.logActivity("[red]reload: " + err.Error())
		} else {
			u.logActivity("[green]config reloaded")
		}
	default:
		u.runByName(fields[0], argAt(fields, 1))
	}
}

// commandVerbs are the verbs the command box understands (for autocomplete).
var commandVerbs = []string{"list", "ps", "logs", "stop", "restart", "start", "run", "config", "reload"}

// suggest returns autocomplete candidates for the command input as full-line
// completions. It completes one token at a time based on what precedes it:
// verb/name at the start, then arguments appropriate to that verb or name — in
// particular, a script's own presets after "<script> " or "start <script> ".
func (u *UI) suggest(current string) []string {
	if current == "" {
		return nil // don't pop a dropdown on an empty line
	}
	fields := strings.Fields(current)
	trailingSpace := strings.HasSuffix(current, " ")

	// Determine the token being completed: its partial text, and the fully-typed
	// tokens that precede it.
	partial := ""
	before := fields
	if !trailingSpace && len(fields) > 0 {
		partial = fields[len(fields)-1]
		before = fields[:len(fields)-1]
	}

	prefix := ""
	if len(before) > 0 {
		prefix = strings.Join(before, " ") + " "
	}
	var out []string
	for _, o := range u.completionOptions(before) {
		if strings.HasPrefix(o, partial) {
			out = append(out, prefix+o)
		}
	}
	sort.Strings(out)
	return dedupe(out)
}

// completionOptions returns the candidate tokens for the position right after
// the already-typed tokens in `before`.
func (u *UI) completionOptions(before []string) []string {
	switch len(before) {
	case 0: // first token: a verb or a runnable name
		return append(append([]string{}, commandVerbs...), u.allNames()...)
	case 1:
		switch head := before[0]; head {
		case "stop", "restart", "logs":
			return u.daemonAndGroupNames()
		case "start", "run":
			return u.allNames()
		case "config":
			return []string{"path", "edit"}
		case "list", "ps", "reload":
			return nil
		default:
			// A bare script name → offer its presets.
			if s, ok := u.cfg.Scripts[head]; ok {
				return s.PresetNames()
			}
			return nil
		}
	case 2:
		// `start <script> ` / `run <script> ` → the script's presets.
		if before[0] == "start" || before[0] == "run" {
			if s, ok := u.cfg.Scripts[before[1]]; ok {
				return s.PresetNames()
			}
		}
		return nil
	}
	return nil
}

// allNames returns every runnable name (scripts, workflows, groups), sorted.
func (u *UI) allNames() []string {
	var out []string
	for n := range u.cfg.Scripts {
		out = append(out, n)
	}
	for n := range u.cfg.Workflows {
		out = append(out, n)
	}
	for n := range u.cfg.Groups {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// daemonAndGroupNames returns background scripts + groups (stop/restart/logs targets).
func (u *UI) daemonAndGroupNames() []string {
	var out []string
	for n, s := range u.cfg.Scripts {
		if s.Background {
			out = append(out, n)
		}
	}
	for n := range u.cfg.Groups {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// dedupe returns ss with consecutive duplicates removed (input must be sorted).
func dedupe(ss []string) []string {
	var out []string
	for i, s := range ss {
		if i == 0 || s != ss[i-1] {
			out = append(out, s)
		}
	}
	return out
}

// cmdList logs the configured scripts and workflows to the activity log.
func (u *UI) cmdList() {
	if len(u.entries) == 0 {
		u.logActivity("[gray](nothing configured — try `config edit`)")
		return
	}
	u.logActivity("[yellow]scripts / workflows:")
	for _, e := range u.entries {
		kind := "script"
		if e.kind == kindWorkflow {
			kind = "workflow"
		}
		u.logActivity("  " + e.name + "  [gray]" + kind)
	}
}

// cmdPs logs running daemons. dm.List() blocks (readiness probes), so it runs
// off the UI thread and reports back via a queued update.
func (u *UI) cmdPs() {
	go func() {
		list, err := u.dm.List()
		u.app.QueueUpdateDraw(func() {
			if err != nil {
				u.logActivity("[red]" + err.Error())
				return
			}
			if len(list) == 0 {
				u.logActivity("[gray]no daemons running")
				return
			}
			u.logActivity("[yellow]daemons:")
			for _, st := range list {
				state, _ := daemonState(st)
				u.logActivity(fmt.Sprintf("  %s  [gray]pid %d  %s", st.Name, st.PID, state))
			}
		})
	}()
}

// cmdConfig handles `config path` and `config edit` (edit reloads on save).
func (u *UI) cmdConfig(sub string) {
	switch sub {
	case "", "path":
		u.logActivity(u.cfgPath)
	case "edit":
		u.suspendRun(func() error {
			editor := os.Getenv("EDITOR")
			if editor == "" {
				editor = "vi"
			}
			cmd := exec.Command(editor, u.cfgPath)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
			return cmd.Run()
		})
		if err := u.reloadConfig(); err != nil {
			u.logActivity("[red]config reload: " + err.Error())
		} else {
			u.logActivity("[green]config reloaded")
		}
	default:
		u.logActivity("[red]usage: config path|edit")
	}
}

// reloadConfig re-reads the config from disk and refreshes the catalog. It
// updates the shared config in place so the runner (same pointer) sees changes.
func (u *UI) reloadConfig() error {
	newCfg, err := config.Load(u.cfgPath)
	if err != nil {
		return err
	}
	*u.cfg = *newCfg
	u.buildCatalog()
	return nil
}

// runByName runs a configured script, workflow, or group from the command box.
func (u *UI) runByName(name, preset string) {
	if _, ok := u.cfg.Groups[name]; ok {
		u.startGroup(name)
		return
	}
	if _, ok := u.cfg.Workflows[name]; ok {
		u.launchWorkflow(name)
		return
	}
	if _, ok := u.cfg.Scripts[name]; ok {
		u.launchScript(name, preset)
		return
	}
	u.logActivity("[red]unknown script, workflow, or group: " + name)
}

// argAt returns fields[i] or "" if out of range.
func argAt(fields []string, i int) string {
	if i < len(fields) {
		return fields[i]
	}
	return ""
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
					u.logActivity("[red]error listing daemons: " + err.Error())
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
				u.logActivity("[red]error listing daemons: " + err.Error())
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
		u.logActivity("[red]error listing daemons: " + err.Error())
		return
	}
	u.renderDaemons(list)
}

// renderDaemons updates the daemon table cells. UI-thread only; does no I/O.
func (u *UI) renderDaemons(list []daemon.Status) {
	// Preserve the current selection by name across refreshes.
	selName, _ := u.selectedDaemon()

	u.daemons.Clear()
	headers := []string{"NAME", "GROUP", "PRESET", "PORT", "PID", "UPTIME", "STATE", "HEALTH", "COMMAND"}
	for c, h := range headers {
		u.daemons.SetCell(0, c, headerCell(h))
	}
	if len(list) == 0 {
		// A selectable table with zero selectable cells makes tview's
		// Table.InputHandler spin forever searching for one (100% CPU, frozen UI).
		// Disable selection while empty; re-enable once there are real rows.
		u.daemons.SetSelectable(false, false)
		u.daemons.SetCell(1, 0, tview.NewTableCell("(no daemons running)").SetTextColor(tcell.ColorGray).SetSelectable(false))
		return
	}
	// Sort by group then name so daemons in the same namespace sit together.
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Group != list[j].Group {
			return list[i].Group < list[j].Group
		}
		return list[i].Name < list[j].Name
	})
	u.daemons.SetSelectable(true, false)
	selRow := 1
	for i, st := range list {
		row := i + 1
		if st.Name == selName {
			selRow = row
		}
		pid := "-"
		uptime := "-"
		if st.Alive {
			pid = fmt.Sprintf("%d", st.PID)
			uptime = time.Since(time.Unix(st.StartUnix, 0)).Round(time.Second).String()
		}
		stateText, stateColor := daemonState(st)
		u.daemons.SetCell(row, 0, tview.NewTableCell(st.Name).SetTextColor(tcell.ColorWhite))
		u.daemons.SetCell(row, 1, tview.NewTableCell(dash(st.Group)).SetTextColor(tcell.ColorAqua))
		u.daemons.SetCell(row, 2, tview.NewTableCell(dash(st.Preset)))
		u.daemons.SetCell(row, 3, tview.NewTableCell(u.daemonPort(st)).SetTextColor(tcell.ColorLightBlue))
		u.daemons.SetCell(row, 4, tview.NewTableCell(pid))
		u.daemons.SetCell(row, 5, tview.NewTableCell(uptime))
		u.daemons.SetCell(row, 6, tview.NewTableCell(stateText).SetTextColor(stateColor))
		health := st.HealthDisplay()
		u.daemons.SetCell(row, 7, tview.NewTableCell(dash(health)).SetTextColor(healthColor(health)))
		// COMMAND flexes to fill the remaining terminal width (responsive).
		u.daemons.SetCell(row, 8, tview.NewTableCell(st.Command).SetTextColor(tcell.ColorGray).SetExpansion(1))
	}
	u.daemons.Select(selRow, 0)
}

// daemonPort reports the port a daemon exposes: its ready_port, or failing that
// a "port" value from its preset in config; "-" if neither is known.
func (u *UI) daemonPort(st daemon.Status) string {
	if st.ReadyPort > 0 {
		return strconv.Itoa(st.ReadyPort)
	}
	if s, ok := u.cfg.Scripts[st.Name]; ok && st.Preset != "" {
		if v, ok := s.Presets[st.Preset]["port"]; ok && v != "" {
			return v
		}
	}
	return "-"
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

// healthColor maps a health label to a cell color; empty/"-" renders gray.
func healthColor(state string) tcell.Color {
	switch state {
	case "healthy":
		return tcell.ColorGreen
	case "unhealthy":
		return tcell.ColorYellow
	case "failed":
		return tcell.ColorRed
	default:
		return tcell.ColorGray
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
	u.logActivity("[yellow]stopping " + name + "…")
	go func() {
		err := u.dm.Stop(name, 10*time.Second)
		list, lerr := u.dm.List() // off the UI thread
		u.app.QueueUpdateDraw(func() {
			if err != nil {
				u.logActivity("[red]stop " + name + ": " + err.Error())
			} else {
				u.logActivity("[green]stopped " + name)
			}
			if lerr == nil {
				u.renderDaemons(list)
			}
		})
	}()
}

func (u *UI) restartDaemon(name string) {
	u.logActivity("[yellow]restarting " + name + "…")
	go func() {
		err := u.run.RestartDaemon(name)
		list, lerr := u.dm.List() // off the UI thread
		u.app.QueueUpdateDraw(func() {
			if err != nil {
				u.logActivity("[red]restart " + name + ": " + err.Error())
			} else {
				u.logActivity("[green]restarted " + name)
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
	for _, name := range u.cfg.GroupNames() {
		g := u.cfg.Groups[name]
		detail := joinDetail(g.Description, fmt.Sprintf("%d member(s)", len(g.Members)))
		if g.AutoRestart {
			detail = joinDetail(detail, "auto-restart")
		}
		u.catalog.SetCell(row, 0, tview.NewTableCell(name).SetTextColor(tcell.ColorWhite))
		u.catalog.SetCell(row, 1, tview.NewTableCell("group").SetTextColor(tcell.ColorAqua))
		u.catalog.SetCell(row, 2, tview.NewTableCell(detail).SetTextColor(tcell.ColorGray))
		u.entries = append(u.entries, catalogEntry{name: name, kind: kindGroup})
		row++
	}
	if len(u.entries) == 0 {
		// Same tview infinite-loop guard as the daemon table: never leave a
		// selectable table with zero selectable cells.
		u.catalog.SetSelectable(false, false)
		u.catalog.SetCell(1, 0, tview.NewTableCell("(nothing configured — see `coop config edit`)").
			SetTextColor(tcell.ColorGray).SetSelectable(false))
		return
	}
	u.catalog.SetSelectable(true, false)
	u.catalog.Select(1, 0)
}

func (u *UI) activateRow(row int) {
	idx := row - 1
	if idx < 0 || idx >= len(u.entries) {
		return
	}
	e := u.entries[idx]
	switch e.kind {
	case kindWorkflow:
		u.launchWorkflow(e.name)
		return
	case kindGroup:
		u.startGroup(e.name)
		return
	}
	s := u.cfg.Scripts[e.name]
	if len(s.Presets) > 0 {
		u.pickPreset(e.name, s, func(preset string) { u.launchScript(e.name, preset) })
		return
	}
	u.launchScript(e.name, "")
}

// startGroup / stopGroup / restartGroup bring a whole group up or down from the
// TUI, off the UI thread, then refresh the daemon table.
func (u *UI) startGroup(name string) {
	u.groupOp("starting", name, u.run.StartGroup)
}
func (u *UI) stopGroup(name string) {
	u.groupOp("stopping", name, u.run.StopGroup)
}
func (u *UI) restartGroup(name string) {
	u.groupOp("restarting", name, u.run.RestartGroup)
}

func (u *UI) groupOp(verb, name string, op func(string) error) {
	u.logActivity("[yellow]" + verb + " group " + name + "…")
	go func() {
		err := op(name)
		list, lerr := u.dm.List() // off the UI thread
		u.app.QueueUpdateDraw(func() {
			if err != nil {
				u.logActivity("[red]group " + name + ": " + err.Error())
			} else {
				u.logActivity("[green]group " + name + " " + verb + " done")
			}
			if lerr == nil {
				u.renderDaemons(list)
			}
		})
	}()
}

func (u *UI) launchScript(name, preset string) {
	s := u.cfg.Scripts[name]
	if s.Background {
		u.logActivity("[yellow]starting " + name + "…")
		go func() {
			err := u.run.RunScript(name, preset)
			list, lerr := u.dm.List() // off the UI thread
			u.app.QueueUpdateDraw(func() {
				if err != nil {
					u.logActivity("[red]" + err.Error())
				} else {
					u.logActivity("[green]started " + name)
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
// The child process shares coop's process group and controlling terminal, so a
// Ctrl-C typed to abort a stuck command (e.g. ssh to an unreachable host) would
// otherwise also SIGINT coop and kill the whole app. We catch and swallow SIGINT
// in coop for the duration: the child inherits the default handler (os/exec resets
// caught signals to SIG_DFL in the child) and dies on Ctrl-C, while coop survives
// and returns to the UI.
func (u *UI) suspendRun(fn func() error) {
	u.app.Suspend(func() {
		sig := make(chan os.Signal, 1)
		stop := make(chan struct{})
		signal.Notify(sig, os.Interrupt)
		go func() {
			for {
				select {
				case <-sig: // swallow: let the child handle Ctrl-C; coop must live
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
		fmt.Print("\n[Ctrl-C aborts the command; press Enter to return to coop]")
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
			u.logActivity("[red]preset name is required")
			return
		}
		if _, exists := s.Presets[presetName]; exists {
			u.logActivity("[red]preset " + presetName + " already exists")
			return
		}
		values := map[string]string{}
		for _, p := range params {
			values[p] = field(p)
		}
		if err := u.savePreset(scriptName, presetName, values); err != nil {
			u.logActivity("[red]" + err.Error())
			return
		}
		u.closeModal(presetFormPage)
		u.logActivity("[green]added preset " + presetName + " to " + scriptName)
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
		u.logActivity("[red]" + err.Error())
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
		SetText("coop — keys\n\n↑/↓  navigate\ntab   switch pane\nenter run script / open daemon logs\nn     add a preset to the selected script\ns     stop daemon\nr     restart daemon\nl     daemon logs (Esc to leave)\ne     command mode — type coop verbs (Esc back)\nq     quit\n\ncommand verbs (Tab to autocomplete):\n  list · ps · logs <daemon> · reload\n  stop <daemon|group> · restart <daemon|group>\n  start <name> [preset]\n  config path|edit\n  <name> [preset]   run a script / workflow / group").
		AddButtons([]string{"OK"}).
		SetDoneFunc(func(int, string) { u.pages.RemovePage("help"); u.app.SetFocus(u.daemons) })
	u.pages.AddPage("help", modal, true, true)
	u.app.SetFocus(modal)
}

// logActivity appends a line to the console activity log and scrolls to it.
func (u *UI) logActivity(s string) {
	fmt.Fprintln(u.consoleLog, s)
	u.consoleLog.ScrollToEnd()
}

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
