package script

import (
	"reflect"
	"testing"

	"github.com/cntunglam/coop/internal/config"
)

func TestWords(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"kubectl --context gke_prod_asia -n payments logs -f api-7f9c2",
			[]string{"kubectl", "--context", "gke_prod_asia", "-n", "payments", "logs", "-f", "api-7f9c2"}},
		{"kubectl --context=gke_prod_asia get pods",
			[]string{"kubectl", "--context=gke_prod_asia", "gke_prod_asia", "get", "pods"}},
		{"docker ps --format '{{.Names}}'", []string{"docker", "ps", "--format"}},
		{`psql "host=db1 dbname=orders"`, []string{"psql", "host=db1 dbname=orders"}},
		{"FOO=bar run bar", []string{"FOO=bar", "bar", "run"}},
	}
	for _, c := range cases {
		if got := Words(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Words(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMakeSwappableLiteral(t *testing.T) {
	orig := "docker --context prod-eu ps --format '{{.Names}}'"
	s := config.Script{Literal: true, Command: orig}
	got, err := MakeSwappable(s, "prod-eu", "context", "eu")
	if err != nil {
		t.Fatal(err)
	}
	if got.Literal {
		t.Error("Literal should be false")
	}
	want := `docker --context {{.context}} ps --format '{{"{{"}}.Names{{"}}"}}'`
	if got.Command != want {
		t.Errorf("Command = %q, want %q", got.Command, want)
	}
	if !reflect.DeepEqual(got.Presets, map[string]map[string]string{"eu": {"context": "prod-eu"}}) {
		t.Errorf("Presets = %v", got.Presets)
	}
	spec, err := Resolve("x", got, "eu")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Argv[2] != orig {
		t.Errorf("resolved %q, want %q", spec.Argv[2], orig)
	}
	if s.Presets != nil {
		t.Error("input mutated")
	}
}

func TestMakeSwappableSpecExample(t *testing.T) {
	s := config.Script{Literal: true, Command: "kubectl --context gke_prod_asia -n payments logs -f api-7f9c2"}
	got, err := MakeSwappable(s, "gke_prod_asia", "context", "prod-asia")
	if err != nil {
		t.Fatal(err)
	}
	if got.Command != "kubectl --context {{.context}} -n payments logs -f api-7f9c2" {
		t.Errorf("Command = %q", got.Command)
	}
	if !reflect.DeepEqual(got.Presets, map[string]map[string]string{"prod-asia": {"context": "gke_prod_asia"}}) {
		t.Errorf("Presets = %v", got.Presets)
	}
}

func TestMakeSwappableExistingPresets(t *testing.T) {
	s := config.Script{Command: "psql -h {{.host}} -d orders",
		Presets: map[string]map[string]string{"a": {"host": "h1"}, "b": {"host": "h2"}}}
	got, err := MakeSwappable(s, "orders", "db", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Command != "psql -h {{.host}} -d {{.db}}" {
		t.Errorf("Command = %q", got.Command)
	}
	if len(got.Presets) != 2 || got.Presets["a"]["db"] != "orders" || got.Presets["b"]["db"] != "orders" {
		t.Errorf("Presets = %v", got.Presets)
	}
	if _, ok := s.Presets["a"]["db"]; ok {
		t.Error("input presets mutated")
	}
}

func TestMakeSwappableWholeWord(t *testing.T) {
	s := config.Script{Literal: true, Command: "kubectl logs api && kubectl logs payments-api -c api && kubectl logs pod/api"}
	got, err := MakeSwappable(s, "api", "pod", "p")
	if err != nil {
		t.Fatal(err)
	}
	want := "kubectl logs {{.pod}} && kubectl logs payments-api -c {{.pod}} && kubectl logs pod/{{.pod}}"
	if got.Command != want {
		t.Errorf("Command = %q, want %q", got.Command, want)
	}
}

func TestMakeSwappableErrors(t *testing.T) {
	lit := config.Script{Literal: true, Command: "echo hi {x}"}
	tmpl := config.Script{Command: "echo {{.host}} a"}
	tmplP := config.Script{Command: "echo {{.host}} a", Presets: map[string]map[string]string{"p": {"host": "h"}}}
	cases := []struct {
		name                string
		s                   config.Script
		word, param, preset string
	}{
		{"bad param", lit, "hi", "1x", "p"},
		{"existing param", tmplP, "a", "host", ""},
		{"absent word", lit, "zzz", "p1", "p"},
		{"no preset", lit, "hi", "p1", ""},
		{"brace word", lit, "{x}", "p1", "p"},
		{"empty word", lit, "", "p1", "p"},
		{"file type", config.Script{Type: config.TypeFile, File: "/bin/x"}, "x", "p1", "p"},
	}
	_ = tmpl
	for _, c := range cases {
		if _, err := MakeSwappable(c.s, c.word, c.param, c.preset); err == nil {
			t.Errorf("%s: expected error", c.name)
		}
	}
}
