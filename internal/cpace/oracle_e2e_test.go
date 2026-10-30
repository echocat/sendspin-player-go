//go:build e2e

package cpace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/internal/e2etest"
)

var independentOracle struct {
	ctx    context.Context
	once   sync.Once
	output []byte
	err    error
}

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	independentOracle.ctx = ctx
	code := m.Run()
	cancel()
	os.Exit(code)
}

func TestE2EIndependentCPaceOracle(t *testing.T) {
	if testing.Short() {
		t.Skip("independent CPace runtime is disabled by -short")
	}
	independentOracle.once.Do(func() {
		root := e2etest.Root()
		python, err := e2etest.Python(independentOracle.ctx, root, "e2e")
		if err != nil {
			independentOracle.err = fmt.Errorf("prepare independent pinned CPace runtime: %w", err)
			return
		}
		// The oracle's default recomputes all transcripts and compares frozen
		// bytes. Never pass --write during a normal test.
		cmd := exec.CommandContext(independentOracle.ctx, python, filepath.Join(root, "internal", "e2e", "cpace_oracle.py"))
		cmd.Dir = root
		cmd.WaitDelay = 5 * time.Second
		independentOracle.output, independentOracle.err = cmd.CombinedOutput()
	})
	if independentOracle.err != nil {
		t.Fatalf("independent CPace oracle: %v\n%s", independentOracle.err, independentOracle.output)
	}
	t.Logf("%s", independentOracle.output)
	// Also compare the just-verified independent corpus with the Go implementation.
	checkPinnedPythonDifferentialCorpus(t)
}
