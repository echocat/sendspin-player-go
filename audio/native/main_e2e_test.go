//go:build e2e

package native_test

import (
	"os"
	"testing"

	"github.com/echocat/sendspin-player-go/internal/e2etest"
)

var suite = e2etest.NewSuite()

func TestMain(m *testing.M) { os.Exit(suite.Run(m)) }
