// Command gh-cru measures GitHub pull requests using the Code Review Unit
// formula (https://github.com/laserlemon/cru).
//
// Subcommands mirror the popular `gh pr` commands:
//
//	gh cru view  [<number> | <url> | <branch>]   Measure one PR (mirrors gh pr view)
//	gh cru list  <gh pr list flags>              Measure every PR gh pr list returns
//
// `gh cru` with no subcommand is an alias for `gh cru view`, so `gh cru`
// (current branch), `gh cru 1234`, and `gh cru my-branch` all work.
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/cli/go-gh/v2/pkg/repository"
	"github.com/cli/go-gh/v2/pkg/term"
	"github.com/hmarr/codeowners"
	"github.com/spf13/cobra"

	"github.com/laserlemon/gh-cru/internal/format"
	ghc "github.com/laserlemon/gh-cru/internal/gh"
	"github.com/laserlemon/gh-cru/internal/input"
	"github.com/laserlemon/gh-cru/internal/score"
)

// Shared flag values. cobra's PersistentFlags on the root makes them
// available to every subcommand without duplicating definitions.
var (
	repoFlag string
	// jsonFlag records whether --json was requested at all; jsonFieldsFlag
	// holds the optional top-level field selection. Bare --json => full
	// object (jsonFlag true, jsonFieldsFlag nil); --json=size,risk =>
	// subset (jsonFlag true, jsonFieldsFlag ["size","risk"]). Equals form
	// only: an optional-value flag can't take a space-separated value
	// without swallowing a following positional (gh cru --json 1234).
	jsonFlag             bool
	jsonRawFlag          string // raw --json value from pflag (root path); normalized by syncJSONFromRaw
	jsonFieldsFlag       []string
	noOwnersFlag         bool
	anonymousFlag        bool
	highRiskLabelsFlag   []string // any matching label triggers high risk (4×); default ["risk:high"]
	mediumRiskLabelsFlag []string // any matching label triggers medium risk (2×); default ["risk:medium"]. High wins over medium.
	webFlag              bool     // gh pr view --web; rejected by gh cru (hidden, handled in runView)
	commentsFlag         bool     // gh pr view --comments; rejected by gh cru (hidden, handled in runView)
)

// jsonNoOptVal is the sentinel pflag assigns to --json when it's given
// with no =value (bare --json). It can't collide with a real selection
// because it contains a NUL byte no gh field name uses. setJSON maps it
// back to "full object, no field filter."
const jsonNoOptVal = "\x00full\x00"

// setJSON records a --json request and parses its optional value. raw is
// the value as seen by either parser: jsonNoOptVal (bare --json => full),
// "" (also full, e.g. the subcommand bare-flag path), or a comma list
// ("size,risk" => subset). Comma-splitting trims blanks so "size," and
// "size,,risk" behave. Idempotent and additive: called from the root
// pflag path and the subcommand hand-parser alike.
func setJSON(raw string) {
	jsonFlag = true
	if raw == "" || raw == jsonNoOptVal {
		return // full object
	}
	for _, f := range strings.Split(raw, ",") {
		if f = strings.TrimSpace(f); f != "" {
			jsonFieldsFlag = append(jsonFieldsFlag, f)
		}
	}
}

