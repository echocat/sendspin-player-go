package specaudit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func saveAtomicFixture(t *testing.T, p *Project) *Project {
	t.Helper()
	p.Index.RequiredObligationIDs = nil
	for _, o := range p.Catalog.Obligations {
		p.Index.RequiredObligationIDs = append(p.Index.RequiredObligationIDs, o.ID)
	}
	if err := writeJSON(filepath.Join(p.Root, IndexPath), p.Index); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(p.Root, IndexPath))
	if err != nil {
		t.Fatal(err)
	}
	p.Catalog.IndexSHA256 = digest(data)
	if err = writeJSON(filepath.Join(p.Root, CatalogPath), p.Catalog); err != nil {
		t.Fatal(err)
	}
	p, err = Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func atomicFixture(t *testing.T) *Project {
	t.Helper()
	p := fixture(t)
	put(t, p.Root, TemplatePath, "{{requirements:core}}\n{{obligations}}\n")
	p.Catalog.Obligations = []Obligation{{ID: "M01.01", Groups: []string{"M01"}, Statement: "One synthetic obligation", Level: "must", Actor: "client", Condition: "All synthetic sessions", Scope: "applicable", Basis: "explicit", Assessment: "T", Verification: "software", Remaining: "none", Citation: "M:3", Sources: []Reference{{"M", 3, 3}}, Implementation: []string{"impl.go"}, Evidence: []string{p.Catalog.Evidence[0].ID}, Notes: "Explicit synthetic review, not inherited from the parent"}}
	p.Catalog.NormativeReviews = []NormativeReview{{Group: "M01", Status: "complete", Notes: "Synthetic one-predicate group classification"}}
	return saveAtomicFixture(t, p)
}

func TestAtomicCatalogRejectsInvalidClassificationAndDeletedBaseline(t *testing.T) {
	for _, mutation := range []string{"duplicate-id", "delete-baseline", "unfrozen-id", "unknown-parent", "duplicate-parent", "unrelated-parent", "missing-review", "duplicate-review", "empty-review", "mixed-level", "missing-condition", "missing-notes", "false-optional-must", "false-optional-should", "hardware-T", "untested-T", "inherited-path", "unknown-test", "duplicate-test", "wrong-citation", "outside-parent", "bad-priority", "hidden-partial", "unresolved-complete", "complete-without-atomic"} {
		t.Run(mutation, func(t *testing.T) {
			p := atomicFixture(t)
			o := &p.Catalog.Obligations[0]
			switch mutation {
			case "duplicate-id":
				p.Catalog.Obligations = append(p.Catalog.Obligations, *o)
			case "delete-baseline":
				p.Catalog.Obligations = nil
			case "unfrozen-id":
				o.ID = "M01.02"
			case "unknown-parent":
				o.Groups = append(o.Groups, "P99")
			case "duplicate-parent":
				o.Groups = append(o.Groups, "M01")
			case "unrelated-parent":
				parent := p.Catalog.Requirements[0]
				parent.ID = "M02"
				parent.Citation = "C:3"
				parent.Sources = []Reference{{"C", 3, 3}}
				p.Catalog.Requirements = append(p.Catalog.Requirements, parent)
				p.Catalog.NormativeReviews = append(p.Catalog.NormativeReviews, NormativeReview{"M02", "complete", "Unrelated document"})
				o.Groups = append(o.Groups, "M02")
			case "missing-review":
				p.Catalog.NormativeReviews = nil
			case "duplicate-review":
				p.Catalog.NormativeReviews = append(p.Catalog.NormativeReviews, p.Catalog.NormativeReviews[0])
			case "empty-review":
				p.Catalog.NormativeReviews[0].Notes = " "
			case "mixed-level":
				o.Level = "mixed"
			case "missing-condition":
				o.Condition = " "
			case "missing-notes":
				o.Notes = " "
			case "false-optional-must", "false-optional-should":
				o.Scope = "excluded"
				o.Assessment = "D"
				o.Remaining = "optional"
				if mutation == "false-optional-should" {
					o.Level = "should"
				}
			case "hardware-T":
				o.Verification = "hardware"
			case "untested-T":
				o.Evidence = nil
			case "inherited-path":
				o.Implementation = nil
			case "unknown-test":
				o.Evidence = []string{"invented"}
			case "duplicate-test":
				o.Evidence = append(o.Evidence, o.Evidence[0])
			case "wrong-citation":
				o.Citation = "M:2"
			case "outside-parent":
				o.Citation = "M:1"
				o.Sources = []Reference{{"M", 1, 1}}
			case "bad-priority":
				o.Priority = 1
			case "hidden-partial":
				o.Assessment = "P"
			case "unresolved-complete":
				o.Level = "unclassified"
				o.Scope = "undetermined"
				o.Assessment = "O"
				o.Verification = "review"
				o.Remaining = "review"
				o.Priority = 1
			case "complete-without-atomic":
				p.Catalog.InventoryReview = "complete"
				p.Catalog.Obligations = nil
				p.Catalog.NormativeReviews = nil
			}
			if err := writeJSON(filepath.Join(p.Root, CatalogPath), p.Catalog); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(p.Root); err == nil {
				t.Fatal("invalid atomic review was accepted")
			}
		})
	}
}

