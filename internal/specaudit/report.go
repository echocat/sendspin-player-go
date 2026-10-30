package specaudit

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

var backendHardwareTests = []string{"TestNativeLoopback", "TestReferenceServerPlaybackScenario", "TestReferenceServerNativeInputScenario"}

type EvidenceReport struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Suite    string `json:"suite"`
	Kind     string `json:"kind"`
	TestFile string `json:"test_file,omitempty"`
}

type RequirementReport struct {
	ID             string           `json:"id"`
	Statement      string           `json:"statement"`
	Assessment     string           `json:"assessment"`
	Applicability  string           `json:"applicability"`
	Level          string           `json:"level"`
	EvidenceStatus string           `json:"evidence_status"`
	Evidence       []EvidenceReport `json:"evidence"`
	Notes          string           `json:"notes"`
}

type SuiteReport struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Path   string `json:"manifest,omitempty"`
	Scope  string `json:"scope"`
}

type ObligationReport struct {
	Obligation
	EvidenceStatus     string           `json:"evidence_status"`
	VerificationStatus string           `json:"verification_status"`
	Evidence           []EvidenceReport `json:"evidence"`
}

type WorkingAssumptionReport struct {
	WorkingAssumption
	EvidenceStatus string           `json:"evidence_status"`
	Evidence       []EvidenceReport `json:"evidence"`
}

type BacklogItem struct {
	ID        string   `json:"id"`
	Groups    []string `json:"groups"`
	Priority  int      `json:"priority"`
	Category  string   `json:"category"`
	Level     string   `json:"level"`
	Actor     string   `json:"actor"`
	Condition string   `json:"condition"`
	Statement string   `json:"statement"`
	Notes     string   `json:"notes"`
}

type Report struct {
	Specification            string                    `json:"specification"`
	Revision                 string                    `json:"revision"`
	CatalogSHA256            string                    `json:"catalog_sha256"`
	SourceSHA256             string                    `json:"source_sha256"`
	GOOS                     string                    `json:"goos"`
	GoVersion                string                    `json:"go_version"`
	GOARCH                   string                    `json:"goarch"`
	Variant                  string                    `json:"variant"`
	Profile                  string                    `json:"profile"`
	InventoryReview          string                    `json:"inventory_review"`
	Scope                    string                    `json:"scope"`
	ConformanceClaim         bool                      `json:"conformance_claim"`
	ConformanceBlockers      []string                  `json:"conformance_blockers"`
	UnreferencedSourceBlocks []string                  `json:"unreferenced_source_blocks"`
	OpenSourceBlocks         []string                  `json:"open_source_blocks"`
	SourceReviews            []SourceReview            `json:"source_reviews"`
	InterpretationSources    []InterpretationSource    `json:"interpretation_sources,omitempty"`
	TemplateInventory        *TemplateInventory        `json:"template_inventory,omitempty"`
	Suites                   []SuiteReport             `json:"suites"`
	Requirements             []RequirementReport       `json:"requirements"`
	Obligations              []ObligationReport        `json:"obligations"`
	NormativeReviews         []NormativeReview         `json:"normative_reviews"`
	WorkingAssumptions       []WorkingAssumptionReport `json:"working_assumptions,omitempty"`
	UnreviewedGroups         []string                  `json:"groups_without_complete_atomic_review"`
	Backlog                  []BacklogItem             `json:"prioritized_remaining_work"`
	Problems                 []string                  `json:"problems"`
}

type ReportOptions struct{ EvidenceDir, Profile, GOOS, GOARCH, GoVersion, Variant string }

