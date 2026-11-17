//go:build e2e

package conformancetest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/internal/e2etest"
)

func TestE2EHarnessReadinessBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("Python harness binding is disabled by -short")
	}
	root := e2etest.Root()
	python, err := e2etest.Python(official.ctx, root, "conformancetest")
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the actual registration seam without starting another harness.
	// A virtual cold start exceeds the old budget deterministically; no sleep,
	// fabricated ready file or changed protocol verdict is needed for this test.
	const script = `
import argparse, asyncio, sys
from pathlib import Path
sys.path.insert(0, sys.argv[1])
import run as binding

async def check():
    run_case = binding.runner.run_case
    wait_for_exit = binding.runner.wait_for_exit
    binding.register(argparse.Namespace(client="unused-sdk-client", fixture_repo=sys.argv[2]))
    assert binding.runner.run_case is run_case
    assert binding.runner.wait_for_exit is wait_for_exit
    assert binding.runner.wait_for_file is binding.wait_for_adapter_file
    calls = []
    async def cold_start(path, *, timeout_s):
        calls.append((path.name, timeout_s))
        if path.name.endswith("-ready.json") and timeout_s < 12:
            raise TimeoutError("cold imports exceeded ten seconds")
    binding.wait_for_file = cold_start
    for name, requested, expected in (
        ("server-ready.json", 10, 60),
        ("client-ready.json", 10, 60),
        ("server-ready.json", 90, 90),
        ("summary.json", 10, 10),
        ("registry.json", 2, 2),
    ):
        await binding.runner.wait_for_file(Path(sys.argv[2]) / name, timeout_s=requested)
        assert calls[-1] == (name, expected), calls
    for failure in (TimeoutError("not ready"), asyncio.CancelledError()):
        async def fail(path, *, timeout_s):
            raise failure
        binding.wait_for_file = fail
        try:
            await binding.runner.wait_for_file(Path("server-ready.json"), timeout_s=10)
        except type(failure) as caught:
            assert caught is failure
        else:
            raise AssertionError("readiness failure/cancellation was hidden")
    print("ok")

asyncio.run(check())
`
	cmd := exec.CommandContext(official.ctx, python, "-c", script, filepath.Join(root, "internal", "conformancetest"), t.TempDir())
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	cmd.WaitDelay = 5 * time.Second
	output, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "ok" {
		t.Fatalf("readiness policy regression: %v\n%s", err, output)
	}
}
