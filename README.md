# laserlemon/gh-cru

A `gh` extension to measure pull requests in Code Review Units ([CRU](https://github.com/laserlemon/cru)).

[![Made by laserlemon](https://img.shields.io/badge/laser-lemon-fc0?style=flat-square)](https://github.com/laserlemon)
[![Latest tag](https://img.shields.io/github/v/tag/laserlemon/gh-cru?style=flat-square&label=tag)](https://github.com/laserlemon/gh-cru/tags)
[![CI](https://img.shields.io/github/actions/workflow/status/laserlemon/gh-cru/ci.yml?style=flat-square)](https://github.com/laserlemon/gh-cru/actions/workflows/ci.yml)

```sh
gh extension install laserlemon/gh-cru

# Measure one PR, mirroring "gh pr view" (the "view" verb is implied).
gh cru                            # the PR for the current branch
gh cru 1234                       # by number
gh cru my-feature-branch          # by branch name
gh cru https://github.com/owner/repo/pull/1234
gh cru --repo owner/name 1234     # a PR in another repo

# Measure every PR that matches a "gh pr list" query.
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
  median) measures exactly `1.0`. Bounded between ~0.18 (typos) and ~5.66
  (monster PRs).
- **ownership share** is `owned lines / total lines` based on CODEOWNERS.
  A 1,000-line PR where 50 lines touch your team's code costs you 5% of the
  size factor. Lines the CODEOWNERS rules don't cover are attributed to a
  synthetic `unowned` owner so unowned work is never silently dropped.
- **risk multiplier** is `1.0` by default. PRs labeled `risk:medium` get `2.0` and
  PRs labeled `risk:high` get `4.0` (same span as the difference between an S
  and an L by size). Configurable via `--high-risk-label` and `--medium-risk-label`;
  high wins when both match.

For the math, see [`laserlemon/cru`](https://github.com/laserlemon/cru).

## Example

```ansi
$ gh cru --repo acme/web 1234

[0;1;39mAdd rate limiting to the webhook dispatcher[0m [0;90macme/web#1234[0m

[0;1;90mSize[0m  [0;1;38;5;124mXL [0m  3.065  [0;90m240 lines[0m
[0;1;90mRisk[0m  [0;1;38;5;30mlow[0m  1.000
[0;1;90mBase[0m       3.065  [0;90mCRU[0m

[0;4;90m [0m  [0;4;90mCODE OWNER             [0m  [0;4;90mLINES[0m  [0;4;90m SHARE[0m  [0;4;90m  CRU[0m
•  acme/payments-reviewers    100   41.7%  1.277
*  acme/big-orca               80   33.3%  1.022
=  laserlemon                  40   16.7%  0.511
[0;1;90m~[0m  [0;1;90mUnowned                [0m     40   16.7%  0.511
[0;1;90m+[0m  [0;1;90mAll ownership          [0m    260  108.3%  3.321
[0;1;90m>[0m  [0;1;90mYour ownership         [0m    120   50.0%  1.533
```

The heading mirrors `gh pr view`: the PR title in bold, then a gray
`owner/repo#N` reference.

The **formula block** is the measurement itself, one factor per row:

| Row | Question it answers |
|---|---|
| `Size` | How big is this PR? (the bucket, its factor, and the raw line count) |
| `Risk` | How risky? (the tier and its multiplier) |
| `Base` | The PR's intrinsic review weight: `Size × Risk`, in CRU |

The **ownership table** then splits that weight across the people on the
hook to review it. A 1-character marker classifies each row:

| Marker | Meaning |
|---|---|
| `=` | Direct `@login` match (you specifically own these lines) |
| `*` | Team membership match (a team you're on owns these) |
| `•` | Someone else owns these |
| `~` | `Unowned`: lines no CODEOWNERS rule matched |
| `+` | `All ownership`: every row summed, the team's total review burden |
| `>` | `Your ownership`: what this PR costs you to review |

`SHARE` is each owner's slice of the PR (their lines over the PR's total);
`CRU` is that slice's review weight (`Base × share`).

The three gray summary rows (`~`, `+`, `>`) frame the raw owner data as
computed totals. `All ownership` sums every row including `Unowned`; here
it exceeds 100% because the `big-orca` and `payments-reviewers` teams share
some of the same files, so per-owner lines sum to more than the PR's total.
With no overlap, `All ownership` lands at exactly the PR's line count and its CRU
equals `Base`.

`Your ownership` is what THIS PR costs you to review: every file owned by
your `@login` OR any team you belong to, counted once even when both match.
For the reviewer above (`laserlemon`, on `acme/big-orca`), that's 40 direct
+ 80 team = 120 lines, share 50.0%. It only renders when you own something.

## Two commands, mirroring `gh pr`

`gh cru` mirrors the `gh pr` commands you already know.

### `gh cru view`

Measures one pull request, the same way `gh pr view` shows one. The verb is
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
the PR, then measures it. A few `gh pr view` flags are intercepted because they
don't fit a measurement:

| Flag | What gh cru does |
|---|---|
| `--json` | Forced to the field set gh cru needs. Your `--json` controls gh cru's own output instead (same as `gh cru list`). |
| `--jq`/`-q`, `--template`/`-t` | Dropped (they format the inner JSON gh cru consumes). |
| `--web`/`-w`, `--comments`/`-c` | Rejected: gh cru produces a measurement, not a browser view or comment thread. |
| `--repo`/`-R` | Honored and forwarded. |

### `gh cru list`

Forward any `gh pr list` flag to measure the whole result set, in the order gh
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

`gh cru list` runs `gh pr list` once, then measures each PR locally without
per-PR API calls. For a single-repo `--state open` workload, that's
**1 list call + 1 CODEOWNERS fetch** for all 50+ PRs.

## Output

Output mode auto-detects the terminal, matching `gh`'s own convention:

- **TTY**: human-readable with colored markers and column alignment
- **piped**: tab-separated rows, no color (gh script-mode convention)
- **`--json`**: structured JSON, matching `gh`'s object-vs-array shape: a bare object for one PR (`gh cru 123`), a JSON array for a batch (`gh cru list`). Pretty-printed and colorized on a TTY, compact when piped.

### JSON

`--json` emits the same data the human view draws, nothing more. It's the
PR heading, the `Size`/`Risk`/`Base` formula block, and an `ownership`
object holding the owner table. Here's the PR from the [Example](#example)
above, piped through `jq`:

```ansi
$ gh cru --repo acme/web 1234 --json | jq
[1;39m{
  [0m[1;34m"repository"[0m[1;39m: [0m[1;39m{
    [0m[1;34m"name"[0m[1;39m: [0m[0;32m"web"[0m[1;39m,
    [0m[1;34m"nameWithOwner"[0m[1;39m: [0m[0;32m"acme/web"[0m[1;39m
  [1;39m}[0m[1;39m,
  [0m[1;34m"pullRequest"[0m[1;39m: [0m[1;39m{
    [0m[1;34m"additions"[0m[1;39m: [0m[0;39m200[0m[1;39m,
    [0m[1;34m"deletions"[0m[1;39m: [0m[0;39m40[0m[1;39m,
    [0m[1;34m"number"[0m[1;39m: [0m[0;39m1234[0m[1;39m,
    [0m[1;34m"state"[0m[1;39m: [0m[0;32m"OPEN"[0m[1;39m,
    [0m[1;34m"title"[0m[1;39m: [0m[0;32m"Add rate limiting to the webhook dispatcher"[0m[1;39m,
    [0m[1;34m"url"[0m[1;39m: [0m[0;32m"https://github.com/acme/web/pull/1234"[0m[1;39m
  [1;39m}[0m[1;39m,
  [0m[1;34m"size"[0m[1;39m: [0m[1;39m{
    [0m[1;34m"label"[0m[1;39m: [0m[0;32m"XL"[0m[1;39m,
    [0m[1;34m"factor"[0m[1;39m: [0m[0;39m3.065154[0m[1;39m,
    [0m[1;34m"lines"[0m[1;39m: [0m[0;39m240[0m[1;39m
  [1;39m}[0m[1;39m,
  [0m[1;34m"risk"[0m[1;39m: [0m[1;39m{
    [0m[1;34m"label"[0m[1;39m: [0m[0;32m"low"[0m[1;39m,
    [0m[1;34m"multiplier"[0m[1;39m: [0m[0;39m1.000000[0m[1;39m
  [1;39m}[0m[1;39m,
  [0m[1;34m"baseCru"[0m[1;39m: [0m[0;39m3.065154[0m[1;39m,
  [0m[1;34m"ownership"[0m[1;39m: [0m[1;39m{
    [0m[1;34m"owners"[0m[1;39m: [0m[1;39m[
      [1;39m{
        [0m[1;34m"name"[0m[1;39m: [0m[0;32m"acme/payments-reviewers"[0m[1;39m,
        [0m[1;34m"type"[0m[1;39m: [0m[0;32m"team"[0m[1;39m,
        [0m[1;34m"isYou"[0m[1;39m: [0m[0;39mfalse[0m[1;39m,
        [0m[1;34m"lines"[0m[1;39m: [0m[0;39m100[0m[1;39m,
        [0m[1;34m"share"[0m[1;39m: [0m[0;39m0.416667[0m[1;39m,
        [0m[1;34m"cru"[0m[1;39m: [0m[0;39m1.277147[0m[1;39m
      [1;39m}[0m[1;39m,
      [1;39m{
        [0m[1;34m"name"[0m[1;39m: [0m[0;32m"acme/big-orca"[0m[1;39m,
        [0m[1;34m"type"[0m[1;39m: [0m[0;32m"team"[0m[1;39m,
        [0m[1;34m"isYou"[0m[1;39m: [0m[0;39mtrue[0m[1;39m,
        [0m[1;34m"lines"[0m[1;39m: [0m[0;39m80[0m[1;39m,
        [0m[1;34m"share"[0m[1;39m: [0m[0;39m0.333333[0m[1;39m,
        [0m[1;34m"cru"[0m[1;39m: [0m[0;39m1.021718[0m[1;39m
      [1;39m}[0m[1;39m,
      [1;39m{
        [0m[1;34m"name"[0m[1;39m: [0m[0;32m"laserlemon"[0m[1;39m,
        [0m[1;34m"type"[0m[1;39m: [0m[0;32m"user"[0m[1;39m,
        [0m[1;34m"isYou"[0m[1;39m: [0m[0;39mtrue[0m[1;39m,
        [0m[1;34m"lines"[0m[1;39m: [0m[0;39m40[0m[1;39m,
        [0m[1;34m"share"[0m[1;39m: [0m[0;39m0.166667[0m[1;39m,
        [0m[1;34m"cru"[0m[1;39m: [0m[0;39m0.510859[0m[1;39m
      [1;39m}[0m[1;39m
    [1;39m][0m[1;39m,
    [0m[1;34m"unowned"[0m[1;39m: [0m[1;39m{
      [0m[1;34m"lines"[0m[1;39m: [0m[0;39m40[0m[1;39m,
      [0m[1;34m"share"[0m[1;39m: [0m[0;39m0.166667[0m[1;39m,
      [0m[1;34m"cru"[0m[1;39m: [0m[0;39m0.510859[0m[1;39m
    [1;39m}[0m[1;39m,
    [0m[1;34m"all"[0m[1;39m: [0m[1;39m{
      [0m[1;34m"lines"[0m[1;39m: [0m[0;39m260[0m[1;39m,
      [0m[1;34m"share"[0m[1;39m: [0m[0;39m1.083333[0m[1;39m,
      [0m[1;34m"cru"[0m[1;39m: [0m[0;39m3.320583[0m[1;39m
    [1;39m}[0m[1;39m,
    [0m[1;34m"you"[0m[1;39m: [0m[1;39m{
      [0m[1;34m"lines"[0m[1;39m: [0m[0;39m120[0m[1;39m,
      [0m[1;34m"share"[0m[1;39m: [0m[0;39m0.500000[0m[1;39m,
      [0m[1;34m"cru"[0m[1;39m: [0m[0;39m1.532577[0m[1;39m
    [1;39m}[0m[1;39m
  [1;39m}[0m[1;39m
[1;39m}[0m
```

The top level holds two borrowed GitHub objects and CRU's own measurement.
`repository` and `pullRequest` carry only fields gh itself puts on those
entities (`repository` is `{name, nameWithOwner}`, matching `gh search prs`
rather than a flattened string). `size`, `risk`, `baseCru`, and `ownership`
are CRU's. `baseCru` is `size.factor × risk.multiplier`, matching the `Base` row.

| Field | Meaning |
|---|---|
| `repository` | gh's repo object: `name`, `nameWithOwner` |
| `pullRequest` | gh's PR fields: `additions`, `deletions`, `number`, `state`, `title`, `url` |
| `size` | The `Size` row: `label` (bucket), `factor`, and `lines` (additions + deletions) |
| `risk` | The `Risk` row: `label` (tier) and `multiplier` |
| `baseCru` | `size.factor × risk.multiplier`, the `Base` row |

`ownership` mirrors the owner table. `owners[]` holds the named rows; the
three summary rows live alongside it as objects:

| Field | Meaning |
|---|---|
| `owners[]` | One object per named owner (the `=`/`*`/`•` rows) |
| `owners[].name` | Bare `login` or `org/team` (the `@` is stripped) |
| `owners[].type` | `"user"` or `"team"` |
| `owners[].isYou` | `true` when the owner is your `@login` directly or a team you're on |
| `unowned` | The `~` row: lines no CODEOWNERS rule matched (always present, zeroed when none) |
| `all` | The `+` row: every owner summed, the team's total review burden (always present) |
| `you` | The `>` row: your stake, counted once across direct and team matches. Present only when your identity is known; omitted entirely otherwise |

Every owner and summary object carries the same `{lines, share, cru}`
shape, mirroring the `LINES`/`SHARE`/`CRU` columns. All floats are pinned
to six decimals so downstream `==` comparisons stay stable. Pipe through
`jq .` for pretty output.

## Flags

| Flag | Purpose |
|---|---|
| `-R, --repo OWNER/NAME` | Repo for the PR; forwarded to `gh pr` |
| `--json` | Structured output (JSON) |
| `--skip-ownership` | Skip CODEOWNERS entirely; end on Base CRU (size × risk), no ownership table |
| `--anonymous` | Don't resolve your identity; omit the `Your ownership` row |
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
