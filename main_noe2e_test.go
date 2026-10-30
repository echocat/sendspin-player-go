//go:build !e2e

package sendspin

import "testing"

func runPackageTests(m *testing.M) int { return m.Run() }
