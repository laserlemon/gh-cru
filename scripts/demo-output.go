// Command demo-output renders representative gh-cru Human-mode output for
// each major code path, using synthetic PRScore values (no network).
//
// Run with `go run ./scripts/demo-output.go` from the gh-cru repo root.
// Color autodetection follows gh CLI conventions: TTY → on, NO_COLOR → off,
// FORCE_COLOR=1 → on regardless. Use FORCE_COLOR=1 to capture ANSI to a
// file for comparison across themes.
//
// This is a developer convenience: it lives outside the package (its own
// main) so it doesn't ship with the release. Whenever you touch
// internal/format, run this and eyeball every variant.
package main

import (
	"fmt"
	"os"

	"github.com/cli/go-gh/v2/pkg/term"

	"github.com/laserlemon/cru"
	"github.com/laserlemon/gh-cru/internal/format"
	ghc "github.com/laserlemon/gh-cru/internal/gh"
	"github.com/laserlemon/gh-cru/internal/score"
)

// scenario builds one PRScore from explicit inputs. Easier than
// round-tripping through score.Compute since we want full control over
// the synthetic case (overlap, my-team membership, unowned LOC, etc).
type scenario struct {
	Name          string
	Repo          string
	Number        int
	LOC           int
	Risk          cru.Risk
	HasCodeowners bool
	Owners        []owner
	UnownedLOC    int
	MyLogin       string
	MyIdentities  []string
}

type owner struct {
	Name string
	LOC  int
}

func main() {
	// One scenario per distinct code path. Each subsequent scenario
	// stacks one new feature on the prior, so a glance down the page is
	// also a tour of the formatter's responsibilities.
	cases := []scenario{
		{
			Name:          "single owner, no overlap (the trivial path)",
			Repo:          "acme/web",
			Number:        1234,
			LOC:           34,
			Risk:          cru.RiskLow,
			HasCodeowners: true,
			Owners:        []owner{{Name: "@acme/web-team", LOC: 34}},
		},
		{
			Name:          "no CODEOWNERS file at all (whole PR rendered as ~ unowned)",
			Repo:          "acme/sandbox",
			Number:        42,
			LOC:           48,
			Risk:          cru.RiskLow,
		},
		{
			Name:          "owners + partial unowned LOC (mixed coverage)",
			Repo:          "acme/web",
			Number:        1236,
			LOC:           200,
			Risk:          cru.RiskLow,
			HasCodeowners: true,
			Owners: []owner{
				{Name: "@acme/web-team", LOC: 120},
				{Name: "@acme/auth-team", LOC: 50},
			},
			UnownedLOC: 30,
		},
		{
			Name:          "multiple owners with overlap (Total CRU exceeds Normal CRU)",
			Repo:          "acme/web",
			Number:        1235,
			LOC:           80,
			Risk:          cru.RiskLow,
			HasCodeowners: true,
			Owners: []owner{
				{Name: "@acme/auth-team", LOC: 80},
				{Name: "@acme/web-team", LOC: 80},
			},
		},
		{
			Name:          "everything at once: direct @login, team, other, unowned, high risk",
			Repo:          "acme/payments",
			Number:        78,
			LOC:           240,
			Risk:          cru.RiskHigh,
			HasCodeowners: true,
			Owners: []owner{
				{Name: "@laserlemon", LOC: 40},
				{Name: "@acme/big-orca", LOC: 60},
				{Name: "@acme/payments-team", LOC: 100},
			},
			UnownedLOC:   40,
			MyLogin:      "laserlemon",
			MyIdentities: []string{"@laserlemon", "@acme/big-orca"},
		},
		{
			Name:          "different repo: heading color hash differentiates from prior cases",
			Repo:          "acme/api",
			Number:        1235,
			LOC:           34,
			Risk:          cru.RiskMedium,
			HasCodeowners: true,
			Owners:        []owner{{Name: "@acme/api-team", LOC: 34}},
		},
	}

	t := term.FromEnv()
	for _, c := range cases {
		fmt.Println()
		bannerColor := func(s string) string { return s }
		if t.IsColorEnabled() {
			// dim+underlined banner so it doesn't compete with the PR heading
			bannerColor = func(s string) string { return "\033[2;4m" + s + "\033[0m" }
		}
		fmt.Fprintln(os.Stdout, bannerColor("=== "+c.Name+" ==="))
		fmt.Println()
		s := buildScore(c)
		format.Human(os.Stdout, c.Repo, s, t)
	}
	fmt.Println()
}

