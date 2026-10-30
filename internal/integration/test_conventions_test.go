package integration

import (
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

type conventionFile struct {
	name      string
	build     constraint.Expr
	tests     []string
	mainCount int
}

// Check source declarations, not text matches: comments, raw strings and methods
// may discuss test names without declaring a runnable Go test.
func TestGoTestConventions(t *testing.T) {
	var files []conventionFile
	err := filepath.WalkDir(integrationRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".venv", "var", "vendor", "venv", "__pycache__":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		file, err := parseConventionFile(path, nil)
		if err != nil {
			return err
		}
		file.name, err = filepath.Rel(integrationRoot, path)
		if err != nil {
			return err
		}
		files = append(files, file)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range conventionProblems(files) {
		t.Error(problem)
	}
}

func parseConventionFile(name string, source any) (conventionFile, error) {
	file := conventionFile{name: name}
	f, err := parser.ParseFile(token.NewFileSet(), name, source, parser.ParseComments)
	if err != nil {
		return file, err
	}
	var legacy constraint.Expr
	for _, group := range f.Comments {
		if group.Pos() >= f.Package {
			break
		}
		for _, comment := range group.List {
			switch {
			case constraint.IsGoBuild(comment.Text):
				if file.build != nil {
					return file, fmt.Errorf("%s: multiple go:build directives", name)
				}
				file.build, err = constraint.Parse(comment.Text)
			case constraint.IsPlusBuild(comment.Text):
				var expr constraint.Expr
				expr, err = constraint.Parse(comment.Text)
				if legacy == nil {
					legacy = expr
				} else {
					legacy = &constraint.AndExpr{X: legacy, Y: expr}
				}
			}
			if err != nil {
				return file, fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	if file.build == nil {
		file.build = legacy
	}
	aliases := map[string]bool{}
	for _, imported := range f.Imports {
		path, err := strconv.Unquote(imported.Path.Value)
		if err != nil || path != "testing" {
			continue
		}
		alias := "testing"
		if imported.Name != nil {
			alias = imported.Name.Name
		}
		aliases[alias] = true
	}
	for _, declaration := range f.Decls {
		fn, ok := declaration.(*ast.FuncDecl)
		if !ok || fn.Recv != nil {
			continue
		}
		if fn.Name.Name == "TestMain" && testingSignature(fn.Type, aliases, "M") {
			file.mainCount++
			continue
		}
		if isGoTestName(fn.Name.Name) && testingSignature(fn.Type, aliases, "T") {
			file.tests = append(file.tests, fn.Name.Name)
		}
	}
	return file, nil
}

func isGoTestName(name string) bool {
	if !strings.HasPrefix(name, "Test") {
		return false
	}
	for _, next := range strings.TrimPrefix(name, "Test") {
		return !unicode.IsLower(next)
	}
	return true
}

func testingSignature(fn *ast.FuncType, aliases map[string]bool, parameter string) bool {
	if fn.TypeParams != nil || fn.Results != nil && len(fn.Results.List) != 0 || fn.Params == nil || len(fn.Params.List) != 1 {
		return false
	}
	field := fn.Params.List[0]
	if len(field.Names) > 1 {
		return false
	}
	pointer, ok := field.Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	switch typ := pointer.X.(type) {
	case *ast.SelectorExpr:
		qualifier, ok := typ.X.(*ast.Ident)
		return ok && aliases[qualifier.Name] && typ.Sel.Name == parameter
	case *ast.Ident:
		return aliases["."] && typ.Name == parameter
	}
	return false
}

func conventionProblems(files []conventionFile) []string {
	var problems []string
	mains := map[string][]conventionFile{}
	for _, file := range files {
		tags := map[string]bool{}
		collectConstraintTags(file.build, tags)
		for tag := range tags {
			if tag != "e2e" && tag != "unix" && !knownOS(tag) && !knownArch(tag) {
				problems = append(problems, fmt.Sprintf("%s: unsupported test build tag %q; use only e2e and OS/architecture constraints", file.name, tag))
			}
		}
		gated := requiresEndToEndTag(file.build)
		for _, name := range file.tests {
			named := strings.Contains(name, "E2E")
			if gated && !named {
				problems = append(problems, fmt.Sprintf("%s: %s requires e2e but its name does not contain E2E", file.name, name))
			} else if named && !gated {
				problems = append(problems, fmt.Sprintf("%s: %s must have a build expression implying e2e", file.name, name))
			}
		}
		// Internal and external tests in one directory share a test executable,
		// so two TestMain declarations conflict even across package/_test names.
		dir := filepath.Dir(file.name)
		for range file.mainCount {
			for _, previous := range mains[dir] {
				if conventionFilesOverlap(previous, file) {
					problems = append(problems, fmt.Sprintf("%s and %s: multiple TestMain declarations in one buildable package", previous.name, file.name))
				}
			}
			mains[dir] = append(mains[dir], file)
		}
	}
	slices.Sort(problems)
	return problems
}

func collectConstraintTags(expr constraint.Expr, tags map[string]bool) {
	switch expr := expr.(type) {
	case *constraint.TagExpr:
		tags[expr.Tag] = true
	case *constraint.NotExpr:
		collectConstraintTags(expr.X, tags)
	case *constraint.AndExpr:
		collectConstraintTags(expr.X, tags)
		collectConstraintTags(expr.Y, tags)
	case *constraint.OrExpr:
		collectConstraintTags(expr.X, tags)
		collectConstraintTags(expr.Y, tags)
	}
}

// Enumerate Go targets rather than assigning OS tags independently: linux and
// windows cannot be true together, while android also satisfies linux, etc.
var conventionPlatforms = strings.Fields(`
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
`)

func knownOS(tag string) bool {
	return slices.Contains(strings.Fields("aix android darwin dragonfly freebsd hurd illumos ios js linux nacl netbsd openbsd plan9 solaris wasip1 windows zos"), tag)
}

func knownArch(tag string) bool {
	return slices.Contains(strings.Fields("386 amd64 amd64p32 arm armbe arm64 arm64be loong64 mips mipsle mips64 mips64le mips64p32 mips64p32le ppc ppc64 ppc64le riscv riscv64 s390 s390x sparc sparc64 wasm"), tag)
}

func platformTag(tag, goos, goarch string, endToEnd bool) bool {
	if tag == "e2e" {
		return endToEnd
	}
	if tag == "unix" {
		return slices.Contains(strings.Fields("aix android darwin dragonfly freebsd hurd illumos ios linux netbsd openbsd solaris"), goos)
	}
	return tag == goos || tag == goarch || tag == "linux" && goos == "android" || tag == "darwin" && goos == "ios" || tag == "solaris" && goos == "illumos"
}

func matchesConstraint(expr constraint.Expr, goos, goarch string, endToEnd bool) bool {
	return expr == nil || expr.Eval(func(tag string) bool { return platformTag(tag, goos, goarch, endToEnd) })
}

func requiresEndToEndTag(expr constraint.Expr) bool {
	tags := map[string]bool{}
	collectConstraintTags(expr, tags)
	if !tags["e2e"] {
		return false
	}
	possible := false
	for _, platform := range conventionPlatforms {
		goos, goarch, _ := strings.Cut(platform, "/")
		if matchesConstraint(expr, goos, goarch, false) {
			return false
		}
		possible = possible || matchesConstraint(expr, goos, goarch, true)
	}
	return possible
}

func matchesConventionFile(file conventionFile, goos, goarch string, endToEnd bool) bool {
	base := filepath.Base(file.name)
	if strings.HasPrefix(base, "_") || strings.HasPrefix(base, ".") {
		return false
	}
	parts := strings.Split(strings.TrimSuffix(base, "_test.go"), "_")
	if len(parts) > 1 && knownArch(parts[len(parts)-1]) {
		if parts[len(parts)-1] != goarch {
			return false
		}
		parts = parts[:len(parts)-1]
	}
	if len(parts) > 1 && knownOS(parts[len(parts)-1]) && !platformTag(parts[len(parts)-1], goos, goarch, endToEnd) {
		return false
	}
	return matchesConstraint(file.build, goos, goarch, endToEnd)
}

func conventionFilesOverlap(a, b conventionFile) bool {
	for _, platform := range conventionPlatforms {
		goos, goarch, _ := strings.Cut(platform, "/")
		for _, endToEnd := range []bool{false, true} {
			if matchesConventionFile(a, goos, goarch, endToEnd) && matchesConventionFile(b, goos, goarch, endToEnd) {
				return true
			}
		}
	}
	return false
}

func TestGoTestConventionsRules(t *testing.T) {
	for _, tt := range []struct {
		name, directive, declaration string
		wantProblem                  bool
	}{
		{"ordinary", "", "TestOrdinary", false},
		{"tag and name", "e2e", "TestE2EWorkflow", false},
		{"platform constraints", "e2e && linux && (amd64 || arm64)", "TestE2EWorkflow", false},
		{"missing name", "e2e", "TestWorkflow", true},
		{"missing tag", "", "TestE2EWorkflow", true},
		{"or is not a gate", "e2e || windows", "TestE2EWorkflow", true},
		{"extra category", "e2e && mdns_e2e", "TestE2EWorkflow", true},
		{"old category", "timefilter_reference", "TestReference", true},
		{"negation is not a gate", "!e2e", "TestE2EWorkflow", true},
		{"impossible gate", "e2e && !e2e", "TestE2EWorkflow", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			header := ""
			if tt.directive != "" {
				header = "//go:build " + tt.directive + "\n\n"
			}
			file, err := parseConventionFile("fixture_test.go", header+"package fixture\nimport t \"testing\"\nfunc "+tt.declaration+"(test *t.T) {}\n")
			if err != nil {
				t.Fatal(err)
			}
			if problems := conventionProblems([]conventionFile{file}); (len(problems) != 0) != tt.wantProblem {
				t.Fatalf("problems = %v; want problem: %v", problems, tt.wantProblem)
			}
		})
	}
	t.Run("declarations only", func(t *testing.T) {
		file, err := parseConventionFile("fixture_test.go", `package fixture
import alias "testing"
// func TestE2EFake(t *testing.T) {}
const mentioned = "func TestE2EText(t *testing.T) {}"
type receiver struct{}
func (receiver) TestE2EMethod(t *alias.T) {}
func TestE2EWrongParameter(t string) {}
func TestE2EExtraParameter(t, other *alias.T) {}
func TestE2EReturns(t *alias.T) int { return 0 }
func TestE2EGeneric[T any](t *alias.T) {}
func Testlowercase(t *alias.T) {}
func TestReal(t *alias.T) {}
func TestMain(m *alias.M) {}
`)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(file.tests, []string{"TestReal"}) || file.mainCount != 1 {
			t.Fatalf("misclassified declarations: %+v", file)
		}
		if problems := conventionProblems([]conventionFile{file}); len(problems) != 0 {
			t.Fatal(problems)
		}
	})
	t.Run("legacy and dot import", func(t *testing.T) {
		file, err := parseConventionFile("fixture_test.go", "// +build e2e\n\npackage fixture\nimport . \"testing\"\nfunc TestE2EReal(*T) {}\nfunc TestMain(*M) {}\n")
		if err != nil {
			t.Fatal(err)
		}
		if len(file.tests) != 1 || file.mainCount != 1 || len(conventionProblems([]conventionFile{file})) != 0 {
			t.Fatalf("misclassified legacy constraints: %+v", file)
		}
	})
	for _, tt := range []struct {
		name, aName, aBuild, bName, bBuild string
		wantOverlap                        bool
	}{
		{"same package", "a_test.go", "", "b_test.go", "e2e", true},
		{"separate operating systems", "a_linux_test.go", "", "b_windows_test.go", "", false},
		{"separate architectures", "a_amd64_test.go", "", "b_arm64_test.go", "", false},
		{"separate tag selections", "a_test.go", "e2e", "b_test.go", "!e2e", false},
		{"OS alias overlaps", "a_linux_test.go", "", "b_android_test.go", "", true},
		{"combined platform suffix", "a_linux_amd64_test.go", "", "b_linux_arm64_test.go", "", false},
		{"ignored filename", "_ignored_test.go", "", "b_test.go", "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			parse := func(name, build string) conventionFile {
				t.Helper()
				header := ""
				if build != "" {
					header = "//go:build " + build + "\n\n"
				}
				file, err := parseConventionFile(name, header+"package fixture\nimport \"testing\"\nfunc TestMain(m *testing.M) {}\n")
				if err != nil {
					t.Fatal(err)
				}
				return file
			}
			problems := conventionProblems([]conventionFile{parse(tt.aName, tt.aBuild), parse(tt.bName, tt.bBuild)})
			if (len(problems) != 0) != tt.wantOverlap {
				t.Fatalf("problems = %v; want overlapping TestMain: %v", problems, tt.wantOverlap)
			}
		})
	}
}
