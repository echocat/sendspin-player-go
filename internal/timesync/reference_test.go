package timesync

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
)

const referenceRevision = "531e2707a6e82f30efe335b0f87eaa4b29390021"
const referenceGolden = "testdata/reference/golden.json"

var referenceBlobs = map[string]string{
	"sendspin_time_filter.h":   "5a8c245b9b3bea7d0dd39a697e02ae0576377569",
	"sendspin_time_filter.cpp": "0e009f9bf1e68336ae229f08e0fc06f8f20d3ad8",
}

var referenceFlags = []string{"-std=c++17", "-O2", "-ffp-contract=off", "-fno-fast-math", "-pthread"}
var referenceSanitizerFlags = []string{"-fsanitize=undefined,float-cast-overflow", "-fno-sanitize-recover=all"}

// Values are [server_time, client_time, initialized, error_us, covariance_us2,
// accepted_sample_count, drift_compensation, sdk_ready_predicate]. Before the
// first accepted sample error/covariance are sentinels, never C++ infinity casts.
type referenceValues [8]int64
type referenceOperation struct {
	kind    byte
	a, b, c int64
}
type referenceScenario struct {
	name       string
	tolerance  int64
	operations []referenceOperation
}
type goldenScenario struct {
	Name        string            `json:"name"`
	TraceSHA256 string            `json:"trace_sha256"`
	ToleranceUS int64             `json:"tolerance_us"`
	Values      []referenceValues `json:"values"`
}
type referenceFixture struct {
	Format          int               `json:"format"`
	Revision        string            `json:"revision"`
	Compiler        string            `json:"compiler"`
	Flags           []string          `json:"flags"`
	Instrumentation []string          `json:"instrumentation,omitempty"`
	Sources         map[string]string `json:"sources_sha256"`
	Scenarios       []goldenScenario  `json:"scenarios,omitempty"`
}

func referenceSources(t *testing.T) map[string]string {
	t.Helper()
	hashes := make(map[string]string)
	for _, name := range []string{"sendspin_time_filter.cpp", "sendspin_time_filter.h", "driver.cpp"} {
		data, err := os.ReadFile(filepath.Join("testdata", "reference", name))
		if err != nil {
			t.Fatal(err)
		}
		if expected, upstream := referenceBlobs[name]; upstream {
			// Verify the exact upstream Git object, including comments and LF.
			blob := sha1.New()
			_, _ = fmt.Fprintf(blob, "blob %d\x00", len(data)) // hash.Hash.Write cannot fail.
			_, _ = blob.Write(data)
			if got := hex.EncodeToString(blob.Sum(nil)); got != expected {
				t.Fatalf("%s differs from pinned upstream blob: got %s want %s", name, got, expected)
			}
		}
		sum := sha256.Sum256(data)
		hashes[name] = hex.EncodeToString(sum[:])
	}
	return hashes
}

func (s *referenceScenario) query(client, server int64) {
	s.operations = append(s.operations, referenceOperation{kind: 'q', a: client, b: server})
}
func (s *referenceScenario) reset() {
	s.operations = append(s.operations, referenceOperation{kind: 'r'})
	s.query(123456, 234567)
}
func (s *referenceScenario) update(measurement, maxError, at int64) {
	s.operations = append(s.operations, referenceOperation{kind: 'u', a: measurement, b: maxError, c: at})
	for _, delta := range []int64{-25000, 0, 150000, 60000000} {
		s.query(at+delta, at+delta+measurement)
	}
}
func (s referenceScenario) trace() []byte {
	var b bytes.Buffer
	for _, op := range s.operations {
		switch op.kind {
		case 'r':
			fmt.Fprintln(&b, "r")
		case 'u':
			fmt.Fprintf(&b, "u %d %d %d\n", op.a, op.b, op.c)
		case 'q':
			fmt.Fprintf(&b, "q %d %d\n", op.a, op.b)
		}
	}
	return b.Bytes()
}
func (s referenceScenario) digest() string {
	hash := sha256.Sum256(s.trace())
	return hex.EncodeToString(hash[:])
}

// Deliberately specified PRNG, independent of standard-library RNG changes.
type referenceRandom uint64

func (r *referenceRandom) noise(halfRange int64) int64 {
	*r = *r*6364136223846793005 + 1442695040888963407
	return int64(uint64(*r)%uint64(2*halfRange+1)) - halfRange
}

