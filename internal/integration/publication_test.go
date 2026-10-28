package integration

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
)

func TestPortAudioDependencyIsNotAnUnresolvableDevelopmentPlaceholder(t *testing.T) {
	if os.Getenv("SENDSPIN_ARCHIVE_INNER") == "1" {
		t.Skip("the distributable root module intentionally excludes the optional module")
	}
	_, here, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(here), "../.."))
	read := func(path string) *modfile.File {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		m, err := modfile.Parse(path, data, nil)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	sdk := read(filepath.Join(root, "go.mod"))
	backend := read(filepath.Join(root, "audio/portaudio/go.mod"))
	if backend.Module.Mod.Path != sdk.Module.Mod.Path+"/audio/portaudio" {
		t.Fatal("optional backend namespace differs from the SDK")
	}
	for _, requirement := range backend.Require {
		if requirement.Mod.Path == sdk.Module.Mod.Path {
			if requirement.Mod.Version == "v0.0.0" || !semver.IsValid(requirement.Mod.Version) {
				t.Fatal("a local replace must not hide an unresolvable consumer dependency")
			}
			return
		}
	}
	t.Fatal("optional backend has no explicit SDK requirement")
}
