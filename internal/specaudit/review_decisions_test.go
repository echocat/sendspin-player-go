package specaudit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func interpretationFixture(t *testing.T) *Project {
	t.Helper()
	p := fixture(t)
	s := p.Index.Sources[0]
	p.Catalog.InterpretationSources = []InterpretationSource{{Path: "template.md", Blob: s.Blob, SHA256: s.SHA256, Lines: s.Lines, Purpose: "Synthetic interpretation context, not a normative inventory"}}
	return saveAtomicFixture(t, p)
}

func TestInterpretationContextCannotExpandInventoryOrLoseItsPins(t *testing.T) {
	p := interpretationFixture(t)
	report, err := p.Report(ReportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Index.Sources) != 9 || report.InventoryReview != "partial" || len(report.InterpretationSources) != 1 || !strings.Contains(report.Markdown(), "not a complete atomic inventory") {
		t.Fatal("interpretation context silently completed or expanded the normative inventory")
	}
	data, err := json.Marshal(report)
	if err != nil || !strings.Contains(string(data), `"interpretation_sources"`) {
		t.Fatal("JSON lost interpretation-source provenance", err)
	}
	for _, mutation := range []string{"path", "duplicate", "blob", "sha256", "lines", "purpose"} {
		t.Run(mutation, func(t *testing.T) {
			p := interpretationFixture(t)
			s := &p.Catalog.InterpretationSources[0]
			switch mutation {
			case "path":
				s.Path = "../README.md"
			case "duplicate":
				p.Catalog.InterpretationSources = append(p.Catalog.InterpretationSources, *s)
			case "blob":
				s.Blob = "not-pinned"
			case "sha256":
				s.SHA256 = "not-pinned"
			case "lines":
				s.Lines = 0
			case "purpose":
				s.Purpose = " "
			}
			if err := writeJSON(filepath.Join(p.Root, CatalogPath), p.Catalog); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(p.Root); err == nil {
				t.Fatal("invalid interpretation source accepted")
			}
		})
	}
}

type specTransport func(*http.Request) (*http.Response, error)

func (f specTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestVerifySpecChecksInterpretationContextAtTheSameRevision(t *testing.T) {
	for _, mutation := range []string{"none", "content", "blob", "lines", "unavailable"} {
		t.Run(mutation, func(t *testing.T) {
			p := interpretationFixture(t)
			if mutation == "blob" {
				p.Catalog.InterpretationSources[0].Blob = strings.Repeat("a", 40)
			}
			if mutation == "lines" {
				p.Catalog.InterpretationSources[0].Lines++
			}
			seen := 0
			client := &http.Client{Transport: specTransport(func(r *http.Request) (*http.Response, error) {
				if !strings.HasPrefix(r.URL.String(), "https://raw.githubusercontent.com/Sendspin/spec/"+p.Catalog.Revision+"/") {
					t.Fatal("wrong interpretation source/revision", r.URL)
				}
				text, status := "# Heading\n\nA normative statement.\n", http.StatusOK
				if strings.HasSuffix(r.URL.Path, "/template.md") {
					seen++
					if mutation == "content" {
						text += "Changed context\n"
					}
					if mutation == "unavailable" {
						status = http.StatusNotFound
					}
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(text)), Header: http.Header{}}, nil
			})}
			err := p.verifySpec(context.Background(), client)
			if seen != 1 || (err == nil) != (mutation == "none") {
				t.Fatal("interpretation pin not fetched/verified", seen, err)
			}
		})
	}
}

func TestCompleteClassificationCannotDischargeOpenMandatoryReview(t *testing.T) {
	p := atomicFixture(t)
	o := &p.Catalog.Obligations[0]
	o.Assessment, o.Verification, o.Remaining, o.Priority = "C", "review", "review", 1
	o.Actor = "sdk-contract"
	p = saveAtomicFixture(t, p)
	evidenceFixture(t, p, validGoEvents)
	report, err := p.Report(ReportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	atom := report.Obligations[0]
	if atom.EvidenceStatus != "pass" || atom.VerificationStatus != "review-required" || atom.Assessment != "C" || len(report.UnreviewedGroups) != 0 || len(report.Backlog) != 1 || !strings.Contains(strings.Join(report.ConformanceBlockers, " "), "M01.01: atomic review/mandatory evidence") || report.ConformanceClaim {
		t.Fatal("completed classification or passing linked software discharged mandatory review", atom)
	}
	// Merely changing the verification label must never certify a manual
	// security/integration obligation as targeted software fulfillment.
	p.Catalog.Obligations[0].Assessment = "T"
	if err = p.Validate(); err == nil {
		t.Fatal("manual review was promoted to a software pass")
	}
}
