// Package devpath owns repository-local generated development paths.
// It is not used by the player runtime or credential storage.
package devpath

import (
	"os"
	"path/filepath"
	"runtime"
)

// Prepare keeps generated data in a nested module so SDK archives omit it.
func Prepare(root string) error {
	if err := os.MkdirAll(filepath.Join(root, "var"), 0700); err != nil {
		return err
	}
	path := filepath.Join(root, "var", "go.mod")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = f.WriteString("module github.com/echocat/sendspin-player-go/var\n")
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
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
