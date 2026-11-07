// Package runner executes scripts and workflows. A foreground script runs
// attached and its exit status is awaited; a background script is handed to the
// daemon supervisor and (optionally) gated on readiness before control returns.
package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/cntunglam/coop/internal/config"
	"github.com/cntunglam/coop/internal/daemon"
	"github.com/cntunglam/coop/internal/health"
	"github.com/cntunglam/coop/internal/script"
)

// ReadyTimeout bounds how long a background step waits to become ready.
const ReadyTimeout = 30 * time.Second

// Runner ties config, the daemon manager, and IO streams together.
type Runner struct {
	cfg *config.Config
	dm  *daemon.Manager

	// Status is where coop's own progress messages go.
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

// StartGroup brings up all members of a group as daemons, in order, tagging each
// with the group name. Members already running are tolerated (idempotent), so a
// group can be re-run to bring up whatever is missing.
func (r *Runner) StartGroup(name string) error {
	g, ok := r.cfg.Groups[name]
	if !ok {
		return fmt.Errorf("unknown group %q", name)
	}
	fmt.Fprintf(r.Status, "group %q: starting %d member(s)\n", name, len(g.Members))
	for i, m := range g.Members {
		s := r.cfg.Scripts[m.Script] // validated to exist and be background
		spec, err := script.Resolve(m.Script, s, m.Preset)
		if err != nil {
			return fmt.Errorf("group %q member %d (%s): %w", name, i+1, m.Script, err)
		}
		spec.Group = name
		if err := r.runBackground(spec, s.ReadyWait, true); err != nil {
			return fmt.Errorf("group %q stopped at member %d (%s): %w", name, i+1, m.Script, err)
		}
	}
	fmt.Fprintf(r.Status, "group %q up\n", name)
	return nil
}

// StopGroup stops all members of a group, in reverse order. It attempts every
// member and returns the first error encountered.
func (r *Runner) StopGroup(name string) error {
	g, ok := r.cfg.Groups[name]
	if !ok {
		return fmt.Errorf("unknown group %q", name)
	}
	var firstErr error
	for i := len(g.Members) - 1; i >= 0; i-- {
		member := g.Members[i].Script
		if alive, _, _ := r.dm.IsRunning(member); !alive {
			continue
		}
		fmt.Fprintf(r.Status, "group %q: stopping %s\n", name, member)
		if err := r.dm.Stop(member, 10*time.Second); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// RestartGroup stops then starts a group.
func (r *Runner) RestartGroup(name string) error {
	if err := r.StopGroup(name); err != nil {
		return err
	}
	return r.StartGroup(name)
}

// GroupMemberScripts returns the member script names of a group, in order.
func (r *Runner) GroupMemberScripts(name string) []string {
	g := r.cfg.Groups[name]
	out := make([]string, len(g.Members))
	for i, m := range g.Members {
		out[i] = m.Script
	}
	return out
}

// memberSupervisor tracks one group member's health across supervisor cycles.
type memberSupervisor struct {
	step        config.Step
	spec        *script.Spec
	probe       health.Probe // nil when the member has no health block
	consecFails int
	restarts    []time.Time // restart times within the rolling window
	failed      bool        // cap exceeded — abandoned
	nextProbe   time.Time
}

// Supervise brings a group up and blocks, keeping it running until stop is
// closed, then tears the group down. Members with a health block are probed on
// their interval and restarted (capped) when unhealthy or dead; members without
// one keep the legacy AutoRestart dead-PID behavior. Intended for `coop serve`.
func (r *Runner) Supervise(name string, stop <-chan struct{}) error {
	g, ok := r.cfg.Groups[name]
	if !ok {
		return fmt.Errorf("unknown group %q", name)
	}
	if err := r.StartGroup(name); err != nil {
		return err
	}

	members := make([]*memberSupervisor, 0, len(g.Members))
	for _, m := range g.Members {
		s := r.cfg.Scripts[m.Script]
		spec, err := script.Resolve(m.Script, s, m.Preset)
		if err != nil {
			fmt.Fprintf(r.Status, "group %q: cannot supervise %s: %v\n", name, m.Script, err)
			continue
		}
		spec.Group = name
		ms := &memberSupervisor{step: m, spec: spec}
		if spec.Health != nil {
			p, err := health.Build(spec.Health.Kind, spec.Health.Target, spec.Health.Env)
			if err != nil {
				fmt.Fprintf(r.Status, "group %q: %s has an invalid health probe (%v) — marking failed, not supervising\n", name, m.Script, err)
				ms.failed = true
				_ = r.dm.SetHealth(m.Script, daemon.HealthFailed)
			} else {
				ms.probe = p
			}
		}
		members = append(members, ms)
	}

	if g.AutoRestart {
		fmt.Fprintf(r.Status, "group %q: supervising (auto-restart on) — Ctrl-C to stop\n", name)
	} else {
		fmt.Fprintf(r.Status, "group %q: supervising — Ctrl-C to stop\n", name)
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			fmt.Fprintf(r.Status, "group %q: shutting down\n", name)
			return r.StopGroup(name)
		case now := <-ticker.C:
			for _, ms := range members {
				r.superviseMember(name, ms, g.AutoRestart, now)
			}
		}
	}
}

// superviseMember runs one supervision cycle for a single member.
func (r *Runner) superviseMember(group string, ms *memberSupervisor, autoRestart bool, now time.Time) {
	if ms.failed {
		return // abandoned after exceeding the restart cap
	}
	alive, _, _ := r.dm.IsRunning(ms.step.Script)
	if !alive {
		if ms.probe != nil {
			r.restartMember(group, ms, now, "down")
		} else if autoRestart {
			fmt.Fprintf(r.Status, "group %q: %s is down — restarting\n", group, ms.step.Script)
			r.startMember(group, ms)
		}
		return
	}
	if ms.probe == nil || now.Before(ms.nextProbe) {
		return
	}
	ms.nextProbe = now.Add(time.Duration(ms.spec.Health.Interval) * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(ms.spec.Health.Timeout)*time.Second)
	err := ms.probe.Check(ctx)
	cancel()
	if err == nil {
		ms.consecFails = 0
		_ = r.dm.SetHealth(ms.step.Script, daemon.HealthHealthy)
		return
	}
	ms.consecFails++
	_ = r.dm.SetHealth(ms.step.Script, daemon.HealthUnhealthy)
	fmt.Fprintf(r.Status, "group %q: %s health failed (%d/%d): %v\n",
		group, ms.step.Script, ms.consecFails, ms.spec.Health.Failures, err)
	if ms.consecFails >= ms.spec.Health.Failures {
		r.restartMember(group, ms, now, "unhealthy")
	}
}

// restartMember restarts a health-configured member, respecting the rolling
// restart cap. On cap exceeded it marks the member failed and stops touching it.
func (r *Runner) restartMember(group string, ms *memberSupervisor, now time.Time, reason string) {
	if ms.failed {
		return
	}
	cutoff := now.Add(-time.Duration(ms.spec.Health.RestartWindow) * time.Second)
	kept := ms.restarts[:0]
	for _, t := range ms.restarts {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	ms.restarts = kept
	if max := ms.spec.Health.MaxRestarts; max >= 0 && len(ms.restarts) >= max {
		ms.failed = true
		_ = r.dm.SetHealth(ms.step.Script, daemon.HealthFailed)
		fmt.Fprintf(r.Status, "group %q: %s exceeded %d restarts in %ds — giving up\n",
			group, ms.step.Script, max, ms.spec.Health.RestartWindow)
		return
	}
	fmt.Fprintf(r.Status, "group %q: restarting %s (%s)\n", group, ms.step.Script, reason)
	if alive, _, _ := r.dm.IsRunning(ms.step.Script); alive {
		_ = r.dm.Stop(ms.step.Script, 10*time.Second)
	}
	r.startMember(group, ms)
	ms.restarts = append(ms.restarts, now)
	ms.consecFails = 0
	ms.nextProbe = now.Add(time.Duration(ms.spec.Health.Interval) * time.Second)
}

// startMember (re)starts a member daemon, tolerating an already-running instance.
func (r *Runner) startMember(group string, ms *memberSupervisor) {
	if err := r.runBackground(ms.spec, ms.spec.ReadyWait, true); err != nil {
		fmt.Fprintf(r.Status, "group %q: start %s failed: %v\n", group, ms.step.Script, err)
	}
}

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
	cmd.Dir = spec.Dir
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
			return fmt.Errorf("daemon %q (pid %d) did not become ready: %w; inspect with `coop logs %s`",
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
