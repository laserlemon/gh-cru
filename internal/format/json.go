package format

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/cli/go-gh/v2/pkg/jsonpretty"
	"github.com/cli/go-gh/v2/pkg/term"

	"github.com/laserlemon/gh-cru/internal/score"
)

// rowJSON is the shared shape for every ownership row: the named owner
// rows in `owners[]` and the three summary rows (unowned/all/you) are
// all {lines, share, cru}, mirroring the LINES/SHARE/CRU table columns.
type rowJSON struct {
	Lines int         `json:"lines"`
	Share json.Number `json:"share"`
	CRU   json.Number `json:"cru"`
}

// ownerJSON is a named owner row: a rowJSON plus the identity columns
// that drive the table marker (name + type + isYou → =/*/•).
type ownerJSON struct {
	Name  string      `json:"name"` // bare login or "org/team"; "@" stripped
	Type  string      `json:"type"` // "user" | "team"
	Lines int         `json:"lines"`
	Share json.Number `json:"share"`
	CRU   json.Number `json:"cru"`
	IsYou bool        `json:"isYou"` // direct @login or team-membership match
}

type ownershipJSON struct {
	Owners  []ownerJSON `json:"owners"`
	Unowned rowJSON     `json:"unowned"`       // always present, zeroed if no unowned lines
	All     rowJSON     `json:"all"`           // always present; Σ over all rows
	You     *rowJSON    `json:"you,omitempty"` // present iff identity is known
}

// repositoryJSON mirrors gh's own nested repository object (as emitted
// by `gh search prs --json repository`): the bare name plus the
// owner-qualified nameWithOwner. gh never abbreviates to "repo" or
// flattens it to a string, so neither do we. `id` is intentionally
// omitted: a repo node ID is useless to a CRU consumer and isn't on
// hand in the `gh pr list` path.
type repositoryJSON struct {
	Name          string `json:"name"`          // bare repo name, e.g. "cli"
	NameWithOwner string `json:"nameWithOwner"` // "owner/name", e.g. "cli/cli"
}

// prJSON is the top-level per-PR measurement object. Its schema carries
// exactly what the human renderer draws, nothing more: the heading
// (repository/number/title), the Size/Risk/Base formula block, and an
// `ownership` object holding the owner table. PR metadata the score
// doesn't use (author, state, additions/deletions, file count) is
// deliberately omitted; consumers who want it have `gh pr view --json`.
type prJSON struct {
	Repository     repositoryJSON `json:"repository"`
	Number         int            `json:"number"`
	Title          string         `json:"title"`
	Lines          int            `json:"lines"`
	SizeLabel      string         `json:"sizeLabel"`
	SizeFactor     json.Number    `json:"sizeFactor"`
	RiskLabel      string         `json:"riskLabel"`
	RiskMultiplier json.Number    `json:"riskMultiplier"`
	BaseCRU        json.Number    `json:"baseCru"`
	Ownership      *ownershipJSON `json:"ownership,omitempty"` // omitted under --skip-ownership
}

// Item pairs a repo ("owner/name") with its computed score for array
// rendering. JSONArray takes a slice of these so the list path can emit
// one JSON array over the whole batch.
type Item struct {
	Repo  string
	Score score.PRScore
}

// buildPR assembles the marshalable per-PR object from a score.
//
// `repository` is gh's own nested object ({name, nameWithOwner}), matching
// `gh search prs --json repository` rather than a flattened "owner/name"
// string.
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
func buildPR(repo string, s score.PRScore) prJSON {
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
	for _, o := range s.SortedOwners() {
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

	all := s.Totals()
	// Split the "owner/name" repo string into gh's nested repository
	// shape. IndexByte on the first "/" so an unexpected slash in the name
	// (there shouldn't be one) doesn't lose data; if there's no slash at
	// all, Name falls back to the whole string and nameWithOwner carries
	// it verbatim.
	repoName := repo
	if i := strings.IndexByte(repo, '/'); i >= 0 {
		repoName = repo[i+1:]
	}
	o := prJSON{
		Repository:     repositoryJSON{Name: repoName, NameWithOwner: repo},
		Number:         s.PR.Number,
		Title:          s.PR.Title,
		Lines:          s.LOC,
		SizeLabel:      s.Size.String(),
		SizeFactor:     num6(float64(s.Size)),
		RiskLabel:      s.Risk.String(),
		RiskMultiplier: num6(s.Risk.Multiplier()),
		BaseCRU:        num6(s.CRU()),
	}
	// --skip-ownership: no CODEOWNERS was consulted, so omit the
	// ownership object entirely rather than emit a fabricated 100%
	// unowned block. The measurement degrades cleanly to baseCru.
	if !s.OwnershipSkipped {
		o.Ownership = &ownershipJSON{
			Owners:  owners,
			Unowned: unowned,
			All:     rowJSON{Lines: all.Lines, Share: num6(all.Share), CRU: num6(all.CRU)},
			You:     you,
		}
	}
	return o
}

// JSON writes one PR's measurement as a bare JSON object, mirroring
// `gh pr view --json` (singular → object). Used by the view path.
func JSON(w io.Writer, repo string, s score.PRScore, t term.Term) error {
	return writeJSON(w, buildPR(repo, s), t)
}

// JSONArray writes a whole batch of measurements as a single JSON array,
// mirroring `gh pr list --json` (plural → array). Used by the list path.
// An empty or nil slice emits `[]` (not `null`), matching gh's empty-list
// JSON so a downstream `jq length` sees 0 rather than a parse error.
func JSONArray(w io.Writer, items []Item, t term.Term) error {
	arr := make([]prJSON, 0, len(items))
	for _, it := range items {
		arr = append(arr, buildPR(it.Repo, it.Score))
	}
	return writeJSON(w, arr, t)
}

// writeJSON encodes v (a bare object or an array) and writes it, choosing
// the output mode the same way gh's own --json does:
//   - On a TTY, pretty-print (2-space indent) and colorize when color is
//     enabled, via go-gh's jsonpretty (the same pretty-printer gh uses for
//     its --json output and --jq rendering).
//   - Piped/redirected, stay compact: a single line, no internal newlines.
//     For an object that's `{...}\n`; for an array `[{...},{...}]\n`, the
//     machine-parseable stream contract for multi-PR runs.
func writeJSON(w io.Writer, v any, t term.Term) error {
	if t.IsTerminalOutput() {
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(v); err != nil {
			return err
		}
		return jsonpretty.Format(w, &buf, "  ", t.IsColorEnabled())
	}
	return json.NewEncoder(w).Encode(v)
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
