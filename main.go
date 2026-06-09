// Command gh-cru scores GitHub pull requests using the Code Review Unit
// formula (https://github.com/laserlemon/cru).
//
// Subcommands:
//
//	gh cru view  [flags] <pr-ref>... | -    Score specific PRs (default)
//	gh cru list  <gh pr list flags>          Score the result of gh pr list
//
// `gh cru` with no subcommand is an alias for `gh cru view`, so existing
// invocations (bare numbers, URLs, xargs pipelines) work unchanged.
package main

import (
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
	noPersonalFlag       bool
	highRiskLabelsFlag   []string // any matching label triggers high risk (4×); default ["risk:high"]
	mediumRiskLabelsFlag []string // any matching label triggers medium risk (2×); default ["risk:medium"]. High wins over medium.
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
		Short: "Score GitHub PRs using the Code Review Unit (CRU) formula",
		Long: `Score GitHub pull requests using the Code Review Unit formula.

Subcommands:

  gh cru view  Score specific PRs (the default; alias of the bare command)
  gh cru list  Run "gh pr list" and score every PR it returns

Run "gh cru view --help" for details on PR refs, stdin, and JSON input.`,
		SilenceUsage: true,
		// Root delegates to view when called with positional args (or "-"
		// on stdin). This preserves "gh cru 123" and "gh pr list ... |
		// gh cru" without forcing users to type the subcommand.
		RunE:               runView,
		DisableAutoGenTag:  true,
		Args:               cobra.ArbitraryArgs,
		SilenceErrors:      false,
		TraverseChildren:   false,
	}
	root.PersistentFlags().StringVarP(&repoFlag, "repo", "R", "",
		"Default repo for bare PR numbers (owner/name)")
	root.PersistentFlags().BoolVar(&jsonFlag, "json", false,
		"Emit one JSON object per PR (NDJSON)")
	root.PersistentFlags().BoolVar(&noOwnersFlag, "skip-ownership", false,
		"Skip CODEOWNERS lookups; treat ownership as 1.0 (size×risk only)")
	root.PersistentFlags().BoolVar(&noPersonalFlag, "skip-personal", false,
		"Skip fetching your team memberships; no YOUR CRU line")
	root.PersistentFlags().StringSliceVar(&highRiskLabelsFlag, "high-risk-label", []string{"risk:high"},
		"PR label(s) that mark high risk (4× multiplier). Repeat or comma-separate for multiple")
	root.PersistentFlags().StringSliceVar(&mediumRiskLabelsFlag, "medium-risk-label", []string{"risk:medium"},
		"PR label(s) that mark medium risk (2× multiplier). Repeat or comma-separate for multiple. High wins if both match")

	root.AddCommand(newViewCmd())
	root.AddCommand(newListCmd())
	return root
}

func newViewCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "view [flags] <pr-ref>... | -",
		Short: "Score specific pull requests by ref, URL, or piped JSON",
		Long: `Score one or more GitHub pull requests.

A <pr-ref> can be:

  123                                     bare number; --repo or git context required
  owner/name#123                          shorthand
  https://github.com/owner/name/pull/123  full URL
  -                                       read JSON from stdin

Stdin JSON accepts a single object, a JSON array, or NDJSON (one object per
line). Each object may carry just a ref (url, repository, number) or include
pre-fetched scoring fields (additions, deletions, files, labels, mergeCommit,
…) to skip the PR API call. JSON-supplied repo info always overrides --repo.

Examples:

  gh cru 1234                             score one PR
  gh cru owner/repo#1234 other/repo#9     score across repos
  gh pr list --state merged --limit 10 \
    --json url,number,repository,additions,deletions,changedFiles,baseRefName,mergeCommit,labels,author,state,files \
    | gh cru view -                       pre-fetched scoring via pipe`,
		SilenceUsage: true,
		Args:         cobra.ArbitraryArgs,
		RunE:         runView,
	}
}

