// Package input parses gh-cru's PR inputs from the JSON gh emits.
//
// gh cru shells out to `gh pr view`/`gh pr list` and feeds their JSON
// output here. Parse accepts the three shapes that path can produce: a
// JSON array (gh pr list), NDJSON (one object per line), or a single
// JSON object (gh pr view). Each object may carry just a ref (e.g.
// {"url": "..."}) or pre-fetched scoring fields that bypass the PR API
// call entirely.
//
// The returned []Input preserves source ordering so the formatter prints
// PRs in the order gh returned them.
package input

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	ghc "github.com/laserlemon/gh-cru/internal/gh"
	"github.com/laserlemon/gh-cru/internal/prref"
)

// Input is one PR's worth of data heading into the scorer.
//
// Ref is always populated. PR and Files are optional; when present, the
// caller may skip the corresponding API fetches. When PR is nil the caller
// must fetch the PR metadata; when Files is nil the caller must fetch the
// PR file list (only when ownership scoring is enabled).
type Input struct {
	Ref   prref.Ref
	PR    *ghc.PR    // nil → caller must fetch
	Files []ghc.File // nil → caller must fetch (when ownership scoring is on)
}

// Parse turns gh's JSON output into a slice of Inputs.
//
//	r       - reader over gh's stdout (array, NDJSON, or a single object).
//	defOwner,
//	defRepo - defaults for entries that carry a bare number but no repo
//	          (from --repo or git context).
//
// Returns the parsed inputs in source order. Errors abort the whole
// batch; we don't silently drop bad rows from a piped list.
func Parse(r io.Reader, defOwner, defRepo string) ([]Input, error) {
	if r == nil {
		return nil, nil
	}
	return parseStdin(r, defOwner, defRepo)
}

// parseStdin reads the full gh payload and dispatches based on shape:
// JSON array (gh pr list), NDJSON (one object per line), or a single
// multi-line JSON object (gh pr view). gh always emits one of these, so
// there's no bare-ref handling here; identity comes from each object's
// url/repository/repo/number fields.
func parseStdin(r io.Reader, defOwner, defRepo string) ([]Input, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read input: %w", err)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	switch trimmed[0] {
	case '[':
		return parseJSONArray(trimmed, defOwner, defRepo)
	case '{':
		// Could be a single (possibly multi-line) JSON object OR NDJSON.
		// Try whole-blob first; succeeds for pretty-printed `gh ... | jq .`
		// output. Falls back to NDJSON when there's more than one object.
		if obj, ok := tryDecodeSingleObject(trimmed); ok {
			in, err := parseJSONObject(obj, defOwner, defRepo)
			if err != nil {
				return nil, fmt.Errorf("gh output: %w", err)
			}
			return []Input{in}, nil
		}
		return parseNDJSON(trimmed, defOwner, defRepo)
	default:
		return nil, fmt.Errorf("gh output: expected JSON object or array, got %q...", previewBytes(trimmed))
	}
}

// tryDecodeSingleObject returns the trimmed input back unchanged when
// it parses as exactly one top-level JSON object with no trailing data.
// Falls through to NDJSON detection otherwise.
func tryDecodeSingleObject(raw []byte) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var probe json.RawMessage
	if err := dec.Decode(&probe); err != nil {
		return nil, false
	}
	// Whatever's left after the first object decoding (only single-object
	// payloads have nothing significant trailing).
	rest := bytes.TrimSpace(raw[dec.InputOffset():])
	if len(rest) > 0 {
		return nil, false
	}
	return probe, true
}

// parseJSONArray handles a single top-level JSON array of PR objects.
func parseJSONArray(raw []byte, defOwner, defRepo string) ([]Input, error) {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, fmt.Errorf("decode JSON array: %w", err)
	}
	out := make([]Input, 0, len(arr))
	for i, entry := range arr {
		in, err := parseJSONObject(entry, defOwner, defRepo)
		if err != nil {
			return nil, fmt.Errorf("gh output[%d]: %w", i, err)
		}
		out = append(out, in)
	}
	return out, nil
}