func main() {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "gh-cru",
		Short: "Measure GitHub PRs using the Code Review Unit (CRU) formula",
		Long: `Measure GitHub pull requests using the Code Review Unit formula.

Subcommands mirror the popular "gh pr" commands:

  gh cru view  Measure one PR (mirrors "gh pr view"; the default, alias of the bare command)
  gh cru list  Run "gh pr list" and measure every PR it returns

Run "gh cru view --help" for the PR argument forms gh cru view accepts.`,
		SilenceUsage: true,
		// Root delegates to view, so the bare command mirrors "gh pr view"
		// with no subcommand: "gh cru" measures the current branch's PR,
		// "gh cru 1234" / "gh cru my-branch" measure a specific one. Root keeps
		// normal flag parsing (so "gh cru --json 1234" strips --json for us);
		// the view subcommand disables it to forward unknown flags to gh pr
		// view. runView handles both arg shapes.
		RunE:              runView,
		DisableAutoGenTag: true,
		Args:              cobra.ArbitraryArgs,
		// runView enforces the "at most one PR" rule itself (mirroring gh
		// pr view) and main() prints the single error line; silence
		// cobra's own duplicate "Error:" print.
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVarP(&repoFlag, "repo", "R", "",
		"Default repo for the PR (owner/name)")
	root.PersistentFlags().StringVar(&jsonRawFlag, "json", "",
		"Emit the measurement as JSON; --json=<fields> selects top-level keys")
	// Optional value: bare --json carries jsonNoOptVal (full object),
	// --json=size,risk carries the selection. NoOptDefVal is what makes
	// the value optional under pflag.
	root.PersistentFlags().Lookup("json").NoOptDefVal = jsonNoOptVal
	root.PersistentFlags().BoolVar(&noOwnersFlag, "skip-ownership", false,
		"Skip CODEOWNERS entirely; end on Base CRU (size×risk), no ownership table")
	root.PersistentFlags().BoolVar(&anonymousFlag, "anonymous", false,
		"Don't resolve your identity; omit the \"Your ownership\" row")
	root.PersistentFlags().StringSliceVar(&highRiskLabelsFlag, "high-risk-label", []string{"risk:high"},
		"PR label(s) that mark high risk (4× multiplier). Repeat or comma-separate for multiple")
	root.PersistentFlags().StringSliceVar(&mediumRiskLabelsFlag, "medium-risk-label", []string{"risk:medium"},
		"PR label(s) that mark medium risk (2× multiplier). Repeat or comma-separate for multiple. High wins if both match")

	// gh pr view's --web/--comments don't fit a measurement. Register them hidden
	// on root so the bare command ("gh cru 1234 --web") doesn't die with
	// cobra's generic "unknown flag"; runView checks the parsed bools and
	// rejects with a message explaining why. The view subcommand uses
	// DisableFlagParsing, so its path catches them in stripViewFlags instead.
	root.PersistentFlags().BoolVarP(&webFlag, "web", "w", false, "")
	root.PersistentFlags().BoolVarP(&commentsFlag, "comments", "c", false, "")
	_ = root.PersistentFlags().MarkHidden("web")
	_ = root.PersistentFlags().MarkHidden("comments")

	root.AddCommand(newViewCmd())
	root.AddCommand(newListCmd())
	return root
}

func newViewCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "view [<number> | <url> | <branch>]",
		Short: "Measure one pull request (mirrors `gh pr view`)",
		Long: `Measure one GitHub pull request, mirroring "gh pr view".

The PR argument takes the same forms "gh pr view" accepts:

  (none)                                  the PR for the current branch
  123                                     bare number; --repo or git context
  my-feature-branch                       a branch name
  https://github.com/owner/name/pull/123  full URL

gh cru shells out to "gh pr view" to resolve and fetch the PR, then measures
it. A few "gh pr view" flags are intercepted because they don't fit a measurement:

  --json is forced to the field set gh cru needs (your --json controls
    gh cru's OWN output instead, same as "gh cru list").
  --jq/-q and --template/-t are dropped (they format the inner JSON we consume).
  --web/-w and --comments/-c are rejected (gh cru produces a measurement, not a view).
  --repo/-R is honored and forwarded.

Examples:

  gh cru                          measure the current branch's PR
  gh cru 1234                     measure one PR by number
  gh cru my-feature-branch        measure the PR for a branch
  gh cru --repo owner/name 1234   measure a PR in another repo
  gh cru --json 1234              emit the measurement as JSON`,
		SilenceUsage:       true,
		SilenceErrors:      true,
		DisableFlagParsing: true, // forward unknown flags to gh pr view; parse ours by hand
		RunE:               runView,
	}
}

func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list [gh pr list flags]",
		Short: "Run `gh pr list` and measure every PR it returns",
		Long: `Run "gh pr list" with the given flags and measure every PR returned,
in the order gh returned them.

All flags after "list" are forwarded to "gh pr list" unchanged, with one
exception: --json is overridden (we need a specific field set). If you pass
--json explicitly, your value is silently replaced with the fields gh-cru
requires to measure without extra API calls.

The CRU rendering flags (--json on gh-cru itself, --skip-ownership, etc.)
must appear BEFORE the "list" word so cobra picks them up:

  gh cru --json list --state open                 JSON array output
  gh cru list --state merged --limit 50           default human output
  gh cru list --author @me --state open
  gh cru list --repo owner/name --label bug
  gh cru list --search "is:open updated:>2026-01-01"

Empty result exits 0 (--json emits "[]", human output stays silent). A
failing PR (deleted file, auth error) aborts the whole batch; use --limit
and re-run with the failing range narrowed to bisect.`,
		SilenceUsage:       true,
		SilenceErrors:      true,
		DisableFlagParsing: true, // everything after "list" passes to gh pr list
		RunE:               runList,
	}
}

