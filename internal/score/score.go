// Package score combines a PR's per-file changes with CODEOWNERS rules to
// produce per-reviewer CRU scores.
package score

import (
	"strings"

	"github.com/hmarr/codeowners"

	"github.com/laserlemon/cru"
	ghc "github.com/laserlemon/gh-cru/internal/gh"
)

// UnownedOwnerLabel is the synthetic "owner" used to attribute review
// cost to LOC that no CODEOWNERS rule matches. Rendered with a `~` gutter
// marker in the Human output, and surfaced in JSON as an owner row with
// `name: null` and `type: "unowned"`.
const UnownedOwnerLabel = "unowned"

// PRScore is the full scoring result for one PR.
//
// Four distinct CRU numbers are computed; each answers a different question:
//
//   - CRU       - owner-agnostic; Size × Risk. The PR's intrinsic
//                 review weight, as if a single reviewer owned every line.
//                 Default scalar score: "how big a review is this?".
//   - AuthorCRU - sum across all CODEOWNERS shares PLUS the synthetic
//                 "unowned" share. Total review burden the PR places on
//                 the team. Always ≥ CRU() since unowned LOC is attributed
//                 to a synthetic "unowned" owner rather than being free.
//   - MyCRU     - what THIS PR actually costs the current user to review,
//                 based on their @login + team memberships matching the
//                 CODEOWNERS rules. Each owned file counted once even if
//                 the user matches multiple owners (self + team).
//   - Per-owner - the value in each Ownership.Score. Individual reviewer's
//                 actual scored work for their ownership share (does not
//                 deduplicate across multiple of the user's identities).
type PRScore struct {
	PR             ghc.PR
	LOC            int
	Size           cru.Size // factor (float64 value) and bucket label (String()) in one
	Risk           cru.Risk // tier (low / medium / high), sealed in the cru package
	HasCodeowners  bool
	OwnerOrder     []string             // first-seen order while walking PR files
	OwnershipMap   map[string]Ownership // owner login → ownership info
	UnownedChanges int                  // LOC not covered by any CODEOWNERS rule

	// MyLogin is the authenticated user's GitHub @login (without the @).
	// Used as the label for the supplemental "user row" in the owners block
	// when they have any ownership in the PR (direct or via team).
	MyLogin string

	// TeamsResolved is true when the caller successfully enumerated the
	// authenticated user's team memberships. False means we fell back to
	// matching only the @login; team-based ownership won't be detected
	// (the Codespaces GITHUB_TOKEN, fine-grained PATs without read:org,
	// etc). The Human formatter uses this to render a dim footnote so
	// the user knows their own row may be missing.
	TeamsResolved bool

	// MyIdentities are the CODEOWNERS-compatible identities used to compute
	// MyCRU. Empty when no personal scoring was requested (e.g. --skip-ownership).
	MyIdentities []string
	MyOwnedLOC   int     // LOC of files owned by any of my identities (counted once)
	MyShare      float64 // ownership share for "me": MyOwnedLOC / total LOC, in [0, 1]
	MyCRU        float64 // Size × MyShare × Risk
}

// CRU returns the owner-agnostic CRU: Size × Risk. This is the
// "what size review is this?" score - independent of who owns what.
func (s PRScore) CRU() float64 {
	return float64(s.Size) * s.Risk.Multiplier()
}

// AuthorCRU returns the total review burden the PR places on the team:
// sum of per-owner scores INCLUDING the synthetic "unowned" owner that
// captures LOC matched by no CODEOWNERS rule. Always ≥ CRU(); equality
// when no owners overlap.
//
// For PRs in a repo with no CODEOWNERS file, the entire PR is attributed
// to the synthetic "unowned" owner, so AuthorCRU() == CRU(). For PRs in
// a CODEOWNERS repo with full coverage and no overlap, also equals
// CRU(). Overlap (one line owned by multiple teams) is the only case
// that drives AuthorCRU > CRU.
func (s PRScore) AuthorCRU() float64 {
	total := 0.0
	for _, o := range s.OwnershipMap {
		total += o.Score
	}
	return total
}

// Ownership is one owner's stake in a PR.
//
//	Score = Size × Share × Risk
//
// where Share is this owner's ownership share (OwnedLOC / total LOC, in
// [0, 1]). The synthetic "unowned" owner uses the same shape with
// Share = unowned_LOC / total LOC.
type Ownership struct {
	Owner    string  // login or @org/team, or UnownedOwnerLabel for the synthetic unowned row
	OwnedLOC int     // LOC of files this owner is responsible for
	Share    float64 // ownership share: OwnedLOC / total LOC, in [0, 1]
	Score    float64 // Size × Share × Risk
}

