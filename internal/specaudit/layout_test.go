package specaudit

import (
	"testing"
)

func TestRootNativeSelectorAndOfflineToolKeepTheirEvidenceSuites(t *testing.T) {
	p := fixture(t)
	put(t, p.Root, "audio/native/output_test.go", "package native\nimport \"testing\"\nfunc TestSelector(t *testing.T){}\n")
	put(t, p.Root, "internal/loopbackcmd/main_test.go", "package main\nimport \"testing\"\nfunc TestOffline(t *testing.T){}\n")
	p, err := Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{
		TestID("desktop-audio", "example.test/audit/audio/native", "TestSelector"),
		TestID("unit", "example.test/audit/internal/loopbackcmd", "TestOffline"),
	} {
		if _, ok := p.Inventory.Tests[id]; !ok {
			t.Fatal("root-module package assigned wrong evidence suite", id)
		}
	}
}

func TestIndependentExampleModulesKeepTheirEvidenceSuite(t *testing.T) {
	for _, prefix := range []string{"example.test/audit/_examples/", "example.test/independent/"} {
		t.Run(prefix, func(t *testing.T) {
			p := fixture(t)
			for _, name := range []string{"player", "external-control"} {
				put(t, p.Root, "_examples/"+name+"/go.mod", "module "+prefix+name+"\n\ngo 1.27.3\n")
				put(t, p.Root, "_examples/"+name+"/main_test.go", "package main\nimport \"testing\"\nfunc TestExample(t *testing.T){}\n")
			}
			inv, err := Discover(p.Root)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"player", "external-control"} {
				id := TestID("examples", prefix+name, "TestExample")
				if _, ok := inv.Tests[id]; !ok {
					t.Fatal("independent example module assigned wrong package or evidence suite", id)
				}
			}
		})
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