func (p *Project) Report(options ReportOptions) (Report, error) {
	if options.GOOS == "" {
		options.GOOS = runtime.GOOS
	}
	if options.GoVersion == "" {
		options.GoVersion = runtime.Version()
	}
	if options.GOARCH == "" {
		options.GOARCH = runtime.GOARCH
	}
	if options.Variant == "" {
		options.Variant = "default"
	}
	if options.Profile == "" {
		options.Profile = "catalog"
	}
	var required []string
	switch options.Profile {
	case "catalog":
	case "full":
		required = FullSuites
	case "native":
		required = []string{"desktop-audio", "examples"}
	case "alsa":
		required = []string{"alsa"}
	case "desktop-audio":
		required = []string{"desktop-audio"}
	case "desktop-audio-e2e", "alsa-e2e":
		required = []string{options.Profile}
	default:
		return Report{}, fmt.Errorf("unknown evidence profile %q", options.Profile)
	}
	report := Report{Specification: p.Catalog.Specification, Revision: p.Catalog.Revision, CatalogSHA256: p.CatalogDigest, SourceSHA256: p.SourceDigest, GOOS: options.GOOS, GOARCH: options.GOARCH, GoVersion: options.GoVersion, Variant: options.Variant, Profile: options.Profile, InventoryReview: p.Catalog.InventoryReview, Scope: p.Catalog.Scope, UnreferencedSourceBlocks: p.UnreferencedBlocks(), OpenSourceBlocks: p.OpenSourceBlocks(), SourceReviews: p.Catalog.SourceReviews}
	if options.EvidenceDir == "" {
		options.EvidenceDir = filepath.Join(p.Root, "var", "audit", "evidence")
	}
	type loaded struct {
		manifest     Manifest
		results      []Result
		status, path string
	}
	suites := map[string]loaded{}
	if _, err := os.Stat(options.EvidenceDir); err == nil {
		err = filepath.WalkDir(options.EvidenceDir, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("evidence must not contain symlinks")
			}
			if entry.IsDir() || entry.Name() != "current.json" {
				return nil
			}
			pathSuite := filepath.Base(filepath.Dir(filepath.Dir(path)))
			if !slices.Contains(append(slices.Clone(FullSuites), BackendSuites...), pathSuite) {
				return nil
			}
			manifest, directory, err := readManifest(path)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			if manifest.GOOS != options.GOOS || manifest.GOARCH != options.GOARCH || manifest.GoVersion != options.GoVersion || manifest.Variant != options.Variant {
				return nil
			}
			if manifest.Suite != pathSuite {
				return fmt.Errorf("evidence folder/suite mismatch: %s", path)
			}
			if _, exists := suites[manifest.Suite]; exists {
				return fmt.Errorf("ambiguous duplicate suite evidence for %s", manifest.Suite)
			}
			value := loaded{manifest: manifest, path: path, status: "pass"}
			if manifest.Schema != 1 || manifest.Suite == "" {
				return errors.New("invalid evidence manifest schema/suite")
			}
			if manifest.CatalogSHA256 != p.CatalogDigest || manifest.SourceSHA256 != p.SourceDigest || (ciRun() != "" && manifest.CIRun != ciRun()) {
				value.status = "stale"
			} else if !manifest.Complete || manifest.ExitCode != 0 || len(manifest.Commands) == 0 || manifest.Started.IsZero() || manifest.Finished.Before(manifest.Started) {
				value.status = "fail"
			}
			if err := p.validateCommands(manifest); err != nil {
				value.status = "fail"
			}
			for _, command := range manifest.Commands {
				if !portablePath(command.Log) || !shaPattern.MatchString(command.SHA256) || command.ExitCode != 0 {
					value.status = "fail"
					continue
				}
				data, err := readEvidenceFile(directory, command.Log)
				if err != nil || digest(data) != command.SHA256 {
					value.status = "fail"
					continue
				}
				var results []Result
				if command.Kind == "harness" {
					results, err = ReadHarnessResults(bytes.NewReader(data))
				} else if command.Kind == "go" || command.Kind == "mdns" {
					results, err = ReadGoResults(bytes.NewReader(data))
				} else {
					err = errors.New("unknown evidence command kind")
				}
				if err != nil || len(results) == 0 {
					value.status = "fail"
					continue
				}
				for _, result := range results {
					if result.Status == "fail" || result.Status == "incomplete" {
						value.status = "fail"
					}
					// Tagged backend suites may compile successfully on hosted CI
					// while every hardware opt-in is skipped. Never call that a pass.
					if (manifest.Suite == "desktop-audio-e2e" || manifest.Suite == "alsa-e2e") && slices.Contains(backendHardwareTests, result.Name) && result.Status == "skip" && value.status == "pass" {
						value.status = "skip"
					}
				}
				value.results = append(value.results, results...)
			}
			if (manifest.Suite == "desktop-audio-e2e" || manifest.Suite == "alsa-e2e") && value.status == "pass" {
				for _, name := range backendHardwareTests {
					if !slices.ContainsFunc(value.results, func(r Result) bool { return r.Name == name && r.Status == "pass" }) {
						value.status = "not-run"
					}
				}
			}
			suites[manifest.Suite] = value
			return nil
		})
		if err != nil {
			return report, err
		}
	} else if !os.IsNotExist(err) {
		return report, err
	}
	allSuites := append(slices.Clone(FullSuites), BackendSuites...)
	for _, suite := range allSuites {
		value, ok := suites[suite]
		status := "not-run"
		if ok {
			status = value.status
		}
		report.Suites = append(report.Suites, SuiteReport{suite, status, value.path, suiteScope(suite)})
		if slices.Contains(required, suite) && status != "pass" {
			report.Problems = append(report.Problems, fmt.Sprintf("required suite %s: %s", suite, status))
		}
	}
	evidence := map[string]Evidence{}
	for _, e := range p.Catalog.Evidence {
		evidence[e.ID] = e
	}
	resolve := func(owner string, ids []string) ([]EvidenceReport, bool) {
		var entries []EvidenceReport
		allPassed := len(ids) > 0
		for _, id := range ids {
			e := evidence[id]
			entry := EvidenceReport{ID: id, Suite: e.Suite, Status: "not-run", Kind: e.Suite}
			if test, ok := p.Inventory.Tests[id]; ok {
				entry.TestFile = test.File
			}
			if value, ok := suites[e.Suite]; ok {
				entry.Status = value.status
				if entry.Status == "pass" {
					entry.Status = "not-run"
					for _, result := range value.results {
						if (e.Suite == "harness" && result.Name == e.Scenario) || (result.Package == e.Package && result.Name == e.Test) {
							entry.Status = result.Status
							break
						}
					}
				}
			}
			if entry.Status != "pass" {
				allPassed = false
				if slices.Contains(required, e.Suite) {
					report.Problems = append(report.Problems, owner+": "+id+" is "+entry.Status)
				}
			}
			entries = append(entries, entry)
		}
		return entries, allPassed
	}
	// Deliberately separate from normative resolution: an assumed policy's pass
	// never fills an atom's missing evidence, review decision or measurement.
	for _, a := range p.Catalog.WorkingAssumptions {
		entries, passed := resolve(a.ID, a.Evidence)
		status := "not-linked"
		if passed {
			status = "pass"
		} else if len(a.Evidence) > 0 {
			status = "incomplete"
		}
		report.WorkingAssumptions = append(report.WorkingAssumptions, WorkingAssumptionReport{WorkingAssumption: a, EvidenceStatus: status, Evidence: entries})
	}
	reviews := map[string]string{}
	report.InterpretationSources = p.Catalog.InterpretationSources
	report.TemplateInventory = p.TemplateInventory
	report.NormativeReviews = p.Catalog.NormativeReviews
	for _, review := range p.Catalog.NormativeReviews {
		reviews[review.Group] = review.Status
	}
	for _, r := range p.Catalog.Requirements {
		row := RequirementReport{ID: r.ID, Statement: r.Statement, Assessment: r.Assessment, Applicability: r.AppliesTo, Level: r.Level, Notes: r.Notes, EvidenceStatus: "not-linked"}
		entries, allPassed := resolve(r.ID, r.Evidence)
		row.Evidence = entries
		if allPassed {
			row.EvidenceStatus = "pass"
		} else if len(r.Evidence) > 0 {
			row.EvidenceStatus = "incomplete"
		}
		// Never promote the manually reviewed assessment because tests passed.
		if reviews[r.ID] != "complete" && r.AppliesTo != "server" && r.AppliesTo != "optional" && (r.Level == "must" || r.Level == "mixed" || r.Level == "unclassified") && (r.Assessment != "T" || !allPassed || r.Level != "must") {
			report.ConformanceBlockers = append(report.ConformanceBlockers, r.ID+": manual review/evidence remains incomplete")
		}
		report.Requirements = append(report.Requirements, row)
		if reviews[r.ID] != "complete" {
			report.UnreviewedGroups = append(report.UnreviewedGroups, r.ID)
		}
	}
	for _, o := range p.Catalog.Obligations {
		entries, passed := resolve(o.ID, o.Evidence)
		status := "not-linked"
		if passed {
			status = "pass"
		} else if len(o.Evidence) > 0 {
			status = "incomplete"
		}
		verificationStatus := "not-established"
		switch {
		case o.Scope == "excluded":
			verificationStatus = "excluded"
		case o.Scope == "undetermined":
			verificationStatus = "review-required"
		case o.Verification == "review" && o.Remaining == "review":
			verificationStatus = "review-required"
		case o.Verification == "hardware":
			verificationStatus = "measurement-required"
		case o.Verification == "software" && o.Assessment == "T" && passed:
			verificationStatus = "targeted-software-evidence"
		case o.Verification == "review" && o.Assessment == "C" && o.Remaining == "none":
			verificationStatus = "reviewed-definition-or-policy"
		}
		report.Obligations = append(report.Obligations, ObligationReport{Obligation: o, EvidenceStatus: status, VerificationStatus: verificationStatus, Evidence: entries})
		if o.Scope == "undetermined" || (o.Scope == "applicable" && (o.Level == "must" || o.Level == "must-not") && (o.Assessment != "T" || o.Verification != "software" || !passed)) {
			report.ConformanceBlockers = append(report.ConformanceBlockers, o.ID+": atomic review/mandatory evidence remains incomplete")
		}
		if o.Scope != "excluded" && o.Remaining != "none" {
			report.Backlog = append(report.Backlog, BacklogItem{ID: o.ID, Groups: o.Groups, Priority: o.Priority, Category: o.Remaining, Level: o.Level, Actor: o.Actor, Condition: o.Condition, Statement: o.Statement, Notes: o.Notes})
		}
	}
	workOrder := map[string]int{"review": 0, "implementation": 1, "software-evidence": 2, "measurement": 3, "platform-network": 4}
	slices.SortFunc(report.Backlog, func(a, b BacklogItem) int {
		if a.Priority < b.Priority {
			return -1
		}
		if a.Priority > b.Priority {
			return 1
		}
		// Resolve normative uncertainty before prescribing implementation or
		// tests. Measurements remain a distinct workstream, not a code defect.
		if workOrder[a.Category] < workOrder[b.Category] {
			return -1
		}
		if workOrder[a.Category] > workOrder[b.Category] {
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	if len(report.UnreviewedGroups) > 0 {
		report.ConformanceBlockers = append(report.ConformanceBlockers, "audit groups remain without a complete atomic normative review")
	}
	if p.Catalog.InventoryReview != "complete" {
		report.ConformanceBlockers = append([]string{"normative inventory review is partial; audit groups are not an exhaustive atomic requirement inventory"}, report.ConformanceBlockers...)
	}
	if len(report.OpenSourceBlocks) > 0 {
		report.ConformanceBlockers = append(report.ConformanceBlockers, "source paragraphs remain without requirement references or explicit context/example review")
	}
	if len(report.Problems) > 0 {
		report.ConformanceBlockers = append(report.ConformanceBlockers, "required execution evidence is missing, stale, skipped or failed")
	}
	return report, nil
}

func (r Report) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# RC1 traceability report\n\n**This is not a conformance certificate.** A test pass does not change a reviewed assessment.\n\n- Specification: `%s` at `%s`\n- Inventory review: **%s**\n- Execution filter: `%s/%s`, `%s`, variant `%s`; profile `%s`\n- Source SHA-256: `%s`\n- Catalog SHA-256: `%s`\n- Audit groups: %d; unreferenced source paragraphs: %d\n- Conformance gate: **%s**\n\n", r.Specification, r.Revision, r.InventoryReview, r.GOOS, r.GOARCH, r.GoVersion, r.Variant, r.Profile, r.SourceSHA256, r.CatalogSHA256, len(r.Requirements), len(r.UnreferencedSourceBlocks), map[bool]string{true: "BLOCKED", false: "eligible for manual release review (not certified)"}[len(r.ConformanceBlockers) > 0])
	fmt.Fprintf(&b, "Scope: %s\n\n", r.Scope)
	for _, source := range r.InterpretationSources {
		fmt.Fprintf(&b, "Interpretation context: [%s](https://github.com/Sendspin/spec/blob/%s/%s), Git blob `%s`, SHA-256 `%s`. %s A fingerprint alone is not a complete atomic inventory; a separate supplemental inventory, when present, follows below.\n\n", source.Path, r.Revision, source.Path, source.Blob, source.SHA256, source.Purpose)
	}
	fmt.Fprintf(&b, "Open source review paragraphs: **%d**; explicitly reviewed context/examples: **%d**. Reference footprint is not an implementation-coverage percentage.\n\n", len(r.OpenSourceBlocks), len(r.SourceReviews))
	fmt.Fprintf(&b, "Atomic obligations/dispositions: **%d**; groups without completed atomic review: **%d**. Group and obligation counts are separate inventories, not coverage percentages.\n\n", len(r.Obligations), len(r.UnreviewedGroups))
	if len(r.WorkingAssumptions) > 0 {
		fmt.Fprintf(&b, "Provisional working assumptions: **%d** (separate policy evidence; not normative review completion). Details follow in the full report.\n\n", len(r.WorkingAssumptions))
	}
	b.WriteString("## Executed suites\n\n| Suite | Evidence | Scope |\n| --- | --- | --- |\n")
	for _, suite := range r.Suites {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", suite.ID, suite.Status, suite.Scope)
	}
	b.WriteString("\n## Requirements → evidence\n\n| ID | Reviewed assessment | Applicability / level | Execution evidence | Requirement |\n| --- | --- | --- | --- | --- |\n")
	for _, row := range r.Requirements {
		fmt.Fprintf(&b, "| %s | %s | %s / %s | %s | %s |\n", row.ID, row.Assessment, row.Applicability, row.Level, row.EvidenceStatus, escape(row.Statement))
	}
	b.WriteString("\n## Prioritized remaining work from reviewed obligations\n\nPriority 1 is release-critical review/mandatory evidence; priority 2 is recommended behavior or supporting evidence; priority 3 is lower priority. This is a partial backlog: unreviewed groups may add work. Excluded server duties and optional choices are not SDK implementation bugs.\n\n| ID | Priority | Work category / level | Obligation |\n| --- | --- | --- | --- |\n")
	for _, item := range r.Backlog {
		fmt.Fprintf(&b, "| %s | %d | %s / %s | %s |\n", item.ID, item.Priority, item.Category, item.Level, escape(item.Statement))
	}
	b.WriteString("\n## Evidence details and remaining work\n")
	for _, row := range r.Requirements {
		fmt.Fprintf(&b, "\n### %s\n\n%s\n\n", row.ID, row.Notes)
		for _, e := range row.Evidence {
			fmt.Fprintf(&b, "- `%s`: **%s** (%s)\n", e.ID, e.Status, e.Kind)
		}
		if len(row.Evidence) == 0 {
			b.WriteString("- No machine-linked execution evidence.\n")
		}
	}
	writeWorkingAssumptions(&b, r.WorkingAssumptions)
	writeTemplateInventory(&b, r.TemplateInventory)
	b.WriteString("\n## Atomic normative obligations (independently reviewed)\n\nA complete group review means classification, not fulfillment. Parent assessments and test links are not inherited. Software execution cannot satisfy a hardware measurement obligation.\n\n| ID / groups | Actor / condition | Level / basis | Scope / assessment / verification | Execution evidence | Obligation |\n| --- | --- | --- | --- | --- | --- |\n")
	for _, o := range r.Obligations {
		fmt.Fprintf(&b, "| %s / %s | %s / %s | %s / %s | %s / %s / %s (%s) | %s | %s |\n", o.ID, strings.Join(o.Groups, ", "), o.Actor, escape(o.Condition), o.Level, o.Basis, o.Scope, o.Assessment, o.Verification, o.VerificationStatus, o.EvidenceStatus, escape(o.Statement))
	}
	for _, o := range r.Obligations {
		fmt.Fprintf(&b, "\n### %s\n\nSource: %s. Remaining: **%s**; priority: %d.\n\n%s\n", o.ID, o.Citation, o.Remaining, o.Priority, o.Notes)
		for _, e := range o.Evidence {
			fmt.Fprintf(&b, "- `%s`: **%s** (%s)\n", e.ID, e.Status, e.Kind)
		}
	}
	b.WriteString("\n## Normative group review decisions\n\n")
	for _, review := range r.NormativeReviews {
		fmt.Fprintf(&b, "- `%s`: **%s**. %s\n", review.Group, review.Status, review.Notes)
	}
	b.WriteString("\n## Groups without completed atomic review\n\n")
	for _, id := range r.UnreviewedGroups {
		fmt.Fprintf(&b, "- `%s`\n", id)
	}
	b.WriteString("\n## Source reference footprint\n\nThe following paragraphs lack full numeric requirement references. Reviewed context/examples are listed separately; only paragraphs in the open list are unresolved source reviews. Neither classification establishes implementation conformance.\n\n")
	for _, id := range r.UnreferencedSourceBlocks {
		fmt.Fprintf(&b, "- `%s`\n", id)
	}
	b.WriteString("\n## Reviewed source context and examples\n\n")
	for _, review := range r.SourceReviews {
		fmt.Fprintf(&b, "- `%s` — **%s**, related: %s. %s\n", review.ID, review.Kind, strings.Join(review.Related, ", "), review.Reason)
	}
	b.WriteString("\n## Open source reviews\n\n")
	if len(r.OpenSourceBlocks) == 0 {
		b.WriteString("None within the nine indexed documents. Atomic normative classification and implementation/hardware evidence may still be incomplete.\n")
	}
	for _, id := range r.OpenSourceBlocks {
		fmt.Fprintf(&b, "- `%s`\n", id)
	}
	b.WriteString("\n## Conformance blockers\n\n")
	for _, problem := range r.ConformanceBlockers {
		fmt.Fprintf(&b, "- %s\n", problem)
	}
	if len(r.Problems) > 0 {
		b.WriteString("\n## Execution problems\n\n")
		for _, problem := range r.Problems {
			fmt.Fprintf(&b, "- %s\n", problem)
		}
	}
	return b.String()
}

func escape(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ") }

func writeWorkingAssumptions(b *strings.Builder, assumptions []WorkingAssumptionReport) {
	if len(assumptions) == 0 {
		return
	}
	b.WriteString("\n## Provisional working assumptions (not normative evidence)\n\nPinned C++ source inspection informs these policies; it is not a C++ runtime test or a conformance proof. Passing policy tests do not resolve the linked normative obligations.\n")
	for _, a := range assumptions {
		fmt.Fprintf(b, "\n### %s — %s\n\nLinked obligations: %s. Policy evidence: **%s**.\n\nDecision: %s\n\nReason / differences: %s\n\nRevisit when: %s\n", a.ID, a.Status, strings.Join(a.Obligations, ", "), a.EvidenceStatus, a.Decision, a.Reason, a.Revisit)
		for _, ref := range a.References {
			fmt.Fprintf(b, "\n- [Pinned C++ source](%s)", ref)
		}
		for _, e := range a.Evidence {
			fmt.Fprintf(b, "\n- `%s`: **%s**", e.ID, e.Status)
		}
		b.WriteString("\n")
	}
}

func (p *Project) RenderAudit() ([]byte, error) {
	template, err := os.ReadFile(filepath.Join(p.Root, TemplatePath))
	if err != nil {
		return nil, err
	}
	text := string(bytes.ReplaceAll(template, []byte("\r\n"), []byte("\n")))
	text = strings.ReplaceAll(text, "| ID | Source / client obligation | Status | Implementation and evidence / remaining work |", "| ID | Source / client obligation | Status | Implementation and evidence / remaining work | Declared execution links (not results) |")
	text = strings.ReplaceAll(text, "| --- | --- | --- | --- |", "| --- | --- | --- | --- | --- |")
	evidence := map[string]Evidence{}
	for _, e := range p.Catalog.Evidence {
		evidence[e.ID] = e
	}
	sections := map[string][]Requirement{}
	for _, r := range p.Catalog.Requirements {
		sections[r.Section] = append(sections[r.Section], r)
	}
	for section, rows := range sections {
		marker := "{{requirements:" + section + "}}"
		if strings.Count(text, marker) != 1 {
			return nil, fmt.Errorf("missing/duplicate audit template marker %s", section)
		}
		var table strings.Builder
		for _, r := range rows {
			var links []string
			for _, id := range r.Evidence {
				e := evidence[id]
				if e.Suite == "harness" {
					links = append(links, fmt.Sprintf("[harness:%s](conformance-testing.md)", e.Scenario))
				} else {
					links = append(links, fmt.Sprintf("[%s:%s](../%s)", e.Suite, e.Test, p.Inventory.Tests[id].File))
				}
			}
			linked := strings.Join(links, "<br>")
			if linked == "" {
				linked = "—"
			}
			fmt.Fprintf(&table, "| %s | %s — %s | %s | %s | %s |\n", r.ID, r.Citation, escape(r.Statement), r.Assessment, escape(r.Notes), linked)
		}
		text = strings.Replace(text, marker, strings.TrimSuffix(table.String(), "\n"), 1)
	}
	if strings.Contains(text, "{{requirements:") {
		return nil, errors.New("unresolved requirement section in audit template")
	}
	if len(p.Catalog.Obligations) > 0 {
		if strings.Count(text, "{{obligations}}") != 1 {
			return nil, errors.New("atomic catalog requires exactly one obligations template marker")
		}
		var table strings.Builder
		fmt.Fprintf(&table, "Individually classified obligations/dispositions: **%d**. These do not replace the %d historical groups or imply a conformance percentage.\n\n", len(p.Catalog.Obligations), len(p.Catalog.Requirements))
		table.WriteString("| ID / groups | Source | Actor / condition | Level / basis | Scope / assessment / verification | Remaining / priority | Obligation and review | Declared execution links (not results) |\n| --- | --- | --- | --- | --- | --- | --- | --- |\n")
		for _, o := range p.Catalog.Obligations {
			var links []string
			for _, id := range o.Evidence {
				e := evidence[id]
				if e.Suite == "harness" {
					links = append(links, fmt.Sprintf("[harness:%s](conformance-testing.md)", e.Scenario))
				} else {
					links = append(links, fmt.Sprintf("[%s:%s](../%s)", e.Suite, e.Test, p.Inventory.Tests[id].File))
				}
			}
			fmt.Fprintf(&table, "| %s / %s | %s | %s / %s | %s / %s | %s / %s / %s | %s / %d | %s — %s | %s |\n", o.ID, strings.Join(o.Groups, ", "), o.Citation, o.Actor, escape(o.Condition), o.Level, o.Basis, o.Scope, o.Assessment, o.Verification, o.Remaining, o.Priority, escape(o.Statement), escape(o.Notes), strings.Join(links, "<br>"))
		}
		table.WriteString("\n### Atomic group review decisions\n\n")
		for _, review := range p.Catalog.NormativeReviews {
			fmt.Fprintf(&table, "- `%s`: **%s**. %s\n", review.Group, review.Status, review.Notes)
		}
		text = strings.Replace(text, "{{obligations}}", table.String(), 1)
	} else {
		text = strings.ReplaceAll(text, "{{obligations}}", "No atomic review entries yet.")
	}
	var assumptions strings.Builder
	declared := make([]WorkingAssumptionReport, 0, len(p.Catalog.WorkingAssumptions))
	for _, a := range p.Catalog.WorkingAssumptions {
		row := WorkingAssumptionReport{WorkingAssumption: a, EvidenceStatus: "declared, not executed here"}
		for _, id := range a.Evidence {
			row.Evidence = append(row.Evidence, EvidenceReport{ID: id, Status: "declared"})
		}
		declared = append(declared, row)
	}
	writeWorkingAssumptions(&assumptions, declared)
	writeTemplateInventory(&assumptions, p.TemplateInventory)
	text += assumptions.String()
	return []byte(text), nil
}

func (p *Project) CheckGenerated() error {
	want, err := p.RenderAudit()
	if err != nil {
		return err
	}
	got, err := os.ReadFile(filepath.Join(p.Root, AuditPath))
	if err != nil {
		return err
	}
	if !bytes.Equal(bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n")), want) {
		return errors.New("generated rc1-audit.md is out of date: run mise run format:audit")
	}
	return nil
}

func WriteReport(directory string, report Report) error {
	if err := writeJSON(filepath.Join(directory, "report.json"), report); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(directory, "report.md"), []byte(report.Markdown()), 0600); err != nil {
		return err
	}
	summary, _, _ := strings.Cut(report.Markdown(), "\n## Evidence details and remaining work")
	if err := os.WriteFile(filepath.Join(directory, "summary.md"), []byte(summary+"\n\nFull evidence details and open review items are in report.md / report.json.\n"), 0600); err != nil {
		return err
	}
	return nil
}
