// Package capture supports `coop save`: it interprets the shell command the
// user just ran (alias expansion, command words, a suggested script name).
package capture

import (
	"path"
	"strconv"
	"strings"
)

// tok is one shell word in a command line: cmd[start:end] verbatim.
type tok struct {
	start, end int
	cmdPos     bool // word is in command position (not an env assignment)
	assign     bool // word is a leading NAME=value
	seg        int  // index of the simple command (split at | || && ; & newline)
}

// reserved lists words after which the next word is still in command position.
var reserved = map[string]bool{
	"do": true, "then": true, "else": true, "elif": true,
	"{": true, "!": true, "if": true, "while": true, "until": true,
}

func isBlank(c byte) bool { return c == ' ' || c == '\t' }

func isSep(c byte) bool { return c == '|' || c == '&' || c == ';' || c == '\n' }

func isAssign(w string) bool {
	eq := strings.IndexByte(w, '=')
	if eq <= 0 {
		return false
	}
	for i := 0; i < eq; i++ {
		c := w[i]
		if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return true
}

// scan splits cmd into words, tracking quotes and backslashes so separators
// inside quotes do not count, and marks which words sit in command position.
func scan(cmd string) []tok {
	var toks []tok
	seg := 0
	expect := true
	inTick := false
	n := len(cmd)
	for i := 0; i < n; {
		c := cmd[i]
		switch {
		case isBlank(c):
			i++
			continue
		case c == '\\' && i+1 < n && cmd[i+1] == '\n':
			i += 2 // line continuation: not a separator
			continue
		case isSep(c) && !(c == '&' && i+1 < n && cmd[i+1] == '>'):
			expect = true
			seg++
			i++
			continue
		case c == '$' && i+1 < n && cmd[i+1] == '(':
			expect = true // $( starts a new command
			seg++
			i += 2
			continue
		case c == '(':
			expect = true
			seg++
			i++
			continue
		case c == ')':
			expect = false
			seg++
			i++
			continue
		case c == '`':
			inTick = !inTick
			expect = inTick // opening starts a command; closing ends it
			seg++
			i++
			continue
		}
		start := i
		for i < n {
			c = cmd[i]
			if isBlank(c) || c == '\n' || c == ';' || c == '|' || c == '(' || c == ')' || c == '`' ||
				(c == '$' && i+1 < n && cmd[i+1] == '(') {
				break
			}
			if c == '&' && !(i > start && (cmd[i-1] == '>' || cmd[i-1] == '<')) && !(i == start && i+1 < n && cmd[i+1] == '>') {
				break
			}
			switch c {
			case '\\':
				i += 2
			case '\'':
				i++
				for i < n && cmd[i] != '\'' {
					i++
				}
				i++
			case '"':
				i++
				for i < n && cmd[i] != '"' {
					if cmd[i] == '\\' {
						i++
					}
					i++
				}
				i++
			default:
				i++
			}
		}
		if i > n {
			i = n
		}
		t := tok{start: start, end: i, seg: seg}
		if expect {
			if w := cmd[start:i]; isAssign(w) {
				t.assign = true
			} else if reserved[w] {
				// keywords that introduce a command: the next word is in command position
			} else {
				t.cmdPos = true
				expect = false
			}
		}
		toks = append(toks, t)
	}
	return toks
}

func plainWord(w string) bool { return w != "" && !strings.ContainsAny(w, `'"\`) }

// CommandWords returns the words in command position, in order.
func CommandWords(cmd string) []string {
	var out []string
	for _, t := range scan(cmd) {
		if t.cmdPos {
			out = append(out, cmd[t.start:t.end])
		}
	}
	return out
}

// ExpandAliases replaces each aliased word in command position with its
// definition, leaving every other byte of cmd untouched.
func ExpandAliases(cmd string, aliases map[string]string) string {
	if len(aliases) == 0 {
		return cmd
	}
	toks := scan(cmd)
	for i := len(toks) - 1; i >= 0; i-- {
		t := toks[i]
		if !t.cmdPos {
			continue
		}
		w := cmd[t.start:t.end]
		if !plainWord(w) {
			continue
		}
		if exp := expandWord(w, aliases, map[string]bool{}); exp != w {
			cmd = cmd[:t.start] + exp + cmd[t.end:]
		}
	}
	return cmd
}

// expandWord expands name through aliases, recursing on the replacement's
// first word but never re-expanding a name already expanded in this chain.
func expandWord(name string, aliases map[string]string, seen map[string]bool) string {
	repl, ok := aliases[name]
	if !ok || seen[name] {
		return name
	}
	seen[name] = true
	for _, t := range scan(repl) {
		if !t.cmdPos {
			continue
		}
		w := repl[t.start:t.end]
		if plainWord(w) {
			repl = repl[:t.start] + expandWord(w, aliases, seen) + repl[t.end:]
		}
		break
	}
	return repl
}

// SuggestName proposes a valid script name for cmd, avoiding names for which
// taken returns true.
func SuggestName(cmd string, taken func(string) bool) string {
	toks := scan(cmd)
	var parts []string
	for i, t := range toks {
		if !t.cmdPos {
			continue
		}
		parts = append(parts, path.Base(cmd[t.start:t.end]))
		for j := i + 1; j < len(toks) && toks[j].seg == t.seg; j++ {
			w := cmd[toks[j].start:toks[j].end]
			if strings.HasPrefix(w, "-") || isAssign(w) {
				continue
			}
			if prev := cmd[toks[j-1].start:toks[j-1].end]; j-1 > i && strings.HasPrefix(prev, "-") && !strings.Contains(prev, "=") {
				continue // value of the preceding flag
			}
			parts = append(parts, w)
			break
		}
		break
	}
	// A word with non-ASCII letters cannot be named faithfully; drop it.
	kept := parts[:0]
	for _, p := range parts {
		if isASCII(p) {
			kept = append(kept, p)
		}
	}
	base := sanitize(strings.Join(kept, "-"))
	if base == "" {
		base = "cmd"
	}
	name := base
	for n := 2; taken != nil && taken(name); n++ {
		name = base + "-" + strconv.Itoa(n)
	}
	return name
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func sanitize(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-_")
}

// TildePath shortens dir to start with ~ when it is home or under home.
func TildePath(dir, home string) string {
	if home == "" {
		return dir
	}
	if dir == home {
		return "~"
	}
	if strings.HasPrefix(dir, strings.TrimSuffix(home, "/")+"/") {
		return "~" + dir[len(strings.TrimSuffix(home, "/")):]
	}
	return dir
}

// ParseAliases parses `alias -rL` output into name -> definition.
func ParseAliases(aliasL string) map[string]string {
	out := map[string]string{}
	s := aliasL
	i := 0
	skipWS := func() {
		for i < len(s) && (isBlank(s[i]) || s[i] == '\n' || s[i] == '\r') {
			i++
		}
	}
	for {
		skipWS()
		if i >= len(s) {
			return out
		}
		word, next := readWord(s, i)
		i = next
		if word != "alias" {
			// not an alias statement: discard the rest of the line
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		}
		for {
			for i < len(s) && isBlank(s[i]) {
				i++
			}
			if i >= len(s) || s[i] == '\n' {
				break
			}
			w, next := readWord(s, i)
			i = next
			if strings.HasPrefix(w, "-") && !strings.Contains(w, "=") {
				continue // -g, -s, --
			}
			if eq := strings.IndexByte(w, '='); eq > 0 {
				out[w[:eq]] = w[eq+1:]
			}
		}
	}
}

// readWord reads one shell word from s at i, removing quoting. It returns the
// unquoted text and the index just past the word.
func readWord(s string, i int) (string, int) {
	var b strings.Builder
	n := len(s)
	for i < n {
		c := s[i]
		if isBlank(c) || c == '\n' {
			break
		}
		switch {
		case c == '\\' && i+1 < n:
			b.WriteByte(s[i+1])
			i += 2
		case c == '\'':
			i++
			for i < n && s[i] != '\'' {
				b.WriteByte(s[i])
				i++
			}
			i++
		case c == '$' && i+1 < n && s[i+1] == '\'':
			i += 2
			for i < n && s[i] != '\'' {
				if s[i] == '\\' && i+1 < n {
					i++
					switch s[i] {
					case 'n':
						b.WriteByte('\n')
					case 't':
						b.WriteByte('\t')
					default:
						b.WriteByte(s[i])
					}
				} else {
					b.WriteByte(s[i])
				}
				i++
			}
			i++
		case c == '"':
			i++
			for i < n && s[i] != '"' {
				if s[i] == '\\' && i+1 < n && strings.IndexByte("\\\"$`", s[i+1]) >= 0 {
					i++
				}
				b.WriteByte(s[i])
				i++
			}
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	if i > n {
		i = n
	}
	return b.String(), i
}
