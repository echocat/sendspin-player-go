package specaudit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/mod/modfile"
)

type Test struct{ ID, Name, Package, File, Suite string }
type Inventory struct {
	Tests map[string]Test
	Files map[string]bool
}

func TestID(suite, pkg, name string) string { return suite + ":" + pkg + "#" + name }

// Discover deliberately scans test declarations, not test-name mentions in
// prose. Build-tagged declarations remain visible; suite validation prevents an
// e2e test or fuzz campaign from being credited as an ordinary unit-test pass.
func Discover(root string) (Inventory, error) {
	result := Inventory{Tests: map[string]Test{}, Files: map[string]bool{}}
	modules := map[string]string{}
	err := walkInputs(root, func(relative string, data []byte) error {
		result.Files[relative] = true
		if path.Base(relative) != "go.mod" {
			return nil
		}
		file, err := modfile.Parse(relative, data, nil)
		if err != nil || file.Module == nil {
			return fmt.Errorf("invalid source module %s", relative)
		}
		modules[path.Dir(relative)] = file.Module.Mod.Path
		return nil
	})
	if err != nil {
		return result, err
	}
	err = walkInputs(root, func(relative string, data []byte) error {
		if !strings.HasSuffix(relative, "_test.go") {
			return nil
		}
		directory := path.Dir(relative)
		moduleRoot := directory
		for modules[moduleRoot] == "" && moduleRoot != "." {
			moduleRoot = path.Dir(moduleRoot)
		}
		module := modules[moduleRoot]
		if module == "" {
			return fmt.Errorf("test outside a known module: %s", relative)
		}
		suffix := strings.TrimPrefix(directory, moduleRoot+"/")
		if moduleRoot == "." {
			suffix = directory
		}
		pkg := module
		if suffix != "." && suffix != moduleRoot {
			pkg += "/" + suffix
		}
		file, err := parser.ParseFile(token.NewFileSet(), relative, data, parser.ParseComments)
		if err != nil {
			return err
		}
		testingAliases := map[string]bool{}
		for _, imp := range file.Imports {
			if imp.Path.Value == `"testing"` {
				name := "testing"
				if imp.Name != nil {
					name = imp.Name.Name
				}
				testingAliases[name] = true
			}
		}
		header := ""
		for _, group := range file.Comments {
			if group.End() < file.Package {
				for _, comment := range group.List {
					if strings.HasPrefix(comment.Text, "//go:build ") {
						header = comment.Text
					}
				}
			}
		}
		var expression constraint.Expr
		if header != "" {
			expression, err = constraint.Parse(header)
			if err != nil {
				return err
			}
		}
		ordinary, e2e := buildActive(expression, false), buildActive(expression, true)
		for _, declaration := range file.Decls {
			fn, ok := declaration.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Type.TypeParams != nil || fn.Type.Params == nil || len(fn.Type.Params.List) != 1 || fn.Type.Results != nil {
				continue
			}
			parameter, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			testingType := ""
			switch typ := parameter.X.(type) {
			case *ast.SelectorExpr:
				if alias, ok := typ.X.(*ast.Ident); ok && testingAliases[alias.Name] {
					testingType = typ.Sel.Name
				}
			case *ast.Ident:
				if testingAliases["."] {
					testingType = typ.Name
				}
			}
			if testingType == "" {
				continue
			}
			name := fn.Name.Name
			if !(strings.HasPrefix(name, "Test") && testingType == "T") && !(strings.HasPrefix(name, "Fuzz") && testingType == "F") {
				continue
			}
			prefix := 4 // Test and Fuzz both have a four-character prefix.
			if len(name) > prefix {
				next, _ := utf8.DecodeRuneInString(name[prefix:])
				if unicode.IsLower(next) {
					continue
				}
			}
			if len(fn.Type.Params.List[0].Names) > 1 {
				continue
			}
			var suite string
			if strings.HasPrefix(name, "TestE2E") && ordinary {
				return fmt.Errorf("E2E test is active without the e2e tag: %s#%s", relative, name)
			}
			if !ordinary && e2e && !strings.HasPrefix(name, "TestE2E") {
				return fmt.Errorf("E2E test must start with TestE2E: %s#%s", relative, name)
			}
			switch {
			case strings.HasPrefix(name, "Fuzz"):
				suite = "fuzz"
			case !ordinary && !e2e:
				return fmt.Errorf("test does not match ordinary Go tests or the e2e tag: %s", relative)
			case !ordinary && directory == "internal/mdnstest":
				suite = "mdns"
			case !ordinary && directory == "internal/conformancetest":
				suite = "harness"
			case !ordinary && relative == "internal/timesync/reference_live_test.go":
				suite = "reference"
			case directory == "audio/alsa" || strings.HasPrefix(directory, "audio/alsa/"):
				suite = "alsa"
				if !ordinary && e2e {
					suite = "alsa-e2e"
				}
			case directory == "audio/native" || strings.HasPrefix(directory, "audio/internal/") || slices.Contains([]string{"audio/pipewire", "audio/coreaudio", "audio/wasapi"}, directory):
				suite = "desktop-audio"
				if !ordinary && e2e {
					suite = "desktop-audio-e2e"
				}
			case strings.HasPrefix(directory, "_examples/"):
				suite = "examples"
			case ordinary:
				suite = "unit"
			case e2e:
				suite = "e2e"
			default:
				return fmt.Errorf("test does not match a known opt-in suite: %s", relative)
			}
			id := TestID(suite, pkg, name)
			if _, exists := result.Tests[id]; exists {
				return fmt.Errorf("ambiguous test declaration %s", id)
			}
			result.Tests[id] = Test{ID: id, Name: name, Package: pkg, File: relative, Suite: suite}
		}
		return nil
	})
	return result, err
}

