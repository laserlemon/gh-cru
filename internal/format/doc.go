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
//
// File layout in this package:
//
//   - colors.go  palette + small color helpers
//   - human.go   Human renderer (header block + tableprinter)
//   - json.go    JSON renderer (compact NDJSON)
package format
