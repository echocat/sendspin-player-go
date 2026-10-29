package specaudit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/echocat/sendspin-player-go/internal/devpath"
)

var FullSuites = []string{"unit", "e2e", "mdns", "harness", "fuzz", "reference", "examples"}

type Command struct {
	Args        []string `json:"args"`
	Directory   string   `json:"directory"`
	Kind        string   `json:"kind"`
	Log         string   `json:"log"`
	SHA256      string   `json:"sha256"`
	ExitCode    int      `json:"exit_code"`
	Environment []string `json:"environment,omitempty"`
}

type Manifest struct {
	Schema        int       `json:"schema"`
	Suite         string    `json:"suite"`
	CatalogSHA256 string    `json:"catalog_sha256"`
	SourceSHA256  string    `json:"source_sha256"`
	GOOS          string    `json:"goos"`
	GOARCH        string    `json:"goarch"`
	GoVersion     string    `json:"go_version"`
	Variant       string    `json:"variant"`
	CIRun         string    `json:"ci_run,omitempty"`
	Scope         string    `json:"scope"`
	Started       time.Time `json:"started"`
	Finished      time.Time `json:"finished"`
	Complete      bool      `json:"complete"`
	ExitCode      int       `json:"exit_code"`
	Commands      []Command `json:"commands"`
}

type invocation struct {
	args      []string
	dir, kind string
	env       []string
}

func (p *Project) invocations(suite string, coverage bool) ([]invocation, error) {
	if coverage && suite != "unit" {
		return nil, errors.New("coverage is supported only for the unit suite")
	}
	goTest := func(dir string, args ...string) []invocation {
		return []invocation{{dir: dir, kind: "go", args: append([]string{"test", "-json", "-count=1"}, args...)}}
	}
	switch suite {
	case "unit":
		args := []string{"-race"}
		if coverage {
			args = append(args, "-covermode=atomic", "-coverprofile="+filepath.Join(p.Root, "var", "coverage", "unit.cov"))
		}
		args = append(args, "./...")
		return goTest(".", args...), nil
	case "e2e":
		// Each platform starts many independent Python peers serially. The
		// first real CI run made continuous successful progress for five
		// minutes on Windows/macOS before the aggregate test budget expired.
		// This changes only the suite budget, never protocol/peer deadlines.
		// The transport campaign adds twelve paced/reconnection scenarios;
		// increase only the aggregate suite budget, never per-peer deadlines.
		return goTest(".", "-race", "-tags=e2e", "-timeout=15m", "."), nil
	case "reference":
		return goTest(".", "-race", "-tags=timefilter_reference", "-timeout=2m", "./internal/timesync"), nil
	case "examples":
		calls := append(goTest("_examples/player", "./..."), goTest("_examples/audio-probe", "./...")...)
		calls = append(calls, goTest("_examples/loopback", "./...")...)
		for i := range calls {
			calls[i].env = []string{"CGO_ENABLED=0"}
		}
		return calls, nil
	case "portaudio":
		calls := goTest("audio/portaudio", "-race", "./...")
		calls[0].env = []string{"CGO_ENABLED=1"}
		return calls, nil
	case "alsa":
		calls := goTest("audio/alsa", "-race", "./...")
		calls[0].env = []string{"CGO_ENABLED=1", "SENDSPIN_ALSA_SILENCE_DEVICE="}
		return calls, nil
	case "desktop-audio":
		calls := goTest("audio/native", "-race", "./...")
		calls[0].env = []string{"CGO_ENABLED=1", "CGO_CFLAGS_ALLOW=-fno-strict-overflow", "SENDSPIN_PIPEWIRE_ISOLATED="}
		return calls, nil
	case "native-example":
		calls := append(goTest("_examples/player", "-race", "./..."), goTest("_examples/audio-probe", "-race", "./...")...)
		for i := range calls {
			calls[i].env = []string{"CGO_ENABLED=1"}
		}
		return calls, nil
	case "fuzz":
		var tests []Test
		for _, test := range p.Inventory.Tests {
			if test.Suite == "fuzz" {
				tests = append(tests, test)
			}
		}
		slices.SortFunc(tests, func(a, b Test) int { return strings.Compare(a.ID, b.ID) })
		var calls []invocation
		for _, test := range tests {
			calls = append(calls, invocation{dir: ".", kind: "go", args: []string{"test", "-json", "-count=1", test.Package, "-run=^$", "-fuzz=^" + test.Name + "$", "-fuzztime=5s", "-parallel=2"}})
		}
		if len(calls) == 0 {
			return nil, errors.New("empty fuzz inventory")
		}
		return calls, nil
	case "mdns", "mdns-docker":
		args := []string{"run", "./internal/mdnstestcmd"}
		if suite == "mdns-docker" {
			args = append(args, "--engine", "docker")
		}
		return []invocation{{dir: ".", kind: "mdns", args: args}}, nil
	case "harness":
		return []invocation{{dir: ".", kind: "harness", args: []string{"run", "./internal/conformancetestcmd"}}}, nil
	}
	return nil, fmt.Errorf("unknown evidence suite %q", suite)
}

