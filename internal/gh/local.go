// Local-CODEOWNERS support: when the target repo matches the current
// working tree, read CODEOWNERS directly from disk. This is the codespace /
// checkout-while-developing case where the file is right there and may
// even be ahead of `main` with edits that affect ownership of the PR
// being scored.
package gh

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cli/go-gh/v2/pkg/repository"
)

// LocalCodeownersResult describes what (if anything) was found on disk.
type LocalCodeownersResult struct {
	Found   bool
	Path    string // absolute path to the CODEOWNERS file
	RelPath string // ".github/CODEOWNERS" / "CODEOWNERS" / "docs/CODEOWNERS"
	Body    []byte
}

// TryLocalCodeowners returns a populated result when the target repo
// matches the current working tree and a CODEOWNERS file is present at
// one of GitHub's standard locations. Returns Found=false (and no error)
// for all "not applicable" cases: no git repo here, mismatched repo,
// no CODEOWNERS file. Returns an error only for unexpected disk failures.
//
// Resolution order matches GitHub's: .github/CODEOWNERS, CODEOWNERS,
// docs/CODEOWNERS - first match wins.
func TryLocalCodeowners(targetOwner, targetRepo string) (LocalCodeownersResult, error) {
	current, err := repository.Current()
	if err != nil {
		// Not in a repo (or no github.com remote); silent miss.
		return LocalCodeownersResult{}, nil
	}
	if !strings.EqualFold(current.Owner, targetOwner) || !strings.EqualFold(current.Name, targetRepo) {
		return LocalCodeownersResult{}, nil
	}

	root, err := gitRoot()
	if err != nil {
		return LocalCodeownersResult{}, nil
	}

	for _, rel := range []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"} {
		full := filepath.Join(root, rel)
		body, err := os.ReadFile(full)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return LocalCodeownersResult{}, fmt.Errorf("read %s: %w", full, err)
		}
		return LocalCodeownersResult{
			Found:   true,
			Path:    full,
			RelPath: rel,
			Body:    body,
		}, nil
	}
	return LocalCodeownersResult{}, nil
}

// gitRoot returns the working-tree root by walking upward looking for
// a .git directory. We avoid shelling out to git for speed; this also
// dodges environments where the git binary isn't on PATH.
func gitRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		fi, err := os.Stat(filepath.Join(wd, ".git"))
		if err == nil && (fi.IsDir() || fi.Mode().IsRegular()) {
			return wd, nil
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			return "", errors.New("not in a git repo")
		}
		wd = parent
	}
}