func buildScore(c scenario) score.PRScore {
	size := cru.CalculateSize(c.LOC)
	sf := float64(size)
	rf := c.Risk.Multiplier()
	result := score.PRScore{
		PR: ghc.PR{
			Number: c.Number,
			Title:  "demo",
			Author: "demo",
			State:  "open",
		},
		LOC:           c.LOC,
		Size:          size,
		Risk:          c.Risk,
		HasCodeowners: c.HasCodeowners,
		OwnershipMap:  make(map[string]score.Ownership),
		MyLogin:       c.MyLogin,
		MyIdentities:  c.MyIdentities,
		TeamsResolved: true,
	}
	if !c.HasCodeowners {
		// Mirror real Compute behavior: no CODEOWNERS file → entire PR
		// is attributed to a single synthetic unowned owner. This makes
		// AuthorCRU == CRU and the table render a single ~ unowned line.
		result.UnownedChanges = c.LOC
		denom := c.LOC
		if denom == 0 {
			denom = 1
		}
		result.OwnershipMap[score.UnownedOwnerLabel] = score.Ownership{
			Owner:    score.UnownedOwnerLabel,
			OwnedLOC: c.LOC,
			Share:    float64(c.LOC) / float64(denom),
			Score:    sf * float64(c.LOC) / float64(denom) * rf,
		}
		result.OwnerOrder = []string{score.UnownedOwnerLabel}
		return result
	}
	// Use the PR's LOC as the denominator, matching real Compute behavior.
	// This lets per-owner LOCs sum to MORE than the PR LOC (overlap case:
	// the same lines are owned by multiple teams) or LESS (gaps: covered
	// by UnownedLOC).
	denom := c.LOC
	if denom == 0 {
		denom = 1
	}

	// my-owned LOC: sum LOC for owners whose name is in MyIdentities,
	// capped at the PR LOC to avoid double-counting when the user matches
	// multiple owners (real Compute counts each matched file once).
	mySet := make(map[string]bool, len(c.MyIdentities))
	for _, id := range c.MyIdentities {
		mySet[id] = true
	}
	myLOC := 0
	for _, o := range c.Owners {
		if mySet[o.Name] {
			myLOC += o.LOC
		}
	}
	if myLOC > c.LOC {
		myLOC = c.LOC
	}
	if len(c.MyIdentities) > 0 {
		result.MyOwnedLOC = myLOC
		result.MyShare = float64(myLOC) / float64(denom)
		result.MyCRU = sf * result.MyShare * rf
	}

	for _, o := range c.Owners {
		share := float64(o.LOC) / float64(denom)
		result.OwnershipMap[o.Name] = score.Ownership{
			Owner:    o.Name,
			OwnedLOC: o.LOC,
			Share:    share,
			Score:    sf * share * rf,
		}
		result.OwnerOrder = append(result.OwnerOrder, o.Name)
	}
	if c.UnownedLOC > 0 {
		share := float64(c.UnownedLOC) / float64(denom)
		result.OwnershipMap[score.UnownedOwnerLabel] = score.Ownership{
			Owner:    score.UnownedOwnerLabel,
			OwnedLOC: c.UnownedLOC,
			Share:    share,
			Score:    sf * share * rf,
		}
		result.OwnerOrder = append(result.OwnerOrder, score.UnownedOwnerLabel)
	}
	result.UnownedChanges = c.UnownedLOC
	return result
}
