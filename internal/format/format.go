// Package format renders PRScore results in two modes, modeled on gh's
// own output structure (see cli/cli/pkg/cmd/pr/list and friends):
//
//   - Human: TTY-friendly. Indented header block, gh-style tableprinter
//     for the owners section with traffic-light coloring. Honors NO_COLOR
//     and CLICOLOR* env vars via cli/go-gh's term package.
//   - JSON:   structured, pipe-friendly for jq.
//
// When stdout isn't a TTY, the tableprinter automatically degrades to
// tab-separated output, so `gh cru 123 | awk` still works. The header
// block also drops color in that mode (delegated to the colorizer).
package format

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/cli/go-gh/v2/pkg/tableprinter"
	"github.com/cli/go-gh/v2/pkg/term"
	"github.com/cli/go-gh/v2/pkg/text"
	"github.com/mgutz/ansi"

	"github.com/laserlemon/cru"
	"github.com/laserlemon/gh-cru/internal/score"
)

// padLeft right-aligns s by padding spaces on the left. tableprinter
// provides text.PadRight for left-aligned; we need the mirror for
// numeric columns.
func padLeft(width int, s string) string {
	w := text.DisplayWidth(s)
	if w >= width {
		return s
	}
	return strings.Repeat(" ", width-w) + s
}