// runView mirrors `gh pr view`: it resolves a single PR (by number, URL,
// branch, or the current branch when no arg is given), fetches the scoring
// fields via `gh pr view --json`, and measures the result. Called by both
// `gh cru` (root alias) and `gh cru view`.
//
// Root parses flags normally, so args reaching runView from the bare command
// have gh-cru's flags already stripped. The view subcommand uses
// DisableFlagParsing, so its args still carry them; extractRootFlags handles
// both (it's a no-op when there's nothing left to find). Whatever positional
// remains, plus any non-gh-cru flags, forward to `gh pr view`.
func runView(cmd *cobra.Command, args []string) error {
	// Reject --web/--comments on the bare-command path, where root parsed
	// them into these bools. (The view subcommand's DisableFlagParsing path
	// catches them in stripViewFlags instead, with the same messages.)
	if webFlag {
		return fmt.Errorf("--web is not supported: gh cru produces a CRU measurement, not a browser view")
	}
	if commentsFlag {
		return fmt.Errorf("--comments is not supported: gh cru produces a CRU measurement, not a comment thread")
	}

	// Pull gh-cru's own flags out of the raw args (harmless when root
	// already parsed them: nothing matches, everything passes through).
	args = extractRootFlags(args)

	// Intercept the gh-pr-view flags that don't fit a measurement: force --json,
	// drop --jq/--template, reject --web/--comments. Leaves --repo and the
	// positional ref in place to forward.
	forwarded, err := stripViewFlags(args)
	if err != nil {
		return err
	}

	// Mirror `gh pr view`, which takes at most one PR. Count the
	// positionals left after flag stripping and reject >1 ourselves with
	// gh's own wording, rather than forwarding them and surfacing gh's
	// "exit status 1" wrapper. The multi-PR path is `gh cru list`
	// (scoreJSON's loop over gh pr list's output), not CLI positionals.
	if n := countPositionals(forwarded); n > 1 {
		return fmt.Errorf("accepts at most 1 arg(s), received %d", n)
	}

	// On the bare command ("gh cru 1234 --repo o/r"), root parses --repo as
	// its own registered flag, so it's already stripped from args and lives
	// in repoFlag. The view subcommand (DisableFlagParsing) leaves --repo in
	// the raw args instead. Re-inject it for gh pr view only when it didn't
	// survive in the forwarded args, so both entry paths reach gh identically
	// and neither double-passes it.
	if repoFlag != "" && !hasRepoFlag(forwarded) {
		forwarded = append(forwarded, "--repo", repoFlag)
	}

	// gh pr view <ref?> --json <our fields>. A missing ref is fine: gh pr
	// view with no positional resolves the current branch's PR.
	cmdArgs := append([]string{"pr", "view"}, forwarded...)
	cmdArgs = append(cmdArgs, "--json", strings.Join(prJSONFields, ","))

	out, err := runGH(cmdArgs)
	if err != nil {
		return err
	}
	// gh pr view resolves a single PR → bare JSON object, mirroring
	// `gh pr view --json` (asArray=false).
	return scoreJSON(out, false)
}

// countPositionals counts the bare positional arguments (PR refs) in a
// flag-stripped arg slice, skipping flags and the value token that
// follows a value-taking flag. After stripViewFlags, the only
// value-taking flag that can survive is --repo/-R (in its space-separated
// spelling); its value must not be miscounted as a PR ref. Used to mirror
// `gh pr view`'s "at most one PR" rule.
func countPositionals(args []string) int {
	n := 0
	skip := false
	for _, a := range args {
		if skip {
			skip = false
			continue
		}
		if a == "--repo" || a == "-R" {
			skip = true // its value is next; not a positional
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue // any other flag (incl. --repo=value / -R=value)
		}
		n++
	}
	return n
}

