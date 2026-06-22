// Package format renders PRScore results in two modes, modeled on gh's
// own output structure (see cli/cli/pkg/cmd/pr/list and friends):
//
//   - Human: TTY-friendly. A gh-native heading (bold PR title + gray
//     owner/repo#N ref), a Size/Risk/Base formula block, and a gh-style
//     tableprinter for the owner table with semantic size/risk coloring.
//     Honors NO_COLOR and CLICOLOR* env vars via cli/go-gh's term package.
//   - JSON:   structured, pipe-friendly for jq. Mirrors gh's own --json
//     shape: a bare object for one PR (the view path), a JSON array for a
//     batch (the list path), pretty-printed and colorized on a TTY and
//     compact when piped; every float pinned to six decimals.
//
// When stdout isn't a TTY, the tableprinter automatically degrades to
// tab-separated output, so `gh cru 123 | awk` still works, and the
// heading drops color (delegated to the colorizer).
//
// File layout in this package:
//
//   - colors.go  palette + small color helpers
//   - human.go   Human renderer (heading + formula block + owner table)
//   - json.go    JSON renderer (object for view, array for list)
package format
