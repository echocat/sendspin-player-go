package specaudit

import (
	"go/build/constraint"
	"testing"
)

func TestBuildActivationUsesOnlyOptInAndPossiblePlatforms(t *testing.T) {
	for _, tc := range []struct {
		expression    string
		ordinary, e2e bool
	}{
		{"!linux || (!amd64 && !arm64)", true, true},
		{"e2e && windows && (amd64 || arm64)", false, true},
		{"e2e && (linux || darwin) && !amd64", false, true},
		{"linux && windows", false, false},
		{"amd64 && arm64", false, false},
		{"e2e && windows && wasm", false, false},
		{"e2e && js && amd64", false, false},
		{"e2e && mdns_e2e", false, false},
		{"timefilter_reference", false, false},
		{"custom_tag", false, false},
		{"unix && !linux && !darwin", true, true},
	} {
		t.Run(tc.expression, func(t *testing.T) {
			expression, err := constraint.Parse("//go:build " + tc.expression)
			if err != nil {
				t.Fatal(err)
			}
			if buildActive(expression, false) != tc.ordinary || buildActive(expression, true) != tc.e2e {
				t.Fatal("wrong ordinary/e2e activation")
			}
		})
	}
}

func TestSpecialOptInSuitesAreAssignedByPathNotGlobalNames(t *testing.T) {
	p := fixture(t)
	for file, suite := range map[string]string{
		"internal/mdnstest/peer_test.go":            "mdns",
		"internal/conformancetest/official_test.go": "harness",
		"internal/timesync/reference_live_test.go":  "reference",
		"internal/other/peer_test.go":               "e2e",
	} {
		put(t, p.Root, file, "//go:build e2e && (windows || linux)\n\npackage peer\nimport \"testing\"\nfunc TestE2ESame(t *testing.T) {}\n")
		inv, err := Discover(p.Root)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, test := range inv.Tests {
			if test.File == file {
				found = test.Suite == suite
			}
		}
		if !found {
			t.Fatal("path has wrong suite", file, suite)
		}
	}
}

func TestDiscoveryRejectsOptInTestsWithoutTagOrName(t *testing.T) {
	for _, body := range []string{
		"func TestE2EAccidentallyOrdinary(t *testing.T) {}",
		"//go:build e2e\n\npackage peer\nimport \"testing\"\nfunc TestNotPrefixed(t *testing.T) {}",
	} {
		p := fixture(t)
		if body[0] != '/' {
			body = "package peer\nimport \"testing\"\n" + body
		}
		put(t, p.Root, "peer_test.go", body)
		if _, err := Discover(p.Root); err == nil {
			t.Fatal("unsafe E2E declaration accepted")
		}
	}
}

func TestDiscoveryRecognizesDotImportsButNotGenericFunctions(t *testing.T) {
	p := fixture(t)
	put(t, p.Root, "dot_test.go", "package audit\nimport . \"testing\"\nfunc TestDot(*T) {}\nfunc TestGeneric[A any](*T) {}\n")
	inv, err := Discover(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := inv.Tests[TestID("unit", "example.test/audit", "TestDot")]; !ok {
		t.Fatal("dot-imported testing.T declaration omitted")
	}
	if _, ok := inv.Tests[TestID("unit", "example.test/audit", "TestGeneric")]; ok {
		t.Fatal("generic function is not a Go test entrypoint")
	}
}
