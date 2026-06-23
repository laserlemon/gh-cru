// Package gh wraps the gh CLI's API clients with the shape gh-cru needs:
// a single fetch entry point per PR, persistent on-disk CODEOWNERS caching
// (so repeated runs in the same repo reuse each large CODEOWNERS fetch
// across subprocess boundaries), and a per-process in-memory cache for
// batched runs.
package gh

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/cli/go-gh/v2"
	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/hmarr/codeowners"

	"github.com/laserlemon/gh-cru/internal/cache"
	"github.com/laserlemon/gh-cru/internal/prref"
)

// Client is the gh-cru API client. Safe for concurrent use.
type Client struct {
	rest *api.RESTClient
	disk *cache.Cache // nil if cache init failed (still functional, just no persistence)

	mu       sync.Mutex
	owners   map[string]ownersEntry // per-process cache (batch within one CLI run)
	noOwners map[string]bool        // repos with no CODEOWNERS file at any standard path

	authLogin    string
	authTeams    []string
	authTeamsOK  bool
	authResolved bool

	// orgReadable caches HEAD /orgs/{org}/teams results per process. true
	// means the token can list that org's teams (proves read:org or
	// equivalent); false means HEAD returned 403 (the silent-empty trap
	// where /user/teams returned 200+[] not because the user is on zero
	// teams but because the token lacks org-read scope).
	orgReadable sync.Map // map[string]bool
}

// ownersEntry is the in-memory record for a repo's CODEOWNERS in one run.
// Source distinguishes local/cache-hit/cache-304/fresh so callers can show
// it in dim footer text.
type ownersEntry struct {
	Rules  codeowners.Ruleset
	Source string // "local" | "cache" | "cache-revalidated" | "fresh"
}

// NewClient builds a Client using gh's default auth and the standard
// on-disk cache directory. The disk cache is best-effort: if it can't be
// initialized (e.g. unset HOME), the client still works using only the
// in-process cache.
func NewClient() (*Client, error) {
	rest, err := api.DefaultRESTClient()
	if err != nil {
		return nil, fmt.Errorf("gh REST client: %w", err)
	}
	disk, _ := cache.New("") // ignore err; nil cache is fine
	return &Client{
		rest:     rest,
		disk:     disk,
		owners:   make(map[string]ownersEntry),
		noOwners: make(map[string]bool),
	}, nil
}

// PR is the slice of a pull request we care about for scoring.
type PR struct {
	Number         int
	URL            string
	Title          string
	State          string
	Additions      int
	Deletions      int
	Labels         []string
	HeadSHA        string
	BaseRef        string
	Merged         bool
	MergeCommitSHA string
}

// LOC is the standard size measure: additions + deletions.
func (p PR) LOC() int { return p.Additions + p.Deletions }

// CodeownersRef returns the git ref at which CODEOWNERS should be evaluated
// for scoring this PR. The semantics are deliberately stable-historical:
//
//   - Merged PRs use merge_commit_sha. CODEOWNERS at that commit is frozen
//     forever, so re-scoring the same PR tomorrow yields the same answer.
//   - Open PRs use the base branch name, which resolves live to the branch's
//     current HEAD. Matches what GitHub itself evaluates for review-request
//     gating today; may shift if owners change before the PR merges, which
//     is correct for "what does it cost to review this NOW".
//   - Closed-unmerged PRs have no merge commit, so we use the base branch
//     name like open. Re-scoring may drift but the PR is academic anyway.
//
// Returns the empty string only if we somehow have nothing to point at
// (no base ref, not merged); callers should treat that as "fall back to
// default branch" by passing it through to the contents API which will
// resolve unset ref to default-branch HEAD.
func (p PR) CodeownersRef() string {
	if p.Merged && p.MergeCommitSHA != "" {
		return p.MergeCommitSHA
	}
	return p.BaseRef
}

// File is one changed file in a PR with its delta size.
type File struct {
	Path    string
	Changes int // additions + deletions
}

