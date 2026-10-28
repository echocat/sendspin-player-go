package specaudit

import (
	"fmt"
	"strings"
	"testing"
)

func templateFixture(t *testing.T) *Project {
	t.Helper()
	p := atomicFixture(t)
	s := p.Index.Sources[0]
	p.Catalog.InterpretationSources = []InterpretationSource{{Path: "template.md", Blob: s.Blob, SHA256: s.SHA256, Lines: 169, Purpose: "Synthetic"}}
	p.TemplateInventory = &TemplateInventory{Schema: 1, Revision: p.Catalog.Revision, Review: "complete", Scope: "Synthetic source-only review", Coverage: []TemplateCoverage{{1, 169, "clauses", "Synthetic coverage"}}}
	for i := range 34 {
		p.TemplateInventory.Clauses = append(p.TemplateInventory.Clauses, TemplateClause{ID: fmt.Sprintf("N%02d", i+1), Start: 24, End: 24, Level: "must", Actor: "client", Condition: "Synthetic condition", Statement: "Synthetic predicate", Related: []string{"M01.01"}, Notes: "Mapping only, no inherited fulfillment"})
	}
	return p
}

func TestTemplateInventoryCannotLoseClausesCoverageOrClaimFulfillment(t *testing.T) {
	for _, mutation := range []string{"none", "delete", "duplicate", "gap", "tail", "context", "claim", "unknown-atom", "unknown-level", "missing-condition"} {
		t.Run(mutation, func(t *testing.T) {
			p := templateFixture(t)
			r := p.TemplateInventory
			switch mutation {
			case "delete":
				r.Clauses = r.Clauses[1:]
			case "duplicate":
				r.Clauses[1].ID = r.Clauses[0].ID
			case "gap":
				r.Coverage[0].Start = 2
			case "tail":
				r.Coverage[0].End = 168
			case "context":
				r.Coverage[0].Kind = "context"
			case "claim":
				r.FulfillmentClaim = true
			case "unknown-atom":
				r.Clauses[0].Related = []string{"M99.99"}
			case "unknown-level":
				r.Clauses[0].Level = "mixed"
			case "missing-condition":
				r.Clauses[0].Condition = " "
			}
			if err := p.Validate(); (err == nil) != (mutation == "none") {
				t.Fatal("invalid template review accepted or valid one rejected", err)
			}
		})
	}
	p := templateFixture(t)
	report, err := p.Report(ReportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.ConformanceClaim || report.InventoryReview != "partial" || len(report.Obligations) != 1 || !strings.Contains(report.Markdown(), "never inherited assessments") {
		t.Fatal("source mapping manufactured primary fulfillment")
	}
}
