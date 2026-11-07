package catalog

import (
	"reflect"
	"testing"

	"github.com/cntunglam/coop/internal/config"
)

func fixture() *config.Config {
	return &config.Config{
		Scripts: map[string]config.Script{
			"orders-logs": {Tags: []string{"prod", "k8s", "orders"}, Command: "kubectl --context gke_prod_asia logs api"},
			"db-shell":    {Description: "psql into orders db", Command: "psql -d orders"},
			"a.b":         {Command: "echo dot"},
			"axb":         {Command: "echo x"},
			"srv":         {Command: "sleep 100", Background: true},
		},
		Workflows: map[string]config.Workflow{
			"flow": {Steps: []config.Step{{Script: "a.b"}}},
		},
		Groups: map[string]config.Group{
			"g": {Members: []config.Step{{Script: "srv"}}},
		},
	}
}

func names(es []Entry) []string {
	out := []string{}
	for _, e := range es {
		out = append(out, e.Name)
	}
	return out
}

func search(q string) []string { return names(Search(Entries(fixture()), q)) }

func TestEntriesOrderAndKinds(t *testing.T) {
	es := Entries(fixture())
	if got, want := names(es), []string{"a.b", "axb", "db-shell", "orders-logs", "srv", "flow", "g"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	kinds := []Kind{KindScript, KindScript, KindScript, KindScript, KindDaemon, KindWorkflow, KindGroup}
	for i, e := range es {
		if e.Kind != kinds[i] {
			t.Errorf("%s kind = %s, want %s", e.Name, e.Kind, kinds[i])
		}
	}
	if es[3].Command == "" || len(es[3].Tags) != 3 {
		t.Errorf("orders-logs entry missing command/tags: %+v", es[3])
	}
}

func TestSearchByTagAndCommand(t *testing.T) {
	if got := search("gke_prod_asia"); !reflect.DeepEqual(got, []string{"orders-logs"}) {
		t.Errorf("command search = %v", got)
	}
	if got := search("k8s"); !reflect.DeepEqual(got, []string{"orders-logs"}) {
		t.Errorf("tag search = %v", got)
	}
}

func TestSearchAND(t *testing.T) {
	if got := search("orders psql"); !reflect.DeepEqual(got, []string{"db-shell"}) {
		t.Errorf("AND search = %v", got)
	}
}

func TestSearchRank(t *testing.T) {
	if got := search("orders"); !reflect.DeepEqual(got, []string{"orders-logs", "db-shell"}) {
		t.Errorf("rank = %v", got)
	}
}

func TestSearchLiteralAndCase(t *testing.T) {
	if got := search("ORDERS"); len(got) != 2 {
		t.Errorf("case = %v", got)
	}
	if got := search("a.b"); !reflect.DeepEqual(got, []string{"a.b"}) {
		t.Errorf("literal = %v", got)
	}
}

func TestParseTags(t *testing.T) {
	if got, want := ParseTags("prod, k8s  payments,,prod"), []string{"prod", "k8s", "payments"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := ParseTags(""); len(got) != 0 {
		t.Errorf("empty = %v", got)
	}
}
