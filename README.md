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

```sh
$ gh cru --repo acme/web 1234

Add rate limiting to the webhook dispatcher acme/web#1234

Size  XL   3.065  240 lines
Risk  low  1.000
Base       3.065  CRU

   CODE OWNER               LINES   SHARE    CRU
•  acme/payments-reviewers    100   41.7%  1.277
*  acme/big-orca               80   33.3%  1.022
=  laserlemon                  40   16.7%  0.511
~  Unowned                     40   16.7%  0.511
+  All ownership              260  108.3%  3.321
>  Your ownership             120   50.0%  1.533
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

```sh
$ gh cru --repo acme/web 1234 --json | jq
{
  "repository": { "name": "web", "nameWithOwner": "acme/web" },
  "pullRequest": {
    "additions": 200,
    "deletions": 40,
    "number": 1234,
    "state": "OPEN",
    "title": "Add rate limiting to the webhook dispatcher",
    "url": "https://github.com/acme/web/pull/1234"
  },
  "size": { "label": "XL", "factor": 3.065154, "lines": 240 },
  "risk": { "label": "low", "multiplier": 1.000000 },
  "baseCru": 3.065154,
  "ownership": {
    "owners": [
      { "name": "acme/payments-reviewers", "type": "team", "isYou": false, "lines": 100, "share": 0.416667, "cru": 1.277147 },
      { "name": "acme/big-orca",           "type": "team", "isYou": true,  "lines": 80,  "share": 0.333333, "cru": 1.021718 },
      { "name": "laserlemon",              "type": "user", "isYou": true,  "lines": 40,  "share": 0.166667, "cru": 0.510859 }
    ],
    "unowned": { "lines": 40,  "share": 0.166667, "cru": 0.510859 },
    "all":     { "lines": 260, "share": 1.083333, "cru": 3.320583 },
    "you":     { "lines": 120, "share": 0.500000, "cru": 1.532577 }
  }
}
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

### Selecting fields

A bare `--json` emits the whole document. To narrow it, attach a comma-separated
list of top-level keys with `=`:

```sh
$ gh cru --repo acme/web 1234 --json=size,risk | jq
{
  "size": { "label": "XL", "factor": 3.065154, "lines": 240 },
  "risk": { "label": "low", "multiplier": 1.000000 }
}
```

This inverts `gh`'s convention. `gh pr view --json` requires the field list and
errors on a bare `--json`, because its PR object runs to dozens of fields behind
many API calls. gh-cru's object is one small, already-computed thing, so bare
`--json` hands you all of it and the list is there for when you want less. The
full output doubles as the menu of valid keys.

The fields attach with `=` (as in `git --color=always`), not a space. A bare
`--json` stays valid on its own, so `gh cru --json 1234` reads `1234` as the PR,
not a field list; the `=` is what separates the two.

A misspelled field is rejected with the valid set, so you keep gh's
typo-catching:

```sh
$ gh cru --repo acme/web 1234 --json=bogus
unknown JSON field: "bogus"
available fields:
  repository
  pullRequest
  size
  risk
  baseCru
  ownership
```

## Flags

| Flag | Purpose |
|---|---|
| `-R, --repo OWNER/NAME` | Repo for the PR; forwarded to `gh pr` |
| `--json` | Structured output; `--json=field,…` selects top-level keys |
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
