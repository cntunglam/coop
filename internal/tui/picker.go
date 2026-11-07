package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/cntunglam/coop/internal/capture"
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
			setNoMatches(table)
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

	input.SetInputCapture(listKeys(app, table, func() int { return len(shown) }, func(i int) {
		f.picked, f.ok = shown[i], true
	}))

	f.input, f.table = input, table
	f.layout = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(input, 1, 0, true).
		AddItem(table, 0, 1, false)
	return f
}

// listKeys is the input capture shared by the pickers: the input keeps the
// typing while Esc cancels, ↑/↓ move the table selection (row 0 is the
// header), and Enter calls pick with the selected index among count() rows.
func listKeys(app *tview.Application, table *tview.Table, count func() int, pick func(i int)) func(*tcell.EventKey) *tcell.EventKey {
	return func(ev *tcell.EventKey) *tcell.EventKey {
		row, _ := table.GetSelection()
		n := count()
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
			if row < n {
				table.Select(row+1, 0)
			}
			return nil
		case tcell.KeyEnter:
			if n > 0 && row >= 1 && row <= n {
				pick(row - 1)
				app.Stop()
			}
			return nil
		}
		return ev
	}
}

// setNoMatches shows the empty-list placeholder. A table that is selectable
// but has no selectable cells freezes tview's input handler, so selection is
// turned off while empty.
func setNoMatches(table *tview.Table) {
	table.SetSelectable(false, false)
	table.SetCell(1, 0, tview.NewTableCell("(no matches)").SetSelectable(false).SetTextColor(tcell.ColorGray))
}

// PickRecent lets the user pick one of other tabs' last commands (rows,
// newest first) in a short-lived app. home shortens folders to ~. ok is
// false if the user cancelled.
func PickRecent(rows []capture.Recent, home string) (capture.Recent, bool, error) {
	return runPickRecent(rows, home, nil)
}

// runPickRecent is PickRecent with an injectable screen, for tests (nil = real terminal).
func runPickRecent(rows []capture.Recent, home string, screen tcell.Screen) (capture.Recent, bool, error) {
	app := tview.NewApplication()
	if screen != nil {
		app.SetScreen(screen)
	}
	p := newRecentPicker(app, rows, home)
	if err := app.SetRoot(p.layout, true).SetFocus(p.input).Run(); err != nil {
		return capture.Recent{}, false, err
	}
	return p.picked, p.ok, nil
}

// recentPicker holds the recent-command picker's widgets and outcome.
type recentPicker struct {
	layout *tview.Flex
	input  *tview.InputField
	table  *tview.Table
	picked capture.Recent
	ok     bool
}

func newRecentPicker(app *tview.Application, rows []capture.Recent, home string) *recentPicker {
	p := &recentPicker{}
	input := tview.NewInputField().SetLabel("recent › ")
	table := tview.NewTable().SetFixed(1, 0)
	var shown []capture.Recent

	refill := func(q string) {
		words := strings.Fields(strings.ToLower(q))
		shown = shown[:0]
		for _, r := range rows {
			if matchesAll(words, strings.ToLower(r.Command), strings.ToLower(capture.TildePath(r.Dir, home))) {
				shown = append(shown, r)
			}
		}
		table.Clear()
		for c, h := range []string{"COMMAND", "AGO", "FOLDER"} {
			table.SetCell(0, c, tview.NewTableCell(h).SetSelectable(false).SetTextColor(tcell.ColorYellow))
		}
		if len(shown) == 0 {
			setNoMatches(table)
			return
		}
		for i, r := range shown {
			line, _, multi := strings.Cut(r.Command, "\n")
			if multi {
				line += " …"
			}
			table.SetCell(i+1, 0, tview.NewTableCell(truncate(line, 60)).SetExpansion(1))
			table.SetCell(i+1, 1, tview.NewTableCell(ago(time.Since(r.When))))
			table.SetCell(i+1, 2, tview.NewTableCell(capture.TildePath(r.Dir, home)))
		}
		table.SetSelectable(true, false)
		table.Select(1, 0)
	}
	refill("")
	input.SetChangedFunc(refill)
	input.SetInputCapture(listKeys(app, table, func() int { return len(shown) }, func(i int) {
		p.picked, p.ok = shown[i], true
	}))

	p.input, p.table = input, table
	p.layout = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(input, 1, 0, true).
		AddItem(table, 0, 1, false)
	return p
}

// matchesAll reports whether every word occurs in at least one of hay.
func matchesAll(words []string, hay ...string) bool {
	for _, w := range words {
		found := false
		for _, h := range hay {
			if strings.Contains(h, w) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// ago formats d compactly: 12s, 5m, 2h, 3d.
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(int(d/time.Second), 0))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
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
