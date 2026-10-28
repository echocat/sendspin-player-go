package specaudit

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func sourceReviewFixture(t *testing.T) *Project {
	t.Helper()
	p := fixture(t)
	data := []byte("# Heading\n\nA normative statement.\n\nBackground introducing that statement.\n")
	blob := sha1.Sum(append([]byte(fmt.Sprintf("blob %d\x00", len(data))), data...))
	source := &p.Index.Sources[0]
	source.Blob = hex.EncodeToString(blob[:])
	source.SHA256 = digest(data)
	source.Lines = 5
	source.Blocks = paragraphs(source.ID, splitLines(data))
	if err := writeJSON(filepath.Join(p.Root, IndexPath), p.Index); err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile(filepath.Join(p.Root, IndexPath))
	if err != nil {
		t.Fatal(err)
	}
	p.Catalog.IndexSHA256 = digest(index)
	p.Catalog.SourceGaps = []Gap{{ID: "M:5-5", Reason: "Synthetic background pending review"}}
	return p
}

func TestSourceReviewSeparatesContextFromFulfilledRequirements(t *testing.T) {
	for _, kind := range []string{"context", "example"} {
		t.Run(kind, func(t *testing.T) {
			p := sourceReviewFixture(t)
			p.Catalog.SourceGaps = nil
			p.Catalog.SourceReviews = []SourceReview{{ID: "M:5-5", Kind: kind, Reason: "Introduces the separately referenced statement without adding a new independent obligation", Related: []string{"M01"}}}
			if err := writeJSON(filepath.Join(p.Root, CatalogPath), p.Catalog); err != nil {
				t.Fatal(err)
			}
			p, err := Load(p.Root)
			if err != nil {
				t.Fatal(err)
			}
			if len(p.OpenSourceBlocks()) != 0 || len(p.UnreferencedBlocks()) != 1 {
				t.Fatal("review erased the numeric source-reference footprint")
			}
			report, err := p.Report(ReportOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if report.InventoryReview != "partial" || report.ConformanceClaim || len(report.ConformanceBlockers) == 0 || report.Requirements[0].Assessment != "T" || report.Requirements[0].EvidenceStatus == "pass" {
				t.Fatal("context review manufactured fulfillment or execution evidence")
			}
		})
	}
}

func TestSourceReviewRequiresSpecificNonduplicatedRationaleAndRelatedGroup(t *testing.T) {
	for _, mutation := range []string{"kind", "missing-reason", "missing-related", "unknown-related", "duplicate-related", "duplicate-review", "already-referenced", "gap-and-review", "unrelated-document"} {
		t.Run(mutation, func(t *testing.T) {
			p := sourceReviewFixture(t)
			p.Catalog.SourceGaps = nil
			r := SourceReview{ID: "M:5-5", Kind: "context", Reason: "Synthetic background for M01", Related: []string{"M01"}}
			switch mutation {
			case "kind":
				r.Kind = "implemented"
			case "missing-reason":
				r.Reason = " "
			case "missing-related":
				r.Related = nil
			case "unknown-related":
				r.Related = []string{"M99"}
			case "duplicate-related":
				r.Related = []string{"M01", "M01"}
			case "already-referenced":
				r.ID = "M:3-3"
			case "gap-and-review":
				p.Catalog.SourceGaps = []Gap{{ID: r.ID, Reason: "Still open"}}
			case "unrelated-document":
				other := p.Catalog.Requirements[0]
				other.ID = "M02"
				other.Citation = "C:3"
				other.Sources = []Reference{{Source: "C", Start: 3, End: 3}}
				p.Catalog.Requirements = append(p.Catalog.Requirements, other)
				r.Related = []string{"M02"}
			}
			p.Catalog.SourceReviews = []SourceReview{r}
			if mutation == "duplicate-review" {
				p.Catalog.SourceReviews = append(p.Catalog.SourceReviews, r)
			}
			if err := writeJSON(filepath.Join(p.Root, CatalogPath), p.Catalog); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(p.Root); err == nil {
				t.Fatal("invalid source review was accepted")
			}
		})
	}
}
