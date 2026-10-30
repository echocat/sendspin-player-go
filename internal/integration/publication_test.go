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

func TestRootSDKHasNoSubmodules(t *testing.T) {
	_, here, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(here), "../.."))
	sdk := readModule(t, filepath.Join(root, "go.mod"))
	for _, requirement := range sdk.Require {
		if requirement.Mod.Path == sdk.Module.Mod.Path || strings.HasPrefix(requirement.Mod.Path, sdk.Module.Mod.Path+"/") {
			t.Fatal("root module must not depend on a separately versioned SDK package", requirement.Mod)
		}
		if requirement.Mod.Path == "github.com/echocat/slf4g/native" {
			t.Fatal("logging providers belong to applications, not the SDK")
		}
	}
	for _, tool := range sdk.Tool {
		if strings.HasPrefix(tool.Path, sdk.Module.Mod.Path+"/_examples/") {
			t.Fatal("example applications must not be root-module tools", tool.Path)
		}
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			// Generated assets, virtual environments and VCS/IDE metadata are
			// not SDK sources. Harness sources still must not contain modules.
			if path != root && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "var" || entry.Name() == "vendor" || entry.Name() == "venv" || entry.Name() == "__pycache__") {
				return filepath.SkipDir
			}
		} else if entry.Name() == "go.mod" && path != filepath.Join(root, "go.mod") {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			switch filepath.ToSlash(rel) {
			case "_examples/player/go.mod", "_examples/external-control/go.mod":
				return nil
			}
			t.Errorf("only the two example applications may have separate modules: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestExamplesHaveIndependentApplicationModules(t *testing.T) {
	if os.Getenv("SENDSPIN_ARCHIVE_INNER") == "1" {
		t.Skip("example modules are deliberately absent from the SDK module zip")
	}
	_, here, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(here), "../.."))
	if _, err := os.Stat(filepath.Join(root, "_examples")); os.IsNotExist(err) {
		t.Skip("example module validation requires checkout sources")
	} else if err != nil {
		t.Fatal(err)
	}
	sdk := readModule(t, filepath.Join(root, "go.mod"))
	for _, dir := range []string{"_examples/player", "_examples/external-control"} {
		t.Run(dir, func(t *testing.T) {
			application := readModule(t, filepath.Join(root, dir, "go.mod"))
			if application.Module.Mod.Path != sdk.Module.Mod.Path+"/"+dir {
				t.Fatal("unexpected example module namespace")
			}
			if application.Go.Version != sdk.Go.Version {
				t.Fatal("examples must use the root Go version")
			}
			var requiresSDK, requiresProvider bool
			for _, requirement := range application.Require {
				if requirement.Mod.Path == sdk.Module.Mod.Path {
					requiresSDK = true
					if requirement.Indirect || requirement.Mod.Version == "v0.0.0" {
						t.Error("examples must directly require a real SDK release")
					}
				} else if strings.HasPrefix(requirement.Mod.Path, sdk.Module.Mod.Path+"/") {
					t.Error("SDK packages must not be separate module dependencies:", requirement.Mod)
				}
				if requirement.Mod.Path == "github.com/echocat/slf4g/native" {
					requiresProvider = !requirement.Indirect
				}
			}
			if !requiresSDK {
				t.Error("example must depend on the SDK")
			}
			if requiresProvider != (dir == "_examples/player") {
				t.Error("only the player example should own a logging provider")
			}
			if len(application.Replace) != 1 || application.Replace[0].Old.Path != sdk.Module.Mod.Path || application.Replace[0].Old.Version != "" || application.Replace[0].New.Path != "../.." || application.Replace[0].New.Version != "" {
				t.Error("examples must replace only the SDK with the local checkout")
			}
			if _, err := os.Stat(filepath.Join(root, dir, "go.sum")); err != nil {
				t.Fatal("example must own its dependency checksums:", err)
			}
		})
	}
}

func TestPythonHarnessesHaveNoModuleMarkers(t *testing.T) {
	_, here, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(here), "../.."))
	for _, dir := range []string{"internal/e2e", "internal/conformancetest"} {
		for _, name := range []string{"go.mod", "go.sum"} {
			path := filepath.Join(root, dir, name)
			if _, err := os.Stat(path); err == nil {
				t.Error("Python harness must not have a Go module marker:", path)
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
		}
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
