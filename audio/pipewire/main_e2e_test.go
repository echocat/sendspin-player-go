//go:build e2e && linux && (amd64 || arm64)

package pipewire

import (
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/echocat/sendspin-player-go/internal/e2etest"
)

var packageSuite *e2etest.Suite
var privateFixture struct {
	sync.Once
	value *privatePipeWire
	err   error
	skip  string
}

func TestMain(m *testing.M) {
	packageSuite = e2etest.NewSuite()
	code := m.Run()
	// Stop native callbacks and the package's one daemon before removing the
	// shared suite's directory. Never restart it between repetitions.
	if privateFixture.value != nil {
		if err := privateFixture.value.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "private PipeWire cleanup:", err)
			code = 1
		}
	}
	if err := packageSuite.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "E2E suite cleanup:", err)
		code = 1
	}
	os.Exit(code)
}
