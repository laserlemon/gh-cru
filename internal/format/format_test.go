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
	Lines int     `json:"lines"`
	Share float64 `json:"share"`
	CRU   float64 `json:"cru"`
	IsYou bool    `json:"is_you"`
}

type jsonOut struct {
	Repo           string  `json:"repo"`
	Number         int     `json:"number"`
	Title          string  `json:"title"`
	Lines          int     `json:"lines"`
	SizeLabel      string  `json:"size_label"`
	SizeFactor     float64 `json:"size_factor"`
	RiskLabel      string  `json:"risk_label"`
	RiskMultiplier float64 `json:"risk_multiplier"`
	BaseCRU        float64 `json:"base_cru"`
	Ownership      struct {
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

// TestJSONShapeOnlyRendersUsedFields verifies the schema carries exactly
// the fields the human output draws and omits the PR metadata it doesn't
// use (author, state, additions, deletions, files, my_identities).
func TestJSONShapeOnlyRendersUsedFields(t *testing.T) {
	owners := []score.Ownership{
		{Owner: "@acme/eng", OwnedLOC: 100, Share: 1.0, Score: 2.0},
	}
	s := mkScore(100, true, owners, 0, "laserlemon", []string{"@laserlemon"})

	var buf bytes.Buffer
	if err := JSON(&buf, "acme/web", s); err != nil {
		t.Fatalf("JSON render: %v", err)
	}
	body := buf.String()
	for _, gone := range []string{
		`"author"`, `"state"`, `"additions"`, `"deletions"`, `"files"`,
		`"my_identities"`, `"normal_cru"`, `"total_cru"`, `"requested_cru"`,
		`"ownership_share"`, `"owned_loc"`, `"unowned_loc"`,
	} {
		if strings.Contains(body, gone) {
			t.Errorf("JSON should not contain %s; got:\n%s", gone, body)
		}
	}
	for _, want := range []string{
		`"base_cru"`, `"ownership"`, `"share"`, `"cru"`, `"lines"`,
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
	if err := JSON(&buf, "acme/web", s); err != nil {
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
	if err := JSON(&buf, "acme/web", s); err != nil {
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
	if err := JSON(&buf, "acme/web", s); err != nil {
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
	if err := JSON(&buf, "acme/web", s); err != nil {
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
	if got.SizeFactor != 0.123457 {
		t.Errorf("size_factor = %v, want 0.123457", got.SizeFactor)
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
// row both set is_you=true, that team names use the slug form (e.g.
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
	if err := JSON(&buf, "acme/web", s); err != nil {
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
		t.Errorf("laserlemon: type=%q is_you=%v; want user/true", direct.Type, direct.IsYou)
	}

	team, ok := byName["acme/justice-league"]
	if !ok {
		t.Fatalf("missing @acme/justice-league row (looked up bare \"acme/justice-league\")")
	}
	if team.Type != "team" || !team.IsYou {
		t.Errorf("acme/justice-league: type=%q is_you=%v; want team/true", team.Type, team.IsYou)
	}

	other, ok := byName["acme/other-team"]
	if !ok {
		t.Fatalf("missing @acme/other-team row")
	}
	if other.Type != "team" || other.IsYou {
		t.Errorf("acme/other-team: type=%q is_you=%v; want team/false", other.Type, other.IsYou)
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
	if err := JSON(&buf, "acme/web", s); err != nil {
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
	if err := JSON(&buf, "acme/web", s); err != nil {
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
	if err := JSON(&buf, "acme/web", s); err != nil {
		t.Fatalf("JSON render: %v", err)
	}
	if strings.Contains(buf.String(), `"you":`) {
		t.Errorf("JSON contains you block when MyLogin is empty; got:\n%s", buf.String())
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
