package integration

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
)

func TestSDKAndExamplesShareTheRootModule(t *testing.T) {
	_, here, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(here), "../.."))
	sdk := readModule(t, filepath.Join(root, "go.mod"))
	for _, requirement := range sdk.Require {
		if requirement.Mod.Path == sdk.Module.Mod.Path || strings.HasPrefix(requirement.Mod.Path, sdk.Module.Mod.Path+"/") {
			t.Fatal("root module must not depend on a separately versioned SDK package", requirement.Mod)
		}
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			// Generated assets and VCS/IDE metadata are not SDK sources. The two
			// Python harness boundaries are checked separately below.
			if path != root && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "var" || entry.Name() == "vendor") {
				return filepath.SkipDir
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			switch filepath.ToSlash(rel) {
			case "internal/e2e", "internal/conformancetest":
				return filepath.SkipDir
			}
		} else if entry.Name() == "go.mod" && path != filepath.Join(root, "go.mod") {
			t.Errorf("SDK packages, tools and examples must use the root module: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPythonHarnessBoundariesAreDependencyFree(t *testing.T) {
	if os.Getenv("SENDSPIN_ARCHIVE_INNER") == "1" {
		t.Skip("Python harnesses are deliberately absent from the module zip")
	}
	_, here, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(here), "../.."))
	sdk := readModule(t, filepath.Join(root, "go.mod"))
	for _, dir := range []string{"internal/e2e", "internal/conformancetest"} {
		t.Run(dir, func(t *testing.T) {
			marker := readModule(t, filepath.Join(root, dir, "go.mod"))
			if marker.Module == nil || marker.Module.Mod.Path != sdk.Module.Mod.Path+"/"+dir {
				t.Fatal("unexpected Python harness namespace")
			}
			if marker.Go == nil || marker.Go.Version != sdk.Go.Version {
				t.Fatal("Python harness marker must use the root Go version")
			}
			if len(marker.Require)+len(marker.Replace)+len(marker.Exclude)+len(marker.Tool) != 0 {
				t.Fatal("Python harness markers are privacy boundaries, not Go implementation modules")
			}
		})
	}
}

func readModule(t *testing.T, path string) *modfile.File {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m, err := modfile.Parse(path, data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Module == nil || m.Go == nil {
		t.Fatal("module and Go version must be explicit:", path)
	}
	return m
}
