//go:build unix

package examples

import (
	"os"
	"os/exec"
	"syscall"
)

func prepareProcess(cmd *exec.Cmd) (func() error, func() error, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return func() error { return nil }, func() error { return nil }, nil
}

func interruptProcess(cmd *exec.Cmd) error {
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
}

func killProcess(cmd *exec.Cmd) error {
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

func processExitCode(exit *exec.ExitError) int {
	if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return exit.ExitCode()
}

func processInterrupts() []os.Signal { return []os.Signal{os.Interrupt, syscall.SIGTERM} }
