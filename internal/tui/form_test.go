package tui

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/cntunglam/coop/internal/config"
	"github.com/cntunglam/coop/internal/script"
)

const formYAML = `
scripts:
  alpha:
    command: "echo {{.x}}"
    presets:
      one: { x: "1" }
  bg:
    command: "sleep 30"
    background: true
workflows:
  flow:
    steps:
      - { script: bg }
`

// frontPage returns the name of the page currently on top.
func frontPage(do func(func()), u *UI) (front string) {
	do(func() { front, _ = u.pages.GetFrontPage() })
	return
}

// logText returns the activity log's contents.
func logText(do func(func()), u *UI) (s string) {
	do(func() { s = u.consoleLog.GetText(true) })
	return
}

// setInput sets the text of the form's input field with the given label.
func setInput(u *UI, label, text string) {
	u.sf.form.GetFormItemByLabel(label).(*tview.InputField).SetText(text)
}

// pressButton activates the form button with the given label.
func pressButton(u *UI, label string) {
	b := u.sf.form.GetButton(u.sf.form.GetButtonIndex(label))
	b.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), func(tview.Primitive) {})
}

func diskConfig(t *testing.T, u *UI) *config.Config {
	t.Helper()
	cfg, err := config.Load(u.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// selectEntry moves the catalog cursor to the named entry and focuses the catalog.
func selectEntry(t *testing.T, u *UI, name string) {
	t.Helper()
	for i, e := range u.entries {
		if e.Name == name {
			u.app.SetFocus(u.catalog)
			u.catalog.Select(i+1, 0)
			return
		}
	}
	t.Fatalf("no catalog entry %q", name)
}

func TestAKeyOpensEmptyForm(t *testing.T) {
	u := testUI(t, formYAML)
	do := startSim(t, u)
	do(func() { u.app.SetFocus(u.catalog) })
	u.app.QueueEvent(tcell.NewEventKey(tcell.KeyRune, 'a', tcell.ModNone))
	waitFor(t, 3*time.Second, func() bool { return frontPage(do, u) == scriptFormPage })
	do(func() {
		if front, _ := u.pages.GetFrontPage(); front != scriptFormPage {
			t.Fatalf("front page = %q, want %q", front, scriptFormPage)
		}
		if got := u.sf.form.GetFormItemByLabel("name").(*tview.InputField).GetText(); got != "" {
			t.Errorf("name = %q, want empty", got)
		}
		if u.sf.oldName != "" || !u.sf.draft.Literal {
			t.Errorf("add form state = %+v", u.sf)
		}
	})
}

func TestEditFormSavesChanges(t *testing.T) {
	u := testUI(t, formYAML)
	do := startSim(t, u)
	do(func() {
		u.openScriptForm("alpha")
		setInput(u, "description", "say x")
		setInput(u, "tags", "prod, k8s")
		u.submitScriptForm()
	})
	if front := frontPage(do, u); front != "main" {
		t.Errorf("front page = %q after save, want main", front)
	}
	s := diskConfig(t, u).Scripts["alpha"]
	if s.Description != "say x" || !reflect.DeepEqual(s.Tags, []string{"prod", "k8s"}) {
		t.Errorf("disk script = %+v", s)
	}
	if s.Presets["one"]["x"] != "1" || s.Command != "echo {{.x}}" {
		t.Errorf("presets/command not preserved: %+v", s)
	}
	if !strings.Contains(logText(do, u), "saved alpha") {
		t.Errorf("log = %q", logText(do, u))
	}
	if u.cfg.Scripts["alpha"].Description != "say x" {
		t.Error("in-memory config not updated")
	}
}

func TestAddFormSavesLiteral(t *testing.T) {
	u := testUI(t, formYAML)
	do := startSim(t, u)
	cmd := "docker ps --format '{{.Names}}'"
	do(func() {
		u.openScriptForm("")
		setInput(u, "name", "new1")
		u.sf.form.GetFormItemByLabel("command").(*tview.TextArea).SetText(cmd, true)
		u.submitScriptForm()
	})
	s, ok := diskConfig(t, u).Scripts["new1"]
	if !ok || !s.Literal || s.Command != cmd {
		t.Errorf("disk new1 = %+v (ok=%v)", s, ok)
	}
}

func TestEditFormRename(t *testing.T) {
	u := testUI(t, formYAML)
	do := startSim(t, u)
	do(func() {
		u.openScriptForm("alpha")
		setInput(u, "name", "beta")
		u.submitScriptForm()
	})
	disk := diskConfig(t, u)
	if _, ok := disk.Scripts["alpha"]; ok {
		t.Error("alpha still on disk")
	}
	if s, ok := disk.Scripts["beta"]; !ok || s.Presets["one"]["x"] != "1" {
		t.Errorf("beta = %+v (ok=%v)", s, ok)
	}
}

func TestEditFormRenameRefusedWhenReferenced(t *testing.T) {
	u := testUI(t, formYAML)
	do := startSim(t, u)
	do(func() {
		u.openScriptForm("bg")
		setInput(u, "name", "bg2")
		u.submitScriptForm()
	})
	if front := frontPage(do, u); front != scriptFormPage {
		t.Errorf("front page = %q, want form to stay open", front)
	}
	disk := diskConfig(t, u)
	if _, ok := disk.Scripts["bg"]; !ok {
		t.Error("bg gone from disk")
	}
	if _, ok := disk.Scripts["bg2"]; ok {
		t.Error("bg2 written to disk")
	}
	if log := logText(do, u); !strings.Contains(log, `workflow "flow"`) {
		t.Errorf("log = %q", log)
	}
}

func TestDeleteAsksThenDeletes(t *testing.T) {
	u := testUI(t, formYAML)
	do := startSim(t, u)
	do(func() {
		u.openScriptForm("alpha")
		pressButton(u, "Delete")
	})
	if front := frontPage(do, u); front != confirmDeletePage {
		t.Fatalf("front page = %q, want %q", front, confirmDeletePage)
	}
	if _, ok := diskConfig(t, u).Scripts["alpha"]; !ok {
		t.Fatal("deleted before confirmation")
	}
	u.app.QueueEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)) // "Delete" is focused
	waitFor(t, 3*time.Second, func() bool { return frontPage(do, u) == "main" })
	if front := frontPage(do, u); front != "main" {
		t.Errorf("front page = %q after delete, want main", front)
	}
	if _, ok := diskConfig(t, u).Scripts["alpha"]; ok {
		t.Error("alpha still on disk after confirmed delete")
	}
}