// hasRepoFlag reports whether a --repo/-R flag (in any of its spellings) is
// already present in an arg slice.
func hasRepoFlag(args []string) bool {
	for _, a := range args {
		if a == "--repo" || a == "-R" ||
			strings.HasPrefix(a, "--repo=") || strings.HasPrefix(a, "-R=") {
			return true
		}
	}
	return false
}

// runGH runs `gh <args>` and returns stdout, surfacing gh's own stderr
// directly so auth/resolution errors reach the user unmodified.
//
// The child gh's stdout is captured as DATA (JSON we parse), never shown,
// so we force it plain regardless of the ambient terminal env. Without
// this, a user (or script) with GH_FORCE_TTY and/or CLICOLOR_FORCE
// exported makes the inner `gh pr view --json` emit colorized,
// pretty-printed JSON with ANSI escapes, which our JSON parser can't read.
// Emptying BOTH forcing vars restores gh's normal "stdout is a pipe →
// plain compact JSON" behavior (NO_COLOR alone does NOT: CLICOLOR_FORCE
// still forces pretty+color over it). gh-cru's OWN output coloring is
// decided separately, from the real stdout term, in the formatters.
func runGH(args []string) ([]byte, error) {
	gh := exec.Command("gh", args...)
	gh.Stderr = os.Stderr
	gh.Env = append(os.Environ(), "GH_FORCE_TTY=", "CLICOLOR_FORCE=", "NO_COLOR=1")
	out, err := gh.Output()
	if err != nil {
		return nil, fmt.Errorf("gh %s: %w", strings.Join(args[:2], " "), err)
	}
	return out, nil
}

// stripViewFlags filters an arg slice headed for `gh pr view`, applying
// gh-cru's interception rules:
//
//   - --json / -q/--jq / -t/--template are dropped: gh-cru forces its own
//     --json field set, so a user-supplied output-format flag would either
//     be overridden (--json) or operate on fields we replaced (--jq/-t). A
//     user's --json on gh cru itself controls gh-cru's OWN output and is
//     consumed earlier by extractRootFlags, not here.
//   - --web/-w and --comments/-c are rejected with an error: gh cru produces
//     a measurement, not a browser view or a comment thread, so silently dropping
//     them would hide that the user asked for something gh cru can't do.
//
// Everything else (the positional ref, --repo/-R, any other gh pr view flag)
// passes through unchanged.
func stripViewFlags(args []string) ([]string, error) {
	out := make([]string, 0, len(args))
	skip := 0
	for i, a := range args {
		if skip > 0 {
			skip--
			continue
		}
		switch {
		case a == "--web" || a == "-w":
			return nil, fmt.Errorf("--web is not supported: gh cru produces a CRU measurement, not a browser view")
		case a == "--comments" || a == "-c":
			return nil, fmt.Errorf("--comments is not supported: gh cru produces a CRU measurement, not a comment thread")
		case a == "--json" || a == "--jq" || a == "-q" || a == "--template" || a == "-t":
			// Consume a following value token when present (--json fields).
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				skip = 1
			}
		case strings.HasPrefix(a, "--json=") || strings.HasPrefix(a, "--jq=") ||
			strings.HasPrefix(a, "-q=") || strings.HasPrefix(a, "--template=") ||
			strings.HasPrefix(a, "-t="):
			// --flag=value form, nothing trailing to skip.
		default:
			out = append(out, a)
		}
	}
	return out, nil
}

