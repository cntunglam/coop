// Package config defines rx's configuration schema and loads/validates it from
// the user's global config file (~/.config/rx/config.yaml).
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// ScriptType distinguishes an inline shell command from a pointer to a script file.
type ScriptType string

const (
	TypeCommand ScriptType = "command"
	TypeFile    ScriptType = "file"
)

// ReservedNames are CLI verbs that a script or workflow name may not shadow.
var ReservedNames = map[string]bool{
	"ps":         true,
	"logs":       true,
	"stop":       true,
	"list":       true,
	"config":     true,
	"help":       true,
	"ui":         true,
	"completion": true, // cobra registers this automatically
}

// Script is a single runnable entry: either an inline command or a file, with
// named presets of parameter values.
type Script struct {
	Description string     `yaml:"description,omitempty"`
	Type        ScriptType `yaml:"type,omitempty"`
	Command     string     `yaml:"command,omitempty"` // for Type == command
	File        string     `yaml:"file,omitempty"`    // for Type == file
	Background  bool       `yaml:"background,omitempty"`
	// ReadyPort, if set, marks the daemon "ready" once this TCP port accepts a
	// connection. It is a template string so a preset value can drive it (e.g. "{{.port}}").
	ReadyPort string `yaml:"ready_port,omitempty"`
	// ReadyWait is a fixed readiness delay in seconds (fallback when there is no port to probe).
	ReadyWait int `yaml:"ready_wait,omitempty"`
	// Presets maps a preset name to its bundle of string parameter values.
	Presets map[string]map[string]string `yaml:"presets,omitempty"`
}

// Step is one entry in a workflow: a script run under a named preset.
type Step struct {
	Script string `yaml:"script"`
	Preset string `yaml:"preset,omitempty"`
}

// Workflow is an ordered sequence of steps run stop-on-failure.
type Workflow struct {
	Description string `yaml:"description,omitempty"`
	Steps       []Step `yaml:"steps"`
}

// Config is the whole config file.
type Config struct {
	Scripts   map[string]Script   `yaml:"scripts"`
	Workflows map[string]Workflow `yaml:"workflows"`
}

// DefaultPath returns the global config path, honoring XDG_CONFIG_HOME.
func DefaultPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "rx", "config.yaml"), nil
}

// Load reads and validates the config at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Save writes cfg back to path as YAML. It marshals, re-parses to guarantee the
// result is valid (so a bug can never leave an unloadable config on disk), and
// only then writes. Comments in the previous file are not preserved.
func Save(path string, cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if _, err := Parse(data); err != nil {
		return fmt.Errorf("refusing to write invalid config: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// Parse decodes and validates config bytes. Split out from Load for testability.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true) // reject unknown/misspelled fields
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	if cfg.Scripts == nil {
		cfg.Scripts = map[string]Script{}
	}
	if cfg.Workflows == nil {
		cfg.Workflows = map[string]Workflow{}
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) validate() error {
	// Reserved-name collisions and script/workflow name clashes.
	for name := range c.Scripts {
		if ReservedNames[name] {
			return fmt.Errorf("script %q collides with a reserved command name", name)
		}
	}
	for name := range c.Workflows {
		if ReservedNames[name] {
			return fmt.Errorf("workflow %q collides with a reserved command name", name)
		}
		if _, dup := c.Scripts[name]; dup {
			return fmt.Errorf("%q is defined as both a script and a workflow", name)
		}
	}

	// Per-script shape checks.
	for name, s := range c.Scripts {
		typ := s.Type
		if typ == "" {
			typ = TypeCommand // default
		}
		switch typ {
		case TypeCommand:
			if s.Command == "" {
				return fmt.Errorf("script %q: type command requires a non-empty 'command'", name)
			}
			if s.File != "" {
				return fmt.Errorf("script %q: 'file' set on a command-type script", name)
			}
		case TypeFile:
			if s.File == "" {
				return fmt.Errorf("script %q: type file requires a non-empty 'file'", name)
			}
			if s.Command != "" {
				return fmt.Errorf("script %q: 'command' set on a file-type script", name)
			}
		default:
			return fmt.Errorf("script %q: unknown type %q (want command or file)", name, typ)
		}
	}

	// Workflow steps must reference existing scripts and valid presets.
	for wname, w := range c.Workflows {
		if len(w.Steps) == 0 {
			return fmt.Errorf("workflow %q has no steps", wname)
		}
		for i, step := range w.Steps {
			s, ok := c.Scripts[step.Script]
			if !ok {
				return fmt.Errorf("workflow %q step %d references unknown script %q", wname, i+1, step.Script)
			}
			if step.Preset != "" {
				if _, ok := s.Presets[step.Preset]; !ok {
					return fmt.Errorf("workflow %q step %d: script %q has no preset %q", wname, i+1, step.Script, step.Preset)
				}
			}
		}
	}
	return nil
}

// ResolvedType returns the script type, applying the command default.
func (s Script) ResolvedType() ScriptType {
	if s.Type == "" {
		return TypeCommand
	}
	return s.Type
}

// PresetNames returns the sorted preset names for a script (for help/errors).
func (s Script) PresetNames() []string {
	names := make([]string, 0, len(s.Presets))
	for n := range s.Presets {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
