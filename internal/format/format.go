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
	colorBold     = ansi.ColorFunc("default+b")
	colorBoldCyan = ansi.ColorFunc("cyan+b")

	// Size-bucket colors, matched to github/github's size/* PR labels
	// (lightly darkened so they hold up on light-mode terminals too).
	// Bold so the values stand out from the gray metadata labels.
	colorSizeXS = ansi.ColorFunc("28+b")  // dark green
	colorSizeS  = ansi.ColorFunc("106+b") // olive lime
	colorSizeM  = ansi.ColorFunc("178+b") // dark mustard
	colorSizeL  = ansi.ColorFunc("166+b") // burnt orange
	colorSizeXL = ansi.ColorFunc("124+b") // deep red

	// Risk colors, matched to github/github's risk:* PR labels.
	colorRiskLow  = ansi.ColorFunc("30+b") // dark teal
	colorRiskHigh = ansi.ColorFunc("88+b") // blood red

	// Heading palette for multi-PR mode. Hashed by repo so all PRs from
	// one repo share a color (visual grouping when running batches via
	// `gh pr list | xargs gh cru`); mixing repos differentiates them.
	// All entries are mid-saturation 256-color codes chosen to stay
	// out of the way of the size/risk palette below them.
	headingPalette = []func(string) string{
		ansi.ColorFunc("110"), // pale blue
		ansi.ColorFunc("139"), // mauve
		ansi.ColorFunc("144"), // beige
		ansi.ColorFunc("180"), // wheat
		ansi.ColorFunc("103"), // periwinkle
		ansi.ColorFunc("108"), // muted green
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

// sizeColor maps the bucket label to its github/github-matched color
// (XS bright green → XL pink). Falls back to the default fg when color
// is disabled.
func sizeColor(bucket string, enabled bool) string {
	if !enabled {
		return bucket
	}
	switch bucket {
	case "XS":
		return colorSizeXS(bucket)
	case "S":
		return colorSizeS(bucket)
	case "M":
		return colorSizeM(bucket)
	case "L":
		return colorSizeL(bucket)
	case "XL":
		return colorSizeXL(bucket)
	}
	return bucket
}

// riskColor maps low/high to their github/github-matched label colors
// (teal / salmon).
func riskColor(label string, enabled bool) string {
	if !enabled {
		return label
	}
	if label == "high" {
		return colorRiskHigh(label)
	}
	return colorRiskLow(label)
}

func dim(s string, enabled bool) string {
	if !enabled {
		return s
	}
	return colorDim(s)
}

// Human writes a TTY-friendly summary for one PR. The caller passes the
// gh term so we can detect TTY/color/width consistently with gh itself.
// showHeading toggles a dim `repo#N` heading at the top — used in batch
// mode (gh cru pr1 pr2 pr3) so users can tell adjacent blocks apart.
//
// Layout (heading shown only when showHeading is true):
//
//	repo#N                       (dim; batch mode only)
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
// PR title/state/author/diffstat and the PR heading itself are omitted:
// callers running batches typically already know what PR they asked for,
// and `gh pr view` covers the metadata.
func Human(w io.Writer, repo string, s score.PRScore, t term.Term, showHeading bool) {
	isTTY := t.IsTerminalOutput()
	color := t.IsColorEnabled()
	width, _, _ := t.Size()
	if width <= 0 {
		width = 80
	}

	// Batch-mode heading: dim `owner/repo#N` so adjacent PR blocks have a
	// visual anchor. Suppressed in single-PR mode where there's nothing
	// to disambiguate.
	if showHeading {
		fmt.Fprintf(w, "%s\n\n", headingColor(repo, color)(fmt.Sprintf("%s#%d", repo, s.PR.Number)))
	}

	// Header block: %-12s pads labels to 12 chars (`Size factor` at 11 +
	// 1-space gap), matching the visual alignment in the user's mockup.
	// Same on TTY and pipe. Labels render in the same gray used for table
	// headers — `label` colorizes after padding so visible-width math
	// stays correct.
	fmt.Fprintf(w, "%s %d\n", label("LOC", color), s.LOC)
	fmt.Fprintf(w, "%s %s\n", label("Size label", color), sizeColor(string(s.Bucket), color))
	fmt.Fprintf(w, "%s %.3f\n", label("Size factor", color), s.SizeFactor)
	fmt.Fprintf(w, "%s %s\n", label("Risk label", color), riskColor(riskLabel(s.Risk), color))
	fmt.Fprintf(w, "%s %.3f\n", label("Risk factor", color), s.Risk)
	fmt.Fprintf(w, "%s %.3f\n", label("Normal CRU", color), s.CRU())

	if !s.HasCodeowners {
		fmt.Fprintf(w, "%s %.3f\n", label("Total CRU", color), s.CRU())
		if s.MyLogin != "" {
			fmt.Fprintf(w, "%s %.3f\n", label("Your CRU", color), s.CRU())
		}
		fmt.Fprintln(w)
		fmt.Fprintf(w, "%s\n", dim("No CODEOWNERS file in repo.", color))
		return
	}

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

// writeOwnerTable uses cli/go-gh's tableprinter so column widths, padding,
// truncation, and TTY/pipe degradation are handled the same way the gh
// CLI itself does it. On a TTY: aligned columns, color, truncation if
// the terminal is narrow. Off a TTY: tab-separated, no color, no
// truncation - `gh cru | awk` works.
func writeOwnerTable(w io.Writer, s score.PRScore, isTTY, color bool, width int) {
	tp := tableprinter.New(w, isTTY, width)

	// Header. Right-align numeric columns; underline + dim styling matches
	// gh CLI's tableprinter convention (iostreams.ColorScheme.TableHeader).
	// The `*` marker on data rows sits in a gutter LEFT of the table — see
	// addOwnerRow's note. The CODE OWNER header therefore starts at column
	// 0, flush with unmarked owner rows; marked rows visually break out
	// into the gutter.
	headerColor := func(v string) string { return v }
	if color {
		headerColor = colorTableHeader
	}
	tp.AddField("CODE OWNER", tableprinter.WithColor(headerColor))
	tp.AddField("LOC", tableprinter.WithColor(headerColor), tableprinter.WithPadding(padLeft))
	tp.AddField("FACTOR", tableprinter.WithColor(headerColor), tableprinter.WithPadding(padLeft))
	tp.AddField("CRU", tableprinter.WithColor(headerColor), tableprinter.WithPadding(padLeft))
	tp.EndRow()

	mySet := makeIdentitySet(s.MyIdentities)

	// No dedicated user row above the table: "Your CRU" lives in the
	// header block now. The table is purely the CODEOWNERS view, with
	// `*` markers on teams the user belongs to.
	for _, o := range s.SortedOwners() {
		isTeamYou := mySet[strings.ToLower(o.Owner)]
		addOwnerRow(tp, isTeamYou, displayOwner(o.Owner), o.OwnedLOC, o.Share, o.Score, false, isTeamYou, color)
	}

	if s.UnownedChanges > 0 {
		ushare := 0.0
		if s.LOC > 0 {
			ushare = float64(s.UnownedChanges) / float64(s.LOC)
		}
		if color {
			tp.AddField("(unowned)", tableprinter.WithColor(colorDim))
		} else {
			tp.AddField("(unowned)")
		}
		tp.AddField(fmt.Sprintf("%d", s.UnownedChanges), tableprinter.WithPadding(padLeft))
		tp.AddField(fmt.Sprintf("%.3f", ushare), tableprinter.WithPadding(padLeft))
		if color {
			tp.AddField("-", tableprinter.WithColor(colorDim), tableprinter.WithPadding(padLeft))
		} else {
			tp.AddField("-", tableprinter.WithPadding(padLeft))
		}
		tp.EndRow()
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

// addOwnerRow appends one owner row. The `* ` marker sits in a "gutter"
// LEFT of the CODE OWNER column (unmarked rows have no leading space at
// all; marked rows visually break out left of the table). This matches
// git branch's convention and keeps the data column tight.
//
// We achieve this without an extra column by *prepending* "* " to the
// owner cell when marked, and accepting that the marked row is visually
// 2 chars wider on the left than unmarked rows. tableprinter's width
// calculation uses the widest cell, so the LOC/FACTOR/CRU columns still
// align across all rows.
func addOwnerRow(tp tableprinter.TablePrinter, marked bool, owner string, loc int, share, cru float64, isYou, isTeamYou, color bool) {
	cell := owner
	if marked {
		cell = "* " + owner
	}
	// Coloring is applied to the whole cell (including marker) so the bold
	// extends over the `*`. The eye reads "this row is yours" as one unit.
	switch {
	case isYou && color:
		cell = colorBoldCyan(cell)
	case isTeamYou && color:
		cell = colorBold(cell)
	}
	tp.AddField(cell)
	tp.AddField(fmt.Sprintf("%d", loc), tableprinter.WithPadding(padLeft))
	tp.AddField(fmt.Sprintf("%.3f", share), tableprinter.WithPadding(padLeft))
	tp.AddField(fmt.Sprintf("%.3f", cru), tableprinter.WithPadding(padLeft))
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
		Owner           string  `json:"owner"`
		OwnedLOC        int     `json:"owned_loc"`
		OwnershipFactor float64 `json:"ownership_factor"`
		RequestedCRU    float64 `json:"requested_cru"`
		IsYou           bool    `json:"is_you"`
	}
	type youJSON struct {
		Login           string  `json:"login"`
		OwnedLOC        int     `json:"owned_loc"`
		OwnershipFactor float64 `json:"ownership_factor"`
		RequestedCRU    float64 `json:"requested_cru"`
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
		owners = append(owners, ownerJSON{
			Owner:           o.Owner,
			OwnedLOC:        o.OwnedLOC,
			OwnershipFactor: o.Share,
			RequestedCRU:    o.Score,
			IsYou:           mySet[strings.ToLower(o.Owner)],
		})
	}
	var you *youJSON
	if s.MyLogin != "" && s.MyOwnedLOC > 0 {
		you = &youJSON{
			Login:           s.MyLogin,
			OwnedLOC:        s.MyOwnedLOC,
			OwnershipFactor: s.MyShare,
			RequestedCRU:    s.MyCRU,
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