func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list [gh pr list flags]",
		Short: "Run `gh pr list` and score every PR it returns",
		Long: `Run "gh pr list" with the given flags and score every PR returned,
in the order gh returned them.

All flags after "list" are forwarded to "gh pr list" unchanged, with one
exception: --json is overridden (we need a specific field set). If you pass
--json explicitly, your value is silently replaced with the fields gh-cru
requires to score without extra API calls.

The CRU rendering flags (--json on gh-cru itself, --skip-ownership, etc.)
must appear BEFORE the "list" word so cobra picks them up:

  gh cru --json list --state open                 NDJSON output
  gh cru list --state merged --limit 50           default human output
  gh cru list --author @me --state open
  gh cru list --repo owner/name --label bug
  gh cru list --search "is:open updated:>2026-01-01"

Empty result exits 0 with no output. A failing PR (deleted file, auth
error) aborts the whole batch — use --limit and re-run with the failing
range narrowed to bisect.`,
		SilenceUsage:       true,
		DisableFlagParsing: true, // everything after "list" passes to gh pr list
		RunE:               runList,
	}
}

// runView is the workhorse: scores everything in args + stdin against the
// shared flag values. Called by both `gh cru` (root alias) and `gh cru view`.
func runView(cmd *cobra.Command, args []string) error {
	defOwner, defRepo := defaultRepo()

	// Stdin handling: literal "-" in args, or auto-detect when args are
	// otherwise empty and stdin is a pipe (non-TTY).
	stdinIsPipe := input.StdinIsPipe(os.Stdin)
	inputs, source, err := input.Parse(args, os.Stdin, stdinIsPipe, defOwner, defRepo)
	if err != nil {
		return err
	}
	if len(inputs) == 0 {
		// Help when called with no args AND no piped stdin — typical
		// "I forgot how to use this" case.
		if source == input.SourceNone {
			return fmt.Errorf("nothing to score: pass a PR ref or pipe JSON to stdin (see --help)")
		}
		return nil // empty pipe = no-op success
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
// then pipes the output into the scoring path.
//
// `list` uses DisableFlagParsing so the user can pass any `gh pr list`
// flag without cobra trying to interpret it. The catch: cobra also stops
// parsing OUR persistent flags (--json, --repo, …) when they appear next
// to the list invocation. We re-parse them manually here so
// `gh cru --json list ...` and `gh cru list --json` both work.
func runList(cmd *cobra.Command, args []string) error {
	args = extractRootFlags(args)

	// gh pr list arg construction: copy user args, drop any --json /
	// --jq the user provided (silently — see help text), tack on our
	// required --json field set.
	forwarded := stripJSONFlags(args)
	cmdArgs := append([]string{"pr", "list"}, forwarded...)
	cmdArgs = append(cmdArgs, "--json", strings.Join(listJSONFields, ","))

	gh := exec.Command("gh", cmdArgs...)
	gh.Stderr = os.Stderr // surface gh's own errors directly
	out, err := gh.Output()
	if err != nil {
		return fmt.Errorf("gh pr list: %w", err)
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" || trimmed == "[]" {
		// Empty result is success, no output.
		return nil
	}

	// Hand off to runView with the JSON payload as if the user had typed
	// `gh cru view -`. We pipe the captured bytes through os.Stdin so
	// input.Parse sees them; swapping os.Stdin is simpler than plumbing
	// a custom reader through the input package.
	r, w, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("pipe: %w", err)
	}
	go func() {
		_, _ = w.Write(out)
		_ = w.Close()
	}()
	origStdin := os.Stdin
	os.Stdin = r
	defer func() {
		os.Stdin = origStdin
		_ = r.Close()
	}()
	return runView(cmd, []string{"-"})
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
// gh-cru-only flags (stripped): --json, --skip-ownership, --skip-personal,
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
		case "--skip-personal":
			noPersonalFlag = true
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
//   - reviewRequests / assignees not currently used; punt to v0.3.0+.
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
			// --json <value> or --jq <value> — consume next token too,
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
// the formatted score.
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
	if noPersonalFlag || noOwnersFlag {
		return "", nil, true
	}
	login, ids, ok, err := client.AuthIdentities()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: could not resolve your identities: %v (continuing without YOUR CRU)\n", err)
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
