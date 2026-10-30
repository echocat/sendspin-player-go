// Package examples locates and operates on the independent example modules.
package examples

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/mod/modfile"
)

const sdkModule = "github.com/echocat/sendspin-player-go"

// Module is an immediate, real module directory below _examples.
type Module struct {
	Name string
	Dir  string
}

// Root locates the SDK checkout independently of the current working directory.
// Walking from the working directory also supports binaries built with -trimpath.
func Root() (string, error) {
	var starts []string
	if _, file, _, ok := runtime.Caller(0); ok && filepath.IsAbs(file) {
		starts = append(starts, filepath.Dir(file))
	}
	if cwd, err := os.Getwd(); err == nil {
		starts = append(starts, cwd)
	}
	if executable, err := os.Executable(); err == nil {
		starts = append(starts, filepath.Dir(executable))
	}
	for _, start := range starts {
		if root, err := findRoot(start); err == nil {
			return root, nil
		}
	}
	return "", fmt.Errorf("cannot locate SDK root (%s)", sdkModule)
}

func findRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && modfile.ModulePath(data) == sdkModule {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no SDK root above %q", start)
		}
		dir = parent
	}
}

// Discover returns modules sorted by directory name. Symlink directories and
// symlink go.mod files are deliberately excluded, not followed outside the tree.
func Discover(root string) ([]Module, error) {
	base := filepath.Join(root, "_examples")
	info, err := os.Lstat(base)
	if err != nil {
		return nil, fmt.Errorf("discover examples: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("examples directory is not a real directory: %s", base)
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, err
	}
	modules := make([]Module, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(base, entry.Name())
		manifest := filepath.Join(dir, "go.mod")
		info, err := os.Lstat(manifest)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(manifest)
		if err != nil {
			return nil, err
		}
		mod, err := modfile.Parse(manifest, data, nil)
		if err != nil {
			return nil, err
		}
		if mod.Module == nil || mod.Module.Mod.Path == sdkModule {
			return nil, fmt.Errorf("example %q must declare its own Go module", entry.Name())
		}
		modules = append(modules, Module{Name: entry.Name(), Dir: dir})
	}
	return modules, nil
}

// Directory only accepts exact discovered names, never arbitrary paths.
func Directory(root, name string) (string, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\\:`) || filepath.IsAbs(name) {
		return "", fmt.Errorf("invalid example name %q; use list to see available examples", name)
	}
	modules, err := Discover(root)
	if err != nil {
		return "", err
	}
	for _, module := range modules {
		if module.Name == name {
			return module.Dir, nil
		}
	}
	return "", fmt.Errorf("unknown example %q; use list to see available examples", name)
}
