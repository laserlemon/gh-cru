package format

import (
	"strings"

	"github.com/cli/go-gh/v2/pkg/text"
	"github.com/mgutz/ansi"

	"github.com/laserlemon/cru"
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
	// colorBold is bold on the terminal's default foreground, matching how
	// `gh pr view` renders a PR title. No color escape on the text itself,
	// so it stays legible in any theme.
	colorBold = ansi.ColorFunc("default+b")

	// Muted gray for incidental text: the formula-block labels, the
	// "N lines" annotation, the "CRU" unit, and the gray owner/repo#N
	// reference in the heading. brightblack (ANSI 90) reads as a stable
	// gray on both light and dark terminals, unlike the dim attribute,
	// which many terminals render inconsistently or not at all.
	colorMuted     = ansi.ColorFunc("black+h")
	colorMutedBold = ansi.ColorFunc("black+hb")

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
	colorRiskMedium = ansi.ColorFunc("214+b") // amber/orange, between teal and red on the heat axis
	colorRiskHigh   = ansi.ColorFunc("88+b")  // blood red

	// Table header styling: brightblack + underlined. The underline marks
	// the column anchors (matching gh CLI's `gh pr checks` convention,
	// including a single underlined space for the marker gutter); the
	// brightblack keeps the header the same stable gray as the rest of the
	// incidental text so the data rows below own the eye.
	colorTableHeader = ansi.ColorFunc("black+hu")
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

// displayOwner strips the leading "@" from CODEOWNERS strings for display.
func displayOwner(s string) string {
	return strings.TrimPrefix(s, "@")
}

// muted wraps incidental gray text (the "N lines" annotation, the "CRU"
// unit, the heading's owner/repo#N reference). Returns bare text when
// color is disabled.
func muted(s string, enabled bool) string {
	if !enabled {
		return s
	}
	return colorMuted(s)
}

// makeIdentitySet returns a lowercased lookup set for CODEOWNERS-style
// identity strings (used by both Human and JSON to detect "is this row me?").
func makeIdentitySet(ids []string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[strings.ToLower(id)] = true
	}
	return m
}
