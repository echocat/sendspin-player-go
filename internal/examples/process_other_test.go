//go:build !unix

package examples

import (
	"os"
	"os/signal"
)

func ignoreFixtureInterrupts() { signal.Ignore(os.Interrupt) }
