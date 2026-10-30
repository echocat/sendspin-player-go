//go:build e2e

package mdnstest

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"

	"github.com/echocat/sendspin-player-go/internal/e2etest"
)

var referenceSuite *e2etest.Suite
var fixtureUnavailable error

func TestMain(m *testing.M) {
	flag.Parse()
	// Go's private cache logfile points into the host's temporary build tree.
	// It is inaccessible in the container, and cached results cannot describe
	// external engine/network state anyway. Leave no host cache log, so go test
	// executes the fixture afresh. All user test-selection/output flags survive.
	if err := flag.Set("test.testlogfile", ""); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if os.Getenv("SENDSPIN_MDNS_ROLE") == "observer" {
		if err := runObserver(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if os.Getenv("SENDSPIN_MDNS_ROLE") == "tests" {
		if err := containerOnly(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		referenceSuite = e2etest.NewSuite()
		os.Exit(referenceSuite.Run(m))
	}
	// Listing tests must not build images or start any external process.
	if listing := flag.Lookup("test.list"); listing != nil && listing.Value.String() != "" {
		os.Exit(m.Run())
	}
	selected, err := selectedE2E(flag.Lookup("test.run").Value.String(), flag.Lookup("test.skip").Value.String())
	if err != nil || !selected {
		// Let testing report invalid filters itself, or execute only the
		// selected pure unit tests. Neither path needs a container engine.
		os.Exit(m.Run())
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	requested := os.Getenv("SENDSPIN_MDNS_ENGINE")
	if requested == "" {
		requested = "auto"
	}
	if requested != "auto" && requested != "podman" && requested != "docker" {
		fmt.Fprintln(os.Stderr, "SENDSPIN_MDNS_ENGINE must be auto, podman or docker")
		os.Exit(1)
	}
	engine, err := selectEngine(ctx, requested)
	if err != nil {
		// Individual E2E cases report the unmet Linux-container prerequisite.
		// No SDK or observer socket is ever opened on this path.
		fixtureUnavailable = err
		os.Exit(m.Run())
	}
	if err := runFixture(ctx, engine, e2etest.Root(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if exit, ok := err.(*exec.ExitError); ok {
			os.Exit(exit.ExitCode())
		}
		os.Exit(1)
	}
	os.Exit(0)
}
