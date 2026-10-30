package examples

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fixtureRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "go.mod"), "module "+sdkModule+"\n\ngo 1.27.3\n")
	for _, name := range []string{"z-last", "a-first"} {
		writeFixture(t, filepath.Join(root, "_examples", name, "go.mod"), "module fixture/"+name+"\n\ngo 1.27.3\n")
	}
	return root
}

func TestDiscover(t *testing.T) {
	root := fixtureRoot(t)
	writeFixture(t, filepath.Join(root, "_examples", "notes", "README"), "not a module")
	writeFixture(t, filepath.Join(root, "_examples", "notes", "nested", "go.mod"), "module fixture/nested\n")
	writeFixture(t, filepath.Join(root, "_examples", "file"), "not a directory")
	modules, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []Module{
		{Name: "a-first", Dir: filepath.Join(root, "_examples", "a-first")},
		{Name: "z-last", Dir: filepath.Join(root, "_examples", "z-last")},
	}
	if !reflect.DeepEqual(modules, want) {
		t.Fatalf("got %#v, want %#v", modules, want)
	}
}

func TestDirectoryRejectsPaths(t *testing.T) {
	root := fixtureRoot(t)
	for _, name := range []string{"", ".", "..", "../outside", "a-first/../z-last", "a-first/", `..\outside`, `C:\outside`, "/outside", "notes", "missing"} {
		t.Run(name, func(t *testing.T) {
			if dir, err := Directory(root, name); err == nil {
				t.Fatalf("accepted %q as %q", name, dir)
			}
		})
	}
	if dir, err := Directory(root, "a-first"); err != nil || dir != filepath.Join(root, "_examples", "a-first") {
		t.Fatalf("valid name: %q, %v", dir, err)
	}
}

func TestDiscoverDoesNotFollowSymlinks(t *testing.T) {
	root := fixtureRoot(t)
	outside := t.TempDir()
	writeFixture(t, filepath.Join(outside, "go.mod"), "module fixture/outside\n")
	if err := os.Symlink(outside, filepath.Join(root, "_examples", "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "_examples", "linked-manifest"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "go.mod"), filepath.Join(root, "_examples", "linked-manifest", "go.mod")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"escape", "linked-manifest"} {
		if _, err := Directory(root, name); err == nil {
			t.Fatalf("accepted symlink %q", name)
		}
	}
	linkedRoot := t.TempDir()
	if err := os.Symlink(filepath.Join(root, "_examples"), filepath.Join(linkedRoot, "_examples")); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(linkedRoot); err == nil {
		t.Fatal("accepted symlink _examples directory")
	}
}

func TestDiscoverRequiresOwnValidModule(t *testing.T) {
	for _, content := range []string{"go 1.27.3\n", "module " + sdkModule + "\n", "this is not a go.mod\n"} {
		root := fixtureRoot(t)
		writeFixture(t, filepath.Join(root, "_examples", "bad", "go.mod"), content)
		if _, err := Discover(root); err == nil {
			t.Fatalf("accepted invalid example manifest %q", content)
		}
	}
}

func TestFindRootFromNestedModule(t *testing.T) {
	root := fixtureRoot(t)
	start := filepath.Join(root, "_examples", "a-first", "nested")
	if err := os.Mkdir(start, 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := findRoot(start); err != nil || got != root {
		t.Fatalf("root = %q, %v; want %q", got, err, root)
	}
	if _, err := findRoot(t.TempDir()); err == nil {
		t.Fatal("found root in unrelated directory")
	}
}

func TestRootIndependentOfWorkingDirectory(t *testing.T) {
	want, err := Root()
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	got, err := Root()
	if err != nil || got != want {
		t.Fatalf("root from unrelated directory = %q, %v; want %q", got, err, want)
	}
}
