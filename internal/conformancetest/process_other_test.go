//go:build e2e && !(aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris || windows)

package conformancetest

import (
	"fmt"
	"os/exec"
	"runtime"
)

func prepareHarnessProcess(_ *exec.Cmd) (func() error, func() error, error) {
	return nil, nil, fmt.Errorf("official harness process containment is unavailable on %s", runtime.GOOS)
}
