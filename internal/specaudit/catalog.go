// Package specaudit validates RC1 traceability and records test evidence.
// A passing test is deliberately not a declaration of protocol conformance.
package specaudit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/echocat/sendspin-player-go/internal/protocol"
)

const CatalogPath = "docs/rc1-requirements.json"
const IndexPath = "docs/rc1-source-index.json"
const TemplatePath = "docs/rc1-audit.template.md"
const AuditPath = "docs/rc1-audit.md"

type Catalog struct {
	Schema                int                    `json:"schema"`
	Specification         string                 `json:"specification"`
	Revision              string                 `json:"revision"`
	InventoryReview       string                 `json:"inventory_review"`
	Scope                 string                 `json:"scope"`
	IndexSHA256           string                 `json:"index_sha256"`
	InterpretationSources []InterpretationSource `json:"interpretation_sources,omitempty"`
	TemplateInventoryPath string                 `json:"template_inventory,omitempty"`
	Requirements          []Requirement          `json:"requirements"`
	Obligations           []Obligation           `json:"obligations,omitempty"`
	NormativeReviews      []NormativeReview      `json:"normative_reviews,omitempty"`
	WorkingAssumptions    []WorkingAssumption    `json:"working_assumptions,omitempty"`
	Evidence              []Evidence             `json:"evidence"`
	SourceGaps            []Gap                  `json:"source_gaps"`
	SourceReviews         []SourceReview         `json:"source_reviews,omitempty"`
	UnmappedTests         []Gap                  `json:"unmapped_tests"`
}

type Requirement struct {
	ID             string      `json:"id"`
	Section        string      `json:"section"`
	Citation       string      `json:"citation"`
	Statement      string      `json:"statement"`
	Level          string      `json:"level"`
	AppliesTo      string      `json:"applies_to"`
	Assessment     string      `json:"assessment"`
	Notes          string      `json:"notes"`
	Sources        []Reference `json:"sources"`
	Implementation []string    `json:"implementation"`
	Evidence       []string    `json:"evidence"`
}

type Reference struct {
	Source string `json:"source"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
}

type Evidence struct {
	ID       string `json:"id"`
	Suite    string `json:"suite"`
	Package  string `json:"package,omitempty"`
	Test     string `json:"test,omitempty"`
	Scenario string `json:"scenario,omitempty"`
}

type Gap struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// SourceReview records why a paragraph introduces no additional independent
// implementation obligation. It never changes a requirement's assessment.
// Normative server/optional clauses belong in Requirements, not in this list.
type SourceReview struct {
	ID      string   `json:"id"`
	Kind    string   `json:"kind"`
	Reason  string   `json:"reason"`
	Related []string `json:"related"`
}

// InterpretationSource pins context used for a classification decision. It is
// not an atomic inventory of that document, nor a replacement for SourceIndex.
type InterpretationSource struct {
	Path    string `json:"path"`
	Blob    string `json:"git_blob"`
	SHA256  string `json:"sha256"`
	Lines   int    `json:"lines"`
	Purpose string `json:"purpose"`
}

type SourceIndex struct {
	Schema                int          `json:"schema"`
	Revision              string       `json:"revision"`
	Description           string       `json:"description"`
	RequiredIDs           []string     `json:"required_ids"`
	RequiredObligationIDs []string     `json:"required_obligation_ids,omitempty"`
	Sources               []SpecSource `json:"sources"`
}

type SpecSource struct {
	ID     string        `json:"id"`
	Path   string        `json:"path"`
	Blob   string        `json:"git_blob"`
	SHA256 string        `json:"sha256"`
	Lines  int           `json:"lines"`
	Blocks []SourceBlock `json:"blocks"`
}

// SourceBlock contains fingerprints/locations, not copies of upstream prose. Blocks
// inventory source paragraphs, not automatically extracted normative obligations.
type SourceBlock struct {
	ID     string `json:"id"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
	SHA256 string `json:"sha256"`
	Kind   string `json:"kind"`
}

type Project struct {
	Root              string
	Catalog           Catalog
	Index             SourceIndex
	Inventory         Inventory
	CatalogDigest     string
	SourceDigest      string
	TemplateInventory *TemplateInventory
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func decodeStrict(data []byte, value any) error {
	if err := uniqueJSONKeys(json.NewDecoder(bytes.NewReader(data))); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("unexpected trailing JSON value")
	}
	return nil
}

