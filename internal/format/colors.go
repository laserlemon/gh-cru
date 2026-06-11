package format

import (
	"fmt"
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
	colorRiskMedium = ansi.ColorFunc("214+b") // amber/orange, between teal and red on the heat axis
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
	// from the repo palette above. Same 68-color WCAG-safe pool: when
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

// displayOwner strips the leading "@" from CODEOWNERS strings for display.
func displayOwner(s string) string {
	return strings.TrimPrefix(s, "@")
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
