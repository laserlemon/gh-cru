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
	"strings"

	"github.com/cli/go-gh/v2/pkg/tableprinter"
	"github.com/cli/go-gh/v2/pkg/term"
	"github.com/cli/go-gh/v2/pkg/text"
	"github.com/mgutz/ansi"

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
	colorRiskLow  = ansi.ColorFunc("30+b") // dark teal
	colorRiskHigh = ansi.ColorFunc("88+b") // blood red

	// Heading palette for multi-PR mode. Hashed by repo so all PRs from
	// one repo share a color (visual grouping when running batches via
	// `gh pr list | xargs gh cru`); mixing repos differentiates them.
	// All entries are mid-saturation 256-color codes chosen to stay
	// out of the way of the size/risk palette below them. Bold so the
	// heading anchors the eye above each PR's block.
	headingPalette = []func(string) string{
		ansi.ColorFunc("110+b"), // pale blue
		ansi.ColorFunc("139+b"), // mauve
		ansi.ColorFunc("144+b"), // beige
		ansi.ColorFunc("180+b"), // wheat
		ansi.ColorFunc("103+b"), // periwinkle
		ansi.ColorFunc("108+b"), // muted green
	}

	// PR-number palette for the `#N` portion of the heading. Hashed by
	// PR number so the number gets its own deterministic color, distinct
	// from the repo palette above so the two pieces are visually
	// separable when scanning a batch of PRs from the same repo.
	// Brighter and warmer than the headingPalette to differentiate; still
	// readable on both dark and light terminals. Also bold for the same
	// reason as headingPalette.
	prNumberPalette = []func(string) string{
		ansi.ColorFunc("173+b"), // tan
		ansi.ColorFunc("215+b"), // peach
		ansi.ColorFunc("221+b"), // amber
		ansi.ColorFunc("209+b"), // salmon
		ansi.ColorFunc("147+b"), // lavender
		ansi.ColorFunc("114+b"), // pale green
		ansi.ColorFunc("117+b"), // sky blue
		ansi.ColorFunc("182+b"), // pink lilac
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
// ("XS") and any numeric value that conceptually shares the bucket's
// color (e.g. the size factor). Falls back to plain text when color
// is disabled.
func sizeColor(text, bucket string, enabled bool) string {
	if !enabled {
		return text
	}
	switch bucket {
	case "XS":
		return colorSizeXS(text)
	case "S":
		return colorSizeS(text)
	case "M":
		return colorSizeM(text)
	case "L":
		return colorSizeL(text)
	case "XL":
		return colorSizeXL(text)
	}
	return text
}

// riskColor returns text colored by risk level (low → teal, high → red).
// Used for both the risk label ("low"/"high") and the risk factor number.
func riskColor(text, level string, enabled bool) string {
	if !enabled {
		return text
	}
	if level == "high" {
		return colorRiskHigh(text)
	}
	return colorRiskLow(text)
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
//	LOC          <n>
//	Size label   <bucket>
//	Size factor  <f>
//	Risk label   <label>
//	Risk factor  <r>
//	Normal CRU   <c>
//	Total CRU    <sum across owners; team review burden>
//	Your CRU     <c>            (only when MyLogin is known)
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

	// Header block: %-12s pads labels to 12 chars (`Size factor` at 11 +
	// 1-space gap), matching the visual alignment in the user's mockup.
	// Same on TTY and pipe. Labels render in the same gray used for table
	// headers — `label` colorizes after padding so visible-width math
	// stays correct.
	fmt.Fprintf(w, "%s %d\n", label("LOC", color), s.LOC)
	fmt.Fprintf(w, "%s %s\n", label("Size label", color), sizeColor(string(s.Bucket), string(s.Bucket), color))
	fmt.Fprintf(w, "%s %s\n", label("Size factor", color), sizeColor(fmt.Sprintf("%.3f", s.SizeFactor), string(s.Bucket), color))
	rl := riskLabel(s.Risk)
	fmt.Fprintf(w, "%s %s\n", label("Risk label", color), riskColor(rl, rl, color))
	fmt.Fprintf(w, "%s %s\n", label("Risk factor", color), riskColor(fmt.Sprintf("%.3f", s.Risk), rl, color))
	fmt.Fprintf(w, "%s %.3f\n", label("Normal CRU", color), s.CRU())
	fmt.Fprintf(w, "%s %.3f\n", label("Total CRU", color), s.AuthorCRU())
	if s.MyLogin != "" {
		fmt.Fprintf(w, "%s %.3f\n", label("Your CRU", color), s.MyCRU)
	}

	fmt.Fprintln(w)
	writeOwnerTable(w, s, isTTY, color, width)
}

// label pads a metadata label to 12 chars and applies the gray styling
// (dim, NOT underlined: labels are inline, unlike table column headers).
// Padding happens before the ANSI escape so visible-width math (used by
// terminals to align columns) is correct.
func label(s string, enabled bool) string {
	padded := fmt.Sprintf("%-12s", s)
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

// JSON writes one indented JSON object per call.
func JSON(w io.Writer, repo string, s score.PRScore) error {
	type ownerJSON struct {
		Owner          string  `json:"owner"`
		OwnedLOC       int     `json:"owned_loc"`
		OwnershipShare float64 `json:"ownership_share"`
		RequestedCRU   float64 `json:"requested_cru"`
		IsYou          bool    `json:"is_you"`
		IsUnowned      bool    `json:"is_unowned"`
	}
	type youJSON struct {
		Login          string  `json:"login"`
		OwnedLOC       int     `json:"owned_loc"`
		OwnershipShare float64 `json:"ownership_share"`
		RequestedCRU   float64 `json:"requested_cru"`
	}
	type out struct {
		Repo          string      `json:"repo"`
		Number        int         `json:"number"`
		Title         string      `json:"title"`
		Author        string      `json:"author"`
		State         string      `json:"state"`
		Additions     int         `json:"additions"`
		Deletions     int         `json:"deletions"`
		LOC           int         `json:"loc"`
		Files         int         `json:"files"`
		SizeLabel     string      `json:"size_label"`
		SizeFactor    float64     `json:"size_factor"`
		RiskLabel     string      `json:"risk_label"`
		RiskFactor    float64     `json:"risk_factor"`
		NormalCRU     float64     `json:"normal_cru"` // size × risk
		TotalCRU      float64     `json:"total_cru"`  // Σ per-owner; review burden
		You           *youJSON    `json:"you,omitempty"`
		MyIdentities  []string    `json:"my_identities,omitempty"`
		HasCodeowners bool        `json:"has_codeowners"`
		Owners        []ownerJSON `json:"owners"`
		UnownedLOC    int         `json:"unowned_loc"`
	}
	owners := make([]ownerJSON, 0)
	mySet := makeIdentitySet(s.MyIdentities)
	for _, o := range s.SortedOwners() {
		isUnowned := o.Owner == score.UnownedOwnerLabel
		owners = append(owners, ownerJSON{
			Owner:          o.Owner,
			OwnedLOC:       o.OwnedLOC,
			OwnershipShare: o.Share,
			RequestedCRU:   o.Score,
			IsYou:          !isUnowned && mySet[strings.ToLower(o.Owner)],
			IsUnowned:      isUnowned,
		})
	}
	var you *youJSON
	if s.MyLogin != "" && s.MyOwnedLOC > 0 {
		you = &youJSON{
			Login:          s.MyLogin,
			OwnedLOC:       s.MyOwnedLOC,
			OwnershipShare: s.MyShare,
			RequestedCRU:   s.MyCRU,
		}
	}
	o := out{
		Repo:          repo,
		Number:        s.PR.Number,
		Title:         s.PR.Title,
		Author:        s.PR.Author,
		State:         s.PR.State,
		Additions:     s.PR.Additions,
		Deletions:     s.PR.Deletions,
		LOC:           s.LOC,
		Files:         s.PR.Files,
		SizeLabel:     string(s.Bucket),
		SizeFactor:    s.SizeFactor,
		RiskLabel:     riskLabel(s.Risk),
		RiskFactor:    s.Risk,
		NormalCRU:     s.CRU(),
		TotalCRU:      s.AuthorCRU(),
		You:           you,
		MyIdentities:  s.MyIdentities,
		HasCodeowners: s.HasCodeowners,
		Owners:        owners,
		UnownedLOC:    s.UnownedChanges,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(o)
}

func riskLabel(r float64) string {
	if r > 1.0 {
		return "high"
	}
	return "low"
}
