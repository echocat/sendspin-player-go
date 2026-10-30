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
	files := make(map[string]bool)
	for _, entry := range r.File {
		files[strings.TrimPrefix(entry.Name, m.Path+"@"+m.Version+"/")] = true
		for _, forbidden := range []string{"/.git/", "/.venv/", "/internal/e2e/", "/internal/conformancetest/", "/var/"} {
			if strings.Contains(entry.Name, forbidden) {
				t.Fatal("archive includes non-SDK assets:", entry.Name)
			}
		}
	}
	// Native selection and all platform backends are ordinary root-module
	// packages. Examples are public source, not private harness assets; ignore
	// directives and the leading underscore only affect package discovery.
	for _, required := range []string{
		"audio/native/output.go", "audio/native/select_linux.go", "audio/native/select_darwin.go", "audio/native/select_windows.go", "audio/native/select_other.go",
		"audio/alsa/output.go", "audio/pipewire/output.go", "audio/coreaudio/output.go", "audio/wasapi/output.go", "audio/noop/output.go",
		"_examples/player/main.go", "_examples/external-control/main.go", "internal/loopbackcmd/main.go",
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
	// ./... deliberately skips _examples. Build them explicitly from the
	// archive too, without writing binaries into the extracted module.
	cmd := exec.CommandContext(ctx, "go", "build", "./_examples/player", "./_examples/external-control")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("archived examples failed: %v\n%s", err, out)
	}
	cmd = exec.CommandContext(ctx, "go", "test", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "SENDSPIN_ARCHIVE_INNER=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("extracted module failed: %v\n%s", err, out)
	}
}
