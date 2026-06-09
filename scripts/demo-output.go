// Command demo-output renders representative gh-cru Human-mode output for
// each major code path, using synthetic PRScore values (no network).
//
// Run with `go run ./scripts/demo-output.go` from the gh-cru repo root.
// Color autodetection follows gh CLI conventions: TTY → on, NO_COLOR → off,
// FORCE_COLOR=1 → on regardless. Use FORCE_COLOR=1 to capture ANSI to a
// file for comparison across themes.
//
// Each scenario is preceded by a `=== name ===` banner so you can scan the
// output and see every variant in one shot.
package main

import (
	"fmt"
	"os"

	"github.com/cli/go-gh/v2/pkg/term"

	"github.com/laserlemon/gh-cru/internal/format"
	ghc "github.com/laserlemon/gh-cru/internal/gh"
	"github.com/laserlemon/gh-cru/internal/score"
	"github.com/laserlemon/cru"
)

// scenario builds one PRScore from explicit inputs. Easier than
// round-tripping through score.Compute since we want full control over
// the synthetic case (overlap, my-team membership, unowned LOC, etc).
type scenario struct {
	Name          string
	Repo          string
	Number        int
	LOC           int
	Risk          float64
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
	cases := []scenario{
		{
			Name:          "1. typical PR: single owner, no overlap, no unowned (clean case)",
			Repo:          "acme/web",
			Number:        1234,
			LOC:           34,
			Risk:          cru.RiskLow,
			HasCodeowners: true,
			Owners:        []owner{{Name: "@acme/web-team", LOC: 34}},
		},
		{
			Name:          "2. multiple owners with overlap (total > normal)",
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
			Name:          "3. owners + unowned LOC (the new case we're shipping)",
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
			Name:          "4. CODEOWNERS exists but no rule matches (entirely unowned, was 0 CRU before)",
			Repo:          "acme/web",
			Number:        1237,
			LOC:           75,
			Risk:          cru.RiskLow,
			HasCodeowners: true,
			UnownedLOC:    75,
		},
		{
			Name:          "5. no CODEOWNERS file at all (whole PR rendered as ~ unowned)",
			Repo:          "acme/sandbox",
			Number:        42,
			LOC:           48,
			Risk:          cru.RiskLow,
		},
		{
			Name:          "6. you're on one of the teams (* blue marker)",
			Repo:          "acme/web",
			Number:        1238,
			LOC:           90,
			Risk:          cru.RiskLow,
			HasCodeowners: true,
			Owners: []owner{
				{Name: "@acme/web-team", LOC: 30},
				{Name: "@acme/big-orca", LOC: 60},
			},
			MyLogin:      "laserlemon",
			MyIdentities: []string{"@laserlemon", "@acme/big-orca"},
		},
		{
			Name:          "7. direct @login match (= blue+bold marker, distinct from team match)",
			Repo:          "acme/web",
			Number:        1239,
			LOC:           50,
			Risk:          cru.RiskLow,
			HasCodeowners: true,
			Owners: []owner{
				{Name: "@acme/web-team", LOC: 30},
				{Name: "@laserlemon", LOC: 20},
			},
			MyLogin:      "laserlemon",
			MyIdentities: []string{"@laserlemon"},
		},
		{
			Name:          "8. high-risk PR (risk factor 4×)",
			Repo:          "acme/payments",
			Number:        77,
			LOC:           42,
			Risk:          cru.RiskHigh,
			HasCodeowners: true,
			Owners: []owner{
				{Name: "@acme/payments-team", LOC: 42},
			},
		},
		{
			Name:          "9. extra-small PR (XS bucket, low CRU)",
			Repo:          "acme/web",
			Number:        1240,
			LOC:           3,
			Risk:          cru.RiskLow,
			HasCodeowners: true,
			Owners:        []owner{{Name: "@acme/web-team", LOC: 3}},
		},
		{
			Name:          "10. extra-large PR (XL bucket, high CRU)",
			Repo:          "acme/web",
			Number:        1241,
			LOC:           650,
			Risk:          cru.RiskLow,
			HasCodeowners: true,
			Owners: []owner{
				{Name: "@acme/web-team", LOC: 400},
				{Name: "@acme/auth-team", LOC: 200},
			},
			UnownedLOC: 50,
		},
		{
			Name:          "11. mix of everything: direct match, team match, other, unowned, high risk",
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
			Name:          "12. PR number color sample (same repo, different PRs — palette differentiates)",
			Repo:          "acme/web",
			Number:        9001,
			LOC:           34,
			Risk:          cru.RiskLow,
			HasCodeowners: true,
			Owners:        []owner{{Name: "@acme/web-team", LOC: 34}},
		},
		{
			Name:          "13. same repo as #12, different PR number (different #N color)",
			Repo:          "acme/web",
			Number:        9002,
			LOC:           34,
			Risk:          cru.RiskLow,
			HasCodeowners: true,
			Owners:        []owner{{Name: "@acme/web-team", LOC: 34}},
		},
		{
			Name:          "14. different repo, same PR number as #2 (different repo color)",
			Repo:          "acme/api",
			Number:        1235,
			LOC:           34,
			Risk:          cru.RiskLow,
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
	sf := cru.SizeFactor(c.LOC)
	result := score.PRScore{
		PR: ghc.PR{
			Number: c.Number,
			Title:  "demo",
			Author: "demo",
			State:  "open",
		},
		LOC:           c.LOC,
		SizeFactor:    sf,
		Bucket:        cru.Bucket(c.LOC),
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
			Score:    sf * float64(c.LOC) / float64(denom) * c.Risk,
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
		result.MyCRU = sf * result.MyShare * c.Risk
	}

	for _, o := range c.Owners {
		share := float64(o.LOC) / float64(denom)
		result.OwnershipMap[o.Name] = score.Ownership{
			Owner:    o.Name,
			OwnedLOC: o.LOC,
			Share:    share,
			Score:    sf * share * c.Risk,
		}
		result.OwnerOrder = append(result.OwnerOrder, o.Name)
	}
	if c.UnownedLOC > 0 {
		share := float64(c.UnownedLOC) / float64(denom)
		result.OwnershipMap[score.UnownedOwnerLabel] = score.Ownership{
			Owner:    score.UnownedOwnerLabel,
			OwnedLOC: c.UnownedLOC,
			Share:    share,
			Score:    sf * share * c.Risk,
		}
		result.OwnerOrder = append(result.OwnerOrder, score.UnownedOwnerLabel)
	}
	result.UnownedChanges = c.UnownedLOC
	return result
}
