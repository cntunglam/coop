package script

import (
	"regexp"
	"sort"

	"github.com/caongoctunglam/rx/internal/config"
)

// templateVar matches Go text/template field references like {{.port}} or
// {{ .instance }}, capturing the field name.
var templateVar = regexp.MustCompile(`{{\s*\.(\w+)\s*}}`)

// ParamNames returns the sorted set of parameter names a script uses: every
// {{.field}} referenced in its command and ready_port, unioned with the keys of
// any presets it already has. This drives the "add preset" form so the user sees
// exactly the fields the script needs.
func ParamNames(s config.Script) []string {
	set := map[string]struct{}{}
	for _, src := range []string{s.Command, s.ReadyPort} {
		for _, m := range templateVar.FindAllStringSubmatch(src, -1) {
			set[m[1]] = struct{}{}
		}
	}
	for _, preset := range s.Presets {
		for k := range preset {
			set[k] = struct{}{}
		}
	}
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