// gh-style palette. mgutz/ansi style strings; gh CLI uses these directly
// in cli/cli/pkg/iostreams/color.go.
var (
	colorDim      = ansi.ColorFunc("default+d")
	colorBlue     = ansi.ColorFunc("blue")
	colorBoldBlue = ansi.ColorFunc("blue+b")

	// Size-bucket colors, matched to GitHub's conventional size/* PR labels
	// (lightly darkened so they hold up on light-mode terminals too).
	// Bold so the values stand out from the gray metadata labels.
	colorSizeXS = ansi.ColorFunc("28+b")  // dark green
	colorSizeS  = ansi.ColorFunc("106+b") // olive lime
	colorSizeM  = ansi.ColorFunc("178+b") // dark mustard
	colorSizeL  = ansi.ColorFunc("166+b") // burnt orange
	colorSizeXL = ansi.ColorFunc("124+b") // deep red

	// Risk colors, matched to conventional risk:* PR labels.
	colorRiskLow    = ansi.ColorFunc("30+b")  // dark teal
	colorRiskMedium = ansi.ColorFunc("214+b") // amber/orange — between teal and red on the heat axis
	colorRiskHigh   = ansi.ColorFunc("88+b")  // blood red

	// Heading palette for multi-PR mode. Hashed by repo so all PRs from
	// one repo share a color (visual grouping when running batches via
	// `gh pr list | xargs gh cru`); mixing repos differentiates them.
	// Every entry sits in the 0.10–0.30 WCAG luminance band so the
	// repo name hits ≥ 3:1 contrast on BOTH dark and light terminals.
	// 68 colors maximizes the chance two adjacent repos draw distinct
	// hashes. Bold so the heading anchors the eye above each PR's block.
	headingPalette = []func(string) string{
		ansi.ColorFunc("25+b"),  // #005faf
		ansi.ColorFunc("26+b"),  // #005fd7
		ansi.ColorFunc("27+b"),  // #005fff
		ansi.ColorFunc("28+b"),  // #008700
		ansi.ColorFunc("29+b"),  // #00875f
		ansi.ColorFunc("30+b"),  // #008787
		ansi.ColorFunc("31+b"),  // #0087af
		ansi.ColorFunc("32+b"),  // #0087d7
		ansi.ColorFunc("33+b"),  // #0087ff
		ansi.ColorFunc("58+b"),  // #5f5f00
		ansi.ColorFunc("59+b"),  // #5f5f5f
		ansi.ColorFunc("60+b"),  // #5f5f87
		ansi.ColorFunc("61+b"),  // #5f5faf
		ansi.ColorFunc("62+b"),  // #5f5fd7
		ansi.ColorFunc("63+b"),  // #5f5fff
		ansi.ColorFunc("64+b"),  // #5f8700
		ansi.ColorFunc("65+b"),  // #5f875f
		ansi.ColorFunc("66+b"),  // #5f8787
		ansi.ColorFunc("67+b"),  // #5f87af
		ansi.ColorFunc("68+b"),  // #5f87d7
		ansi.ColorFunc("69+b"),  // #5f87ff
		ansi.ColorFunc("92+b"),  // #8700d7
		ansi.ColorFunc("93+b"),  // #8700ff
		ansi.ColorFunc("94+b"),  // #875f00
		ansi.ColorFunc("95+b"),  // #875f5f
		ansi.ColorFunc("96+b"),  // #875f87
		ansi.ColorFunc("97+b"),  // #875faf
		ansi.ColorFunc("98+b"),  // #875fd7
		ansi.ColorFunc("99+b"),  // #875fff
		ansi.ColorFunc("100+b"), // #878700
		ansi.ColorFunc("101+b"), // #87875f
		ansi.ColorFunc("102+b"), // #878787
		ansi.ColorFunc("103+b"), // #8787af
		ansi.ColorFunc("104+b"), // #8787d7
		ansi.ColorFunc("105+b"), // #8787ff
		ansi.ColorFunc("126+b"), // #af0087
		ansi.ColorFunc("127+b"), // #af00af
		ansi.ColorFunc("128+b"), // #af00d7
		ansi.ColorFunc("129+b"), // #af00ff
		ansi.ColorFunc("130+b"), // #af5f00
		ansi.ColorFunc("131+b"), // #af5f5f
		ansi.ColorFunc("132+b"), // #af5f87
		ansi.ColorFunc("133+b"), // #af5faf
		ansi.ColorFunc("134+b"), // #af5fd7
		ansi.ColorFunc("135+b"), // #af5fff
		ansi.ColorFunc("136+b"), // #af8700
		ansi.ColorFunc("137+b"), // #af875f
		ansi.ColorFunc("138+b"), // #af8787
		ansi.ColorFunc("139+b"), // #af87af
		ansi.ColorFunc("160+b"), // #d70000
		ansi.ColorFunc("161+b"), // #d7005f
		ansi.ColorFunc("162+b"), // #d70087
		ansi.ColorFunc("163+b"), // #d700af
		ansi.ColorFunc("164+b"), // #d700d7
		ansi.ColorFunc("165+b"), // #d700ff
		ansi.ColorFunc("166+b"), // #d75f00
		ansi.ColorFunc("167+b"), // #d75f5f
		ansi.ColorFunc("168+b"), // #d75f87
		ansi.ColorFunc("169+b"), // #d75faf
		ansi.ColorFunc("170+b"), // #d75fd7
		ansi.ColorFunc("171+b"), // #d75fff
		ansi.ColorFunc("196+b"), // #ff0000
		ansi.ColorFunc("197+b"), // #ff005f
		ansi.ColorFunc("198+b"), // #ff0087
		ansi.ColorFunc("199+b"), // #ff00af
		ansi.ColorFunc("200+b"), // #ff00d7
		ansi.ColorFunc("201+b"), // #ff00ff
		ansi.ColorFunc("202+b"), // #ff5f00
	}

	// PR-number palette for the `#N` portion of the heading. Hashed by
	// PR number so the number gets its own deterministic color, distinct
	// from the repo palette above. Same 68-color WCAG-safe pool — when
	// the repo hash and number hash land on the same color it's a rare
	// collision the brain processes as "same family" without losing
	// info.
	prNumberPalette = []func(string) string{
		ansi.ColorFunc("25+b"),  // #005faf
		ansi.ColorFunc("26+b"),  // #005fd7
		ansi.ColorFunc("27+b"),  // #005fff
		ansi.ColorFunc("28+b"),  // #008700
		ansi.ColorFunc("29+b"),  // #00875f
		ansi.ColorFunc("30+b"),  // #008787
		ansi.ColorFunc("31+b"),  // #0087af
		ansi.ColorFunc("32+b"),  // #0087d7
		ansi.ColorFunc("33+b"),  // #0087ff
		ansi.ColorFunc("58+b"),  // #5f5f00
		ansi.ColorFunc("59+b"),  // #5f5f5f
		ansi.ColorFunc("60+b"),  // #5f5f87
		ansi.ColorFunc("61+b"),  // #5f5faf
		ansi.ColorFunc("62+b"),  // #5f5fd7
		ansi.ColorFunc("63+b"),  // #5f5fff
		ansi.ColorFunc("64+b"),  // #5f8700
		ansi.ColorFunc("65+b"),  // #5f875f
		ansi.ColorFunc("66+b"),  // #5f8787
		ansi.ColorFunc("67+b"),  // #5f87af
		ansi.ColorFunc("68+b"),  // #5f87d7
		ansi.ColorFunc("69+b"),  // #5f87ff
		ansi.ColorFunc("92+b"),  // #8700d7
		ansi.ColorFunc("93+b"),  // #8700ff
		ansi.ColorFunc("94+b"),  // #875f00
		ansi.ColorFunc("95+b"),  // #875f5f
		ansi.ColorFunc("96+b"),  // #875f87
		ansi.ColorFunc("97+b"),  // #875faf
		ansi.ColorFunc("98+b"),  // #875fd7
		ansi.ColorFunc("99+b"),  // #875fff
		ansi.ColorFunc("100+b"), // #878700
		ansi.ColorFunc("101+b"), // #87875f
		ansi.ColorFunc("102+b"), // #878787
		ansi.ColorFunc("103+b"), // #8787af
		ansi.ColorFunc("104+b"), // #8787d7
		ansi.ColorFunc("105+b"), // #8787ff
		ansi.ColorFunc("126+b"), // #af0087
		ansi.ColorFunc("127+b"), // #af00af
		ansi.ColorFunc("128+b"), // #af00d7
		ansi.ColorFunc("129+b"), // #af00ff
		ansi.ColorFunc("130+b"), // #af5f00
		ansi.ColorFunc("131+b"), // #af5f5f
		ansi.ColorFunc("132+b"), // #af5f87
		ansi.ColorFunc("133+b"), // #af5faf
		ansi.ColorFunc("134+b"), // #af5fd7
		ansi.ColorFunc("135+b"), // #af5fff
		ansi.ColorFunc("136+b"), // #af8700
		ansi.ColorFunc("137+b"), // #af875f
		ansi.ColorFunc("138+b"), // #af8787
		ansi.ColorFunc("139+b"), // #af87af
		ansi.ColorFunc("160+b"), // #d70000
		ansi.ColorFunc("161+b"), // #d7005f
		ansi.ColorFunc("162+b"), // #d70087
		ansi.ColorFunc("163+b"), // #d700af
		ansi.ColorFunc("164+b"), // #d700d7
		ansi.ColorFunc("165+b"), // #d700ff
		ansi.ColorFunc("166+b"), // #d75f00
		ansi.ColorFunc("167+b"), // #d75f5f
		ansi.ColorFunc("168+b"), // #d75f87
		ansi.ColorFunc("169+b"), // #d75faf
		ansi.ColorFunc("170+b"), // #d75fd7
		ansi.ColorFunc("171+b"), // #d75fff
		ansi.ColorFunc("196+b"), // #ff0000
		ansi.ColorFunc("197+b"), // #ff005f
		ansi.ColorFunc("198+b"), // #ff0087
		ansi.ColorFunc("199+b"), // #ff00af
		ansi.ColorFunc("200+b"), // #ff00d7
		ansi.ColorFunc("201+b"), // #ff00ff
		ansi.ColorFunc("202+b"), // #ff5f00
	}

	// Table header styling matches gh CLI's iostreams.ColorScheme.TableHeader:
	// dim + underlined for dark themes, dim + underlined for unknown themes
	// (no theme detection in go-gh's term package). gh uses `white+du` for
	// dark and `black+hu` for light; we use `default+du` so it adapts to the
	// terminal's foreground color without us having to detect theme.
	colorTableHeader = ansi.ColorFunc("default+du")

	// colorLabel is the metadata-label gray: same dim as table headers
	// but without the underline (labels are inline, not column anchors).
	colorLabel = ansi.ColorFunc("default+d")
)

