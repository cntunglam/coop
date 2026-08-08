package cli

import (
	"testing"

	"github.com/cntunglam/coop/internal/config"
)

// TestSeedConfigParses guards first-run: the seeded example config must always be
// valid, or a fresh install fails to start.
func TestSeedConfigParses(t *testing.T) {
	cfg, err := config.Parse([]byte(seedConfig))
	if err != nil {
		t.Fatalf("seed config does not parse: %v", err)
	}
	// The two runnable examples must be present.
	if _, ok := cfg.Scripts["hello"]; !ok {
		t.Error("seed missing hello script")
	}
	if s, ok := cfg.Scripts["sleeper"]; !ok || !s.Background {
		t.Error("seed missing sleeper background daemon")
	}
}
