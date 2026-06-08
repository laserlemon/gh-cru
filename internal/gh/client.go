// Package gh wraps the gh CLI's API clients with the shape gh-cru needs:
// a single fetch entry point per PR, and a per-process CODEOWNERS cache so
// batched runs (`gh cru 1 2 3 4 5`) only hit the CODEOWNERS API once per repo.
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

	"github.com/laserlemon/gh-cru/internal/prref"
)

// Client is the gh-cru API client. Safe for concurrent use.
type Client struct {
	rest *api.RESTClient

	mu       sync.Mutex
	owners   map[string]codeowners.Ruleset // key: owner/repo@default_branch_sha (we use default branch ref)
	noOwners map[string]bool               // repos with no CODEOWNERS file at any standard path

	authLogin    string
	authTeams    []string
	authTeamsOK  bool
	authResolved bool
}

// NewClient builds a Client using gh's default auth.
func NewClient() (*Client, error) {
	rest, err := api.DefaultRESTClient()
	if err != nil {
		return nil, fmt.Errorf("gh REST client: %w", err)
	}
	return &Client{
		rest:     rest,
		owners:   make(map[string]codeowners.Ruleset),
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

// FetchCodeowners returns the parsed CODEOWNERS ruleset for the repo, cached
// for the lifetime of the Client.
//
// Returns (nil, false, nil) when the repo has no CODEOWNERS file in any
// standard location. Returns (nil, false, err) for actual API failures.
//
// We key the cache on owner/repo (not a ref/sha) because gh-cru is a
// short-lived CLI invocation; the few seconds of skew between scoring
// adjacent PRs is not worth the extra round trips to pin per-PR SHAs.
func (c *Client) FetchCodeowners(owner, repo string) (codeowners.Ruleset, bool, error) {
	key := owner + "/" + repo
	c.mu.Lock()
	if rs, ok := c.owners[key]; ok {
		c.mu.Unlock()
		return rs, true, nil
	}
	if c.noOwners[key] {
		c.mu.Unlock()
		return nil, false, nil
	}
	c.mu.Unlock()

	// Try the standard locations in priority order.
	body, err := c.fetchCodeownersRaw(owner, repo)
	if err != nil {
		return nil, false, err
	}
	if body == nil {
		c.mu.Lock()
		c.noOwners[key] = true
		c.mu.Unlock()
		return nil, false, nil
	}

	rs, err := codeowners.ParseFile(bytes.NewReader(body))
	if err != nil {
		return nil, false, fmt.Errorf("parse CODEOWNERS for %s/%s: %w", owner, repo, err)
	}
	c.mu.Lock()
	c.owners[key] = rs
	c.mu.Unlock()
	return rs, true, nil
}

func (c *Client) fetchCodeownersRaw(owner, repo string) ([]byte, error) {
	paths := []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"}
	for _, p := range paths {
		endpoint := fmt.Sprintf("repos/%s/%s/contents/%s", owner, repo, p)
		resp, err := c.rest.Request(http.MethodGet, endpoint, nil)
		if err != nil {
			if isNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("GET %s: %w", endpoint, err)
		}
		defer resp.Body.Close()
		var meta struct {
			Content     string `json:"content"`
			Encoding    string `json:"encoding"`
			DownloadURL string `json:"download_url"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
			return nil, fmt.Errorf("decode %s: %w", endpoint, err)
		}
		switch meta.Encoding {
		case "base64":
			clean := strings.NewReplacer("\n", "", "\r", "", " ", "").Replace(meta.Content)
			decoded, err := base64Decode(clean)
			if err != nil {
				return nil, fmt.Errorf("base64 decode %s: %w", endpoint, err)
			}
			return decoded, nil
		case "none", "":
			// File too large to inline. Fetch the raw download_url.
			if meta.DownloadURL == "" {
				return nil, fmt.Errorf("no download_url for large file %s", endpoint)
			}
			raw, err := c.fetchRawURL(meta.DownloadURL)
			if err != nil {
				return nil, fmt.Errorf("fetch raw %s: %w", meta.DownloadURL, err)
			}
			return raw, nil
		default:
			return nil, fmt.Errorf("unexpected encoding %q for %s", meta.Encoding, endpoint)
		}
	}
	return nil, nil
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