// sizeColor returns text colored by the size bucket's palette
// (XS bright green → XL pink). Used for both the bucket label cell
// (cru.SizeXS etc.) and any numeric value that conceptually shares
// the bucket's color (e.g. the size factor). Falls back to plain text
// when color is disabled.
func sizeColor(text, bucket string, enabled bool) string {
	if !enabled {
		return text
	}
	switch bucket {
	case cru.SizeXS:
		return colorSizeXS(text)
	case cru.SizeS:
		return colorSizeS(text)
	case cru.SizeM:
		return colorSizeM(text)
	case cru.SizeL:
		return colorSizeL(text)
	case cru.SizeXL:
		return colorSizeXL(text)
	}
	return text
}

// riskColor returns text colored by risk level (low → teal, high → red).
// riskColor paints the risk label and risk multiplier in their tier color.
// Drives both the human-readable "low/medium/high" string and the
// numeric multiplier (e.g. "4.000") so the eye reads them as one unit.
func riskColor(text string, risk cru.Risk, enabled bool) string {
	if !enabled {
		return text
	}
	switch risk {
	case cru.RiskHigh:
		return colorRiskHigh(text)
	case cru.RiskMedium:
		return colorRiskMedium(text)
	default:
		return colorRiskLow(text)
	}
}

func dim(s string, enabled bool) string {
	if !enabled {
		return s
	}
	return colorDim(s)
}

