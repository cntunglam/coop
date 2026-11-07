package capture

import (
	"reflect"
	"testing"

	"github.com/cntunglam/coop/internal/config"
)

func TestParseAliases(t *testing.T) {
	in := "alias k=kubectl\nalias ll='ls -l'\nalias q='echo '\\''hi'\\'''"
	got := ParseAliases(in)
	want := map[string]string{"k": "kubectl", "ll": "ls -l", "q": "echo 'hi'"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestParseAliasesDashDashAndAnsiC(t *testing.T) {
	got := ParseAliases("alias -- -x='ls -a'\nalias n=$'a\\nb'\nalias ''\n")
	if got["-x"] != "ls -a" || got["n"] != "a\nb" {
		t.Fatalf("got %#v", got)
	}
}

func TestExpandAliases(t *testing.T) {
	a := map[string]string{"k": "kubectl", "ll": "ls -l", "ls": "ls -G", "kg": "k get"}
	cases := []struct{ in, want string }{
		{"k get pods -n payments", "kubectl get pods -n payments"},
		{"k get pods | k logs x && ll", "kubectl get pods | kubectl logs x && ls -G -l"},
		{"echo k", "echo k"},
		{"echo 'a; k'", "echo 'a; k'"},
		{`echo "a; k"`, `echo "a; k"`},
		{"ls", "ls -G"},
		{"FOO=1 k get", "FOO=1 kubectl get"},
		{"k get \\\n  pods", "kubectl get \\\n  pods"},
		{"kg pods", "kubectl get pods"},
		{"echo a\nk get", "echo a\nkubectl get"},
		{"'k' get", "'k' get"},
		{"echo hi 2>&1 | k get", "echo hi 2>&1 | kubectl get"},
		{"for p in a b; do k get $p; done", "for p in a b; do kubectl get $p; done"},
		{"if x; then k get; else k logs; fi", "if x; then kubectl get; else kubectl logs; fi"},
		{"if x; then y; elif k get; then z; fi", "if x; then y; elif kubectl get; then z; fi"},
		{"while k get; do :; done", "while kubectl get; do :; done"},
		{"{ k get; }", "{ kubectl get; }"},
		{"! k get", "! kubectl get"},
		{"(k get)", "(kubectl get)"},
		{"echo $(k get) k", "echo $(kubectl get) k"},
		{"echo `k get` k", "echo `kubectl get` k"},
		{"x=$(k get pods); k get", "x=$(kubectl get pods); kubectl get"},
		{"echo '$(k get)'", "echo '$(k get)'"},
		{"echo '`k get`'", "echo '`k get`'"},
		{"echo k; echo \"k\"", "echo k; echo \"k\""},
	}
	for _, c := range cases {
		if got := ExpandAliases(c.in, a); got != c.want {
			t.Errorf("ExpandAliases(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCommandWords(t *testing.T) {
	got := CommandWords("FOO=1 kubectl get | grep x && mycmd")
	want := []string{"kubectl", "grep", "mycmd"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestCommandWordsReservedAndSubshell(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"for x in a; do mk; done", []string{"for", "mk", "done"}},
		{"echo $(mk) `nk`", []string{"echo", "mk", "nk"}},
		{"(mk)", []string{"mk"}},
	}
	for _, c := range cases {
		if got := CommandWords(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("CommandWords(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestSuggestName(t *testing.T) {
	none := func(string) bool { return false }
	cases := []struct{ in, want string }{
		{"kubectl --context gke_prod_asia -n payments logs -f api-7f9c2", "kubectl-logs"},
		{"psql -d orders", "psql"},
		{"docker ps --format x", "docker-ps"},
		{"FOO=1 kubectl get pods", "kubectl-get"},
		{"/usr/local/bin/foo bar", "foo-bar"},
		{"ünï", "cmd"},
		{"", "cmd"},
		{"Make BUILD.all", "make-build-all"},
		{"echo a | wc -l", "echo-a"},
	}
	for _, c := range cases {
		got := SuggestName(c.in, none)
		if got != c.want {
			t.Errorf("SuggestName(%q) = %q, want %q", c.in, got, c.want)
		}
		if err := config.ValidName(got); err != nil {
			t.Errorf("SuggestName(%q) = %q invalid: %v", c.in, got, err)
		}
	}
	taken := func(n string) bool { return n == "kubectl-logs" || n == "kubectl-logs-2" }
	if got := SuggestName("kubectl logs x", taken); got != "kubectl-logs-3" {
		t.Errorf("got %q", got)
	}
	if got := SuggestName("kubectl logs x", func(n string) bool { return n == "kubectl-logs" }); got != "kubectl-logs-2" {
		t.Errorf("got %q", got)
	}
}

func TestTildePath(t *testing.T) {
	cases := []struct{ dir, home, want string }{
		{"/h/x/work", "/h/x", "~/work"},
		{"/h/x", "/h/x", "~"},
		{"/other/y", "/h/x", "/other/y"},
		{"/h/xy", "/h/x", "/h/xy"},
		{"/h/x/work", "", "/h/x/work"},
	}
	for _, c := range cases {
		if got := TildePath(c.dir, c.home); got != c.want {
			t.Errorf("TildePath(%q,%q) = %q, want %q", c.dir, c.home, got, c.want)
		}
	}
}
