//go:build !unix && !windows

package examples

import (
	"os"
	"os/exec"
)

func prepareProcess(_ *exec.Cmd) (func() error, func() error, error) {
	return func() error { return nil }, func() error { return nil }, nil
}

func interruptProcess(cmd *exec.Cmd) error { return cmd.Process.Signal(os.Interrupt) }

func killProcess(cmd *exec.Cmd) error { return cmd.Process.Kill() }

func processExitCode(exit *exec.ExitError) int {
	if code := exit.ExitCode(); code >= 0 {
		return code
	}
	return 1
}

func processInterrupts() []os.Signal { return []os.Signal{os.Interrupt} }
