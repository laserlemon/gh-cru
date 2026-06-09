# gh-cru

A `gh` extension to measure pull requests in Code Review Units (CRU).

```sh
gh extension install laserlemon/gh-cru

# Score one PR or a list of refs (the "view" verb is implied).
gh cru 1234
gh cru owner/repo#1234
gh cru https://github.com/owner/repo/pull/1234
gh cru cli/cli#13612 cli/cli#13599 cli/cli#13597

# Score every PR that matches a "gh pr list" query.
gh cru list --repo owner/name --state merged --limit 50
gh cru list --author @me --state open
gh cru list --search 'is:open updated:>2026-01-01'

# Stdin: pre-fetched JSON (skips per-PR API calls when fields are included).
gh pr list --repo owner/name --state merged --limit 50 --json \
  url,number,additions,deletions,changedFiles,baseRefName,mergeCommit,labels,author,state,files,title \
  | gh cru view -
```

## What CRU is

CRU is a bounded, anchored measure of PR review effort:

```
CRU = size factor × ownership share × risk factor
```

- **size factor** is `2^(5·F(L) − 2.5)`, where `F(L)` is the PR's percentile
  rank in a locked reference distribution of merged PR sizes from a large
  monolithic GitHub repository with thousands of individual contributors.
  The unit is calibrated so that one "typical" PR (the distribution's
  median) scores exactly `1.0`. Bounded between ~0.18 (typos) and ~5.66
  (monster PRs).
- **ownership share** is `owned_LOC / total_LOC` based on CODEOWNERS.
  A 1,000-line PR where 50 lines touch your team's code costs you 5% of the
  size factor. LOC the CODEOWNERS rules don't cover is attributed to a
  synthetic `unowned` owner so unowned work is never silently dropped.
- **risk factor** is `1.0` by default. PRs labeled `risk:medium` get `2.0` and
  PRs labeled `risk:high` get `4.0` (same span as the difference between an S
  and an L by size). Configurable via `--high-risk-label` and `--medium-risk-label`;
  high wins when both match.

For the math, see [`laserlemon/cru`](https://github.com/laserlemon/cru).

## Example

```sh
$ gh cru acme/web#1234

acme/web#1234

LOC          240
Size label   XL
Size factor  3.391
Risk label   low
Risk factor  1.000
Normal CRU   3.391
Total CRU    3.673
Your CRU     1.695

   CODE OWNER               LOC  SHARE    CRU
=  laserlemon                40  0.167  0.565
*  acme/big-orca             80  0.333  1.130
•  acme/payments-reviewers  100  0.417  1.413
~  unowned                   40  0.167  0.565
```

A 1-character marker in the gutter classifies each row:

| Marker | Meaning |
|---|---|
| `=` | Direct `@login` match (you specifically own these lines, bold blue) |
| `*` | Team membership match (a team you're on owns these, blue) |
| `•` | Someone else owns these |
| `~` | Synthetic `unowned` row: lines no CODEOWNERS rule matched |

`Normal CRU` is the PR's intrinsic weight (size × risk). `Total CRU` sums
all rows in the table including `unowned`; it exceeds `Normal CRU` here
because the `big-orca` team and the `payments-reviewers` team share some
of the same files, so per-owner LOC sums to more than the PR's total LOC.
With no overlap, `Total CRU` equals `Normal CRU`.

`Your CRU` is what THIS PR costs you to review: every file owned by your
`@login` OR any team you belong to, counted once even when both match.
For the reviewer above (`laserlemon`, on `acme/big-orca`), that's 40
direct + 80 team = 120 LOC, share 0.500.

## Four numbers, four questions

| Number | Question it answers |
|---|---|
| `size factor` | How big is this PR? (just LOC, in CRU-space) |
| `normal CRU` | How much review weight does this PR carry intrinsically? |
| `your CRU` | What does this PR actually cost me to review, given my team membership? |
| `total CRU` | How much team review burden did this PR create? |

## Batch and scripting

PR references can be bare numbers (if `--repo` is set or you're in a git
repo), shorthand (`owner/name#123`), or URLs. Pass several at once or
pipe them in:

```sh
# Score many PRs at once; CODEOWNERS is fetched once per repo per invocation.
gh cru 1234 1235 1236 1237

# Cross-repo: each ref carries its own owner/name.
gh cru cli/cli#13612 laserlemon/cru#1
```

### `gh cru list`

Forward any `gh pr list` flag to score the whole result set:

```sh
gh cru list --state merged --limit 50
gh cru list --author @me --state open
gh cru list --repo owner/name --label bug
gh cru list --search 'is:open updated:>2026-01-01'
```

`gh cru list` runs `gh pr list` once with a fixed `--json` field set, then
scores each PR locally without per-PR API calls. CODEOWNERS is fetched
once per unique repo. For a single-repo `--state open` workload, that's
**1 list call + 1 CODEOWNERS fetch** for all 50+ PRs. Any `--json` flag
the user passes is silently overridden so the required field set is
always emitted.

### `gh cru view -` (stdin)

For full control over which PRs get scored, pipe JSON to `gh cru view -`
(or just `gh cru -`). Accepts a JSON array, NDJSON, or one JSON object.

```sh
# Use gh search to span orgs, then score.
gh search prs --json url --limit 20 author:laserlemon \
  | gh cru view -

# Pre-fetched scoring fields: 0 PR-API calls per row.
gh pr list --repo owner/name --json \
  url,additions,deletions,changedFiles,baseRefName,mergeCommit,labels,author,state,files \
  | gh cru view -
```

Output mode auto-detects TTY:

- **TTY**: human-readable with colored markers and column alignment
- **piped**: tab-separated rows, no color (gh script-mode convention)
- **`--json`**: compact NDJSON; one PR per line. Each owner row carries
  `name` (bare login or `org/team`, `null` for the synthetic unowned row),
  `type` (`"user"` / `"team"` / `"unowned"`), and `is_you` (true when the
  owner matches your `@login` directly or via team membership). Pipe
  through `jq .` for pretty output

## Flags

| Flag | Purpose |
|---|---|
| `-R, --repo OWNER/NAME` | Default repo for bare PR numbers |
| `--json` | Structured output |
| `--skip-ownership` | Skip CODEOWNERS lookups; treat ownership as 1.0 |
| `--skip-personal` | Skip fetching your team memberships; no "your CRU" |
| `--high-risk-label LABEL` | PR label(s) that mark high risk (4×); repeat or comma-separate (default: `risk:high`) |
| `--medium-risk-label LABEL` | PR label(s) that mark medium risk (2×); repeat or comma-separate (default: `risk:medium`). High wins over medium |

## Install

```sh
gh extension install laserlemon/gh-cru
gh extension upgrade gh-cru
```

To build from source (Go ≥ 1.25):

```sh
git clone https://github.com/laserlemon/gh-cru
cd gh-cru
go build -o gh-cru .
```

To see every visual variant of the output (markers, colors, edge cases)
locally without hitting GitHub:

```sh
go run ./scripts/demo-output.go
```

## License

MIT