func referenceScenarios() []referenceScenario {
	var scenarios []referenceScenario
	add := func(name string) *referenceScenario {
		scenarios = append(scenarios, referenceScenario{name: name, tolerance: 1})
		s := &scenarios[len(scenarios)-1]
		s.reset()
		return s
	}
	startup := add("startup-and-rejected-samples")
	for _, sample := range [][3]int64{{0, 0, 0}, {100, 20, -1}, {100, 20, 0}, {100, 0, 100}, {100, -10, 100}, {123456, 100, 1000}, {999999, 10, 1000}, {999999, 10, 999}, {123457, 100, 2000}, {123458, 100, 3000}, {123459, 100, 4000}} {
		startup.update(sample[0], sample[1], sample[2])
	}
	for _, config := range []struct {
		name                       string
		offset, ppm, error, jitter int64
	}{
		{"constant-offset", 123456789, 0, 200, 0},
		{"negative-epoch-offset", -1700000000000000, 0, 250, 40},
		{"positive-drift", 34000000, 60, 200, 30},
		{"negative-drift", 8000, -150, 150, 20},
	} {
		s := add(config.name)
		random := referenceRandom(42)
		for i := int64(1); i <= 400; i++ {
			at := i * 10000000
			s.update(config.offset+at*config.ppm/1000000+random.noise(config.jitter), config.error, at)
		}
	}
	jitter := add("changing-network-uncertainty")
	random := referenceRandom(123)
	for i := int64(1); i <= 400; i++ {
		at := i * 10000000
		uncertainty := []int64{500, 5000, 30000, 1000}[i%4]
		jitter.update(900000+at*40/1000000+random.noise(uncertainty/2), uncertainty, at)
	}
	for _, early := range []bool{true, false} {
		name := "clock-step-after-warmup"
		step := int64(150)
		if early {
			name = "clock-step-before-warmup"
			step = 20
		}
		s := add(name)
		random := referenceRandom(321)
		for i := int64(1); i <= 400; i++ {
			offset := int64(0)
			if i >= step {
				offset += 200000
			}
			if i >= 300 {
				offset -= 400000
			}
			s.update(offset+random.noise(100), 500, i*10000000)
		}
	}
	outliers := add("isolated-outliers")
	random = referenceRandom(456)
	for i := int64(1); i <= 500; i++ {
		at := i * 10000000
		offset := 100000 + at*75/1000000 + random.noise(40)
		if i%73 == 0 {
			offset += 100000
		}
		outliers.update(offset, 200, at)
	}
	outage := add("six-hour-network-outage")
	at := int64(0)
	random = referenceRandom(789)
	for i := int64(1); i <= 400; i++ {
		at += 10000000
		if i == 250 {
			at += 6 * 60 * 60 * 1000000
		}
		uncertainty := int64(200)
		if i == 250 {
			uncertainty = 20000
		}
		outage.update(100000+at*40/1000000+random.noise(50), uncertainty, at)
	}
	wander := add("changing-clock-drift")
	offset := int64(50000)
	random = referenceRandom(246)
	for i := int64(1); i <= 600; i++ {
		ppm := []int64{20, 200, -150, 0}[(i-1)/150]
		offset += 10000000 * ppm / 1000000
		wander.update(offset+random.noise(100), 500, i*10000000)
	}
	bursts := add("minimum-uncertainty-bursts")
	random = referenceRandom(135)
	for i := int64(1); i <= 300; i++ {
		base := i * 10000000
		bestError := int64(math.MaxInt64)
		var bestOffset, bestAt int64
		correlated := random.noise(80)
		for j := int64(0); j < 8; j++ {
			at := base + j*5000
			uncertainty := 200 + random.noise(100) + j*20
			measurement := 30000 + at*35/1000000 + correlated + random.noise(30)
			if uncertainty < bestError {
				bestError, bestOffset, bestAt = uncertainty, measurement, at
			}
		}
		bursts.update(bestOffset, bestError, bestAt)
	}
	long := add("long-running-monotonic-clock")
	random = referenceRandom(975)
	for i := int64(1); i <= 300; i++ {
		local := int64(1700000000000000) + i*10000000
		long.update(-930000000000+i*600+random.noise(40), 250, local)
	}
	precision := add("double-precision-boundary")
	precision.tolerance = 2
	random = referenceRandom(864)
	for i := int64(1); i <= 80; i++ {
		local := int64(1<<53) - 100000 + i*2000
		precision.update(1024+random.noise(1), 4, local)
	}
	ready := add("readiness-and-uncertainty-recovery")
	random = referenceRandom(753)
	for i := int64(1); i <= 400; i++ {
		uncertainty := int64(1000000)
		if i > 200 {
			uncertainty = 100
		}
		ready.update(100000+random.noise(uncertainty/4), uncertainty, i*10000000)
	}
	reset := add("reset-and-reconvergence")
	for i := int64(1); i <= 150; i++ {
		reset.update(5000+i*40, 100, i*1000000)
	}
	reset.reset()
	reset.update(1, 0, 1000)
	reset.update(1, 100, -1)
	for i := int64(1); i <= 150; i++ {
		reset.update(-12000-i*100, 200, i*1000000)
	}
	return scenarios
}

