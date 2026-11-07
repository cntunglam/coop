// Package catalog builds the flat list of runnable entries (scripts, workflows,
// groups) shared by the TUI and `coop find`, and searches it.
package catalog

import (
	"sort"
	"strings"

	"github.com/cntunglam/coop/internal/config"
)

// Kind classifies a catalog entry.
type Kind string

const (
	KindScript   Kind = "script"
	KindDaemon   Kind = "daemon"
	KindWorkflow Kind = "workflow"
	KindGroup    Kind = "group"
)

// Entry is one runnable thing in the config.
type Entry struct {
	Name, Description string
	Command           string // script command, or file path for file scripts; "" otherwise
	Kind              Kind
	Tags              []string
	Presets           []string // sorted preset names (scripts only)
}

// Entries lists scripts (sorted), then workflows (sorted), then groups (sorted).
func Entries(cfg *config.Config) []Entry {
	var out []Entry
	for _, name := range sortedKeys(cfg.Scripts) {
		s := cfg.Scripts[name]
		kind := KindScript
		if s.Background {
			kind = KindDaemon
		}
		cmd := s.Command
		if s.ResolvedType() == config.TypeFile {
			cmd = s.File
		}
		out = append(out, Entry{
			Name: name, Description: s.Description, Command: cmd,
			Kind: kind, Tags: s.Tags, Presets: s.PresetNames(),
		})
	}
	for _, name := range sortedKeys(cfg.Workflows) {
		out = append(out, Entry{Name: name, Description: cfg.Workflows[name].Description, Kind: KindWorkflow})
	}
	for _, name := range cfg.GroupNames() {
		out = append(out, Entry{Name: name, Description: cfg.Groups[name].Description, Kind: KindGroup})
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Search keeps entries where every whitespace-separated word of query is a
// case-insensitive substring of the name, a tag, the description, or the
// command. A word hitting the name or a tag scores 2, any other hit 1; results
// are stable-sorted by total score, descending. An empty query returns entries.
func Search(entries []Entry, query string) []Entry {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return entries
	}
	type scored struct {
		e     Entry
		score int
	}
	var hits []scored
	for _, e := range entries {
		high := []string{strings.ToLower(e.Name)}
		for _, t := range e.Tags {
			high = append(high, strings.ToLower(t))
		}
		low := []string{strings.ToLower(e.Description), strings.ToLower(e.Command)}
		total, ok := 0, true
		for _, w := range words {
			switch {
			case anyContains(high, w):
				total += 2
			case anyContains(low, w):
				total++
			default:
				ok = false
			}
			if !ok {
				break
			}
		}
		if ok {
			hits = append(hits, scored{e, total})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	out := make([]Entry, len(hits))
	for i, h := range hits {
		out[i] = h.e
	}
	return out
}

func anyContains(hay []string, w string) bool {
	for _, h := range hay {
		if strings.Contains(h, w) {
			return true
		}
	}
	return false
}

// ParseTags splits s on spaces and commas, dropping empties and duplicates
// (first occurrence wins).
func ParseTags(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}
