// Package runner executes scripts and workflows. A foreground script runs
// attached and its exit status is awaited; a background script is handed to the
// daemon supervisor and (optionally) gated on readiness before control returns.
package runner

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/caongoctunglam/rx/internal/config"
	"github.com/caongoctunglam/rx/internal/daemon"
	"github.com/caongoctunglam/rx/internal/script"
)

// ReadyTimeout bounds how long a background step waits to become ready.
const ReadyTimeout = 30 * time.Second

// Runner ties config, the daemon manager, and IO streams together.
type Runner struct {
	cfg *config.Config
	dm  *daemon.Manager

	// Status is where rx's own progress messages go.
	Status io.Writer
	// Stdout/Stderr/Stdin are wired to foreground child processes.
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
}

// New returns a Runner with default IO wired to the process std streams.
func New(cfg *config.Config, dm *daemon.Manager) *Runner {
	return &Runner{
		cfg:    cfg,
		dm:     dm,
		Status: os.Stderr,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Stdin:  os.Stdin,
	}
}

// RunScript runs a single named script under an optional preset. A background
// script that is already running is refused (strict idempotency).
func (r *Runner) RunScript(name, preset string) error {
	s, ok := r.cfg.Scripts[name]
	if !ok {
		return fmt.Errorf("unknown script %q", name)
	}
	return r.runStep(name, s, preset, false)
}

// RestartDaemon stops a running daemon (if any) and starts it again using the
// preset recorded when it was first launched.
func (r *Runner) RestartDaemon(name string) error {
	meta, err := r.dm.Get(name)
	if err != nil {
		return err
	}
	s, ok := r.cfg.Scripts[name]
	if !ok {
		return fmt.Errorf("daemon %q has no matching script in config", name)
	}
	if err := r.dm.Stop(name, 10*time.Second); err != nil {
		return err
	}
	return r.runStep(name, s, meta.Preset, false)
}

// Config exposes the loaded config to callers such as the TUI.
func (r *Runner) Config() *config.Config { return r.cfg }

// Daemons exposes the daemon manager to callers such as the TUI.
func (r *Runner) Daemons() *daemon.Manager { return r.dm }

// RunWorkflow runs a workflow's steps in order, stopping at the first failure.
func (r *Runner) RunWorkflow(name string) error {
	w, ok := r.cfg.Workflows[name]
	if !ok {
		return fmt.Errorf("unknown workflow %q", name)
	}
	fmt.Fprintf(r.Status, "workflow %q: %d step(s)\n", name, len(w.Steps))
	for i, step := range w.Steps {
		s := r.cfg.Scripts[step.Script] // presence guaranteed by config validation
		fmt.Fprintf(r.Status, "\n[%d/%d] %s%s\n", i+1, len(w.Steps), step.Script, presetSuffix(step.Preset))
		// Within a workflow, a background daemon that is already up is treated as
		// satisfied (and still gated on readiness) so the workflow is re-runnable.
		if err := r.runStep(step.Script, s, step.Preset, true); err != nil {
			return fmt.Errorf("workflow %q stopped at step %d (%s): %w", name, i+1, step.Script, err)
		}
	}
	fmt.Fprintf(r.Status, "\nworkflow %q completed\n", name)
	return nil
}

// runStep resolves a script+preset and dispatches to background or foreground.
// tolerateRunning controls whether an already-running background daemon is an
// error (standalone runs) or an accepted no-op (workflow steps).
func (r *Runner) runStep(name string, s config.Script, preset string, tolerateRunning bool) error {
	spec, err := script.Resolve(name, s, preset)
	if err != nil {
		return err
	}
	if spec.Background {
		return r.runBackground(spec, s.ReadyWait, tolerateRunning)
	}
	return r.runForeground(spec)
}

// runForeground executes attached and waits for the process to exit.
func (r *Runner) runForeground(spec *script.Spec) error {
	fmt.Fprintf(r.Status, "→ %s\n", spec.Display)
	cmd := exec.Command(spec.Argv[0], spec.Argv[1:]...)
	cmd.Env = spec.Env
	cmd.Stdin = r.Stdin
	cmd.Stdout = r.Stdout
	cmd.Stderr = r.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", spec.Name, err)
	}
	return nil
}

// runBackground starts a managed daemon and waits for its readiness gate so a
// following workflow step can safely run against it. If tolerateRunning is set
// and a daemon of this name is already up, it is accepted as-is (still gated on
// readiness) rather than refused.
func (r *Runner) runBackground(spec *script.Spec, readyWait int, tolerateRunning bool) error {
	if tolerateRunning {
		if alive, existing, _ := r.dm.IsRunning(spec.Name); alive {
			fmt.Fprintf(r.Status, "→ daemon %q already running (pid %d)\n", existing.Name, existing.PID)
			return r.gateReadiness(existing, spec, readyWait)
		}
	}
	meta, err := r.dm.Start(spec)
	if err != nil {
		return err
	}
	fmt.Fprintf(r.Status, "→ started daemon %q (pid %d)\n", meta.Name, meta.PID)
	return r.gateReadiness(meta, spec, readyWait)
}

// gateReadiness blocks until the daemon passes its readiness condition.
func (r *Runner) gateReadiness(meta *daemon.Meta, spec *script.Spec, readyWait int) error {
	if spec.ReadyPort > 0 || readyWait > 0 {
		fmt.Fprintf(r.Status, "  waiting for readiness…\n")
		if err := r.dm.WaitReady(meta, readyWait, ReadyTimeout); err != nil {
			return fmt.Errorf("daemon %q (pid %d) did not become ready: %w; inspect with `rx logs %s`",
				meta.Name, meta.PID, err, meta.Name)
		}
		fmt.Fprintf(r.Status, "  ready.\n")
	}
	return nil
}

func presetSuffix(preset string) string {
	if preset == "" {
		return ""
	}
	return " (" + preset + ")"
}
