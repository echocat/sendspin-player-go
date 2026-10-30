package integration

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"
)

type archiveSourceFile struct{ root, name string }

func (f archiveSourceFile) Path() string { return f.name }
func (f archiveSourceFile) Lstat() (os.FileInfo, error) {
	return os.Lstat(filepath.Join(f.root, filepath.FromSlash(f.name)))
}
func (f archiveSourceFile) Open() (io.ReadCloser, error) {
	return os.Open(filepath.Join(f.root, filepath.FromSlash(f.name)))
}

// Validate the working contents of version-controlled source files, matching
// the published VCS source set. Ignore directives alone do not filter zip files.
// Generated/untracked artifacts are neither enumerated nor opened. modzip.Create
// still applies its normal nested-module, vendor, filename and symlink rules.
// Application examples and non-Go reference harnesses are excluded explicitly;
// the SDK needs no nested-module markers to express that packaging boundary.
func trackedArchiveFiles(parent context.Context, root string) ([]modzip.File, error) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "ls-files", "--cached", "-z", "--")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list version-controlled archive inputs: %w", err)
	}
	var files []modzip.File
	seen := make(map[string]bool)
	for raw := range bytes.SplitSeq(out, []byte{0}) {
		name := string(raw)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if path.IsAbs(name) || path.Clean(name) != name || strings.HasPrefix(name, "../") || strings.Contains(name, "\\") {
			return nil, fmt.Errorf("invalid version-controlled archive path: %q", name)
		}
		if name == "var" || strings.HasPrefix(name, "var/") {
			return nil, fmt.Errorf("generated var path must not be version-controlled: %q", name)
		}
		if name == "_examples" || strings.HasPrefix(name, "_examples/") || strings.HasPrefix(name, "internal/e2e/") || strings.HasPrefix(name, "internal/conformancetest/") {
			continue
		}
		file := archiveSourceFile{root, name}
		info, err := file.Lstat()
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			continue
		} // Git submodule entries are not root-module files.
		files = append(files, file)
	}
	return files, nil
}

func TestTrackedArchiveExcludesGeneratedAndUntrackedFilesWithoutModuleMarker(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write("go.mod", "module example.test/archive\n\ngo 1.27.3\n\nignore (\n var\n)\n")
	write(".gitignore", "/var/\n")
	write("sdk.go", "package archive\n")
	write("_examples/player/go.mod", "module example.test/player\n")
	write("_examples/player/main.go", "package main\nfunc main(){}\n")
	write("internal/e2e/fixture.py", "# versioned reference harness, no Go module marker\n")
	write("internal/conformancetest/fixture.py", "# versioned conformance harness, no Go module marker\n")
	git("init", "--quiet")
	git("add", "--", "go.mod", ".gitignore", "sdk.go", "_examples", "internal")
	write("var/FAKE-private-test-data.json", "synthetic data, never a credential\n")
	write("var/generated.go", "not valid Go; excluded from discovery\n")
	write("scratch/untracked.json", "synthetic untracked artifact\n")
	if _, err := os.Stat(filepath.Join(root, "var", "go.mod")); !os.IsNotExist(err) {
		t.Fatal("fixture unexpectedly uses a var module marker")
	}
	files, err := trackedArchiveFiles(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	m := module.Version{Path: "example.test/archive", Version: "v0.0.0"}
	if err = modzip.Create(&b, m, files); err != nil {
		t.Fatal(err)
	}
	r, err := zip.NewReader(bytes.NewReader(b.Bytes()), int64(b.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.File) != 3 {
		t.Fatal("archive contains non-source artifacts", r.File)
	}
	for _, entry := range r.File {
		name := strings.TrimPrefix(entry.Name, m.Path+"@"+m.Version+"/")
		if name != "sdk.go" && name != "go.mod" && name != ".gitignore" {
			t.Fatal("private/generated/harness data entered archive", name)
		}
	}
	cmd := exec.Command("go", "list", "./...")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "example.test/archive" {
		t.Fatalf("ignore var did not protect package discovery: %v\n%s", err, out)
	}
	git("add", "--force", "--", "var/FAKE-private-test-data.json")
	if _, err = trackedArchiveFiles(context.Background(), root); err == nil {
		t.Fatal("accidentally version-controlled private var data was accepted")
	}
}