func TestAtomicEvidenceIsNeverInheritedAndHardwareIsNeverPromoted(t *testing.T) {
	for _, hardware := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-inheritance", true: "hardware-remains-partial"}[hardware], func(t *testing.T) {
			p := atomicFixture(t)
			p.Catalog.Requirements[0].Assessment = "P"
			o := &p.Catalog.Obligations[0]
			o.Assessment = "C"
			o.Remaining = "software-evidence"
			o.Priority = 1
			o.Evidence = nil
			if hardware {
				o.Assessment = "P"
				o.Verification = "hardware"
				o.Remaining = "measurement"
				o.Evidence = []string{p.Catalog.Evidence[0].ID}
			}
			p = saveAtomicFixture(t, p)
			evidenceFixture(t, p, validGoEvents)
			report, err := p.Report(ReportOptions{})
			if err != nil {
				t.Fatal(err)
			}
			atom := report.Obligations[0]
			want := "not-linked"
			if hardware {
				want = "pass"
			}
			if atom.EvidenceStatus != want || report.Requirements[0].Assessment != "P" || report.ConformanceClaim || len(report.Backlog) != 1 || !strings.Contains(strings.Join(report.ConformanceBlockers, " "), "M01.01") {
				t.Fatal("atomic evidence inherited or promoted fulfillment")
			}
			if hardware && atom.VerificationStatus != "measurement-required" {
				t.Fatal("software pass became hardware verification")
			}
			if !strings.Contains(report.Markdown(), "Groups without completed atomic review") {
				t.Fatal("report lost partial inventory boundary")
			}
			data, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			var value struct {
				Obligations []struct {
					Evidence []EvidenceReport `json:"evidence"`
				} `json:"obligations"`
			}
			if err = json.Unmarshal(data, &value); err != nil {
				t.Fatal("report embedded test IDs instead of actual evidence entries", err)
			}
		})
	}
}

func TestAtomicBacklogDistinguishesImplementationMeasurementAndExcludedServer(t *testing.T) {
	p := atomicFixture(t)
	base := p.Catalog.Obligations[0]
	base.Assessment = "O"
	base.Level = "should"
	base.Remaining = "implementation"
	base.Priority = 2
	base.Evidence = nil
	p.Catalog.Obligations = []Obligation{base}
	hardware := base
	hardware.ID = "M01.02"
	hardware.Level = "must"
	hardware.Assessment = "P"
	hardware.Verification = "hardware"
	hardware.Remaining = "measurement"
	hardware.Priority = 1
	server := base
	server.ID = "M01.03"
	server.Actor = "server"
	server.Level = "must"
	server.Scope = "excluded"
	server.Assessment = "S"
	server.Verification = "external-server"
	server.Remaining = "external"
	server.Priority = 0
	server.Implementation = nil
	p.Catalog.Obligations = append(p.Catalog.Obligations, hardware, server)
	p = saveAtomicFixture(t, p)
	report, err := p.Report(ReportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Backlog) != 2 || report.Backlog[0].ID != "M01.02" || report.Backlog[1].Category != "implementation" || report.Backlog[1].Level != "should" {
		t.Fatal("backlog merged external duties, recommendations or measurements")
	}
	data, err := p.RenderAudit()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "M01.03") || !strings.Contains(string(data), "external-server") {
		t.Fatal("generated audit hid server-owned normative duty")
	}
}

