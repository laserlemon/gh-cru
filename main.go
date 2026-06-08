// Command gh-cru scores GitHub pull requests using the Code Review Unit
// formula (https://github.com/laserlemon/cru).
//
// Usage:
//
//	gh cru [flags] <pr-ref>...
//
// Each <pr-ref> can be a bare PR number (uses --repo or current git context),
// a shorthand like "owner/repo#123", or a full URL.
package main

import (
	"fmt"
	"os"

	"github.com/cli/go-gh/v2/pkg/repository"
	"github.com/cli/go-gh/v2/pkg/term"
	"github.com/hmarr/codeowners"
	"github.com/spf13/cobra"

	"github.com/laserlemon/gh-cru/internal/format"
	ghc "github.com/laserlemon/gh-cru/internal/gh"
	"github.com/laserlemon/gh-cru/internal/prref"
	"github.com/laserlemon/gh-cru/internal/score"
)

var (
	repoFlag       string
	jsonFlag       bool
	noOwnersFlag   bool
	noPersonalFlag bool
	riskLabelFlag  string
)

func main() {
	root := &cobra.Command{
		Use:   "gh-cru [flags] <pr-ref>...",
		Short: "Score GitHub PRs using the Code Review Unit (CRU) formula",
		Long: `Score one or more GitHub pull requests using the Code Review Unit
formula.

A <pr-ref> can be:

  123                                 (bare number; --repo or git context required)
  owner/name#123                      (shorthand)
  https://github.com/owner/name/pull/123  (URL)

Multiple PRs can be passed in one call, including via xargs:

  gh pr list --state merged --limit 100 --json url --jq '.[].url' | xargs gh cru

CODEOWNERS for each repo is fetched once per invocation and reused across all
PRs from that repo. Your team memberships are fetched once at startup so the
"YOUR CRU" line reflects the PR's actual cost to you as a reviewer.`,
		SilenceUsage: true,
		Args:         cobra.MinimumNArgs(1),
		RunE:         runRoot,
	}
	root.Flags().StringVarP(&repoFlag, "repo", "R", "",
		"Default repo for bare PR numbers (owner/name)")
	root.Flags().BoolVar(&jsonFlag, "json", false,
		"Emit one JSON object per PR (NDJSON)")
	root.Flags().BoolVar(&noOwnersFlag, "skip-ownership", false,
		"Skip CODEOWNERS lookups; treat ownership as 1.0 (size×risk only)")
	root.Flags().BoolVar(&noPersonalFlag, "skip-personal", false,
		"Skip fetching your team memberships; no YOUR CRU line")
	root.Flags().StringVar(&riskLabelFlag, "risk-label", "risk:high",
		"PR label that marks high risk (4× multiplier)")

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func runRoot(cmd *cobra.Command, args []string) error {
	defOwner, defRepo := defaultRepo()

	client, err := ghc.NewClient()
	if err != nil {
		return err
	}

	var myLogin string
	var myIdentities []string
	teamsOK := true
	if !noPersonalFlag && !noOwnersFlag {
		login, ids, ok, err := client.AuthIdentities()
		if err != nil {
			fmt.Fprintf(os.Stderr, "warn: could not resolve your identities: %v (continuing without YOUR CRU)\n", err)
		} else {
			myLogin = login
			myIdentities = ids
			teamsOK = ok
		}
	}

	exitErr := 0
	for i, arg := range args {
		ref, err := prref.Parse(arg, defOwner, defRepo)
		if err != nil {
			fmt.Fprintln(os.Stderr, "skip:", err)
			exitErr = 1
			continue
		}
		if i > 0 {
			// Blank line between PRs so the heading has breathing room
			// above it. Heading itself owns the breathing room below.
			fmt.Println()
		}
		if err := scoreOne(client, ref, myLogin, myIdentities, teamsOK); err != nil {
			fmt.Fprintf(os.Stderr, "skip %s: %v\n", ref, err)
			exitErr = 1
			continue
		}
	}
	if exitErr != 0 {
		os.Exit(exitErr)
	}
	return nil
}

func scoreOne(client *ghc.Client, ref prref.Ref, myLogin string, myIdentities []string, teamsOK bool) error {
	pr, err := client.FetchPR(ref)
	if err != nil {
		return err
	}

	var files []ghc.File
	var owners codeowners.Ruleset
	if !noOwnersFlag {
		files, err = client.FetchPRFiles(ref)
		if err != nil {
			return err
		}
		owners, _, err = client.FetchCodeowners(ref.Owner, ref.Repo, pr.CodeownersRef())
		if err != nil {
			return err
		}
	}

	s := score.Compute(pr, files, owners, riskLabelFlag, myLogin, myIdentities)
	s.TeamsResolved = teamsOK
	repoStr := fmt.Sprintf("%s/%s", ref.Owner, ref.Repo)
	if jsonFlag {
		return format.JSON(os.Stdout, repoStr, s)
	}
	// One code path for human and script modes: tableprinter degrades to
	// tab-separated automatically when stdout isn't a TTY.
	format.Human(os.Stdout, repoStr, s, term.FromEnv())
	return nil
}

// defaultRepo returns owner, repo if the current directory is a git
// repository with a github.com remote. Otherwise returns "", "" - caller
// must require --repo for bare numbers.
func defaultRepo() (string, string) {
	if repoFlag != "" {
		// User passed --repo owner/name.
		// Trust it; gh's repository.Parse would normalize.
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
