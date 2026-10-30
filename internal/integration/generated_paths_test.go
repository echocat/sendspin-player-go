package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitIgnoreContainsOnlyGeneratedVarTree(t *testing.T) {
	root := integrationRoot
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	// IDE metadata and private E2E device configuration are not generated-output
	// directories. Build/test artifacts still belong in the single var tree.
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		line = strings.TrimSpace(line)
		if (line != "/var/" && line != "/.idea/" && line != "/.env") || seen[line] {
			t.Fatal("generated outputs must not require scattered ignore patterns")
		}
		seen[line] = true
	}
	if !seen["/var/"] {
		t.Fatal("generated var tree is not ignored")
	}
}