func TestEditRunningDaemonMessage(t *testing.T) {
	u := testUI(t, formYAML)
	spec, err := script.Resolve("bg", u.cfg.Scripts["bg"], "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.dm.Start(spec); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = u.dm.Stop("bg", 2*time.Second) })
	do := startSim(t, u)
	do(func() {
		u.openScriptForm("bg")
		setInput(u, "description", "sleeper")
		u.submitScriptForm()
	})
	if log := logText(do, u); !strings.Contains(log, "saved bg — daemon is running; changes apply on next start") {
		t.Errorf("log = %q", log)
	}
	if got := diskConfig(t, u).Scripts["bg"].Description; got != "sleeper" {
		t.Errorf("description on disk = %q", got)
	}
}

func TestEKeyOnWorkflowShowsMessage(t *testing.T) {
	u := testUI(t, formYAML)
	do := startSim(t, u)
	do(func() { selectEntry(t, u, "flow") })
	u.app.QueueEvent(tcell.NewEventKey(tcell.KeyRune, 'E', tcell.ModNone))
	waitFor(t, 3*time.Second, func() bool { return strings.Contains(logText(do, u), "isn't supported yet") })
	if log := logText(do, u); !strings.Contains(log, "isn't supported yet") {
		t.Errorf("log = %q", log)
	}
	if front := frontPage(do, u); front != "main" {
		t.Errorf("front page = %q, want main", front)
	}
}

func TestEKeyOpensEditForm(t *testing.T) {
	u := testUI(t, formYAML)
	do := startSim(t, u)
	do(func() { selectEntry(t, u, "bg") })
	u.app.QueueEvent(tcell.NewEventKey(tcell.KeyRune, 'E', tcell.ModNone))
	waitFor(t, 3*time.Second, func() bool { return frontPage(do, u) == scriptFormPage })
	do(func() {
		if u.sf == nil || u.sf.oldName != "bg" {
			t.Fatalf("form state = %+v", u.sf)
		}
		if got := u.sf.form.GetFormItemByLabel("name").(*tview.InputField).GetText(); got != "bg" {
			t.Errorf("name = %q", got)
		}
	})
}

func TestBackgroundToggleShowsReadyFields(t *testing.T) {
	u := testUI(t, formYAML)
	do := startSim(t, u)
	const box = "run in background (daemon)"
	// toggle focuses the checkbox and presses space, like a user would.
	toggle := func() {
		do(func() {
			u.sf.form.SetFocus(u.sf.form.GetFormItemIndex(box))
			u.app.SetFocus(u.sf.form)
		})
		u.app.QueueEvent(tcell.NewEventKey(tcell.KeyRune, ' ', tcell.ModNone))
	}
	hasReady := func() (ok bool) {
		do(func() { ok = u.sf.form.GetFormItemByLabel("ready port") != nil })
		return
	}
	do(func() {
		u.openScriptForm("alpha")
		setInput(u, "description", "typed")
		u.sf.form.GetFormItemByLabel("command").(*tview.TextArea).SetText("echo typed", true)
	})
	if hasReady() {
		t.Fatal("ready port shown for a foreground script")
	}
	toggle()
	waitFor(t, 3*time.Second, hasReady)
	if !hasReady() {
		t.Fatal("ready port not shown after checking the box")
	}
	do(func() {
		setInput(u, "ready port", "8080")
		if got := u.sf.form.GetFormItemByLabel("description").(*tview.InputField).GetText(); got != "typed" {
			t.Errorf("description after rebuild = %q", got)
		}
		if got := u.sf.form.GetFormItemByLabel("command").(*tview.TextArea).GetText(); got != "echo typed" {
			t.Errorf("command after rebuild = %q", got)
		}
	})
	toggle()
	waitFor(t, 3*time.Second, func() bool { return !hasReady() })
	if hasReady() {
		t.Fatal("ready port still shown after unchecking the box")
	}
	toggle()
	waitFor(t, 3*time.Second, hasReady)
	do(func() {
		if got := u.sf.form.GetFormItemByLabel("ready port").(*tview.InputField).GetText(); got != "8080" {
			t.Errorf("ready port after re-check = %q, want kept value", got)
		}
	})
}

func TestReadyWaitMustBeWholeNumber(t *testing.T) {
	u := testUI(t, formYAML)
	do := startSim(t, u)
	do(func() {
		u.openScriptForm("bg")
		setInput(u, "ready wait", "1.5")
		u.submitScriptForm()
	})
	if log := logText(do, u); !strings.Contains(log, "ready wait must be a whole number of seconds") {
		t.Errorf("log = %q", log)
	}
	if front := frontPage(do, u); front != scriptFormPage {
		t.Errorf("front page = %q, want form to stay open", front)
	}
}

func TestEditFormChangedElsewhere(t *testing.T) {
	u := testUI(t, formYAML)
	do := startSim(t, u)
	do(func() { u.openScriptForm("alpha") })
	if _, err := u.ed.Put("alpha", "alpha", config.Script{Command: "echo other"}, nil); err != nil {
		t.Fatal(err)
	}
	do(func() {
		setInput(u, "description", "mine")
		u.submitScriptForm()
	})
	if log := logText(do, u); !strings.Contains(log, "alpha changed elsewhere — close the form and run `reload`") {
		t.Errorf("log = %q", log)
	}
	if got := diskConfig(t, u).Scripts["alpha"].Command; got != "echo other" {
		t.Errorf("outside write overwritten: command = %q", got)
	}
}

// formErrorText returns the error line shown inside the open form.
func formErrorText(do func(func()), u *UI) (s string) {
	do(func() { s = u.sf.errView.GetText(true) })
	return
}

// TestFormShowsSaveError: a failed Save shows its error inside the form (not
// only in the log behind it), and the error survives a form rebuild.
func TestFormShowsSaveError(t *testing.T) {
	u := testUI(t, formYAML)
	do := startSim(t, u)
	do(func() {
		u.openScriptForm("bg")
		setInput(u, "ready wait", "1.5")
		u.submitScriptForm()
	})
	want := "ready wait must be a whole number of seconds"
	if got := formErrorText(do, u); got != want {
		t.Errorf("form error = %q, want %q", got, want)
	}
	do(func() { u.rebuildScriptForm() })
	if got := formErrorText(do, u); got != want {
		t.Errorf("form error after rebuild = %q, want %q", got, want)
	}
	do(func() {
		setInput(u, "ready wait", "")
		setInput(u, "name", "flow") // taken by the workflow
		u.submitScriptForm()
	})
	if got := formErrorText(do, u); !strings.Contains(got, `"flow" is already taken`) {
		t.Errorf("form error = %q, want the latest error", got)
	}
}

// TestFormButtonsAndErrorOnScreen renders the form: the buttons row and the
// error line both fit inside the box.
func TestFormButtonsAndErrorOnScreen(t *testing.T) {
	u := testUI(t, formYAML)
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	u.app.SetScreen(screen)
	go func() { _ = u.app.Run() }()
	t.Cleanup(u.app.Stop)
	time.Sleep(200 * time.Millisecond)
	do := func(fn func()) {
		done := make(chan struct{})
		u.app.QueueUpdateDraw(func() { fn(); close(done) })
		<-done
	}
	do(func() {
		screen.SetSize(90, 40) // only sticks once the app is running
		u.openScriptForm("alpha")
		setInput(u, "name", "flow")
		u.submitScriptForm()
	})
	do(func() {}) // one more draw
	cells, w, h := screen.GetContents()
	if w != 90 || h != 40 {
		t.Fatalf("screen is %dx%d, want 90x40", w, h)
	}
	var text strings.Builder
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if r := cells[y*w+x].Runes; len(r) > 0 {
				text.WriteRune(r[0])
			} else {
				text.WriteRune(' ')
			}
		}
		text.WriteRune('\n')
	}
	for _, want := range []string{"Save     Make swappable…", `║ "flow" is already taken`} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("screen lacks %q:\n%s", want, text.String())
		}
	}
}

