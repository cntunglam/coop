package tui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/cntunglam/coop/internal/catalog"
	"github.com/cntunglam/coop/internal/config"
	"github.com/cntunglam/coop/internal/edit"
	"github.com/cntunglam/coop/internal/runner"
)

const (
	scriptFormPage    = "script-form"
	confirmDeletePage = "confirm-delete"
)

// EditUnsupported is shown when asked to edit a workflow or group.
const EditUnsupported = "editing workflows and groups isn't supported yet — use `coop config edit`"

// Form field labels.
const (
	labelName       = "name"
	labelDesc       = "description"
	labelTags       = "tags"
	labelCommand    = "command"
	labelFile       = "file"
	labelFolder     = "folder"
	labelBackground = "run in background (daemon)"
	labelReadyPort  = "ready port"
	labelReadyWait  = "ready wait"
)

// scriptForm is the open add/edit form.
type scriptForm struct {
	oldName string        // "" when adding
	base    config.Script // snapshot when opened (passed to Put)
	draft   config.Script // carries presets/health/type/file/literal and swap results
	form    *tview.Form
	vals    formValues // last-read field text; keeps hidden ready fields across toggles
}

// formValues is the text of every form field, so the form can be rebuilt
// without losing what was typed.
type formValues struct {
	name, description, tags, body, folder string
	background                            bool
	readyPort, readyWait                  string
}

// EditScript opens the edit form for one script full-screen (`coop edit`); the
// app stops when the form closes.
func EditScript(run *runner.Runner, cfgPath, name string) error {
	if _, ok := run.Config().Scripts[name]; !ok {
		return fmt.Errorf("no script %q", name)
	}
	u := &UI{
		app:        tview.NewApplication(),
		cfg:        run.Config(),
		dm:         run.Daemons(),
		run:        run,
		cfgPath:    cfgPath,
		ed:         &edit.Editor{Path: cfgPath, Daemons: run.Daemons()},
		done:       make(chan struct{}),
		standalone: true,
	}
	u.build()
	u.openScriptForm(name)
	return u.app.Run()
}

// openScriptForm opens the add form (name == "") or the edit form for a script.
func (u *UI) openScriptForm(name string) {
	sf := &scriptForm{oldName: name, draft: config.Script{Literal: true}}
	if name != "" {
		s, ok := u.cfg.Scripts[name]
		if !ok {
			u.logActivity("[red]no script " + name)
			return
		}
		sf.base, sf.draft = s, cloneScript(s)
	}
	sf.vals = formValues{
		name:        name,
		description: sf.draft.Description,
		tags:        strings.Join(sf.draft.Tags, " "),
		body:        sf.draft.Command,
		folder:      sf.draft.Dir,
		background:  sf.draft.Background,
		readyPort:   sf.draft.ReadyPort,
	}
	if sf.draft.Type == config.TypeFile {
		sf.vals.body = sf.draft.File
	}
	if sf.draft.ReadyWait != 0 {
		sf.vals.readyWait = strconv.Itoa(sf.draft.ReadyWait)
	}
	u.sf = sf
	u.showScriptForm(0)
}

// showScriptForm (re)builds the form from u.sf.vals and shows it, focusing
// the item at index focus.
func (u *UI) showScriptForm(focus int) {
	sf := u.sf
	v := sf.vals
	form := tview.NewForm()
	title := " add script "
	if sf.oldName != "" {
		title = fmt.Sprintf(" edit %q ", sf.oldName)
	}
	form.SetBorder(true).SetTitle(title + " (Esc cancels) ")

	height := 0 // rows used by items, including the padding row after each
	addInput := func(label, text string) {
		form.AddInputField(label, text, 0, nil, nil)
		height += 2
	}
	addInput(labelName, v.name)
	addInput(labelDesc, v.description)
	addInput(labelTags, v.tags)
	if sf.draft.Type == config.TypeFile {
		addInput(labelFile, v.body)
	} else {
		form.AddTextArea(labelCommand, v.body, 0, 4, 0, nil)
		height += 5
	}
	addInput(labelFolder, v.folder)
	form.AddCheckbox(labelBackground, v.background, func(bool) { u.rebuildScriptForm() })
	height += 2
	if v.background {
		addInput(labelReadyPort, v.readyPort)
		addInput(labelReadyWait, v.readyWait)
	}
	if sf.draft.Health != nil {
		form.AddTextView("health check:", "edit in YAML for now", 0, 1, false, false)
		height += 2
	}

	form.AddButton("Save", u.submitScriptForm)
	if sf.oldName != "" {
		form.AddButton("Delete", func() { u.confirmDelete(sf.oldName) })
	}
	form.AddButton("Cancel", u.closeScriptForm)
	form.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEscape {
			u.closeScriptForm()
			return nil
		}
		return ev
	})
	form.SetFocus(focus)
	sf.form = form

	u.pages.RemovePage(scriptFormPage)
	u.showModal(scriptFormPage, form, 76, height+4) // + buttons row and border
}

