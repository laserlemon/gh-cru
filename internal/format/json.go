package format

import (
	"encoding/json"
	"fmt"
	"io"
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
// Every float is pinned to exactly 6 decimal places via json.Number
// (e.g. "2.000000", "0.166667"), matching the cru CLI byte-for-byte.
// Two reasons over bare float64:
//   - Noise: the underlying math (Φ-CDF approximations, division) leaves
//     `.999999999` / `.000000001` tails. json.Number(%.6f) rounds them
//     off, so jq/duckdb/Python downstream can `==`-compare without
//     surprises. (Plain Go float marshaling would re-expand a value like
//     0.166667 back to 0.16666666666666666.)
//   - Stability: fixed-width output. 2 stays "2.000000", not "2"; a zero
//     share stays "0.000000". Columns line up and diffs stay quiet.
//
// Six places preserves all meaningful precision (PR factors never need
// more than ~4 significant digits).
func JSON(w io.Writer, repo string, s score.PRScore) error {
	// rowJSON is the shared shape for every ownership row: the named owner
	// rows in `owners[]` and the three summary rows (unowned/all/you) are
	// all {lines, share, cru}, mirroring the LINES/SHARE/CRU table columns.
	type rowJSON struct {
		Lines int         `json:"lines"`
		Share json.Number `json:"share"`
		CRU   json.Number `json:"cru"`
	}
	// ownerJSON is a named owner row: a rowJSON plus the identity columns
	// that drive the table marker (name + type + is_you → =/*/•).
	type ownerJSON struct {
		Name  string      `json:"name"` // bare login or "org/team"; "@" stripped
		Type  string      `json:"type"` // "user" | "team"
		Lines int         `json:"lines"`
		Share json.Number `json:"share"`
		CRU   json.Number `json:"cru"`
		IsYou bool        `json:"is_you"` // direct @login or team-membership match
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
		SizeFactor     json.Number   `json:"size_factor"`
		RiskLabel      string        `json:"risk_label"`
		RiskMultiplier json.Number   `json:"risk_multiplier"`
		BaseCRU        json.Number   `json:"base_cru"`
		Ownership      ownershipJSON `json:"ownership"`
	}

	mySet := makeIdentitySet(s.MyIdentities)
	myLoginKey := ""
	if s.MyLogin != "" {
		myLoginKey = "@" + strings.ToLower(s.MyLogin)
	}

	// Named owners only; the synthetic unowned owner is surfaced as the
	// `ownership.unowned` summary row, not as an entry in `owners[]`.
	// Zero-init unowned so the no-unowned case still emits valid JSON: the
	// zero value of json.Number is "", which marshals to an invalid empty
	// token, so it must carry num6(0) up front rather than rely on Go's
	// struct zero value.
	owners := make([]ownerJSON, 0)
	unowned := rowJSON{Lines: 0, Share: num6(0), CRU: num6(0)}
	var allLOC int
	var allShare, allCRU float64
	for _, o := range s.SortedOwners() {
		allLOC += o.OwnedLOC
		allShare += o.Share
		allCRU += o.Score

		if o.Owner == score.UnownedOwnerLabel {
			unowned = rowJSON{Lines: o.OwnedLOC, Share: num6(o.Share), CRU: num6(o.Score)}
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
			Share: num6(o.Share),
			CRU:   num6(o.Score),
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
			Share: num6(s.MyShare),
			CRU:   num6(s.MyCRU),
		}
	}

	o := out{
		Repo:           repo,
		Number:         s.PR.Number,
		Title:          s.PR.Title,
		Lines:          s.LOC,
		SizeLabel:      s.Size.String(),
		SizeFactor:     num6(float64(s.Size)),
		RiskLabel:      s.Risk.String(),
		RiskMultiplier: num6(s.Risk.Multiplier()),
		BaseCRU:        num6(s.CRU()),
		Ownership: ownershipJSON{
			Owners:  owners,
			Unowned: unowned,
			All:     rowJSON{Lines: allLOC, Share: num6(allShare), CRU: num6(allCRU)},
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

// num6 formats a float64 to exactly 6 decimal places as a json.Number.
// json.Number is a string under the hood that the encoder emits as a raw
// numeric literal (no quotes), so this pins precision at "1.807063" /
// "2.000000" instead of letting the encoder revert to shortest-roundtrip
// printing (which would re-expand a rounded value to 14+ digits when it
// can't be represented exactly in float64). Matches the cru CLI's num6.
func num6(f float64) json.Number {
	return json.Number(fmt.Sprintf("%.6f", f))
}
