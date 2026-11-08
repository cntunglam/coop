// Package script resolves a configured script plus a preset into a concrete,
// runnable execution spec: an argv and the environment to run it under.
package script

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/cntunglam/coop/internal/config"
)

// Spec is a fully-resolved command ready to be exec'd.
type Spec struct {
	Name       string   // script name
	Preset     string   // preset name ("" if none)
	Group      string   // group/namespace this daemon belongs to ("" if none)
	Argv       []string // command + args
	Env        []string // full environment (os.Environ() + preset vars)
	Background bool
	ReadyPort  int         // 0 if none
	ReadyWait  int         // seconds; 0 if none
	Display    string      // human-readable command for logs/meta
	Dir        string      // absolute working directory; "" = inherit
	Health     *HealthSpec // ongoing liveness probe; nil if none
}

// HealthSpec is a fully-resolved liveness probe descriptor.
type HealthSpec struct {
	Kind          string   // "tcp" | "command" | "http"
	Target        string   // resolved port / command / url
	Env           []string // used by the command probe
	Interval      int
	Timeout       int
	Failures      int
	MaxRestarts   int
	RestartWindow int
}

const (
	defHealthInterval      = 10
	defHealthTimeout       = 3
	defHealthFailures      = 3
	defHealthMaxRestarts   = 5
	defHealthRestartWindow = 60
)

// Resolve turns (script, preset) into a Spec. presetName may be "" only when the
// script has no presets; otherwise it must name an existing preset.
func Resolve(name string, s config.Script, presetName string) (*Spec, error) {
	chosen, values, err := selectPreset(name, s, presetName)
	if err != nil {
		return nil, err
	}

	spec := &Spec{
		Name:       name,
		Preset:     chosen,
		Env:        buildEnv(values),
		Background: s.Background,
		ReadyWait:  s.ReadyWait,
	}

	if s.Dir != "" {
		dir := expandHome(s.Dir)
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			return nil, fmt.Errorf("script %q: folder %q does not exist", name, s.Dir)
		}
		spec.Dir = dir
		spec.Env = setEnv(spec.Env, "PWD", dir)
	}

	switch s.ResolvedType() {
	case config.TypeCommand:
		if s.Literal {
			spec.Argv = []string{"sh", "-c", s.Command}
			spec.Display = s.Command
			break
		}
		rendered, err := renderTemplate("command", s.Command, values)
		if err != nil {
			return nil, err
		}
		spec.Argv = []string{"sh", "-c", rendered}
		spec.Display = rendered
	case config.TypeFile:
		path := expandHome(s.File)
		spec.Argv = []string{path}
		spec.Display = path
	default:
		return nil, fmt.Errorf("script %q: unsupported type", name)
	}

	if s.ReadyPort != "" {
		portStr, err := renderTemplate("ready_port", s.ReadyPort, values)
		if err != nil {
			return nil, err
		}
		port, err := strconv.Atoi(strings.TrimSpace(portStr))
		if err != nil {
			return nil, fmt.Errorf("script %q: ready_port %q is not a number: %w", name, portStr, err)
		}
		spec.ReadyPort = port
	}

	if s.Health != nil {
		hs, err := resolveHealth(s.Health, values, spec.Env)
		if err != nil {
			return nil, fmt.Errorf("script %q: %w", name, err)
		}
		spec.Health = hs
	}

	return spec, nil
}

// selectPreset returns the chosen preset's name and parameter values, with
// helpful errors. An empty presetName selects the script's only preset when it
// has exactly one.
func selectPreset(name string, s config.Script, presetName string) (string, map[string]string, error) {
	if presetName == "" {
		switch len(s.Presets) {
		case 0:
			return "", map[string]string{}, nil
		case 1:
			for n, v := range s.Presets {
				return n, v, nil
			}
		}
		return "", nil, fmt.Errorf("script %q requires a preset; choose one of: %s",
			name, strings.Join(s.PresetNames(), ", "))
	}
	values, ok := s.Presets[presetName]
	if !ok {
		return "", nil, fmt.Errorf("script %q has no preset %q; choose one of: %s",
			name, presetName, strings.Join(s.PresetNames(), ", "))
	}
	return presetName, values, nil
}

// buildEnv appends preset values to the current environment, exporting each key
// both as-is and upper-cased so scripts can read $port or $PORT.
func buildEnv(values map[string]string) []string {
	env := os.Environ()
	// Deterministic order for reproducibility/testability.
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := values[k]
		env = append(env, k+"="+v)
		up := strings.ToUpper(k)
		if up != k {
			env = append(env, up+"="+v)
		}
	}
	return env
}

// renderTemplate executes a text/template with strict missing-key errors so a
// typo'd {{.foo}} fails loudly instead of substituting "<no value>".
func renderTemplate(what, tmpl string, values map[string]string) (string, error) {
	t, err := template.New(what).Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("%s template parse: %w", what, err)
	}
	var sb strings.Builder
	if err := t.Execute(&sb, values); err != nil {
		return "", fmt.Errorf("%s template render: %w", what, err)
	}
	return sb.String(), nil
}

// expandHome expands a leading ~ to the user's home directory.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	return p
}

// resolveHealth turns a config.Health into a HealthSpec: it picks the probe
// kind, templates the target, carries the daemon's env for the command probe,
// and fills timing/policy defaults for zero-valued fields.
func resolveHealth(h *config.Health, values map[string]string, env []string) (*HealthSpec, error) {
	var kind, raw string
	switch {
	case h.TCP != "":
		kind, raw = "tcp", h.TCP
	case h.Command != "":
		kind, raw = "command", h.Command
	case h.HTTP != "":
		kind, raw = "http", h.HTTP
	default:
		return nil, fmt.Errorf("health has no probe") // unreachable: config validated
	}
	target, err := renderTemplate("health", raw, values)
	if err != nil {
		return nil, err
	}
	hs := &HealthSpec{
		Kind:          kind,
		Target:        strings.TrimSpace(target),
		Env:           env,
		Interval:      orDefault(h.Interval, defHealthInterval),
		Timeout:       orDefault(h.Timeout, defHealthTimeout),
		Failures:      orDefault(h.Failures, defHealthFailures),
		RestartWindow: orDefault(h.RestartWindow, defHealthRestartWindow),
	}
	// max_restarts: 0 (unset) -> default; -1 (unlimited) and positive kept as-is.
	if h.MaxRestarts == 0 {
		hs.MaxRestarts = defHealthMaxRestarts
	} else {
		hs.MaxRestarts = h.MaxRestarts
	}
	return hs, nil
}

func orDefault(v, d int) int {
	if v == 0 {
		return d
	}
	return v
}

// setEnv sets key=val in env, replacing any existing entries for key.
func setEnv(env []string, key, val string) []string {
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if !strings.HasPrefix(e, key+"=") {
			out = append(out, e)
		}
	}
	return append(out, key+"="+val)
}
