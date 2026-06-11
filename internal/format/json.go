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
// Float values are rounded to 6 decimal places before serialization.
// Rationale: the underlying math (Size, ownership shares, CRU)
// involves Φ-CDF approximations and floating-point division whose tails
// of `.999999999` and `.000000001` artifacts are noise, not signal.
// Six places preserves all meaningful precision (PR sizes never need
// more than ~4 significant digits of factor) and produces output that
// jq/duckdb/Python downstream can `==`-compare without surprises.
func JSON(w io.Writer, repo string, s score.PRScore) error {
	type ownerJSON struct {
		Name           *string `json:"name"` // null when type=="unowned"
		Type           string  `json:"type"` // "user" | "team" | "unowned"
		OwnedLOC       int     `json:"owned_loc"`
		OwnershipShare float64 `json:"ownership_share"`
		RequestedCRU   float64 `json:"requested_cru"`
		IsYou          bool    `json:"is_you"` // direct @login or team-membership match
	}
	type youJSON struct {
		Login          string  `json:"login"`
		OwnedLOC       int     `json:"owned_loc"`
		OwnershipShare float64 `json:"ownership_share"`
		RequestedCRU   float64 `json:"requested_cru"`
	}
	type out struct {
		Repo           string      `json:"repo"`
		Number         int         `json:"number"`
		Title          string      `json:"title"`
		Author         string      `json:"author"`
		State          string      `json:"state"`
		Additions      int         `json:"additions"`
		Deletions      int         `json:"deletions"`
		LOC            int         `json:"loc"`
		Files          int         `json:"files"`
		SizeLabel      string      `json:"size_label"`
		SizeFactor     float64     `json:"size_factor"`
		RiskLabel      string      `json:"risk_label"`
		RiskMultiplier float64     `json:"risk_multiplier"`
		NormalCRU      float64     `json:"normal_cru"` // size × risk
		TotalCRU       float64     `json:"total_cru"`  // Σ per-owner; review burden
		You            *youJSON    `json:"you,omitempty"`
		MyIdentities   []string    `json:"my_identities,omitempty"`
		Owners         []ownerJSON `json:"owners"`
		UnownedLOC     int         `json:"unowned_loc"`
	}
	owners := make([]ownerJSON, 0)
	mySet := makeIdentitySet(s.MyIdentities)
	myLoginKey := ""
	if s.MyLogin != "" {
		myLoginKey = "@" + strings.ToLower(s.MyLogin)
	}
	for _, o := range s.SortedOwners() {
		isUnowned := o.Owner == score.UnownedOwnerLabel
		ownerKey := strings.ToLower(o.Owner)
		isYou := !isUnowned && ((myLoginKey != "" && ownerKey == myLoginKey) ||
			mySet[ownerKey])

		// type: "unowned" for the synthetic row, "team" for slug-style
		// "@org/team" identifiers, "user" otherwise. CODEOWNERS doesn't
		// distinguish at the syntactic level, so we use the "/" convention
		// the same way GitHub's UI does.
		var ownerType string
		var name *string
		switch {
		case isUnowned:
			ownerType = "unowned"
			name = nil
		default:
			display := displayOwner(o.Owner)
			name = &display
			if strings.Contains(o.Owner, "/") {
				ownerType = "team"
			} else {
				ownerType = "user"
			}
		}

		owners = append(owners, ownerJSON{
			Name:           name,
			Type:           ownerType,
			OwnedLOC:       o.OwnedLOC,
			OwnershipShare: round6(o.Share),
			RequestedCRU:   round6(o.Score),
			IsYou:          isYou,
		})
	}
	var you *youJSON
	if s.MyLogin != "" && s.MyOwnedLOC > 0 {
		you = &youJSON{
			Login:          s.MyLogin,
			OwnedLOC:       s.MyOwnedLOC,
			OwnershipShare: round6(s.MyShare),
			RequestedCRU:   round6(s.MyCRU),
		}
	}
	o := out{
		Repo:           repo,
		Number:         s.PR.Number,
		Title:          s.PR.Title,
		Author:         s.PR.Author,
		State:          s.PR.State,
		Additions:      s.PR.Additions,
		Deletions:      s.PR.Deletions,
		LOC:            s.LOC,
		Files:          s.PR.Files,
		SizeLabel:      s.Size.String(),
		SizeFactor:     round6(float64(s.Size)),
		RiskLabel:      s.Risk.String(),
		RiskMultiplier: round6(s.Risk.Multiplier()),
		NormalCRU:      round6(s.CRU()),
		TotalCRU:       round6(s.AuthorCRU()),
		You:            you,
		MyIdentities:   s.MyIdentities,
		Owners:         owners,
		UnownedLOC:     s.UnownedChanges,
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
