package tui

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

const swapYAML = `
scripts:
  lit:
    command: "kubectl --context gke_prod_asia -n payments logs -f api-7f9c2"
    literal: true
  withp:
    command: "echo {{.x}} hi"
    presets:
      one: { x: "1" }
  fileone:
    type: file
    file: /bin/true
  used:
    command: "sleep 30"
    literal: true
workflows:
  flow:
    steps:
      - { script: used }
`

func ctrlP(u *UI) {
	u.app.QueueEvent(tcell.NewEventKey(tcell.KeyCtrlP, 0, tcell.ModNone))
}

func TestCtrlPOpensSwapDialog(t *testing.T) {
	u := testUI(t, swapYAML)
	do := startSim(t, u)
	do(func() { u.openScriptForm("lit") })
	ctrlP(u)
	waitFor(t, 3*time.Second, func() bool { return frontPage(do, u) == swapPage })
}

func TestSwapApplyThenSave(t *testing.T) {
	u := testUI(t, swapYAML)
	do := startSim(t, u)
	do(func() {
		u.openScriptForm("lit")
		if err := u.applySwap(u.sf, "gke_prod_asia", "context", "prod-asia"); err != nil {
			t.Fatal(err)
		}
		if got := u.sf.form.GetFormItemByLabel("command").(*tview.TextArea).GetText(); !strings.Contains(got, "{{.context}}") {
			t.Errorf("field = %q", got)
		}
		u.submitScriptForm()
	})
	s := diskConfig(t, u).Scripts["lit"]
	if s.Command != "kubectl --context {{.context}} -n payments logs -f api-7f9c2" || s.Literal ||
		!reflect.DeepEqual(s.Presets, map[string]map[string]string{"prod-asia": {"context": "gke_prod_asia"}}) {
		t.Errorf("disk = %+v", s)
	}
}

func TestSwapNotSavedUntilSave(t *testing.T) {
	u := testUI(t, swapYAML)
	do := startSim(t, u)
	do(func() {
		u.openScriptForm("lit")
		if err := u.applySwap(u.sf, "gke_prod_asia", "context", "p"); err != nil {
			t.Fatal(err)
		}
		pressButton(u, "Cancel")
	})
	s := diskConfig(t, u).Scripts["lit"]
	if strings.Contains(s.Command, "{{") || len(s.Presets) != 0 {
		t.Errorf("disk changed: %+v", s)
	}
}

func TestSwapRefusedWhenReferenced(t *testing.T) {
	u := testUI(t, swapYAML)
	do := startSim(t, u)
	do(func() {
		u.openScriptForm("used")
		u.openSwapDialog(u.sf)
	})
	if front := frontPage(do, u); front != scriptFormPage {
		t.Errorf("front page = %q, want %q", front, scriptFormPage)
	}
	if !strings.Contains(logText(do, u), "can't make swappable") {
		t.Errorf("log = %q", logText(do, u))
	}
}

func TestSwapNoPresetNameWhenPresetsExist(t *testing.T) {
	u := testUI(t, swapYAML)
	do := startSim(t, u)
	do(func() {
		u.openScriptForm("withp")
		u.openSwapForm(u.sf, "hi")
		page := u.pages.GetPage(swapPage)
		if page == nil {
			t.Fatal("no swap page")
		}
		form := findForm(page)
		if form == nil || form.GetFormItemByLabel(labelPartName) == nil {
			t.Fatal("part name missing")
		}
		if form.GetFormItemByLabel(labelPresetName) != nil {
			t.Error("preset name shown although presets exist")
		}
	})
	do(func() {
		u.closeSwapDialog(u.sf)
		u.openScriptForm("lit")
		u.openSwapForm(u.sf, "gke_prod_asia")
		if findForm(u.pages.GetPage(swapPage)).GetFormItemByLabel(labelPresetName) == nil {
			t.Error("preset name missing for script without presets")
		}
	})
}

// findForm digs the swap form out of the modal's flex layout.
func findForm(p tview.Primitive) *tview.Form {
	if f, ok := p.(*tview.Form); ok {
		return f
	}
	if fl, ok := p.(*tview.Flex); ok {
		for i := 0; i < fl.GetItemCount(); i++ {
			if f := findForm(fl.GetItem(i)); f != nil {
				return f
			}
		}
	}
	return nil
}

func TestSwapButtonOnlyForCommandScripts(t *testing.T) {
	u := testUI(t, swapYAML)
	do := startSim(t, u)
	do(func() {
		u.openScriptForm("lit")
		if u.sf.form.GetButtonIndex("Make swappable…") < 0 {
			t.Error("command script form lacks the button")
		}
		u.closeScriptForm()
		u.openScriptForm("fileone")
		if u.sf.form.GetButtonIndex("Make swappable…") >= 0 {
			t.Error("file script form has the button")
		}
	})
	ctrlP(u)
	time.Sleep(300 * time.Millisecond)
	if front := frontPage(do, u); front != scriptFormPage {
		t.Errorf("front page = %q after Ctrl-P on file script", front)
	}
}

func TestSwapDialogTypesLetters(t *testing.T) {
	u := testUI(t, swapYAML)
	do := startSim(t, u)
	do(func() {
		u.openScriptForm("lit")
		u.openSwapForm(u.sf, "gke_prod_asia")
	})
	for _, r := range "qe?" {
		u.app.QueueEvent(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	time.Sleep(300 * time.Millisecond)
	do(func() {
		got := findForm(u.pages.GetPage(swapPage)).GetFormItemByLabel(labelPartName).(*tview.InputField).GetText()
		if got != "qe?" {
			t.Errorf("part name = %q", got)
		}
	})
}