func TestEditFormChangedElsewhereShownInForm(t *testing.T) {
	u := testUI(t, formYAML)
	do := startSim(t, u)
	do(func() { u.openScriptForm("alpha") })
	if _, err := u.ed.Put("alpha", "alpha", config.Script{Command: "echo other"}, nil); err != nil {
		t.Fatal(err)
	}
	do(func() { u.submitScriptForm() })
	if got := formErrorText(do, u); got != "alpha changed elsewhere — close the form and run `reload`" {
		t.Errorf("form error = %q", got)
	}
}

func TestFileScriptShowsFileField(t *testing.T) {
	u := testUI(t, "scripts:\n  f:\n    type: file\n    file: /bin/true\n")
	do := startSim(t, u)
	do(func() {
		u.openScriptForm("f")
		if u.sf.form.GetFormItemByLabel("file") == nil {
			t.Error("file field missing")
		}
		if u.sf.form.GetFormItemByLabel("command") != nil {
			t.Error("command field shown for a file script")
		}
		if u.sf.form.GetButtonIndex("Make swappable…") != -1 {
			t.Error("Make swappable offered for a file script")
		}
	})
}

// TestStandaloneCancelStopsApp: in `coop edit` mode, closing the form ends the app.
func TestStandaloneCancelStopsApp(t *testing.T) {
	u := testUI(t, formYAML)
	u.standalone = true
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	u.app.SetScreen(screen)
	u.openScriptForm("alpha")
	done := make(chan error, 1)
	go func() { done <- u.app.Run() }()
	t.Cleanup(u.app.Stop)
	time.Sleep(200 * time.Millisecond)
	u.app.QueueEvent(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("app still running after Esc in standalone form")
	}
}

