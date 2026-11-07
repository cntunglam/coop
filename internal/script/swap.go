package script

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/cntunglam/coop/internal/config"
)

var (
	paramNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	assignRe    = regexp.MustCompile(`^\w+=`)
	braceRe     = regexp.MustCompile(`\{\{|\}\}`)
)

// Words returns the pickable words of a shell command, in order and deduped.
// Whitespace splits words unless quoted; quotes are stripped. A `--flag=value`
// or `NAME=value` token yields both itself and the value. Words containing
// braces are omitted.
func Words(command string) []string {
	type token struct {
		text string
		lead bool // began with a quote: a whole quoted string is one plain word
	}
	var tokens []token
	var cur strings.Builder
	has, lead := false, false
	var quote rune
	flush := func() {
		if has {
			tokens = append(tokens, token{cur.String(), lead})
		}
		cur.Reset()
		has, lead = false, false
	}
	for _, r := range command {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			if !has {
				lead = true
			}
			quote = r
			has = true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			flush()
		default:
			cur.WriteRune(r)
			has = true
		}
	}
	flush()

	seen := map[string]bool{}
	var out []string
	add := func(w string) {
		if w == "" || strings.ContainsAny(w, "{}") || seen[w] {
			return
		}
		seen[w] = true
		out = append(out, w)
	}
	for _, t := range tokens {
		add(t.text)
		if !t.lead && (strings.HasPrefix(t.text, "-") || assignRe.MatchString(t.text)) {
			if i := strings.Index(t.text, "="); i >= 0 {
				add(t.text[i+1:])
			}
		}
	}
	return out
}

// MakeSwappable returns a copy of s in which every whole-word occurrence of
// word in the command is replaced by {{.param}}, with param recorded in the
// presets. If s has no presets, a new preset named preset is created holding
// the original word; otherwise param is added to every existing preset.
func MakeSwappable(s config.Script, word, param, preset string) (config.Script, error) {
	if !paramNameRe.MatchString(param) {
		return s, fmt.Errorf("invalid parameter name %q", param)
	}
	for _, p := range ParamNames(s) {
		if p == param {
			return s, fmt.Errorf("parameter %q already exists", param)
		}
	}
	if word == "" || strings.ContainsAny(word, "{}") {
		return s, fmt.Errorf("invalid word %q", word)
	}
	if len(s.Presets) == 0 && preset == "" {
		return s, fmt.Errorf("a preset name is required")
	}
	if s.ResolvedType() == config.TypeFile {
		return s, fmt.Errorf("file scripts cannot be made swappable")
	}

	cmd := s.Command
	if s.Literal {
		cmd = braceRe.ReplaceAllStringFunc(cmd, func(m string) string {
			return `{{"` + m + `"}}`
		})
	}
	out, n := replaceWholeWord(cmd, word, "{{."+param+"}}")
	if n == 0 {
		return s, fmt.Errorf("%q not found as a whole word in the command", word)
	}

	res := s
	res.Command = out
	res.Literal = false
	if len(s.Presets) == 0 {
		res.Presets = map[string]map[string]string{preset: {param: word}}
	} else {
		res.Presets = make(map[string]map[string]string, len(s.Presets))
		for name, vals := range s.Presets {
			cp := make(map[string]string, len(vals)+1)
			for k, v := range vals {
				cp[k] = v
			}
			cp[param] = word
			res.Presets[name] = cp
		}
	}
	return res, nil
}

const wordBoundary = " \t\r\n=;|&()'\",/:@"

func isBoundary(s string, i int, atEnd bool) bool {
	if atEnd {
		return i >= len(s) || strings.IndexByte(wordBoundary, s[i]) >= 0
	}
	return i < 0 || strings.IndexByte(wordBoundary, s[i]) >= 0
}

// replaceWholeWord replaces whole-word occurrences of word in s and reports
// how many were replaced.
func replaceWholeWord(s, word, repl string) (string, int) {
	var b strings.Builder
	n, pos := 0, 0
	for {
		i := strings.Index(s[pos:], word)
		if i < 0 {
			break
		}
		i += pos
		end := i + len(word)
		if isBoundary(s, i-1, false) && isBoundary(s, end, true) {
			b.WriteString(s[pos:i])
			b.WriteString(repl)
			n++
			pos = end
		} else {
			b.WriteString(s[pos : i+1])
			pos = i + 1
		}
	}
	b.WriteString(s[pos:])
	return b.String(), n
}