// rebuildScriptForm redraws the form after the background box is toggled,
// keeping every value typed so far.
func (u *UI) rebuildScriptForm() {
	u.readFormValues()
	focus, _ := u.sf.form.GetFocusedItemIndex()
	u.showScriptForm(focus)
}

// readFormValues copies the form's fields into u.sf.vals and returns them.
// Fields not currently shown (ready port/wait while unchecked) keep their last value.
func (u *UI) readFormValues() formValues {
	sf := u.sf
	text := func(label string, dst *string) {
		switch item := sf.form.GetFormItemByLabel(label).(type) {
		case *tview.InputField:
			*dst = strings.TrimSpace(item.GetText())
		case *tview.TextArea:
			*dst = strings.TrimSpace(item.GetText())
		}
	}
	v := &sf.vals
	text(labelName, &v.name)
	text(labelDesc, &v.description)
	text(labelTags, &v.tags)
	text(labelCommand, &v.body)
	text(labelFile, &v.body)
	text(labelFolder, &v.folder)
	text(labelReadyPort, &v.readyPort)
	text(labelReadyWait, &v.readyWait)
	if cb, ok := sf.form.GetFormItemByLabel(labelBackground).(*tview.Checkbox); ok {
		v.background = cb.IsChecked()
	}
	return *v
}

// submitScriptForm reads the fields into the draft and saves it via the Editor.
func (u *UI) submitScriptForm() {
	sf := u.sf
	if sf == nil {
		return
	}
	v := u.readFormValues()
	s := sf.draft
	s.Description = v.description
	s.Tags = catalog.ParseTags(v.tags)
	if s.Type == config.TypeFile {
		s.File = v.body
	} else {
		s.Command = v.body
	}
	s.Dir = v.folder
	s.Background = v.background
	// Readiness only means something for a daemon; drop it for foreground scripts.
	s.ReadyPort, s.ReadyWait = "", 0
	if v.background {
		s.ReadyPort = v.readyPort
		if v.readyWait != "" {
			n, err := strconv.Atoi(v.readyWait)
			if err != nil || n < 0 {
				u.logActivity("[red]ready wait must be a whole number of seconds")
				return
			}
			s.ReadyWait = n
		}
	}

	var base *config.Script
	if sf.oldName != "" {
		b := sf.base
		base = &b
	}
	cfg, err := u.ed.Put(sf.oldName, v.name, s, base)
	switch {
	case errors.Is(err, edit.ErrChanged):
		u.logActivity("[red]" + sf.oldName + " changed elsewhere — close the form and run `reload`")
		return
	case err != nil:
		u.logActivity("[red]" + err.Error())
		return
	}
	u.applyConfig(cfg)
	running := sf.oldName != "" && u.ed.Running(sf.oldName)
	u.closeScriptForm()
	if running {
		u.logActivity("[yellow]saved " + v.name + " — daemon is running; changes apply on next start")
	} else {
		u.logActivity("[green]saved " + v.name)
	}
}

// confirmDelete asks before deleting the script being edited.
func (u *UI) confirmDelete(name string) {
	modal := tview.NewModal().
		SetText(fmt.Sprintf("delete %q?", name)).
		AddButtons([]string{"Delete", "Cancel"}).
		SetDoneFunc(func(_ int, label string) {
			u.pages.RemovePage(confirmDeletePage)
			if label == "Delete" {
				u.deleteScript(name)
				return
			}
			if u.sf != nil {
				u.app.SetFocus(u.sf.form) // back to the still-open form
			}
		})
	u.pages.AddPage(confirmDeletePage, modal, true, true)
	u.app.SetFocus(modal)
}

// deleteScript deletes a script (after confirmation) and closes the form.
func (u *UI) deleteScript(name string) {
	cfg, err := u.ed.Delete(name)
	if err != nil {
		u.logActivity("[red]" + err.Error())
		if u.sf != nil {
			u.app.SetFocus(u.sf.form)
		}
		return
	}
	u.applyConfig(cfg)
	u.closeScriptForm()
	u.logActivity("[green]deleted " + name)
}

// closeScriptForm closes the form; under `coop edit` that ends the app.
func (u *UI) closeScriptForm() {
	u.sf = nil
	u.closeModal(scriptFormPage)
	if u.standalone {
		u.app.Stop()
	}
}

// cloneScript copies s so the form's draft never shares maps or slices with
// the loaded config or the base snapshot.
func cloneScript(s config.Script) config.Script {
	s.Tags = append([]string(nil), s.Tags...)
	if s.Presets != nil {
		presets := make(map[string]map[string]string, len(s.Presets))
		for name, vals := range s.Presets {
			cp := make(map[string]string, len(vals))
			for k, v := range vals {
				cp[k] = v
			}
			presets[name] = cp
		}
		s.Presets = presets
	}
	if s.Health != nil {
		h := *s.Health
		s.Health = &h
	}
	return s
}

// editableScript reports whether a catalog entry kind can be edited in the form.
func editableScript(k catalog.Kind) bool {
	return k == catalog.KindScript || k == catalog.KindDaemon
}
