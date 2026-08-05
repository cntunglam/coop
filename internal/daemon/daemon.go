// Package daemon supervises background (long-lived) scripts: it starts them
// detached from the launching process, tracks them by PID under a state
// directory, and supports listing, log access, and stopping.
package daemon

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/caongoctunglam/rx/internal/script"
)

// Meta is the persisted record of a running daemon.
type Meta struct {
	Name      string `json:"name"`
	Preset    string `json:"preset"`
	Group     string `json:"group,omitempty"` // namespace this daemon belongs to
	Command   string `json:"command"`         // resolved, human-readable
	LogPath   string `json:"log_path"`
	PID       int    `json:"pid"`
	StartUnix int64  `json:"start_unix"`
	ReadyPort int    `json:"ready_port,omitempty"`
}

// Status is a Meta enriched with live checks for `rx ps`.
type Status struct {
	Meta
	Alive bool
	Ready bool // only meaningful when ReadyPort > 0
}

// Manager owns a state directory holding one subdirectory per daemon.
type Manager struct {
	base string
}

// NewManager returns a Manager rooted at the default state dir
// (honoring XDG_STATE_HOME, else ~/.local/state/rx).
func NewManager() (*Manager, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return &Manager{base: filepath.Join(base, "rx")}, nil
}

// NewManagerAt returns a Manager rooted at an explicit directory (for tests).
func NewManagerAt(dir string) *Manager { return &Manager{base: dir} }

func (m *Manager) dir(name string) string   { return filepath.Join(m.base, name) }
func (m *Manager) metaPath(n string) string { return filepath.Join(m.dir(n), "meta.json") }

// Start launches spec as a detached daemon. It refuses if a daemon of the same
// name is already alive (idempotency). It does not wait for readiness — call
// WaitReady separately when the caller needs the daemon reachable.
func (m *Manager) Start(spec *script.Spec) (*Meta, error) {
	if strings.ContainsAny(spec.Name, "/\\") {
		return nil, fmt.Errorf("invalid daemon name %q", spec.Name)
	}
	if alive, existing, _ := m.IsRunning(spec.Name); alive {
		return nil, fmt.Errorf("daemon %q is already running (pid %d); use `rx stop %s` first",
			spec.Name, existing.PID, spec.Name)
	}

	dir := m.dir(spec.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	logPath := filepath.Join(dir, "log")
	// Truncate the log on a fresh start so `rx logs` shows only the current run.
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	defer logFile.Close()

	cmd := buildDetachedCmd(spec)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting daemon %q: %w", spec.Name, err)
	}
	// Reap the child when it exits so it does not linger as a zombie while this
	// process stays alive (e.g. the long-running TUI). A zombie would falsely
	// read as "alive" via kill(pid,0) and make Stop wait its full timeout. The
	// daemon still survives us exiting: it is Setsid-detached and reparents to
	// launchd/init, which then reaps it instead.
	go func() { _ = cmd.Wait() }()

	meta := &Meta{
		Name:      spec.Name,
		Preset:    spec.Preset,
		Group:     spec.Group,
		Command:   spec.Display,
		LogPath:   logPath,
		PID:       cmd.Process.Pid,
		StartUnix: time.Now().Unix(),
		ReadyPort: spec.ReadyPort,
	}
	if err := m.writeMeta(meta); err != nil {
		return nil, err
	}
	return meta, nil
}

// WaitReady blocks until the daemon's readiness condition passes or timeout
// elapses. ReadyPort (TCP probe) takes precedence; otherwise ReadyWait seconds
// from the spec are honored via the meta. Returns nil immediately if neither is set.
func (m *Manager) WaitReady(meta *Meta, readyWait int, timeout time.Duration) error {
	if meta.ReadyPort > 0 {
		return waitPort(meta.ReadyPort, timeout)
	}
	if readyWait > 0 {
		time.Sleep(time.Duration(readyWait) * time.Second)
	}
	return nil
}

func waitPort(port int, timeout time.Duration) error {
	addr := net.JoinHostPort("localhost", strconv.Itoa(port))
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for port %d to accept connections", timeout, port)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// Get returns the recorded metadata for a daemon, erroring if unknown.
func (m *Manager) Get(n string) (*Meta, error) {
	return m.readMeta(n)
}

// IsRunning reports whether a daemon named n has a live process.
func (m *Manager) IsRunning(n string) (bool, *Meta, error) {
	meta, err := m.readMeta(n)
	if err != nil {
		return false, nil, err
	}
	return processAlive(meta.PID), meta, nil
}

// List returns the status of every recorded daemon, sorted by name.
func (m *Manager) List() ([]Status, error) {
	entries, err := os.ReadDir(m.base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Status
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		meta, err := m.readMeta(e.Name())
		if err != nil {
			continue // ignore malformed/partial dirs
		}
		st := Status{Meta: *meta, Alive: processAlive(meta.PID)}
		if st.Alive && meta.ReadyPort > 0 {
			st.Ready = waitPort(meta.ReadyPort, 300*time.Millisecond) == nil
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// LogPath returns the path to a daemon's log file, erroring if unknown.
func (m *Manager) LogPath(n string) (string, error) {
	meta, err := m.readMeta(n)
	if err != nil {
		return "", err
	}
	return meta.LogPath, nil
}

// Stop sends SIGTERM to the daemon, escalating to SIGKILL if it does not exit
// within timeout, then removes its state directory.
func (m *Manager) Stop(n string, timeout time.Duration) error {
	meta, err := m.readMeta(n)
	if err != nil {
		return err
	}
	if processAlive(meta.PID) {
		// Signal the whole process group (negative pid) since we Setsid'd it.
		_ = syscall.Kill(-meta.PID, syscall.SIGTERM)
		_ = syscall.Kill(meta.PID, syscall.SIGTERM)

		deadline := time.Now().Add(timeout)
		for processAlive(meta.PID) && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		if processAlive(meta.PID) {
			_ = syscall.Kill(-meta.PID, syscall.SIGKILL)
			_ = syscall.Kill(meta.PID, syscall.SIGKILL)
			time.Sleep(100 * time.Millisecond)
		}
	}
	return os.RemoveAll(m.dir(n))
}

func (m *Manager) writeMeta(meta *Meta) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.metaPath(meta.Name), data, 0o644)
}

func (m *Manager) readMeta(n string) (*Meta, error) {
	data, err := os.ReadFile(m.metaPath(n))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no daemon named %q", n)
		}
		return nil, err
	}
	var meta Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("reading daemon %q meta: %w", n, err)
	}
	return &meta, nil
}

// processAlive reports whether pid refers to a live process. Signal 0 performs
// permission/existence checks without delivering a signal.
//
// Limitation: PIDs are reused by the OS, so in the rare window where our PID has
// been recycled by an unrelated process this may report a false positive. We do
// not query per-process start time (platform-specific) to disambiguate.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	// EPERM means the process exists but we may not signal it (still "alive").
	return err == syscall.EPERM
}
