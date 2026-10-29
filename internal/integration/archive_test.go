package integration

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"
)

func TestModuleArchive(t *testing.T) {
	if os.Getenv("SENDSPIN_ARCHIVE_INNER") == "1" {
		t.Skip("already executing the extracted archive")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate repository")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	m := module.Version{Path: "github.com/echocat/sendspin-player-go", Version: "v0.0.0"}
	var archive bytes.Buffer
	if err := modzip.CreateFromDir(&archive, m, root); err != nil {
		t.Fatal(err)
	}
	r, err := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range r.File {
		for _, forbidden := range []string{"/.git/", "/.venv/", "/_examples/", "/audio/native/", "/internal/e2e/", "/internal/conformancetest/", "/var/"} {
			if strings.Contains(entry.Name, forbidden) {
				t.Fatal("archive includes non-SDK assets:", entry.Name)
			}
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
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "SENDSPIN_ARCHIVE_INNER=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("extracted module failed: %v\n%s", err, out)
	}
}
