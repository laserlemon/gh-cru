package format

import (
	"bytes"
	"encoding/json"
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

// TestJSONShapeIncludesUnowned verifies that the JSON output includes
// the synthetic unowned owner row with name=null and type="unowned",
// since the JSON shape is a public contract for downstream scripts.
func TestJSONShapeIncludesUnowned(t *testing.T) {
	owners := []score.Ownership{
		{Owner: "@acme/eng", OwnedLOC: 60, Share: 0.6, Score: 1.2},
		{Owner: score.UnownedOwnerLabel, OwnedLOC: 40, Share: 0.4, Score: 0.8},
	}
	s := mkScore(100, true, owners, 40, "", nil)

	var buf bytes.Buffer
	if err := JSON(&buf, "acme/web", s); err != nil {
		t.Fatalf("JSON render: %v", err)
	}

	var got struct {
		Owners []struct {
			Name           *string `json:"name"`
			Type           string  `json:"type"`
			OwnedLOC       int     `json:"owned_loc"`
			OwnershipShare float64 `json:"ownership_share"`
			RequestedCRU   float64 `json:"requested_cru"`
			IsYou          bool    `json:"is_you"`
		} `json:"owners"`
		UnownedLOC int `json:"unowned_loc"`
	}
	if err := json.NewDecoder(&buf).Decode(&got); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}

	if len(got.Owners) != 2 {
		t.Fatalf("Owners size = %d, want 2", len(got.Owners))
	}
	if got.UnownedLOC != 40 {
		t.Errorf("UnownedLOC = %d, want 40", got.UnownedLOC)
	}
	// The unowned row should have Type=="unowned", Name==nil, IsYou=false.
	var found bool
	for _, o := range got.Owners {
		if o.Type == "unowned" {
			found = true
			if o.Name != nil {
				t.Errorf("unowned row has Name=%q; want null", *o.Name)
			}
			if o.IsYou {
				t.Errorf("unowned row has IsYou=true; want false")
			}
		}
	}
	if !found {
		t.Errorf("no unowned row found in JSON output")
	}
}

// TestJSONShapeUsesOwnershipShare verifies the field is named
// ownership_share (not ownership_factor; that was the old name and
// downstream consumers might still query it).
func TestJSONShapeUsesOwnershipShare(t *testing.T) {
	owners := []score.Ownership{
		{Owner: "@acme/eng", OwnedLOC: 100, Share: 1.0, Score: 2.0},
	}
	s := mkScore(100, true, owners, 0, "", nil)

	var buf bytes.Buffer
	if err := JSON(&buf, "acme/web", s); err != nil {
		t.Fatalf("JSON render: %v", err)
	}

	body := buf.String()
	if !strings.Contains(body, `"ownership_share"`) {
		t.Errorf("JSON missing ownership_share field; got:\n%s", body)
	}
	if strings.Contains(body, `"ownership_factor"`) {
		t.Errorf("JSON still has stale ownership_factor field; got:\n%s", body)
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

	// Spot-check a known field round-trips to its rounded value.
	var got struct {
		SizeFactor float64 `json:"size_factor"`
		You        *struct {
			OwnershipShare float64 `json:"ownership_share"`
			RequestedCRU   float64 `json:"requested_cru"`
		} `json:"you"`
	}
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&got); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if got.SizeFactor != 0.123457 {
		t.Errorf("size_factor = %v, want 0.123457", got.SizeFactor)
	}
	if got.You == nil {
		t.Fatalf("missing you block")
	}
	if got.You.OwnershipShare != 0.333333 {
		t.Errorf("you.ownership_share = %v, want 0.333333", got.You.OwnershipShare)
	}
	if got.You.RequestedCRU != 0.777778 {
		t.Errorf("you.requested_cru = %v, want 0.777778", got.You.RequestedCRU)
	}
}

