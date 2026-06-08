// Package gh wraps the gh CLI's API clients with the shape gh-cru needs:
// a single fetch entry point per PR, persistent on-disk CODEOWNERS caching
// (so `gh pr list | xargs gh cru` reuses the 2.5MB github/github fetch
// across subprocess boundaries), and a per-process in-memory cache for
// batched runs.
package gh

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

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
	owners   map[string]ownersEntry        // per-process cache (batch within one CLI run)
	noOwners map[string]bool               // repos with no CODEOWNERS file at any standard path

	authLogin    string
	authTeams    []string
	authTeamsOK  bool
	authResolved bool
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
	Number    int
	Title     string
	Author    string
	State     string
	Additions int
	Deletions int
	Files     int
	Labels    []string
	HeadSHA   string
	BaseRef   string
}

// LOC is the standard size measure: additions + deletions.
func (p PR) LOC() int { return p.Additions + p.Deletions }

// File is one changed file in a PR with its delta size.
type File struct {
	Path    string
	Changes int // additions + deletions
}

// AuthIdentities returns the authenticated user's CODEOWNERS-compatible
// identities: their @login plus every @org/team-slug they belong to. These
// are the strings that match against codeowners rule owners.
//
// teamsOK indicates whether team enumeration via /user/teams succeeded. It
// returns false when the token lacks `read:org` scope (e.g. the default
// Codespaces GITHUB_TOKEN, fine-grained PATs without org permissions, or
// any other narrow-scope token). When teamsOK is false, identities still
// contains the @login so direct CODEOWNERS matches work, but team-based
// ownership won't be detected - surface this to the user.
//
// Resolved once per process (paginated /user/teams may cost a few API calls
// for accounts on many teams; cached afterwards).
func (c *Client) AuthIdentities() (login string, identities []string, teamsOK bool, err error) {
	c.mu.Lock()
	if c.authResolved {
		l, t, ok := c.authLogin, c.authTeams, c.authTeamsOK
		c.mu.Unlock()
		return l, t, ok, nil
	}
	c.mu.Unlock()

	// 1. /user
	var u struct {
		Login string `json:"login"`
	}
	if err := c.rest.Get("user", &u); err != nil {
		return "", nil, false, fmt.Errorf("GET /user: %w", err)
	}

	// 2. /user/teams (paginated). Each team contributes "@org/team-slug".
	teams := []string{"@" + u.Login}
	teamsOK = true
	for page := 1; page <= 50; page++ {
		var batch []struct {
			Slug         string `json:"slug"`
			Organization struct {
				Login string `json:"login"`
			} `json:"organization"`
		}
		endpoint := fmt.Sprintf("user/teams?per_page=100&page=%d", page)
		if err := c.rest.Get(endpoint, &batch); err != nil {
			// /user/teams requires read:org. If the token lacks it (e.g.
			// Codespaces ghu_ tokens, fine-grained PATs without the right
			// permissions), record that and surface it to the caller -
			// we still have the @login but team-based ownership won't
			// be detected.
			if isForbidden(err) || isNotFound(err) {
				teamsOK = false
				break
			}
			return "", nil, false, fmt.Errorf("GET %s: %w", endpoint, err)
		}
		if len(batch) == 0 {
			break
		}
		for _, t := range batch {
			if t.Slug == "" || t.Organization.Login == "" {
				continue
			}
			teams = append(teams, fmt.Sprintf("@%s/%s", t.Organization.Login, t.Slug))
		}
		if len(batch) < 100 {
			break
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

// FetchPR returns the basic PR metadata.
func (c *Client) FetchPR(ref prref.Ref) (PR, error) {
	endpoint := fmt.Sprintf("repos/%s/%s/pulls/%d", ref.Owner, ref.Repo, ref.Number)
	var raw struct {
		Number    int    `json:"number"`
		Title     string `json:"title"`
		State     string `json:"state"`
		Additions int    `json:"additions"`
		Deletions int    `json:"deletions"`
		Files     int    `json:"changed_files"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
		Labels []struct {
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
		Number:    raw.Number,
		Title:     raw.Title,
		Author:    raw.User.Login,
		State:     raw.State,
		Additions: raw.Additions,
		Deletions: raw.Deletions,
		Files:     raw.Files,
		Labels:    labels,
		HeadSHA:   raw.Head.SHA,
		BaseRef:   raw.Base.Ref,
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

// FetchCodeowners returns the parsed CODEOWNERS ruleset for the repo.
// Resolution priority:
//
//  1. Local working tree, if the target repo == the current git repo.
//     Uncached — the file may include unmerged edits to ownership.
//  2. In-process cache from earlier PR(s) this run.
//  3. On-disk cache pointer, if fresh (< DefaultTTL).
//  4. On-disk cache pointer w/ conditional GET (If-None-Match), if present.
//  5. Cold fetch from the contents API.
//
// Returns (nil, false, nil) when the repo has no CODEOWNERS file in any
// standard location. Returns (nil, false, err) for actual API failures.
func (c *Client) FetchCodeowners(owner, repo string) (codeowners.Ruleset, bool, error) {
	rs, _, ok, err := c.fetchCodeownersWithSource(owner, repo)
	return rs, ok, err
}

// fetchCodeownersWithSource is the resolver. Returns ruleset, provenance
// ("local"/"cache"/"fresh"), and the usual ok/err pair.
//
// Resolution priority:
//
//  1. In-process cache (this CLI run).
//  2. Local working tree, if target repo == cwd repo (uncached: may
//     contain unmerged edits to ownership).
//  3. Per CODEOWNERS path (.github/CODEOWNERS, CODEOWNERS, docs/CODEOWNERS):
//     HEAD to learn current ETag. If the disk cache already has a body
//     for that ETag, use it (browser-style hit; handles rollbacks too).
//     Otherwise GET the body and save it.
//  4. All three HEADs 404 → repo has no CODEOWNERS, mark in-process so
//     we don't re-probe within this run.
func (c *Client) fetchCodeownersWithSource(owner, repo string) (codeowners.Ruleset, string, bool, error) {
	key := owner + "/" + repo

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

	// 2. Local working-tree file when target repo == cwd repo.
	if local, err := TryLocalCodeowners(owner, repo); err == nil && local.Found {
		rs, perr := codeowners.ParseFile(bytes.NewReader(local.Body))
		if perr != nil {
			return nil, "", false, fmt.Errorf("parse local %s: %w", local.Path, perr)
		}
		c.rememberOwners(key, rs, "local")
		return rs, "local", true, nil
	}

	// 3. HEAD-then-(cache-or-GET) walk of the standard paths.
	body, source, err := c.resolveRemote(owner, repo)
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
		return nil, "", false, fmt.Errorf("parse CODEOWNERS for %s/%s: %w", owner, repo, err)
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

// resolveRemote walks the standard CODEOWNERS paths in priority order.
// For each path, HEAD reveals the current ETag; if the cache already has
// a body for that ETag, we use it. Otherwise GET + save. Returns nil body
// (no error) when all three paths 404.
func (c *Client) resolveRemote(owner, repo string) ([]byte, string, error) {
	for _, path := range []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"} {
		etag, status, err := c.headCodeowners(owner, repo, path)
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
		if c.disk != nil && c.disk.HasBody(owner, repo, etag) {
			body, rerr := c.disk.ReadBody(owner, repo, etag)
			if rerr == nil {
				return body, "cache", nil
			}
			// Corrupt cache entry; fall through to a fresh GET.
		}

		// Unknown ETag: fetch + cache.
		body, _, err := c.getCodeownersBody(owner, repo, path)
		if err != nil {
			return nil, "", err
		}
		if body == nil {
			continue
		}
		if c.disk != nil {
			_ = c.disk.SaveBody(owner, repo, etag, body)
		}
		return body, "fresh", nil
	}
	return nil, "", nil
}

// getCodeownersBody fetches the body for one path. Returns (nil, "", nil)
// if the path doesn't exist (404). Blob SHA is returned for callers that
// want it, but the cache doesn't track it.
func (c *Client) getCodeownersBody(owner, repo, path string) ([]byte, string, error) {
	endpoint := fmt.Sprintf("repos/%s/%s/contents/%s", owner, repo, path)
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

// headCodeowners issues a HEAD against the contents API for one path.
// Returns ETag + status. We use HEAD (not a conditional GET with
// If-None-Match) because the gh REST client can't set custom headers.
// HEAD is a no-body request against a stock GET endpoint and works
// through the standard client.
func (c *Client) headCodeowners(owner, repo, path string) (string, int, error) {
	endpoint := fmt.Sprintf("repos/%s/%s/contents/%s", owner, repo, path)
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
		decoded, err := base64Decode(clean)
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

func isForbidden(err error) bool {
	var httpErr *api.HTTPError
	if !errors.As(err, &httpErr) {
		return strings.Contains(err.Error(), "403")
	}
	return httpErr.StatusCode == 403
}

// errAs is errors.As without dragging in the import where unnecessary.
// Deprecated: kept for backwards compat; use errors.As directly.
func errAs(err error, target any) bool {
	return errors.As(err, target)
}
