//go:build ignore

// Command ux-gallery renders a single, self-contained tour of EVERY
// user-facing gh-cru surface: help text, the measurement tables across all
// ownership permutations, the --skip-ownership / --anonymous
// variants, the auth-degradation footnote, the JSON surface, and the
// warning / error texts. It writes synthetic data only (no network), so
// the output is deterministic and reviewable in one file.
//
// Build a faithful, COLORED, fully-padded ANSI capture like this:
//
//	GH_FORCE_TTY=100 CLICOLOR_FORCE=1 \
//	  go run ./scripts/ux-gallery.go > gh-cru-ux-gallery.ansi
//
// Why GH_FORCE_TTY: go-gh's term.FromEnv() only reports a terminal (and
// only then does tableprinter pad+align columns) when stdout is a TTY.
// Redirecting to a file would otherwise collapse every table to bare,
// tab-separated text. GH_FORCE_TTY=<width> forces both the terminal flag
// and a fixed width, so the capture looks exactly like a real 100-column
// terminal. CLICOLOR_FORCE=1 keeps ANSI color on through the redirect.
//
// View it back with `less -R gh-cru-ux-gallery.ansi` or `cat` it into any
// ANSI-aware pager. NO_COLOR=1 produces a plain-text version.
//
// A pre-rendered capture is committed at docs/ux-gallery.ansi so you can
// read the whole UX surface without a Go toolchain; regenerate it after
// any change to internal/format or the help text:
//
//	GH_FORCE_TTY=100 CLICOLOR_FORCE=1 \
//	  go run ./scripts/ux-gallery.go > docs/ux-gallery.ansi
//
// This file is build-tagged `ignore`: it's a developer tool, never part
// of the package or the release binary.
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/cli/go-gh/v2/pkg/term"

	"github.com/laserlemon/cru"
	"github.com/laserlemon/gh-cru/internal/format"
	ghc "github.com/laserlemon/gh-cru/internal/gh"
	"github.com/laserlemon/gh-cru/internal/score"
)

func main() {
	t := term.FromEnv()
	color := t.IsColorEnabled()
	g := &gallery{t: t, color: color, out: os.Stdout}

	g.cover()

	// 1. HELP TEXT ---------------------------------------------------
	g.h1("1", "Help text")
	g.note("The three help screens a user reaches via -h / --help. Rendered")
	g.note("by invoking the real built binary so what you see is verbatim.")
	g.helpScreens()

	// 2. THE SCORING SURFACE (Human mode) ----------------------------
	g.h1("2", "Measurement output \u2014 Human mode (the default on a TTY)")
	g.note("Every distinct ownership shape. Each PR heading is bold; the")
	g.note("formula block (Size / Risk / Base) sits above the owners table.")
	g.legend()
	for _, c := range scenarios() {
		g.h2(c.Name)
		s := buildScore(c)
		format.Human(g.out, c.Repo, s, t)
	}

	// 3. --skip-ownership --------------------------------------------
	g.h1("3", "--skip-ownership")
	g.note("Skips CODEOWNERS entirely: no file fetch, no ownership lookup.")
	g.note("The measurement is just Size \u00d7 Risk, so the output ends on the")
	g.note("Base CRU line with NO ownership table (distinct from the genuine")
	g.note("no-CODEOWNERS case in section 2, which still shows a ~ Unowned")
	g.note("row). Resolving your identity is implied-off too.")
	g.h2("gh cru --skip-ownership 78")
	format.Human(g.out, "acme/payments", skipOwnershipScore(), t)

	// 4. --anonymous -------------------------------------------------
	g.h1("4", "--anonymous")
	g.note("Keeps the full CODEOWNERS table (owners + All ownership) but")
	g.note("doesn't resolve YOUR identity, so there's no \"Your ownership\"")
	g.note("row and no read:org-scoped call. Compare against the \"everything")
	g.note("at once\" table in section 2, which DOES show it.")
	g.h2("gh cru --anonymous 78")
	format.Human(g.out, "acme/payments", anonymousScore(), t)

	// 5. AUTH DEGRADATION --------------------------------------------
	g.h1("5", "Insufficient token access (graceful degradation)")
	g.note("When the token can't read your team memberships (the Codespaces")
	g.note("default GITHUB_TOKEN, or a fine-grained PAT without read:org),")
	g.note("gh-cru can't tell which team-owned lines are yours. It does NOT")
	g.note("fail; it measures what it can and footnotes the gap. The footnote")
	g.note("fires only when your stake would read 0 AND you're not already a")
	g.note("direct @login owner, i.e. exactly when the 0 might mislead.")
	g.h2("PR owned by a team you're (silently) on \u2014 footnote shown")
	format.Human(g.out, "acme/payments", degradedScore(), t)
	g.subnote("The same run also prints this to stderr if identity lookup itself failed:")
	g.stderrLine(`warn: could not resolve your identities: <reason> (continuing without "Your ownership")`)

	// 6. JSON SURFACE ------------------------------------------------
	g.h1("6", "JSON output (--json)")
	g.note("The structured surface, one compact NDJSON object per PR. This is")
	g.note("the canonical form; the Human view is a render OF these numbers.")
	g.note("Shown pretty-printed here for reading; real output is one line.")
	g.h2("gh cru --json 78   (the \"everything at once\" PR)")
	g.jsonBlock("acme/payments", buildScore(everythingScenario()))
	g.h2("gh cru --json --skip-ownership 78")
	g.note("Under --skip-ownership the `ownership` object is omitted entirely;")
	g.note("the measurement degrades cleanly to base_cru.")
	g.jsonBlock("acme/payments", skipOwnershipScore())

	// 7. WARNINGS & ERRORS -------------------------------------------
	g.h1("7", "Warnings & errors")
	g.note("Messages gh-cru can emit to stderr, plus the rejections for")
	g.note("flags that don't fit a score.")
	g.errExample("gh cru --web 1234", `error: --web is not supported: gh cru produces a CRU score, not a browser view`)
	g.errExample("gh cru --comments 1234", `error: --comments is not supported: gh cru produces a CRU score, not a comment thread`)
	g.errExample("gh cru list  (one PR in the batch fails)", `skip acme/web#1240: could not fetch files: HTTP 404`)
	g.errExample("gh cru  (team lookup degraded)", `warn: could not resolve your identities: HTTP 403 (continuing without "Your ownership")`)

	g.footer()
}

