//go:build e2e

package timesync

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var updateReference = flag.Bool("update-timefilter-reference", false, "Regenerate frozen vectors from the unmodified C++ reference (never Go outputs)")

// TestMain owns the package deadline and every live-reference resource. Lazy
// setup means -short and -run selections that omit the E2E test need no C++.
var liveReference struct {
	ctx                 context.Context
	once                sync.Once
	directory, compiler string
	rows                [][]referenceValues
	err                 error
	missingCompiler     bool
}

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	liveReference.ctx = ctx
	code := m.Run()
	cancel()
	if liveReference.directory != "" {
		if err := os.RemoveAll(liveReference.directory); err != nil {
			fmt.Fprintln(os.Stderr, "clean C++ reference:", err)
			code = 1
		}
	}
	os.Exit(code)
}

func compileReference(ctx context.Context) (string, string, error) {
	compiler := os.Getenv("CXX")
	if compiler == "" {
		for _, candidate := range []string{"c++", "g++", "clang++"} {
			if path, err := exec.LookPath(candidate); err == nil {
				compiler = path
				break
			}
		}
	}
	if compiler == "" {
		liveReference.missingCompiler = true
		return "", "", fmt.Errorf("live time-filter comparison requires a native C++17 compiler; install GCC/Clang or set CXX to its executable path")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	version, err := exec.CommandContext(ctx, compiler, "--version").CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("CXX version: %w\n%s", err, version)
	}
	fingerprint, _, _ := strings.Cut(strings.TrimSpace(string(version)), "\n")
	directory, err := os.MkdirTemp("", "sendspin-time-filter-reference-")
	if err != nil {
		return "", "", err
	}
	liveReference.directory = directory
	executable := filepath.Join(directory, "time-filter-reference")
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	args := append([]string(nil), referenceFlags...)
	if os.Getenv("SENDSPIN_REFERENCE_UBSAN") == "1" {
		args = append(args, referenceSanitizerFlags...)
	}
	args = append(args, "testdata/reference/sendspin_time_filter.cpp", "testdata/reference/driver.cpp", "-o", executable)
	out, err := exec.CommandContext(ctx, compiler, args...).CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("compile C++ reference: %w\n%s", err, out)
	}
	return executable, fingerprint, nil
}

func runReference(ctx context.Context, executable string, scenarios []referenceScenario) ([][]referenceValues, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var input bytes.Buffer
	for _, scenario := range scenarios {
		// Each trace begins with r; explicit boundaries prevent state leakage.
		input.WriteString("r\n")
		input.Write(scenario.trace())
	}
	cmd := exec.CommandContext(ctx, executable)
	cmd.Stdin = &input
	cmd.WaitDelay = 5 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("C++ batch: %w\n%s", err, stderr.String())
	}
	reader := bytes.NewReader(out)
	rows := make([][]referenceValues, len(scenarios))
	for i, scenario := range scenarios {
		for _, op := range scenario.operations {
			if op.kind != 'q' {
				continue
			}
			var row referenceValues
			for field := range row {
				if _, err = fmt.Fscan(reader, &row[field]); err != nil {
					return nil, fmt.Errorf("C++ output %s row %d field %d: %w", scenario.name, len(rows[i]), field, err)
				}
			}
			rows[i] = append(rows[i], row)
		}
	}
	var extra string
	if _, err = fmt.Fscan(reader, &extra); err != io.EOF {
		return nil, fmt.Errorf("C++ batch expected EOF after all queries, got %q (%v)", extra, err)
	}
	return rows, nil
}

func writeReferenceFixture(t *testing.T, fixture referenceFixture) {
	t.Helper()
	var out bytes.Buffer
	// One observation per line keeps generated-vector diffs reviewable.
	header := fixture
	header.Scenarios = nil
	data, err := json.MarshalIndent(header, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	out.Write(bytes.TrimSuffix(data, []byte("\n}")))
	out.WriteString(",\n  \"scenarios\": [\n")
	for i, scenario := range fixture.Scenarios {
		if i > 0 {
			out.WriteString(",\n")
		}
		name, _ := json.Marshal(scenario.Name)
		fmt.Fprintf(&out, "    {\"name\": %s, \"trace_sha256\": %q, \"tolerance_us\": %d, \"values\": [\n", name, scenario.TraceSHA256, scenario.ToleranceUS)
		for j, row := range scenario.Values {
			if j > 0 {
				out.WriteString(",\n")
			}
			data, _ := json.Marshal(row)
			out.WriteString("      ")
			out.Write(data)
		}
		out.WriteString("\n    ]}")
	}
	out.WriteString("\n  ]\n}\n")
	if !json.Valid(out.Bytes()) {
		t.Fatal("generated invalid reference JSON")
	}
	if err = os.WriteFile(referenceGolden, out.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestE2ECPPReference(t *testing.T) {
	if testing.Short() {
		t.Skip("live C++ reference is disabled by -short")
	}
	sources := referenceSources(t)
	scenarios := referenceScenarios()
	if len(scenarios) != 16 {
		t.Fatalf("reference trace inventory changed: %d", len(scenarios))
	}
	liveReference.once.Do(func() {
		var executable string
		executable, liveReference.compiler, liveReference.err = compileReference(liveReference.ctx)
		if liveReference.err == nil {
			liveReference.rows, liveReference.err = runReference(liveReference.ctx, executable, scenarios)
		}
	})
	if liveReference.missingCompiler {
		t.Skip(liveReference.err)
	}
	if liveReference.err != nil {
		t.Fatal(liveReference.err)
	}
	fixture := referenceFixture{Format: 1, Revision: referenceRevision, Compiler: liveReference.compiler, Flags: referenceFlags, Sources: sources}
	if os.Getenv("SENDSPIN_REFERENCE_UBSAN") == "1" {
		fixture.Instrumentation = referenceSanitizerFlags
	}
	for i, scenario := range scenarios {
		fixture.Scenarios = append(fixture.Scenarios, goldenScenario{Name: scenario.name, TraceSHA256: scenario.digest(), ToleranceUS: scenario.tolerance, Values: liveReference.rows[i]})
	}
	if *updateReference {
		writeReferenceFixture(t, fixture)
	}
	for i, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) { compareReference(t, scenario, fixture.Scenarios[i].Values) })
	}
}