// AuthIdentities returns the authenticated user's CODEOWNERS-compatible
// identities: their @login plus every @org/team-slug they belong to. These
// are the strings that match against codeowners rule owners.
//
// teamsOK indicates whether team enumeration via /user/teams looked
// honest. It's false when the call errors outright (403 / 404) but is
// also subject to a per-process probe (CanListOrgTeams) for the silent-
// empty trap: tokens lacking read:org get 200+[] from /user/teams instead
// of 403, so an empty result is ambiguous. Use CanListOrgTeams against a
// known org to disambiguate.
//
// Identities always include the @login, so direct CODEOWNERS matches
// work even when teamsOK is false; only team-based ownership is missed.
//
// Resolved once per process. /user/teams is shelled to gh with a 1h disk
// cache (gh's own --cache flag) so back-to-back invocations within the
// hour don't re-hit the API.
func (c *Client) AuthIdentities() (login string, identities []string, teamsOK bool, err error) {
	c.mu.Lock()
	if c.authResolved {
		l, t, ok := c.authLogin, c.authTeams, c.authTeamsOK
		c.mu.Unlock()
		return l, t, ok, nil
	}
	c.mu.Unlock()

	// 1. /user: direct REST, sub-millisecond response, not worth caching.
	var u struct {
		Login string `json:"login"`
	}
	if err := c.rest.Get("user", &u); err != nil {
		return "", nil, false, fmt.Errorf("GET /user: %w", err)
	}

	// 2. /user/teams via `gh api --cache 1h --paginate --jq …`. Shelling
	// to gh buys us:
	//   - gh's own 1h disk cache, no extra wiring
	//   - --paginate (no manual page loop)
	//   - --jq to extract just the strings we want
	//   - gh's auth resolution (env, keyring, gh auth status)
	// Cost: a few-ms gh process launch per cache-miss invocation.
	teams := []string{"@" + u.Login}
	teamsOK = true
	stdout, _, gerr := gh.Exec(
		"api",
		"--cache", "1h",
		"--paginate",
		"--jq", `.[] | "@" + .organization.login + "/" + .slug`,
		"user/teams",
	)
	if gerr != nil {
		// gh exits non-zero on 4xx/5xx. 403 / 404 = no read:org or no
		// teams endpoint; treat as "fall back to @login only".
		// Anything else, bubble the error so the user sees what broke.
		msg := gerr.Error()
		if strings.Contains(msg, "HTTP 403") || strings.Contains(msg, "HTTP 404") {
			teamsOK = false
		} else {
			return "", nil, false, fmt.Errorf("gh api user/teams: %w", gerr)
		}
	} else {
		// One @org/team per line, blank lines from empty pages elided.
		for _, line := range strings.Split(stdout.String(), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			teams = append(teams, line)
		}
	}

	c.mu.Lock()
	c.authLogin = u.Login
	c.authTeams = teams
	c.authTeamsOK = teamsOK
	c.authResolved = true
	c.mu.Unlock()
	return u.Login, teams, teamsOK, nil
}

// CanListOrgTeams probes whether the current token has org-read access
// against the given org. Used to disambiguate the silent-empty trap
// where /user/teams returns 200+[] on a token without read:org instead
// of 403. HEAD /orgs/{org}/teams is the only honest signal: it returns
// 200 when the token can list (proves read:org or membership-equivalent
// scope) and 403 when it can't.
//
// Result is cached per process per org. No disk cache: the 403 case
// isn't cached by gh anyway (go-gh's cache.go explicitly skips 403),
// and the 200 case is cheap enough to re-probe each process.
func (c *Client) CanListOrgTeams(org string) bool {
	if org == "" {
		return false
	}
	if v, ok := c.orgReadable.Load(org); ok {
		return v.(bool)
	}
	// HEAD via gh api: -X HEAD sends no body, -i would print response
	// headers (we don't need them; exit code carries the verdict).
	_, _, err := gh.Exec("api", "-X", "HEAD", "orgs/"+org+"/teams")
	readable := err == nil
	c.orgReadable.Store(org, readable)
	return readable
}