// ---------------------------------------------------------------------
// gallery: the framed-output helper. All banners are theme-safe ANSI
// that we emit directly (not via the formatter), so the gallery's own
// chrome is visually distinct from gh-cru's real output.
// ---------------------------------------------------------------------

type gallery struct {
	t     term.Term
	color bool
	out   *os.File
}

func (g *gallery) esc(code, s string) string {
	if !g.color {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

func (g *gallery) cover() {
	bar := strings.Repeat("\u2501", 64)
	fmt.Fprintln(g.out, g.esc("36;1", bar))
	fmt.Fprintln(g.out, g.esc("36;1", "  gh-cru \u2014 UX gallery"))
	fmt.Fprintln(g.out, g.esc("2", "  Every user-facing surface, rendered from synthetic data."))
	fmt.Fprintln(g.out, g.esc("2", "  No network. Deterministic. Generated by scripts/ux-gallery.go."))
	fmt.Fprintln(g.out, g.esc("36;1", bar))
}

func (g *gallery) h1(n, title string) {
	fmt.Fprintln(g.out)
	fmt.Fprintln(g.out)
	label := fmt.Sprintf(" %s. %s ", n, title)
	fmt.Fprintln(g.out, g.esc("46;30;1", label))
	fmt.Fprintln(g.out)
}

func (g *gallery) h2(title string) {
	fmt.Fprintln(g.out)
	fmt.Fprintln(g.out, g.esc("35;1", "\u25b8 "+title))
	fmt.Fprintln(g.out)
}

func (g *gallery) note(s string) { fmt.Fprintln(g.out, g.esc("2", "  "+s)) }
func (g *gallery) subnote(s string) {
	fmt.Fprintln(g.out)
	fmt.Fprintln(g.out, g.esc("2;3", "  "+s))
}

func (g *gallery) stderrLine(s string) {
	fmt.Fprintln(g.out, g.esc("33", "  "+s))
}

func (g *gallery) legend() {
	fmt.Fprintln(g.out)
	fmt.Fprintln(g.out, g.esc("2", "  Row markers:  ")+
		g.esc("1", "=")+g.esc("2", " you (direct @login)   ")+
		g.esc("1", "*")+g.esc("2", " you (via a team)   ")+
		g.esc("1", "\u2022")+g.esc("2", " someone else"))
	fmt.Fprintln(g.out, g.esc("2", "  Summary rows: ")+
		g.esc("1", "~")+g.esc("2", " Unowned   ")+
		g.esc("1", "+")+g.esc("2", " All ownership   ")+
		g.esc("1", ">")+g.esc("2", " Your ownership"))
}

func (g *gallery) errExample(cmd, msg string) {
	fmt.Fprintln(g.out)
	fmt.Fprintln(g.out, g.esc("2", "  $ ")+g.esc("0", cmd))
	fmt.Fprintln(g.out, g.esc("31", "  "+msg))
}

func (g *gallery) jsonBlock(repo string, s score.PRScore) {
	var buf bytes.Buffer
	if err := format.JSON(&buf, repo, s); err != nil {
		fmt.Fprintln(g.out, g.esc("31", "  JSON render error: "+err.Error()))
		return
	}
	// format.JSON emits compact NDJSON; pretty-print via `jq .` when
	// available so the gallery is readable, else show the raw line.
	pretty := buf.Bytes()
	if jq, err := exec.LookPath("jq"); err == nil {
		c := exec.Command(jq, ".")
		c.Stdin = bytes.NewReader(buf.Bytes())
		if outBytes, err := c.Output(); err == nil {
			pretty = outBytes
		}
	}
	for _, line := range strings.Split(strings.TrimRight(string(pretty), "\n"), "\n") {
		fmt.Fprintln(g.out, "  "+g.esc("2", line))
	}
}

func (g *gallery) helpScreens() {
	// Build the binary once, then ask it for each help screen so the
	// text is byte-for-byte what a user sees. Fall back to a note if the
	// build fails (e.g. offline module cache); the rest of the gallery
	// still renders.
	bin, cleanup, err := buildBinary()
	if err != nil {
		g.subnote("(could not build gh-cru to capture help text: " + err.Error() + ")")
		return
	}
	defer cleanup()
	for _, hc := range []struct {
		title string
		args  []string
	}{
		{"gh cru --help", []string{"--help"}},
		// view/list set DisableFlagParsing, so "<sub> --help" forwards to
		// gh pr view/list instead of printing cobra help. Route through
		// cobra's help COMMAND ("gh-cru help <sub>") to get the real text.
		{"gh cru view --help", []string{"help", "view"}},
		{"gh cru list --help", []string{"help", "list"}},
	} {
		g.h2(hc.title)
		cmd := exec.Command(bin, hc.args...)
		out, _ := cmd.CombinedOutput()
		for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
			fmt.Fprintln(g.out, "  "+line)
		}
	}
}

func (g *gallery) footer() {
	fmt.Fprintln(g.out)
	fmt.Fprintln(g.out)
	fmt.Fprintln(g.out, g.esc("2", strings.Repeat("\u2500", 64)))
	fmt.Fprintln(g.out, g.esc("2", "  End of gallery. Regenerate with:"))
	fmt.Fprintln(g.out, g.esc("2", "  GH_FORCE_TTY=100 CLICOLOR_FORCE=1 go run ./scripts/ux-gallery.go"))
}

// buildBinary compiles gh-cru to a temp path for help-text capture.
func buildBinary() (path string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "ghcru-gallery")
	if err != nil {
		return "", func() {}, err
	}
	bin := dir + "/gh-cru"
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		return "", func() {}, fmt.Errorf("%v: %s", err, out)
	}
	return bin, func() { os.RemoveAll(dir) }, nil
}