// Human writes a TTY-friendly summary for one PR. The caller passes the
// gh term so we can detect TTY/color/width consistently with gh itself.
//
// Layout:
//
//	owner/repo#N                 (always-on, color hashed by repo)
//
//	LOC              <n>
//	Size label       <bucket>
//	Size factor      <f>
//	Risk label       <label>
//	Risk multiplier  <r>
//	Normal CRU       <c>
//	Total CRU        <sum across owners; team review burden>
//	Your CRU         <c>            (only when MyLogin is known)
//
//	CODE OWNER                       LOC  FACTOR    CRU
//	  github/some-team                34  0.895  0.971
//	* github/team-you-own              4  0.105  0.114
//
//	Calculating your personal CRU requires read:org authorization to
//	read your team memberships.   (only when teams couldn't be enumerated)
//
// PR title/state/author/diffstat are omitted: callers running batches
// typically already know what PR they asked for, and `gh pr view`
// covers the metadata.
func Human(w io.Writer, repo string, s score.PRScore, t term.Term) {
	isTTY := t.IsTerminalOutput()
	color := t.IsColorEnabled()
	width, _, _ := t.Size()
	if width <= 0 {
		width = 80
	}

	// Always-on heading. The `owner/repo` portion takes a deterministic
	// color hashed from the repo name (so a batch of PRs from one repo
	// visually groups together); the `#N` portion takes its own
	// deterministic color hashed from the PR number (so distinct PRs in
	// the same repo are still differentiable at a glance). Both palettes
	// are bold so the heading anchors the eye above each PR's block.
	// The two palettes are chosen to be visually distinct from each other.
	repoColor := headingColor(repo, color)
	prColor := prNumberColor(s.PR.Number, color)
	heading := repoColor(repo) + prColor(fmt.Sprintf("#%d", s.PR.Number))
	fmt.Fprintf(w, "%s\n\n", heading)

	// Header block: %-16s pads labels to 16 chars (`Risk multiplier` at 15 +
	// 1-space gap), matching the visual alignment in the user's mockup.
	// Same on TTY and pipe. Labels render in the same gray used for table
	// headers — `label` colorizes after padding so visible-width math
	// stays correct.
	fmt.Fprintf(w, "%s %d\n", label("LOC", color), s.LOC)
	fmt.Fprintf(w, "%s %s\n", label("Size label", color), sizeColor(s.Size.String(), s.Size.String(), color))
	fmt.Fprintf(w, "%s %s\n", label("Size factor", color), sizeColor(fmt.Sprintf("%.3f", float64(s.Size)), s.Size.String(), color))
	rl := s.Risk.String()
	fmt.Fprintf(w, "%s %s\n", label("Risk label", color), riskColor(rl, s.Risk, color))
	fmt.Fprintf(w, "%s %s\n", label("Risk multiplier", color), riskColor(fmt.Sprintf("%.3f", s.Risk.Multiplier()), s.Risk, color))
	fmt.Fprintf(w, "%s %.3f\n", label("Normal CRU", color), s.CRU())
	fmt.Fprintf(w, "%s %.3f\n", label("Total CRU", color), s.AuthorCRU())
	if s.MyLogin != "" {
		fmt.Fprintf(w, "%s %.3f\n", label("Your CRU", color), s.MyCRU)
	}

	fmt.Fprintln(w)
	writeOwnerTable(w, s, isTTY, color, width)
}

