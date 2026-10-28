package specaudit

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// WorkingAssumption records a provisional implementation policy, not a normative
// disposition. Its tests cannot supply evidence or change assessment for an atom.
type WorkingAssumption struct {
	ID          string   `json:"id"`
	Status      string   `json:"status"`
	Obligations []string `json:"obligations"`
	Decision    string   `json:"decision"`
	Reason      string   `json:"reason"`
	Revisit     string   `json:"revisit"`
	References  []string `json:"references"`
	Evidence    []string `json:"evidence"`
}

var assumptionID = regexp.MustCompile(`^WA[0-9]{2,}$`)
var comparisonURL = regexp.MustCompile(`^https://github\.com/Sendspin/sendspin-cpp/blob/[0-9a-f]{40}/([A-Za-z0-9_./-]+)#L([0-9]+)-L([0-9]+)$`)

func (p *Project) validateWorkingAssumptions(evidence map[string]Evidence, used map[string]bool) error {
	atoms := map[string]bool{}
	for _, o := range p.Catalog.Obligations {
		atoms[o.ID] = true
	}
	ids := map[string]bool{}
	for _, a := range p.Catalog.WorkingAssumptions {
		if !assumptionID.MatchString(a.ID) || ids[a.ID] || a.Status != "provisional" || strings.TrimSpace(a.Decision) == "" || strings.TrimSpace(a.Reason) == "" || strings.TrimSpace(a.Revisit) == "" || len(a.Obligations) == 0 || len(a.References) == 0 {
			return fmt.Errorf("invalid/duplicate working assumption %s", a.ID)
		}
		ids[a.ID] = true
		seen := map[string]bool{}
		for _, id := range a.Obligations {
			if !atoms[id] || seen[id] {
				return fmt.Errorf("%s: unknown/duplicate assumption obligation %s", a.ID, id)
			}
			seen[id] = true
		}
		seen = map[string]bool{}
		for _, ref := range a.References {
			m := comparisonURL.FindStringSubmatch(ref)
			if m == nil || !portablePath(m[1]) || seen[ref] {
				return fmt.Errorf("%s: comparison requires a pinned file/line URL", a.ID)
			}
			first, errFirst := strconv.Atoi(m[2])
			last, errLast := strconv.Atoi(m[3])
			if errFirst != nil || errLast != nil || first < 1 || last < first {
				return fmt.Errorf("%s: invalid comparison line range", a.ID)
			}
			seen[ref] = true
		}
		seen = map[string]bool{}
		for _, id := range a.Evidence {
			if evidence[id].ID == "" || seen[id] {
				return fmt.Errorf("%s: unknown/duplicate assumption evidence %s", a.ID, id)
			}
			seen[id], used[id] = true, true
		}
	}
	return nil
}
