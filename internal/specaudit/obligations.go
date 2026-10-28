package specaudit

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Obligation refines, but never overwrites, the frozen grouped audit. Each row
// has its own assessment and test links: nothing is inherited from its parents.
type Obligation struct {
	ID             string      `json:"id"`
	Groups         []string    `json:"groups"`
	Statement      string      `json:"statement"`
	Level          string      `json:"level"`
	Actor          string      `json:"actor"`
	Condition      string      `json:"condition"`
	Scope          string      `json:"scope"`
	EnabledBy      string      `json:"enabled_by,omitempty"`
	Basis          string      `json:"basis"`
	Assessment     string      `json:"assessment"`
	Verification   string      `json:"verification"`
	Remaining      string      `json:"remaining"`
	Priority       int         `json:"priority"`
	Citation       string      `json:"citation"`
	Sources        []Reference `json:"sources"`
	Implementation []string    `json:"implementation"`
	Evidence       []string    `json:"evidence"`
	Notes          string      `json:"notes"`
}

type NormativeReview struct {
	Group  string `json:"group"`
	Status string `json:"status"`
	Notes  string `json:"notes"`
}

var obligationID = regexp.MustCompile(`^((?:M|CN|P|CT|MD|CL|AW|VZ|R)[0-9]{2,})\.[0-9]{2,}$`)

