package integration

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDependabotMatchesReferencePolicy(t *testing.T) {
	if os.Getenv("SENDSPIN_ARCHIVE_INNER") == "1" {
		t.Skip("GitHub configuration is not part of the published SDK")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate repository")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	data, err := os.ReadFile(filepath.Join(root, ".github", "dependabot.yml"))
	if err != nil {
		t.Fatal(err)
	}
	// Pin the cli4g policy, adapting only the module name and existing directories.
	// A static snapshot avoids a YAML dependency and still checks indentation,
	// grouping, and the absence of additional ecosystems or update policies.
	const want = `version: 2
updates:
  - package-ecosystem: "github-actions"
    directory: "/"
    schedule:
      interval: "daily"
    groups:
      codeql-actions:
        applies-to: "version-updates"
        patterns:
          - "github/codeql-action/*"
      codeql-actions-security:
        applies-to: "security-updates"
        patterns:
          - "github/codeql-action/*"
  - package-ecosystem: "gomod"
    directories:
      - "/"
      - "/_examples/*"
    schedule:
      interval: "daily"
    allow:
      - dependency-type: "all"
    ignore:
      - dependency-name: "github.com/echocat/sendspin-player-go"
    groups:
      go-modules:
        applies-to: "version-updates"
        group-by: "dependency-name"
        patterns:
          - "*"`
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, " \t\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		lines = append(lines, line)
	}
	if got := strings.Join(lines, "\n"); got != want {
		t.Fatalf("Dependabot policy differs from the adapted cli4g reference\ngot:\n%s\nwant:\n%s", got, want)
	}
}
