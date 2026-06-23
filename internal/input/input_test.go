package input

import (
	"strings"
	"testing"

	ghc "github.com/laserlemon/gh-cru/internal/gh"
	"github.com/laserlemon/gh-cru/internal/prref"
)

func TestParse_NilReader(t *testing.T) {
	out, err := Parse(nil, "", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected 0 inputs, got %d", len(out))
	}
}

func TestParse_EmptyStdinIsNoOp(t *testing.T) {
	out, err := Parse(strings.NewReader(""), "", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected 0 inputs from empty stdin, got %d", len(out))
	}
}

func TestParse_SingleObject(t *testing.T) {
	out, err := Parse(strings.NewReader(`{"url":"https://github.com/x/y/pull/7"}`), "", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 1 || out[0].Ref.Number != 7 || out[0].Ref.Repo != "y" {
		t.Fatalf("bad out: %+v", out)
	}
}

func TestParse_JSONArray(t *testing.T) {
	in := strings.NewReader(`[
		{"url":"https://github.com/x/y/pull/1"},
		{"url":"https://github.com/x/y/pull/2"}
	]`)
	out, err := Parse(in, "", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 2 || out[0].Ref.Number != 1 || out[1].Ref.Number != 2 {
		t.Fatalf("bad: %+v", out)
	}
}

func TestParse_NDJSON_PreservesOrder(t *testing.T) {
	in := strings.NewReader(`{"url":"https://github.com/x/y/pull/1"}
{"url":"https://github.com/x/y/pull/2"}
{"url":"https://github.com/x/y/pull/3"}`)
	out, err := Parse(in, "", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3, got %d", len(out))
	}
	for i, want := range []int{1, 2, 3} {
		if out[i].Ref.Number != want {
			t.Errorf("out[%d] = #%d, want #%d (order)", i, out[i].Ref.Number, want)
		}
	}
}

func TestParse_NDJSON_WithRepositoryShape(t *testing.T) {
	// gh's nested repository object: {name, nameWithOwner}. nameWithOwner
	// is what resolves owner+repo. This is the shape `gh search prs
	// --json repository` emits and what gh-cru's own --json output carries.
	in := strings.NewReader(`{"number":42,"repository":{"name":"r","nameWithOwner":"o/r"}}
{"number":43,"repository":{"name":"r","nameWithOwner":"o/r"}}`)
	out, err := Parse(in, "", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2, got %d", len(out))
	}
	for i, want := range []int{42, 43} {
		if out[i].Ref.Number != want || out[i].Ref.Owner != "o" || out[i].Ref.Repo != "r" {
			t.Errorf("out[%d] = %+v, want #%d in o/r", i, out[i].Ref, want)
		}
	}
}

func TestParse_RepositoryBadNameWithOwnerErrors(t *testing.T) {
	// A repository object whose nameWithOwner isn't "owner/name" can't
	// resolve an owner; that's an error, not a silent fallback.
	in := strings.NewReader(`{"number":42,"repository":{"name":"r","nameWithOwner":"noslug"}}`)
	_, err := Parse(in, "", "")
	if err == nil {
		t.Fatal("expected error for malformed nameWithOwner")
	}
}

func TestParse_JSONRepoOverridesDefaults(t *testing.T) {
	// JSON-supplied repo info ALWAYS overrides --repo/git context.
	in := strings.NewReader(`{"url":"https://github.com/from-json/repo/pull/5"}`)
	out, err := Parse(in, "default-owner", "default-repo")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out[0].Ref.Owner != "from-json" || out[0].Ref.Repo != "repo" {
		t.Errorf("JSON repo should override defaults, got %+v", out[0].Ref)
	}
}

func TestParse_JSONRepoShorthand(t *testing.T) {
	// The "repo":"owner/name" shorthand resolves identity for a bare number.
	in := strings.NewReader(`{"number":7,"repo":"o/r"}`)
	out, err := Parse(in, "", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out[0].Ref != (prref.Ref{Owner: "o", Repo: "r", Number: 7}) {
		t.Errorf("got %+v", out[0].Ref)
	}
}

func TestParse_JSONFallbackToDefaults(t *testing.T) {
	// number without any repo info → uses --repo/git defaults.
	in := strings.NewReader(`{"number":7}`)
	out, err := Parse(in, "octo", "repo")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out[0].Ref != (prref.Ref{Owner: "octo", Repo: "repo", Number: 7}) {
		t.Errorf("got %+v", out[0].Ref)
	}
}

func TestParse_JSONNoIdentityErrors(t *testing.T) {
	in := strings.NewReader(`{"number":7}`) // no defaults, no repo info
	_, err := Parse(in, "", "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestParse_JSONUrlWins(t *testing.T) {
	// url present + number + repository: url should be authoritative.
	in := strings.NewReader(`{"url":"https://github.com/a/b/pull/1","number":99,"repository":{"name":"y","nameWithOwner":"x/y"}}`)
	out, err := Parse(in, "", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out[0].Ref != (prref.Ref{Owner: "a", Repo: "b", Number: 1}) {
		t.Errorf("got %+v, expected a/b#1 (url priority)", out[0].Ref)
	}
}

func TestParse_PreFetchedScoringFields(t *testing.T) {
	// When JSON carries LOC fields and files, the input should be pre-
	// populated so the caller can skip API fetches.
	in := strings.NewReader(`{
		"url":"https://github.com/o/r/pull/1",
		"title":"fix thing",
		"state":"MERGED",
		"additions":50,
		"deletions":10,
		"changedFiles":3,
		"mergeCommit":{"oid":"abc123"},
		"baseRefName":"main",
		"author":{"login":"alice"},
		"labels":[{"name":"bug"}],
		"files":[
			{"path":"a.go","additions":30,"deletions":5},
			{"path":"b.go","additions":20,"deletions":5}
		]
	}`)
	out, err := Parse(in, "", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	inp := out[0]
	if inp.PR == nil {
		t.Fatal("expected pre-populated PR")
	}
	pr := *inp.PR
	if pr.Additions != 50 || pr.Deletions != 10 {
		t.Errorf("LOC fields: %+v", pr)
	}
	if pr.URL != "https://github.com/o/r/pull/1" {
		t.Errorf("url: %q", pr.URL)
	}
	if pr.Author != "alice" {
		t.Errorf("author: %q", pr.Author)
	}
	if !pr.Merged || pr.State != "merged" || pr.MergeCommitSHA != "abc123" {
		t.Errorf("merge fields: state=%q merged=%v sha=%q", pr.State, pr.Merged, pr.MergeCommitSHA)
	}
	if len(pr.Labels) != 1 || pr.Labels[0] != "bug" {
		t.Errorf("labels: %+v", pr.Labels)
	}
	if len(inp.Files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(inp.Files))
	}
	if inp.Files[0] != (ghc.File{Path: "a.go", Changes: 35}) {
		t.Errorf("files[0] = %+v", inp.Files[0])
	}
}

func TestParse_RefOnlyJSON_NoPrePopulate(t *testing.T) {
	// {url:...} alone shouldn't pre-populate PR (no scoring fields)
	in := strings.NewReader(`{"url":"https://github.com/o/r/pull/1"}`)
	out, err := Parse(in, "", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out[0].PR != nil {
		t.Errorf("ref-only JSON should not pre-populate PR, got %+v", out[0].PR)
	}
}

func TestParse_BadJSONErrors(t *testing.T) {
	in := strings.NewReader(`not json at all`)
	_, err := Parse(in, "", "")
	if err == nil {
		t.Fatal("expected error for non-JSON stdin")
	}
}
