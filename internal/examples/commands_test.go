package examples

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCommands(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, ".golangci.yaml")
	for mode, want := range map[string][]Command{
		"test": {{Tool: "go", Args: []string{"test", "./..."}}},
		"format": {
			{Tool: "golangci-lint", Args: []string{"fmt", "--config", config, "./..."}},
			{Tool: "go", Args: []string{"mod", "tidy"}},
		},
		"check": {
			{Tool: "go", Args: []string{"mod", "tidy", "-diff"}},
			{Tool: "go", Args: []string{"vet", "./..."}},
			{Tool: "golangci-lint", Args: []string{"fmt", "--diff", "--config", config, "./..."}},
			{Tool: "golangci-lint", Args: []string{"run", "--config", config, "./..."}},
		},
	} {
		t.Run(mode, func(t *testing.T) {
			got, err := Commands(root, mode)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("got %#v, %v; want %#v", got, err, want)
			}
			moduleCommands, err := ModuleCommands(root, mode, Module{Name: "custom-name"})
			if err != nil || !reflect.DeepEqual(moduleCommands, want) {
				t.Fatalf("module commands = %#v, %v; want %#v", moduleCommands, err, want)
			}
		})
	}
	check, _ := Commands(root, "check")
	code, _ := Commands(root, "code")
	if !reflect.DeepEqual(check, code) {
		t.Fatal("code mode differs from CLI check")
	}
	if _, err := Commands(root, "unknown"); err == nil {
		t.Fatal("accepted unknown command mode")
	}
}

// This process fixture does not build or open any audio application.
func TestProcessFixture(t *testing.T) {
	if os.Getenv("SENDSPIN_EXAMPLE_PROCESS_FIXTURE") != "1" {
		return
	}
	separator := 0
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i + 1
			break
		}
	}
	if separator == 0 {
		os.Exit(99)
	}
	args := os.Args[separator:]
	if len(args) > 0 && args[0] == "interrupt-child" {
		interrupts := make(chan os.Signal, 1)
		signal.Notify(interrupts, Interrupts()...)
		_, _ = os.Stdout.WriteString("ready\n")
		<-interrupts
		_, _ = os.Stdout.WriteString("child interrupted\n")
		os.Exit(0)
	}
	if len(args) > 0 && args[0] == "group" {
		ignoreFixtureInterrupts()
		binary, err := os.Executable()
		if err != nil {
			os.Exit(98)
		}
		child := exec.Command(binary, "-test.run=^TestProcessFixture$", "--", "interrupt-child")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Run(); err != nil {
			os.Exit(97)
		}
		_, _ = os.Stdout.WriteString("parent reaped child\n")
		os.Exit(0)
	}
	if len(args) > 0 && args[0] == "wait" {
		// Deliberately ignore a graceful interrupt to exercise bounded shutdown.
		ignoreFixtureInterrupts()
		_, _ = os.Stdout.WriteString("ready\n")
		for {
			time.Sleep(time.Hour)
		}
	}
	cwd, _ := os.Getwd()
	_ = json.NewEncoder(os.Stdout).Encode(struct {
		Args   []string
		Dir    string
		GoWork string
		GoOS   string
		GoArch string
		CGO    string
	}{args, cwd, os.Getenv("GOWORK"), os.Getenv("GOOS"), os.Getenv("GOARCH"), os.Getenv("CGO_ENABLED")})
	os.Exit(23)
}

