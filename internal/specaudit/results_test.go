package specaudit

import (
	"encoding/json"
	"strings"
	"testing"
)

const validGoEvents = `{"Action":"start","Package":"example.test/audit"}
{"Action":"run","Package":"example.test/audit","Test":"TestOne"}
{"Action":"pass","Package":"example.test/audit","Test":"TestOne"}
{"Action":"pass","Package":"example.test/audit"}
`

func TestGoEvidenceRequiresCompleteRealTerminalEvents(t *testing.T) {
	for _, tc := range []struct {
		name, events, want string
		invalid            bool
	}{
		{"pass", validGoEvents, "pass", false},
		{"empty", "", "", false},
		{"truncated", validGoEvents + `{`, "", true},
		{"pass-without-run", strings.Replace(validGoEvents, `{"Action":"run","Package":"example.test/audit","Test":"TestOne"}`+"\n", "", 1), "", true},
		{"missing-package-pass", strings.TrimSuffix(validGoEvents, `{"Action":"pass","Package":"example.test/audit"}`+"\n"), "incomplete", false},
		{"package-fail", strings.Replace(validGoEvents, `{"Action":"pass","Package":"example.test/audit"}`, `{"Action":"fail","Package":"example.test/audit"}`, 1), "fail", false},
		{"skipped", strings.Replace(validGoEvents, `"Action":"pass","Package":"example.test/audit","Test":"TestOne"`, `"Action":"skip","Package":"example.test/audit","Test":"TestOne"`, 1), "skip", false},
		{"printed-pass-is-not-pass", `{"Action":"start","Package":"example.test/audit"}` + "\n" + `{"Action":"run","Package":"example.test/audit","Test":"TestOne"}` + "\n" + `{"Action":"output","Package":"example.test/audit","Test":"TestOne","Output":"PASS\n"}` + "\n", "incomplete", false},
		{"repeated-run", strings.Replace(validGoEvents, `{"Action":"pass","Package":"example.test/audit"}`, `{"Action":"run","Package":"example.test/audit","Test":"TestOne"}`+"\n"+`{"Action":"pass","Package":"example.test/audit","Test":"TestOne"}`+"\n"+`{"Action":"pass","Package":"example.test/audit"}`, 1), "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			results, err := ReadGoResults(strings.NewReader(tc.events))
			if tc.invalid {
				if err == nil {
					t.Fatal("invalid event stream accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if len(results) != 0 {
					t.Fatal(results)
				}
				return
			}
			if len(results) != 1 || results[0].Status != tc.want {
				t.Fatal("wrong evidence outcome", results)
			}
		})
	}
}

func TestParentPassCannotHideSkippedOrFailedChildren(t *testing.T) {
	for _, status := range []string{"skip", "fail"} {
		events := strings.Replace(validGoEvents, `{"Action":"pass","Package":"example.test/audit","Test":"TestOne"}`, `{"Action":"run","Package":"example.test/audit","Test":"TestOne/child"}`+"\n"+`{"Action":"`+status+`","Package":"example.test/audit","Test":"TestOne/child"}`+"\n"+`{"Action":"pass","Package":"example.test/audit","Test":"TestOne"}`, 1)
		results, err := ReadGoResults(strings.NewReader(events))
		if err != nil {
			t.Fatal(err)
		}
		want := "partial"
		if status == "fail" {
			want = "fail"
		}
		for _, result := range results {
			if result.Name == "TestOne" && result.Status != want {
				t.Fatal("parent masked child", result)
			}
		}
	}
}

func harnessFixture() map[string]any {
	var results []map[string]any
	for _, scenario := range HarnessScenarios {
		results = append(results, map[string]any{"scenario_id": scenario, "status": "passed", "client_exit_code": 0, "server_exit_code": 0, "unsupported": false, "client_impl": "sendspin-player-go", "server_impl": "aiosendspin"})
	}
	return map[string]any{"implementation": "sendspin-player-go", "harness_revision": "fa6dc45795f0810c5810af9f9b828595b7267ece", "sdk_specification": "1.0.0-rc1", "audited_spec_revision": "671a34d408581fc25ea56b3528a4a3f13e3be901", "protocol_assertion_revision": "8c9577ea8719ad082d051ec13cc73ef15ed68948", "reference_package": "10.2.1", "results": results}
}

func TestHarnessEvidenceCannotHideFailuresOmissionsOrWrongPins(t *testing.T) {
	for _, mutation := range []string{"none", "missing-case", "duplicate-case", "wrong-spec", "wrong-harness", "wrong-implementation", "unsupported", "failed-exit", "missing-exit", "duplicate-json-key", "trailing-json"} {
		t.Run(mutation, func(t *testing.T) {
			value := harnessFixture()
			cases := value["results"].([]map[string]any)
			switch mutation {
			case "missing-case":
				value["results"] = cases[1:]
			case "duplicate-case":
				cases[1]["scenario_id"] = cases[0]["scenario_id"]
			case "wrong-spec":
				value["audited_spec_revision"] = "future"
			case "wrong-harness":
				value["harness_revision"] = "future"
			case "wrong-implementation":
				value["implementation"] = "sendspin-go"
			case "unsupported":
				cases[0]["unsupported"] = true
			case "failed-exit":
				cases[0]["client_exit_code"] = 1
			case "missing-exit":
				delete(cases[0], "server_exit_code")
			}
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if mutation == "duplicate-json-key" {
				data = []byte(strings.Replace(string(data), `"implementation":"sendspin-player-go"`, `"implementation":"sendspin-go","implementation":"sendspin-player-go"`, 1))
			}
			if mutation == "trailing-json" {
				data = append(data, []byte(`{}`)...)
			}
			results, err := ReadHarnessResults(strings.NewReader(string(data)))
			if mutation == "unsupported" || mutation == "failed-exit" || mutation == "missing-exit" {
				if err != nil {
					t.Fatal(err)
				}
				if results[0].Status != "fail" {
					t.Fatal("failed case credited as passed")
				}
				return
			}
			if mutation != "none" {
				if err == nil {
					t.Fatal("invalid harness evidence accepted")
				}
				return
			}
			if err != nil || len(results) != 15 {
				t.Fatal(results, err)
			}
		})
	}
}
