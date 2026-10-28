package devpath

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareAndSuitePathsStayInsideVar(t *testing.T) {
	root := t.TempDir()
	if err := Prepare(root); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "var"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "go.mod" {
		t.Fatal("preparation must only create the module boundary", entries, err)
	}
	marker := filepath.Join(root, "var", "go.mod")
	before, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err = Prepare(root); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(marker)
	if err != nil || string(before) != string(after) {
		t.Fatal("preparation rewrote module boundary", err)
	}
	if Python(root, "e2e") == Python(root, "conformance") {
		t.Fatal("test suites share a Python environment")
	}
	for _, suite := range []string{"e2e", "conformance"} {
		if relative, err := filepath.Rel(filepath.Join(root, "var"), Python(root, suite)); err != nil || strings.HasPrefix(relative, "..") {
			t.Fatal("Python escaped var", relative, err)
		}
		settings := map[string]string{}
		for _, entry := range PythonEnv(root, suite) {
			key, value, ok := strings.Cut(entry, "=")
			if ok {
				settings[key] = value
			}
		}
		for _, key := range []string{"UV_PROJECT_ENVIRONMENT", "PYTHONPYCACHEPREFIX"} {
			relative, err := filepath.Rel(filepath.Join(root, "var"), settings[key])
			if err != nil || strings.HasPrefix(relative, "..") {
				t.Fatal("child path escaped var", key, relative, err)
			}
		}
	}
}

func TestPythonEnvPreservesSystemTempAndSharedCaches(t *testing.T) {
	keys := []string{"GOTMPDIR", "TMPDIR", "TEMP", "TMP", "GOCACHE", "GOMODCACHE", "GOLANGCI_LINT_CACHE", "UV_CACHE_DIR"}
	for _, inherited := range []bool{true, false} {
		name := "unset"
		if inherited {
			name = "inherited"
		}
		t.Run(name, func(t *testing.T) {
			for _, key := range keys {
				t.Setenv(key, "user-selected-"+key)
				if !inherited {
					if err := os.Unsetenv(key); err != nil {
						t.Fatal(err)
					}
				}
			}
			settings := map[string]string{}
			for _, entry := range PythonEnv("project", "e2e") {
				key, value, ok := strings.Cut(entry, "=")
				if ok {
					settings[key] = value
				}
			}
			for _, key := range keys {
				value, exists := settings[key]
				if exists != inherited || (inherited && value != "user-selected-"+key) {
					t.Fatalf("changed %s: %q (present: %v)", key, value, exists)
				}
			}
		})
	}
}