func uniqueJSONKeys(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	keys := map[string]bool{}
	for d.More() {
		if delim == '{' {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || keys[name] {
				return fmt.Errorf("invalid/duplicate JSON key %q", key)
			}
			keys[name] = true
		}
		if err := uniqueJSONKeys(d); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}

func Load(root string) (*Project, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(root, CatalogPath))
	if err != nil {
		return nil, err
	}
	index, err := os.ReadFile(filepath.Join(root, IndexPath))
	if err != nil {
		return nil, err
	}
	p := &Project{Root: root, CatalogDigest: digest(data)}
	if err = decodeStrict(data, &p.Catalog); err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	if err = decodeStrict(index, &p.Index); err != nil {
		return nil, fmt.Errorf("source index: %w", err)
	}
	if p.Catalog.IndexSHA256 != digest(index) {
		return nil, errors.New("source index digest changed: review and update the catalog pin")
	}
	p.Inventory, err = Discover(root)
	if err != nil {
		return nil, err
	}
	if p.Catalog.TemplateInventoryPath != "" {
		if p.Catalog.TemplateInventoryPath != "docs/rc1-template-inventory.json" {
			return nil, errors.New("invalid template inventory path")
		}
		data, err := os.ReadFile(filepath.Join(root, p.Catalog.TemplateInventoryPath))
		if err != nil {
			return nil, err
		}
		p.TemplateInventory = &TemplateInventory{}
		if err = decodeStrict(data, p.TemplateInventory); err != nil {
			return nil, err
		}
	}
	if err = p.Validate(); err != nil {
		return nil, err
	}
	p.SourceDigest, err = Fingerprint(root)
	return p, err
}

var requirementID = regexp.MustCompile(`^(M|CN|P|CT|MD|CL|AW|VZ|R)[0-9]{2,}$`)
var shaPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var blobPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var sourcePaths = map[string]string{"M": "messaging.md", "C": "connection.md", "R": "pairing.md", "P": "roles/player/v1.md", "CT": "roles/controller/v1.md", "MD": "roles/metadata/v1.md", "CL": "roles/color/v1.md", "AW": "roles/artwork/v1.md", "VZ": "roles/visualizer/v1.md"}
var citationMarker = regexp.MustCompile(`\b([A-Z]+):`)
var citationRange = regexp.MustCompile(`([0-9]+)(?:[–-]([0-9]+))?`)

