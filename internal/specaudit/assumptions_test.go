package specaudit

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const comparisonFixture = "https://github.com/Sendspin/sendspin-cpp/blob/985fba57afb0462b520ab9ce974d1860858ed685/src/protocol.cpp#L595-L613"

func assumptionFixture(t *testing.T) *Project {
	t.Helper()
	p := atomicFixture(t)
	o := &p.Catalog.Obligations[0]
	o.Level, o.Scope, o.Assessment = "unclassified", "undetermined", "O"
	o.Verification, o.Remaining, o.Priority, o.Evidence = "review", "review", 1, nil
	p.Catalog.NormativeReviews[0].Status = "partial"
	p.Catalog.WorkingAssumptions = []WorkingAssumption{{ID: "WA01", Status: "provisional", Obligations: []string{o.ID}, Decision: "Keep the tested local policy", Reason: "A comparison is not normative proof", Revisit: "Authoritative clarification or contrary evidence", References: []string{comparisonFixture}, Evidence: []string{p.Catalog.Evidence[0].ID}}}
	return saveAtomicFixture(t, p)
}

func TestWorkingAssumptionsRequirePinnedSourcesAndExplicitLinks(t *testing.T) {
	for _, mutation := range []string{"duplicate", "bad-id", "promoted-status", "empty-decision", "empty-reason", "empty-revisit", "no-obligation", "unknown-obligation", "duplicate-obligation", "no-reference", "moving-reference", "short-pin", "unsafe-path", "zero-line", "reversed-lines", "duplicate-reference", "unknown-evidence", "duplicate-evidence"} {
		t.Run(mutation, func(t *testing.T) {
			p := assumptionFixture(t)
			a := &p.Catalog.WorkingAssumptions[0]
			switch mutation {
			case "duplicate":
				p.Catalog.WorkingAssumptions = append(p.Catalog.WorkingAssumptions, *a)
			case "bad-id":
				a.ID = "M01.01"
			case "promoted-status":
				a.Status = "normative-complete"
			case "empty-decision":
				a.Decision = " "
			case "empty-reason":
				a.Reason = " "
			case "empty-revisit":
				a.Revisit = " "
			case "no-obligation":
				a.Obligations = nil
			case "unknown-obligation":
				a.Obligations = []string{"M99.01"}
			case "duplicate-obligation":
				a.Obligations = append(a.Obligations, a.Obligations[0])
			case "no-reference":
				a.References = nil
			case "moving-reference":
				a.References[0] = strings.Replace(comparisonFixture, "985fba57afb0462b520ab9ce974d1860858ed685", "main", 1)
			case "short-pin":
				a.References[0] = strings.Replace(comparisonFixture, "985fba57afb0462b520ab9ce974d1860858ed685", "985fba5", 1)
			case "unsafe-path":
				a.References[0] = strings.Replace(comparisonFixture, "src/protocol.cpp", "src/../protocol.cpp", 1)
			case "zero-line":
				a.References[0] = strings.Replace(comparisonFixture, "#L595-L613", "#L0-L1", 1)
			case "reversed-lines":
				a.References[0] = strings.Replace(comparisonFixture, "#L595-L613", "#L613-L595", 1)
			case "duplicate-reference":
				a.References = append(a.References, a.References[0])
			case "unknown-evidence":
				a.Evidence = []string{"invented"}
			case "duplicate-evidence":
				a.Evidence = append(a.Evidence, a.Evidence[0])
			}
			if err := p.Validate(); err == nil {
				t.Fatal("invalid working assumption accepted")
			}
		})
	}
}

func TestWorkingAssumptionPassCannotResolveNormativeReview(t *testing.T) {
	p := assumptionFixture(t)
	evidenceFixture(t, p, validGoEvents)
	report, err := p.Report(ReportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.WorkingAssumptions) != 1 || report.WorkingAssumptions[0].EvidenceStatus != "pass" || report.WorkingAssumptions[0].Status != "provisional" {
		t.Fatal("policy evidence was not separately resolved")
	}
	o := report.Obligations[0]
	if o.EvidenceStatus != "not-linked" || o.VerificationStatus != "review-required" || o.Assessment != "O" || report.NormativeReviews[0].Status != "partial" || report.ConformanceClaim {
		t.Fatal("assumption evidence promoted a normative decision")
	}
	withAssumptions := report.ConformanceBlockers
	p.Catalog.WorkingAssumptions = nil
	without, err := p.Report(ReportOptions{})
	if err != nil || !reflect.DeepEqual(withAssumptions, without.ConformanceBlockers) {
		t.Fatal("working assumptions changed normative blockers", err)
	}
	if !strings.Contains(report.Markdown(), "Provisional working assumptions (not normative evidence)") || !strings.Contains(report.Markdown(), comparisonFixture) {
		t.Fatal("report omitted provisional policy boundary/source")
	}
	data, err := json.Marshal(report)
	if err != nil || !strings.Contains(string(data), `"working_assumptions"`) || !strings.Contains(string(data), `"evidence_status":"pass"`) {
		t.Fatal("machine report omitted assumption execution", err)
	}
}

func TestWorkingAssumptionDoesNotInheritAtomEvidence(t *testing.T) {
	p := assumptionFixture(t)
	o := &p.Catalog.Obligations[0]
	o.Level, o.Scope, o.Assessment = "must", "applicable", "T"
	o.Verification, o.Remaining, o.Priority = "software", "none", 0
	o.Evidence = []string{p.Catalog.Evidence[0].ID}
	p.Catalog.NormativeReviews[0].Status = "complete"
	p.Catalog.WorkingAssumptions[0].Evidence = nil
	p = saveAtomicFixture(t, p)
	evidenceFixture(t, p, validGoEvents)
	report, err := p.Report(ReportOptions{})
	if err != nil || report.Obligations[0].EvidenceStatus != "pass" || report.WorkingAssumptions[0].EvidenceStatus != "not-linked" {
		t.Fatal("assumption inherited atomic tests", err)
	}
	text, err := p.RenderAudit()
	if err != nil || !strings.Contains(string(text), "declared, not executed here") || strings.Contains(string(text), "Policy evidence: **pass**") {
		t.Fatal("generated audit confused declarations with execution", err)
	}
	// Evidence inputs include the assumptions themselves via the catalog digest.
	before := p.CatalogDigest
	p.Catalog.WorkingAssumptions[0].Decision += " changed"
	if err = writeJSON(filepath.Join(p.Root, CatalogPath), p.Catalog); err != nil {
		t.Fatal(err)
	}
	p, err = Load(p.Root)
	if err != nil || p.CatalogDigest == before {
		t.Fatal("policy change did not invalidate provenance", err)
	}
	report, err = p.Report(ReportOptions{})
	if err != nil || report.Obligations[0].EvidenceStatus == "pass" {
		t.Fatal("old policy/catalog evidence was reused", err)
	}
}