// Compute builds a PRScore from a fetched PR, its file diffs, and the
// (optional) CODEOWNERS ruleset. Pass nil ruleset for repos without
// CODEOWNERS - the result will treat the whole PR as unowned.
//
// highRiskLabels and mediumRiskLabels are the sets of label names that
// trigger the high-risk (4×) and medium-risk (2×) multipliers
// respectively. Any one matching the PR's labels (case-insensitive)
// activates that tier. High wins over medium when both match. Pass nil
// or empty to disable a tier.
//
// myLogin is the authenticated user's GitHub @login (no leading @); empty
// when running without personal scoring. myIdentities, when non-empty, are
// the CODEOWNERS-compatible identity strings (e.g. ["@laserlemon",
// "@github/justice-league"]) used to compute MyCRU. Pass nil to skip
// personal scoring.
func Compute(pr ghc.PR, files []ghc.File, owners codeowners.Ruleset, highRiskLabels []string, mediumRiskLabels []string, myLogin string, myIdentities []string) PRScore {
	risk := cru.RiskLow
	switch {
	case hasAnyLabel(pr.Labels, highRiskLabels):
		risk = cru.RiskHigh
	case hasAnyLabel(pr.Labels, mediumRiskLabels):
		risk = cru.RiskMedium
	}

	loc := pr.LOC()
	size := cru.CalculateSize(loc)
	sf := float64(size)

	result := PRScore{
		PR:            pr,
		LOC:           loc,
		Size:          size,
		Risk:          risk,
		HasCodeowners: owners != nil,
		OwnershipMap:  make(map[string]Ownership),
		MyLogin:       myLogin,
		MyIdentities:  myIdentities,
	}

	if owners == nil {
		// No CODEOWNERS file in the repo: the entire PR is unowned.
		// Populate the synthetic unowned owner so the score and the
		// owners table are uniform with the CODEOWNERS-but-no-rule-match
		// case. AuthorCRU sums OwnershipMap; with one unowned entry of
		// share=1.0, it equals CRU() (size × risk).
		//
		// Safe to use cru.Calculate here because totalLOC == ownedLOC,
		// so the share is 1.0 and the share-denominator subtlety in the
		// CODEOWNERS branch below (denom = sum(file.Changes) which may
		// differ from pr.LOC when renames inflate the count) doesn't
		// apply.
		result.UnownedChanges = loc
		denom := loc
		if denom == 0 {
			denom = 1
		}
		result.OwnershipMap[UnownedOwnerLabel] = Ownership{
			Owner:    UnownedOwnerLabel,
			OwnedLOC: loc,
			Share:    float64(loc) / float64(denom),
			Score:    cru.Calculate(loc, loc, risk),
		}
		result.OwnerOrder = []string{UnownedOwnerLabel}
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
			Score:    sf * share * risk.Multiplier(),
		}
	}

	// Synthetic "unowned" owner: attribute the LOC that no CODEOWNERS rule
	// matched. Without this, AuthorCRU under-counts review reality (those
	// lines still need a reviewer, just not a CODEOWNERS-named one). With
	// it, AuthorCRU is always ≥ CRU(), with equality when ownership shares
	// (real + unowned) sum to exactly 1.0.
	//
	// Skipped when there is no unowned LOC. Also rendered with a `~`
	// gutter marker in the Human formatter and as an owner row with
	// `name: null` and `type: "unowned"` in JSON.
	if result.UnownedChanges > 0 {
		share := float64(result.UnownedChanges) / float64(denom)
		result.OwnershipMap[UnownedOwnerLabel] = Ownership{
			Owner:    UnownedOwnerLabel,
			OwnedLOC: result.UnownedChanges,
			Share:    share,
			Score:    sf * share * risk.Multiplier(),
		}
		// Append to owner order so the unowned row renders LAST in the
		// table (after all real owners).
		result.OwnerOrder = append(result.OwnerOrder, UnownedOwnerLabel)
	}

	if len(myIdentities) > 0 {
		result.MyOwnedLOC = myOwnedLOC
		result.MyShare = float64(myOwnedLOC) / float64(denom)
		result.MyCRU = sf * result.MyShare * risk.Multiplier()
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
// this is Size × Risk (treating the synthetic unowned owner as 100%).
//
// Deprecated: prefer AuthorCRU() which says the same thing more precisely.
func (s PRScore) TotalScore() float64 {
	return s.AuthorCRU()
}

// hasAnyLabel returns true when any target appears in labels
// (case-insensitive). Empty targets returns false.
func hasAnyLabel(labels []string, targets []string) bool {
	if len(targets) == 0 {
		return false
	}
	for _, t := range targets {
		if t == "" {
			continue
		}
		for _, l := range labels {
			if strings.EqualFold(l, t) {
				return true
			}
		}
	}
	return false
}
