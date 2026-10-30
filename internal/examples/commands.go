package examples

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

const shutdownGrace = 3 * time.Second

// Command describes a native process invocation, without shell interpretation.
type Command struct {
	Tool string
	Args []string
	Env  []string // Optional overrides of the default native process environment.
}

// ModuleCommands adds module-specific build outputs to the shared operations.
// Builds honor an explicit target; interactive runs and checks remain native.
func ModuleCommands(root, mode string, module Module) ([]Command, error) {
	if mode != "build" {
		return Commands(root, mode)
	}
	goos := os.Getenv("GOOS")
	if goos == "" {
		goos = runtime.GOOS
	}
	goarch := os.Getenv("GOARCH")
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	binary := module.Name
	if goos == "windows" {
		binary += ".exe"
	}
	output := filepath.Join(root, "var", "bin", "examples", module.Name, binary)
	return []Command{{
		Tool: "go",
		Args: []string{"build", "-o", output, "."},
		Env:  []string{"GOOS=" + goos, "GOARCH=" + goarch},
	}}, nil
}

// Commands is shared by the CLI and the opt-in module component tests.
func Commands(root, mode string) ([]Command, error) {
	config := filepath.Join(root, ".golangci.yaml")
	switch mode {
	case "test":
		return []Command{{Tool: "go", Args: []string{"test", "./..."}}}, nil
	case "format":
		return []Command{
			{Tool: "golangci-lint", Args: []string{"fmt", "--config", config, "./..."}},
			{Tool: "go", Args: []string{"mod", "tidy"}},
		}, nil
	case "check", "code":
		return []Command{
			{Tool: "go", Args: []string{"mod", "tidy", "-diff"}},
			{Tool: "go", Args: []string{"vet", "./..."}},
			{Tool: "golangci-lint", Args: []string{"fmt", "--diff", "--config", config, "./..."}},
			{Tool: "golangci-lint", Args: []string{"run", "--config", config, "./..."}},
		}, nil
	default:
		return nil, fmt.Errorf("unknown example mode %q", mode)
	}
}

// Run executes in an example's own module. On cancellation it first interrupts
// the child, then forcibly stops it after a bounded grace period. Unix children
// get a process group so go run's application is interrupted along with go.
func Run(ctx context.Context, directory string, command Command, input io.Reader, output, diagnostics io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd := exec.Command(command.Tool, command.Args...)
	cmd.Dir = directory
	cmd.Env = append(cmd.Environ(), "GOWORK=off", "GOOS="+runtime.GOOS, "GOARCH="+runtime.GOARCH)
	cmd.Env = append(cmd.Env, command.Env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = input, output, diagnostics
	cmd.WaitDelay = shutdownGrace
	started, cleanup, err := prepareProcess(cmd)
	if err != nil {
		return err
	}
	defer func() { _ = cleanup() }()
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := started(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	// The process may already have exited; Wait still owns reporting its result.
	_ = interruptProcess(cmd)
	timer := time.NewTimer(shutdownGrace)
	defer timer.Stop()
	select {
	case err := <-done:
		// A go command can exit before its application; do not leave descendants.
		_ = killProcess(cmd)
		return errors.Join(ctx.Err(), err)
	case <-timer.C:
		_ = killProcess(cmd)
		_ = cleanup()
		return errors.Join(ctx.Err(), <-done)
	}
}

// ExitCode preserves the Go CLI's exit status (go run itself maps application
// failures to status 1). Cancellation without a process status uses 130.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return processExitCode(exit)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return 130
	}
	return 1
}

// Interrupts includes termination signals on platforms that support them.
func Interrupts() []os.Signal { return processInterrupts() }
