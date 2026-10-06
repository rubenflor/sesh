//go:build unix

package execwrap

import (
	"os"
	"os/exec"
	"syscall"
)

// Own the probe's process group so cancellation also terminates descendants
// retaining output pipes. Clean up the group even when the direct child exits.
func prepareProcessCleanup(command *exec.Cmd) func() {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	kill := func() error {
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	command.Cancel = kill
	return func() {
		if command.Process != nil {
			_ = kill()
		}
	}
}
