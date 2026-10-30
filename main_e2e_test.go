//go:build e2e

package sendspin

import (
	"testing"

	"github.com/echocat/sendspin-player-go/internal/e2etest"
)

var referenceSuite = e2etest.NewSuite()

func runPackageTests(m *testing.M) int { return referenceSuite.Run(m) }