// Run records the latest explicit invocation per suite/platform/toolchain. It
// publishes an incomplete manifest BEFORE running, so a killed run cannot reuse
// a previous pass. Logs remain in unique subdirectories for diagnosis.
func (p *Project) Run(parent context.Context, suite string, coverage bool) error {
	calls, err := p.invocations(suite, coverage)
	if err != nil {
		return err
	}
	if suite == "mdns-docker" {
		suite = "mdns"
	}
	if err = devpath.Prepare(p.Root); err != nil {
		return err
	}
	if coverage {
		if err = os.MkdirAll(filepath.Join(p.Root, "var", "coverage"), 0700); err != nil {
			return err
		}
	}
	variant := os.Getenv("SENDSPIN_EVIDENCE_VARIANT")
	if variant == "" {
		variant = "default"
	}
	slot := filepath.Join(p.Root, "var", "audit", "evidence", suite, runtime.GOOS+"-"+runtime.GOARCH+"-"+digest([]byte(runtime.Version() + "/" + variant))[:12])
	if err = os.MkdirAll(slot, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(slot, "running.lock"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("evidence slot busy (after a forced kill, inspect/remove its running.lock): %w", err)
	}
	if err = lock.Close(); err != nil {
		return err
	}
	defer func() { _ = os.Remove(filepath.Join(slot, "running.lock")) }()
	directory, err := os.MkdirTemp(slot, "run-")
	if err != nil {
		return err
	}
	manifest := Manifest{Schema: 1, Suite: suite, CatalogSHA256: p.CatalogDigest, SourceSHA256: p.SourceDigest, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, GoVersion: runtime.Version(), Variant: variant, CIRun: ciRun(), Scope: suiteScope(suite), Started: time.Now().UTC(), ExitCode: -1}
	current := filepath.Join(slot, "current.json")
	if err = writeJSON(filepath.Join(directory, "manifest.json"), manifest); err != nil {
		return err
	}
	if err = writeJSON(current, struct {
		Manifest string `json:"manifest"`
	}{filepath.ToSlash(filepath.Join(filepath.Base(directory), "manifest.json"))}); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Minute)
	defer cancel()
	var runErr error
	for i, call := range calls {
		command, callErr := p.execute(ctx, directory, i, call)
		manifest.Commands = append(manifest.Commands, command)
		if callErr != nil {
			runErr = callErr
			break
		}
	}
	manifest.Complete = true
	manifest.Finished = time.Now().UTC()
	manifest.ExitCode = 0
	if runErr != nil {
		manifest.ExitCode = 1
	}
	if after, hashErr := Fingerprint(p.Root); hashErr != nil || after != p.SourceDigest {
		manifest.ExitCode = 1
		runErr = errors.Join(runErr, errors.New("source inputs changed while the evidence suite was running"), hashErr)
	}
	if err = writeJSON(filepath.Join(directory, "manifest.json"), manifest); err != nil {
		return errors.Join(runErr, err)
	}
	fmt.Fprintf(os.Stderr, "[audit] %s: exit=%d; evidence %s\n", suite, manifest.ExitCode, current)
	return runErr
}