// Evaluate each platform separately: treating all platform tags as true loses
// !linux/!amd64 alternatives and accepts impossible linux && windows expressions.
// Only e2e is an opt-in tag; obsolete/custom tags are never implicitly enabled.
func buildActive(expression constraint.Expr, e2e bool) bool {
	if expression == nil {
		return true
	}
	for _, platform := range strings.Fields(`
		aix/ppc64 android/386 android/amd64 android/arm android/arm64
		darwin/amd64 darwin/arm64 dragonfly/amd64
		freebsd/386 freebsd/amd64 freebsd/arm freebsd/arm64 illumos/amd64
		ios/amd64 ios/arm64 js/wasm
		linux/386 linux/amd64 linux/arm linux/arm64 linux/loong64
		linux/mips linux/mips64 linux/mips64le linux/mipsle
		linux/ppc64 linux/ppc64le linux/riscv64 linux/s390x
		netbsd/386 netbsd/amd64 netbsd/arm netbsd/arm64
		openbsd/386 openbsd/amd64 openbsd/arm openbsd/arm64 openbsd/ppc64 openbsd/riscv64
		plan9/386 plan9/amd64 plan9/arm solaris/amd64 wasip1/wasm
		windows/386 windows/amd64 windows/arm64
	`) {
		goos, goarch, _ := strings.Cut(platform, "/")
		if expression.Eval(func(tag string) bool {
			switch tag {
			case "e2e":
				return e2e
			case "unix":
				return !slices.Contains([]string{"js", "plan9", "wasip1", "windows"}, goos)
			case "linux":
				return goos == "linux" || goos == "android"
			case "darwin":
				return goos == "darwin" || goos == "ios"
			case "solaris":
				return goos == "solaris" || goos == "illumos"
			default:
				return tag == goos || tag == goarch
			}
		}) {
			return true
		}
	}
	return false
}

// Fingerprint includes the catalog, test/implementation inputs, fixtures,
// lockfiles and CI/task definitions. It excludes generated data and IDE/secrets.
// Normalize text line endings so identical Windows/Linux checkouts correlate.
func Fingerprint(root string) (string, error) {
	hash := sha256.New()
	err := walkInputs(root, func(relative string, data []byte) error {
		if slices.Contains([]string{".go", ".md", ".json", ".toml", ".yaml", ".yml", ".py", ".c", ".cpp", ".h", ".sh", ".ps1", ".txt", ".lock"}, path.Ext(relative)) || slices.Contains([]string{"go.mod", "go.sum", ".python-version", ".gitattributes", ".gitignore", "LICENSE"}, path.Base(relative)) {
			data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
		}
		_, _ = fmt.Fprintf(hash, "%s\x00%d\x00", relative, len(data))
		_, _ = hash.Write(data)
		return nil
	})
	return hex.EncodeToString(hash.Sum(nil)), err
}

func walkInputs(root string, visit func(string, []byte) error) error {
	return filepath.WalkDir(root, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, filename)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "." {
			return nil
		}
		parts := strings.Split(relative, "/")
		if entry.IsDir() {
			if slices.Contains([]string{"var", ".git", ".idea", ".venv", "__pycache__", "node_modules"}, entry.Name()) {
				return filepath.SkipDir
			}
			if len(parts) == 1 && !slices.Contains([]string{"audio", "codec", "diagnostic", "discovery", "internal", "_examples", "docs", "mise-tasks", "scripts", ".github", "LICENSES"}, parts[0]) {
				return filepath.SkipDir
			}
			return nil
		}
		miseInput := strings.HasPrefix(relative, "mise.") && slices.Contains([]string{".toml", ".lock"}, path.Ext(relative))
		if len(parts) == 1 && !strings.HasSuffix(relative, ".go") && !miseInput && !slices.Contains([]string{"go.mod", "go.sum", "README.md", "NOTICES.md", "LICENSE", ".gitattributes", ".gitignore", ".golangci.yaml"}, relative) {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("cannot fingerprint a source symlink: %s", relative)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("non-regular source input: %s", relative)
		}
		data, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		return visit(relative, data)
	})
}