// TestJSONShapeOwnerTypes verifies the per-owner `type` field
// distinguishes user / team / unowned, that the user's direct @login row
// AND any team-membership row both set is_you=true, that team names use
// the slug form (e.g. acme/justice-league), user names use bare login
// (e.g. laserlemon, no leading "@"), and the unowned row has name==nil.
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

	var got struct {
		Owners []struct {
			Name  *string `json:"name"`
			Type  string  `json:"type"`
			IsYou bool    `json:"is_you"`
		} `json:"owners"`
	}
	if err := json.NewDecoder(&buf).Decode(&got); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}

	type row struct {
		name  string // empty for unowned
		typ   string
		isYou bool
	}
	byName := map[string]row{}
	var unowned *row
	for _, o := range got.Owners {
		r := row{typ: o.Type, isYou: o.IsYou}
		if o.Name == nil {
			if o.Type != "unowned" {
				t.Errorf("name==nil but type=%q; want \"unowned\"", o.Type)
			}
			u := r
			unowned = &u
			continue
		}
		r.name = *o.Name
		byName[*o.Name] = r
	}

	direct, ok := byName["laserlemon"]
	if !ok {
		t.Fatalf("missing @laserlemon row (looked up bare \"laserlemon\")")
	}
	if direct.typ != "user" || !direct.isYou {
		t.Errorf("laserlemon: type=%q is_you=%v; want user/true", direct.typ, direct.isYou)
	}

	team, ok := byName["acme/justice-league"]
	if !ok {
		t.Fatalf("missing @acme/justice-league row (looked up bare \"acme/justice-league\")")
	}
	if team.typ != "team" || !team.isYou {
		t.Errorf("acme/justice-league: type=%q is_you=%v; want team/true", team.typ, team.isYou)
	}

	other, ok := byName["acme/other-team"]
	if !ok {
		t.Fatalf("missing @acme/other-team row")
	}
	if other.typ != "team" || other.isYou {
		t.Errorf("acme/other-team: type=%q is_you=%v; want team/false", other.typ, other.isYou)
	}

	// No unowned row in this fixture (mkScore did not include one).
	if unowned != nil {
		t.Errorf("unexpected unowned row in JSON; fixture has no unowned LOC")
	}
}


// TestJSONShapeYouBlock verifies the optional `you` block surfaces with
// the correct ownership_share field name and only renders when the user
// has any owned LOC.
func TestJSONShapeYouBlock(t *testing.T) {
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

	var got struct {
		You *struct {
			Login          string  `json:"login"`
			OwnedLOC       int     `json:"owned_loc"`
			OwnershipShare float64 `json:"ownership_share"`
			RequestedCRU   float64 `json:"requested_cru"`
		} `json:"you"`
	}
	if err := json.NewDecoder(&buf).Decode(&got); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if got.You == nil {
		t.Fatalf("missing you block; want populated when MyOwnedLOC > 0")
	}
	if got.You.Login != "laserlemon" {
		t.Errorf("you.login = %q, want laserlemon", got.You.Login)
	}
	if got.You.OwnershipShare != 0.4 {
		t.Errorf("you.ownership_share = %v, want 0.4", got.You.OwnershipShare)
	}
}