// scoreJSON parses gh JSON output (a single object from gh pr view, or an
// array / NDJSON from gh pr list) into Inputs, scores each one, and renders
// the batch. Shared by runView and runList so the scoring engine lives in
// exactly one place.
//
// asArray picks the --json output shape, mirroring gh's own object-vs-array
// convention (decision: gh pr view → object, gh pr list → array):
//   - false (view): a bare JSON object per PR. The view path resolves
//     exactly one PR, so this is a single `{...}`, matching `gh pr view --json`.
//   - true (list): one JSON array over the whole batch, `[{...},{...}]`,
//     matching `gh pr list --json` even for a single (or empty) result.
//
// Human output streams per-PR in both modes (better for long lists than
// buffering). Per-PR fetch/score failures are reported to stderr and
// skipped; the batch still finishes and exits non-zero.
func scoreJSON(jsonOut []byte, asArray bool) error {
	// Validate any --json field selection once, up front, so a bad field
	// name fails before we emit anything (and before per-PR work).
	if jsonFlag {
		if err := format.ValidateJSONFields(jsonFieldsFlag); err != nil {
			return err
		}
	}
	trimmed := strings.TrimSpace(string(jsonOut))
	if trimmed == "" || trimmed == "[]" {
		// Empty result (e.g. gh pr list matched nothing). In list JSON
		// mode emit `[]` so a downstream consumer always gets valid JSON
		// (matching `gh pr list --json` on no matches); object and human
		// modes stay silent.
		if jsonFlag && asArray {
			return format.JSONArray(os.Stdout, nil, term.FromEnv(), jsonFieldsFlag)
		}
		return nil
	}

	defOwner, defRepo := defaultRepo()
	inputs, err := input.Parse(bytes.NewReader(jsonOut), defOwner, defRepo)
	if err != nil {
		return err
	}
	if len(inputs) == 0 {
		if jsonFlag && asArray {
			return format.JSONArray(os.Stdout, nil, term.FromEnv(), jsonFieldsFlag)
		}
		return nil
	}

	client, err := ghc.NewClient()
	if err != nil {
		return err
	}
	myLogin, myIdentities, teamsOK := resolveMe(client)

	exitErr := 0
	printedHuman := false   // for the blank-line separator in human mode
	var items []format.Item // collected for array mode; emitted once at the end
	for _, in := range inputs {
		repoStr, s, err := computeOne(client, in, myLogin, myIdentities, teamsOK)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skip %s/%s#%d: %v\n", in.Ref.Owner, in.Ref.Repo, in.Ref.Number, err)
			exitErr = 1
			continue
		}
		switch {
		case jsonFlag && asArray:
			// Collect; the whole batch emits as one array below.
			items = append(items, format.Item{Repo: repoStr, Score: s})
		case jsonFlag:
			// Bare object per PR (NDJSON when piped over multiple).
			if err := format.JSON(os.Stdout, repoStr, s, term.FromEnv(), jsonFieldsFlag); err != nil {
				return err
			}
		default:
			// Human: blank line between PRs so headings have breathing
			// room. Gate on prior successful output so a leading skip
			// doesn't leave an orphan blank line.
			if printedHuman {
				fmt.Println()
			}
			format.Human(os.Stdout, repoStr, s, term.FromEnv())
			printedHuman = true
		}
	}
	if jsonFlag && asArray {
		if err := format.JSONArray(os.Stdout, items, term.FromEnv(), jsonFieldsFlag); err != nil {
			return err
		}
	}
	if exitErr != 0 {
		os.Exit(exitErr)
	}
	return nil
}

// runList shells out to `gh pr list`, forces the JSON field set we need,
// then scores the output.
//
// `list` uses DisableFlagParsing so the user can pass any `gh pr list`
// flag without cobra trying to interpret it. The catch: cobra also stops
// parsing OUR persistent flags (--json, --repo, …) when they appear next
// to the list invocation. We re-parse them manually here so
// `gh cru --json list ...` and `gh cru list --json` both work.
func runList(cmd *cobra.Command, args []string) error {
	args = extractRootFlags(args)

	// gh pr list arg construction: copy user args, drop any --json /
	// --jq the user provided (silently; see help text), tack on our
	// required --json field set.
	forwarded := stripJSONFlags(args)
	cmdArgs := append([]string{"pr", "list"}, forwarded...)
	cmdArgs = append(cmdArgs, "--json", strings.Join(prJSONFields, ","))

	out, err := runGH(cmdArgs)
	if err != nil {
		return err
	}
	// gh pr list emits a JSON array; scoreJSON handles array, NDJSON, and
	// the empty-result case uniformly with the view path. The list path
	// renders our --json as a JSON array too (asArray=true), mirroring
	// `gh pr list --json`.
	return scoreJSON(out, true)
}

