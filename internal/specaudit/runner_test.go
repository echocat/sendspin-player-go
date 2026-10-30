package specaudit

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestExampleSuiteRunsBothIndependentModulesWithoutCGO(t *testing.T) {
	p := fixture(t)
	calls, err := p.invocations("examples", false)
	if err != nil || len(calls) != 2 {
		t.Fatal("example suite must run both modules separately", calls, err)
	}
	manifest := Manifest{Suite: "examples", Scope: suiteScope("examples")}
	for i, directory := range []string{"_examples/player", "_examples/external-control"} {
		call := calls[i]
		if call.dir != directory || call.kind != "go" || !slices.Equal(call.args, []string{"test", "-json", "-count=1", "./..."}) || !slices.Equal(call.env, []string{"CGO_ENABLED=0"}) {
			t.Fatal("unexpected example module invocation", call)
		}
		manifest.Commands = append(manifest.Commands, Command{Directory: call.dir, Kind: call.kind, Args: call.args, Environment: executionEnv(call)})
	}
	if err = p.validateCommands(manifest); err != nil {
		t.Fatal("independent module commands rejected", err)
	}
	manifest.Commands = manifest.Commands[:1]
	if err = p.validateCommands(manifest); err == nil {
		t.Fatal("missing second example module accepted")
	}
	manifest.Commands = manifest.Commands[:2]
	manifest.Commands[0] = Command{Directory: ".", Kind: "go", Args: []string{"test", "-json", "-count=1", "./_examples/player", "./_examples/external-control"}, Environment: executionEnv(calls[0])}
	if err = p.validateCommands(manifest); err == nil {
		t.Fatal("obsolete root-module example invocation accepted")
	}
}

func TestActualFailedRunAndSourceMutationCannotReuseAPreviousPass(t *testing.T) {
	for _, mode := range []string{"compiler-failure", "test-failure", "source-mutated"} {
		t.Run(mode, func(t *testing.T) {
			p := fixture(t)
			put(t, p.Root, "_examples/player/go.mod", "module example.test/player\n\ngo 1.27.3\n")
			put(t, p.Root, "_examples/external-control/go.mod", "module example.test/external-control\n\ngo 1.27.3\n")
			put(t, p.Root, "_examples/external-control/host_test.go", "package host\nimport \"testing\"\nfunc TestHost(t *testing.T){}\n")
			fault := `t.Fatal("controlled runtime failure")`
			if mode == "source-mutated" {
				fault = `if err:=os.WriteFile("../../impl.go",[]byte("package audit\n// mutated by evidence fault fixture\n"),0600);err!=nil{t.Fatal(err)}`
			}
			body := "package player\nimport (\"testing\";\"os\")\nfunc TestOutput(t *testing.T) { if _,err:=os.Stat(\"../../var/trigger\");os.IsNotExist(err){return};" + fault + " }\n"
			put(t, p.Root, "_examples/player/output_test.go", body)
			var err error
			p, err = Load(p.Root)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("SENDSPIN_EVIDENCE_VARIANT", "default")
			slot := filepath.Join(p.Root, "var/audit/evidence/examples", runtime.GOOS+"-"+runtime.GOARCH+"-"+digest([]byte(runtime.Version() + "/default"))[:12])
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err = p.Run(ctx, "examples", false); err != nil {
				t.Fatal("could not establish an actual previous pass", err)
			}
			previous, previousDir, err := readManifest(filepath.Join(slot, "current.json"))
			if err != nil || previous.ExitCode != 0 || !previous.Complete || len(previous.Commands) != 2 {
				t.Fatal("missing successful baseline", err)
			}
			if mode == "compiler-failure" {
				put(t, p.Root, "_examples/player/output_test.go", "package player\nimport \"testing\"\nfunc TestOutput(t *testing.T){ doesNotCompile() }\n")
				p, err = Load(p.Root)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				put(t, p.Root, "var/trigger", "controlled runtime fault; not a source input")
			}
			if err = p.Run(ctx, "examples", false); err == nil {
				t.Fatal("invalid real execution was accepted")
			}
			if mode == "source-mutated" && !strings.Contains(err.Error(), "source inputs changed") {
				t.Fatal("source mutation was not the reason for rejection", err)
			}
			manifest, directory, err := readManifest(filepath.Join(slot, "current.json"))
			if err != nil {
				t.Fatal(err)
			}
			if !manifest.Complete || manifest.ExitCode == 0 || directory == previousDir {
				t.Fatal("failed run reused previous evidence", manifest)
			}
			wantCommands := 1
			if mode == "source-mutated" {
				wantCommands = 2
			}
			if len(manifest.Commands) != wantCommands {
				t.Fatal("missing actual command record")
			}
			if mode != "source-mutated" && manifest.Commands[0].ExitCode == 0 {
				t.Fatal("compiler/runtime failure recorded as successful command")
			}
			if mode == "source-mutated" && manifest.Commands[0].ExitCode != 0 {
				t.Fatal("source-mutation fixture did not actually pass its tests first")
			}
			if mode == "source-mutated" && manifest.Commands[1].ExitCode != 0 {
				t.Fatal("source-mutation fixture did not complete the second example module")
			}
			if _, err = os.Stat(filepath.Join(slot, "running.lock")); !os.IsNotExist(err) {
				t.Fatal("ordinary failure leaked execution lock", err)
			}
		})
	}
}

func TestEvidencePathsArePortableAndCannotEscape(t *testing.T) {
	for _, path := range []string{"../outside", "..\\outside", "C:\\outside", "/absolute", "run/../../outside"} {
		if portablePath(path) {
			t.Fatal("unsafe cross-platform path accepted", path)
		}
	}
	root := t.TempDir()
	put(t, root, "inside.json", "data")
	if data, err := readEvidenceFile(root, "inside.json"); err != nil || string(data) != "data" {
		t.Fatal(err)
	}
	if _, err := readEvidenceFile(root, "../outside.json"); err == nil {
		t.Fatal("escaping evidence read accepted")
	}
}

func TestRunnerCannotMislabelAnotherGoToolchainOrTarget(t *testing.T) {
	actual := goContext{Version: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}
	if err := checkGoContext(actual); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"version", "os", "arch"} {
		wrong := actual
		switch field {
		case "version":
			wrong.Version = "go0.0.1"
		case "os":
			wrong.OS = "other"
		case "arch":
			wrong.Arch = "other"
		}
		if err := checkGoContext(wrong); err == nil {
			t.Fatal("mislabeled execution context accepted", field)
		}
	}
}
