// Package devpath owns repository-local generated development paths.
// It is not used by the player runtime or credential storage.
package devpath

import (
	"os"
	"path/filepath"
	"runtime"
)

// Prepare creates the project output directory. Package discovery excludes var
// through the root go.mod ignore directive; preparation creates no submodule.
func Prepare(root string) error {
	return os.MkdirAll(filepath.Join(root, "var"), 0700)
}

func Python(root, suite string) string {
	dir := filepath.Join(root, "var", "python", suite)
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, "Scripts", "python.exe")
	}
	return filepath.Join(dir, "bin", "python")
}

// PythonEnv isolates fixture environments and keeps bytecode out of source
// directories. System temporary paths and shared tool caches are inherited.
func PythonEnv(root, suite string) []string {
	return append(os.Environ(),
		"UV_PROJECT_ENVIRONMENT="+filepath.Join(root, "var", "python", suite),
		"PYTHONPYCACHEPREFIX="+filepath.Join(root, "var", "python", "pycache"),
	)
}
