// Package cache is gh-cru's persistent disk cache for CODEOWNERS bodies.
// It survives across CLI invocations so that `gh pr list | xargs gh cru`
// runs as a sequence of fast subprocess calls instead of repeatedly
// re-downloading the same 2.5MB CODEOWNERS file from github/github.
//
// Layout under base directory (defaults to os.UserCacheDir()/gh-cru):
//
//	codeowners/<owner>/<repo>/<etag>.codeowners.txt
//
// One file per body, keyed by HTTP ETag. No pointer file, no metadata —
// the body file IS the cache entry. The resolver always issues a HEAD
// (up to 3 paths, one per standard CODEOWNERS location) to learn the
// current ETag, then asks the cache "do you have a body for this?".
// A HEAD that reveals a known ETag short-circuits the 2.5MB GET; an
// unknown ETag triggers a cold fetch + save. This also gracefully handles
// upstream rollbacks: same ETag → same body, even from old history.
package cache

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Cache reads and writes CODEOWNERS bodies under base.
type Cache struct {
	base string // <user-cache-dir>/gh-cru
}

// New returns a Cache rooted at base. If base is empty, uses
// os.UserCacheDir() + "/gh-cru".
func New(base string) (*Cache, error) {
	if base == "" {
		root, err := os.UserCacheDir()
		if err != nil {
			return nil, fmt.Errorf("user cache dir: %w", err)
		}
		base = filepath.Join(root, "gh-cru")
	}
	return &Cache{base: base}, nil
}

// Base returns the cache root for debugging / inspection.
func (c *Cache) Base() string { return c.base }

// HasBody reports whether a body file exists for the given ETag.
func (c *Cache) HasBody(owner, repo, etag string) bool {
	_, err := os.Stat(c.bodyPath(owner, repo, etag))
	return err == nil
}

// ReadBody returns the cached body for an ETag.
func (c *Cache) ReadBody(owner, repo, etag string) ([]byte, error) {
	return os.ReadFile(c.bodyPath(owner, repo, etag))
}

// SaveBody writes the body atomically. Safe to call concurrently for the
// same ETag (writers will race but produce identical content).
func (c *Cache) SaveBody(owner, repo, etag string, body []byte) error {
	if etag == "" {
		return fmt.Errorf("SaveBody: empty ETag")
	}
	dir := c.repoDir(owner, repo)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	return writeAtomic(c.bodyPath(owner, repo, etag), body)
}

func (c *Cache) repoDir(owner, repo string) string {
	return filepath.Join(c.base, "codeowners", owner, repo)
}

func (c *Cache) bodyPath(owner, repo, etag string) string {
	return filepath.Join(c.repoDir(owner, repo), etagFileKey(etag)+".codeowners.txt")
}

// etagFileKey derives a filesystem-safe filename component from a raw
// ETag header. GitHub ETags are typically `"<hex>"` or `W/"<hex>"`; we
// strip quotes, slashes, and the weak prefix so paths stay flat and
// predictable.
var etagSanitize = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

func etagFileKey(etag string) string {
	s := strings.TrimSpace(etag)
	s = strings.TrimPrefix(s, "W/")
	s = strings.Trim(s, `"`)
	s = etagSanitize.ReplaceAllString(s, "_")
	if s == "" {
		s = "unknown"
	}
	return s
}

// writeAtomic writes content to path via a temp file + rename so concurrent
// readers never see a partial file.
func writeAtomic(path string, content []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