// extractRootFlags pulls gh-cru's persistent root flags out of an
// unparsed args slice (because DisableFlagParsing on the list subcommand
// swallows them). Mutates package-level flag vars as it finds matches,
// returns the args with gh-cru-only flags removed so they don't leak
// into `gh pr list`.
//
// --repo / -R is special: it's meaningful to both gh-cru (default for
// bare numbers, though `list` doesn't use bare numbers) and to gh pr
// list itself. We snoop the value into repoFlag but leave the flag in
// the forwarded args so gh sees it.
//
// --high-risk-label and --medium-risk-label can be passed multiple times
// or comma-separated, matching cobra's StringSliceVar semantics: the
// first user-supplied value replaces the default entirely, then later
// instances append. Comma-separated values are split inside each instance.
//
// gh-cru-only flags (stripped): --json, --skip-ownership, --anonymous,
// --high-risk-label, --medium-risk-label. Anything else passes through unmodified.
func extractRootFlags(args []string) []string {
	// Root (bare command) path: pflag already parsed --json into
	// jsonRawFlag. Sync it here so both entry paths converge on
	// jsonFlag/jsonFieldsFlag. On the subcommand path jsonRawFlag stays
	// empty (the hand-parser below calls setJSON instead), so this is a
	// no-op there. jsonRawFlag != "" means --json was seen: the value is
	// jsonNoOptVal for bare --json or the =selection.
	if jsonRawFlag != "" {
		setJSON(jsonRawFlag)
	}

	out := make([]string, 0, len(args))
	skip := 0

	// makeAccumulator returns a func that mutates a target slice with
	// pflag.StringSliceVar semantics: first user value replaces default,
	// subsequent values append, comma-separated values are split.
	makeAccumulator := func(target *[]string) func(string) {
		seen := false
		return func(raw string) {
			if !seen {
				*target = nil
				seen = true
			}
			for _, v := range strings.Split(raw, ",") {
				if v != "" {
					*target = append(*target, v)
				}
			}
		}
	}
	addHighRiskLabel := makeAccumulator(&highRiskLabelsFlag)
	addMediumRiskLabel := makeAccumulator(&mediumRiskLabelsFlag)

	for i, a := range args {
		if skip > 0 {
			skip--
			continue
		}
		// Booleans (gh-cru-only, no value, strip).
		switch a {
		case "--json":
			setJSON("")
			continue
		case "--skip-ownership":
			noOwnersFlag = true
			continue
		case "--anonymous":
			anonymousFlag = true
			continue
		}
		// --json=<fields>: gh-cru-only, strip; records the selection.
		if strings.HasPrefix(a, "--json=") {
			setJSON(strings.TrimPrefix(a, "--json="))
			continue
		}
		// gh-cru-only string slice flags --high-risk-label / --medium-risk-label (strip).
		if a == "--high-risk-label" {
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				addHighRiskLabel(args[i+1])
				skip = 1
			}
			continue
		}
		if strings.HasPrefix(a, "--high-risk-label=") {
			addHighRiskLabel(strings.TrimPrefix(a, "--high-risk-label="))
			continue
		}
		if a == "--medium-risk-label" {
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				addMediumRiskLabel(args[i+1])
				skip = 1
			}
			continue
		}
		if strings.HasPrefix(a, "--medium-risk-label=") {
			addMediumRiskLabel(strings.TrimPrefix(a, "--medium-risk-label="))
			continue
		}
		// --repo / -R: shared with gh pr list. Snoop value, preserve flag.
		if a == "--repo" || a == "-R" {
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				repoFlag = args[i+1]
			}
			out = append(out, a)
			continue
		}
		if strings.HasPrefix(a, "--repo=") {
			repoFlag = strings.TrimPrefix(a, "--repo=")
			out = append(out, a)
			continue
		}
		if strings.HasPrefix(a, "-R=") {
			repoFlag = strings.TrimPrefix(a, "-R=")
			out = append(out, a)
			continue
		}
		out = append(out, a)
	}
	return out
}

// prJSONFields is the field set gh-cru asks `gh pr view`/`gh pr list` to
// emit so the scorer can run without per-PR API fetches. These are the
// gh `--json` field names (camelCase: mergeCommit, baseRefName),
// distinct from the REST snake_case spellings in
// internal/gh. Order doesn't matter to gh.
//
// Notable omissions in `gh pr list --json`:
//   - No `merged` boolean: state == "MERGED" is authoritative on both the
//     view and list paths, so we key merged-detection on state alone.
//   - No `repository` field (single-repo command; we derive owner/repo from
//     --repo or the URL). When gh ever adds --search across repos, we'll
//     need a per-row repo block; today we don't.
//   - body, comments, reviews not used by scoring (large payloads).
//   - reviewRequests / assignees not used by scoring; omitted to keep the
//     payload lean.
var prJSONFields = []string{
	"url",
	"number",
	"title",
	"state",
	"additions",
	"deletions",
	"baseRefName",
	"mergeCommit",
	"labels",
	"files",
}

