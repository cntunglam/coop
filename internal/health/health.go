// Package health probes whether a running daemon is actually serving, beyond
// merely having a live PID. A Probe returns nil when the target is healthy.
package health

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
)

// Probe reports whether a target is healthy. Check returns nil for healthy,
// a non-nil error (including context timeout) for unhealthy.
type Probe interface {
	Check(ctx context.Context) error
}

// Build constructs a Probe from a resolved descriptor. kind is one of
// "tcp", "command", or "http". target is the resolved port / command / URL.
// env is applied only to the command probe.
func Build(kind, target string, env []string) (Probe, error) {
	switch kind {
	case "tcp":
		port, err := strconv.Atoi(strings.TrimSpace(target))
		if err != nil {
			return nil, fmt.Errorf("health: tcp target %q is not a port: %w", target, err)
		}
		return tcpProbe{addr: net.JoinHostPort("localhost", strconv.Itoa(port))}, nil
	case "command":
		return commandProbe{command: target, env: env}, nil
	case "http":
		return httpProbe{url: target}, nil
	default:
		return nil, fmt.Errorf("health: unknown probe kind %q", kind)
	}
}

type tcpProbe struct{ addr string }

func (p tcpProbe) Check(ctx context.Context) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", p.addr)
	if err != nil {
		return err
	}
	return conn.Close()
}

type commandProbe struct {
	command string
	env     []string
}

func (p commandProbe) Check(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "sh", "-c", p.command)
	cmd.Env = p.env
	return cmd.Run() // non-zero exit or ctx kill -> non-nil
}

type httpProbe struct{ url string }

func (p httpProbe) Check(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("health: HTTP %d from %s", resp.StatusCode, p.url)
	}
	return nil
}
