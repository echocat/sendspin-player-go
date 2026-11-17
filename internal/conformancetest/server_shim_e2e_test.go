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
}
