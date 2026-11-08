package cli

import (
	"strings"
	"testing"

	"github.com/cntunglam/coop/internal/config"
)

func TestCheckEditable(t *testing.T) {
	cfg, err := config.Parse([]byte(`
scripts:
  alpha:
    command: echo a
    background: true
workflows:
  flow:
    steps:
      - { script: alpha }
groups:
  grp:
    members:
      - { script: alpha }
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := checkEditable(cfg, "alpha"); err != nil {
		t.Errorf("alpha: %v", err)
	}
	if err := checkEditable(cfg, "nope"); err == nil || err.Error() != `no script "nope"` {
		t.Errorf("nope: %v", err)
	}
	for _, name := range []string{"flow", "grp"} {
		if err := checkEditable(cfg, name); err == nil || !strings.Contains(err.Error(), "isn't supported yet") {
			t.Errorf("%s: %v", name, err)
		}
	}
}