func TestBuildCommandWithFakeProcess(t *testing.T) {
	t.Setenv("SENDSPIN_EXAMPLE_PROCESS_FIXTURE", "1")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		goos   string
		goarch string
		cgo    string
	}{
		{name: "native-default", cgo: "1"},
		{name: "windows", goos: "windows", goarch: "arm64", cgo: "0"},
		{name: "linux", goos: "linux", goarch: "amd64", cgo: "1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("GOOS", test.goos)
			t.Setenv("GOARCH", test.goarch)
			t.Setenv("CGO_ENABLED", test.cgo)
			goos, goarch := test.goos, test.goarch
			if goos == "" {
				goos = runtime.GOOS
			}
			if goarch == "" {
				goarch = runtime.GOARCH
			}
			root := filepath.Join(t.TempDir(), "root with spaces")
			module := Module{Name: "custom-name", Dir: t.TempDir()}
			commands, err := ModuleCommands(root, "build", module)
			if err != nil || len(commands) != 1 {
				t.Fatalf("build commands = %#v, %v", commands, err)
			}
			name := module.Name
			if goos == "windows" {
				name += ".exe"
			}
			wantArgs := []string{"build", "-o", filepath.Join(root, "var", "bin", "examples", module.Name, name), "."}
			command := commands[0]
			if command.Tool != "go" || !reflect.DeepEqual(command.Args, wantArgs) {
				t.Fatalf("build command = %#v, want go %#v", command, wantArgs)
			}
			// Replace only the executable with a Go fixture; retain build argv and
			// target overrides to check exactly what the real child would receive.
			command.Tool = binary
			command.Args = append([]string{"-test.run=^TestProcessFixture$", "--"}, command.Args...)
			var output, diagnostics bytes.Buffer
			if err := Run(t.Context(), module.Dir, command, nil, &output, &diagnostics); ExitCode(err) != 23 {
				t.Fatalf("fake build: %v, diagnostics %s", err, &diagnostics)
			}
			var result struct {
				Args   []string
				GoOS   string
				GoArch string
				CGO    string
			}
			if err := json.Unmarshal(output.Bytes(), &result); err != nil {
				t.Fatalf("decode %q: %v", output.String(), err)
			}
			if !reflect.DeepEqual(result.Args, wantArgs) || result.GoOS != goos || result.GoArch != goarch || result.CGO != test.cgo {
				t.Fatalf("unexpected fake build invocation: %+v", result)
			}
		})
	}
}

func TestRunPreservesArgumentsDirectoryAndExitCode(t *testing.T) {
	t.Setenv("SENDSPIN_EXAMPLE_PROCESS_FIXTURE", "1")
	t.Setenv("GOWORK", "unwanted-workspace")
	t.Setenv("GOOS", "not-native")
	t.Setenv("GOARCH", "not-native")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"", "two words", "quotes'\" $dollar; & back\\slash", "--help", "--", "-args"}
	command := Command{Tool: binary, Args: append([]string{"-test.run=^TestProcessFixture$", "--"}, args...)}
	dir := t.TempDir()
	var output, diagnostics bytes.Buffer
	err = Run(t.Context(), dir, command, nil, &output, &diagnostics)
	if ExitCode(err) != 23 {
		t.Fatalf("exit = %d, error = %v, diagnostics = %s", ExitCode(err), err, &diagnostics)
	}
	var result struct {
		Args   []string
		Dir    string
		GoWork string
		GoOS   string
		GoArch string
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("decode %q: %v", output.String(), err)
	}
	if !reflect.DeepEqual(result.Args, args) {
		t.Fatalf("args = %#v, want %#v", result.Args, args)
	}
	gotDir, err := filepath.EvalSymlinks(result.Dir)
	if err != nil {
		t.Fatal(err)
	}
	wantDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if gotDir != wantDir || result.GoWork != "off" || result.GoOS != runtime.GOOS || result.GoArch != runtime.GOARCH {
		t.Fatalf("unexpected child configuration: %+v", result)
	}
}

type readyWriter struct {
	ready chan struct{}
}

func (w readyWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "ready") {
		select {
		case w.ready <- struct{}{}:
		default:
		}
	}
	return len(p), nil
}

func TestRunCancellationIsBounded(t *testing.T) {
	t.Setenv("SENDSPIN_EXAMPLE_PROCESS_FIXTURE", "1")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready := make(chan struct{}, 1)
	done := make(chan error, 1)
	dir := t.TempDir()
	go func() {
		done <- Run(ctx, dir, Command{Tool: binary, Args: []string{"-test.run=^TestProcessFixture$", "--", "wait"}}, nil, readyWriter{ready}, nil)
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("fixture stopped before readiness: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("fixture never became ready")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error = %v", err)
		}
	case <-time.After(3 * shutdownGrace):
		t.Fatal("child did not stop within the shutdown bound")
	}
}

func TestRunCanceledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := Run(ctx, t.TempDir(), Command{Tool: "not-a-real-command"}, nil, nil, nil)
	if !errors.Is(err, context.Canceled) || ExitCode(err) != 130 {
		t.Fatalf("got %v, exit %d", err, ExitCode(err))
	}
	if ExitCode(nil) != 0 || ExitCode(errors.New("start failure")) != 1 {
		t.Fatal("incorrect non-process exit code")
	}
}
