package format

import (
	"fmt"
	"io"
	"strings"

	"github.com/cli/go-gh/v2/pkg/tableprinter"
	"github.com/cli/go-gh/v2/pkg/term"

	"github.com/laserlemon/gh-cru/internal/score"
)

// Human writes a TTY-friendly summary for one PR. The caller passes the
// gh term so we can detect TTY/color/width consistently with gh itself.
//
// Layout:
//
//	<title>  owner/repo#N            (bold title + gray ref, mirrors gh pr view)
//
//	Size  <bucket>  <factor>  <n> lines
//	Risk  <tier>    <mult>
//	Base            <base>    CRU    (Base = Size × Risk, the review weight)
//
//	   CODE OWNER       LINES   SHARE    CRU
//	*  acme/some-team    34   29.3%  0.585   (named owners: plain rows)
//	•  acme/other-team   80   68.9%  1.378
//	~  Unowned            2    1.7%  0.034   (summary rows: gray+bold)
//	+  All ownership    116  100.0%  2.000
//	>  Your ownership     0    0.0%  0.000   (only when you own something)
//
//	Calculating your CRU requires read:org authorization to
//	read your team memberships.   (only when teams couldn't be enumerated)
//
// PR state/author/diffstat are omitted: callers running batches typically
// already know what PR they asked for, and `gh pr view` covers the
// metadata.
func Human(w io.Writer, repo string, s score.PRScore, t term.Term) {
	isTTY := t.IsTerminalOutput()
	color := t.IsColorEnabled()
	width, _, _ := t.Size()
	if width <= 0 {
		width = 80
	}

	// Always-on heading, mirroring `gh pr view`: the PR title in bold,
	// then a gray `owner/repo#N` reference on the same line. gh renders
	// its title in bold default-fg and incidental refs in muted gray, so
	// following both keeps gh cru's heading consistent with the rest of
	// the gh CLI. No state (Open/Merged/Closed) line: a CRU score isn't a
	// PR view, so that row would just be noise.
	ref := muted(fmt.Sprintf("%s#%d", repo, s.PR.Number), color)
	if title := s.PR.Title; title != "" {
		if color {
			title = colorBold(title)
		}
		fmt.Fprintf(w, "%s %s\n\n", title, ref)
	} else {
		fmt.Fprintf(w, "%s\n\n", ref)
	}

	// Formula block: the three factors that make up the base CRU. Rendered
	// through the same tableprinter as the owner table (see
	// writeFormulaBlock) so its columns content-size and space themselves
	// identically: a 2-space gap between columns, numeric values right-
	// aligned (so the factors line up on the decimal), and the "N lines" /
	// "CRU" annotation in its own trailing column. Base is Size × Risk, the
	// owner-agnostic review weight; "CRU" rides as a trailing gray unit so
	// every number below it is silently understood to be in CRU.
	writeFormulaBlock(w, s, isTTY, color, width)

	// --skip-ownership: CODEOWNERS was never consulted, so there's no
	// ownership to show. End on the Base CRU line above rather than
	// fabricate a 100%-unowned table (which would imply we looked).
	if s.OwnershipSkipped {
		return
	}

	fmt.Fprintln(w)
	writeOwnerTable(w, s, isTTY, color, width)
}

