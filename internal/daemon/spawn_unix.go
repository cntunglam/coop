//go:build unix

package daemon

import (
	"os/exec"
	"syscall"

	"github.com/cntunglam/coop/internal/script"
)

// buildDetachedCmd constructs an exec.Cmd that, once started, runs in its own
// session (Setsid) with no controlling terminal, so it survives the parent
// process exiting and the terminal window closing.
func buildDetachedCmd(spec *script.Spec) *exec.Cmd {
	cmd := exec.Command(spec.Argv[0], spec.Argv[1:]...)
	cmd.Env = spec.Env
	cmd.Dir = spec.Dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd
}
