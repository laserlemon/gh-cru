// Package score combines a PR's per-file changes with CODEOWNERS rules to
// produce per-reviewer CRU scores.
package score

import (
	"strings"

	"github.com/hmarr/codeowners"

	"github.com/laserlemon/cru"
	ghc "github.com/laserlemon/gh-cru/internal/gh"
)

// PRScore is the full scoring result for one PR.
//
// Four distinct CRU numbers are computed; each answers a different question:
//
//   - CRU       — owner-agnostic; size_factor × risk. The PR's intrinsic
//                 review weight, as if a single reviewer owned every line.
//                 Default scalar score: "how big a review is this?".
//   - AuthorCRU — sum across all CODEOWNERS shares. Total review burden the
//                 PR places on the team. Useful for the author and for
//                 aggregate workload dashboards.
//   - MyCRU     — what THIS PR actually costs the current user to review,
//                 based on their @login + team memberships matching the
//                 CODEOWNERS rules. Each owned file counted once even if
//                 the user matches multiple owners (self + team).
//   - Per-owner — the value in each Ownership.Score. Individual reviewer's
//                 actual scored work for their slice (does not deduplicate
//                 across multiple of the user's identities).
type PRScore struct {
	PR             ghc.PR
	LOC            int
	SizeFactor     float64
	Bucket         cru.Size
	Risk           float64
	HasCodeowners  bool
	OwnerOrder     []string             // first-seen order while walking PR files
	OwnershipMap   map[string]Ownership // owner login → ownership info
	UnownedChanges int                  // LOC not covered by any CODEOWNERS rule

	// MyLogin is the authenticated user's GitHub @login (without the @).
	// Used as the label for the supplemental "user row" in the owners block
	// when they have any ownership in the PR (direct or via team).
	MyLogin string

	// MyIdentities are the CODEOWNERS-compatible identities used to compute
	// MyCRU. Empty when no personal scoring was requested (e.g. --skip-ownership).
	MyIdentities []string
	MyOwnedLOC   int     // LOC of files owned by any of my identities (counted once)
	MyShare      float64 // MyOwnedLOC / total LOC, in [0, 1]
	MyCRU        float64 // size_factor × MyShare × risk
}

// CRU returns the owner-agnostic CRU: size_factor × risk. This is the
// "what size review is this?" score — independent of who owns what.
func (s PRScore) CRU() float64 {
	return s.SizeFactor * s.Risk
}

// AuthorCRU returns the total review burden the PR places on the team:
// sum of per-owner scores. For PRs without CODEOWNERS, this equals CRU().
func (s PRScore) AuthorCRU() float64 {
	if !s.HasCodeowners {
		return s.CRU()
	}
	total := 0.0
	for _, o := range s.OwnershipMap {
		total += o.Score
	}
	return total
}

// Ownership is one owner's stake in a PR.
type Ownership struct {
	Owner       string  // login or @org/team
	OwnedLOC    int     // LOC of files this owner is responsible for
	Share       float64 // OwnedLOC / total LOC, in [0, 1]
	Score       float64 // size_factor × Share × risk
}

// Compute builds a PRScore from a fetched PR, its file diffs, and the
// (optional) CODEOWNERS ruleset. Pass nil ruleset for repos without
// CODEOWNERS — the result will treat the whole PR as unowned.
//
// myLogin is the authenticated user's GitHub @login (no leading @); empty
// when running without personal scoring. myIdentities, when non-empty, are
// the CODEOWNERS-compatible identity strings (e.g. ["@laserlemon",
// "@github/justice-league"]) used to compute MyCRU. Pass nil to skip
// personal scoring.
func Compute(pr ghc.PR, files []ghc.File, owners codeowners.Ruleset, riskLabel string, myLogin string, myIdentities []string) PRScore {
	risk := cru.RiskLow
	if hasLabel(pr.Labels, riskLabel) {
		risk = cru.RiskHigh
	}

	loc := pr.LOC()
	sf := cru.SizeFactor(loc)

	result := PRScore{
		PR:            pr,
		LOC:           loc,
		SizeFactor:    sf,
		Bucket:        cru.Bucket(loc),
		Risk:          risk,
		HasCodeowners: owners != nil,
		OwnershipMap:  make(map[string]Ownership),
		MyLogin:       myLogin,
		MyIdentities:  myIdentities,
	}

	if owners == nil {
		result.UnownedChanges = loc
		return result
	}

	// Build a lookup set for my identities (lowercased, since CODEOWNERS
	// matching is case-insensitive for user/team names).
	mySet := make(map[string]bool, len(myIdentities))
	for _, id := range myIdentities {
		mySet[strings.ToLower(id)] = true
	}

	ownedTotals := make(map[string]int)
	ownerOrder := make([]string, 0)
	seen := make(map[string]bool)
	myOwnedLOC := 0
	for _, f := range files {
		rule, err := owners.Match(f.Path)
		if err != nil || rule == nil {
			result.UnownedChanges += f.Changes
			continue
		}
		matchedMe := false
		for _, o := range rule.Owners {
			key := o.String()
			ownedTotals[key] += f.Changes
			if !seen[key] {
				seen[key] = true
				ownerOrder = append(ownerOrder, key)
			}
			if !matchedMe && mySet[strings.ToLower(key)] {
				matchedMe = true
			}
		}
		if matchedMe {
			myOwnedLOC += f.Changes
		}
	}
	result.OwnerOrder = ownerOrder

	// Use the file-changes sum as denominator (see prior note about
	// renames making pr.LOC ≠ sum(file.Changes)).
	denom := 0
	for _, f := range files {
		denom += f.Changes
	}
	if denom == 0 {
		denom = loc
		if denom == 0 {
			denom = 1
		}
	}

	for owner, ownedLOC := range ownedTotals {
		share := float64(ownedLOC) / float64(denom)
		result.OwnershipMap[owner] = Ownership{
			Owner:    owner,
			OwnedLOC: ownedLOC,
			Share:    share,
			Score:    sf * share * risk,
		}
	}

	if len(myIdentities) > 0 {
		result.MyOwnedLOC = myOwnedLOC
		result.MyShare = float64(myOwnedLOC) / float64(denom)
		result.MyCRU = sf * result.MyShare * risk
	}

	return result
}

// SortedOwners returns ownerships in the order CODEOWNERS owners were
// first encountered while walking the PR's changed files. This preserves
// any deliberate priority encoded by file ordering and stays stable
// across risk/score perturbations. Callers that want the current user
// hoisted to the top (e.g. the Human formatter) do that themselves.
func (s PRScore) SortedOwners() []Ownership {
	out := make([]Ownership, 0, len(s.OwnershipMap))
	for _, owner := range s.OwnerOrder {
		if o, ok := s.OwnershipMap[owner]; ok {
			out = append(out, o)
		}
	}
	return out
}

// TotalScore is the sum of all owner scores. For PRs with no CODEOWNERS,
// this is the size_factor × risk (treating the reviewer as 100% owner).
//
// Deprecated: prefer AuthorCRU() which says the same thing more precisely.
func (s PRScore) TotalScore() float64 {
	return s.AuthorCRU()
}

func hasLabel(labels []string, target string) bool {
	if target == "" {
		return false
	}
	for _, l := range labels {
		if strings.EqualFold(l, target) {
			return true
		}
	}
	return false
}
