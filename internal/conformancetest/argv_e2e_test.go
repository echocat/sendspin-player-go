//go:build e2e

package conformancetest

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/internal/e2etest"
)

func TestE2EClientShimArguments(t *testing.T) {
	if testing.Short() {
		t.Skip("Python launch shim is disabled by -short")
	}
	ctx := official.ctx
	root := e2etest.Root()
	python, err := e2etest.Python(ctx, root, "conformancetest")
	if err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(root, "internal", "conformancetest", "sdk_client.py")

	t.Run("argv-round-trip", func(t *testing.T) {
		// Use the Go test executable, not a second Python test program. Its
		// TestMain captures argv before flag parsing. Include argv[0] in the
		// comparison: an executable path containing spaces also needs quoting.
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(executable)
		if err != nil {
			t.Fatal(err)
		}
		binary := filepath.Join(t.TempDir(), "Go argument capture ä 東京")
		if runtime.GOOS == "windows" {
			binary += ".exe"
		}
		if err := os.WriteFile(binary, data, 0700); err != nil {
			t.Fatal(err)
		}
		args := []string{
			"--client-name", "Sendspin Conformance Client",
			"--server-name", "aiosendspin server",
			"--client-id", "client-ä-東京-🎵",
			"--metadata-artist", "",
			"--metadata-title", `literal "quote"`,
			"--metadata-album", `C:\music with spaces\`,
			"--metadata-album-artist", `backslash\"quote\\`,
			"--metadata-year", "tab\tvalue",
			"--summary", filepath.Join(t.TempDir(), "case with spaces", "summary.json"),
		}
		cmd := exec.CommandContext(ctx, python, append([]string{shim}, args...)...)
		cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1", "SENDSPIN_CONFORMANCE_CLIENT="+binary, "SENDSPIN_CONFORMANCE_CAPTURE_ARGS=1")
		cmd.WaitDelay = 5 * time.Second
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("run launch shim: %v\n%s", err, output)
		}
		var got []string
		if err := json.Unmarshal(output, &got); err != nil {
			t.Fatalf("decode Go argv capture: %v\n%s", err, output)
		}
		want := append([]string{binary}, args...)
		if !slices.Equal(got, want) {
			t.Fatalf("shim changed argv:\n got: %q\nwant: %q", got, want)
		}
	})

	t.Run("SDK-still-rejects-invalid-CLI", func(t *testing.T) {
		// Reuse the package's sole SDK build and official harness process.
		official.once.Do(func() { official.err = setupOfficial() })
		if official.err != nil {
			t.Fatal(official.err)
		}
		binary := filepath.Join(official.directory, "sdk-client")
		if runtime.GOOS == "windows" {
			binary += ".exe"
		}
		valid := []string{"--client-name", "Sendspin Conformance Client", "--client-id", "test-client", "--server-id", "test-server", "--summary", "summary.json", "--ready", "ready.json", "--registry", "registry.json"}
		for _, test := range []struct {
			name string
			args []string
		}{
			{"missing-required", []string{"--client-name", "Sendspin Conformance Client"}},
			{"positional", append(slices.Clone(valid), "unexpected")},
			{"empty-required", append(slices.Clone(valid), "--client-name", "")},
			{"invalid-timeout", append(slices.Clone(valid), "--timeout-seconds", "NaN")},
		} {
			t.Run(test.name, func(t *testing.T) {
				cmd := exec.CommandContext(ctx, binary, test.args...)
				cmd.Dir = t.TempDir()
				cmd.WaitDelay = 5 * time.Second
				output, err := cmd.CombinedOutput()
				if err == nil {
					t.Fatal("invalid adapter CLI succeeded")
				}
				if !strings.Contains(string(output), "invalid/incomplete adapter arguments") {
					t.Fatalf("missing SDK rejection: %v\n%s", err, output)
				}
				for _, name := range []string{"ready.json", "summary.json", "registry.json"} {
					if _, err := os.Stat(filepath.Join(cmd.Dir, name)); !os.IsNotExist(err) {
						t.Fatalf("invalid CLI created %s (stat: %v)", name, err)
					}
				}
			})
		}
	})
}
