package dynlib

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestExplicitMissingLibraryNeverUsesDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-explicit-library")
	if _, err := Open(path, "a-default-that-must-not-be-tried"); err == nil || !strings.Contains(err.Error(), strconv.Quote(path)) || strings.Contains(err.Error(), "a-default-that-must-not-be-tried") {
		t.Fatal("explicit library selection was not strict", err)
	}
	if _, err := Open("bad\x00path", ""); err == nil {
		t.Fatal("NUL path accepted")
	}
}
