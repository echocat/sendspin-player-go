//go:build timefilter_reference

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
	"testing"
	"time"
)

var updateReference = flag.Bool("update-timefilter-reference", false, "Regenerate frozen vectors from the unmodified C++ reference (never Go outputs)")

func compileReference(t *testing.T) (string, string) {
	t.Helper()
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
		t.Fatal("live time-filter comparison requires a native C++17 compiler; install GCC/Clang or set CXX to its executable path")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	version, err := exec.CommandContext(ctx, compiler, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("CXX version: %v\n%s", err, version)
	}
	fingerprint, _, _ := strings.Cut(strings.TrimSpace(string(version)), "\n")
	executable := filepath.Join(t.TempDir(), "time-filter-reference")
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
		t.Fatalf("compile C++ reference: %v\n%s", err, out)
	}
	return executable, fingerprint
}

func runReference(t *testing.T, executable string, scenario referenceScenario) []referenceValues {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable)
	cmd.Stdin = bytes.NewReader(scenario.trace())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("C++ scenario %s: %v\n%s", scenario.name, err, stderr.String())
	}
	reader := bytes.NewReader(out)
	var rows []referenceValues
	for {
		var row referenceValues
		for field := range row {
			_, err = fmt.Fscan(reader, &row[field])
			if err != nil {
				if err == io.EOF && field == 0 {
					return rows
				}
				t.Fatalf("C++ output %s row %d field %d: %v", scenario.name, len(rows), field, err)
			}
		}
		rows = append(rows, row)
	}
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

func TestCPPReference(t *testing.T) {
	sources := referenceSources(t)
	executable, compiler := compileReference(t)
	fixture := referenceFixture{Format: 1, Revision: referenceRevision, Compiler: compiler, Flags: referenceFlags, Sources: sources}
	if os.Getenv("SENDSPIN_REFERENCE_UBSAN") == "1" {
		fixture.Instrumentation = referenceSanitizerFlags
	}
	scenarios := referenceScenarios()
	for _, scenario := range scenarios {
		fixture.Scenarios = append(fixture.Scenarios, goldenScenario{Name: scenario.name, TraceSHA256: scenario.digest(), ToleranceUS: scenario.tolerance, Values: runReference(t, executable, scenario)})
	}
	if *updateReference {
		writeReferenceFixture(t, fixture)
	}
	for i, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) { compareReference(t, scenario, fixture.Scenarios[i].Values) })
	}
}
