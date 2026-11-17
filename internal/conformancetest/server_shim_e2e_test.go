//go:build e2e

package conformancetest

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/internal/e2etest"
)

func TestE2EServerShimPreloadsBeforeOfficialAdapter(t *testing.T) {
	if testing.Short() {
		t.Skip("Python launch shim is disabled by -short")
	}
	root := e2etest.Root()
	python, err := e2etest.Python(official.ctx, root, "conformancetest")
	if err != nil {
		t.Fatal(err)
	}
	for _, failImport := range []bool{false, true} {
		name := "slow-cold-imports"
		if failImport {
			name = "import-failure-is-not-hidden"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			// Stand-ins exercise only the launch boundary, not protocol verdicts.
			// Each cold import exceeds the upstream clip's 250-ms lead budget.
			dependency := "import time\ntime.sleep(0.3)\n"
			if failImport {
				dependency = "raise ImportError('deliberate cold dependency failure')\n"
			}
			for path, content := range map[string]string{
				"av.py": dependency, "numpy.py": dependency,
				"conformance/__init__.py": "", "conformance/adapters/__init__.py": "",
				"conformance/adapters/aiosendspin_server.py": "import json, sys\nassert 'av' in sys.modules and 'numpy' in sys.modules\nassert __name__ == '__main__'\nprint(json.dumps(sys.argv[1:]))\n",
			} {
				path = filepath.Join(dir, filepath.FromSlash(path))
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"--server-name", "Server ä with spaces", "--port", "12345"}
			cmd := exec.CommandContext(official.ctx, python, append([]string{filepath.Join(root, "internal", "conformancetest", "sdk_server.py")}, args...)...)
			cmd.Env = append(os.Environ(), "PYTHONPATH="+dir, "PYTHONDONTWRITEBYTECODE=1")
			cmd.WaitDelay = 5 * time.Second
			output, err := cmd.CombinedOutput()
			if failImport {
				if err == nil || !strings.Contains(string(output), "deliberate cold dependency failure") {
					t.Fatalf("import failure was hidden: %v\n%s", err, output)
				}
				return
			}
			if err != nil {
				t.Fatalf("server launch shim: %v\n%s", err, output)
			}
			var got []string
			if err := json.Unmarshal(output, &got); err != nil || !slices.Equal(got, args) {
				t.Fatalf("shim changed arguments: %v\n%s", err, output)
			}
		})
	}
	t.Run("dynamic-port", func(t *testing.T) { testServerShimDynamicPorts(t, root, python) })
}

func testServerShimDynamicPorts(t *testing.T, root, python string) {
	for _, test := range []struct {
		name    string
		args    []string
		bad     bool
		badHost bool
	}{
		{"separate-flag", []string{"--port", "0"}, false, false},
		{"equals-flag", []string{"--port=0"}, false, false},
		{"reject-non-loopback-address", []string{"--port", "0"}, true, false},
		{"reject-non-loopback-bind", []string{"--port", "0", "--host", "0.0.0.0"}, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			// Exercise the pinned binding seam with a real ephemeral listener,
			// without running another protocol harness or reserving a free port.
			const server = `
import asyncio, os
from types import SimpleNamespace
class SendspinServer:
    async def start_server(self, *, port, host):
        assert port == 0 and host == "127.0.0.1"
        self.listener = await asyncio.start_server(lambda r, w: w.close(), host, port)
        addresses = [sock.getsockname() for sock in self.listener.sockets]
        if os.environ.get("SENDSPIN_TEST_BAD_BOUND_ADDRESS") == "1":
            addresses = [("0.0.0.0", addresses[0][1])]
        self._app_runner = SimpleNamespace(addresses=addresses)
`
			const adapter = `
import argparse, json, sys
from aiosendspin.server.server import SendspinServer
def build_parser():
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int)
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--client-name")
    return parser
async def _run(args):
    server = SendspinServer()
    try:
        await server.start_server(port=args.port, host=args.host)
        bound = server.listener.sockets[0].getsockname()[1]
        assert args.port == bound and 0 < bound <= 65535
        print(json.dumps({"port": args.port, "argv": sys.argv[1:]}))
        return 0
    finally:
        if hasattr(server, "listener"):
            server.listener.close()
            await server.listener.wait_closed()
`
			for path, content := range map[string]string{
				"av.py": "", "numpy.py": "",
				"aiosendspin/__init__.py": "", "aiosendspin/server/__init__.py": "",
				"aiosendspin/server/server.py": server,
				"conformance/__init__.py":      "", "conformance/adapters/__init__.py": "",
				"conformance/adapters/aiosendspin_server.py": adapter,
			} {
				path = filepath.Join(dir, filepath.FromSlash(path))
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			args := append(slices.Clone(test.args), "--client-name", "Client with spaces ä")
			cmd := exec.CommandContext(official.ctx, python, append([]string{filepath.Join(root, "internal", "conformancetest", "sdk_server.py")}, args...)...)
			cmd.Env = append(os.Environ(), "PYTHONPATH="+dir, "PYTHONDONTWRITEBYTECODE=1", "SENDSPIN_TEST_BAD_BOUND_ADDRESS=0")
			if test.bad {
				cmd.Env = append(cmd.Env, "SENDSPIN_TEST_BAD_BOUND_ADDRESS=1")
			}
			cmd.WaitDelay = 5 * time.Second
			output, err := cmd.CombinedOutput()
			if test.bad || test.badHost {
				want := "expected one bound loopback server address"
				if test.badHost {
					want = "dynamic port launch requires an explicit loopback bind"
				}
				if err == nil || !strings.Contains(string(output), want) {
					t.Fatalf("unexpected bound address accepted: %v\n%s", err, output)
				}
				return
			}
			if err != nil {
				t.Fatalf("dynamic port launch: %v\n%s", err, output)
			}
			var got struct {
				Port int      `json:"port"`
				Args []string `json:"argv"`
			}
			if err := json.Unmarshal(output, &got); err != nil || got.Port <= 0 || !slices.Equal(got.Args, args) {
				t.Fatalf("bound port/arguments not preserved: %v\n%s", err, output)
			}
		})
	}
}
