package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

var integrationRoot string

func TestMain(m *testing.M) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		fmt.Fprintln(os.Stderr, "cannot locate integration test sources")
		os.Exit(1)
	}
	integrationRoot = filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	os.Exit(m.Run())
}
