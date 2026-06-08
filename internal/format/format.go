// Package format renders PRScore results in three modes, modeled on the
// gh CLI's own output structure (see cli/cli/pkg/cmd/pr/view/view.go):
//
//   - Human: TTY-friendly, two-line header, indented sections, ANSI-free.
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
// Layout matches the locked output spec:
//
//	<title> <repo>#<number>
//	<state> • by @author • +adds -dels in N file(s)
//	  Size factor:  <f>
//	  Risk factor:  <r>
//	  Normal CRU:   <c>
//	  Your CRU:     <c>   (only if MyIdentities was provided)
//	  CRU by owner:
//	      @owner1   X LOC  Y%  →  Z CRU
//	    * @owner2   X LOC  Y%  →  Z CRU
//	  Total CRU:    <sum>
//
// Owners on the user's team-list (or matching their login) get a leading
// `* ` marker in the git-branch style. Non-matching rows are prefixed
// with two spaces so columns align.
func Human(w io.Writer, repo string, s score.PRScore) {
	pr := s.PR
	fmt.Fprintf(w, "%s %s#%d\n", pr.Title, repo, pr.Number)
	fmt.Fprintf(w, "%s • by @%s • +%d -%d in %d file(s)\n\n",
		pr.State, pr.Author, pr.Additions, pr.Deletions, pr.Files)

	fmt.Fprintf(w, "  Size factor:  %.2f   (%d LOC, %s)\n", s.SizeFactor, s.LOC, s.Bucket)
	fmt.Fprintf(w, "  Risk factor:  %.1f   (%s)\n", s.Risk, riskTag(s.Risk))
	fmt.Fprintf(w, "  Normal CRU:   %.2f   (size × risk; PR-intrinsic weight)\n", s.CRU())

	if !s.HasCodeowners {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  Ownership:    no CODEOWNERS file in repo\n")
		return
	}

	if len(s.MyIdentities) > 0 {
		fmt.Fprintf(w, "  Your CRU:     %.2f   (%d/%d LOC = %.1f%% match your identities)\n",
			s.MyCRU, s.MyOwnedLOC, s.LOC, s.MyShare*100)
	}

	mySet := makeIdentitySet(s.MyIdentities)
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  CRU by owner:\n")
	for _, o := range s.SortedOwners() {
		mark := "  "
		if mySet[strings.ToLower(o.Owner)] {
			mark = "* "
		}
		fmt.Fprintf(w, "    %s%-40s  %5d LOC  %5.1f%%  →  %.2f CRU\n",
			mark, o.Owner, o.OwnedLOC, o.Share*100, o.Score)
	}
	if s.UnownedChanges > 0 {
		fmt.Fprintf(w, "      %-40s  %5d LOC  (unowned, not attributed)\n",
			"", s.UnownedChanges)
	}
	fmt.Fprintf(w, "  Total CRU:    %.2f   (sum across owners; team review burden)\n",
		s.AuthorCRU())
}

func makeIdentitySet(ids []string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[strings.ToLower(id)] = true
	}
	return m
}

// Script writes tab-separated key:\tvalue rows, gh-style. Designed for
// pipelines: `gh cru 123 | grep ^cru:`.
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
		{"bucket", string(s.Bucket)},
		{"size_factor", fmt.Sprintf("%.6f", s.SizeFactor)},
		{"risk", fmt.Sprintf("%.1f", s.Risk)},
		{"cru", fmt.Sprintf("%.6f", s.CRU())},
		{"author_cru", fmt.Sprintf("%.6f", s.AuthorCRU())},
		{"my_cru", fmt.Sprintf("%.6f", s.MyCRU)},
		{"my_share", fmt.Sprintf("%.6f", s.MyShare)},
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
		Owner    string  `json:"owner"`
		OwnedLOC int     `json:"owned_loc"`
		Share    float64 `json:"share"`
		Score    float64 `json:"score"`
		IsYou    bool    `json:"is_you"`
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
		Bucket        string      `json:"bucket"`
		SizeFactor    float64     `json:"size_factor"`
		Risk          float64     `json:"risk"`
		CRU           float64     `json:"cru"`             // owner-agnostic
		AuthorCRU     float64     `json:"author_cru"`      // team review burden
		MyCRU         float64     `json:"my_cru"`          // current user's burden
		MyShare       float64     `json:"my_share"`        // current user's LOC share
		MyIdentities  []string    `json:"my_identities,omitempty"`
		HasCodeowners bool        `json:"has_codeowners"`
		Owners        []ownerJSON `json:"owners"`
		Unowned       int         `json:"unowned_loc"`
	}
	owners := make([]ownerJSON, 0)
	mySet := makeIdentitySet(s.MyIdentities)
	for _, o := range s.SortedOwners() {
		owners = append(owners, ownerJSON{
			Owner:    o.Owner,
			OwnedLOC: o.OwnedLOC,
			Share:    o.Share,
			Score:    o.Score,
			IsYou:    mySet[strings.ToLower(o.Owner)],
		})
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
		Bucket:        string(s.Bucket),
		SizeFactor:    s.SizeFactor,
		Risk:          s.Risk,
		CRU:           s.CRU(),
		AuthorCRU:     s.AuthorCRU(),
		MyCRU:         s.MyCRU,
		MyShare:       s.MyShare,
		MyIdentities:  s.MyIdentities,
		HasCodeowners: s.HasCodeowners,
		Owners:        owners,
		Unowned:       s.UnownedChanges,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(o)
}

func riskTag(r float64) string {
	if r > 1.0 {
		return fmt.Sprintf("high (×%.0f)", r)
	}
	return "low"
}
