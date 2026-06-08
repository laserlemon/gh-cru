// Package format renders PRScore results in three modes, modeled on the
// gh CLI's own output structure (see cli/cli/pkg/cmd/pr/view/view.go):
//
//   - Human: TTY-friendly, indented sections, ANSI-free.
//   - Script: tab-separated key:\tvalue rows for grep/awk pipelines.
//   - JSON:   structured, pipe-friendly for jq.
//
// The caller picks the mode based on TTY detection and the --json flag.
package format

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/laserlemon/gh-cru/internal/score"
)

// Human writes a TTY-friendly summary for one PR.
//
// Layout:
//
//	<repo>#<number>
//	  LOC:            <n>
//	  Size label:     <bucket>
//	  Size factor:    <f>
//	  Risk label:     <label>
//	  Risk factor:    <r>
//	  Normal CRU:     <c>
//	  Total CRU:      <sum across owners; team review burden>
//	  Owners:
//	  * <login>                  (user row, only if user matched any owner)
//	      Owned LOC:         <n>
//	      Ownership factor:  <s>
//	      Requested CRU:     <c>
//	  * <owner1>                 (* marks owners the user is part of)
//	      Owned LOC:         <n>
//	      Ownership factor:  <s>
//	      Requested CRU:     <c>
//	    <owner2>
//	      ...
//
// PR title/state/author/diffstat are intentionally omitted: the diff size
// is already encoded in Size factor, and `gh pr view` covers the rest.
// Use --json for the full metadata payload.
//
// Math contract: per-owner CRU values are summed unrounded for Total CRU,
// then rounded for display. The user row is supplemental and is NOT
// included in Total CRU — Total CRU is computed independent of who's
// asking.
func Human(w io.Writer, repo string, s score.PRScore) {
	fmt.Fprintf(w, "%s#%d\n", repo, s.PR.Number)
	fmt.Fprintf(w, "  LOC:            %d\n", s.LOC)
	fmt.Fprintf(w, "  Size label:     %s\n", s.Bucket)
	fmt.Fprintf(w, "  Size factor:    %.3f\n", s.SizeFactor)
	fmt.Fprintf(w, "  Risk label:     %s\n", riskLabel(s.Risk))
	fmt.Fprintf(w, "  Risk factor:    %.3f\n", s.Risk)
	fmt.Fprintf(w, "  Normal CRU:     %.3f\n", s.CRU())

	if !s.HasCodeowners {
		fmt.Fprintf(w, "  Total CRU:      %.3f\n", s.CRU())
		fmt.Fprintln(w, "  Owners:         no CODEOWNERS file in repo")
		return
	}

	fmt.Fprintf(w, "  Total CRU:      %.3f\n", s.AuthorCRU())
	fmt.Fprintln(w, "  Owners:")

	mySet := makeIdentitySet(s.MyIdentities)
	owners := s.SortedOwners()

	// Supplemental user row first (only when the user actually owns LOC,
	// directly or via team). Label is the @login, not any team name —
	// this is "what does YOUR review queue look like."
	if s.MyLogin != "" && s.MyOwnedLOC > 0 {
		writeOwnerBlock(w, "* ", s.MyLogin, s.MyOwnedLOC, s.MyShare, s.MyCRU)
	}

	for _, o := range owners {
		mark := "  "
		if mySet[strings.ToLower(o.Owner)] {
			mark = "* "
		}
		writeOwnerBlock(w, mark, displayOwner(o.Owner), o.OwnedLOC, o.Share, o.Score)
	}

	if s.UnownedChanges > 0 {
		writeUnownedBlock(w, s.UnownedChanges, s.LOC)
	}
}

// writeOwnerBlock writes one owner's four-line block with the leading
// mark (`* ` or `  `) on the heading line. Values are rounded only for
// display; callers pass the full-precision floats.
func writeOwnerBlock(w io.Writer, mark, label string, ownedLOC int, share, requestedCRU float64) {
	fmt.Fprintf(w, "  %s%s\n", mark, label)
	fmt.Fprintf(w, "      Owned LOC:         %d\n", ownedLOC)
	fmt.Fprintf(w, "      Ownership factor:  %.3f\n", share)
	fmt.Fprintf(w, "      Requested CRU:     %.3f\n", requestedCRU)
}

func writeUnownedBlock(w io.Writer, unownedLOC, totalLOC int) {
	share := 0.0
	if totalLOC > 0 {
		share = float64(unownedLOC) / float64(totalLOC)
	}
	fmt.Fprintf(w, "    (unowned)\n")
	fmt.Fprintf(w, "      Owned LOC:         %d\n", unownedLOC)
	fmt.Fprintf(w, "      Ownership factor:  %.3f\n", share)
	fmt.Fprintf(w, "      Requested CRU:     (not attributed)\n")
}

func makeIdentitySet(ids []string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[strings.ToLower(id)] = true
	}
	return m
}

// displayOwner strips the leading "@" from CODEOWNERS strings for display
// (CODEOWNERS always starts owner strings with "@"; the symbol carries no
// info). The data layer keeps the canonical form.
func displayOwner(s string) string {
	return strings.TrimPrefix(s, "@")
}

