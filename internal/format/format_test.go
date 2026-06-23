package format

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/cli/go-gh/v2/pkg/term"

	"github.com/laserlemon/cru"
	ghc "github.com/laserlemon/gh-cru/internal/gh"
	"github.com/laserlemon/gh-cru/internal/score"
)

// noColorTerm returns a term.Term suitable for tests: relies on NO_COLOR
// being set in TestMain to disable color globally so assertions can
// string-match against plain output.
func noColorTerm() term.Term {
	return term.FromEnv()
}

// compactTerm returns a zero-value term.Term, whose IsTerminalOutput() and
// IsColorEnabled() are both false. JSON() uses these to choose its output
// mode, so this forces the compact NDJSON path deterministically (the
// shape the JSON-shape assertions decode), independent of whether the test
// process's stdout happens to be a TTY or GH_FORCE_TTY is set.
func compactTerm() term.Term {
	return term.Term{}
}

func TestMain(m *testing.M) {
	os.Setenv("NO_COLOR", "1")
	os.Exit(m.Run())
}

// mkScore builds a minimal but complete PRScore for renderer tests.
// Avoids round-tripping through score.Compute so we can craft edge cases
// (e.g. arbitrary owner overlap, specific marker scenarios) directly.
func mkScore(loc int, hasCodeowners bool, owners []score.Ownership, unowned int, myLogin string, myIdentities []string) score.PRScore {
	ownerMap := make(map[string]score.Ownership, len(owners))
	order := make([]string, 0, len(owners))
	for _, o := range owners {
		ownerMap[o.Owner] = o
		order = append(order, o.Owner)
	}
	s := score.PRScore{
		PR: ghc.PR{
			Number:    42,
			Title:     "test",
			Author:    "tester",
			State:     "open",
			Additions: loc / 2,
			Deletions: loc - loc/2,
		},
		LOC:            loc,
		Size:           cru.CalculateSize(loc),
		Risk:           cru.RiskLow,
		HasCodeowners:  hasCodeowners,
		OwnershipMap:   ownerMap,
		OwnerOrder:     order,
		UnownedChanges: unowned,
		MyLogin:        myLogin,
		MyIdentities:   myIdentities,
		TeamsResolved:  true,
	}
	return s
}

// stripANSI removes ANSI SGR escape sequences so tests can assert on
// plain text content regardless of color state.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) {
				c := s[j]
				j++
				if c >= '@' && c <= '~' {
					break
				}
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// --- JSON shape -----------------------------------------------------------

// jsonRow mirrors the shared {loc, share, cru} shape used by every
// ownership row (named owners and the unowned/all/you summaries).
type jsonRow struct {
	Lines int     `json:"lines"`
	Share float64 `json:"share"`
	CRU   float64 `json:"cru"`
}

type jsonOwner struct {
	Name  string  `json:"name"`
	Type  string  `json:"type"`
	IsYou bool    `json:"isYou"`
	Lines int     `json:"lines"`
	Share float64 `json:"share"`
	CRU   float64 `json:"cru"`
}