// parseNDJSON handles one JSON object per line. Bufio's default 64KB token
// limit isn't enough for some `gh pr list` rows that include large file
// lists, so we use a larger buffer.
func parseNDJSON(raw []byte, defOwner, defRepo string) ([]Input, error) {
	out := make([]Input, 0)
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	const maxLine = 8 * 1024 * 1024 // 8MB per line; gh pr list rarely exceeds 100KB
	scanner.Buffer(make([]byte, 64*1024), maxLine)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		in, err := parseJSONObject(line, defOwner, defRepo)
		if err != nil {
			return nil, fmt.Errorf("gh output line %d: %w", lineNum, err)
		}
		out = append(out, in)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read gh output: %w", err)
	}
	return out, nil
}

// jsonPR mirrors the union of fields gh pr list emits and a few extra
// shorthands we accept. All fields are optional except enough to resolve
// owner+repo+number.
type jsonPR struct {
	// Identity (one of these must yield owner+repo+number)
	URL    string `json:"url"`    // https://github.com/o/r/pull/N
	Number int    `json:"number"` // bare PR number
	Repo   string `json:"repo"`   // "owner/name" shorthand for --repo
	// gh's nested repository object, as emitted by `gh search prs --json
	// repository`: {name, nameWithOwner}. nameWithOwner ("owner/name")
	// is the part we need; name alone can't resolve the owner.
	Repository *struct {
		Name          string `json:"name"`          // bare repo name, e.g. "cli"
		NameWithOwner string `json:"nameWithOwner"` // "owner/name", e.g. "cli/cli"
	} `json:"repository"`

	// Scoring fields (pre-fetched optional)
	Title       string `json:"title"`
	State       string `json:"state"`
	Additions   int    `json:"additions"`
	Deletions   int    `json:"deletions"`
	HeadSHA     string `json:"headRefOid"`
	BaseRef     string `json:"baseRefName"`
	MergeCommit *struct {
		Oid string `json:"oid"`
	} `json:"mergeCommit"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Files []struct {
		Path      string `json:"path"`
		Additions int    `json:"additions"`
		Deletions int    `json:"deletions"`
	} `json:"files"`
}

// parseJSONObject turns one PR JSON entry into an Input. Identity
// resolution priority:
//
//  1. url:   full https://github.com/owner/repo/pull/N URL (gh-canonical)
//  2. repository.owner + .name + number: gh pr list shape
//  3. repo + number: our shorthand ("owner/name")
//  4. number alone: uses defOwner/defRepo from --repo or git context
//
// JSON-supplied repo info ALWAYS overrides --repo and git context. Each
// row may come from a different repo (typical with `gh pr list --search`
// across orgs), so a single --repo default can't apply uniformly.
func parseJSONObject(raw []byte, defOwner, defRepo string) (Input, error) {
	var j jsonPR
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // tolerate large numeric ids without precision loss
	dec.DisallowUnknownFields()
	// Re-decode without DisallowUnknownFields if we hit one (gh pr list
	// can include fields we don't know about). Two-pass keeps strict
	// errors for the developer-facing case while staying lenient with
	// real-world input.
	if err := dec.Decode(&j); err != nil {
		j = jsonPR{}
		if err2 := json.Unmarshal(raw, &j); err2 != nil {
			return Input{}, fmt.Errorf("decode JSON: %w", err2)
		}
	}

	owner, repo, number, err := resolveIdentity(j, defOwner, defRepo)
	if err != nil {
		return Input{}, err
	}
	ref := prref.Ref{Owner: owner, Repo: repo, Number: number}
	in := Input{Ref: ref}

	// Promote PR + files when the JSON carries enough to score without
	// fetching. The bar is: additions, deletions, changedFiles present
	// AND (for unowned-or-CODEOWNERS work) either a head SHA or merge
	// commit so CODEOWNERS resolves to the right ref.
	if hasScoringFields(j) {
		pr := jsonToPR(j, number)
		in.PR = &pr
	}
	if len(j.Files) > 0 {
		files := make([]ghc.File, 0, len(j.Files))
		for _, f := range j.Files {
			files = append(files, ghc.File{
				Path:    f.Path,
				Changes: f.Additions + f.Deletions,
			})
		}
		in.Files = files
	}
	return in, nil
}

// resolveIdentity unwinds the four ways a JSON entry can identify a PR.
// Returns an error when no consistent owner+repo+number can be derived.
func resolveIdentity(j jsonPR, defOwner, defRepo string) (string, string, int, error) {
	// 1. URL is the strongest signal.
	if j.URL != "" {
		ref, err := prref.Parse(j.URL, defOwner, defRepo)
		if err != nil {
			return "", "", 0, fmt.Errorf("parse url %q: %w", j.URL, err)
		}
		return ref.Owner, ref.Repo, ref.Number, nil
	}
	// Number is required for the remaining paths.
	if j.Number == 0 {
		return "", "", 0, fmt.Errorf("entry has no url and no number")
	}
	// 2. repository.nameWithOwner ("owner/name"), gh's nested shape.
	if j.Repository != nil && j.Repository.NameWithOwner != "" {
		parts := strings.SplitN(j.Repository.NameWithOwner, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", "", 0, fmt.Errorf("bad repository.nameWithOwner %q (want owner/name)", j.Repository.NameWithOwner)
		}
		return parts[0], parts[1], j.Number, nil
	}
	// 3. repo: "owner/name" shorthand.
	if j.Repo != "" {
		parts := strings.SplitN(j.Repo, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", "", 0, fmt.Errorf("bad repo shorthand %q (want owner/name)", j.Repo)
		}
		return parts[0], parts[1], j.Number, nil
	}
	// 4. Fall back to defaults.
	if defOwner == "" || defRepo == "" {
		return "", "", 0, fmt.Errorf("entry has number %d but no repo (need url/repository/repo, or --repo / git context)", j.Number)
	}
	return defOwner, defRepo, j.Number, nil
}

// hasScoringFields returns true when the JSON carries enough metadata
// for score.Compute to run without an extra PR fetch. Files are checked
// separately by the caller.
func hasScoringFields(j jsonPR) bool {
	// We trust the source: any non-zero LOC field signals "scoring
	// fields included." A truly empty PR (0 additions + 0 deletions)
	// is unusual; force re-fetch in that case to be safe.
	return j.Additions != 0 || j.Deletions != 0 || j.State != "" || j.MergeCommit != nil
}

// jsonToPR projects the JSON shape into the internal ghc.PR struct used
// by the scorer. State is lower-cased to match gh CLI's "OPEN"/"MERGED"
// versus the REST API's "open"/"merged". Merged status rides on the
// canonical state enum (state == "MERGED"), with a non-nil mergeCommit as
// a backstop for any source that supplies the merge ref without the state.
func jsonToPR(j jsonPR, number int) ghc.PR {
	merged := strings.EqualFold(j.State, "merged") ||
		j.MergeCommit != nil
	pr := ghc.PR{
		Number:    number,
		URL:       j.URL,
		Title:     j.Title,
		State:     strings.ToLower(j.State),
		Additions: j.Additions,
		Deletions: j.Deletions,
		HeadSHA:   j.HeadSHA,
		BaseRef:   j.BaseRef,
		Merged:    merged,
	}
	if j.MergeCommit != nil {
		pr.MergeCommitSHA = j.MergeCommit.Oid
	}
	pr.Labels = make([]string, 0, len(j.Labels))
	for _, l := range j.Labels {
		pr.Labels = append(pr.Labels, l.Name)
	}
	return pr
}

// previewBytes returns a short, single-line preview of an input chunk for
// error messages. Strips newlines and caps at 40 chars.
func previewBytes(b []byte) string {
	s := strings.ReplaceAll(string(b), "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	if len(s) > 40 {
		s = s[:40] + "..."
	}
	return s
}
