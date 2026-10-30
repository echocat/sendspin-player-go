package integration

import (
	"bytes"
	"context"
	"flag"
	"testing"
	"time"

	"github.com/echocat/sendspin-player-go/internal/examples"
)

var exampleMode = flag.String("examples", "off", "example module checks: off, test, code or smoke")

// TestExamples checks independent modules, not hardware applications. It stays
// opt-in so SDK module archives do not need the excluded _examples directory.
func TestExamples(t *testing.T) {
	switch *exampleMode {
	case "off":
		t.Skip("enable example module checks with -examples=test or -examples=code")
	case "smoke":
		t.Skip("no generic hardware-safe application smoke contract is defined; use -examples=test")
	case "test", "code":
	default:
		t.Fatalf("unknown examples mode %q (want off, test, code or smoke)", *exampleMode)
	}
	root, err := examples.Root()
	if err != nil {
		t.Fatal(err)
	}
	modules, err := examples.Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(modules) == 0 {
		t.Fatal("no example modules discovered")
	}
	commands, err := examples.Commands(root, *exampleMode)
	if err != nil {
		t.Fatal(err)
	}
	for _, module := range modules {
		t.Run(module.Name, func(t *testing.T) {
			for _, command := range commands {
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
				var output bytes.Buffer
				err := examples.Run(ctx, module.Dir, command, nil, &output, &output)
				cancel()
				if err != nil {
					t.Fatalf("%s %q: %v\n%s", command.Tool, command.Args, err, output.String())
				}
				t.Logf("%s %q\n%s", command.Tool, command.Args, output.String())
			}
		})
	}
}
