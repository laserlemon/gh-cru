package format

import (
	"encoding/json"
	"io"
	"math"
	"strings"

	"github.com/laserlemon/gh-cru/internal/score"
)

// JSON writes one compact JSON object per call (NDJSON when looped over
// multiple PRs).
//
// The schema carries exactly what the human renderer draws, nothing more:
// the heading (repo/number/title), the Size/Risk/Base formula block, and
// an `ownership` object holding the owner table. PR metadata the score
// doesn't use (author, state, additions/deletions, file count) is
// deliberately omitted; consumers who want it have `gh pr view --json`.
//
// Float values are rounded to 6 decimal places before serialization.
// Rationale: the underlying math (Size, ownership shares, CRU) involves
// Φ-CDF approximations and floating-point division whose tails of
// `.999999999` and `.000000001` artifacts are noise, not signal. Six
// places preserves all meaningful precision (PR sizes never need more
// than ~4 significant digits of factor) and produces output that
// jq/duckdb/Python downstream can `==`-compare without surprises.
func JSON(w io.Writer, repo string, s score.PRScore) error {
	// rowJSON is the shared shape for every ownership row: the named owner
	// rows in `owners[]` and the three summary rows (unowned/all/you) are
	// all {lines, share, cru}, mirroring the LINES/SHARE/CRU table columns.
	type rowJSON struct {
		Lines int     `json:"lines"`
		Share float64 `json:"share"`
		CRU   float64 `json:"cru"`
	}
	// ownerJSON is a named owner row: a rowJSON plus the identity columns
	// that drive the table marker (name + type + is_you → =/*/•).
	type ownerJSON struct {
		Name  string  `json:"name"` // bare login or "org/team"; "@" stripped
		Type  string  `json:"type"` // "user" | "team"
		Lines int     `json:"lines"`
		Share float64 `json:"share"`
		CRU   float64 `json:"cru"`
		IsYou bool    `json:"is_you"` // direct @login or team-membership match
	}
	type ownershipJSON struct {
		Owners  []ownerJSON `json:"owners"`
		Unowned rowJSON     `json:"unowned"`       // always present, zeroed if no unowned lines
		All     rowJSON     `json:"all"`           // always present; Σ over all rows
		You     *rowJSON    `json:"you,omitempty"` // present iff identity is known
	}
	type out struct {
		Repo           string        `json:"repo"`
		Number         int           `json:"number"`
		Title          string        `json:"title"`
		Lines          int           `json:"lines"`
		SizeLabel      string        `json:"size_label"`
		SizeFactor     float64       `json:"size_factor"`
		RiskLabel      string        `json:"risk_label"`
		RiskMultiplier float64       `json:"risk_multiplier"`
		BaseCRU        float64       `json:"base_cru"`
		Ownership      ownershipJSON `json:"ownership"`
	}

	mySet := makeIdentitySet(s.MyIdentities)
	myLoginKey := ""
	if s.MyLogin != "" {
		myLoginKey = "@" + strings.ToLower(s.MyLogin)
	}

	// Named owners only; the synthetic unowned owner is surfaced as the
	// `ownership.unowned` summary row, not as an entry in `owners[]`.
	owners := make([]ownerJSON, 0)
	var unowned rowJSON
	var allLOC int
	var allShare, allCRU float64
	for _, o := range s.SortedOwners() {
		allLOC += o.OwnedLOC
		allShare += o.Share
		allCRU += o.Score

		if o.Owner == score.UnownedOwnerLabel {
			unowned = rowJSON{Lines: o.OwnedLOC, Share: round6(o.Share), CRU: round6(o.Score)}
			continue
		}

		ownerKey := strings.ToLower(o.Owner)
		isYou := (myLoginKey != "" && ownerKey == myLoginKey) || mySet[ownerKey]
		// type: "team" for slug-style "@org/team" identifiers, "user"
		// otherwise. CODEOWNERS doesn't distinguish at the syntactic level,
		// so we use the "/" convention the same way GitHub's UI does.
		ownerType := "user"
		if strings.Contains(o.Owner, "/") {
			ownerType = "team"
		}
		owners = append(owners, ownerJSON{
			Name:  displayOwner(o.Owner),
			Type:  ownerType,
			Lines: o.OwnedLOC,
			Share: round6(o.Share),
			CRU:   round6(o.Score),
			IsYou: isYou,
		})
	}

	// you: present whenever we know who you are, even at zero stake, so the
	// human output (which shows the > row on any known identity) and the
	// JSON agree. Absent entirely when unauthenticated / identity unknown.
	var you *rowJSON
	if s.MyLogin != "" {
		you = &rowJSON{
			Lines: s.MyOwnedLOC,
			Share: round6(s.MyShare),
			CRU:   round6(s.MyCRU),
		}
	}

	o := out{
		Repo:           repo,
		Number:         s.PR.Number,
		Title:          s.PR.Title,
		Lines:          s.LOC,
		SizeLabel:      s.Size.String(),
		SizeFactor:     round6(float64(s.Size)),
		RiskLabel:      s.Risk.String(),
		RiskMultiplier: round6(s.Risk.Multiplier()),
		BaseCRU:        round6(s.CRU()),
		Ownership: ownershipJSON{
			Owners:  owners,
			Unowned: unowned,
			All:     rowJSON{Lines: allLOC, Share: round6(allShare), CRU: round6(allCRU)},
			You:     you,
		},
	}
	enc := json.NewEncoder(w)
	// Compact NDJSON: one PR per line, no internal newlines. This makes
	// multi-PR output ("gh cru a b c", "gh cru list ...") a real
	// machine-parseable stream. Users who want pretty-printed JSON for
	// a single PR can pipe through `jq .`.
	return enc.Encode(o)
}

// round6 rounds a float64 to 6 decimal places. Used by JSON output to
// strip floating-point noise from CDF approximations and division (e.g.
// 0.16666666666666666 → 0.166667, 2.0000000000000004 → 2). Six places
// preserves all signal (PR sizes never need more than ~4 significant
// digits of factor) and keeps downstream `==` comparisons stable.
func round6(x float64) float64 {
	return math.Round(x*1e6) / 1e6
}
