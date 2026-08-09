package health

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTCPProbeHealthyWhenPortOpen(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	p, err := Build("tcp", port, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.Check(ctx); err != nil {
		t.Errorf("open port should be healthy, got %v", err)
	}
}

func TestTCPProbeErrorWhenPortClosed(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close() // free the port so the dial fails

	p, _ := Build("tcp", port, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := p.Check(ctx); err == nil {
		t.Error("closed port should be unhealthy")
	}
}

func TestCommandProbeExitZeroHealthy(t *testing.T) {
	p, _ := Build("command", "true", nil)
	if err := p.Check(context.Background()); err != nil {
		t.Errorf("exit 0 should be healthy, got %v", err)
	}
}

func TestCommandProbeExitNonZeroError(t *testing.T) {
	p, _ := Build("command", "false", nil)
	if err := p.Check(context.Background()); err == nil {
		t.Error("exit 1 should be unhealthy")
	}
}

func TestCommandProbeUsesEnv(t *testing.T) {
	p, _ := Build("command", `test "$WANT" = ok`, []string{"WANT=ok"})
	if err := p.Check(context.Background()); err != nil {
		t.Errorf("env-driven probe should pass, got %v", err)
	}
}

func TestCommandProbeTimeoutKills(t *testing.T) {
	p, _ := Build("command", "sleep 5", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := p.Check(ctx); err == nil {
		t.Error("timed-out probe should error")
	}
	if time.Since(start) > 2*time.Second {
		t.Error("probe did not respect context timeout")
	}
}

func TestHTTPProbe2xxHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()
	p, _ := Build("http", srv.URL, nil)
	if err := p.Check(context.Background()); err != nil {
		t.Errorf("200 should be healthy, got %v", err)
	}
}

func TestHTTPProbe5xxError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	p, _ := Build("http", srv.URL, nil)
	if err := p.Check(context.Background()); err == nil {
		t.Error("500 should be unhealthy")
	}
}

func TestBuildUnknownKind(t *testing.T) {
	if _, err := Build("bogus", "x", nil); err == nil {
		t.Error("unknown kind should error")
	}
}

func TestBuildTCPBadPort(t *testing.T) {
	if _, err := Build("tcp", "not-a-port", nil); err == nil {
		t.Error("non-numeric tcp target should error")
	}
}