// stripJSONFlags removes any --json / --jq / -q the user explicitly
// passed to `gh cru list`. Both --flag=value and --flag value shapes.
// gh's --jq operates on --json output; dropping --json forces us to
// drop --jq too or gh complains.
func stripJSONFlags(args []string) []string {
	out := make([]string, 0, len(args))
	skip := 0
	for i, a := range args {
		if skip > 0 {
			skip--
			continue
		}
		switch {
		case a == "--json" || a == "--jq" || a == "-q":
			// --json <value> or --jq <value>: consume next token too,
			// but only if it doesn't look like another flag.
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				skip = 1
			}
		case strings.HasPrefix(a, "--json=") || strings.HasPrefix(a, "--jq=") || strings.HasPrefix(a, "-q="):
			// --json=value form, nothing to skip after.
		default:
			out = append(out, a)
		}
	}
	return out
}

// computeOne fetches whatever the Input doesn't already carry, scores the
// PR, and returns its "owner/name" repo string plus the score. Rendering is
// the caller's job (scoreJSON), so the same compute path feeds human output,
// per-PR JSON objects, and the batched JSON array without duplicating the
// fetch/score dance.
func computeOne(client *ghc.Client, in input.Input, myLogin string, myIdentities []string, teamsOK bool) (string, score.PRScore, error) {
	var pr ghc.PR
	if in.PR != nil {
		pr = *in.PR
		// Number defensively defaults from the ref when JSON omitted it.
		if pr.Number == 0 {
			pr.Number = in.Ref.Number
		}
	} else {
		fetched, err := client.FetchPR(in.Ref)
		if err != nil {
			return "", score.PRScore{}, err
		}
		pr = fetched
	}

	var files []ghc.File
	var owners codeowners.Ruleset
	if !noOwnersFlag {
		if in.Files != nil {
			files = in.Files
		} else {
			fetched, err := client.FetchPRFiles(in.Ref)
			if err != nil {
				return "", score.PRScore{}, err
			}
			files = fetched
		}
		var err error
		owners, _, err = client.FetchCodeowners(in.Ref.Owner, in.Ref.Repo, pr.CodeownersRef())
		if err != nil {
			return "", score.PRScore{}, err
		}
	}

	s := score.Compute(pr, files, owners, highRiskLabelsFlag, mediumRiskLabelsFlag, myLogin, myIdentities)
	s.TeamsResolved = teamsOK
	s.OwnershipSkipped = noOwnersFlag
	// Probe for the /user/teams silent-empty trap: tokens lacking read:org
	// (the default Codespaces GITHUB_TOKEN, fine-grained PATs without org
	// scope) return 200+[] from /user/teams instead of 403. If we got only
	// the @login back and the PR sits in an org whose teams the token
	// can't HEAD, downgrade TeamsResolved so the formatter shows the
	// missing-team footnote instead of silently zeroing MyCRU.
	if myLogin != "" && teamsOK && len(myIdentities) == 1 && in.Ref.Owner != "" {
		if !client.CanListOrgTeams(in.Ref.Owner) {
			s.TeamsResolved = false
		}
	}
	repoStr := fmt.Sprintf("%s/%s", in.Ref.Owner, in.Ref.Repo)
	return repoStr, s, nil
}

// resolveMe centralizes the identity-resolution dance so view and list
// share it without duplicating the warning path.
func resolveMe(client *ghc.Client) (string, []string, bool) {
	if anonymousFlag || noOwnersFlag {
		return "", nil, true
	}
	login, ids, ok, err := client.AuthIdentities()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: could not resolve your identities: %v (continuing without \"Your ownership\")\n", err)
		return "", nil, true
	}
	return login, ids, ok
}

// defaultRepo returns owner, repo if --repo is set or the current
// directory is a git repo with a github.com remote. Otherwise "", "".
func defaultRepo() (string, string) {
	if repoFlag != "" {
		r, err := repository.Parse(repoFlag)
		if err == nil {
			return r.Owner, r.Name
		}
	}
	r, err := repository.Current()
	if err != nil {
		return "", ""
	}
	return r.Owner, r.Name
}
