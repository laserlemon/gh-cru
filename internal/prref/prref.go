// Package prref parses PR references in the formats gh accepts:
//
//	123                                          (bare number, repo inferred from cwd)
//	owner/repo#123                               (shorthand)
//	https://github.com/owner/repo/pull/123       (URL)
//	https://github.com/owner/repo/pull/123/...   (URL with extra path)
//
// When the bare-number form is used and no --repo flag was passed, the caller
// is expected to fall back to the git context (gh-go's repository.Current).
package prref

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Ref is a fully-resolved PR reference.
type Ref struct {
	Owner  string
	Repo   string
	Number int
}

var (
	bareNumber = regexp.MustCompile(`^#?(\d+)$`)
	shorthand  = regexp.MustCompile(`^([^/\s]+)/([^/\s#]+)#(\d+)$`)
	urlPath    = regexp.MustCompile(`^/([^/]+)/([^/]+)/pull/(\d+)`)
)

// Parse resolves a single PR reference. Pass defaultOwner/defaultRepo (from
// --repo or git context) so bare numbers and unqualified shorthands can be
// completed. Returns an error if the bare form is used with no default repo.
func Parse(s, defaultOwner, defaultRepo string) (Ref, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Ref{}, fmt.Errorf("empty PR reference")
	}

	if m := shorthand.FindStringSubmatch(s); m != nil {
		num, _ := strconv.Atoi(m[3])
		return Ref{Owner: m[1], Repo: m[2], Number: num}, nil
	}

	if m := bareNumber.FindStringSubmatch(s); m != nil {
		num, _ := strconv.Atoi(m[1])
		if defaultOwner == "" || defaultRepo == "" {
			return Ref{}, fmt.Errorf("bare PR number %q given but no repo context (use --repo OWNER/NAME or run inside a git repo)", s)
		}
		return Ref{Owner: defaultOwner, Repo: defaultRepo, Number: num}, nil
	}

	// URL forms - github.com/foo/bar/pull/N, http://, https://, with or without path.
	if strings.Contains(s, "/pull/") {
		u, err := url.Parse(s)
		if err == nil {
			if m := urlPath.FindStringSubmatch(u.Path); m != nil {
				num, _ := strconv.Atoi(m[3])
				return Ref{Owner: m[1], Repo: m[2], Number: num}, nil
			}
		}
	}

	return Ref{}, fmt.Errorf("unrecognized PR reference: %q", s)
}