// FetchPR returns the basic PR metadata.
func (c *Client) FetchPR(ref prref.Ref) (PR, error) {
	endpoint := fmt.Sprintf("repos/%s/%s/pulls/%d", ref.Owner, ref.Repo, ref.Number)
	var raw struct {
		Number         int    `json:"number"`
		HTMLURL        string `json:"html_url"`
		Title          string `json:"title"`
		State          string `json:"state"`
		Additions      int    `json:"additions"`
		Deletions      int    `json:"deletions"`
		Merged         bool   `json:"merged"`
		MergeCommitSHA string `json:"merge_commit_sha"`
		Labels         []struct {
			Name string `json:"name"`
		} `json:"labels"`
		Head struct {
			SHA string `json:"sha"`
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	if err := c.rest.Get(endpoint, &raw); err != nil {
		return PR{}, fmt.Errorf("GET %s: %w", endpoint, err)
	}
	labels := make([]string, 0, len(raw.Labels))
	for _, l := range raw.Labels {
		labels = append(labels, l.Name)
	}
	return PR{
		Number:         raw.Number,
		URL:            raw.HTMLURL,
		Title:          raw.Title,
		State:          raw.State,
		Additions:      raw.Additions,
		Deletions:      raw.Deletions,
		Labels:         labels,
		HeadSHA:        raw.Head.SHA,
		BaseRef:        raw.Base.Ref,
		Merged:         raw.Merged,
		MergeCommitSHA: raw.MergeCommitSHA,
	}, nil
}

// FetchPRFiles returns the per-file change counts for a PR. May make multiple
// API calls for large PRs (page size = 100, max 3000 files per gh API limit).
func (c *Client) FetchPRFiles(ref prref.Ref) ([]File, error) {
	endpoint := fmt.Sprintf("repos/%s/%s/pulls/%d/files?per_page=100", ref.Owner, ref.Repo, ref.Number)
	var files []File
	for page := 1; page <= 30; page++ {
		var raw []struct {
			Filename string `json:"filename"`
			Changes  int    `json:"changes"`
		}
		// go-gh's RESTClient handles pagination via Link header, but its
		// .Get() doesn't follow them automatically. Manual paging:
		url := fmt.Sprintf("%s&page=%d", endpoint, page)
		if err := c.rest.Get(url, &raw); err != nil {
			return nil, fmt.Errorf("GET %s: %w", url, err)
		}
		if len(raw) == 0 {
			break
		}
		for _, f := range raw {
			files = append(files, File{Path: f.Filename, Changes: f.Changes})
		}
		if len(raw) < 100 {
			break
		}
	}
	return files, nil
}

// FetchCodeowners returns the parsed CODEOWNERS ruleset for the repo at
// the given git ref (commit SHA or branch name). For PR scoring callers,
// pass pr.CodeownersRef(): that returns merge_commit_sha for merged PRs
// (stable historical evaluation) and the base branch name for open PRs
// (live evaluation, matching GitHub's own behavior).
//
// An empty ref resolves to the repo's default-branch HEAD on the server
// side. That is rarely what you want for PR scoring; the gh-cru caller
// always has either a merge SHA or a base branch name to pass.
//
// Returns (nil, false, nil) when the repo has no CODEOWNERS file in any
// standard location at that ref. Returns (nil, false, err) for actual
// API failures.
func (c *Client) FetchCodeowners(owner, repo, ref string) (codeowners.Ruleset, bool, error) {
	rs, _, ok, err := c.fetchCodeownersWithSource(owner, repo, ref)
	return rs, ok, err
}

// fetchCodeownersWithSource is the resolver. Returns ruleset, provenance
// ("local"/"cache"/"fresh"), and the usual ok/err pair.
//
// Resolution priority:
//
//  1. In-process cache keyed by owner/repo@ref (this CLI run).
//  2. Local working tree, ONLY when target repo == cwd repo AND ref looks
//     like a branch name (not a SHA) that matches the current branch.
//     Merged-PR scoring (which uses merge_commit_sha) never short-circuits
//     to local; we need the historical file at that exact commit.
//  3. Per CODEOWNERS path (.github/CODEOWNERS, CODEOWNERS, docs/CODEOWNERS):
//     HEAD to learn current ETag at ?ref=<ref>. If the disk cache already
//     has a body for that ETag, use it (browser-style hit; handles
//     rollbacks and shared-history hits across refs for free).
//     Otherwise GET the body and save it under <ref>/<etag>.
//  4. All three HEADs 404 → repo has no CODEOWNERS at this ref, mark
//     in-process so we don't re-probe within this run.
func (c *Client) fetchCodeownersWithSource(owner, repo, ref string) (codeowners.Ruleset, string, bool, error) {
	key := owner + "/" + repo + "@" + ref

	// 1. In-process cache (covers batches and the local-file path).
	c.mu.Lock()
	if entry, ok := c.owners[key]; ok {
		c.mu.Unlock()
		return entry.Rules, entry.Source, true, nil
	}
	if c.noOwners[key] {
		c.mu.Unlock()
		return nil, "", false, nil
	}
	c.mu.Unlock()

	// 2. Local working-tree file when target repo == cwd repo AND the
	// caller is asking about the current branch (open-PR case, not a
	// merged-PR scoring against a frozen SHA).
	if isLikelyBranchName(ref) {
		if local, err := TryLocalCodeowners(owner, repo); err == nil && local.Found {
			if currentBranch, berr := currentGitBranch(); berr == nil && currentBranch == ref {
				rs, perr := codeowners.ParseFile(bytes.NewReader(local.Body))
				if perr != nil {
					return nil, "", false, fmt.Errorf("parse local %s: %w", local.Path, perr)
				}
				c.rememberOwners(key, rs, "local")
				return rs, "local", true, nil
			}
		}
	}

	// 3. HEAD-then-(cache-or-GET) walk of the standard paths.
	body, source, err := c.resolveRemote(owner, repo, ref)
	if err != nil {
		return nil, "", false, err
	}
	if body == nil {
		c.mu.Lock()
		c.noOwners[key] = true
		c.mu.Unlock()
		return nil, "", false, nil
	}
	rs, err := codeowners.ParseFile(bytes.NewReader(body))
	if err != nil {
		return nil, "", false, fmt.Errorf("parse CODEOWNERS for %s/%s@%s: %w", owner, repo, ref, err)
	}
	c.rememberOwners(key, rs, source)
	return rs, source, true, nil
}

// rememberOwners populates the in-process cache. Convenience to keep the
// lock dance out of the resolver.
func (c *Client) rememberOwners(key string, rs codeowners.Ruleset, source string) {
	c.mu.Lock()
	c.owners[key] = ownersEntry{Rules: rs, Source: source}
	c.mu.Unlock()
}

// resolveRemote walks the standard CODEOWNERS paths in priority order at
// the given ref. For each path, HEAD reveals the current ETag; if the
// cache already has a body for that ETag, we use it. Otherwise GET + save.
// Returns nil body (no error) when all three paths 404 at this ref.
func (c *Client) resolveRemote(owner, repo, ref string) ([]byte, string, error) {
	for _, path := range []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"} {
		etag, status, err := c.headCodeowners(owner, repo, path, ref)
		if err != nil {
			return nil, "", err
		}
		if status == http.StatusNotFound {
			continue
		}
		if status != http.StatusOK || etag == "" {
			continue
		}

		// Browser-style cache hit: known ETag → known body.
		if c.disk != nil && c.disk.HasBody(owner, repo, ref, etag) {
			body, rerr := c.disk.ReadBody(owner, repo, ref, etag)
			if rerr == nil {
				return body, "cache", nil
			}
			// Corrupt cache entry; fall through to a fresh GET.
		}

		// Unknown ETag: fetch + cache.
		body, _, err := c.getCodeownersBody(owner, repo, path, ref)
		if err != nil {
			return nil, "", err
		}
		if body == nil {
			continue
		}
		if c.disk != nil {
			_ = c.disk.SaveBody(owner, repo, ref, etag, body)
		}
		return body, "fresh", nil
	}
	return nil, "", nil
}

// getCodeownersBody fetches the body for one path at the given ref.
// Returns (nil, "", nil) if the path doesn't exist at that ref (404).
// Blob SHA is returned for callers that want it, but the cache doesn't
// track it.
func (c *Client) getCodeownersBody(owner, repo, path, ref string) ([]byte, string, error) {
	endpoint := contentsEndpoint(owner, repo, path, ref)
	resp, err := c.rest.Request(http.MethodGet, endpoint, nil)
	if err != nil {
		if isNotFound(err) {
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("GET %s: %w", endpoint, err)
	}
	body, blobSHA, derr := decodeContentsResponse(resp, endpoint, c.fetchRawURL)
	resp.Body.Close()
	if derr != nil {
		return nil, "", derr
	}
	return body, blobSHA, nil
}

// headCodeowners issues a HEAD against the contents API for one path at
// the given ref. Returns ETag + status. We use HEAD (not a conditional
// GET with If-None-Match) because the gh REST client can't set custom
// headers. HEAD is a no-body request against a stock GET endpoint and
// works through the standard client.
func (c *Client) headCodeowners(owner, repo, path, ref string) (string, int, error) {
	endpoint := contentsEndpoint(owner, repo, path, ref)
	resp, err := c.rest.Request(http.MethodHead, endpoint, nil)
	if err != nil {
		if isNotFound(err) {
			return "", http.StatusNotFound, nil
		}
		return "", 0, err
	}
	defer resp.Body.Close()
	return resp.Header.Get("ETag"), resp.StatusCode, nil
}

// contentsEndpoint builds the contents API URL with optional ?ref=. We
// keep the ref as-passed (branch name or SHA); GitHub accepts both.
func contentsEndpoint(owner, repo, path, ref string) string {
	endpoint := fmt.Sprintf("repos/%s/%s/contents/%s", owner, repo, path)
	if ref != "" {
		endpoint += "?ref=" + ref
	}
	return endpoint
}

// isLikelyBranchName returns true when ref looks like a human branch name
// rather than a full commit SHA. Used to gate the local-CODEOWNERS
// short-circuit: we only want to short-circuit when the caller is asking
// about a branch we might have checked out, not a frozen SHA.
//
// Heuristic: anything that's not a 40-char hex string is treated as a
// branch name. Short SHAs (7-12 hex chars) are theoretically ambiguous
// but PR base.ref is always a full branch name, never a SHA, so this is
// safe for our callers.
func isLikelyBranchName(ref string) bool {
	if len(ref) != 40 {
		return true
	}
	for _, r := range ref {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return true
		}
	}
	return false
}

// currentGitBranch returns the name of the current branch from HEAD.
// Returns "" + error in detached-HEAD state or outside a repo.
func currentGitBranch() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	root := wd
	for {
		if fi, err := os.Stat(filepath.Join(root, ".git")); err == nil && (fi.IsDir() || fi.Mode().IsRegular()) {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			return "", fmt.Errorf("not in a git repo")
		}
		root = parent
	}
	headBytes, err := os.ReadFile(filepath.Join(root, ".git", "HEAD"))
	if err != nil {
		return "", err
	}
	head := strings.TrimSpace(string(headBytes))
	// "ref: refs/heads/<branch>" on a branch; bare SHA when detached.
	const prefix = "ref: refs/heads/"
	if strings.HasPrefix(head, prefix) {
		return strings.TrimPrefix(head, prefix), nil
	}
	return "", fmt.Errorf("detached HEAD")
}

