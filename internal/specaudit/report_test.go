package specaudit

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func evidenceFixture(t *testing.T, p *Project, events string) (Manifest, string) {
	t.Helper()
	directory := filepath.Join(p.Root, "var/audit/evidence/unit/context/run-fixture")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	put(t, directory, "events.jsonl", events)
	calls, err := p.invocations("unit", false)
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Schema: 1, Suite: "unit", CatalogSHA256: p.CatalogDigest, SourceSHA256: p.SourceDigest, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, GoVersion: runtime.Version(), Variant: "default", CIRun: ciRun(), Scope: suiteScope("unit"), Started: time.Now().UTC(), Finished: time.Now().UTC(), Complete: true, ExitCode: 0, Commands: []Command{{Args: calls[0].args, Directory: ".", Kind: "go", Environment: executionEnv(calls[0]), Log: "events.jsonl", SHA256: digest([]byte(events)), ExitCode: 0}}}
	if err = writeJSON(filepath.Join(directory, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	if err = writeJSON(filepath.Join(filepath.Dir(directory), "current.json"), struct {
		Manifest string `json:"manifest"`
	}{"run-fixture/manifest.json"}); err != nil {
		t.Fatal(err)
	}
	return manifest, directory
}

func TestPassingEvidenceCannotPromoteManualAssessmentOrConformance(t *testing.T) {
	p := fixture(t)
	p.Catalog.Requirements[0].Assessment = "P"
	if err := writeJSON(filepath.Join(p.Root, CatalogPath), p.Catalog); err != nil {
		t.Fatal(err)
	}
	var err error
	p, err = Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	evidenceFixture(t, p, validGoEvents)
	report, err := p.Report(ReportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Requirements[0].EvidenceStatus != "pass" || report.Requirements[0].Assessment != "P" || report.ConformanceClaim || len(report.ConformanceBlockers) == 0 {
		t.Fatal("passing software evidence promoted a partial manual assessment")
	}
	if !strings.Contains(report.Markdown(), "BLOCKED") {
		t.Fatal("missing conformance warning")
	}
}

func TestEvidenceCannotPassWhenMissingStaleFailedSkippedOrTampered(t *testing.T) {
	for _, mutation := range []string{"missing", "incomplete", "exit-failure", "stale-source", "stale-catalog", "wrong-goos", "wrong-arch", "wrong-go", "wrong-variant", "wrong-ci-run", "changed-log", "truncated-log", "package-fail", "missing-test", "skip", "wrong-count", "fake-fuzz-mode", "missing-command"} {
		t.Run(mutation, func(t *testing.T) {
			p := fixture(t)
			manifest, directory := evidenceFixture(t, p, validGoEvents)
			events := validGoEvents
			switch mutation {
			case "missing":
				if err := os.Remove(filepath.Join(filepath.Dir(directory), "current.json")); err != nil {
					t.Fatal(err)
				}
			case "incomplete":
				manifest.Complete = false
			case "exit-failure":
				manifest.ExitCode = 1
			case "stale-source":
				manifest.SourceSHA256 = strings.Repeat("f", 64)
			case "stale-catalog":
				manifest.CatalogSHA256 = strings.Repeat("f", 64)
			case "wrong-goos":
				manifest.GOOS = "other"
			case "wrong-arch":
				manifest.GOARCH = "other"
			case "wrong-go":
				manifest.GoVersion = "go0.0.1"
			case "wrong-variant":
				manifest.Variant = "stable"
			case "wrong-ci-run":
				t.Setenv("GITHUB_RUN_ID", "current")
				t.Setenv("GITHUB_RUN_ATTEMPT", "1")
				manifest.CIRun = "old/1"
			case "changed-log":
				put(t, directory, "events.jsonl", "not the hashed evidence")
			case "truncated-log":
				events += "{"
			case "package-fail":
				events = strings.Replace(events, `{"Action":"pass","Package":"example.test/audit"}`, `{"Action":"fail","Package":"example.test/audit"}`, 1)
			case "missing-test":
				events = strings.ReplaceAll(events, "TestOne", "TestOther")
			case "skip":
				events = strings.Replace(events, `"Action":"pass","Package":"example.test/audit","Test":"TestOne"`, `"Action":"skip","Package":"example.test/audit","Test":"TestOne"`, 1)
			case "wrong-count":
				manifest.Commands[0].Args[2] = "-count=2"
			case "fake-fuzz-mode":
				manifest.Commands[0].Kind = "harness"
			case "missing-command":
				manifest.Commands = nil
			}
			if events != validGoEvents {
				put(t, directory, "events.jsonl", events)
				manifest.Commands[0].SHA256 = digest([]byte(events))
			}
			if err := writeJSON(filepath.Join(directory, "manifest.json"), manifest); err != nil {
				t.Fatal(err)
			}
			report, err := p.Report(ReportOptions{Profile: "full"})
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Problems) == 0 || report.Requirements[0].EvidenceStatus == "pass" || report.ConformanceClaim {
				t.Fatal("invalid evidence credited", report.Requirements[0])
			}
		})
	}
}

func TestDuplicateSuiteEvidenceIsAmbiguousNotBestPassingRun(t *testing.T) {
	p := fixture(t)
	manifest, _ := evidenceFixture(t, p, validGoEvents)
	other := filepath.Join(p.Root, "var/audit/evidence/duplicate/unit/context/run-fixture")
	put(t, other, "events.jsonl", validGoEvents)
	if err := writeJSON(filepath.Join(other, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(filepath.Dir(other), "current.json"), struct {
		Manifest string `json:"manifest"`
	}{"run-fixture/manifest.json"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Report(ReportOptions{}); err == nil {
		t.Fatal("ambiguous manifests were silently merged or selected")
	}
}

func TestEvidenceCannotReadAnEscapingManifest(t *testing.T) {
	p := fixture(t)
	_, directory := evidenceFixture(t, p, validGoEvents)
	if err := writeJSON(filepath.Join(filepath.Dir(directory), "current.json"), struct {
		Manifest string `json:"manifest"`
	}{"../../outside.json"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Report(ReportOptions{}); err == nil {
		t.Fatal("accepted an escaping evidence path")
	}
}

func TestEveryMissingRequiredSuiteRemainsVisible(t *testing.T) {
	p := fixture(t)
	report, err := p.Report(ReportOptions{Profile: "full"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Problems) < len(FullSuites) {
		t.Fatal("missing suites were treated as successful")
	}
	for _, suite := range report.Suites {
		if suite.Status != "not-run" {
			t.Fatal("invented suite result", suite)
		}
	}
}

func TestFuzzProfileExecutesCampaignNotSeeds(t *testing.T) {
	p := fixture(t)
	put(t, p.Root, "fuzz_test.go", "package audit\nimport \"testing\"\nfunc FuzzPayload(f *testing.F) {}\n")
	var err error
	p.Inventory, err = Discover(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	calls, err := p.invocations("fuzz", false)
	if err != nil || len(calls) != 1 {
		t.Fatal(calls, err)
	}
	command := strings.Join(calls[0].args, " ")
	if !strings.Contains(command, "-fuzz=^FuzzPayload$") || !strings.Contains(command, "-fuzztime=5s") || !strings.Contains(command, "-run=^$") {
		t.Fatal("fuzz profile could only run seeds", command)
	}
}

func TestRealRunRejectsUnknownSuiteWithoutExecutingIt(t *testing.T) {
	p := fixture(t)
	if _, err := p.invocations("not-a-suite", false); err == nil {
		t.Fatal("accepted arbitrary execution profile")
	}
}

func TestALSASuitePinsHardwareOptOutAndDoesNotInheritEvidence(t *testing.T) {
	p := fixture(t)
	calls, err := p.invocations("alsa", false)
	if err != nil || len(calls) != 1 || calls[0].dir != "audio/alsa" || !slices.Contains(calls[0].env, "CGO_ENABLED=0") || !slices.Contains(calls[0].env, "SENDSPIN_ALSA_SILENCE_DEVICE=") {
		t.Fatal("ALSA suite must not inherit a hardware-test opt-in", calls, err)
	}
	if !slices.Contains(calls[0].env, "SENDSPIN_ALSA_REQUIRE_LIBRARY=1") {
		t.Fatal("dedicated ALSA evidence must require the real system library for its private null PCM")
	}
	if !slices.Contains(calls[0].env, "SENDSPIN_ALSA_DUPLEX_CAPTURE=") {
		t.Fatal("ordinary ALSA evidence must explicitly disable capture consent")
	}
	if !slices.Contains(calls[0].env, "SENDSPIN_ALSA_REFERENCE_PLAYBACK=") {
		t.Fatal("ordinary ALSA evidence must explicitly disable nonzero reference playback")
	}
	for _, flag := range []string{"SENDSPIN_ALSA_E2E_PLAYBACK=", "SENDSPIN_ALSA_E2E_VIRTUAL="} {
		if !slices.Contains(calls[0].env, flag) {
			t.Fatal("ordinary ALSA evidence must disable independent-server opt-ins")
		}
	}
	report, err := p.Report(ReportOptions{Profile: "alsa"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(report.Problems, "required suite alsa: not-run") || report.ConformanceClaim {
		t.Fatal("ALSA native/physical evidence was invented", report.Problems)
	}
}

func TestNativeProfileRequiresCurrentBackendsNotRetiredPortAudio(t *testing.T) {
	p := fixture(t)
	report, err := p.Report(ReportOptions{Profile: "native"})
	if err != nil {
		t.Fatal(err)
	}
	for _, suite := range []string{"desktop-audio", "examples"} {
		if !slices.Contains(report.Problems, "required suite "+suite+": not-run") {
			t.Fatal(report.Problems)
		}
	}
	for _, suite := range []string{"portaudio", "native-example"} {
		if _, err := p.invocations(suite, false); err == nil {
			t.Fatal("retired suite still executable", suite)
		}
		for _, problem := range report.Problems {
			if strings.Contains(problem, suite) {
				t.Fatal(problem)
			}
		}
	}
}

func TestBackendInventoryUsesRootPackagesAndSeparateOptInSuites(t *testing.T) {
	p := fixture(t)
	for _, file := range []string{"audio/pipewire/abi_test.go", "audio/internal/bridge/callback_test.go"} {
		put(t, p.Root, file, "package backend\nimport \"testing\"\nfunc TestABI(t *testing.T) {}\n")
	}
	put(t, p.Root, "audio/native/hardware_e2e_test.go", "//go:build e2e\n\npackage native\nimport \"testing\"\nfunc TestE2ENativeLoopback(t *testing.T) {}\n")
	put(t, p.Root, "audio/pipewire/isolated_e2e_test.go", "//go:build e2e && linux\n\npackage pipewire\nimport \"testing\"\nfunc TestE2EPrivatePipeWire(t *testing.T) {}\n")
	inv, err := Discover(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"desktop-audio:example.test/audit/audio/pipewire#TestABI", "desktop-audio:example.test/audit/audio/internal/bridge#TestABI", "desktop-audio-e2e:example.test/audit/audio/native#TestE2ENativeLoopback", "desktop-audio-e2e:example.test/audit/audio/pipewire#TestE2EPrivatePipeWire"} {
		if _, ok := inv.Tests[id]; !ok {
			t.Fatal("missing migrated test", id)
		}
	}
	calls, err := p.invocations("desktop-audio", false)
	if err != nil || len(calls) != 1 || calls[0].dir != "." || !slices.Contains(calls[0].args, "./audio/native/...") {
		t.Fatal(calls, err)
	}
	for _, call := range calls {
		if !slices.Contains(call.env, "CGO_ENABLED=0") {
			t.Fatal("backend suite requires CGO", call)
		}
	}
	for _, suite := range []string{"alsa-e2e", "desktop-audio-e2e"} {
		calls, err := p.invocations(suite, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range calls {
			if !slices.Contains(call.env, "SENDSPIN_AUDIOTEST_SOURCE_E2E_FAKE=") {
				t.Fatal("native E2E inherited software-only source opt-in", call)
			}
			for _, prefix := range []string{"ALSA", "PIPEWIRE", "COREAUDIO", "WASAPI", "AUDIOTEST"} {
				if !slices.Contains(call.env, "SENDSPIN_"+prefix+"_E2E_VIRTUAL=") {
					t.Fatal("native E2E can inherit a virtual fake", call)
				}
			}
		}
	}
}

func nativeHardwareReportFixture(t *testing.T, events string) Report {
	t.Helper()
	events = strings.ReplaceAll(events, `"Package":"example.test/audit"`, `"Package":"example.test/audit/audio/native"`)
	p := fixture(t)
	manifest, directory := evidenceFixture(t, p, events)
	manifest.Suite = "desktop-audio-e2e"
	manifest.Scope = suiteScope(manifest.Suite)
	calls, err := p.invocations(manifest.Suite, false)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Commands[0].Args = calls[0].args
	manifest.Commands[0].Directory = calls[0].dir
	manifest.Commands[0].Environment = executionEnv(calls[0])
	if err := os.Remove(filepath.Join(filepath.Dir(directory), "current.json")); err != nil {
		t.Fatal(err)
	}
	slot := filepath.Join(p.Root, "var/audit/evidence/desktop-audio-e2e/context")
	put(t, slot, "run-fixture/events.jsonl", events)
	if err := writeJSON(filepath.Join(slot, "run-fixture/manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(slot, "current.json"), struct {
		Manifest string `json:"manifest"`
	}{"run-fixture/manifest.json"}); err != nil {
		t.Fatal(err)
	}
	report, err := p.Report(ReportOptions{Profile: "desktop-audio-e2e"})
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestNativeSkipIsNotReportedAsHardwarePass(t *testing.T) {
	for _, name := range backendHardwareTests {
		t.Run(name, func(t *testing.T) {
			events := strings.ReplaceAll(validGoEvents, `"Action":"pass","Package":"example.test/audit","Test":"TestOne"`, `"Action":"skip","Package":"example.test/audit","Test":"TestOne"`)
			events = strings.ReplaceAll(events, "TestOne", name)
			report := nativeHardwareReportFixture(t, events)
			if !slices.Contains(report.Problems, "required suite desktop-audio-e2e: skip") || report.ConformanceClaim {
				t.Fatal("hardware skip promoted", report.Problems)
			}
		})
	}
}

func TestNativeHardwareRequiresInputNotOnlyOutput(t *testing.T) {
	// Successful output and marker tests must not hide an absent Source/Input run.
	events := `{"Action":"start","Package":"example.test/audit"}` + "\n"
	for _, name := range []string{"TestE2ENativeLoopback", "TestE2EReferenceServerPlaybackScenario"} {
		events += `{"Action":"run","Package":"example.test/audit","Test":"` + name + `"}` + "\n"
		events += `{"Action":"pass","Package":"example.test/audit","Test":"` + name + `"}` + "\n"
	}
	events += `{"Action":"pass","Package":"example.test/audit"}` + "\n"
	report := nativeHardwareReportFixture(t, events)
	if !slices.Contains(report.Problems, "required suite desktop-audio-e2e: not-run") || report.ConformanceClaim {
		t.Fatal("missing Source/Input E2E promoted", report.Problems)
	}
}

func TestHardwareNamesInAnotherPackageCannotSatisfyNativeEvidence(t *testing.T) {
	events := `{"Action":"start","Package":"example.test/other"}` + "\n"
	for _, name := range backendHardwareTests {
		events += `{"Action":"run","Package":"example.test/other","Test":"` + name + `"}` + "\n"
		events += `{"Action":"pass","Package":"example.test/other","Test":"` + name + `"}` + "\n"
	}
	events += `{"Action":"pass","Package":"example.test/other"}` + "\n"
	report := nativeHardwareReportFixture(t, events)
	if !slices.Contains(report.Problems, "required suite desktop-audio-e2e: not-run") {
		t.Fatal("same global test names in another package credited as native", report.Problems)
	}
}

func TestPrivatePipeWireSoftwarePassCannotSatisfyNativeHardware(t *testing.T) {
	events := strings.ReplaceAll(validGoEvents, "TestOne", "TestE2EPrivatePipeWire")
	events = strings.ReplaceAll(events, `"Package":"example.test/audit"`, `"Package":"example.test/audit/audio/pipewire"`)
	report := nativeHardwareReportFixture(t, events)
	if !slices.Contains(report.Problems, "required suite desktop-audio-e2e: not-run") || report.ConformanceClaim {
		t.Fatal("private software daemon credited as generic hardware", report.Problems)
	}
}

func TestOrdinaryUnitPassCannotCoverTaggedPeerTests(t *testing.T) {
	p := fixture(t)
	e := Evidence{Suite: "e2e", Package: "example.test/audit", Test: "TestE2EPeer"}
	e.ID = TestID(e.Suite, e.Package, e.Test)
	p.Catalog.Evidence = append(p.Catalog.Evidence, e)
	p.Catalog.Requirements[0].Evidence = append(p.Catalog.Requirements[0].Evidence, e.ID)
	evidenceFixture(t, p, validGoEvents)
	report, err := p.Report(ReportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Requirements[0].EvidenceStatus == "pass" {
		t.Fatal("ordinary unit pass promoted to E2E coverage")
	}
	for _, evidence := range report.Requirements[0].Evidence {
		if evidence.ID == e.ID && evidence.Status != "not-run" {
			t.Fatal("tagged E2E unexpectedly credited", evidence)
		}
	}
}

func TestHistoricalCommandsRemainStaleAfterGoEntrypointMigration(t *testing.T) {
	p := fixture(t)
	manifest, directory := evidenceFixture(t, p, validGoEvents)
	manifest.SourceSHA256 = strings.Repeat("f", 64)
	manifest.Commands[0].Kind = "harness"
	manifest.Commands[0].Args = []string{"run", "./internal/conformancetestcmd"}
	if err := writeJSON(filepath.Join(directory, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	report, err := p.Report(ReportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Requirements[0].Evidence[0].Status != "stale" || report.Requirements[0].EvidenceStatus == "pass" {
		t.Fatal("historical commands were revalidated or credited for the current checkout")
	}
}