type jsonOut struct {
	Repository struct {
		Name          string `json:"name"`
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	PullRequest struct {
		Additions int    `json:"additions"`
		Deletions int    `json:"deletions"`
		Number    int    `json:"number"`
		State     string `json:"state"`
		Title     string `json:"title"`
		URL       string `json:"url"`
	} `json:"pullRequest"`
	Size struct {
		Label  string  `json:"label"`
		Factor float64 `json:"factor"`
		Lines  int     `json:"lines"`
	} `json:"size"`
	Risk struct {
		Label      string  `json:"label"`
		Multiplier float64 `json:"multiplier"`
	} `json:"risk"`
	BaseCRU   float64 `json:"baseCru"`
	Ownership struct {
		Owners  []jsonOwner `json:"owners"`
		Unowned jsonRow     `json:"unowned"`
		All     jsonRow     `json:"all"`
		You     *jsonRow    `json:"you"`
	} `json:"ownership"`
}

func decodeJSON(t *testing.T, buf *bytes.Buffer) jsonOut {
	t.Helper()
	var got jsonOut
	if err := json.NewDecoder(buf).Decode(&got); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	return got
}

// TestJSONShapeRendersExpectedFields pins the schema contract: the two
// borrowed gh objects (repository, pullRequest) carry their canonical
// fields, the CRU grade lives in size/risk/baseCru/ownership, and the
// fields we deliberately don't surface (author, and any snake_case
// internal spellings) stay out.
func TestJSONShapeRendersExpectedFields(t *testing.T) {
	owners := []score.Ownership{
		{Owner: "@acme/eng", OwnedLOC: 100, Share: 1.0, Score: 2.0},
	}
	s := mkScore(100, true, owners, 0, "laserlemon", []string{"@laserlemon"})

	var buf bytes.Buffer
	if err := JSON(&buf, "acme/web", s, compactTerm(), nil); err != nil {
		t.Fatalf("JSON render: %v", err)
	}
	body := buf.String()
	// author isn't surfaced; neither are the snake_case internal names a
	// naive marshal might leak.
	for _, gone := range []string{
		`"author"`, `"my_identities"`, `"normal_cru"`, `"total_cru"`,
		`"requested_cru"`, `"ownership_share"`, `"owned_loc"`, `"unowned_loc"`,
		`"sizeLabel"`, `"sizeFactor"`, `"riskLabel"`, `"riskMultiplier"`,
	} {
		if strings.Contains(body, gone) {
			t.Errorf("JSON should not contain %s; got:\n%s", gone, body)
		}
	}
	// The borrowed objects and the CRU grade keys are all present.
	for _, want := range []string{
		`"repository"`, `"pullRequest"`, `"additions"`, `"deletions"`,
		`"state"`, `"url"`, `"size"`, `"risk"`, `"baseCru"`, `"ownership"`,
		`"share"`, `"cru"`, `"lines"`, `"factor"`, `"multiplier"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("JSON missing expected key %s; got:\n%s", want, body)
		}
	}
}

// TestJSONShapeUnownedIsSummaryRow verifies the synthetic unowned owner is
// surfaced as ownership.unowned (a {loc,share,cru} summary), NOT as an
// entry in ownership.owners[].
func TestJSONShapeUnownedIsSummaryRow(t *testing.T) {
	owners := []score.Ownership{
		{Owner: "@acme/eng", OwnedLOC: 60, Share: 0.6, Score: 1.2},
		{Owner: score.UnownedOwnerLabel, OwnedLOC: 40, Share: 0.4, Score: 0.8},
	}
	s := mkScore(100, true, owners, 40, "", nil)

	var buf bytes.Buffer
	if err := JSON(&buf, "acme/web", s, compactTerm(), nil); err != nil {
		t.Fatalf("JSON render: %v", err)
	}
	got := decodeJSON(t, &buf)

	// owners[] holds only the named owner, not the unowned row.
	if len(got.Ownership.Owners) != 1 {
		t.Fatalf("ownership.owners size = %d, want 1 (unowned excluded)", len(got.Ownership.Owners))
	}
	if got.Ownership.Owners[0].Name != "acme/eng" {
		t.Errorf("owners[0].name = %q, want acme/eng", got.Ownership.Owners[0].Name)
	}
	// unowned surfaces as its own summary row.
	if got.Ownership.Unowned.Lines != 40 {
		t.Errorf("ownership.unowned.lines = %d, want 40", got.Ownership.Unowned.Lines)
	}
	if got.Ownership.Unowned.CRU != 0.8 {
		t.Errorf("ownership.unowned.cru = %v, want 0.8", got.Ownership.Unowned.CRU)
	}
}

// TestJSONShapeUnownedZeroedWhenAbsent verifies ownership.unowned is always
// present, zeroed when the PR has no unowned LOC.
func TestJSONShapeUnownedZeroedWhenAbsent(t *testing.T) {
	owners := []score.Ownership{
		{Owner: "@acme/eng", OwnedLOC: 100, Share: 1.0, Score: 2.0},
	}
	s := mkScore(100, true, owners, 0, "", nil)

	var buf bytes.Buffer
	if err := JSON(&buf, "acme/web", s, compactTerm(), nil); err != nil {
		t.Fatalf("JSON render: %v", err)
	}
	body := buf.String()
	if !strings.Contains(body, `"unowned":{"lines":0,"share":0.000000,"cru":0.000000}`) {
		t.Errorf("ownership.unowned should be present and zeroed; got:\n%s", body)
	}
}

// TestJSONShapeAllSumsEveryRow verifies ownership.all is the sum across
// every row including unowned (the team's total review burden), and can
// exceed the PR's LOC / 100%% under CODEOWNERS overlap.
func TestJSONShapeAllSumsEveryRow(t *testing.T) {
	// Two teams overlap (each owns the same 80 LOC) → all.loc = 160 > 80.
	owners := []score.Ownership{
		{Owner: "@acme/auth", OwnedLOC: 80, Share: 1.0, Score: 2.0},
		{Owner: "@acme/web", OwnedLOC: 80, Share: 1.0, Score: 2.0},
	}
	s := mkScore(80, true, owners, 0, "", nil)

	var buf bytes.Buffer
	if err := JSON(&buf, "acme/web", s, compactTerm(), nil); err != nil {
		t.Fatalf("JSON render: %v", err)
	}
	got := decodeJSON(t, &buf)
	if got.Ownership.All.Lines != 160 {
		t.Errorf("ownership.all.lines = %d, want 160 (overlap sum)", got.Ownership.All.Lines)
	}
	if got.Ownership.All.CRU != 4.0 {
		t.Errorf("ownership.all.cru = %v, want 4.0", got.Ownership.All.CRU)
	}
}

// TestAllOwnershipAgreesAcrossSurfaces is the regression guard for the
// "All ownership" total being computed from one source. The human and
// JSON renderers MUST report identical lines/share/CRU for that summary
// row; before unification each summed independently (human ranged
// OwnershipMap, JSON ranged SortedOwners), an unenforced invariant that
// could silently diverge. An overlap fixture (shares sum past 1.0, lines
// past the PR's own LOC) makes the totals non-trivial so a regression
// can't hide behind a 100%/1.0 coincidence.
func TestAllOwnershipAgreesAcrossSurfaces(t *testing.T) {
	owners := []score.Ownership{
		{Owner: "@acme/auth", OwnedLOC: 80, Share: 1.0, Score: 2.0},
		{Owner: "@acme/web", OwnedLOC: 60, Share: 0.75, Score: 1.5},
		{Owner: score.UnownedOwnerLabel, OwnedLOC: 20, Share: 0.25, Score: 0.5},
	}
	s := mkScore(80, true, owners, 20, "", nil)

	// JSON side: the canonical numbers.
	var jbuf bytes.Buffer
	if err := JSON(&jbuf, "acme/web", s, compactTerm(), nil); err != nil {
		t.Fatalf("JSON render: %v", err)
	}
	got := decodeJSON(t, &jbuf)
	all := got.Ownership.All

	// Sanity: the fixture is genuinely non-trivial (overlap present).
	if all.Lines != 160 {
		t.Fatalf("fixture all.lines = %d, want 160 (overlap sum)", all.Lines)
	}

	// Human side: the same total must appear, formatted exactly as
	// addSummaryRow writes it (%d lines, %.1f%% share, %.3f CRU).
	var hbuf bytes.Buffer
	Human(&hbuf, "acme/web", s, noColorTerm())
	out := stripANSI(hbuf.String())

	wantLines := fmt.Sprintf("%d", all.Lines)
	wantShare := fmt.Sprintf("%.1f%%", all.Share*100)
	wantCRU := fmt.Sprintf("%.3f", all.CRU)
	for _, needle := range []string{wantLines, wantShare, wantCRU} {
		if !strings.Contains(out, needle) {
			t.Errorf("human All-ownership total missing %q (JSON reports lines=%d share=%v cru=%v):\n%s",
				needle, all.Lines, all.Share, all.CRU, out)
		}
	}
}

// TestJSONShapeRoundsFloats verifies that every float64 field in the
// JSON output is rounded to ≤ 6 decimal places. Catches regressions if
// someone adds a new float field and forgets to pass it through round6.
// The fixture deliberately uses 7-decimal inputs so unrounded values
// would visibly fail the regex.
func TestJSONShapeRoundsFloats(t *testing.T) {
	owners := []score.Ownership{
		// 7 decimals; round6 → 0.3333333 → 0.333333.
		{Owner: "@laserlemon", OwnedLOC: 1, Share: 0.3333333, Score: 0.6666666},
		{Owner: "@acme/eng", OwnedLOC: 2, Share: 0.6666666, Score: 1.3333333},
	}
	s := mkScore(3, true, owners, 0, "laserlemon", []string{"@laserlemon"})
	s.Size = cru.Size(0.1234567)
	s.MyOwnedLOC = 1
	s.MyShare = 0.3333333
	s.MyCRU = 0.7777777

	var buf bytes.Buffer
	if err := JSON(&buf, "acme/web", s, compactTerm(), nil); err != nil {
		t.Fatalf("JSON render: %v", err)
	}

	// Any field with > 6 decimal digits after a `.` violates the contract.
	body := buf.String()
	bad := regexp.MustCompile(`\d+\.\d{7,}`)
	if m := bad.FindAllString(body, -1); len(m) > 0 {
		t.Errorf("JSON contains floats with > 6 decimal places: %v\nbody:\n%s", m, body)
	}

	// Spot-check known fields round-trip to their rounded values.
	got := decodeJSON(t, &buf)
	// repository is gh's nested object, split from the "acme/web" arg.
	if got.Repository.Name != "web" {
		t.Errorf("repository.name = %q, want web", got.Repository.Name)
	}
	if got.Repository.NameWithOwner != "acme/web" {
		t.Errorf("repository.nameWithOwner = %q, want acme/web", got.Repository.NameWithOwner)
	}
	if got.Size.Factor != 0.123457 {
		t.Errorf("size.factor = %v, want 0.123457", got.Size.Factor)
	}
	if got.Ownership.You == nil {
		t.Fatalf("missing ownership.you block")
	}
	if got.Ownership.You.Share != 0.333333 {
		t.Errorf("ownership.you.share = %v, want 0.333333", got.Ownership.You.Share)
	}
	if got.Ownership.You.CRU != 0.777778 {
		t.Errorf("ownership.you.cru = %v, want 0.777778", got.Ownership.You.CRU)
	}
}

// TestJSONShapeOwnerTypes verifies the per-owner `type` field distinguishes
// user / team, that the user's direct @login row AND any team-membership
// row both set isYou=true, that team names use the slug form (e.g.
// acme/justice-league) and user names use the bare login (no leading "@").
func TestJSONShapeOwnerTypes(t *testing.T) {
	owners := []score.Ownership{
		{Owner: "@laserlemon", OwnedLOC: 30, Share: 0.3, Score: 0.6},
		{Owner: "@acme/justice-league", OwnedLOC: 50, Share: 0.5, Score: 1.0},
		{Owner: "@acme/other-team", OwnedLOC: 20, Share: 0.2, Score: 0.4},
	}
	// User is laserlemon and is on @acme/justice-league.
	s := mkScore(100, true, owners, 0, "laserlemon",
		[]string{"@laserlemon", "@acme/justice-league"})

	var buf bytes.Buffer
	if err := JSON(&buf, "acme/web", s, compactTerm(), nil); err != nil {
		t.Fatalf("JSON render: %v", err)
	}
	got := decodeJSON(t, &buf)

	byName := map[string]jsonOwner{}
	for _, o := range got.Ownership.Owners {
		if o.Type == "unowned" {
			t.Errorf("owner %q has retired type \"unowned\"", o.Name)
		}
		byName[o.Name] = o
	}

	direct, ok := byName["laserlemon"]
	if !ok {
		t.Fatalf("missing @laserlemon row (looked up bare \"laserlemon\")")
	}
	if direct.Type != "user" || !direct.IsYou {
		t.Errorf("laserlemon: type=%q isYou=%v; want user/true", direct.Type, direct.IsYou)
	}

	team, ok := byName["acme/justice-league"]
	if !ok {
		t.Fatalf("missing @acme/justice-league row (looked up bare \"acme/justice-league\")")
	}
	if team.Type != "team" || !team.IsYou {
		t.Errorf("acme/justice-league: type=%q isYou=%v; want team/true", team.Type, team.IsYou)
	}

	other, ok := byName["acme/other-team"]
	if !ok {
		t.Fatalf("missing @acme/other-team row")
	}
	if other.Type != "team" || other.IsYou {
		t.Errorf("acme/other-team: type=%q isYou=%v; want team/false", other.Type, other.IsYou)
	}
}

// TestJSONShapeYouShownWhenKnown verifies ownership.you surfaces as a pure
// {loc,share,cru} row (no login field) whenever the identity is known,
// even at zero stake.
func TestJSONShapeYouShownWhenKnown(t *testing.T) {
	owners := []score.Ownership{
		{Owner: "@laserlemon", OwnedLOC: 40, Share: 0.4, Score: 0.8},
	}
	s := mkScore(100, true, owners, 0, "laserlemon", []string{"@laserlemon"})
	s.MyOwnedLOC = 40
	s.MyShare = 0.4
	s.MyCRU = 0.8

	var buf bytes.Buffer
	if err := JSON(&buf, "acme/web", s, compactTerm(), nil); err != nil {
		t.Fatalf("JSON render: %v", err)
	}
	body := buf.String()
	if strings.Contains(body, `"login"`) {
		t.Errorf("ownership.you should not carry a login field; got:\n%s", body)
	}
	got := decodeJSON(t, &buf)
	if got.Ownership.You == nil {
		t.Fatalf("missing ownership.you block; want populated when identity is known")
	}
	if got.Ownership.You.Lines != 40 || got.Ownership.You.Share != 0.4 {
		t.Errorf("ownership.you = %+v, want lines=40 share=0.4", *got.Ownership.You)
	}
}

// TestJSONShapeYouZeroedAtNoStake verifies ownership.you is present (not
// omitted) when the identity is known but owns nothing, matching the human
// output's always-shown "Your ownership" row.
func TestJSONShapeYouZeroedAtNoStake(t *testing.T) {
	owners := []score.Ownership{
		{Owner: "@acme/eng", OwnedLOC: 100, Share: 1.0, Score: 2.0},
	}
	s := mkScore(100, true, owners, 0, "laserlemon", []string{"@laserlemon"})
	s.MyOwnedLOC = 0

	var buf bytes.Buffer
	if err := JSON(&buf, "acme/web", s, compactTerm(), nil); err != nil {
		t.Fatalf("JSON render: %v", err)
	}
	got := decodeJSON(t, &buf)
	if got.Ownership.You == nil {
		t.Fatalf("ownership.you should be present at zero stake when identity is known")
	}
	if got.Ownership.You.Lines != 0 || got.Ownership.You.CRU != 0 {
		t.Errorf("ownership.you = %+v, want zeroed", *got.Ownership.You)
	}
}

// TestJSONShapeNoYouWhenAnon verifies ownership.you is omitted entirely
// (key and all) when no identity was resolved.
func TestJSONShapeNoYouWhenAnon(t *testing.T) {
	owners := []score.Ownership{
		{Owner: "@acme/eng", OwnedLOC: 100, Share: 1.0, Score: 2.0},
	}
	s := mkScore(100, true, owners, 0, "", nil)

	var buf bytes.Buffer
	if err := JSON(&buf, "acme/web", s, compactTerm(), nil); err != nil {
		t.Fatalf("JSON render: %v", err)
	}
	if strings.Contains(buf.String(), `"you":`) {
		t.Errorf("JSON contains you block when MyLogin is empty; got:\n%s", buf.String())
	}
}

// --- --skip-ownership -----------------------------------------------------

// TestHumanSkipOwnershipDropsTable verifies that under --skip-ownership
// (OwnershipSkipped=true) the Human output ends on the Base CRU line and
// renders NO ownership table: no CODE OWNER header, no All ownership row,
// no Unowned row. The measurement degrades cleanly to size × risk.
func TestHumanSkipOwnershipDropsTable(t *testing.T) {
	// Even with a populated ownership map (as Compute would leave from a
	// prior path), the skip flag must suppress the whole table.
	owners := []score.Ownership{
		{Owner: score.UnownedOwnerLabel, OwnedLOC: 240, Share: 1.0, Score: 12.0},
	}
	s := mkScore(240, false, owners, 240, "", nil)
	s.OwnershipSkipped = true

	var buf bytes.Buffer
	Human(&buf, "acme/payments", s, noColorTerm())
	out := stripANSI(buf.String())

	// Formula block IS present (the user still gets the Base CRU).
	for _, want := range []string{"Size", "Risk", "Base", "CRU"} {
		if !strings.Contains(out, want) {
			t.Errorf("skip-ownership output missing formula label %q:\n%s", want, out)
		}
	}
	// Ownership table is entirely absent.
	for _, gone := range []string{"CODE OWNER", "All ownership", "Unowned", "Your ownership"} {
		if strings.Contains(out, gone) {
			t.Errorf("skip-ownership output should not contain %q:\n%s", gone, out)
		}
	}
}

// TestJSONSkipOwnershipOmitsObject verifies that under --skip-ownership
// the JSON omits the `ownership` object entirely (rather than emitting a
// fabricated 100%-unowned block), while still carrying baseCru.
func TestJSONSkipOwnershipOmitsObject(t *testing.T) {
	owners := []score.Ownership{
		{Owner: score.UnownedOwnerLabel, OwnedLOC: 240, Share: 1.0, Score: 12.0},
	}
	s := mkScore(240, false, owners, 240, "", nil)
	s.OwnershipSkipped = true

	var buf bytes.Buffer
	if err := JSON(&buf, "acme/payments", s, compactTerm(), nil); err != nil {
		t.Fatalf("JSON render: %v", err)
	}
	body := buf.String()
	if strings.Contains(body, `"ownership"`) {
		t.Errorf("skip-ownership JSON should omit the ownership object; got:\n%s", body)
	}
	// baseCru is still present: the measurement degrades to size × risk.
	if !strings.Contains(body, `"baseCru"`) {
		t.Errorf("skip-ownership JSON should still carry baseCru; got:\n%s", body)
	}
	// And it must remain valid JSON.
	if _, err := json.Marshal(json.RawMessage(strings.TrimSpace(body))); err != nil {
		t.Errorf("skip-ownership JSON is not valid: %v\n%s", err, body)
	}
}

// --- table marker selection ----------------------------------------------

// TestHumanMarkers verifies the marker glyphs render on the right rows:
// the three data-row markers (=, *, •) keyed to owner relationship, and
// the three summary-row markers (~, +, >). End-to-end through Human()
// with color disabled and TTY false so we can string-match against plain
// (tab-separated) output.
func TestHumanMarkers(t *testing.T) {
	owners := []score.Ownership{
		{Owner: "@laserlemon", OwnedLOC: 25, Share: 0.25, Score: 0.5},    // direct → =
		{Owner: "@acme/big-orca", OwnedLOC: 25, Share: 0.25, Score: 0.5}, // team → *
		{Owner: "@acme/web-team", OwnedLOC: 25, Share: 0.25, Score: 0.5}, // other → •
		{Owner: score.UnownedOwnerLabel, OwnedLOC: 25, Share: 0.25, Score: 0.5},
	}
	s := mkScore(100, true, owners, 25, "laserlemon",
		[]string{"@laserlemon", "@acme/big-orca"})
	s.MyOwnedLOC = 50
	s.MyShare = 0.5
	s.MyCRU = 1.0

	var buf bytes.Buffer
	Human(&buf, "acme/web", s, noColorTerm())

	out := stripANSI(buf.String())
	// Each marker should appear next to its row label. In non-TTY mode the
	// tableprinter uses tab separators, so assert with a tab between the
	// marker and the row label.
	tests := []struct {
		marker string
		label  string
	}{
		{"=", "laserlemon"},     // direct @login data row
		{"*", "acme/big-orca"},  // team-you data row
		{"•", "acme/web-team"},  // someone-else data row
		{"~", "Unowned"},        // unowned summary row
		{"+", "All ownership"},  // all-ownership summary row
		{">", "Your ownership"}, // your-ownership summary row
	}
	for _, tc := range tests {
		needle := tc.marker + "	" + tc.label
		if !strings.Contains(out, needle) {
			t.Errorf("missing %q row in output:\n%s", needle, out)
		}
	}
}

// TestHumanFormulaBlock verifies the Size/Risk/Base formula block renders
// with the grade, factor value, and "CRU" unit. The base value is the
// owner-agnostic Size × Risk score.
func TestHumanFormulaBlock(t *testing.T) {
	owners := []score.Ownership{
		{Owner: score.UnownedOwnerLabel, OwnedLOC: 50, Share: 1.0, Score: 1.5},
	}
	s := mkScore(50, false, owners, 50, "", nil)

	var buf bytes.Buffer
	Human(&buf, "acme/web", s, noColorTerm())

	out := stripANSI(buf.String())
	for _, needle := range []string{"Size", "Risk", "Base", "CRU", "lines"} {
		if !strings.Contains(out, needle) {
			t.Errorf("formula block missing %q in output:\n%s", needle, out)
		}
	}
	// The base value (Size × Risk) should be present, formatted to 3dp.
	base := fmt.Sprintf("%.3f", s.CRU())
	if !strings.Contains(out, base) {
		t.Errorf("formula block missing base value %s in output:\n%s", base, out)
	}
	// The retired header labels must be gone.
	for _, gone := range []string{"Normal CRU", "Total CRU", "Your CRU", "Size label", "Size factor", "Risk multiplier"} {
		if strings.Contains(out, gone) {
			t.Errorf("output still has retired label %q:\n%s", gone, out)
		}
	}
}

// TestHumanSummaryRows verifies the three computed-total rows append to the
// owner table: ~ Unowned (when unowned LOC exists), + All ownership
// (always), and > Your ownership (only when the user owns something).
func TestHumanSummaryRows(t *testing.T) {
	owners := []score.Ownership{
		{Owner: "@laserlemon", OwnedLOC: 40, Share: 0.4, Score: 0.8},
		{Owner: "@acme/eng", OwnedLOC: 40, Share: 0.4, Score: 0.8},
		{Owner: score.UnownedOwnerLabel, OwnedLOC: 20, Share: 0.2, Score: 0.4},
	}
	s := mkScore(100, true, owners, 20, "laserlemon", []string{"@laserlemon"})
	s.MyOwnedLOC = 40
	s.MyShare = 0.4
	s.MyCRU = 0.8

	var buf bytes.Buffer
	Human(&buf, "acme/web", s, noColorTerm())

	out := stripANSI(buf.String())
	for _, needle := range []string{"Unowned", "All ownership", "Your ownership"} {
		if !strings.Contains(out, needle) {
			t.Errorf("missing summary row %q in output:\n%s", needle, out)
		}
	}
	// SHARE renders as a percentage now, not a bare fraction.
	if !strings.Contains(out, "%") {
		t.Errorf("SHARE column should be a percentage:\n%s", out)
	}
}

// TestHumanYourOwnershipShownWhenKnown verifies the > Your ownership row
// renders whenever we know who you are, even at zero stake, so the value
// is explicit rather than silently missing. It is absent only when no
// identity was resolved (unauthenticated / no MyLogin).
func TestHumanYourOwnershipShownWhenKnown(t *testing.T) {
	owners := []score.Ownership{
		{Owner: "@acme/eng", OwnedLOC: 100, Share: 1.0, Score: 2.0},
	}

	// Known identity but zero stake: the row should still render.
	known := mkScore(100, true, owners, 0, "laserlemon", []string{"@laserlemon"})
	known.MyOwnedLOC = 0
	var buf bytes.Buffer
	Human(&buf, "acme/web", known, noColorTerm())
	if out := stripANSI(buf.String()); !strings.Contains(out, "Your ownership") {
		t.Errorf("Your ownership row should render at zero stake when identity is known:\n%s", out)
	}

	// No identity at all: the row should be absent.
	anon := mkScore(100, true, owners, 0, "", nil)
	var buf2 bytes.Buffer
	Human(&buf2, "acme/web", anon, noColorTerm())
	if out := stripANSI(buf2.String()); strings.Contains(out, "Your ownership") {
		t.Errorf("Your ownership row should be absent when no identity is known:\n%s", out)
	}
}

// TestDisplayOwnerStripsAt verifies the @ prefix is removed for display.
func TestDisplayOwnerStripsAt(t *testing.T) {
	cases := map[string]string{
		"@laserlemon": "laserlemon",
		"@acme/eng":   "acme/eng",
		"laserlemon":  "laserlemon", // no @, unchanged
		"":            "",
		"@@double":    "@double", // only first @ stripped
	}
	for in, want := range cases {
		if got := displayOwner(in); got != want {
			t.Errorf("displayOwner(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestJSONArrayEmitsArray verifies the list path (JSONArray) wraps the
// whole batch in a single JSON array, mirroring `gh pr list --json`, and
// that each element carries the same per-PR shape JSON() emits for the
// view path. This is the object-vs-array split: view → object, list → array.
func TestJSONArrayEmitsArray(t *testing.T) {
	mk := func(repo string, loc int, login string) Item {
		s := mkScore(loc, true, []score.Ownership{
			{Owner: "@" + login, OwnedLOC: loc, Share: 1.0, Score: 2.0},
		}, 0, login, []string{"@" + login})
		return Item{Repo: repo, Score: s}
	}
	items := []Item{
		mk("acme/web", 100, "alice"),
		mk("acme/api", 250, "bob"),
	}

	var buf bytes.Buffer
	if err := JSONArray(&buf, items, compactTerm(), nil); err != nil {
		t.Fatalf("JSONArray: %v", err)
	}

	// Decodes as a JSON array of the per-PR shape (not NDJSON, not an object).
	var got []jsonOut
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode JSON array: %v\noutput: %s", err, buf.String())
	}
	if len(got) != 2 {
		t.Fatalf("got %d elements, want 2:\n%s", len(got), buf.String())
	}
	if got[0].Repository.NameWithOwner != "acme/web" || got[0].Repository.Name != "web" {
		t.Errorf("element 0 repository = %+v, want {web, acme/web}", got[0].Repository)
	}
	if got[1].Repository.NameWithOwner != "acme/api" || got[1].Repository.Name != "api" {
		t.Errorf("element 1 repository = %+v, want {api, acme/api}", got[1].Repository)
	}
	// Order is preserved (the batch renders in gh's returned order).
	if got[0].Size.Lines != 100 || got[1].Size.Lines != 250 {
		t.Errorf("lines = [%d, %d], want [100, 250] (order preserved)", got[0].Size.Lines, got[1].Size.Lines)
	}
}

// TestJSONArrayEmptyIsBrackets verifies an empty/nil batch emits `[]`, not
// `null`, so a downstream `jq` consumer always gets a valid (iterable) JSON
// array, matching `gh pr list --json` on a no-match result.
func TestJSONArrayEmptyIsBrackets(t *testing.T) {
	for _, items := range [][]Item{nil, {}} {
		var buf bytes.Buffer
		if err := JSONArray(&buf, items, compactTerm(), nil); err != nil {
			t.Fatalf("JSONArray(empty): %v", err)
		}
		if got := strings.TrimSpace(buf.String()); got != "[]" {
			t.Errorf("empty batch = %q, want %q", got, "[]")
		}
	}
}

// TestJSONArrayCompactIsSingleLine verifies the piped (non-TTY) array is a
// single line with no internal newlines: the machine-parseable stream
// contract. The whole array is one `[...]\n`, distinct from NDJSON's
// one-object-per-line shape on the view path.
func TestJSONArrayCompactIsSingleLine(t *testing.T) {
	items := []Item{
		{Repo: "acme/web", Score: mkScore(100, true, []score.Ownership{
			{Owner: "@acme/eng", OwnedLOC: 100, Share: 1.0, Score: 2.0},
		}, 0, "", nil)},
		{Repo: "acme/api", Score: mkScore(50, true, []score.Ownership{
			{Owner: "@acme/eng", OwnedLOC: 50, Share: 1.0, Score: 1.5},
		}, 0, "", nil)},
	}
	var buf bytes.Buffer
	if err := JSONArray(&buf, items, compactTerm(), nil); err != nil {
		t.Fatalf("JSONArray: %v", err)
	}
	out := strings.TrimRight(buf.String(), "\n")
	if strings.Contains(out, "\n") {
		t.Errorf("compact array should have no internal newlines:\n%q", out)
	}
	if !strings.HasPrefix(out, "[") || !strings.HasSuffix(out, "]") {
		t.Errorf("compact array should be bracket-wrapped, got:\n%q", out)
	}
}

// --- field selection ------------------------------------------------------

// TestValidateJSONFields covers the gh-style unknown-field error and that
// an empty (bare --json = full) selection always validates.
func TestValidateJSONFields(t *testing.T) {
	if err := ValidateJSONFields(nil); err != nil {
		t.Errorf("empty selection should be valid, got %v", err)
	}
	if err := ValidateJSONFields([]string{"size", "risk", "ownership"}); err != nil {
		t.Errorf("known fields should validate, got %v", err)
	}
	err := ValidateJSONFields([]string{"size", "bogus"})
	if err == nil {
		t.Fatal("unknown field should error")
	}
	if !strings.Contains(err.Error(), "bogus") || !strings.Contains(err.Error(), "available") {
		t.Errorf("error should name the bad field and list available; got: %v", err)
	}
}

// TestJSONFieldSelection verifies --json=<fields> emits exactly the
// requested top-level keys, always in canonical order regardless of the
// order they were requested.
func TestJSONFieldSelection(t *testing.T) {
	owners := []score.Ownership{
		{Owner: "@acme/eng", OwnedLOC: 100, Share: 1.0, Score: 2.0},
	}
	s := mkScore(100, true, owners, 0, "laserlemon", []string{"@laserlemon"})

	// Requested out of canonical order; output must still be canonical
	// (repository, pullRequest, size, risk, baseCru, ownership).
	var buf bytes.Buffer
	if err := JSON(&buf, "acme/web", s, compactTerm(), []string{"risk", "size"}); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	out := strings.TrimRight(buf.String(), "\n")
	if out != `{"size":{"label":"L","factor":1.807063,"lines":100},"risk":{"label":"low","multiplier":1.000000}}` {
		t.Errorf("subset selection wrong; got:\n%s", out)
	}

	// A single key.
	buf.Reset()
	if err := JSON(&buf, "acme/web", s, compactTerm(), []string{"baseCru"}); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	out = strings.TrimRight(buf.String(), "\n")
	if out != `{"baseCru":1.807063}` {
		t.Errorf("single-key selection wrong; got:\n%s", out)
	}
}

// TestJSONFieldSelectionSkipsAbsentKey verifies that selecting a valid
// key that's absent from this particular object (ownership under
// --skip-ownership) silently omits it rather than emitting null.
func TestJSONFieldSelectionSkipsAbsentKey(t *testing.T) {
	s := mkScore(100, true, nil, 0, "", nil)
	s.OwnershipSkipped = true

	var buf bytes.Buffer
	if err := JSON(&buf, "acme/web", s, compactTerm(), []string{"size", "ownership"}); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	out := strings.TrimRight(buf.String(), "\n")
	if strings.Contains(out, "ownership") {
		t.Errorf("ownership should be omitted under --skip-ownership, got:\n%s", out)
	}
	if !strings.Contains(out, `"size"`) {
		t.Errorf("size should be present, got:\n%s", out)
	}
}

// TestJSONArrayFieldSelection verifies field selection applies to every
// element of the list-path array.
func TestJSONArrayFieldSelection(t *testing.T) {
	items := []Item{
		{Repo: "acme/web", Score: mkScore(100, true, []score.Ownership{
			{Owner: "@acme/eng", OwnedLOC: 100, Share: 1.0, Score: 2.0},
		}, 0, "", nil)},
		{Repo: "acme/api", Score: mkScore(50, true, []score.Ownership{
			{Owner: "@acme/eng", OwnedLOC: 50, Share: 1.0, Score: 1.5},
		}, 0, "", nil)},
	}
	var buf bytes.Buffer
	if err := JSONArray(&buf, items, compactTerm(), []string{"repository"}); err != nil {
		t.Fatalf("JSONArray: %v", err)
	}
	out := strings.TrimRight(buf.String(), "\n")
	want := `[{"repository":{"name":"web","nameWithOwner":"acme/web"}},{"repository":{"name":"api","nameWithOwner":"acme/api"}}]`
	if out != want {
		t.Errorf("array subset selection wrong;\n got: %s\nwant: %s", out, want)
	}
}
