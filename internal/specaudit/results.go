package specaudit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

var HarnessScenarios = []string{
	"client-initiated-pcm", "server-initiated-pcm",
	"server-initiated-protocol-baseline-v1", "server-initiated-audio-chunk-framing",
	"server-initiated-pcm-24bit", "server-initiated-flac", "server-initiated-opus",
	"client-initiated-state-format-pcm", "client-initiated-state-format-flac",
	"client-initiated-metadata", "server-initiated-metadata",
	"client-initiated-controller", "server-initiated-controller",
	"client-initiated-artwork", "server-initiated-artwork",
}

type GoEvent struct {
	Action  string
	Package string
	Test    string
	Output  string
}

type Result struct {
	Package string `json:"package,omitempty"`
	Name    string `json:"name"`
	Status  string `json:"status"`
}

// ReadGoResults never infers passes from log text. Only terminal test2json
// actions count, and a parent pass cannot hide a failed/skipped child.
func ReadGoResults(reader io.Reader) ([]Result, error) {
	d := json.NewDecoder(reader)
	tests := map[string]Result{}
	packages := map[string]string{}
	seen := map[string]bool{}
	started := map[string]bool{}
	for {
		var event GoEvent
		if err := d.Decode(&event); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("invalid/truncated test2json: %w", err)
		}
		if event.Action == "" {
			return nil, errors.New("test2json record has no action")
		}
		if event.Package == "" {
			continue
		} // Includes go test build-output records.
		if event.Test == "" {
			if event.Action == "start" {
				started[event.Package] = true
			}
			if event.Action == "pass" || event.Action == "fail" || event.Action == "skip" {
				packages[event.Package] = event.Action
			}
			continue
		}
		key := event.Package + "#" + event.Test
		if event.Action == "run" {
			if seen[key] {
				return nil, fmt.Errorf("repeated test run (expected count=1): %s", key)
			}
			seen[key] = true
			tests[key] = Result{event.Package, event.Test, "incomplete"}
		}
		if event.Action == "pass" || event.Action == "fail" || event.Action == "skip" {
			if !seen[key] && event.Action != "skip" {
				return nil, fmt.Errorf("test terminal event without run: %s", key)
			}
			previous := tests[key]
			if previous.Status == "fail" || previous.Status == "skip" {
				continue
			}
			tests[key] = Result{event.Package, event.Test, event.Action}
		}
	}
	results := make([]Result, 0, len(tests))
	for _, result := range tests {
		if !started[result.Package] || packages[result.Package] != "pass" {
			result.Status = "incomplete"
			if packages[result.Package] == "fail" {
				result.Status = "fail"
			}
		}
		if result.Status == "pass" {
			for _, child := range tests {
				if child.Package == result.Package && strings.HasPrefix(child.Name, result.Name+"/") {
					if child.Status == "fail" {
						result.Status = "fail"
						break
					}
					if child.Status == "skip" || child.Status == "incomplete" {
						result.Status = "partial"
					}
				}
			}
		}
		results = append(results, result)
	}
	return results, nil
}

type HarnessRun struct {
	Implementation    string `json:"implementation"`
	HarnessRevision   string `json:"harness_revision"`
	Specification     string `json:"sdk_specification"`
	SpecRevision      string `json:"audited_spec_revision"`
	AssertionRevision string `json:"protocol_assertion_revision"`
	Reference         string `json:"reference_package"`
	Results           []struct {
		Scenario    string `json:"scenario_id"`
		Status      string `json:"status"`
		ServerExit  *int   `json:"server_exit_code"`
		ClientExit  *int   `json:"client_exit_code"`
		Unsupported *bool  `json:"unsupported"`
		Client      string `json:"client_impl"`
		Server      string `json:"server_impl"`
	} `json:"results"`
}

func ReadHarnessResults(reader io.Reader) ([]Result, error) {
	data, err := io.ReadAll(io.LimitReader(reader, (16<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 16<<20 {
		return nil, errors.New("harness result too large")
	}
	if err = uniqueJSONKeys(json.NewDecoder(bytes.NewReader(data))); err != nil {
		return nil, err
	}
	var run HarnessRun
	d := json.NewDecoder(bytes.NewReader(data))
	if err = d.Decode(&run); err != nil {
		return nil, err
	}
	var trailing any
	if err = d.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing harness JSON")
	}
	if run.Implementation != "sendspin-player-go" || run.HarnessRevision != "fa6dc45795f0810c5810af9f9b828595b7267ece" || run.Specification != "1.0.0-rc1" || run.SpecRevision != "671a34d408581fc25ea56b3528a4a3f13e3be901" || run.AssertionRevision != "8c9577ea8719ad082d051ec13cc73ef15ed68948" || run.Reference != "10.2.1" {
		return nil, errors.New("harness evidence pin/implementation mismatch")
	}
	seen := map[string]bool{}
	var results []Result
	for _, entry := range run.Results {
		if seen[entry.Scenario] || !contains(HarnessScenarios, entry.Scenario) {
			return nil, fmt.Errorf("unknown/duplicate harness case %s", entry.Scenario)
		}
		seen[entry.Scenario] = true
		status := "fail"
		if entry.Status == "passed" && entry.Unsupported != nil && !*entry.Unsupported && entry.Client == "sendspin-player-go" && entry.Server == "aiosendspin" && entry.ServerExit != nil && entry.ClientExit != nil && *entry.ServerExit == 0 && *entry.ClientExit == 0 {
			status = "pass"
		}
		results = append(results, Result{Name: entry.Scenario, Status: status})
	}
	if len(seen) != len(HarnessScenarios) {
		return nil, errors.New("missing harness cases (partial run cannot satisfy the full suite)")
	}
	return results, nil
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