// writeFormulaBlock renders the Size/Risk/Base factor block as a content-
// sized table so its columns obey the same rules as the owner table: each
// column is as wide as its widest cell, columns are separated by the
// tableprinter's standard 2-space delimiter, and the numeric value column
// is right-aligned so the factors line up on the decimal point. The grade
// (Size bucket / Risk tier) carries its semantic color; the value carries
// none (default fg, theme-safe); the trailing "N lines" / "CRU" annotation
// is gray. Risk has no annotation and Base has no grade, so those cells are
// empty but still occupy their column, keeping every value and the "CRU"
// unit aligned under the rows above. Like the owner table, this degrades to
// tab-separated output off a TTY.
func writeFormulaBlock(w io.Writer, s score.PRScore, isTTY, color bool, width int) {
	tp := tableprinter.New(w, isTTY, width)

	labelColor := func(v string) string { return v }
	mutedColor := func(v string) string { return v }
	if color {
		labelColor = colorMutedBold
		mutedColor = colorMuted
	}

	sizeBucket := s.Size.String()

	// Size: grade = bucket (size color), value = size factor, annotation
	// = "N lines". The first row defines the column count (4), so it carries
	// every column even though Risk omits the annotation.
	tp.AddField("Size", tableprinter.WithColor(labelColor))
	tp.AddField(sizeBucket, tableprinter.WithColor(func(v string) string {
		return sizeColor(v, sizeBucket, color)
	}))
	tp.AddField(fmt.Sprintf("%.3f", float64(s.Size)), tableprinter.WithPadding(padLeft))
	tp.AddField(fmt.Sprintf("%d lines", s.LOC), tableprinter.WithColor(mutedColor))
	tp.EndRow()

	// Risk: grade = tier (risk color), value = risk multiplier, no annotation.
	tp.AddField("Risk", tableprinter.WithColor(labelColor))
	tp.AddField(s.Risk.String(), tableprinter.WithColor(func(v string) string {
		return riskColor(v, s.Risk, color)
	}))
	tp.AddField(fmt.Sprintf("%.3f", s.Risk.Multiplier()), tableprinter.WithPadding(padLeft))
	tp.EndRow()

	// Base: no grade (empty cell holds the column), value = Size × Risk,
	// annotation = "CRU" (the unit). The empty grade keeps Base's value
	// aligned under the Size factor and Risk multiplier above it.
	tp.AddField("Base", tableprinter.WithColor(labelColor))
	tp.AddField("")
	tp.AddField(fmt.Sprintf("%.3f", s.CRU()), tableprinter.WithPadding(padLeft))
	tp.AddField("CRU", tableprinter.WithColor(mutedColor))
	tp.EndRow()

	if err := tp.Render(); err != nil {
		fmt.Fprintf(w, "  (formula render error: %v)\n", err)
	}
}

// writeOwnerTable uses cli/go-gh's tableprinter so column widths, padding,
// truncation, and TTY/pipe degradation are handled the same way the gh
// CLI itself does it. On a TTY: aligned columns, color, truncation if
// the terminal is narrow. Off a TTY: tab-separated, no color, no
// truncation - `gh cru | awk` works.
//
// Layout uses a dedicated 1-char gutter column for the row-type marker:
//
//	   CODE OWNER             LINES   SHARE    CRU
//	=  laserlemon              20   40.0%  0.800   (direct match)
//	*  acme/big-orca           80   40.0%  0.800   (team you're on)
//	•  acme/web-team          120   60.0%  1.200   (someone else)
//	~  Unowned                 60   30.0%  0.600   (no CODEOWNERS rule)
//	+  All ownership          200  103.4%  2.069   (team review burden)
//	>  Your ownership          61   52.6%  1.052   (your slice; if any)
//
// Data rows (=, *, •) render plain: the marker glyph alone signals
// whether a row is yours (=/*), so no color is needed and the numeric
// columns read cleanly. The three trailing summary rows (~, +, >) render
// in gray+bold, matching the formula block's labels, so they frame the
// raw owner data as computed totals. SHARE is a percentage; CRU is each
// row's review burden. The marker column header is a single underlined
// space, matching the `gh pr checks` convention for status-marker columns.
func writeOwnerTable(w io.Writer, s score.PRScore, isTTY, color bool, width int) {
	tp := tableprinter.New(w, isTTY, width)

	// Header. Right-align numeric columns; underline + brightblack styling
	// matches gh CLI's tableprinter convention (iostreams.ColorScheme.
	// TableHeader). The marker column header is a single space, styled the
	// same so it reads as an underlined gutter aligned with the markers.
	headerColor := func(v string) string { return v }
	if color {
		headerColor = colorTableHeader
	}
	tp.AddField(" ", tableprinter.WithColor(headerColor))
	tp.AddField("CODE OWNER", tableprinter.WithColor(headerColor))
	tp.AddField("LINES", tableprinter.WithColor(headerColor), tableprinter.WithPadding(padLeft))
	tp.AddField("SHARE", tableprinter.WithColor(headerColor), tableprinter.WithPadding(padLeft))
	tp.AddField("CRU", tableprinter.WithColor(headerColor), tableprinter.WithPadding(padLeft))
	tp.EndRow()

	mySet := makeIdentitySet(s.MyIdentities)
	myLoginKey := "@" + strings.ToLower(s.MyLogin)

	// Data rows: every real CODEOWNERS owner, ranked by descending CRU
	// (SortedOwners). The synthetic "unowned" owner is held back and
	// rendered as a summary row below, so the data section is strictly the
	// named owners.
	for _, o := range s.SortedOwners() {
		if o.Owner == score.UnownedOwnerLabel {
			continue
		}
		isDirectYou := s.MyLogin != "" && strings.ToLower(o.Owner) == myLoginKey
		isTeamYou := !isDirectYou && mySet[strings.ToLower(o.Owner)]
		addOwnerRow(tp, o, isDirectYou, isTeamYou)
	}

	// Summary rows, gray+bold to frame them as computed totals:
	//   ~ Unowned        lines matched by no CODEOWNERS rule (only if any)
	//   + All ownership  sum across every owner incl. unowned = Totals().CRU
	//   > Your ownership your slice (only when you own something)
	if u, ok := s.OwnershipMap[score.UnownedOwnerLabel]; ok && u.OwnedLOC > 0 {
		addSummaryRow(tp, "~", "Unowned", u.OwnedLOC, u.Share, u.Score, color)
	}

	all := s.Totals()
	addSummaryRow(tp, "+", "All ownership", all.Lines, all.Share, all.CRU, color)

	// Your ownership: shown whenever we know who you are, even if your
	// share is zero. Keying on identity (not MyOwnedLOC > 0) keeps the
	// human output and the JSON `you` block consistent: both surface the
	// moment authentication resolves an identity, so "you own nothing here"
	// reads as an explicit 0.000 row rather than a silent omission.
	if s.MyLogin != "" {
		addSummaryRow(tp, ">", "Your ownership", s.MyOwnedLOC, s.MyShare, s.MyCRU, color)
	}

	if err := tp.Render(); err != nil {
		fmt.Fprintf(w, "  (table render error: %v)\n", err)
	}

	// Footnote: render only when team enumeration was incomplete AND the
	// user didn't surface as a direct @login owner. The Codespaces default
	// GITHUB_TOKEN is the common case: a PR owned by a team the user is in
	// shows up with no `*` marker and "Your ownership" reads 0, which would
	// understate their real stake, so we explain why.
	if !s.TeamsResolved && s.MyLogin != "" && s.MyOwnedLOC == 0 {
		fmt.Fprintln(w)
		note := "Calculating your CRU requires read:org authorization to read your team memberships."
		if color {
			fmt.Fprintln(w, muted(note, color))
		} else {
			fmt.Fprintln(w, note)
		}
	}
}

