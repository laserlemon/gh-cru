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
	colorGreen    = ansi.ColorFunc("green")
	colorYellow   = ansi.ColorFunc("yellow")
	colorRed      = ansi.ColorFunc("red")
	colorDim      = ansi.ColorFunc("default+d")
	colorBold     = ansi.ColorFunc("default+b")
	colorBoldCyan = ansi.ColorFunc("cyan+b")
	colorBoldRed  = ansi.ColorFunc("red+b")

	// Table header styling matches gh CLI's iostreams.ColorScheme.TableHeader:
	// dim + underlined for dark themes, dim + underlined for unknown themes
	// (no theme detection in go-gh's term package). gh uses `white+du` for
	// dark and `black+hu` for light; we use `default+du` so it adapts to the
	// terminal's foreground color without us having to detect theme.
	colorTableHeader = ansi.ColorFunc("default+du")
)

// sizeColor: traffic light. M stays neutral so outliers visually pop.
func sizeColor(bucket string, enabled bool) string {
	if !enabled {
		return bucket
	}
	switch bucket {
	case "XS", "S":
		return colorGreen(bucket)
	case "L":
		return colorYellow(bucket)
	case "XL":
		return colorRed(bucket)
	default:
		return bucket
	}
}

// riskColor: low dim, high bold red.
func riskColor(label string, enabled bool) string {
	if !enabled {
		return label
	}
	if label == "high" {
		return colorBoldRed(label)
	}
	return colorDim(label)
}

func dim(s string, enabled bool) string {
	if !enabled {
		return s
	}
	return colorDim(s)
}

// Human writes a TTY-friendly summary for one PR. The caller passes the
// gh term so we can detect TTY/color/width consistently with gh itself.
func Human(w io.Writer, repo string, s score.PRScore, t term.Term) {
	isTTY := t.IsTerminalOutput()
	color := t.IsColorEnabled()
	width, _, _ := t.Size()
	if width <= 0 {
		width = 80
	}

	fmt.Fprintf(w, "%s#%d\n", repo, s.PR.Number)
	// Header block: %-14s pads labels to 14 chars (`Size factor:` at 12 +
	// 2-space min gap). Same on TTY and pipe.
	fmt.Fprintf(w, "  %-14s%d\n", "LOC:", s.LOC)
	fmt.Fprintf(w, "  %-14s%s\n", "Size label:", sizeColor(string(s.Bucket), color))
	fmt.Fprintf(w, "  %-14s%.3f\n", "Size factor:", s.SizeFactor)
	fmt.Fprintf(w, "  %-14s%s\n", "Risk label:", riskColor(riskLabel(s.Risk), color))
	fmt.Fprintf(w, "  %-14s%.3f\n", "Risk factor:", s.Risk)
	fmt.Fprintf(w, "  %-14s%.3f\n", "Normal CRU:", s.CRU())

	if !s.HasCodeowners {
		fmt.Fprintf(w, "  %-14s%.3f\n", "Total CRU:", s.CRU())
		fmt.Fprintf(w, "  %-14s%s\n", "Owners:", dim("no CODEOWNERS file in repo", color))
		return
	}

	fmt.Fprintf(w, "  %-14s%.3f\n", "Total CRU:", s.AuthorCRU())
	writeOwnerTable(w, s, isTTY, color, width)
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
	// OWNER header gets the same 2-space indent the `* `/`  ` marker eats
	// on data rows below, so the column visually starts under the names.
	headerColor := func(v string) string { return v }
	if color {
		headerColor = colorTableHeader
	}
	tp.AddField("  OWNER", tableprinter.WithColor(headerColor))
	tp.AddField("LOC", tableprinter.WithColor(headerColor), tableprinter.WithPadding(padLeft))
	tp.AddField("SHARE", tableprinter.WithColor(headerColor), tableprinter.WithPadding(padLeft))
	tp.AddField("CRU", tableprinter.WithColor(headerColor), tableprinter.WithPadding(padLeft))
	tp.EndRow()

	mySet := makeIdentitySet(s.MyIdentities)

	// Supplemental user row first.
	if s.MyLogin != "" && s.MyOwnedLOC > 0 {
		addOwnerRow(tp, true, s.MyLogin, s.MyOwnedLOC, s.MyShare, s.MyCRU, true, false, color)
	}

	for _, o := range s.SortedOwners() {
		isTeamYou := mySet[strings.ToLower(o.Owner)]
		addOwnerRow(tp, isTeamYou, displayOwner(o.Owner), o.OwnedLOC, o.Share, o.Score, false, isTeamYou, color)
	}

	if s.UnownedChanges > 0 {
		ushare := 0.0
		if s.LOC > 0 {
			ushare = float64(s.UnownedChanges) / float64(s.LOC)
		}
		ownerCell := "  (unowned)"
		if color {
			tp.AddField(ownerCell, tableprinter.WithColor(colorDim))
		} else {
			tp.AddField(ownerCell)
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

	// Footnote: render only when the team enumeration was incomplete AND
	// the user didn't surface in the table (no direct @login match).
	// This is the Codespaces case: a default GITHUB_TOKEN can't read
	// team membership, so a PR owned by a team the user is in shows up
	// with no user row. We tell them once, quietly, in the right place.
	if !s.TeamsResolved && s.MyLogin != "" && s.MyOwnedLOC == 0 {
		note := fmt.Sprintf("(team memberships for @%s unavailable; needs read:org)", s.MyLogin)
		if color {
			fmt.Fprintf(w, "  %s\n", colorDim(note))
		} else {
			fmt.Fprintf(w, "  %s\n", note)
		}
	}
}

// addOwnerRow appends one owner row. The `* ` / `  ` marker is part of
// the OWNER cell itself (not a separate column) so we don't pay a 2-space
// delimiter for it. Coloring is applied only to the owner name portion,
// leaving the marker plain (gh's `gh auth status` does the same with its
// active-account `*`).
func addOwnerRow(tp tableprinter.TablePrinter, marked bool, owner string, loc int, share, cru float64, isYou, isTeamYou, color bool) {
	marker := "  "
	if marked {
		marker = "* "
	}
	// Color only the name; marker stays plain so it never looks "off".
	var cell string
	switch {
	case isYou && color:
		cell = marker + colorBoldCyan(owner)
	case isTeamYou && color:
		cell = marker + colorBold(owner)
	default:
		cell = marker + owner
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
