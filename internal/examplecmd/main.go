// Command examplecmd provides the Go-only implementation of mise's example tasks.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/echocat/sendspin-player-go/internal/examples"
)

const usage = "usage: examplecmd run <example> [args...] | list | build | format | check | test"

func main() {
	os.Exit(runMain())
}

func runMain() int {
	ctx, stop := signal.NotifyContext(context.Background(), examples.Interrupts()...)
	defer stop()
	return run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
}

type invocation struct {
	command string
	name    string
	args    []string
}

func parse(args []string) (invocation, error) {
	if len(args) == 0 {
		return invocation{}, fmt.Errorf("%s", usage)
	}
	call := invocation{command: args[0]}
	switch call.command {
	case "run":
		if len(args) < 2 {
			return invocation{}, fmt.Errorf("%s", usage)
		}
		call.name, call.args = args[1], args[2:]
	case "list", "build", "format", "check", "test", "--help", "-h":
		if len(args) != 1 {
			return invocation{}, fmt.Errorf("%s does not accept arguments\n%s", call.command, usage)
		}
	default:
		return invocation{}, fmt.Errorf("unknown example command %q\n%s", call.command, usage)
	}
	return call, nil
}

func run(ctx context.Context, args []string, input io.Reader, output, diagnostics io.Writer) int {
	call, err := parse(args)
	if err != nil {
		_, _ = fmt.Fprintln(diagnostics, err)
		return 2
	}
	if call.command == "--help" || call.command == "-h" {
		_, _ = fmt.Fprintln(output, usage)
		return 0
	}
	root, err := examples.Root()
	if err != nil {
		_, _ = fmt.Fprintln(diagnostics, err)
		return 1
	}
	return execute(ctx, root, call, input, output, diagnostics)
}

func execute(ctx context.Context, root string, call invocation, input io.Reader, output, diagnostics io.Writer) int {
	if call.command == "run" {
		directory, err := examples.Directory(root, call.name)
		if err != nil {
			_, _ = fmt.Fprintln(diagnostics, err)
			return 2
		}
		args := append([]string{"run", "."}, call.args...)
		return report(examples.Run(ctx, directory, examples.Command{Tool: "go", Args: args}, input, output, diagnostics), diagnostics)
	}
	modules, err := examples.Discover(root)
	if err != nil {
		return report(err, diagnostics)
	}
	if call.command == "list" {
		for _, module := range modules {
			if _, err := fmt.Fprintln(output, module.Name); err != nil {
				return report(err, diagnostics)
			}
		}
		return 0
	}
	for _, module := range modules {
		commands, err := examples.ModuleCommands(root, call.command, module)
		if err != nil {
			return report(err, diagnostics)
		}
		for _, command := range commands {
			if err := examples.Run(ctx, module.Dir, command, input, output, diagnostics); err != nil {
				return report(fmt.Errorf("example %s: %w", module.Name, err), diagnostics)
			}
		}
	}
	return 0
}

func report(err error, diagnostics io.Writer) int {
	if err != nil {
		_, _ = fmt.Fprintln(diagnostics, err)
	}
	return examples.ExitCode(err)
}
