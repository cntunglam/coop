package tui

import (
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/cntunglam/coop/internal/catalog"
	"github.com/cntunglam/coop/internal/config"
)

type findResult struct {
	e   catalog.Entry
	ok  bool
	err error
}

func findCfg() *config.Config {
	return &config.Config{
		Scripts: map[string]config.Script{
			"orders-logs": {Tags: []string{"prod", "k8s", "orders"}, Command: "kubectl logs api"},
			"db-shell":    {Description: "psql into orders db", Command: "psql -d orders"},
			"hello":       {Command: "echo hello"},
		},
	}
}

// startFind runs the finder on a simulation screen and returns the screen and result channel.
func startFind(t *testing.T, cfg *config.Config, query string) (tcell.SimulationScreen, <-chan findResult) {
	t.Helper()
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	res := make(chan findResult, 1)
	go func() {
		e, ok, err := runFind(cfg, query, screen)
		res <- findResult{e, ok, err}
	}()
	time.Sleep(200 * time.Millisecond)
	return screen, res
}

func waitFind(t *testing.T, res <-chan findResult) findResult {
	t.Helper()
	select {
	case r := <-res:
		if r.err != nil {
			t.Fatal(r.err)
		}
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("finder did not return")
	}
	return findResult{}
}

func key(s tcell.SimulationScreen, k tcell.Key, r rune) {
	s.InjectKey(k, r, tcell.ModNone)
	time.Sleep(50 * time.Millisecond)
}

func TestFindFiltersAndPicks(t *testing.T) {
	cfg := findCfg()
	want := catalog.Search(catalog.Entries(cfg), "ord")[1]
	s, res := startFind(t, cfg, "")
	for _, r := range "ord" {
		key(s, tcell.KeyRune, r)
	}
	key(s, tcell.KeyDown, 0)
	key(s, tcell.KeyEnter, 0)
	r := waitFind(t, res)
	if !r.ok || r.e.Name != want.Name {
		t.Fatalf("got %+v ok=%v, want %s", r.e, r.ok, want.Name)
	}
}

func TestFindPrefilledQuery(t *testing.T) {
	s, res := startFind(t, findCfg(), "k8s")
	key(s, tcell.KeyEnter, 0)
	r := waitFind(t, res)
	if !r.ok || r.e.Name != "orders-logs" {
		t.Fatalf("got %+v ok=%v", r.e, r.ok)
	}
}

func TestFindEscCancels(t *testing.T) {
	s, res := startFind(t, findCfg(), "")
	key(s, tcell.KeyEscape, 0)
	if r := waitFind(t, res); r.ok {
		t.Fatalf("Esc returned ok: %+v", r.e)
	}
}

func TestFindSingleMatchStillShown(t *testing.T) {
	s, res := startFind(t, findCfg(), "hello")
	select {
	case <-res:
		t.Fatal("finder returned on its own with a single match")
	case <-time.After(300 * time.Millisecond):
	}
	key(s, tcell.KeyEnter, 0)
	if r := waitFind(t, res); !r.ok || r.e.Name != "hello" {
		t.Fatalf("got %+v ok=%v", r.e, r.ok)
	}
}

func TestFindNoMatchEnterIsNoop(t *testing.T) {
	s, res := startFind(t, findCfg(), "zzzz")
	key(s, tcell.KeyEnter, 0)
	select {
	case <-res:
		t.Fatal("Enter with no matches returned")
	case <-time.After(200 * time.Millisecond):
	}
	key(s, tcell.KeyEscape, 0)
	waitFind(t, res)
}

// TestFindEmptyTableNotSelectable guards the zero-selectable-cells freeze: with no
// matches the table must show the placeholder and have selection turned off.
func TestFindEmptyTableNotSelectable(t *testing.T) {
	f := newFinder(tview.NewApplication(), findCfg(), "zzzz")
	if got := f.table.GetCell(1, 0).Text; got != "(no matches)" {
		t.Fatalf("placeholder = %q", got)
	}
	if rowsSel, colsSel := f.table.GetSelectable(); rowsSel || colsSel {
		t.Fatal("empty table is selectable")
	}
	f.input.SetText("hello")
	if rowsSel, _ := f.table.GetSelectable(); !rowsSel {
		t.Fatal("table not selectable again after matches return")
	}
}
