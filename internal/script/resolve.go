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

	"github.com/caongoctunglam/rx/internal/config"
)

// Spec is a fully-resolved command ready to be exec'd.
type Spec struct {
	Name       string   // script name
	Preset     string   // preset name ("" if none)
	Group      string   // group/namespace this daemon belongs to ("" if none)
	Argv       []string // command + args
	Env        []string // full environment (os.Environ() + preset vars)
	Background bool
	ReadyPort  int    // 0 if none
	ReadyWait  int    // seconds; 0 if none
	Display    string // human-readable command for logs/meta
}

// Resolve turns (script, preset) into a Spec. presetName may be "" only when the
// script has no presets; otherwise it must name an existing preset.
func Resolve(name string, s config.Script, presetName string) (*Spec, error) {
	values, err := selectPreset(name, s, presetName)
	if err != nil {
		return nil, err
	}

	spec := &Spec{
		Name:       name,
		Preset:     presetName,
		Env:        buildEnv(values),
		Background: s.Background,
		ReadyWait:  s.ReadyWait,
	}

	switch s.ResolvedType() {
	case config.TypeCommand:
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

	return spec, nil
}

// selectPreset returns the parameter values for the chosen preset, with helpful errors.
func selectPreset(name string, s config.Script, presetName string) (map[string]string, error) {
	if presetName == "" {
		if len(s.Presets) == 0 {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("script %q requires a preset; choose one of: %s",
			name, strings.Join(s.PresetNames(), ", "))
	}
	values, ok := s.Presets[presetName]
	if !ok {
		return nil, fmt.Errorf("script %q has no preset %q; choose one of: %s",
			name, presetName, strings.Join(s.PresetNames(), ", "))
	}
	return values, nil
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
