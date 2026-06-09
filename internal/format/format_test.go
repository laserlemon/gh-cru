package format

import (
	"bytes"
	"encoding/json"
	"os"
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
	sf := cru.SizeFactor(loc)
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
		SizeFactor:     sf,
		Bucket:         cru.Bucket(loc),
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
// the synthetic unowned owner row with is_unowned=true, since the JSON
// shape is a public contract for downstream scripts.
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
			Owner          string  `json:"owner"`
			OwnedLOC       int     `json:"owned_loc"`
			OwnershipShare float64 `json:"ownership_share"`
			RequestedCRU   float64 `json:"requested_cru"`
			IsYou          bool    `json:"is_you"`
			IsUnowned      bool    `json:"is_unowned"`
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
	// The unowned row should have IsUnowned=true and IsYou=false.
	var found bool
	for _, o := range got.Owners {
		if o.Owner == score.UnownedOwnerLabel {
			found = true
			if !o.IsUnowned {
				t.Errorf("unowned row has IsUnowned=false; want true")
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
// ownership_share (not ownership_factor — that was the old name and
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

// TestRiskLabel verifies the float-to-label mapping.
func TestRiskLabel(t *testing.T) {
	if got := riskLabel(cru.RiskLow); got != "low" {
		t.Errorf("riskLabel(RiskLow) = %q, want low", got)
	}
	if got := riskLabel(cru.RiskHigh); got != "high" {
		t.Errorf("riskLabel(RiskHigh) = %q, want high", got)
	}
}