func (p *Project) Validate() error {
	if err := p.validateTemplateInventory(); err != nil {
		return err
	}
	c := p.Catalog
	if c.Schema != 1 || p.Index.Schema != 1 || c.Specification != protocol.Specification || c.Revision != protocol.Revision || p.Index.Revision != protocol.Revision {
		return errors.New("catalog/index schema or pinned implementation specification mismatch")
	}
	if !slices.Contains([]string{"partial", "complete"}, c.InventoryReview) || strings.TrimSpace(c.Scope) == "" {
		return errors.New("inventory review and explicit scope required")
	}
	sources := map[string]SpecSource{}
	blocks := map[string]SourceBlock{}
	for _, source := range p.Index.Sources {
		if source.ID == "" || sources[source.ID].ID != "" || !fs.ValidPath(source.Path) || sourcePaths[source.ID] != source.Path || source.Lines < 1 || len(source.Blocks) == 0 || !shaPattern.MatchString(source.SHA256) || !blobPattern.MatchString(source.Blob) {
			return fmt.Errorf("invalid/duplicate spec source %q", source.ID)
		}
		last := 0
		for _, block := range source.Blocks {
			if block.ID != fmt.Sprintf("%s:%d-%d", source.ID, block.Start, block.End) || block.Start <= last || block.End < block.Start || block.End > source.Lines || !shaPattern.MatchString(block.SHA256) || !slices.Contains([]string{"heading", "content"}, block.Kind) {
				return fmt.Errorf("invalid source block %q", block.ID)
			}
			last = block.End
			blocks[block.ID] = block
		}
		sources[source.ID] = source
	}
	if len(sources) != 9 {
		return errors.New("the RC1 audit must inventory all nine pinned source documents")
	}
	contextPaths := map[string]bool{}
	for _, source := range c.InterpretationSources {
		if source.Path != "template.md" || contextPaths[source.Path] || !blobPattern.MatchString(source.Blob) || !shaPattern.MatchString(source.SHA256) || source.Lines < 1 || strings.TrimSpace(source.Purpose) == "" {
			return fmt.Errorf("invalid/duplicate interpretation source %q", source.Path)
		}
		contextPaths[source.Path] = true
	}
	refs := map[string]Evidence{}
	for _, e := range c.Evidence {
		if e.ID == "" || refs[e.ID].ID != "" {
			return fmt.Errorf("duplicate/empty evidence ID %q", e.ID)
		}
		if err := p.validateEvidence(e); err != nil {
			return err
		}
		refs[e.ID] = e
	}
	ids, used := map[string]bool{}, map[string]bool{}
	for _, r := range c.Requirements {
		if !requirementID.MatchString(r.ID) || ids[r.ID] || r.Section == "" || r.Statement == "" || r.Citation == "" || r.Notes == "" {
			return fmt.Errorf("invalid/duplicate requirement %q", r.ID)
		}
		ids[r.ID] = true
		if !slices.Contains([]string{"must", "should", "may", "mixed", "informative", "unclassified"}, r.Level) || !slices.Contains([]string{"client", "server", "mixed", "optional", "sdk-contract"}, r.AppliesTo) {
			return fmt.Errorf("%s: invalid level/applicability", r.ID)
		}
		seen := map[string]bool{}
		for _, status := range strings.Split(r.Assessment, "/") {
			if !slices.Contains([]string{"T", "C", "P", "O", "S", "D"}, status) || seen[status] {
				return fmt.Errorf("%s: invalid assessment", r.ID)
			}
			seen[status] = true
		}
		if len(r.Sources) == 0 {
			return fmt.Errorf("%s: missing source references", r.ID)
		}
		if r.AppliesTo != "server" && r.AppliesTo != "optional" && len(r.Implementation) == 0 {
			return fmt.Errorf("%s: missing implementation links", r.ID)
		}
		parsed := parseCitation(r.Citation)
		if len(parsed) == 0 {
			if r.AppliesTo != "sdk-contract" {
				return fmt.Errorf("%s: missing numeric human citation", r.ID)
			}
		} else if !slices.Equal(parsed, r.Sources) {
			return fmt.Errorf("%s: human citation and machine source ranges disagree", r.ID)
		}
		for _, ref := range r.Sources {
			source, ok := sources[ref.Source]
			if !ok || ref.Start < 1 || ref.End < ref.Start || ref.End > source.Lines {
				return fmt.Errorf("%s: invalid source reference %+v", r.ID, ref)
			}
		}
		for _, path := range r.Implementation {
			if !portablePath(path) || !p.Inventory.Files[path] || !strings.HasSuffix(path, ".go") {
				return fmt.Errorf("%s: unsafe implementation path", r.ID)
			}
			info, err := os.Lstat(filepath.Join(p.Root, filepath.FromSlash(path)))
			if err != nil || !info.Mode().IsRegular() {
				return fmt.Errorf("%s: missing/non-regular implementation %s", r.ID, path)
			}
		}
		seen = map[string]bool{}
		for _, id := range r.Evidence {
			if refs[id].ID == "" || seen[id] {
				return fmt.Errorf("%s: unknown/duplicate evidence %q", r.ID, id)
			}
			seen[id] = true
			used[id] = true
		}
		if strings.Contains(r.Assessment, "T") && len(r.Evidence) == 0 {
			return fmt.Errorf("%s: tested assessment without a machine-linked test", r.ID)
		}
	}
	baseline := map[string]bool{}
	for _, id := range p.Index.RequiredIDs {
		if !requirementID.MatchString(id) || baseline[id] {
			return fmt.Errorf("invalid/duplicate baseline group %s", id)
		}
		baseline[id] = true
		if !ids[id] {
			return fmt.Errorf("missing baseline audit group %s", id)
		}
	}
	if err := p.validateObligations(ids, refs, sources, used); err != nil {
		return err
	}
	if err := p.validateWorkingAssumptions(refs, used); err != nil {
		return err
	}
	for id := range refs {
		if !used[id] {
			return fmt.Errorf("orphan evidence %s", id)
		}
	}
	knownGaps := map[string]bool{}
	reviewed := map[string]bool{}
	unreferenced := p.UnreferencedBlocks()
	for _, review := range c.SourceReviews {
		block, ok := blocks[review.ID]
		if !ok || block.Kind != "content" || reviewed[review.ID] || !slices.Contains([]string{"context", "example"}, review.Kind) || strings.TrimSpace(review.Reason) == "" || len(review.Related) == 0 || !slices.Contains(unreferenced, review.ID) {
			return fmt.Errorf("invalid/duplicate/obsolete source review %q", review.ID)
		}
		related := map[string]bool{}
		sameSource := false
		source, _, _ := strings.Cut(review.ID, ":")
		for _, id := range review.Related {
			if !ids[id] || related[id] {
				return fmt.Errorf("%s: unknown/duplicate related requirement %s", review.ID, id)
			}
			related[id] = true
			for _, r := range c.Requirements {
				if r.ID == id {
					for _, ref := range r.Sources {
						if ref.Source == source {
							sameSource = true
						}
					}
				}
			}
		}
		if !sameSource {
			return fmt.Errorf("%s: source review has no related requirement in its document", review.ID)
		}
		reviewed[review.ID] = true
	}
	for _, gap := range c.SourceGaps {
		if blocks[gap.ID].ID == "" || gap.Reason == "" || knownGaps[gap.ID] || reviewed[gap.ID] {
			return fmt.Errorf("invalid/duplicate source gap %q", gap.ID)
		}
		knownGaps[gap.ID] = true
	}
	for _, id := range unreferenced {
		if !knownGaps[id] && !reviewed[id] {
			return fmt.Errorf("new unmapped source block %s: add a requirement or an explicit open-review disposition", id)
		}
	}
	for id := range knownGaps {
		if !slices.Contains(unreferenced, id) {
			return fmt.Errorf("obsolete source-gap disposition %s", id)
		}
	}
	if c.InventoryReview == "complete" && len(p.OpenSourceBlocks()) != 0 {
		return errors.New("complete inventory review cannot contain open source blocks")
	}
	ignored := map[string]bool{}
	for _, gap := range c.UnmappedTests {
		if _, ok := p.Inventory.Tests[gap.ID]; !ok || gap.Reason == "" || ignored[gap.ID] || used[gap.ID] {
			return fmt.Errorf("invalid/duplicate unmapped-test disposition %q", gap.ID)
		}
		ignored[gap.ID] = true
	}
	for id, test := range p.Inventory.Tests {
		mapped := used[id]
		if test.Suite == "harness" {
			for _, e := range c.Evidence {
				if e.Suite == test.Suite && e.Package == test.Package && strings.HasPrefix(e.Test, test.Name+"/") {
					mapped = true
				}
			}
		}
		if (strings.Contains(test.Suite, "e2e") || slices.Contains([]string{"mdns", "harness", "reference"}, test.Suite)) && !mapped && !ignored[id] {
			return fmt.Errorf("unmapped E2E test %s", id)
		}
	}
	return nil
}

