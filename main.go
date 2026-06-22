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
	repoFlag             string
	jsonFlag             bool
	noOwnersFlag         bool
	anonymousFlag        bool
	highRiskLabelsFlag   []string // any matching label triggers high risk (4×); default ["risk:high"]
	mediumRiskLabelsFlag []string // any matching label triggers medium risk (2×); default ["risk:medium"]. High wins over medium.
	webFlag              bool     // gh pr view --web; rejected by gh cru (hidden, handled in runView)
	commentsFlag         bool     // gh pr view --comments; rejected by gh cru (hidden, handled in runView)
)

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
	}
	root.PersistentFlags().StringVarP(&repoFlag, "repo", "R", "",
		"Default repo for the PR (owner/name)")
	root.PersistentFlags().BoolVar(&jsonFlag, "json", false,
		"Emit the measurement as JSON")
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

  gh cru --json list --state open                 NDJSON output
  gh cru list --state merged --limit 50           default human output
  gh cru list --author @me --state open
  gh cru list --repo owner/name --label bug
  gh cru list --search "is:open updated:>2026-01-01"

Empty result exits 0 with no output. A failing PR (deleted file, auth
error) aborts the whole batch; use --limit and re-run with the failing
range narrowed to bisect.`,
		SilenceUsage:       true,
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
	cmdArgs = append(cmdArgs, "--json", strings.Join(listJSONFields, ","))

	out, err := runGH(cmdArgs)
	if err != nil {
		return err
	}
	return scoreJSON(out)
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
func runGH(args []string) ([]byte, error) {
	gh := exec.Command("gh", args...)
	gh.Stderr = os.Stderr
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
// array / NDJSON from gh pr list) into Inputs and scores each one. Shared by
// runView and runList so the scoring engine lives in exactly one place.
func scoreJSON(jsonOut []byte) error {
	trimmed := strings.TrimSpace(string(jsonOut))
	if trimmed == "" || trimmed == "[]" {
		// Empty result (e.g. gh pr list matched nothing) is success.
		return nil
	}

	defOwner, defRepo := defaultRepo()
	inputs, _, err := input.Parse(nil, bytes.NewReader(jsonOut), true, defOwner, defRepo)
	if err != nil {
		return err
	}
	if len(inputs) == 0 {
		return nil
	}

	client, err := ghc.NewClient()
	if err != nil {
		return err
	}
	myLogin, myIdentities, teamsOK := resolveMe(client)

	exitErr := 0
	for i, in := range inputs {
		if i > 0 {
			// Blank line between PRs so headings have breathing room.
			fmt.Println()
		}
		if err := scoreOne(client, in, myLogin, myIdentities, teamsOK); err != nil {
			fmt.Fprintf(os.Stderr, "skip %s/%s#%d: %v\n", in.Ref.Owner, in.Ref.Repo, in.Ref.Number, err)
			exitErr = 1
			continue
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
	cmdArgs = append(cmdArgs, "--json", strings.Join(listJSONFields, ","))

	out, err := runGH(cmdArgs)
	if err != nil {
		return err
	}
	// gh pr list emits a JSON array; scoreJSON handles array, NDJSON, and
	// the empty-result case uniformly with the view path.
	return scoreJSON(out)
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
			jsonFlag = true
			continue
		case "--skip-ownership":
			noOwnersFlag = true
			continue
		case "--anonymous":
			anonymousFlag = true
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

// listJSONFields is the field set gh-cru asks `gh pr list` to emit so the
// scorer can run without per-PR API fetches. Order doesn't matter to gh.
//
// Notable omissions in `gh pr list --json`:
//   - No `merged` boolean (use state == "MERGED").
//   - No `repository` field (single-repo command; we derive owner/repo from
//     --repo or the URL). When gh ever adds --search across repos, we'll
//     need a per-row repo block; today we don't.
//   - body, comments, reviews not used by scoring (large payloads).
//   - reviewRequests / assignees not used by scoring; omitted to keep the
//     payload lean.
var listJSONFields = []string{
	"url",
	"number",
	"title",
	"state",
	"author",
	"additions",
	"deletions",
	"changedFiles",
	"baseRefName",
	"mergeCommit",
	"mergedAt", // proxy for "merged?" since gh pr list lacks `merged` bool
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

// scoreOne fetches whatever the Input doesn't already carry and writes
// the formatted measurement.
func scoreOne(client *ghc.Client, in input.Input, myLogin string, myIdentities []string, teamsOK bool) error {
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
			return err
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
				return err
			}
			files = fetched
		}
		var err error
		owners, _, err = client.FetchCodeowners(in.Ref.Owner, in.Ref.Repo, pr.CodeownersRef())
		if err != nil {
			return err
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
	if jsonFlag {
		return format.JSON(os.Stdout, repoStr, s)
	}
	format.Human(os.Stdout, repoStr, s, term.FromEnv())
	return nil
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
