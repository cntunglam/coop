package tui

import (
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/cntunglam/coop/internal/capture"
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

type recentResult struct {
	r   capture.Recent
	ok  bool
	err error
}

func recentRows() []capture.Recent {
	now := time.Now()
	return []capture.Recent{
		{PID: 1, When: now.Add(-12 * time.Second), Dir: "/home/u/api", Command: "kubectl logs api"},
		{PID: 2, When: now.Add(-5 * time.Minute), Dir: "/srv/db", Command: "psql -d orders \\\n  -c 'select 1'"},
		{PID: 3, When: now.Add(-2 * time.Hour), Dir: "/home/u/web", Command: "npm run dev"},
	}
}

func startRecent(t *testing.T, rows []capture.Recent) (tcell.SimulationScreen, <-chan recentResult) {
	t.Helper()
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	res := make(chan recentResult, 1)
	go func() {
		r, ok, err := runPickRecent(rows, "/home/u", screen)
		res <- recentResult{r, ok, err}
	}()
	time.Sleep(200 * time.Millisecond)
	return screen, res
}

func waitRecent(t *testing.T, res <-chan recentResult) recentResult {
	t.Helper()
	select {
	case r := <-res:
		if r.err != nil {
			t.Fatal(r.err)
		}
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("picker did not return")
	}
	return recentResult{}
}

func TestPickRecentRows(t *testing.T) {
	p := newRecentPicker(tview.NewApplication(), recentRows(), "/home/u")
	for c, want := range []string{"kubectl logs api", "12s", "~/api"} {
		if got := p.table.GetCell(1, c).Text; got != want {
			t.Errorf("row 1 col %d = %q, want %q", c, got, want)
		}
	}
	for c, want := range []string{"psql -d orders \\ …", "5m", "/srv/db"} {
		if got := p.table.GetCell(2, c).Text; got != want {
			t.Errorf("row 2 col %d = %q, want %q", c, got, want)
		}
	}
	if got := p.table.GetCell(3, 1).Text; got != "2h" {
		t.Errorf("row 3 age = %q", got)
	}
}

// TestPickRecentFiltersAndPicks: the filter matches the shown folder text
// ("~/" hits ~/api and ~/web, not /srv/db); ↓ stops at the last match.
func TestPickRecentFiltersAndPicks(t *testing.T) {
	s, res := startRecent(t, recentRows())
	for _, r := range "~/" {
		key(s, tcell.KeyRune, r)
	}
	key(s, tcell.KeyDown, 0)
	key(s, tcell.KeyDown, 0) // already last: stays
	key(s, tcell.KeyUp, 0)
	key(s, tcell.KeyDown, 0)
	key(s, tcell.KeyEnter, 0)
	r := waitRecent(t, res)
	if !r.ok || r.r.PID != 3 {
		t.Fatalf("got %+v ok=%v, want PID 3", r.r, r.ok)
	}
}

func TestPickRecentUpPicks(t *testing.T) {
	s, res := startRecent(t, recentRows())
	key(s, tcell.KeyDown, 0)
	key(s, tcell.KeyDown, 0)
	key(s, tcell.KeyUp, 0)
	key(s, tcell.KeyEnter, 0)
	if r := waitRecent(t, res); !r.ok || r.r.PID != 2 {
		t.Fatalf("got %+v ok=%v, want PID 2", r.r, r.ok)
	}
}

func TestPickRecentMatchesCommandWords(t *testing.T) {
	p := newRecentPicker(tview.NewApplication(), recentRows(), "/home/u")
	p.input.SetText("SELECT orders")
	if got := p.table.GetCell(1, 2).Text; got != "/srv/db" || p.table.GetRowCount() != 2 {
		t.Fatalf("filtered to %d rows, first folder %q", p.table.GetRowCount()-1, got)
	}
}

func TestPickRecentEscCancels(t *testing.T) {
	s, res := startRecent(t, recentRows())
	key(s, tcell.KeyEscape, 0)
	if r := waitRecent(t, res); r.ok {
		t.Fatalf("Esc returned ok: %+v", r.r)
	}
}

func TestPickRecentSingleRowStillShown(t *testing.T) {
	s, res := startRecent(t, recentRows()[:1])
	select {
	case <-res:
		t.Fatal("picker returned on its own with a single row")
	case <-time.After(300 * time.Millisecond):
	}
	key(s, tcell.KeyEnter, 0)
	if r := waitRecent(t, res); !r.ok || r.r.PID != 1 {
		t.Fatalf("got %+v ok=%v", r.r, r.ok)
	}
}

// TestPickRecentEmptyTableNotSelectable guards the zero-selectable-cells freeze.
func TestPickRecentEmptyTableNotSelectable(t *testing.T) {
	p := newRecentPicker(tview.NewApplication(), recentRows(), "/home/u")
	p.input.SetText("zzzz")
	if got := p.table.GetCell(1, 0).Text; got != "(no matches)" {
		t.Fatalf("placeholder = %q", got)
	}
	if rowsSel, colsSel := p.table.GetSelectable(); rowsSel || colsSel {
		t.Fatal("empty table is selectable")
	}
	p.input.SetText("npm")
	if rowsSel, _ := p.table.GetSelectable(); !rowsSel {
		t.Fatal("table not selectable again after matches return")
	}
}
