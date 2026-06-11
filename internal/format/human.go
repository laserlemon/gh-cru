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
	// headers; `label` colorizes after padding so visible-width math
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
