//go:build e2e

package audiotest

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/echocat/sendspin-player-go/internal/loopback"
)

func privateDirectory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// ReferenceWAV validates an optional user-supplied file without fallback, or
// generates the bounded synthetic reference in the test's private directory.
func ReferenceWAV(t *testing.T, dir string) string {
	t.Helper()
	path := os.Getenv("SENDSPIN_TEST_REFERENCE_WAV")
	if path == "" {
		path = filepath.Join(dir, "reference.wav")
		if err := loopback.Run([]string{"generate", "-out", path, "-seconds", "3", "-amplitude", "0.01"}, io.Discard); err != nil {
			t.Fatal("generate synthetic weak reference:", err)
		}
	}
	if _, err := loadWeakReference(path); err != nil {
		t.Fatal("invalid reference WAV; no fallback:", err)
	}
	return path
}
