package specaudit

import (
	"testing"
)

func TestRootModuleExamplesAndNativeSelectorKeepTheirEvidenceSuites(t *testing.T) {
	p := fixture(t)
	put(t, p.Root, "_examples/player/output_test.go", "package main\nimport \"testing\"\nfunc TestExample(t *testing.T){}\n")
	put(t, p.Root, "audio/native/output_test.go", "package native\nimport \"testing\"\nfunc TestSelector(t *testing.T){}\n")
	put(t, p.Root, "internal/loopbackcmd/main_test.go", "package main\nimport \"testing\"\nfunc TestOffline(t *testing.T){}\n")
	p, err := Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{
		TestID("examples", "example.test/audit/_examples/player", "TestExample"),
		TestID("desktop-audio", "example.test/audit/audio/native", "TestSelector"),
		TestID("unit", "example.test/audit/internal/loopbackcmd", "TestOffline"),
	} {
		if _, ok := p.Inventory.Tests[id]; !ok {
			t.Fatal("root-module package assigned wrong evidence suite", id)
		}
	}
}

func TestMovedShellHelpersRemainPartOfSourceFingerprint(t *testing.T) {
	p := fixture(t)
	put(t, p.Root, "scripts/audio/helper.sh", "#!/bin/sh\nexit 0\n")
	before, err := Fingerprint(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	put(t, p.Root, "scripts/audio/helper.sh", "#!/bin/sh\nexit 1\n")
	after, err := Fingerprint(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("moved helper changes can reuse stale source evidence")
	}
	put(t, p.Root, "scripts/tests/__pycache__/generated.pyc", "generated only")
	withoutCache, err := Fingerprint(p.Root)
	if err != nil || withoutCache != after {
		t.Fatal("generated Python cache contaminated source fingerprint", err)
	}
}