// ---------------------------------------------------------------------
// Scenario data + score construction (shared shape with the older
// demo-output.go, kept self-contained here so the gallery is one file).
// ---------------------------------------------------------------------

type scenario struct {
	Name          string
	Title         string
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

func scenarios() []scenario {
	return []scenario{
		{
			Name:  "single owner, full coverage (the trivial path)",
			Title: "Add rate limiting to webhook dispatcher", Repo: "acme/web",
			Number: 1234, LOC: 34, Risk: cru.RiskLow, HasCodeowners: true,
			Owners: []owner{{"@acme/web-team", 34}},
		},
		{
			Name:  "no CODEOWNERS file at all (whole PR is ~ unowned)",
			Title: "Bump sandbox image to node 22", Repo: "acme/sandbox",
			Number: 42, LOC: 48, Risk: cru.RiskLow,
		},
		{
			Name:  "owners + partial unowned lines (mixed coverage)",
			Title: "Refactor session store and add Redis backend", Repo: "acme/web",
			Number: 1236, LOC: 200, Risk: cru.RiskLow, HasCodeowners: true,
			Owners: []owner{{"@acme/web-team", 120}, {"@acme/auth-team", 50}}, UnownedLOC: 30,
		},
		{
			Name:  "multiple owners with overlap (All ownership exceeds Base)",
			Title: "Tighten auth middleware on the billing routes", Repo: "acme/web",
			Number: 1235, LOC: 80, Risk: cru.RiskLow, HasCodeowners: true,
			Owners: []owner{{"@acme/auth-team", 80}, {"@acme/web-team", 80}},
		},
		{
			Name:  "medium risk (amber tier)",
			Title: "Add pagination to the public list endpoints", Repo: "acme/api",
			Number: 1238, LOC: 34, Risk: cru.RiskMedium, HasCodeowners: true,
			Owners: []owner{{"@acme/api-team", 34}},
		},
		everythingScenario(),
	}
}

func everythingScenario() scenario {
	return scenario{
		Name:  "everything at once: direct @login, team, other, unowned, high risk",
		Title: "Migrate payments ledger to double-entry schema", Repo: "acme/payments",
		Number: 78, LOC: 240, Risk: cru.RiskHigh, HasCodeowners: true,
		Owners: []owner{
			{"@laserlemon", 40}, {"@acme/big-orca", 60}, {"@acme/payments-team", 100},
		},
		UnownedLOC: 40, MyLogin: "laserlemon",
		MyIdentities: []string{"@laserlemon", "@acme/big-orca"},
	}
}

// skipOwnershipScore mirrors what gh-cru produces under --skip-ownership:
// CODEOWNERS is never consulted and OwnershipSkipped is set, so the
// formatters end on Base CRU with no ownership table at all.
func skipOwnershipScore() score.PRScore {
	s := buildScore(scenario{
		Title: "Migrate payments ledger to double-entry schema", Repo: "acme/payments",
		Number: 78, LOC: 240, Risk: cru.RiskHigh, HasCodeowners: false,
	})
	s.OwnershipSkipped = true
	return s
}

// anonymousScore is the "everything" PR with --anonymous: full owners
// table, but MyLogin empty so no "Your ownership" row.
func anonymousScore() score.PRScore {
	c := everythingScenario()
	c.MyLogin = ""
	c.MyIdentities = nil
	return buildScore(c)
}

// degradedScore is a PR owned by a team the user is silently on, with
// TeamsResolved=false: MyLogin known, MyOwnedLOC 0, so the footnote fires.
func degradedScore() score.PRScore {
	s := buildScore(scenario{
		Title: "Rotate the signing keys for the auth service", Repo: "acme/payments",
		Number: 91, LOC: 120, Risk: cru.RiskHigh, HasCodeowners: true,
		Owners:  []owner{{"@acme/payments-team", 120}},
		MyLogin: "laserlemon", // known identity...
	})
	s.TeamsResolved = false // ...but we couldn't read team memberships
	return s
}

func buildScore(c scenario) score.PRScore {
	size := cru.CalculateSize(c.LOC)
	sf := float64(size)
	rf := c.Risk.Multiplier()
	result := score.PRScore{
		PR:            ghc.PR{Number: c.Number, Title: c.Title, Author: "demo", State: "open"},
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
		result.UnownedChanges = c.LOC
		denom := c.LOC
		if denom == 0 {
			denom = 1
		}
		result.OwnershipMap[score.UnownedOwnerLabel] = score.Ownership{
			Owner: score.UnownedOwnerLabel, OwnedLOC: c.LOC,
			Share: float64(c.LOC) / float64(denom),
			Score: sf * float64(c.LOC) / float64(denom) * rf,
		}
		result.OwnerOrder = []string{score.UnownedOwnerLabel}
		return result
	}
	denom := c.LOC
	if denom == 0 {
		denom = 1
	}
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
			Owner: o.Name, OwnedLOC: o.LOC, Share: share, Score: sf * share * rf,
		}
		result.OwnerOrder = append(result.OwnerOrder, o.Name)
	}
	if c.UnownedLOC > 0 {
		share := float64(c.UnownedLOC) / float64(denom)
		result.OwnershipMap[score.UnownedOwnerLabel] = score.Ownership{
			Owner: score.UnownedOwnerLabel, OwnedLOC: c.UnownedLOC,
			Share: share, Score: sf * share * rf,
		}
		result.OwnerOrder = append(result.OwnerOrder, score.UnownedOwnerLabel)
	}
	result.UnownedChanges = c.UnownedLOC
	return result
}
