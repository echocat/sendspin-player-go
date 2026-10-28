package specaudit

import (
	"fmt"
	"slices"
	"strings"
)

type TemplateInventory struct {
	Schema           int                `json:"schema"`
	Revision         string             `json:"revision"`
	Review           string             `json:"review"`
	FulfillmentClaim bool               `json:"fulfillment_claim"`
	Scope            string             `json:"scope"`
	Coverage         []TemplateCoverage `json:"coverage"`
	Clauses          []TemplateClause   `json:"clauses"`
}
type TemplateCoverage struct {
	Start  int    `json:"start"`
	End    int    `json:"end"`
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}
type TemplateClause struct {
	ID        string   `json:"id"`
	Start     int      `json:"start"`
	End       int      `json:"end"`
	Level     string   `json:"level"`
	Actor     string   `json:"actor"`
	Condition string   `json:"condition"`
	Statement string   `json:"statement"`
	Related   []string `json:"related"`
	Notes     string   `json:"notes"`
}

func (p *Project) validateTemplateInventory() error {
	t := p.TemplateInventory
	if t == nil {
		if p.Catalog.TemplateInventoryPath != "" {
			return fmt.Errorf("declared template inventory is missing")
		}
		return nil
	}
	pinned := false
	for _, s := range p.Catalog.InterpretationSources {
		if s.Path == "template.md" && s.Lines == 169 {
			pinned = true
		}
	}
	if !pinned || t.Schema != 1 || t.Revision != p.Catalog.Revision || t.Review != "complete" || t.FulfillmentClaim || strings.TrimSpace(t.Scope) == "" || len(t.Clauses) != 34 {
		return fmt.Errorf("invalid/unpinned source-only template inventory")
	}
	next := 1
	for _, c := range t.Coverage {
		if c.Start != next || c.End < c.Start || c.End > 169 || !slices.Contains([]string{"context", "example", "clauses"}, c.Kind) || strings.TrimSpace(c.Reason) == "" {
			return fmt.Errorf("template inventory has incomplete/invalid line coverage")
		}
		next = c.End + 1
		if c.Kind == "clauses" {
			found := false
			for _, a := range t.Clauses {
				found = found || (a.Start >= c.Start && a.End <= c.End)
			}
			if !found {
				return fmt.Errorf("template normative section has no atomic clauses")
			}
		}
	}
	if next != 170 {
		return fmt.Errorf("template inventory omits source lines")
	}
	known := map[string]bool{}
	for _, o := range p.Catalog.Obligations {
		known[o.ID] = true
	}
	for i, a := range t.Clauses {
		if a.ID != fmt.Sprintf("N%02d", i+1) || a.Start < 1 || a.End < a.Start || a.End > 169 || strings.TrimSpace(a.Statement) == "" || strings.TrimSpace(a.Condition) == "" || strings.TrimSpace(a.Notes) == "" || !slices.Contains([]string{"must", "must-not", "should", "may", "informative"}, a.Level) || !slices.Contains([]string{"client", "server", "sdk-contract", "spec-author"}, a.Actor) {
			return fmt.Errorf("invalid/missing template clause %s", a.ID)
		}
		covered := false
		for _, c := range t.Coverage {
			covered = covered || (c.Kind == "clauses" && c.Start <= a.Start && c.End >= a.End)
		}
		if !covered {
			return fmt.Errorf("template clause %s hidden as context", a.ID)
		}
		seen := map[string]bool{}
		for _, id := range a.Related {
			if !known[id] || seen[id] {
				return fmt.Errorf("template clause %s has invalid primary mapping", a.ID)
			}
			seen[id] = true
		}
	}
	return nil
}

func writeTemplateInventory(b *strings.Builder, t *TemplateInventory) {
	if t == nil {
		return
	}
	b.WriteString("\n## Supplemental template source inventory\n\nAll 169 template.md lines have explicit dispositions and 34 atomic source clauses. This is classification only: related primary atoms are cross-references, never inherited assessments or test passes. External referenced documents and remaining primary decisions are not certified.\n\n| ID / template lines | Actor / condition | Level | Clause / mapping and limitations |\n| --- | --- | --- | --- |\n")
	for _, a := range t.Clauses {
		fmt.Fprintf(b, "| %s / %d-%d | %s / %s | %s | %s Related: %s. %s |\n", a.ID, a.Start, a.End, a.Actor, escape(a.Condition), a.Level, escape(a.Statement), strings.Join(a.Related, ", "), escape(a.Notes))
	}
}
