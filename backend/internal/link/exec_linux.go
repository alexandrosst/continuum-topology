package link

import (
	"os"
	"os/exec"
	"syscall"
)

// killGroupOnCancel puts the program in a process group of its own and kills the whole group when the context ends. Killing
// only the program leaves what it started (owping forks an endpoint per session) running and holding its output pipes, so
// the call would wait out WaitDelay with the gate held, and the orphan would live on.
func killGroupOnCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			if err == syscall.ESRCH {
				return os.ErrProcessDone
			}
			return err
		}
		return nil
	}
}