func TestConditionalMustNeedsAnExplicitUnselectedMayParent(t *testing.T) {
	p := atomicFixture(t)
	permission := p.Catalog.Obligations[0]
	permission.Level = "may"
	permission.Scope = "excluded"
	permission.Assessment = "D"
	permission.Verification = "review"
	permission.Remaining = "optional"
	permission.Evidence = nil
	permission.Implementation = nil
	child := permission
	child.ID = "M01.02"
	child.Level = "must"
	child.EnabledBy = permission.ID
	child.Condition = "Only if the unselected optional permission is implemented"
	p.Catalog.Obligations = []Obligation{permission, child}
	p = saveAtomicFixture(t, p)
	report, err := p.Report(ReportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Backlog) != 0 || report.Obligations[1].Level != "must" || report.Obligations[1].EnabledBy != "M01.01" {
		t.Fatal("conditional MUST was flattened into MAY or a current SDK bug")
	}
	for _, mutation := range []string{"unknown", "self", "mandatory-parent", "selected-parent", "may-child"} {
		t.Run(mutation, func(t *testing.T) {
			catalog := p.Catalog
			catalog.Obligations = append([]Obligation(nil), p.Catalog.Obligations...)
			switch mutation {
			case "unknown":
				catalog.Obligations[1].EnabledBy = "M01.99"
			case "self":
				catalog.Obligations[1].EnabledBy = "M01.02"
			case "mandatory-parent":
				catalog.Obligations[0].Level = "must"
			case "selected-parent":
				catalog.Obligations[0].Scope = "applicable"
				catalog.Obligations[0].Assessment = "C"
				catalog.Obligations[0].Remaining = "software-evidence"
				catalog.Obligations[0].Priority = 2
				catalog.Obligations[0].Implementation = []string{"impl.go"}
			case "may-child":
				catalog.Obligations[1].Level = "may"
			}
			if err := writeJSON(filepath.Join(p.Root, CatalogPath), catalog); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(p.Root); err == nil {
				t.Fatal("invalid optional-branch exclusion was accepted")
			}
		})
	}
}

func TestAtomicAuditMarkerAndClassificationChangesCannotDisappear(t *testing.T) {
	p := atomicFixture(t)
	data, err := p.RenderAudit()
	if err != nil {
		t.Fatal(err)
	}
	put(t, p.Root, AuditPath, string(data))
	if err = p.CheckGenerated(); err != nil {
		t.Fatal(err)
	}
	p.Catalog.Obligations[0].Condition = "Changed applicability condition"
	if err = p.CheckGenerated(); err == nil {
		t.Fatal("changed condition did not invalidate generated atomic audit")
	}
	put(t, p.Root, TemplatePath, "{{requirements:core}}\n")
	if _, err = p.RenderAudit(); err == nil {
		t.Fatal("template silently omitted all atomic obligations")
	}
}

func TestConditionalExclusionMustCiteItsPermissionAcrossGroups(t *testing.T) {
	p := atomicFixture(t)
	permission := p.Catalog.Obligations[0]
	permission.Level = "may"
	permission.Scope = "excluded"
	permission.Assessment = "D"
	permission.Verification = "review"
	permission.Remaining = "optional"
	permission.Evidence = nil
	permission.Implementation = nil
	group := p.Catalog.Requirements[0]
	group.ID = "M02"
	group.Citation = "C:3"
	group.Sources = []Reference{{"C", 3, 3}}
	p.Catalog.Requirements = append(p.Catalog.Requirements, group)
	p.Catalog.NormativeReviews = append(p.Catalog.NormativeReviews, NormativeReview{"M02", "complete", "A condition whose enabling permission is in another reviewed group"})
	child := permission
	child.ID = "M02.01"
	child.Groups = []string{"M02", "M01"}
	child.Level = "must"
	child.EnabledBy = permission.ID
	child.Citation = "M:3; C:3"
	child.Sources = []Reference{{"M", 3, 3}, {"C", 3, 3}}
	p.Catalog.Obligations = []Obligation{permission, child}
	p = saveAtomicFixture(t, p)
	// The broad parent group already cites C:3, so merely sharing a group or
	// overlapping its range would not prove citation of the actual MAY at M:3.
	p.Catalog.Obligations[1].Citation = "C:3"
	p.Catalog.Obligations[1].Sources = []Reference{{"C", 3, 3}}
	if err := writeJSON(filepath.Join(p.Root, CatalogPath), p.Catalog); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p.Root); err == nil {
		t.Fatal("an unrelated clause in a broad parent hid an unconditional MUST")
	}
}
