package integration

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/echocat/sendspin-player-go/internal/examples"
	"golang.org/x/mod/modfile"
)

func TestDependabotMatchesReferencePolicy(t *testing.T) {
	if os.Getenv("SENDSPIN_ARCHIVE_INNER") == "1" {
		t.Skip("GitHub configuration is not part of the published SDK")
	}
	root := integrationRoot
	data, err := os.ReadFile(filepath.Join(root, ".github", "dependabot.yml"))
	if err != nil {
		t.Fatal(err)
	}
	// Pin the cli4g policy, adapting the module name and using the root workspace.
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
    directory: "/"
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

func TestDependabotWorkspaceCoversAllModules(t *testing.T) {
	if os.Getenv("SENDSPIN_ARCHIVE_INNER") == "1" {
		t.Skip("dependency update configuration requires checkout sources")
	}
	name := filepath.Join(integrationRoot, "go.work")
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := modfile.ParseWork(name, data, nil)
	if err != nil {
		t.Fatal(err)
	}
	sdk := readModule(t, filepath.Join(integrationRoot, "go.mod"))
	if workspace.Go == nil || workspace.Go.Version != sdk.Go.Version {
		t.Fatal("dependency update workspace must use the SDK's Go version")
	}
	if len(workspace.Replace) != 0 {
		t.Fatal("workspace must not override independent module dependencies")
	}
	modules, err := examples.Discover(integrationRoot)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"."}
	for _, module := range modules {
		want = append(want, "_examples/"+module.Name)
	}
	var got []string
	for _, use := range workspace.Use {
		got = append(got, filepath.ToSlash(filepath.Clean(use.Path)))
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("go.work must cover exactly the SDK and all discovered examples: got %v, want %v", got, want)
	}
}

func TestMiseDisablesWorkspaceForIndependentModuleChecks(t *testing.T) {
	if os.Getenv("SENDSPIN_ARCHIVE_INNER") == "1" {
		t.Skip("mise configuration requires checkout sources")
	}
	data, err := os.ReadFile(filepath.Join(integrationRoot, "mise.toml"))
	if err != nil {
		t.Fatal(err)
	}
	// Keep this global: all SDK tasks, not just example child processes, must
	// validate the module's own graph rather than the workspace's merged graph.
	config := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.Contains(config, "\n[env]\nGOWORK = \"off\"\n") {
		t.Fatal("mise must globally disable workspace mode for independent module checks")
	}
}