func (p *Project) execute(ctx context.Context, directory string, index int, call invocation) (Command, error) {
	logName := fmt.Sprintf("command-%02d.jsonl", index)
	if call.kind != "go" {
		logName = fmt.Sprintf("command-%02d.log", index)
	}
	log, err := os.Create(filepath.Join(directory, logName))
	if err != nil {
		return Command{}, err
	}
	args := slices.Clone(call.args)
	if call.kind == "harness" {
		args = append(args, "--manifest", filepath.Join(directory, "harness.json"))
	}
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = filepath.Join(p.Root, filepath.FromSlash(call.dir))
	cmd.Env = append(os.Environ(), executionEnv(call)...)
	cmd.Stdout = log
	cmd.Stderr = io.MultiWriter(os.Stderr, log)
	// go test writes non-JSON compiler diagnostics to stderr. Keep these out of
	// its event stream; a nonzero command exit is authoritative either way.
	if call.kind == "go" {
		cmd.Stderr = os.Stderr
	}
	fmt.Fprintf(os.Stderr, "[audit] %s: go %s\n", call.dir, strings.Join(args, " "))
	// Do not label a prebuilt runner's results with its own version when PATH,
	// GOTOOLCHAIN or a nested module would actually select another Go context.
	err = p.checkExecutionContext(ctx, call)
	if err == nil {
		err = cmd.Run()
	}
	closeErr := log.Close()
	entry := Command{Args: args, Directory: call.dir, Kind: call.kind, Log: logName, Environment: executionEnv(call), ExitCode: 0}
	if err != nil {
		entry.ExitCode = 1
	}
	if call.kind == "harness" {
		entry.Log = "harness.json"
	}
	if call.kind == "mdns" && err == nil {
		entry.Log = "mdns.jsonl"
		input, openErr := os.Open(filepath.Join(p.Root, "var", "mdns", "tests.log"))
		if openErr != nil {
			return entry, openErr
		}
		defer func() { _ = input.Close() }()
		output, createErr := os.Create(filepath.Join(directory, entry.Log))
		if createErr != nil {
			return entry, createErr
		}
		convert := exec.CommandContext(ctx, "go", "tool", "test2json", "-p", "github.com/echocat/sendspin-player-go")
		convert.Stdin = input
		convert.Stdout = output
		convert.Stderr = os.Stderr
		err = convert.Run()
		closeErr = errors.Join(closeErr, output.Close())
	}
	data, readErr := os.ReadFile(filepath.Join(directory, entry.Log))
	if readErr == nil {
		entry.SHA256 = digest(data)
	}
	if err == nil && closeErr == nil && readErr == nil {
		var results []Result
		if call.kind == "harness" {
			results, err = ReadHarnessResults(strings.NewReader(string(data)))
		} else {
			results, err = ReadGoResults(strings.NewReader(string(data)))
		}
		if err == nil && len(results) == 0 {
			err = errors.New("command produced no test evidence")
		}
		for _, result := range results {
			if result.Status == "fail" || result.Status == "incomplete" {
				err = fmt.Errorf("incomplete/failed evidence: %s %s", result.Package, result.Name)
				break
			}
		}
	}
	return entry, errors.Join(err, closeErr, readErr)
}

type goContext struct {
	Version string `json:"GOVERSION"`
	OS      string `json:"GOOS"`
	Arch    string `json:"GOARCH"`
}

func checkGoContext(actual goContext) error {
	if actual.Version != runtime.Version() || actual.OS != runtime.GOOS || actual.Arch != runtime.GOARCH {
		return errors.New("executed Go context differs from the evidence runner; invoke it with the same toolchain/target")
	}
	return nil
}

func (p *Project) checkExecutionContext(ctx context.Context, call invocation) error {
	command := exec.CommandContext(ctx, "go", "env", "-json", "GOVERSION", "GOOS", "GOARCH")
	command.Dir = filepath.Join(p.Root, filepath.FromSlash(call.dir))
	command.Env = append(os.Environ(), executionEnv(call)...)
	data, err := command.Output()
	if err != nil {
		return fmt.Errorf("cannot verify execution Go context: %w", err)
	}
	var actual goContext
	if err = json.Unmarshal(data, &actual); err != nil {
		return err
	}
	return checkGoContext(actual)
}

func ciRun() string {
	if os.Getenv("GITHUB_RUN_ID") == "" {
		return ""
	}
	return os.Getenv("GITHUB_RUN_ID") + "/" + os.Getenv("GITHUB_RUN_ATTEMPT")
}

// Only evidence subprocesses use a controlled module graph. This leaves shared
// build/download caches, system temp paths and the user's global Go settings alone.
func executionEnv(call invocation) []string {
	return append([]string{"GOWORK=off", "GOFLAGS=-mod=readonly"}, call.env...)
}

