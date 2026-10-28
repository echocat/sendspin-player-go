// Command conformancetestcmd builds the client adapter and runs the pinned harness.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/echocat/sendspin-player-go/internal/devpath"
)

func main() {
	scenarios := flag.String("scenarios", "", "Comma-separated official scenario IDs (default: supported local suite)")
	manifest := flag.String("manifest", "", "Optional exact path for a fresh harness result sidecar")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *scenarios, *manifest); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func invoke(ctx context.Context, command string, args ...string) error {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
func run(parent context.Context, scenarios, manifest string) error {
	ctx, cancel := context.WithTimeout(parent, 15*time.Minute)
	defer cancel()
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	if err = devpath.Prepare(root); err != nil {
		return err
	}
	directory := filepath.Join(root, "var", "conformance")
	if err = os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	buildDirectory, err := os.MkdirTemp("", "sendspin-conformance-adapter-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(buildDirectory) }()
	binary := filepath.Join(buildDirectory, "client")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	defer func() { _ = os.Remove(binary) }()
	fmt.Fprintln(os.Stderr, "[conformance] Building race-enabled public-SDK adapter")
	if err = invoke(ctx, "go", "build", "-race", "-o", binary, "./internal/conformanceclient"); err != nil {
		return err
	}
	setup := exec.CommandContext(ctx, "uv", "sync", "--locked", "--project", "internal/conformancetest")
	setup.Env = devpath.PythonEnv(root, "conformance")
	setup.Stdout = os.Stdout
	setup.Stderr = os.Stderr
	if err = setup.Run(); err != nil {
		return err
	}
	python := devpath.Python(root, "conformance")
	args := []string{"internal/conformancetest/run.py", "--client", binary, "--scenarios", scenarios}
	if manifest != "" {
		args = append(args, "--manifest", manifest)
	}
	cmd := exec.CommandContext(ctx, python, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = devpath.PythonEnv(root, "conformance")
	// Let asyncio unwind and terminate tracked child adapters before the hard
	// deadline. Windows may not support Signal; WaitDelay still bounds shutdown.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 5 * time.Second
	return cmd.Run()
}
