package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	args := []string{"--help", "", "two words", "quotes'\" $dollar; & back\\slash", "--", "-args"}
	call, err := parse(append([]string{"run", "arbitrary-example"}, args...))
	if err != nil || call.command != "run" || call.name != "arbitrary-example" || !reflect.DeepEqual(call.args, args) {
		t.Fatalf("got %#v, %v", call, err)
	}
	for _, command := range []string{"list", "build", "format", "check", "test", "--help", "-h"} {
		if call, err := parse([]string{command}); err != nil || call.command != command {
			t.Fatalf("parse %q: %#v, %v", command, call, err)
		}
	}
	for _, args := range [][]string{nil, {"run"}, {"unknown"}, {"list", "extra"}, {"build", "extra"}, {"check", "extra"}, {"format", "extra"}, {"test", "extra"}} {
		if _, err := parse(args); err == nil {
			t.Fatalf("accepted %#v", args)
		}
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"run"}, {"unknown"}} {
		var out, diagnostics bytes.Buffer
		if code := run(t.Context(), args, nil, &out, &diagnostics); code != 2 || !strings.Contains(diagnostics.String(), "usage:") {
			t.Fatalf("args %#v: exit %d, diagnostics %q", args, code, diagnostics.String())
		}
	}
	var out, diagnostics bytes.Buffer
	if code := run(t.Context(), []string{"--help"}, nil, &out, &diagnostics); code != 0 || !strings.Contains(out.String(), "usage:") || !strings.Contains(out.String(), "build") {
		t.Fatalf("help: exit %d, output %q", code, out.String())
	}
}

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
	for _, name := range []string{"z-last", "custom-name"} {
		dir := filepath.Join(root, "_examples", name)
		writeFixture(t, filepath.Join(dir, "go.mod"), "module fixture/"+name+"\n\ngo 1.27.3\n")
		writeFixture(t, filepath.Join(dir, "main.go"), `package main
import ("encoding/json"; "os")
func main() {
 _ = json.NewEncoder(os.Stdout).Encode(os.Args[1:])
 if os.Getenv("SENDSPIN_EXAMPLE_FAIL") == "1" { os.Exit(23) }
}
`)
	}
	return root
}

func TestListAndRejectedPaths(t *testing.T) {
	root := fixtureRoot(t)
	var out, diagnostics bytes.Buffer
	code := execute(t.Context(), root, invocation{command: "list"}, nil, &out, &diagnostics)
	if code != 0 || out.String() != "custom-name\nz-last\n" {
		t.Fatalf("list: exit %d, output %q, diagnostics %q", code, out.String(), diagnostics.String())
	}
	for _, name := range []string{"../outside", "custom-name/../z-last", "missing"} {
		diagnostics.Reset()
		if code := execute(t.Context(), root, invocation{command: "run", name: name}, nil, &out, &diagnostics); code != 2 {
			t.Fatalf("accepted path %q: exit %d", name, code)
		}
	}
}

func TestRunGoModuleVerbatim(t *testing.T) {
	root := fixtureRoot(t)
	t.Chdir(t.TempDir())
	args := []string{"--help", "", "two words", "quotes'\" $dollar; & back\\slash", "--", "-args"}
	var out, diagnostics bytes.Buffer
	call := invocation{command: "run", name: "custom-name", args: args}
	if code := execute(t.Context(), root, call, nil, &out, &diagnostics); code != 0 {
		t.Fatalf("run: exit %d, diagnostics %s", code, &diagnostics)
	}
	var got []string
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, args) {
		t.Fatalf("got %#v, %v; want %#v", got, err, args)
	}
	t.Setenv("SENDSPIN_EXAMPLE_FAIL", "1")
	out.Reset()
	diagnostics.Reset()
	if code := execute(context.Background(), root, call, nil, &out, &diagnostics); code != 1 {
		t.Fatalf("must preserve go run's status 1, not the application's status 23: got %d, %s", code, &diagnostics)
	}
}

func TestTestLoopsOverOwnModules(t *testing.T) {
	root := fixtureRoot(t)
	// A root SDK test must never be reached by the independent module loop.
	writeFixture(t, filepath.Join(root, "go.mod"), "module rootfixture\n\ngo 1.27.3\n")
	writeFixture(t, filepath.Join(root, "root_test.go"), "package rootfixture\nimport \"testing\"\nfunc TestNever(t *testing.T) { t.Fatal(\"root test recursion\") }\n")
	var out, diagnostics bytes.Buffer
	if code := execute(t.Context(), root, invocation{command: "test"}, nil, &out, &diagnostics); code != 0 {
		t.Fatalf("test modules: exit %d, diagnostics %s", code, &diagnostics)
	}
	for _, name := range []string{"fixture/custom-name", "fixture/z-last"} {
		if !strings.Contains(out.String(), name) {
			t.Fatalf("test output does not mention %q: %s", name, &out)
		}
	}
}

func TestBuildLoopsOverOwnModules(t *testing.T) {
	root := fixtureRoot(t)
	writeFixture(t, filepath.Join(root, "go.mod"), "module rootfixture\n\ngo 1.27.3\n")
	writeFixture(t, filepath.Join(root, "main.go"), "must never build the root SDK\n")
	t.Chdir(t.TempDir())
	t.Setenv("CGO_ENABLED", "0")
	t.Setenv("GOOS", runtime.GOOS)
	t.Setenv("GOARCH", runtime.GOARCH)
	var out, diagnostics bytes.Buffer
	if code := execute(t.Context(), root, invocation{command: "build"}, nil, &out, &diagnostics); code != 0 {
		t.Fatalf("build modules: exit %d, diagnostics %s", code, &diagnostics)
	}
	for _, name := range []string{"custom-name", "z-last"} {
		binary := name
		if runtime.GOOS == "windows" {
			binary += ".exe"
		}
		path := filepath.Join(root, "var", "bin", "examples", name, binary)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			t.Fatalf("missing built example executable: %s", path)
		}
	}
}