// decodeContentsResponse parses a GitHub contents API JSON body and returns
// the decoded file content + blob sha. Handles both inline base64 (under
// 1MB) and the "encoding=none" case via download_url.
func decodeContentsResponse(resp *http.Response, endpoint string, rawFetch func(string) ([]byte, error)) ([]byte, string, error) {
	var meta struct {
		SHA         string `json:"sha"`
		Content     string `json:"content"`
		Encoding    string `json:"encoding"`
		DownloadURL string `json:"download_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return nil, "", fmt.Errorf("decode %s: %w", endpoint, err)
	}
	switch meta.Encoding {
	case "base64":
		clean := strings.NewReplacer("\n", "", "\r", "", " ", "").Replace(meta.Content)
		decoded, err := base64.StdEncoding.DecodeString(clean)
		if err != nil {
			return nil, "", fmt.Errorf("base64 decode %s: %w", endpoint, err)
		}
		return decoded, meta.SHA, nil
	case "none", "":
		if meta.DownloadURL == "" {
			return nil, "", fmt.Errorf("no download_url for large file %s", endpoint)
		}
		raw, err := rawFetch(meta.DownloadURL)
		if err != nil {
			return nil, "", fmt.Errorf("fetch raw %s: %w", meta.DownloadURL, err)
		}
		return raw, meta.SHA, nil
	default:
		return nil, "", fmt.Errorf("unexpected encoding %q for %s", meta.Encoding, endpoint)
	}
}

// fetchRawURL downloads a file via its absolute https URL. Used for files
// too large to inline through the contents API.
func (c *Client) fetchRawURL(rawURL string) ([]byte, error) {
	resp, err := http.Get(rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	buf := bytes.NewBuffer(nil)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func isNotFound(err error) bool {
	var httpErr *api.HTTPError
	if !errors.As(err, &httpErr) {
		return strings.Contains(err.Error(), "404")
	}
	return httpErr.StatusCode == 404
}
