// Package edit is the single write path for the config file: every change
// re-reads the file from disk, applies one change, and saves.
package edit

import (
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/cntunglam/coop/internal/config"
	"github.com/cntunglam/coop/internal/daemon"
	"github.com/cntunglam/coop/internal/script"
)

// ErrChanged means the script was modified on disk since editing began.
var ErrChanged = errors.New("changed elsewhere — reload and try again")

// Editor applies single changes to the config file at Path.
type Editor struct {
	Path    string          // config file
	Daemons *daemon.Manager // for running checks
}

// Put creates (oldName == "") or replaces/renames a script. base is the script as
// it was when editing began (nil for create); if the on-disk script differs, ErrChanged.
func (e *Editor) Put(oldName, newName string, s config.Script, base *config.Script) (*config.Config, error) {
	cfg, err := config.Load(e.Path)
	if err != nil {
		return nil, err
	}
	if cfg.Scripts == nil {
		cfg.Scripts = map[string]config.Script{}
	}
	if oldName == "" {
		if err := config.ValidName(newName); err != nil {
			return nil, err
		}
		if cfg.NameTaken(newName) {
			return nil, fmt.Errorf("%q is already taken", newName)
		}
	} else {
		disk, ok := cfg.Scripts[oldName]
		if !ok {
			return nil, fmt.Errorf("no script %q", oldName)
		}
		if base != nil && !sameScript(disk, *base) {
			return nil, ErrChanged
		}
		if newName != oldName {
			if err := config.ValidName(newName); err != nil {
				return nil, err
			}
			if cfg.NameTaken(newName) {
				return nil, fmt.Errorf("%q is already taken", newName)
			}
			if err := e.checkFree(cfg, oldName); err != nil {
				return nil, err
			}
		}
		if refs := cfg.References(oldName); len(refs) > 0 && gainsParam(disk, s) {
			return nil, fmt.Errorf("%q is used by %s; can't add swappable parts", oldName, strings.Join(refs, ", "))
		}
		delete(cfg.Scripts, oldName)
	}
	cfg.Scripts[newName] = s
	return e.saveAndReload(cfg)
}

// Delete removes a script that no workflow/group uses and no daemon is running.
func (e *Editor) Delete(name string) (*config.Config, error) {
	cfg, err := config.Load(e.Path)
	if err != nil {
		return nil, err
	}
	if _, ok := cfg.Scripts[name]; !ok {
		return nil, fmt.Errorf("no script %q", name)
	}
	if err := e.checkFree(cfg, name); err != nil {
		return nil, err
	}
	delete(cfg.Scripts, name)
	return e.saveAndReload(cfg)
}

// AddPreset adds a new preset to a non-literal script.
func (e *Editor) AddPreset(scriptName, preset string, values map[string]string) (*config.Config, error) {
	cfg, err := config.Load(e.Path)
	if err != nil {
		return nil, err
	}
	s, ok := cfg.Scripts[scriptName]
	if !ok {
		return nil, fmt.Errorf("no script %q", scriptName)
	}
	if s.Literal {
		return nil, fmt.Errorf("%q is a literal command; use Make swappable in the edit form", scriptName)
	}
	if _, exists := s.Presets[preset]; exists {
		return nil, fmt.Errorf("preset %q already exists", preset)
	}
	presets := make(map[string]map[string]string, len(s.Presets)+1)
	for k, v := range s.Presets {
		presets[k] = v
	}
	presets[preset] = values
	s.Presets = presets
	cfg.Scripts[scriptName] = s
	return e.saveAndReload(cfg)
}

// Running reports whether a daemon named name is alive.
func (e *Editor) Running(name string) bool {
	if e.Daemons == nil {
		return false
	}
	ok, _, err := e.Daemons.IsRunning(name)
	return err == nil && ok
}

// checkFree refuses renames/deletes of a script that is referenced or running.
func (e *Editor) checkFree(cfg *config.Config, name string) error {
	if refs := cfg.References(name); len(refs) > 0 {
		return fmt.Errorf("%q is used by %s", name, strings.Join(refs, ", "))
	}
	if e.Running(name) {
		return fmt.Errorf("daemon %q is running; stop it first", name)
	}
	return nil
}

// gainsParam reports whether updated has a parameter that old lacks.
func gainsParam(old, updated config.Script) bool {
	had := map[string]bool{}
	for _, p := range script.ParamNames(old) {
		had[p] = true
	}
	for _, p := range script.ParamNames(updated) {
		if !had[p] {
			return true
		}
	}
	return false
}

// saveAndReload saves cfg and returns a fresh load, so callers see exactly what is on disk.
func (e *Editor) saveAndReload(cfg *config.Config) (*config.Config, error) {
	if err := config.Save(e.Path, cfg); err != nil {
		return nil, err
	}
	return config.Load(e.Path)
}

// sameScript compares scripts by their YAML form, so nil and empty
// maps/slices (omitted by omitempty) are equal.
func sameScript(a, b config.Script) bool {
	x, errA := yaml.Marshal(a)
	y, errB := yaml.Marshal(b)
	return errA == nil && errB == nil && string(x) == string(y)
}
