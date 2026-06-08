# gh-cru

A `gh` extension to measure pull requests in Code Review Units (CRU).

```sh
gh extension install laserlemon/gh-cru

gh cru 1234
gh cru owner/repo#1234
gh cru https://github.com/owner/repo/pull/1234
```

## What CRU is

CRU is a bounded, anchored measure of PR review effort:

```
CRU = size_factor × ownership_share × risk
```

- **size_factor** is `2^(5·F(L) − 2.5)`, where `F(L)` is the PR's percentile
  rank in a locked reference distribution of pre-Copilot github/github merged
  PRs. The unit is calibrated so that one "typical" PR (the distribution's
  median) scores exactly `1.0`. Bounded between ~0.18 (typos) and ~5.66
  (monster PRs).
- **ownership_share** is `your_owned_LOC / total_LOC` based on CODEOWNERS.
  A 1,000-line PR where 50 lines touch your team's code costs you 5% of the
  size factor.
- **risk** is `1.0` by default; PRs labeled `risk:high` get `4.0` (same span
  as the difference between an S and an L by size). Configurable via
  `--risk-label`.

For the math, see [`laserlemon/cru`](https://github.com/laserlemon/cru).

## Example

```sh
$ gh cru https://github.com/github/github/pull/434551

Rename DelegatedBypassReviewersComponent to DelegatedBypassersComponent github/github#434551
closed • by @rupss • +19 -19 in 5 file(s)

  size factor:  1.09   (38 LOC, M)
  risk factor:  1.0    (low)
  normal CRU:   1.09   (size × risk; PR-intrinsic weight)
  your CRU:     0.11   (4/38 LOC = 10.5% match your identities)

  CRU by owner:
      @github/secret-scanning-experiences-reviewers     34 LOC   89.5%  →  0.97 CRU
      @github/secret-scanning-reviewers                 34 LOC   89.5%  →  0.97 CRU
    * @github/security-products-enablement-reviewers     4 LOC   10.5%  →  0.11 CRU
  total CRU:    2.06   (sum across owners; team review burden)
```

The `* ` marker is the git-branch convention: that line matches one of
your identities (your @login or any team you're on).

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

# Pipe from gh pr list.
gh pr list --state merged --limit 100 --json url --jq '.[].url' | xargs gh cru
```

Output mode auto-detects TTY:

- **TTY**: human-readable, with the `* ` marker
- **piped**: tab-separated `key:\tvalue` rows (gh script-mode convention)
- **`--json`**: structured, with `is_you: true|false` per owner

## Flags

| Flag | Purpose |
|---|---|
| `-R, --repo OWNER/NAME` | Default repo for bare PR numbers |
| `--json` | Structured output |
| `--skip-ownership` | Skip CODEOWNERS lookups; treat ownership as 1.0 |
| `--skip-personal` | Skip fetching your team memberships; no "your CRU" |
| `--risk-label LABEL` | PR label that marks high risk (default: `risk:high`) |

## Install

```sh
gh extension install laserlemon/gh-cru
gh extension upgrade gh-cru
```

To build from source (Go ≥ 1.25):

```sh
git clone https://github.com/laserlemon/gh-cru
cd gh-cru
go build -o gh-cru ./cmd/gh-cru
```

## License

MIT
