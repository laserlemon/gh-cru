// Package input parses gh-cru's PR inputs from CLI arguments and stdin.
//
// Three input shapes are supported, in priority order:
//
//  1. JSON on stdin (literal "-" arg, or auto-detected when args are empty
//     and stdin is piped). Accepts a JSON array, NDJSON (one object per
//     line), or a single JSON object. Each object may carry just a ref
//     (e.g. {"url": "..."}) or pre-fetched scoring fields that bypass the
//     PR API call entirely.
//
//  2. Bare CLI args (numbers, owner/repo#N shorthand, full URLs). Each is
//     parsed by prref.Parse and emitted with PR=nil so callers fetch.
//
//  3. Mixed: pass "-" alongside other args. JSON entries and refs are
//     concatenated in argument order.
//
// The returned []Input preserves caller ordering so the formatter prints
// PRs in the order the user (or upstream pipeline) intended.
package input

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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

// Source identifies how stdin was provided so callers can render
// appropriate error messages.
type Source int

const (
	SourceNone     Source = iota // no stdin consulted
	SourceLiteral                // "-" appeared in args
	SourceAutoPipe               // args empty, stdin was a pipe
)

// Parse turns CLI args + an optional stdin reader into a slice of Inputs.
//
//	args        - the positional arguments to the cru command (no flags).
//	stdin       - reader for stdin; may be nil to skip auto-detection.
//	stdinIsPipe - true when stdin is a pipe (non-TTY). Used for auto-detect.
//	defOwner,
//	defRepo     - defaults for bare PR numbers in args (from --repo or git).
//
// Returns the parsed inputs in input order and the source of any stdin
// consumption. Errors are returned for the entire batch; partial success
// is the caller's responsibility (we don't want to silently drop bad
// rows from a piped list).
func Parse(args []string, stdin io.Reader, stdinIsPipe bool, defOwner, defRepo string) ([]Input, Source, error) {
	// Decide whether stdin contributes. Literal "-" wins; auto-detect
	// only applies when args is otherwise empty AND stdin is a pipe.
	wantStdin := false
	source := SourceNone
	filtered := make([]string, 0, len(args))
	for _, a := range args {
		if a == "-" {
			wantStdin = true
			source = SourceLiteral
			continue
		}
		filtered = append(filtered, a)
	}
	if !wantStdin && len(filtered) == 0 && stdin != nil && stdinIsPipe {
		wantStdin = true
		source = SourceAutoPipe
	}

	out := make([]Input, 0, len(args))

	// Stdin first when present, so piped input preserves its order
	// relative to subsequent bare args. (`gh cru - 99` means "stdin
	// rows, then PR 99".)
	if wantStdin {
		if stdin == nil {
			return nil, source, errors.New("stdin requested but no reader available")
		}
		ins, err := parseStdin(stdin, defOwner, defRepo)
		if err != nil {
			return nil, source, err
		}
		out = append(out, ins...)
	}

	for _, a := range filtered {
		ref, err := prref.Parse(a, defOwner, defRepo)
		if err != nil {
			return nil, source, err
		}
		out = append(out, Input{Ref: ref})
	}
	return out, source, nil
}

// parseStdin reads the full stdin payload and dispatches based on shape:
// JSON array, NDJSON (one object per line), or a single multi-line JSON
// object. Bare numeric or URL refs on stdin are NOT supported — that's
// a perpetual source of `xargs gh cru` surprise, and users who want it
// can just `xargs` directly. Stdin is for JSON only.
func parseStdin(r io.Reader, defOwner, defRepo string) ([]Input, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read stdin: %w", err)
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
		// Try whole-blob first — succeeds for pretty-printed `gh ... | jq .`
		// output. Falls back to NDJSON when there's more than one object.
		if obj, ok := tryDecodeSingleObject(trimmed); ok {
			in, err := parseJSONObject(obj, defOwner, defRepo)
			if err != nil {
				return nil, fmt.Errorf("stdin: %w", err)
			}
			return []Input{in}, nil
		}
		return parseNDJSON(trimmed, defOwner, defRepo)
	default:
		return nil, fmt.Errorf("stdin: expected JSON object or array, got %q...", previewBytes(trimmed))
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
	// Whatever's left after the first object decoding — only single-object
	// payloads have nothing significant trailing.
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
			return nil, fmt.Errorf("stdin[%d]: %w", i, err)
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
			return nil, fmt.Errorf("stdin line %d: %w", lineNum, err)
		}
		out = append(out, in)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read stdin: %w", err)
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
	// gh pr list --json repository
	Repository *struct {
		Owner string `json:"owner"`
		Name  string `json:"name"`
	} `json:"repository"`

	// Scoring fields (pre-fetched optional)
	Title       string  `json:"title"`
	State       string  `json:"state"`
	Merged      bool    `json:"merged"`
	MergedAt    string  `json:"mergedAt"` // gh pr list shape: non-empty means merged
	Additions   int     `json:"additions"`
	Deletions   int     `json:"deletions"`
	ChangedFiles int    `json:"changedFiles"`
	HeadSHA     string  `json:"headRefOid"`
	BaseRef     string  `json:"baseRefName"`
	MergeCommit *struct {
		Oid string `json:"oid"`
	} `json:"mergeCommit"`
	Author *struct {
		Login string `json:"login"`
	} `json:"author"`
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
//  1. url   — full https://github.com/owner/repo/pull/N URL (gh-canonical)
//  2. repository.owner + .name + number — gh pr list shape
//  3. repo + number — our shorthand ("owner/name")
//  4. number alone — uses defOwner/defRepo from --repo or git context
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
		return "", "", 0, errors.New("entry has no url and no number")
	}
	// 2. repository.owner + .name (gh pr list shape).
	if j.Repository != nil && j.Repository.Owner != "" && j.Repository.Name != "" {
		return j.Repository.Owner, j.Repository.Name, j.Number, nil
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
	return j.Additions != 0 || j.Deletions != 0 || j.State != "" || j.MergeCommit != nil || j.MergedAt != ""
}

// jsonToPR projects the JSON shape into the internal ghc.PR struct used
// by the scorer. State is lower-cased to match gh CLI's "OPEN"/"MERGED"
// versus the REST API's "open"/"merged". Merged status is derived from
// (in priority order): explicit `merged` bool, non-empty `mergedAt`,
// state == "MERGED", or non-nil mergeCommit.
func jsonToPR(j jsonPR, number int) ghc.PR {
	merged := j.Merged ||
		j.MergedAt != "" ||
		strings.EqualFold(j.State, "merged") ||
		j.MergeCommit != nil
	pr := ghc.PR{
		Number:    number,
		Title:     j.Title,
		State:     strings.ToLower(j.State),
		Additions: j.Additions,
		Deletions: j.Deletions,
		Files:     j.ChangedFiles,
		HeadSHA:   j.HeadSHA,
		BaseRef:   j.BaseRef,
		Merged:    merged,
	}
	if j.Author != nil {
		pr.Author = j.Author.Login
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

// StdinIsPipe is a small helper for callers that already hold os.Stdin —
// returns true when it's not a TTY (i.e. piped or redirected).
func StdinIsPipe(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) == 0
}
