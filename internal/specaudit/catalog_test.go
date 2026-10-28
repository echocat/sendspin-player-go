package specaudit

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func put(t *testing.T, root, name, text string) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) *Project {
	t.Helper()
	root := t.TempDir()
	put(t, root, "go.mod", "module example.test/audit\n\ngo 1.27.3\n")
	put(t, root, "impl.go", "package audit\nfunc Implementation() {}\n")
	put(t, root, "impl_test.go", "package audit\nimport \"testing\"\nfunc TestOne(t *testing.T) {}\n")
	put(t, root, TemplatePath, "{{requirements:core}}\n")
	index := SourceIndex{Schema: 1, Revision: "671a34d408581fc25ea56b3528a4a3f13e3be901", Description: "Synthetic validation fixture", RequiredIDs: []string{"M01"}}
	var references []Reference
	var citations []string
	ids := []string{"M", "C", "R", "P", "CT", "MD", "CL", "AW", "VZ"}
	for _, id := range ids {
		data := []byte("# Heading\n\nA normative statement.\n")
		blob := sha1.Sum(append([]byte(fmt.Sprintf("blob %d\x00", len(data))), data...))
		index.Sources = append(index.Sources, SpecSource{ID: id, Path: sourcePaths[id], Blob: hex.EncodeToString(blob[:]), SHA256: digest(data), Lines: 3, Blocks: paragraphs(id, splitLines(data))})
		references = append(references, Reference{id, 3, 3})
		citations = append(citations, id+":3")
	}
	if err := writeJSON(filepath.Join(root, IndexPath), index); err != nil {
		t.Fatal(err)
	}
	indexData, err := os.ReadFile(filepath.Join(root, IndexPath))
	if err != nil {
		t.Fatal(err)
	}
	e := Evidence{ID: TestID("unit", "example.test/audit", "TestOne"), Suite: "unit", Package: "example.test/audit", Test: "TestOne"}
	catalog := Catalog{Schema: 1, Specification: "1.0.0-rc1", Revision: index.Revision, InventoryReview: "partial", Scope: "Synthetic, not certification", IndexSHA256: digest(indexData), Evidence: []Evidence{e}, Requirements: []Requirement{{ID: "M01", Section: "core", Citation: strings.Join(citations, "; "), Statement: "Synthetic statement", Level: "must", AppliesTo: "client", Assessment: "T", Notes: "Review remains partial", Sources: references, Implementation: []string{"impl.go"}, Evidence: []string{e.ID}}}}
	if err = writeJSON(filepath.Join(root, CatalogPath), catalog); err != nil {
		t.Fatal(err)
	}
	p, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := p.RenderAudit()
	if err != nil {
		t.Fatal(err)
	}
	put(t, root, AuditPath, string(rendered))
	p, err = Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRealCatalogAndGeneratedAudit(t *testing.T) {
	if os.Getenv("SENDSPIN_ARCHIVE_INNER") == "1" {
		t.Skip("source-checkout traceability includes intentionally excluded example/native modules")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate checkout")
	}
	p, err := Load(filepath.Clean(filepath.Join(filepath.Dir(file), "../..")))
	if err != nil {
		t.Fatal(err)
	}
	if err = p.CheckGenerated(); err != nil {
		t.Fatal(err)
	}
	if p.Catalog.InventoryReview != "partial" {
		t.Fatal("open classification and external-scope decisions must not become a complete inventory")
	}
	if len(p.Catalog.NormativeReviews) != len(p.Catalog.Requirements) {
		t.Fatal("every historical group must retain its atomic review disposition")
	}
	if p.TemplateInventory == nil || len(p.TemplateInventory.Clauses) != 34 {
		t.Fatal("supplemental template inventory disappeared")
	}
}

func TestCatalogRejectsBrokenLinksAndMissingMappings(t *testing.T) {
	for _, name := range []string{"duplicate-id", "deleted-id", "missing-test", "wrong-suite", "untested-T", "invalid-source", "mismatched-citation", "unknown-source", "missing-implementation", "orphan-evidence", "unmapped-e2e", "seed-is-not-fuzz", "complete-with-gap"} {
		t.Run(name, func(t *testing.T) {
			p := fixture(t)
			catalog := p.Catalog
			switch name {
			case "duplicate-id":
				catalog.Requirements = append(catalog.Requirements, catalog.Requirements[0])
			case "deleted-id":
				catalog.Requirements = nil
			case "missing-test":
				catalog.Evidence[0].Test = "TestMissing"
			case "wrong-suite":
				catalog.Evidence[0].Suite = "e2e"
			case "untested-T":
				catalog.Requirements[0].Evidence = nil
			case "invalid-source":
				catalog.Requirements[0].Sources[0].End = 999
			case "mismatched-citation":
				catalog.Requirements[0].Citation = "M:1"
			case "unknown-source":
				catalog.Requirements[0].Sources[0].Source = "FUTURE"
			case "missing-implementation":
				catalog.Requirements[0].Implementation = []string{"deleted.go"}
			case "orphan-evidence":
				put(t, p.Root, "other_test.go", "package audit\nimport \"testing\"\nfunc TestOther(t *testing.T) {}\n")
				catalog.Evidence = append(catalog.Evidence, Evidence{ID: TestID("unit", "example.test/audit", "TestOther"), Suite: "unit", Package: "example.test/audit", Test: "TestOther"})
			case "unmapped-e2e":
				put(t, p.Root, "peer_test.go", "//go:build e2e\n\npackage audit\nimport \"testing\"\nfunc TestPeer(t *testing.T) {}\n")
			case "seed-is-not-fuzz":
				put(t, p.Root, "fuzz_test.go", "package audit\nimport \"testing\"\nfunc FuzzPayload(f *testing.F) {}\n")
				catalog.Evidence[0] = Evidence{ID: TestID("unit", "example.test/audit", "FuzzPayload"), Suite: "unit", Package: "example.test/audit", Test: "FuzzPayload"}
				catalog.Requirements[0].Evidence = []string{catalog.Evidence[0].ID}
			case "complete-with-gap":
				catalog.InventoryReview = "complete"
				catalog.Requirements[0].Sources = catalog.Requirements[0].Sources[1:]
				catalog.Requirements[0].Citation = strings.TrimPrefix(catalog.Requirements[0].Citation, "M:3; ")
				catalog.SourceGaps = []Gap{{ID: "M:3-3", Reason: "still open"}}
			}
			if err := writeJSON(filepath.Join(p.Root, CatalogPath), catalog); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(p.Root); err == nil {
				t.Fatal("invalid traceability was accepted")
			}
		})
	}
}

func TestCatalogRejectsDuplicateKeysUnknownFieldsAndChangedIndex(t *testing.T) {
	for _, name := range []string{"duplicate-key", "unknown-field", "trailing-json", "index-pin"} {
		t.Run(name, func(t *testing.T) {
			p := fixture(t)
			data, err := os.ReadFile(filepath.Join(p.Root, CatalogPath))
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "duplicate-key":
				data = []byte(strings.Replace(string(data), `"schema": 1`, `"schema": 1, "schema": 1`, 1))
			case "unknown-field":
				data = []byte(strings.Replace(string(data), "{", `{"invented": true,`, 1))
			case "trailing-json":
				data = append(data, []byte(`{}`)...)
			case "index-pin":
				index := p.Index
				index.Sources[0].Lines++
				if err = writeJSON(filepath.Join(p.Root, IndexPath), index); err != nil {
					t.Fatal(err)
				}
			}
			if err = os.WriteFile(filepath.Join(p.Root, CatalogPath), data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = Load(p.Root); err == nil {
				t.Fatal("invalid catalog/index accepted")
			}
		})
	}
}

func TestUnmappedPeerNeedsAnExplicitDisposition(t *testing.T) {
	p := fixture(t)
	put(t, p.Root, "peer_test.go", "//go:build mdns_e2e && e2e\n\npackage audit\nimport \"testing\"\nfunc TestPeer(t *testing.T) {}\n")
	p.Catalog.UnmappedTests = []Gap{{ID: TestID("mdns", "example.test/audit", "TestPeer"), Reason: "Synthetic test not claimed as normative evidence"}}
	if err := writeJSON(filepath.Join(p.Root, CatalogPath), p.Catalog); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p.Root); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoveryUsesDeclarationsAndActualBuildHeader(t *testing.T) {
	p := fixture(t)
	put(t, p.Root, "mention_test.go", "package audit\nimport tt \"testing\"\n// TestMention is prose, not a declaration.\nconst text = \"//go:build e2e\"\nfunc TestActual(t *tt.T) {}\nfunc Testhelper(t *tt.T) {}\nfunc TestNotATest() {}\n")
	inventory, err := Discover(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := inventory.Tests[TestID("unit", "example.test/audit", "TestActual")]; !ok {
		t.Fatal("lost alias signature or misclassified a string literal as a build tag")
	}
	if len(inventory.Tests) != 2 {
		t.Fatal("discovered prose/non-test declarations", inventory.Tests)
	}
}

func TestFingerprintIgnoresGeneratedAndIDEButTracksTestsAndDefinitions(t *testing.T) {
	p := fixture(t)
	before, err := Fingerprint(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	put(t, p.Root, "var/private.go", "generated")
	put(t, p.Root, ".idea/private.go", "IDE")
	put(t, p.Root, "device.json", "not a test input")
	after, err := Fingerprint(p.Root)
	if err != nil || before != after {
		t.Fatal("generated/IDE/runtime data contaminated provenance", err)
	}
	for _, name := range []string{"impl_test.go", CatalogPath, ".github/workflows/ci.yml", "mise.toml"} {
		put(t, p.Root, name, "changed source input")
		after, err = Fingerprint(p.Root)
		if err != nil || before == after {
			t.Fatal("changed source input did not invalidate evidence", name, err)
		}
		before = after
	}
}

func TestSourceFingerprintsDetectOmittedParagraphs(t *testing.T) {
	p := fixture(t)
	source := p.Index.Sources[0]
	data := []byte("# Heading\n\nA normative statement.\n")
	if err := verifySource(source, data); err != nil {
		t.Fatal(err)
	}
	source.Blocks = source.Blocks[:1]
	if err := verifySource(source, data); err == nil {
		t.Fatal("omitted source paragraph was accepted")
	}
	blocks := paragraphs("M", splitLines([]byte("# Heading\nRequirement immediately after heading.\n")))
	if len(blocks) != 1 || blocks[0].Kind != "content" {
		t.Fatal("a heading hid normative text")
	}
}

func TestGeneratedAuditCannotDrift(t *testing.T) {
	p := fixture(t)
	if err := p.CheckGenerated(); err != nil {
		t.Fatal(err)
	}
	put(t, p.Root, AuditPath, "hand-edited table")
	if err := p.CheckGenerated(); err == nil {
		t.Fatal("manual generated-document drift accepted")
	}
}

func TestCatalogJSONRoundtripDoesNotChangeAssessments(t *testing.T) {
	p := fixture(t)
	data, err := json.Marshal(p.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	var clone Catalog
	if err = decodeStrict(data, &clone); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(clone.Requirements[0].Evidence, p.Catalog.Requirements[0].Evidence) || clone.Requirements[0].Assessment != "T" {
		t.Fatal("roundtrip changed review data")
	}
}