// TestJSONShapeNoYouWhenAbsent verifies the `you` block is omitted from
// the output when no personal scoring was requested.
func TestJSONShapeNoYouWhenAbsent(t *testing.T) {
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

// TestHumanMarkers verifies the four marker variants render in the
// owner table. End-to-end through Human() with color disabled and TTY
// false so we can string-match against plain output.
func TestHumanMarkers(t *testing.T) {
	owners := []score.Ownership{
		{Owner: "@laserlemon", OwnedLOC: 25, Share: 0.25, Score: 0.5},  // direct → =
		{Owner: "@acme/big-orca", OwnedLOC: 25, Share: 0.25, Score: 0.5}, // team → *
		{Owner: "@acme/web-team", OwnedLOC: 25, Share: 0.25, Score: 0.5}, // other → •
		{Owner: score.UnownedOwnerLabel, OwnedLOC: 25, Share: 0.25, Score: 0.5},
	}
	s := mkScore(100, true, owners, 25, "laserlemon",
		[]string{"@laserlemon", "@acme/big-orca"})
	s.MyOwnedLOC = 25
	s.MyShare = 0.25
	s.MyCRU = 0.5

	var buf bytes.Buffer
	Human(&buf, "acme/web", s, noColorTerm())

	out := stripANSI(buf.String())
	// Verify each marker shows up on the right row.
	tests := []struct {
		marker string
		owner  string
	}{
		{"=", "laserlemon"},
		{"*", "acme/big-orca"},
		{"•", "acme/web-team"},
		{"~", "unowned"},
	}
	for _, tc := range tests {
		// In non-TTY mode the tableprinter uses tab separators between
		// columns. Build the assertion with a tab between marker and owner.
		needle := tc.marker + "\t" + tc.owner
		if !strings.Contains(out, needle) {
			t.Errorf("missing %q row in output:\n%s", needle, out)
		}
	}
}

// TestHumanShowsTotalCRU verifies the header block always renders both
// Normal CRU and Total CRU lines, even on a no-CODEOWNERS PR where they
// are equal.
func TestHumanShowsTotalCRU(t *testing.T) {
	owners := []score.Ownership{
		{Owner: score.UnownedOwnerLabel, OwnedLOC: 50, Share: 1.0, Score: 1.5},
	}
	s := mkScore(50, false, owners, 50, "", nil)

	var buf bytes.Buffer
	Human(&buf, "acme/web", s, noColorTerm())

	out := stripANSI(buf.String())
	for _, line := range []string{"Normal CRU", "Total CRU"} {
		if !strings.Contains(out, line) {
			t.Errorf("missing %q line in output:\n%s", line, out)
		}
	}
}

// TestHumanShowsUnownedRowEvenWithoutCodeowners verifies the unified
// table rendering: even when HasCodeowners=false, the table shows up
// with a single ~ unowned row.
func TestHumanShowsUnownedRowEvenWithoutCodeowners(t *testing.T) {
	owners := []score.Ownership{
		{Owner: score.UnownedOwnerLabel, OwnedLOC: 50, Share: 1.0, Score: 1.5},
	}
	s := mkScore(50, false, owners, 50, "", nil)

	var buf bytes.Buffer
	Human(&buf, "acme/web", s, noColorTerm())

	out := stripANSI(buf.String())
	if !strings.Contains(out, "CODE OWNER") {
		t.Errorf("missing CODE OWNER header; table should always render:\n%s", out)
	}
	if !strings.Contains(out, "~") || !strings.Contains(out, "unowned") {
		t.Errorf("missing ~ unowned row:\n%s", out)
	}
}

// TestHumanShareColumn verifies the SHARE header (formerly FACTOR).
func TestHumanShareColumn(t *testing.T) {
	owners := []score.Ownership{
		{Owner: "@acme/eng", OwnedLOC: 100, Share: 1.0, Score: 2.0},
	}
	s := mkScore(100, true, owners, 0, "", nil)

	var buf bytes.Buffer
	Human(&buf, "acme/web", s, noColorTerm())

	out := stripANSI(buf.String())
	if !strings.Contains(out, "SHARE") {
		t.Errorf("missing SHARE header:\n%s", out)
	}
	if strings.Contains(out, "FACTOR") {
		t.Errorf("output still has stale FACTOR header:\n%s", out)
	}
}

// --- color hashing -------------------------------------------------------

// TestHeadingColorDeterministic verifies that the same repo name always
// hashes to the same color (callers rely on this for visual grouping).
func TestHeadingColorDeterministic(t *testing.T) {
	a := headingColor("acme/web", true)("acme/web")
	b := headingColor("acme/web", true)("acme/web")
	if a != b {
		t.Errorf("headingColor not deterministic: %q != %q", a, b)
	}
}

// TestPRNumberColorDeterministic verifies the same property for PR numbers.
func TestPRNumberColorDeterministic(t *testing.T) {
	a := prNumberColor(1234, true)("#1234")
	b := prNumberColor(1234, true)("#1234")
	if a != b {
		t.Errorf("prNumberColor not deterministic: %q != %q", a, b)
	}
}

// TestColorsRespectsDisabled verifies that color=false returns the
// identity colorizer (no ANSI escape codes in output).
func TestColorsRespectsDisabled(t *testing.T) {
	got := headingColor("acme/web", false)("acme/web")
	if got != "acme/web" {
		t.Errorf("headingColor(color=false) returned %q, want plain text", got)
	}
	got2 := prNumberColor(1234, false)("#1234")
	if got2 != "#1234" {
		t.Errorf("prNumberColor(color=false) returned %q, want plain text", got2)
	}
}

// TestDisplayOwnerStripsAt verifies the @ prefix is removed for display.
func TestDisplayOwnerStripsAt(t *testing.T) {
	cases := map[string]string{
		"@laserlemon":     "laserlemon",
		"@acme/eng":       "acme/eng",
		"laserlemon":      "laserlemon",   // no @, unchanged
		"":                "",
		"@@double":        "@double",      // only first @ stripped
	}
	for in, want := range cases {
		if got := displayOwner(in); got != want {
			t.Errorf("displayOwner(%q) = %q, want %q", in, got, want)
		}
	}
}