func (p *Project) OpenSourceBlocks() []string {
	reviewed := map[string]bool{}
	for _, review := range p.Catalog.SourceReviews {
		reviewed[review.ID] = true
	}
	var open []string
	for _, id := range p.UnreferencedBlocks() {
		if !reviewed[id] {
			open = append(open, id)
		}
	}
	return open
}

func portablePath(path string) bool { return fs.ValidPath(path) && !strings.ContainsAny(path, "\\:") }

func parseCitation(citation string) []Reference {
	markers := citationMarker.FindAllStringSubmatchIndex(citation, -1)
	var refs []Reference
	for i, marker := range markers {
		end := len(citation)
		if i+1 < len(markers) {
			end = markers[i+1][0]
		}
		for _, match := range citationRange.FindAllStringSubmatch(citation[marker[1]:end], -1) {
			var start, last int
			_, _ = fmt.Sscan(match[1], &start)
			last = start
			if match[2] != "" {
				_, _ = fmt.Sscan(match[2], &last)
			}
			refs = append(refs, Reference{citation[marker[2]:marker[3]], start, last})
		}
	}
	return refs
}

func (p *Project) validateEvidence(e Evidence) error {
	if e.Suite == "harness" {
		parent, scenario, ok := strings.Cut(e.Test, "/")
		test, exists := p.Inventory.Tests[TestID(e.Suite, e.Package, parent)]
		if !ok || !exists || test.Suite != "harness" || e.ID != TestID(e.Suite, e.Package, e.Test) || scenario != e.Scenario || !slices.Contains(HarnessScenarios, e.Scenario) {
			return fmt.Errorf("invalid harness evidence %q", e.ID)
		}
		return nil
	}
	test, ok := p.Inventory.Tests[e.ID]
	if !ok || e.ID != TestID(e.Suite, e.Package, e.Test) || e.Scenario != "" || test.Package != e.Package || test.Name != e.Test || test.Suite != e.Suite {
		return fmt.Errorf("unknown test or wrong suite: %s", e.ID)
	}
	return nil
}

func (p *Project) evidenceTest(e Evidence) (Test, bool) {
	name := e.Test
	if e.Suite == "harness" {
		name, _, _ = strings.Cut(name, "/")
	}
	test, ok := p.Inventory.Tests[TestID(e.Suite, e.Package, name)]
	return test, ok
}

func (p *Project) UnreferencedBlocks() []string {
	var missing []string
	for _, source := range p.Index.Sources {
		for _, block := range source.Blocks {
			if block.Kind == "heading" {
				continue
			}
			covered := true
			for line := block.Start; line <= block.End; line++ {
				found := false
				for _, r := range p.Catalog.Requirements {
					for _, ref := range r.Sources {
						if ref.Source == source.ID && line >= ref.Start && line <= ref.End {
							found = true
							break
						}
					}
					if found {
						break
					}
				}
				if !found {
					covered = false
					break
				}
			}
			if !covered {
				missing = append(missing, block.ID)
			}
		}
	}
	return missing
}
