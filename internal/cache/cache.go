// Package cache is gh-cru's persistent disk cache for CODEOWNERS bodies.
// It survives across CLI invocations so that `gh pr list | xargs gh cru`
// runs as a sequence of fast subprocess calls instead of repeatedly
// re-downloading the same multi-megabyte CODEOWNERS file from a large
// monorepo.
//
// Layout under base directory (defaults to os.UserCacheDir()/gh-cru):
//
//	codeowners/<owner>/<repo>/<ref>/<etag>.codeowners
//
// One file per body, keyed by (ref, ETag). The ref segment partitions
// the cache cleanly by git ref (commit SHA or branch name): different
// refs that happen to share a CODEOWNERS body still get their own entry,
// which keeps the on-disk layout self-describing and rules out cross-ref
// collisions. The resolver always issues a HEAD (up to 3 paths, one per
// standard CODEOWNERS location) to learn the current ETag at that ref,
// then asks the cache "do you have a body for this?". A HEAD that
// reveals a known ETag short-circuits the 2.5MB GET; an unknown ETag
// triggers a cold fetch + save. This also gracefully handles upstream
// rollbacks: same ETag at the same ref → same body, even from old
// history.
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

// HasBody reports whether a body file exists for the given (ref, ETag).
func (c *Cache) HasBody(owner, repo, ref, etag string) bool {
	_, err := os.Stat(c.bodyPath(owner, repo, ref, etag))
	return err == nil
}

// ReadBody returns the cached body for a (ref, ETag) pair.
func (c *Cache) ReadBody(owner, repo, ref, etag string) ([]byte, error) {
	return os.ReadFile(c.bodyPath(owner, repo, ref, etag))
}

// SaveBody writes the body atomically. Safe to call concurrently for the
// same (ref, ETag); writers will race but produce identical content.
func (c *Cache) SaveBody(owner, repo, ref, etag string, body []byte) error {
	if etag == "" {
		return fmt.Errorf("SaveBody: empty ETag")
	}
	dir := c.refDir(owner, repo, ref)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	return writeAtomic(c.bodyPath(owner, repo, ref, etag), body)
}

func (c *Cache) refDir(owner, repo, ref string) string {
	return filepath.Join(c.base, "codeowners", owner, repo, refKey(ref))
}

func (c *Cache) bodyPath(owner, repo, ref, etag string) string {
	return filepath.Join(c.refDir(owner, repo, ref), etagFileKey(etag)+".codeowners")
}

// refKey sanitizes a git ref (commit SHA or branch name) into a single
// filesystem-safe path segment. SHAs pass through unchanged; branch names
// have slashes and other separators normalized so e.g. "release/v2" stays
// inside one directory level.
var refSanitize = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

func refKey(ref string) string {
	s := strings.TrimSpace(ref)
	if s == "" {
		return "default"
	}
	s = refSanitize.ReplaceAllString(s, "_")
	return s
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