// label pads a metadata label to 16 chars and applies the gray styling
// (dim, NOT underlined: labels are inline, unlike table column headers).
// Padding happens before the ANSI escape so visible-width math (used by
// terminals to align columns) is correct.
func label(s string, enabled bool) string {
	padded := fmt.Sprintf("%-16s", s)
	if !enabled {
		return padded
	}
	return colorLabel(padded)
}

// headingColor returns a colorizer for the multi-PR heading, picked by
// hashing the repo name. Same repo → same color; different repos →
// (almost certainly) different colors, so adjacent PRs from different
// repos visually separate while batches from one repo group together.
// Returns identity when color is disabled.
func headingColor(repo string, enabled bool) func(string) string {
	if !enabled {
		return func(s string) string { return s }
	}
	var h uint32 = 2166136261 // FNV-1a 32-bit offset basis
	for i := 0; i < len(repo); i++ {
		h ^= uint32(repo[i])
		h *= 16777619
	}
	return headingPalette[int(h%uint32(len(headingPalette)))]
}

// prNumberColor returns a colorizer for the `#N` portion of the heading,
// picked by hashing the PR number. Same number → same color (across runs
// and across repos); different numbers → likely different colors so
// distinct PRs are visually separable even in single-repo batches.
// Returns identity when color is disabled.
//
// The hash uses the same FNV-1a recipe as headingColor but folds the
// integer bytes; this gives well-mixed output even for adjacent small
// integers like #1234, #1235, #1236 which would otherwise collide under
// a simple modulo.
func prNumberColor(n int, enabled bool) func(string) string {
	if !enabled {
		return func(s string) string { return s }
	}
	var h uint32 = 2166136261
	u := uint32(n)
	for i := 0; i < 4; i++ {
		h ^= u & 0xff
		h *= 16777619
		u >>= 8
	}
	return prNumberPalette[int(h%uint32(len(prNumberPalette)))]
}

