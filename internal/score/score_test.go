package score

import (
	"math"
	"strings"
	"testing"

	"github.com/hmarr/codeowners"

	"github.com/laserlemon/cru"
	ghc "github.com/laserlemon/gh-cru/internal/gh"
)

// pr returns a test PR with deterministic LOC. Title/State/Author are
// rarely material to scoring; default them.
func pr(loc int, labels ...string) ghc.PR {
	// Split loc roughly evenly between additions and deletions so LOC()
	// returns loc. Edge: loc=0 keeps both zero.
	add := loc / 2
	del := loc - add
	return ghc.PR{
		Number:    1,
		Title:     "test",
		Author:    "tester",
		State:     "open",
		Additions: add,
		Deletions: del,
		Labels:    labels,
	}
}

// file is a one-liner constructor for the gh.File shape used by Compute.
func file(path string, changes int) ghc.File {
	return ghc.File{Path: path, Changes: changes}
}

// ownersFrom parses a CODEOWNERS string into a ruleset. Tests fail loudly
// if the test fixture is malformed.
func ownersFrom(t *testing.T, body string) codeowners.Ruleset {
	t.Helper()
	rs, err := codeowners.ParseFile(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse CODEOWNERS: %v", err)
	}
	return rs
}

// closeTo returns true if a and b agree to within 1e-9.
func closeTo(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// TestComputeNoCodeowners verifies that when the repo has no CODEOWNERS
// file at all (owners == nil), the entire PR is attributed to a single
// synthetic "unowned" owner. This makes Totals().CRU == CRU and renders a
// uniform 1-row table.
func TestComputeNoCodeowners(t *testing.T) {
	p := pr(100)
	files := []ghc.File{file("README.md", 100)}
	s := Compute(p, files, nil, nil, nil, "", nil)

	if s.HasCodeowners {
		t.Errorf("HasCodeowners = true, want false")
	}
	if len(s.OwnershipMap) != 1 {
		t.Fatalf("OwnershipMap size = %d, want 1", len(s.OwnershipMap))
	}
	o, ok := s.OwnershipMap[UnownedOwnerLabel]
	if !ok {
		t.Fatalf("missing %q in OwnershipMap; have %v", UnownedOwnerLabel, s.OwnershipMap)
	}
	if o.OwnedLOC != 100 {
		t.Errorf("unowned OwnedLOC = %d, want 100", o.OwnedLOC)
	}
	if !closeTo(o.Share, 1.0) {
		t.Errorf("unowned Share = %v, want 1.0", o.Share)
	}
	if !closeTo(s.Totals().CRU, s.CRU()) {
		t.Errorf("Totals().CRU = %v, CRU = %v, want equal", s.Totals().CRU, s.CRU())
	}
	if got, want := s.OwnerOrder, []string{UnownedOwnerLabel}; len(got) != 1 || got[0] != want[0] {
		t.Errorf("OwnerOrder = %v, want %v", got, want)
	}
}

// TestComputeCodeownersNoMatch verifies that when CODEOWNERS exists but
// no rule matches the PR's files, the synthetic unowned owner still
// covers 100% (previously this was the "group B" bug where total_cru
// silently went to 0).
func TestComputeCodeownersNoMatch(t *testing.T) {
	p := pr(50)
	files := []ghc.File{file("docs/random.md", 50)}
	owners := ownersFrom(t, "*.go @acme/eng\n") // no rule matches *.md

	s := Compute(p, files, owners, nil, nil, "", nil)

	if !s.HasCodeowners {
		t.Errorf("HasCodeowners = false, want true")
	}
	if len(s.OwnershipMap) != 1 {
		t.Fatalf("OwnershipMap size = %d, want 1", len(s.OwnershipMap))
	}
	if _, ok := s.OwnershipMap[UnownedOwnerLabel]; !ok {
		t.Fatalf("missing synthetic unowned owner")
	}
	if !closeTo(s.Totals().CRU, s.CRU()) {
		t.Errorf("Totals().CRU = %v, CRU = %v, want equal (full unowned)", s.Totals().CRU, s.CRU())
	}
}

// TestComputeFullCoverage verifies the clean case: one owner covers
// 100% of the PR. Total CRU == Normal CRU; no synthetic unowned row.
func TestComputeFullCoverage(t *testing.T) {
	p := pr(80)
	files := []ghc.File{file("main.go", 80)}
	owners := ownersFrom(t, "*.go @acme/eng\n")

	s := Compute(p, files, owners, nil, nil, "", nil)

	if len(s.OwnershipMap) != 1 {
		t.Fatalf("OwnershipMap size = %d, want 1 (single owner, no unowned)", len(s.OwnershipMap))
	}
	if _, ok := s.OwnershipMap[UnownedOwnerLabel]; ok {
		t.Errorf("unexpected synthetic unowned owner with full coverage")
	}
	if !closeTo(s.Totals().CRU, s.CRU()) {
		t.Errorf("Totals().CRU = %v, CRU = %v, want equal (no overlap, no gap)", s.Totals().CRU, s.CRU())
	}
}

// TestComputePartialCoverage verifies the unowned-row-with-real-owners
// case: some files are owned, some aren't. The shares should sum to 1.0
// and Totals().CRU should equal CRU.
func TestComputePartialCoverage(t *testing.T) {
	p := pr(100)
	files := []ghc.File{
		file("main.go", 60),
		file("README.md", 40),
	}
	owners := ownersFrom(t, "*.go @acme/eng\n")

	s := Compute(p, files, owners, nil, nil, "", nil)

	if len(s.OwnershipMap) != 2 {
		t.Fatalf("OwnershipMap size = %d, want 2 (eng + unowned)", len(s.OwnershipMap))
	}
	eng, ok := s.OwnershipMap["@acme/eng"]
	if !ok {
		t.Fatalf("missing @acme/eng owner")
	}
	if eng.OwnedLOC != 60 {
		t.Errorf("eng.OwnedLOC = %d, want 60", eng.OwnedLOC)
	}
	un, ok := s.OwnershipMap[UnownedOwnerLabel]
	if !ok {
		t.Fatalf("missing synthetic unowned owner")
	}
	if un.OwnedLOC != 40 {
		t.Errorf("unowned.OwnedLOC = %d, want 40", un.OwnedLOC)
	}
	if !closeTo(eng.Share+un.Share, 1.0) {
		t.Errorf("shares sum to %v, want 1.0", eng.Share+un.Share)
	}
	if !closeTo(s.Totals().CRU, s.CRU()) {
		t.Errorf("Totals().CRU = %v, CRU = %v, want equal (no overlap)", s.Totals().CRU, s.CRU())
	}
}

// TestComputeOverlap verifies the case that breaks the "Totals().CRU == CRU"
// equality: a CODEOWNERS rule with multiple owners (the only way the
// codeowners library produces multiple owners on the same file, since
// it uses last-match-wins semantics across rules). Each listed owner
// gets 100% share, so Totals().CRU = 2 × CRU.
func TestComputeOverlap(t *testing.T) {
	p := pr(50)
	files := []ghc.File{file("api/foo.go", 50)}
	owners := ownersFrom(t, "*.go @acme/eng @acme/api\n")

	s := Compute(p, files, owners, nil, nil, "", nil)

	if len(s.OwnershipMap) != 2 {
		t.Fatalf("OwnershipMap size = %d, want 2 (eng + api, both 100%%)", len(s.OwnershipMap))
	}
	for _, o := range s.OwnershipMap {
		if !closeTo(o.Share, 1.0) {
			t.Errorf("%s.Share = %v, want 1.0 (multi-owner rule)", o.Owner, o.Share)
		}
	}
	if s.Totals().CRU <= s.CRU() {
		t.Errorf("Totals().CRU = %v should EXCEED CRU = %v (overlap doubles)", s.Totals().CRU, s.CRU())
	}
	if !closeTo(s.Totals().CRU, 2*s.CRU()) {
		t.Errorf("Totals().CRU = %v, want 2 × CRU = %v (two owners, full coverage each)",
			s.Totals().CRU, 2*s.CRU())
	}
}

// TestComputeMyCRUDirect verifies the your-CRU computation for a
// direct @login match (not via team).
func TestComputeMyCRUDirect(t *testing.T) {
	p := pr(100)
	files := []ghc.File{
		file("a.go", 40),
		file("b.go", 60),
	}
	owners := ownersFrom(t, "a.go @laserlemon\nb.go @acme/eng\n")

	s := Compute(p, files, owners, nil, nil, "laserlemon", []string{"@laserlemon"})

	if s.MyOwnedLOC != 40 {
		t.Errorf("MyOwnedLOC = %d, want 40", s.MyOwnedLOC)
	}
	if !closeTo(s.MyShare, 0.4) {
		t.Errorf("MyShare = %v, want 0.4", s.MyShare)
	}
	if !closeTo(s.MyCRU, cru.Calculate(100, 40, cru.RiskLow)) {
		t.Errorf("MyCRU = %v, want %v", s.MyCRU, cru.Calculate(100, 40, cru.RiskLow))
	}
}

// TestComputeMyCRUTeam verifies that ownership via team membership is
// counted toward MyCRU.
func TestComputeMyCRUTeam(t *testing.T) {
	p := pr(100)
	files := []ghc.File{file("a.go", 100)}
	owners := ownersFrom(t, "*.go @acme/big-orca\n")

	s := Compute(p, files, owners, nil, nil, "laserlemon",
		[]string{"@laserlemon", "@acme/big-orca"})

	if s.MyOwnedLOC != 100 {
		t.Errorf("MyOwnedLOC = %d, want 100 (via team)", s.MyOwnedLOC)
	}
	if !closeTo(s.MyShare, 1.0) {
		t.Errorf("MyShare = %v, want 1.0", s.MyShare)
	}
}

// TestComputeMyCRUDedup verifies that when the user matches via BOTH
// direct @login AND a team on the same file, the LOC is counted ONCE.
func TestComputeMyCRUDedup(t *testing.T) {
	p := pr(100)
	files := []ghc.File{file("a.go", 100)}
	owners := ownersFrom(t, "*.go @laserlemon @acme/big-orca\n")

	s := Compute(p, files, owners, nil, nil, "laserlemon",
		[]string{"@laserlemon", "@acme/big-orca"})

	if s.MyOwnedLOC != 100 {
		t.Errorf("MyOwnedLOC = %d, want 100 (counted once, not 200)", s.MyOwnedLOC)
	}
}

// TestComputeHighRisk verifies the risk multiplier is applied when the
// configured risk label is present.
func TestComputeHighRisk(t *testing.T) {
	p := pr(50, "risk:high")
	files := []ghc.File{file("a.go", 50)}
	owners := ownersFrom(t, "*.go @acme/eng\n")

	s := Compute(p, files, owners, []string{"risk:high"}, nil, "", nil)

	if s.Risk != cru.RiskHigh {
		t.Errorf("Risk = %v, want %v (RiskHigh)", s.Risk, cru.RiskHigh)
	}
	wantCRU := cru.Calculate(50, 50, cru.RiskHigh)
	if !closeTo(s.CRU(), wantCRU) {
		t.Errorf("CRU = %v, want %v (size × 4)", s.CRU(), wantCRU)
	}
}

// TestComputeRiskLabelCaseInsensitive verifies that label matching is
// case-insensitive (e.g. "RISK:HIGH" still triggers RiskHigh).
func TestComputeRiskLabelCaseInsensitive(t *testing.T) {
	p := pr(50, "RISK:HIGH")
	files := []ghc.File{file("a.go", 50)}
	owners := ownersFrom(t, "*.go @acme/eng\n")

	s := Compute(p, files, owners, []string{"risk:high"}, nil, "", nil)

	if s.Risk != cru.RiskHigh {
		t.Errorf("Risk = %v, want %v (case-insensitive match)", s.Risk, cru.RiskHigh)
	}
}

// TestSortedOwnersOrder verifies that the synthetic unowned owner always
// renders LAST in the sorted output, even when added before real owners
// would be (since OwnerOrder is append-only and unowned is appended after
// the loop).
func TestSortedOwnersOrder(t *testing.T) {
	p := pr(100)
	files := []ghc.File{
		file("a.go", 60),
		file("README.md", 40), // unowned
	}
	owners := ownersFrom(t, "*.go @acme/eng\n")

	s := Compute(p, files, owners, nil, nil, "", nil)

	ordered := s.SortedOwners()
	if len(ordered) != 2 {
		t.Fatalf("SortedOwners returned %d, want 2", len(ordered))
	}
	if ordered[len(ordered)-1].Owner != UnownedOwnerLabel {
		t.Errorf("last owner = %q, want %q", ordered[len(ordered)-1].Owner, UnownedOwnerLabel)
	}
}

// TestCRUEqualsSizeTimesRisk verifies the trivial composition: CRU() is
// just size × risk and doesn't depend on ownership.
func TestCRUEqualsSizeTimesRisk(t *testing.T) {
	p := pr(50, "risk:high")
	files := []ghc.File{file("a.go", 50)}
	owners := ownersFrom(t, "*.go @nobody/important\n")

	s := Compute(p, files, owners, []string{"risk:high"}, nil, "", nil)
	want := cru.Calculate(50, 50, cru.RiskHigh)
	if !closeTo(s.CRU(), want) {
		t.Errorf("CRU() = %v, want %v", s.CRU(), want)
	}
}

// TestTotalsEmpty handles the edge: an empty OwnershipMap (which
// shouldn't happen via Compute but is reachable via zero-value PRScore).
// Totals().CRU should be 0, not panic.
func TestTotalsEmpty(t *testing.T) {
	s := PRScore{OwnershipMap: map[string]Ownership{}}
	if got := s.Totals().CRU; got != 0 {
		t.Errorf("Totals().CRU on empty map = %v, want 0", got)
	}
}

// TestComputeZeroLOC handles the degenerate empty PR. The formula
// shouldn't divide by zero; CRU should hit the bounded floor.
func TestComputeZeroLOC(t *testing.T) {
	p := pr(0)
	files := []ghc.File{}
	s := Compute(p, files, nil, nil, nil, "", nil)

	if s.LOC != 0 {
		t.Errorf("LOC = %d, want 0", s.LOC)
	}
	// The synthetic unowned row should still exist with OwnedLOC = 0.
	if len(s.OwnershipMap) != 1 {
		t.Errorf("OwnershipMap size = %d, want 1 (one unowned row even at 0 LOC)", len(s.OwnershipMap))
	}
	// Should not panic; CRU stays at the bounded floor.
	if math.IsNaN(s.CRU()) || math.IsInf(s.CRU(), 0) {
		t.Errorf("CRU at 0 LOC = %v, want finite (bounded floor)", s.CRU())
	}
}

func TestHasAnyLabel(t *testing.T) {
	tests := []struct {
		name    string
		labels  []string
		targets []string
		want    bool
	}{
		{"empty targets", []string{"bug"}, nil, false},
		{"empty labels", nil, []string{"risk:high"}, false},
		{"single match", []string{"bug", "risk:high"}, []string{"risk:high"}, true},
		{"case insensitive", []string{"Risk:High"}, []string{"risk:high"}, true},
		{"any of many matches", []string{"bug", "danger"}, []string{"risk:high", "danger", "p0"}, true},
		{"none match", []string{"bug"}, []string{"risk:high", "danger"}, false},
		{"empty target string skipped", []string{"bug"}, []string{"", "bug"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasAnyLabel(tc.labels, tc.targets); got != tc.want {
				t.Errorf("hasAnyLabel(%v, %v) = %v, want %v", tc.labels, tc.targets, got, tc.want)
			}
		})
	}
}

func TestCompute_RiskLabels_MultipleAccepted(t *testing.T) {
	p := ghc.PR{Number: 1, Additions: 100, Deletions: 50, Labels: []string{"p0"}}
	files := []ghc.File{{Path: "x.go", Changes: 150}}
	s := Compute(p, files, nil, []string{"risk:high", "p0", "critical"}, nil, "", nil)
	if s.Risk != cru.RiskHigh {
		t.Errorf("expected high risk (any of multi-label set matches), got %v", s.Risk)
	}
}

func TestCompute_MediumRisk(t *testing.T) {
	p := ghc.PR{Number: 1, Additions: 100, Deletions: 50, Labels: []string{"risk:medium"}}
	files := []ghc.File{{Path: "x.go", Changes: 150}}
	s := Compute(p, files, nil, []string{"risk:high"}, []string{"risk:medium"}, "", nil)
	if s.Risk != cru.RiskMedium {
		t.Errorf("expected medium risk, got %v", s.Risk)
	}
}

func TestCompute_HighWinsOverMedium(t *testing.T) {
	// A PR carrying BOTH labels should be scored at high, not medium.
	p := ghc.PR{Number: 1, Additions: 100, Deletions: 50, Labels: []string{"risk:medium", "risk:high"}}
	files := []ghc.File{{Path: "x.go", Changes: 150}}
	s := Compute(p, files, nil, []string{"risk:high"}, []string{"risk:medium"}, "", nil)
	if s.Risk != cru.RiskHigh {
		t.Errorf("high should win over medium; got risk=%v", s.Risk)
	}
}

func TestCompute_MediumLabelMultiAccepted(t *testing.T) {
	p := ghc.PR{Number: 1, Additions: 100, Deletions: 50, Labels: []string{"needs-care"}}
	files := []ghc.File{{Path: "x.go", Changes: 150}}
	s := Compute(p, files, nil, nil, []string{"risk:medium", "needs-care", "watch"}, "", nil)
	if s.Risk != cru.RiskMedium {
		t.Errorf("expected medium risk via multi-label set, got %v", s.Risk)
	}
}

func TestCompute_NoMatchStaysLow(t *testing.T) {
	p := ghc.PR{Number: 1, Additions: 100, Deletions: 50, Labels: []string{"bug"}}
	files := []ghc.File{{Path: "x.go", Changes: 150}}
	s := Compute(p, files, nil, []string{"risk:high"}, []string{"risk:medium"}, "", nil)
	if s.Risk != cru.RiskLow {
		t.Errorf("expected low risk (no labels match), got %v", s.Risk)
	}
}