// addOwnerRow appends one named-owner data row. The marker glyph signals
// the row's relationship to the current user:
//
//   - `=` direct @login match (you specifically own these lines)
//   - `*` team membership match (a team you're on owns these)
//   - `•` someone else owns these
//
// Rows render plain (no color): the glyph alone carries the "is this me?"
// signal, and uncolored numbers stay easy to scan against each other and
// against the summary rows below. The marker lives in its own 1-char
// column so owner names stay vertically aligned. SHARE is formatted as a
// percentage; CRU is the owner's review-burden score.
func addOwnerRow(tp tableprinter.TablePrinter, o score.Ownership, isDirectYou, isTeamYou bool) {
	marker := "•"
	switch {
	case isDirectYou:
		marker = "="
	case isTeamYou:
		marker = "*"
	}
	plain := func(s string) string { return s }
	tp.AddField(marker, tableprinter.WithColor(plain))
	tp.AddField(displayOwner(o.Owner), tableprinter.WithColor(plain))
	tp.AddField(fmt.Sprintf("%d", o.OwnedLOC), tableprinter.WithPadding(padLeft))
	tp.AddField(fmt.Sprintf("%.1f%%", o.Share*100), tableprinter.WithPadding(padLeft))
	tp.AddField(fmt.Sprintf("%.3f", o.Score), tableprinter.WithPadding(padLeft))
	tp.EndRow()
}

// addSummaryRow appends one of the trailing computed-total rows (~ Unowned,
// + All ownership, > Your ownership). The marker and label render in
// gray+bold so the row reads as a summary, framing the plain data rows
// above; the numeric columns render plain so they line up with the data.
func addSummaryRow(tp tableprinter.TablePrinter, marker, name string, loc int, share, cru float64, color bool) {
	cellColor := func(s string) string { return s }
	if color {
		cellColor = colorMutedBold
	}
	tp.AddField(marker, tableprinter.WithColor(cellColor))
	tp.AddField(name, tableprinter.WithColor(cellColor))
	tp.AddField(fmt.Sprintf("%d", loc), tableprinter.WithPadding(padLeft))
	tp.AddField(fmt.Sprintf("%.1f%%", share*100), tableprinter.WithPadding(padLeft))
	tp.AddField(fmt.Sprintf("%.3f", cru), tableprinter.WithPadding(padLeft))
	tp.EndRow()
}
