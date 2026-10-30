//go:build e2e

package integration

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"
)

func TestE2EModuleArchive(t *testing.T) {
	root := integrationRoot
	if _, err := os.Stat(filepath.Join(root, ".git")); os.IsNotExist(err) {
		t.Skip("local archive validation requires a version-controlled checkout")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("local archive validation requires git")
	}
	m := module.Version{Path: "github.com/echocat/sendspin-player-go", Version: "v0.0.0"}
	var archive bytes.Buffer
	sourceFiles, err := trackedArchiveFiles(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := modzip.Create(&archive, m, sourceFiles); err != nil {
		t.Fatal(err)
	}
	r, err := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := make(map[string]bool)
	for _, entry := range r.File {
		files[strings.TrimPrefix(entry.Name, m.Path+"@"+m.Version+"/")] = true
		for _, forbidden := range []string{"/.git/", "/.venv/", "/_examples/", "/internal/e2e/", "/internal/conformancetest/", "/var/"} {
			if strings.Contains(entry.Name, forbidden) {
				t.Fatal("archive includes non-SDK assets:", entry.Name)
			}
		}
	}
	// Native selection and all platform backends are ordinary root-module
	// packages. Examples are independent applications with their own modules,
	// not part of the distributable SDK module.
	for _, required := range []string{
		"audio/native/output.go", "audio/native/select_linux.go", "audio/native/select_darwin.go", "audio/native/select_windows.go", "audio/native/select_other.go",
		"audio/alsa/output.go", "audio/pipewire/output.go", "audio/coreaudio/output.go", "audio/wasapi/output.go", "audio/noop/output.go",
		"internal/loopbackcmd/main.go",
		"internal/loopback/cli.go", "internal/loopback/analyze.go", "internal/loopback/signal.go", "internal/loopback/wave.go",
		"internal/e2etest/bootstrap.go", "internal/e2etest/runtime.go", "internal/e2etest/suite.go",
	} {
		if !files[required] {
			t.Error("archive omits root-module source:", required)
		}
	}
	archivePath := filepath.Join(t.TempDir(), "sdk.zip")
	if err = os.WriteFile(archivePath, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "module")
	if err = modzip.Unzip(dir, m, archivePath); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "./...")
	cmd.Dir = dir
	// The extracted module runs the ordinary suite, never the archive E2E
	// tests again (including when a caller supplied tags through GOFLAGS).
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=", "SENDSPIN_ARCHIVE_INNER=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("extracted module failed: %v\n%s", err, out)
	}
}