func (p *Project) validateObligations(groups map[string]bool, evidence map[string]Evidence, sources map[string]SpecSource, used map[string]bool) error {
	reviews := map[string]NormativeReview{}
	for _, review := range p.Catalog.NormativeReviews {
		if !groups[review.Group] || reviews[review.Group].Group != "" || !slices.Contains([]string{"partial", "complete"}, review.Status) || strings.TrimSpace(review.Notes) == "" {
			return fmt.Errorf("invalid/duplicate normative review %s", review.Group)
		}
		reviews[review.Group] = review
	}
	parents := map[string]Requirement{}
	for _, group := range p.Catalog.Requirements {
		parents[group.ID] = group
	}
	ids := map[string]bool{}
	byID := map[string]Obligation{}
	for _, o := range p.Catalog.Obligations {
		byID[o.ID] = o
	}
	counts := map[string]int{}
	for _, o := range p.Catalog.Obligations {
		match := obligationID.FindStringSubmatch(o.ID)
		if match == nil || ids[o.ID] || len(o.Groups) == 0 || o.Groups[0] != match[1] || strings.TrimSpace(o.Statement) == "" || strings.TrimSpace(o.Condition) == "" || strings.TrimSpace(o.Notes) == "" {
			return fmt.Errorf("invalid/duplicate atomic obligation %s", o.ID)
		}
		ids[o.ID] = true
		if !slices.Contains([]string{"must", "must-not", "should", "should-not", "may", "informative", "unclassified"}, o.Level) || !slices.Contains([]string{"client", "server", "sdk-contract"}, o.Actor) || !slices.Contains([]string{"applicable", "excluded", "undetermined"}, o.Scope) || !slices.Contains([]string{"explicit", "declarative", "example"}, o.Basis) {
			return fmt.Errorf("%s: invalid atomic classification", o.ID)
		}
		if !slices.Contains([]string{"T", "C", "P", "O", "S", "D"}, o.Assessment) || !slices.Contains([]string{"software", "hardware", "review", "external-server"}, o.Verification) || !slices.Contains([]string{"none", "implementation", "software-evidence", "measurement", "platform-network", "review", "external", "optional"}, o.Remaining) {
			return fmt.Errorf("%s: invalid atomic assessment/remaining work", o.ID)
		}
		if o.Scope == "applicable" {
			if o.Actor == "server" || o.Assessment == "S" || o.Assessment == "D" || o.Remaining == "external" || o.Remaining == "optional" {
				return fmt.Errorf("%s: applicable obligation hidden as server/optional", o.ID)
			}
			if o.Assessment == "T" && (len(o.Evidence) == 0 || o.Verification != "software" || o.Remaining != "none") {
				return fmt.Errorf("%s: targeted software evidence cannot certify hardware or remaining work", o.ID)
			}
		} else if o.Scope == "excluded" {
			if o.Actor == "server" {
				if o.Assessment != "S" || o.Verification != "external-server" || o.Remaining != "external" {
					return fmt.Errorf("%s: server exclusion needs an explicit external disposition", o.ID)
				}
			} else {
				optional := slices.Contains([]string{"may", "informative"}, o.Level)
				if o.EnabledBy != "" {
					parent, ok := byID[o.EnabledBy]
					shared := false
					for _, g := range parent.Groups {
						if slices.Contains(o.Groups, g) {
							shared = true
						}
					}
					permissionCited := false
					for _, ref := range o.Sources {
						for _, permission := range parent.Sources {
							if ref.Source == permission.Source && ref.Start <= permission.End && ref.End >= permission.Start {
								permissionCited = true
							}
						}
					}
					optional = ok && parent.ID != o.ID && parent.Level == "may" && parent.Actor == "client" && parent.Scope == "excluded" && parent.EnabledBy == "" && shared && permissionCited
				}
				if !optional || o.Assessment != "D" || o.Remaining != "optional" {
					return fmt.Errorf("%s: mandatory/recommended client duty needs an explicit unselected MAY parent, not a blanket exclusion", o.ID)
				}
			}
		} else if o.Assessment != "O" || o.Remaining != "review" || o.Verification != "review" {
			return fmt.Errorf("%s: unresolved applicability must remain open review", o.ID)
		}
		if o.EnabledBy != "" && (o.Scope != "excluded" || o.Actor != "client" || o.Level == "may") {
			return fmt.Errorf("%s: invalid conditional optional branch", o.ID)
		}
		if o.Level == "unclassified" && o.Scope != "undetermined" {
			return fmt.Errorf("%s: unclassified obligation must have undetermined scope", o.ID)
		}
		if o.Basis == "example" && o.Level != "informative" {
			return fmt.Errorf("%s: suggested strategy cannot manufacture a normative duty", o.ID)
		}
		if o.Remaining == "none" || o.Scope == "excluded" {
			if o.Priority != 0 {
				return fmt.Errorf("%s: closed/external/optional item cannot be prioritized SDK work", o.ID)
			}
		} else if o.Priority < 1 || o.Priority > 3 {
			return fmt.Errorf("%s: open applicable work needs priority 1..3", o.ID)
		}
		if o.Scope == "applicable" && (o.Assessment == "P" || o.Assessment == "O") && o.Remaining == "none" {
			return fmt.Errorf("%s: partial/open assessment needs remaining work", o.ID)
		}
		seenGroups := map[string]bool{}
		for _, group := range o.Groups {
			if !groups[group] || seenGroups[group] || reviews[group].Group == "" {
				return fmt.Errorf("%s: unknown/duplicate/unreviewed parent %s", o.ID, group)
			}
			seenGroups[group] = true
			counts[group]++
			overlap := false
			for _, ref := range o.Sources {
				for _, parent := range parents[group].Sources {
					if ref.Source == parent.Source && ref.Start <= parent.End && ref.End >= parent.Start {
						overlap = true
					}
				}
			}
			if !overlap {
				return fmt.Errorf("%s: unrelated parent group %s", o.ID, group)
			}
			if reviews[group].Status == "complete" && (o.Level == "unclassified" || o.Scope == "undetermined") {
				return fmt.Errorf("%s: complete group review retains unresolved atomic classification", group)
			}
		}
		if len(o.Sources) == 0 || !slices.Equal(parseCitation(o.Citation), o.Sources) {
			return fmt.Errorf("%s: missing/inconsistent atomic source citation", o.ID)
		}
		for _, ref := range o.Sources {
			source, ok := sources[ref.Source]
			if !ok || ref.Start < 1 || ref.End < ref.Start || ref.End > source.Lines {
				return fmt.Errorf("%s: invalid atomic source range", o.ID)
			}
			for line := ref.Start; line <= ref.End; line++ {
				covered := false
				for _, group := range o.Groups {
					for _, parent := range parents[group].Sources {
						if parent.Source == ref.Source && parent.Start <= line && parent.End >= line {
							covered = true
						}
					}
				}
				if !covered {
					return fmt.Errorf("%s: source line outside parent references", o.ID)
				}
			}
		}
		if o.Scope == "applicable" && len(o.Implementation) == 0 {
			return fmt.Errorf("%s: missing independent implementation links", o.ID)
		}
		for _, path := range o.Implementation {
			if !portablePath(path) || !p.Inventory.Files[path] || !strings.HasSuffix(path, ".go") {
				return fmt.Errorf("%s: unsafe/missing atomic implementation", o.ID)
			}
			info, err := os.Lstat(filepath.Join(p.Root, filepath.FromSlash(path)))
			if err != nil || !info.Mode().IsRegular() {
				return fmt.Errorf("%s: non-regular atomic implementation", o.ID)
			}
		}
		seenEvidence := map[string]bool{}
		for _, id := range o.Evidence {
			if evidence[id].ID == "" || seenEvidence[id] {
				return fmt.Errorf("%s: unknown/duplicate atomic evidence %s", o.ID, id)
			}
			seenEvidence[id] = true
			used[id] = true
		}
	}
	for group := range reviews {
		if counts[group] == 0 {
			return fmt.Errorf("%s: normative review has no atomic obligations/dispositions", group)
		}
	}
	baseline := map[string]bool{}
	for _, id := range p.Index.RequiredObligationIDs {
		if !obligationID.MatchString(id) || baseline[id] || !ids[id] {
			return fmt.Errorf("missing/invalid/duplicate baseline obligation %s", id)
		}
		baseline[id] = true
	}
	for id := range ids {
		if !baseline[id] {
			return fmt.Errorf("unfrozen atomic obligation %s: review/update source index baseline", id)
		}
	}
	if p.Catalog.InventoryReview == "complete" {
		for id := range groups {
			if reviews[id].Status != "complete" {
				return fmt.Errorf("%s: complete inventory requires a complete atomic group review", id)
			}
		}
	}
	return nil
}
