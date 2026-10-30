//go:build e2e && (aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package conformancetest

import (
	"errors"
	"os/exec"
	"syscall"
)

// The unchanged harness may spawn scenario adapters. A private process group
// lets Go clean those up even if the upstream coroutine is interrupted.
func prepareHarnessProcess(cmd *exec.Cmd) (func() error, func() error, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return func() error { return nil }, func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}, nil
}
