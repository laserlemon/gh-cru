# laserlemon/gh-cru

A `gh` extension to measure pull requests in Code Review Units ([CRU](https://github.com/laserlemon/cru)).

[![Made by laserlemon](https://img.shields.io/badge/laser-lemon-fc0?style=flat-square)](https://github.com/laserlemon)
[![Latest tag](https://img.shields.io/github/v/tag/laserlemon/gh-cru?style=flat-square&label=tag)](https://github.com/laserlemon/gh-cru/tags)
[![CI](https://img.shields.io/github/actions/workflow/status/laserlemon/gh-cru/ci.yml?style=flat-square)](https://github.com/laserlemon/gh-cru/actions/workflows/ci.yml)

```sh
gh extension install laserlemon/gh-cru

# Score one PR, mirroring "gh pr view" (the "view" verb is implied).
gh cru                            # the PR for the current branch
gh cru 1234                       # by number
gh cru my-feature-branch          # by branch name
gh cru https://github.com/owner/repo/pull/1234
gh cru --repo owner/name 1234     # a PR in another repo

# Score every PR that matches a "gh pr list" query.
gh cru list --repo owner/name --state merged --limit 50
gh cru list --author @me --state open
gh cru list --search 'is:open updated:>2026-01-01'
```

## What CRU is

CRU is a bounded, anchored measure of PR review effort:

```
CRU = size factor × ownership share × risk multiplier
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
- **risk multiplier** is `1.0` by default. PRs labeled `risk:medium` get `2.0` and
  PRs labeled `risk:high` get `4.0` (same span as the difference between an S
  and an L by size). Configurable via `--high-risk-label` and `--medium-risk-label`;
  high wins when both match.

For the math, see [`laserlemon/cru`](https://github.com/laserlemon/cru).

## Example

```sh
$ gh cru --repo acme/web 1234

acme/web#1234

LOC              240
Size label       XL
Size factor      3.065
Risk label       low
Risk multiplier  1.000
Normal CRU       3.065
Total CRU        3.321
Your CRU         1.533

   CODE OWNER               LOC  SHARE    CRU
=  laserlemon                40  0.167  0.511
*  acme/big-orca             80  0.333  1.022
•  acme/payments-reviewers  100  0.417  1.277
~  unowned                   40  0.167  0.511
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

## Two commands, mirroring `gh pr`

`gh cru` mirrors the `gh pr` commands you already know.

### `gh cru view`

Scores one pull request, the same way `gh pr view` shows one. The verb is
implied, so `gh cru` on its own is `gh cru view`. The PR argument takes the
same forms `gh pr view` accepts:

```sh
gh cru                            # the PR for the current branch
gh cru 1234                       # by number (--repo or git context)
gh cru my-feature-branch          # by branch name
gh cru https://github.com/owner/repo/pull/1234
gh cru --repo owner/name 1234     # a PR in another repo
```

Under the hood `gh cru view` shells out to `gh pr view` to resolve and fetch
the PR, then scores it. A few `gh pr view` flags are intercepted because they
don't fit a score:

| Flag | What gh cru does |
|---|---|
| `--json` | Forced to the field set gh cru needs. Your `--json` controls gh cru's own output instead (same as `gh cru list`). |
| `--jq`/`-q`, `--template`/`-t` | Dropped (they format the inner JSON gh cru consumes). |
| `--web`/`-w`, `--comments`/`-c` | Rejected: gh cru produces a score, not a browser view or comment thread. |
| `--repo`/`-R` | Honored and forwarded. |

### `gh cru list`

Forward any `gh pr list` flag to score the whole result set, in the order gh
returned them:

```sh
gh cru list --state merged --limit 50
gh cru list --author @me --state open
gh cru list --repo owner/name --label bug
gh cru list --search 'is:open updated:>2026-01-01'
```

CODEOWNERS is fetched once per repo per invocation, so a batch is cheap. As
with `view`, gh cru forces its own `--json` field set; pass `--json` on
`gh cru` itself (before `list`) to get JSON output instead:

```sh
gh cru --json list --state open
```

`gh cru list` runs `gh pr list` once, then scores each PR locally without
per-PR API calls. For a single-repo `--state open` workload, that's
**1 list call + 1 CODEOWNERS fetch** for all 50+ PRs.

## Output

Output mode auto-detects the terminal, matching `gh`'s own convention:

- **TTY**: human-readable with colored markers and column alignment
- **piped**: tab-separated rows, no color (gh script-mode convention)
- **`--json`**: compact JSON; one object per PR (NDJSON from `gh cru list`).
  Each owner row carries `name` (bare login or `org/team`, `null` for the
  synthetic unowned row), `type` (`"user"` / `"team"` / `"unowned"`), and
  `is_you` (true when the owner matches your `@login` directly or via team
  membership). Pipe through `jq .` for pretty output

## Flags

| Flag | Purpose |
|---|---|
| `-R, --repo OWNER/NAME` | Repo for the PR; forwarded to `gh pr` |
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