// Script writes tab-separated key:\tvalue rows, gh-style. Designed for
// pipelines: `gh cru 123 | grep ^normal_cru:`.
//
// Multi-PR runs emit a blank line between PRs.
func Script(w io.Writer, repo string, s score.PRScore) {
	pr := s.PR
	rows := [][2]string{
		{"repo", repo},
		{"number", fmt.Sprintf("%d", pr.Number)},
		{"title", pr.Title},
		{"author", pr.Author},
		{"state", pr.State},
		{"additions", fmt.Sprintf("%d", pr.Additions)},
		{"deletions", fmt.Sprintf("%d", pr.Deletions)},
		{"loc", fmt.Sprintf("%d", s.LOC)},
		{"files", fmt.Sprintf("%d", pr.Files)},
		{"size_label", string(s.Bucket)},
		{"size_factor", fmt.Sprintf("%.6f", s.SizeFactor)},
		{"risk_label", riskLabel(s.Risk)},
		{"risk_factor", fmt.Sprintf("%.6f", s.Risk)},
		{"normal_cru", fmt.Sprintf("%.6f", s.CRU())},
		{"total_cru", fmt.Sprintf("%.6f", s.AuthorCRU())},
		{"my_login", s.MyLogin},
		{"my_owned_loc", fmt.Sprintf("%d", s.MyOwnedLOC)},
		{"my_ownership_factor", fmt.Sprintf("%.6f", s.MyShare)},
		{"my_requested_cru", fmt.Sprintf("%.6f", s.MyCRU)},
		{"has_codeowners", fmt.Sprintf("%t", s.HasCodeowners)},
	}
	for _, kv := range rows {
		fmt.Fprintf(w, "%s:\t%s\n", kv[0], kv[1])
	}
	if s.HasCodeowners {
		mySet := makeIdentitySet(s.MyIdentities)
		for _, o := range s.SortedOwners() {
			isMine := mySet[strings.ToLower(o.Owner)]
			fmt.Fprintf(w, "owner:\t%s\t%d\t%.6f\t%.6f\t%t\n",
				o.Owner, o.OwnedLOC, o.Share, o.Score, isMine)
		}
	}
}

// JSON writes one indented JSON object per call (NDJSON style for batch
// runs is opt-in via a `--compact` flag in the future).
func JSON(w io.Writer, repo string, s score.PRScore) error {
	type ownerJSON struct {
		Owner            string  `json:"owner"`
		OwnedLOC         int     `json:"owned_loc"`
		OwnershipFactor  float64 `json:"ownership_factor"`
		RequestedCRU     float64 `json:"requested_cru"`
		IsYou            bool    `json:"is_you"`
	}
	type youJSON struct {
		Login            string  `json:"login"`
		OwnedLOC         int     `json:"owned_loc"`
		OwnershipFactor  float64 `json:"ownership_factor"`
		RequestedCRU     float64 `json:"requested_cru"`
	}
	type out struct {
		Repo          string      `json:"repo"`
		Number        int         `json:"number"`
		Title         string      `json:"title"`
		Author        string      `json:"author"`
		State         string      `json:"state"`
		Additions     int         `json:"additions"`
		Deletions     int         `json:"deletions"`
		LOC           int         `json:"loc"`
		Files         int         `json:"files"`
		SizeLabel     string      `json:"size_label"`
		SizeFactor    float64     `json:"size_factor"`
		RiskLabel     string      `json:"risk_label"`
		RiskFactor    float64     `json:"risk_factor"`
		NormalCRU     float64     `json:"normal_cru"`     // size × risk
		TotalCRU      float64     `json:"total_cru"`      // Σ per-owner; review burden
		You           *youJSON    `json:"you,omitempty"`  // supplemental user row
		MyIdentities  []string    `json:"my_identities,omitempty"`
		HasCodeowners bool        `json:"has_codeowners"`
		Owners        []ownerJSON `json:"owners"`
		UnownedLOC    int         `json:"unowned_loc"`
	}
	owners := make([]ownerJSON, 0)
	mySet := makeIdentitySet(s.MyIdentities)
	for _, o := range s.SortedOwners() {
		owners = append(owners, ownerJSON{
			Owner:           o.Owner,
			OwnedLOC:        o.OwnedLOC,
			OwnershipFactor: o.Share,
			RequestedCRU:    o.Score,
			IsYou:           mySet[strings.ToLower(o.Owner)],
		})
	}
	var you *youJSON
	if s.MyLogin != "" && s.MyOwnedLOC > 0 {
		you = &youJSON{
			Login:           s.MyLogin,
			OwnedLOC:        s.MyOwnedLOC,
			OwnershipFactor: s.MyShare,
			RequestedCRU:    s.MyCRU,
		}
	}
	o := out{
		Repo:          repo,
		Number:        s.PR.Number,
		Title:         s.PR.Title,
		Author:        s.PR.Author,
		State:         s.PR.State,
		Additions:     s.PR.Additions,
		Deletions:     s.PR.Deletions,
		LOC:           s.LOC,
		Files:         s.PR.Files,
		SizeLabel:     string(s.Bucket),
		SizeFactor:    s.SizeFactor,
		RiskLabel:     riskLabel(s.Risk),
		RiskFactor:    s.Risk,
		NormalCRU:     s.CRU(),
		TotalCRU:      s.AuthorCRU(),
		You:           you,
		MyIdentities:  s.MyIdentities,
		HasCodeowners: s.HasCodeowners,
		Owners:        owners,
		UnownedLOC:    s.UnownedChanges,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(o)
}

func riskLabel(r float64) string {
	if r > 1.0 {
		return "high"
	}
	return "low"
}