type editResult struct {
	msg string
	err error
}

// startStandalone runs the `coop edit` flow for name on a simulation screen. It
// returns a helper that runs fn on the UI goroutine, and the channel that
// receives runEditForm's result once the app stops.
func startStandalone(t *testing.T, u *UI, name string) (func(func()), <-chan editResult) {
	t.Helper()
	u.standalone = true
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	u.app.SetScreen(screen)
	res := make(chan editResult, 1)
	go func() {
		msg, err := u.runEditForm(name)
		res <- editResult{msg, err}
	}()
	t.Cleanup(u.app.Stop)
	time.Sleep(200 * time.Millisecond)
	do := func(fn func()) {
		done := make(chan struct{})
		u.app.QueueUpdateDraw(func() { fn(); close(done) })
		<-done
	}
	return do, res
}

func waitResult(t *testing.T, res <-chan editResult) string {
	t.Helper()
	select {
	case r := <-res:
		if r.err != nil {
			t.Fatal(r.err)
		}
		return r.msg
	case <-time.After(5 * time.Second):
		t.Fatal("standalone form did not exit")
		return ""
	}
}

func TestStandaloneSaveReturnsMessage(t *testing.T) {
	u := testUI(t, formYAML)
	do, res := startStandalone(t, u, "alpha")
	do(func() {
		setInput(u, "description", "edited")
		u.submitScriptForm()
	})
	if msg := waitResult(t, res); msg != "saved alpha" {
		t.Errorf("outcome = %q, want %q", msg, "saved alpha")
	}
}

