package integration

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGitIgnoreContainsOnlyGeneratedVarTree(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate sources")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	// The developer's IDE exclusion is not a generated-output directory. Keep
	// that repository policy without allowing build/test artifacts elsewhere.
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		line = strings.TrimSpace(line)
		if (line != "/var/" && line != "/.idea/") || seen[line] {
			t.Fatal("generated outputs must not require scattered ignore patterns")
		}
		seen[line] = true
	}
	if !seen["/var/"] {
		t.Fatal("generated var tree is not ignored")
	}
}
