package capture

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Recent is another zsh tab's last command, read from its record file.
type Recent struct {
	PID     int
	When    time.Time
	Dir     string
	Command string
}

// ShellsDir is where the zsh snippet writes one record file per shell:
// ${XDG_STATE_HOME:-$HOME/.local/state}/coop/shells/<pid>.
func ShellsDir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "coop", "shells"), nil
}

// ReadRecent returns the records in dir, newest first, except selfPID's.
// A record is "epoch\x00PWD\x00command\x00" (zsh's print -rN), so multi-line
// commands round-trip byte-for-byte. Records of dead shells (alive false;
// nil means a signal-0 check) and malformed records are deleted. Files whose
// name is not a PID are not records and are left alone.
func ReadRecent(dir string, selfPID int, alive func(int) bool) ([]Recent, error) {
	if alive == nil {
		alive = pidAlive
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Recent
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 || !e.Type().IsRegular() {
			continue
		}
		if pid == selfPID {
			continue
		}
		path := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		r, ok := parseRecord(pid, string(b))
		if !ok || !alive(pid) {
			os.Remove(path)
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].When.Equal(out[j].When) {
			return out[i].When.After(out[j].When)
		}
		return out[i].PID < out[j].PID
	})
	return out, nil
}

func parseRecord(pid int, body string) (Recent, bool) {
	f := strings.Split(body, "\x00")
	if len(f) != 4 || f[3] != "" || f[2] == "" {
		return Recent{}, false
	}
	epoch, err := strconv.ParseInt(f[0], 10, 64)
	if err != nil {
		return Recent{}, false
	}
	return Recent{PID: pid, When: time.Unix(epoch, 0), Dir: f[1], Command: f[2]}, true
}

// pidAlive reports whether pid is a live process (signal 0; EPERM means it
// exists but belongs to someone else).
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