func TestStandaloneSaveRunningDaemonReturnsNotice(t *testing.T) {
	u := testUI(t, formYAML)
	spec, err := script.Resolve("bg", u.cfg.Scripts["bg"], "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.dm.Start(spec); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = u.dm.Stop("bg", 2*time.Second) })
	do, res := startStandalone(t, u, "bg")
	do(func() {
		setInput(u, "description", "sleeper")
		u.submitScriptForm()
	})
	want := "saved bg — daemon is running; changes apply on next start"
	if msg := waitResult(t, res); msg != want {
		t.Errorf("outcome = %q, want %q", msg, want)
	}
}

func TestStandaloneDeleteReturnsMessage(t *testing.T) {
	u := testUI(t, formYAML)
	do, res := startStandalone(t, u, "alpha")
	do(func() { pressButton(u, "Delete") })
	u.app.QueueEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)) // confirm
	if msg := waitResult(t, res); msg != "deleted alpha" {
		t.Errorf("outcome = %q, want %q", msg, "deleted alpha")
	}
	if _, ok := diskConfig(t, u).Scripts["alpha"]; ok {
		t.Error("alpha still on disk")
	}
}

func TestStandaloneCancelReturnsNoMessage(t *testing.T) {
	u := testUI(t, formYAML)
	_, res := startStandalone(t, u, "alpha")
	u.app.QueueEvent(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	if msg := waitResult(t, res); msg != "" {
		t.Errorf("outcome = %q, want empty", msg)
	}
}

// TestStandaloneChangedElsewhere: `coop edit` has no reload, so the advice is
// to cancel and run coop edit again; the form stays open.
func TestStandaloneChangedElsewhere(t *testing.T) {
	u := testUI(t, formYAML)
	do, _ := startStandalone(t, u, "alpha")
	if _, err := u.ed.Put("alpha", "alpha", config.Script{Command: "echo other"}, nil); err != nil {
		t.Fatal(err)
	}
	do(func() {
		setInput(u, "description", "mine")
		u.submitScriptForm()
	})
	want := "alpha changed elsewhere — cancel and run coop edit again"
	if got := formErrorText(do, u); got != want {
		t.Errorf("form error = %q, want %q", got, want)
	}
	if front := frontPage(do, u); front != scriptFormPage {
		t.Errorf("front page = %q, want form to stay open", front)
	}
}