func goValues(f *Filter, op referenceOperation) (referenceValues, error) {
	f.mu.RLock()
	count, variance, drift := f.count, f.p00, f.useDrift
	f.mu.RUnlock()
	values := referenceValues{f.ServerTime(op.a), f.ClientTime(op.b), 0, 0, 0, int64(count), 0, 0}
	server, serverOK := f.CheckedServerTime(op.a)
	client, clientOK := f.CheckedClientTime(op.b)
	if !serverOK || !clientOK || server != values[0] || client != values[1] {
		return values, fmt.Errorf("checked SDK mapping disagrees with reference-range query: server=%d/%v client=%d/%v", server, serverOK, client, clientOK)
	}
	if count > 0 {
		if variance < 0 || math.IsNaN(variance) || math.IsInf(variance, 0) {
			return values, fmt.Errorf("invalid initialized covariance %g", variance)
		}
		values[2] = 1
		values[3] = int64(f.Error())
		values[4] = int64(math.Round(variance))
	} else if !math.IsInf(f.Error(), 1) {
		return values, fmt.Errorf("uninitialized error should be infinity")
	}
	if drift {
		values[6] = 1
	}
	if f.Ready() {
		values[7] = 1
	}
	return values, nil
}

func compareReference(t *testing.T, scenario referenceScenario, expected []referenceValues) {
	t.Helper()
	filter := New()
	index := 0
	for step, op := range scenario.operations {
		switch op.kind {
		case 'r':
			filter = New()
		case 'u':
			filter.Update(op.a, op.b, op.c)
		case 'q':
			if index >= len(expected) {
				t.Fatalf("%s: missing reference query %d", scenario.name, index)
			}
			got, err := goValues(filter, op)
			if err != nil {
				t.Fatalf("%s operation %d: %v", scenario.name, step, err)
			}
			want := expected[index]
			for field := range got {
				tolerance := int64(0)
				if field < 2 {
					tolerance = scenario.tolerance
				}
				if field == 3 || field == 4 {
					tolerance = 1
				}
				if !referenceClose(got[field], want[field], tolerance) {
					t.Fatalf("%s query %d (operation %d), field %d: got %d want %d (tolerance %d)\nGo: %v\nC++: %v", scenario.name, index, step, field, got[field], want[field], tolerance, got, want)
				}
			}
			index++
		}
	}
	if index != len(expected) {
		t.Fatalf("%s: %d Go queries, %d reference outputs", scenario.name, index, len(expected))
	}
}

func referenceClose(a, b, tolerance int64) bool {
	if a >= b {
		return uint64(a)-uint64(b) <= uint64(tolerance)
	}
	return uint64(b)-uint64(a) <= uint64(tolerance)
}

func TestReferenceComparisonCannotHideOverflow(t *testing.T) {
	if referenceClose(math.MinInt64, math.MaxInt64, 1) || referenceClose(math.MaxInt64, math.MinInt64, 1) {
		t.Fatal("reference comparator wrapped an extreme mismatch into tolerance")
	}
	if !referenceClose(-1, 0, 1) || !referenceClose(1, 0, 1) || referenceClose(2, 0, 1) {
		t.Fatal("incorrect signed reference tolerance")
	}
}

func TestPinnedReferenceVectors(t *testing.T) {
	sources := referenceSources(t)
	data, err := os.ReadFile(referenceGolden)
	if err != nil {
		t.Fatal(err)
	}
	var fixture referenceFixture
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Format != 1 || fixture.Revision != referenceRevision || fixture.Compiler == "" {
		t.Fatal("invalid reference provenance")
	}
	for name, sum := range sources {
		if fixture.Sources[name] != sum {
			t.Fatalf("%s changed: regenerate reference vectors deliberately", name)
		}
	}
	if fmt.Sprint(fixture.Flags) != fmt.Sprint(referenceFlags) {
		t.Fatal("reference compiler flags changed")
	}
	if len(fixture.Instrumentation) > 0 && fmt.Sprint(fixture.Instrumentation) != fmt.Sprint(referenceSanitizerFlags) {
		t.Fatal("unknown reference instrumentation")
	}
	scenarios := referenceScenarios()
	if len(scenarios) != len(fixture.Scenarios) {
		t.Fatal("reference scenario inventory changed")
	}
	for i, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			expected := fixture.Scenarios[i]
			if expected.Name != scenario.name || expected.TraceSHA256 != scenario.digest() || expected.ToleranceUS != scenario.tolerance {
				t.Fatal("reference inputs changed: regenerate C++ vectors deliberately")
			}
			compareReference(t, scenario, expected.Values)
		})
	}
}