// writeOwnerTable uses cli/go-gh's tableprinter so column widths, padding,
// truncation, and TTY/pipe degradation are handled the same way the gh
// CLI itself does it. On a TTY: aligned columns, color, truncation if
// the terminal is narrow. Off a TTY: tab-separated, no color, no
// truncation - `gh cru | awk` works.
//
// Layout uses a dedicated 1-char gutter column for the row-type marker:
//
//	_  CODE OWNER             LOC   SHARE    CRU
//	=  laserlemon              20   0.400  0.800   (direct match, blue+b)
//	*  acme/big-orca           80   0.400  0.800   (team match, blue)
//	•  acme/web-team          120   0.600  1.200   (someone else)
//	~  unowned                 60   0.300  0.600   (no owner, dim marker+name)
//
// Markers (color matches the OWNER cell so they read as one unit):
//   - `=` blue+bold    - direct @login match (you specifically own these lines)
//   - `*` blue         - team membership match (a team you're on owns these)
//   - `•` default      - someone else owns these
//   - `~` dim          - synthetic unowned row (no CODEOWNERS rule matched)
//
// All four are 1 column wide so owner names stay vertically aligned.
// The marker column header is a single underlined space, matching the
// `gh pr checks` convention for status-marker columns.
func writeOwnerTable(w io.Writer, s score.PRScore, isTTY, color bool, width int) {
	tp := tableprinter.New(w, isTTY, width)

	// Header. Right-align numeric columns; underline + dim styling matches
	// gh CLI's tableprinter convention (iostreams.ColorScheme.TableHeader).
	// The marker column header is a single space, styled with the same
	// underline + dim treatment so it visually reads as `_` and stays
	// aligned with the data column below.
	headerColor := func(v string) string { return v }
	if color {
		headerColor = colorTableHeader
	}
	tp.AddField(" ", tableprinter.WithColor(headerColor))
	tp.AddField("CODE OWNER", tableprinter.WithColor(headerColor))
	tp.AddField("LOC", tableprinter.WithColor(headerColor), tableprinter.WithPadding(padLeft))
	tp.AddField("SHARE", tableprinter.WithColor(headerColor), tableprinter.WithPadding(padLeft))
	tp.AddField("CRU", tableprinter.WithColor(headerColor), tableprinter.WithPadding(padLeft))
	tp.EndRow()

	mySet := makeIdentitySet(s.MyIdentities)
	myLoginKey := "@" + strings.ToLower(s.MyLogin)

	for _, o := range s.SortedOwners() {
		isUnowned := o.Owner == score.UnownedOwnerLabel
		isDirectYou := s.MyLogin != "" && strings.ToLower(o.Owner) == myLoginKey
		isTeamYou := !isDirectYou && mySet[strings.ToLower(o.Owner)]
		addOwnerRow(tp, o, isDirectYou, isTeamYou, isUnowned, color)
	}

	if err := tp.Render(); err != nil {
		fmt.Fprintf(w, "  (table render error: %v)\n", err)
	}

	// Footnote: render only when team enumeration was incomplete AND the
	// user didn't surface in the table (no direct @login match). The
	// Codespaces default GITHUB_TOKEN is the common case: a PR owned by a
	// team the user is in shows up with no `*` marker and "Your CRU"
	// reads 0.
	if !s.TeamsResolved && s.MyLogin != "" && s.MyOwnedLOC == 0 {
		fmt.Fprintln(w)
		note := "Calculating your personal CRU requires read:org authorization to read your team memberships."
		if color {
			fmt.Fprintln(w, colorDim(note))
		} else {
			fmt.Fprintln(w, note)
		}
	}
}

// addOwnerRow appends one row to the owners table. Selects a marker
// based on the row's type and applies colors:
//
//   - direct @login match     → `=` blue+bold, owner cell blue+bold
//   - team membership match   → `*` blue, owner cell blue
//   - someone else            → `•` default, owner cell default
//   - synthetic unowned       → `~` dim, owner cell dim (numeric columns stay default)
//
// The marker lives in its own column (1 char wide) so owner names stay
// vertically aligned across all rows. Marker color matches owner cell
// color so marker + name read as one visual unit; numeric columns
// (LOC/SHARE/CRU) always render in default color, including on the
// unowned row, so the values are easy to scan against each other.
func addOwnerRow(tp tableprinter.TablePrinter, o score.Ownership, isDirectYou, isTeamYou, isUnowned, color bool) {
	display := displayOwner(o.Owner)

	// Pick marker + colorizer. cellColor is applied to the OWNER cell;
	// markerColor matches so they read as a single unit.
	identity := func(s string) string { return s }
	marker := "•"
	markerColor := identity
	cellColor := identity

	switch {
	case isUnowned:
		marker = "~"
		if color {
			markerColor = colorDim
			cellColor = colorDim
		}
	case isDirectYou:
		marker = "="
		if color {
			markerColor = colorBoldBlue
			cellColor = colorBoldBlue
		}
	case isTeamYou:
		marker = "*"
		if color {
			markerColor = colorBlue
			cellColor = colorBlue
		}
	}

	tp.AddField(marker, tableprinter.WithColor(markerColor))
	tp.AddField(display, tableprinter.WithColor(cellColor))
	tp.AddField(fmt.Sprintf("%d", o.OwnedLOC), tableprinter.WithPadding(padLeft))
	tp.AddField(fmt.Sprintf("%.3f", o.Share), tableprinter.WithPadding(padLeft))
	tp.AddField(fmt.Sprintf("%.3f", o.Score), tableprinter.WithPadding(padLeft))
	tp.EndRow()
}

func makeIdentitySet(ids []string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[strings.ToLower(id)] = true
	}
	return m
}

// displayOwner strips the leading "@" from CODEOWNERS strings for display.
func displayOwner(s string) string {
	return strings.TrimPrefix(s, "@")
}

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
