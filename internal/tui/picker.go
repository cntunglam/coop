package tui

import (
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/cntunglam/coop/internal/catalog"
	"github.com/cntunglam/coop/internal/config"
)

// Find runs the interactive finder as its own short-lived app. ok is false if
// the user cancelled.
func Find(cfg *config.Config, query string) (catalog.Entry, bool, error) {
	return runFind(cfg, query, nil)
}

// runFind is Find with an injectable screen, for tests (nil = real terminal).
func runFind(cfg *config.Config, query string, screen tcell.Screen) (catalog.Entry, bool, error) {
	app := tview.NewApplication()
	if screen != nil {
		app.SetScreen(screen)
	}
	f := newFinder(app, cfg, query)
	if err := app.SetRoot(f.layout, true).SetFocus(f.input).Run(); err != nil {
		return catalog.Entry{}, false, err
	}
	return f.picked, f.ok, nil
}

// finder holds the finder's widgets and outcome.
type finder struct {
	layout *tview.Flex
	input  *tview.InputField
	table  *tview.Table
	picked catalog.Entry
	ok     bool
}

func newFinder(app *tview.Application, cfg *config.Config, query string) *finder {
	all := catalog.Entries(cfg)
	f := &finder{}
	input := tview.NewInputField().SetLabel("find › ").SetText(query)
	table := tview.NewTable().SetFixed(1, 0)
	var shown []catalog.Entry

	refill := func(q string) {
		shown = catalog.Search(all, q)
		table.Clear()
		for c, h := range []string{"NAME", "KIND", "TAGS", "COMMAND"} {
			table.SetCell(0, c, tview.NewTableCell(h).SetSelectable(false).SetTextColor(tcell.ColorYellow))
		}
		if len(shown) == 0 {
			// A table that is selectable but has no selectable cells freezes tview's
			// input handler, so turn selection off while empty.
			table.SetSelectable(false, false)
			table.SetCell(1, 0, tview.NewTableCell("(no matches)").SetSelectable(false).SetTextColor(tcell.ColorGray))
			return
		}
		for i, e := range shown {
			r := i + 1
			table.SetCell(r, 0, tview.NewTableCell(e.Name))
			table.SetCell(r, 1, tview.NewTableCell(string(e.Kind)))
			table.SetCell(r, 2, tview.NewTableCell(strings.Join(e.Tags, ",")))
			table.SetCell(r, 3, tview.NewTableCell(truncate(e.Command, 60)).SetExpansion(1))
		}
		table.SetSelectable(true, false)
		table.Select(1, 0)
	}
	refill(query)
	input.SetChangedFunc(refill)

	input.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		row, _ := table.GetSelection()
		switch ev.Key() {
		case tcell.KeyEscape:
			app.Stop()
			return nil
		case tcell.KeyUp:
			if row > 1 {
				table.Select(row-1, 0)
			}
			return nil
		case tcell.KeyDown:
			if row < len(shown) {
				table.Select(row+1, 0)
			}
			return nil
		case tcell.KeyEnter:
			if len(shown) > 0 && row >= 1 && row <= len(shown) {
				f.picked, f.ok = shown[row-1], true
				app.Stop()
			}
			return nil
		}
		return ev
	})

	f.input, f.table = input, table
	f.layout = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(input, 1, 0, true).
		AddItem(table, 0, 1, false)
	return f
}

// ChoosePreset runs a small list app to pick one of presets. ok is false on cancel.
func ChoosePreset(script string, presets []string) (string, bool, error) {
	app := tview.NewApplication()
	list := tview.NewList().ShowSecondaryText(false)
	list.SetBorder(true).SetTitle(" preset for " + script + " ")
	var chosen string
	ok := false
	for _, p := range presets {
		p := p
		list.AddItem(p, "", 0, func() {
			chosen, ok = p, true
			app.Stop()
		})
	}
	list.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEscape {
			app.Stop()
			return nil
		}
		return ev
	})
	if err := app.SetRoot(list, true).SetFocus(list).Run(); err != nil {
		return "", false, err
	}
	return chosen, ok, nil
}