func suiteScope(suite string) string {
	switch suite {
	case "unit":
		return "Host Go unit/integration tests; fake audio, not physical output"
	case "e2e":
		return "Independent loopback reference; real protocol, simulated audio output"
	case "mdns":
		return "Real IPv4 multicast in isolated Linux container; not native host-network validation"
	case "harness":
		return "15 selected official loopback scenarios; simulated audio sink"
	case "reference":
		return "Host C++ algorithm comparison; not hardware clock accuracy"
	case "fuzz":
		return "Bounded host fuzz campaigns; not exhaustive input coverage"
	case "portaudio", "native-example":
		return "Host native build/callback/selection tests; no physical output measurement"
	case "alsa":
		return "Linux ALSA ABI with discard-only null PCM and fake timing/lifecycle tests; no physical device, capture or synchronization measurement"
	case "desktop-audio":
		return "Host direct PipeWire/WASAPI/CoreAudio build, C clock/callback ABI and fake queue/lifecycle tests; no audio device or server connection, capture or physical measurement"
	case "examples":
		return "CGO-free example selection and offline synthetic WAV analysis tests; no audio device, capture or physical measurement"
	}
	return ""
}

func (p *Project) validateCommands(manifest Manifest) error {
	if manifest.Scope != suiteScope(manifest.Suite) || manifest.Scope == "" {
		return errors.New("evidence scope mismatch")
	}
	coverage := false
	for _, command := range manifest.Commands {
		for _, arg := range command.Args {
			if strings.HasPrefix(arg, "-coverprofile=") {
				coverage = true
			}
		}
	}
	suite := manifest.Suite
	if suite == "mdns" && len(manifest.Commands) == 1 && slices.Contains(manifest.Commands[0].Args, "docker") {
		suite = "mdns-docker"
	}
	want, err := p.invocations(suite, coverage)
	if err != nil {
		return err
	}
	if len(want) != len(manifest.Commands) {
		return errors.New("missing/extra suite commands")
	}
	for i, call := range want {
		got := manifest.Commands[i]
		args := slices.Clone(got.Args)
		if call.kind == "harness" {
			if len(args) < 2 || args[len(args)-2] != "--manifest" || filepath.Base(strings.ReplaceAll(args[len(args)-1], "\\", "/")) != "harness.json" {
				return errors.New("harness has no explicit result sidecar")
			}
			args = args[:len(args)-2]
		}
		for j, arg := range args {
			if strings.HasPrefix(arg, "-coverprofile=") {
				args[j] = "-coverprofile=<artifact>"
			}
		}
		expected := slices.Clone(call.args)
		for j, arg := range expected {
			if strings.HasPrefix(arg, "-coverprofile=") {
				expected[j] = "-coverprofile=<artifact>"
			}
		}
		if got.Directory != call.dir || got.Kind != call.kind || !slices.Equal(got.Environment, executionEnv(call)) || !slices.Equal(args, expected) {
			return errors.New("evidence command does not match the declared suite (tags, count, race, fuzz or environment)")
		}
	}
	return nil
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".audit-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err = file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func readManifest(current string) (Manifest, string, error) {
	var pointer struct {
		Manifest string `json:"manifest"`
	}
	data, err := os.ReadFile(current)
	if err != nil {
		return Manifest{}, "", err
	}
	if err = decodeStrict(data, &pointer); err != nil {
		return Manifest{}, "", err
	}
	if !portablePath(pointer.Manifest) {
		return Manifest{}, "", errors.New("unsafe manifest path")
	}
	path := filepath.Join(filepath.Dir(current), filepath.FromSlash(pointer.Manifest))
	data, err = readEvidenceFile(filepath.Dir(current), pointer.Manifest)
	if err != nil {
		return Manifest{}, "", err
	}
	var manifest Manifest
	if err = decodeStrict(data, &manifest); err != nil {
		return Manifest{}, "", err
	}
	return manifest, filepath.Dir(path), nil
}

func readEvidenceFile(directory, relative string) ([]byte, error) {
	if !portablePath(relative) {
		return nil, errors.New("unsafe evidence path")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	file, err := root.Open(filepath.FromSlash(relative))
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("non-regular evidence input")
	}
	data, err := io.ReadAll(io.LimitReader(file, (128<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 128<<20 {
		return nil, errors.New("evidence log exceeds the reader limit")
	}
	return data, nil
}
