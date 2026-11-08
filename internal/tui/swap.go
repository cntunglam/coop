package tui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/cntunglam/coop/internal/config"
	"github.com/cntunglam/coop/internal/script"
)

const swapPage = "swap"

const (
	labelPartName   = "part name"
	labelPresetName = "preset name"
)

// closeSwapDialog removes the dialog and returns focus to the script form.
func (u *UI) closeSwapDialog(f *scriptForm) {
	u.pages.RemovePage(swapPage)
	u.app.SetFocus(f.form)
}

// swapped runs MakeSwappable on the draft with its command replaced by the
// form's current command text.
func (u *UI) swapped(f *scriptForm, word, param, preset string) (config.Script, error) {
	s := cloneScript(f.draft)
	s.Command = u.readFormValues().body
	return script.MakeSwappable(s, word, param, preset)
}

// openSwapDialog lets the user pick a word of the command and turn it into a
// parameter. Nothing is saved until the script form's Save.
func (u *UI) openSwapDialog(f *scriptForm) {
	if f.oldName != "" {
		if refs := u.cfg.References(f.oldName); len(refs) > 0 {
			u.logActivity(fmt.Sprintf("[red]can't make swappable: %s is used by %s", f.oldName, strings.Join(refs, ", ")))
			return
		}
	}
	words := script.Words(u.readFormValues().body)
	if len(words) == 0 {
		u.logActivity("[red]nothing to make swappable: the command is empty")
		return
	}
	list := tview.NewList().ShowSecondaryText(false)
	list.SetBorder(true).SetTitle(" pick the part to make swappable (Esc cancels) ")
	for _, w := range words {
		w := w
		list.AddItem(w, "", 0, func() { u.openSwapForm(f, w) })
	}
	list.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEscape {
			u.closeSwapDialog(f)
			return nil
		}
		return ev
	})
	h := len(words) + 2
	if h > 20 {
		h = 20
	}
	u.showModal(swapPage, list, 70, h)
}

// openSwapForm is page 2: name the parameter (and first preset) for word.
func (u *UI) openSwapForm(f *scriptForm, word string) {
	needPreset := len(f.draft.Presets) == 0
	form := tview.NewForm()
	form.SetBorder(true).SetTitle(fmt.Sprintf(" make %q swappable (Esc cancels) ", word))
	preview := tview.NewTextView().SetWrap(true)

	read := func() (param, preset string) {
		param = strings.TrimSpace(form.GetFormItemByLabel(labelPartName).(*tview.InputField).GetText())
		if needPreset {
			preset = strings.TrimSpace(form.GetFormItemByLabel(labelPresetName).(*tview.InputField).GetText())
		}
		return
	}
	update := func(string) {
		param, preset := read()
		if s, err := u.swapped(f, word, param, preset); err == nil {
			preview.SetText(s.Command)
		} else {
			preview.SetText("")
		}
	}
	form.AddInputField(labelPartName, "", 0, nil, update)
	height := 2
	if needPreset {
		form.AddInputField(labelPresetName, "", 0, nil, update)
		height += 2
	}
	preview.SetBorder(true).SetTitle(" preview ")
	form.AddButton("Apply", func() {
		param, preset := read()
		if err := u.applySwap(f, word, param, preset); err != nil {
			u.logActivity("[red]" + err.Error())
		}
	})
	form.AddButton("Cancel", func() { u.closeSwapDialog(f) })
	form.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEscape {
			u.closeSwapDialog(f)
			return nil
		}
		return ev
	})
	u.pages.RemovePage(swapPage)
	content := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(form, height+3, 0, true). // + buttons row and border
		AddItem(preview, 4, 0, false)
	u.showModal(swapPage, content, 76, height+7)
}

// applySwap rewrites the draft (and the visible command field) so word becomes
// {{.param}}. Nothing is written to disk until the form's Save.
func (u *UI) applySwap(f *scriptForm, word, param, preset string) error {
	s, err := u.swapped(f, word, param, preset)
	if err != nil {
		return err
	}
	f.draft = s
	f.vals.body = s.Command
	if ta, ok := f.form.GetFormItemByLabel(labelCommand).(*tview.TextArea); ok {
		ta.SetText(s.Command, true)
	}
	u.pages.RemovePage(swapPage)
	u.app.SetFocus(f.form)
	u.logActivity(fmt.Sprintf("[green]command now uses {{.%s}} — Save to keep it", param))
	return nil
}
