package input

import (
	"errors"
	"strings"
	"testing"

	ghc "github.com/laserlemon/gh-cru/internal/gh"
	"github.com/laserlemon/gh-cru/internal/prref"
)

func TestParse_NoArgsNoStdin(t *testing.T) {
	out, src, err := Parse(nil, nil, false, "", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected 0 inputs, got %d", len(out))
	}
	if src != SourceNone {
		t.Fatalf("expected SourceNone, got %v", src)
	}
}

func TestParse_BareArgs_NumberWithDefaults(t *testing.T) {
	out, _, err := Parse([]string{"123", "456"}, nil, false, "octo", "repo")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2, got %d", len(out))
	}
	want := []prref.Ref{{Owner: "octo", Repo: "repo", Number: 123}, {Owner: "octo", Repo: "repo", Number: 456}}
	for i, w := range want {
		if out[i].Ref != w {
			t.Errorf("ref[%d] = %+v, want %+v", i, out[i].Ref, w)
		}
		if out[i].PR != nil {
			t.Errorf("bare arg should not pre-populate PR")
		}
	}
}

func TestParse_BareArg_URL(t *testing.T) {
	out, _, err := Parse([]string{"https://github.com/o/r/pull/9"}, nil, false, "", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out[0].Ref != (prref.Ref{Owner: "o", Repo: "r", Number: 9}) {
		t.Errorf("got %+v", out[0].Ref)
	}
}

func TestParse_LiteralDashConsumesStdin(t *testing.T) {
	stdin := strings.NewReader(`{"url":"https://github.com/x/y/pull/7"}`)
	out, src, err := Parse([]string{"-"}, stdin, false, "", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if src != SourceLiteral {
		t.Fatalf("expected SourceLiteral, got %v", src)
	}
	if len(out) != 1 || out[0].Ref.Number != 7 || out[0].Ref.Repo != "y" {
		t.Fatalf("bad out: %+v", out)
	}
}

func TestParse_AutoDetectPipedStdin(t *testing.T) {
	stdin := strings.NewReader(`{"url":"https://github.com/x/y/pull/11"}`)
	out, src, err := Parse(nil, stdin, true, "", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if src != SourceAutoPipe {
		t.Fatalf("expected SourceAutoPipe, got %v", src)
	}
	if len(out) != 1 || out[0].Ref.Number != 11 {
		t.Fatalf("bad out: %+v", out)
	}
}

func TestParse_AutoDetectSuppressedWhenArgsPresent(t *testing.T) {
	stdin := strings.NewReader(`{"url":"https://github.com/x/y/pull/11"}`)
	out, src, err := Parse([]string{"5"}, stdin, true, "o", "r")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if src != SourceNone {
		t.Fatalf("expected SourceNone (args present), got %v", src)
	}
	if len(out) != 1 || out[0].Ref.Number != 5 {
		t.Fatalf("bad out: %+v", out)
	}
}

func TestParse_StdinAndArgsMixed(t *testing.T) {
	// "-" before "99" → stdin entries come first, then 99.
	stdin := strings.NewReader(`{"url":"https://github.com/x/y/pull/1"}
{"url":"https://github.com/x/y/pull/2"}`)
	out, _, err := Parse([]string{"-", "99"}, stdin, false, "o", "r")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3, got %d", len(out))
	}
	if out[0].Ref.Number != 1 || out[1].Ref.Number != 2 || out[2].Ref.Number != 99 {
		t.Fatalf("order wrong: %+v", out)
	}
}

func TestParse_EmptyStdinIsNoOp(t *testing.T) {
	stdin := strings.NewReader("")
	out, _, err := Parse(nil, stdin, true, "", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected 0 inputs from empty stdin, got %d", len(out))
	}
}

func TestParse_JSONArray(t *testing.T) {
	stdin := strings.NewReader(`[
		{"url":"https://github.com/x/y/pull/1"},
		{"url":"https://github.com/x/y/pull/2"}
	]`)
	out, _, err := Parse(nil, stdin, true, "", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 2 || out[0].Ref.Number != 1 || out[1].Ref.Number != 2 {
		t.Fatalf("bad: %+v", out)
	}
}

func TestParse_NDJSON_WithRepositoryShape(t *testing.T) {
	// gh pr list --json url,number,repository emits this shape.
	stdin := strings.NewReader(`{"number":42,"repository":{"owner":"o","name":"r"}}
{"number":43,"repository":{"owner":"o","name":"r"}}`)
	out, _, err := Parse(nil, stdin, true, "", "")
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

func TestParse_JSONRepoOverridesDefaults(t *testing.T) {
	// JSON-supplied repo info ALWAYS overrides --repo/git context.
	stdin := strings.NewReader(`{"url":"https://github.com/from-json/repo/pull/5"}`)
	out, _, err := Parse([]string{"-"}, stdin, false, "default-owner", "default-repo")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out[0].Ref.Owner != "from-json" || out[0].Ref.Repo != "repo" {
		t.Errorf("JSON repo should override defaults, got %+v", out[0].Ref)
	}
}

func TestParse_JSONRepoShorthand(t *testing.T) {
	stdin := strings.NewReader(`{"number":7,"repo":"o/r"}`)
	out, _, err := Parse([]string{"-"}, stdin, false, "", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out[0].Ref != (prref.Ref{Owner: "o", Repo: "r", Number: 7}) {
		t.Errorf("got %+v", out[0].Ref)
	}
}

func TestParse_JSONFallbackToDefaults(t *testing.T) {
	// number without any repo info → uses --repo/git defaults.
	stdin := strings.NewReader(`{"number":7}`)
	out, _, err := Parse([]string{"-"}, stdin, false, "octo", "repo")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out[0].Ref != (prref.Ref{Owner: "octo", Repo: "repo", Number: 7}) {
		t.Errorf("got %+v", out[0].Ref)
	}
}

func TestParse_JSONNoIdentityErrors(t *testing.T) {
	stdin := strings.NewReader(`{"number":7}`) // no defaults, no repo info
	_, _, err := Parse([]string{"-"}, stdin, false, "", "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestParse_JSONUrlWins(t *testing.T) {
	// url present + number + repository: url should be authoritative.
	stdin := strings.NewReader(`{"url":"https://github.com/a/b/pull/1","number":99,"repository":{"owner":"x","name":"y"}}`)
	out, _, err := Parse([]string{"-"}, stdin, false, "", "")
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
	stdin := strings.NewReader(`{
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
	out, _, err := Parse([]string{"-"}, stdin, false, "", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	in := out[0]
	if in.PR == nil {
		t.Fatal("expected pre-populated PR")
	}
	pr := *in.PR
	if pr.Additions != 50 || pr.Deletions != 10 {
		t.Errorf("LOC fields: %+v", pr)
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
	if len(in.Files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(in.Files))
	}
	if in.Files[0] != (ghc.File{Path: "a.go", Changes: 35}) {
		t.Errorf("files[0] = %+v", in.Files[0])
	}
}

func TestParse_RefOnlyJSON_NoPrePopulate(t *testing.T) {
	// {url:...} alone shouldn't pre-populate PR (no scoring fields)
	stdin := strings.NewReader(`{"url":"https://github.com/o/r/pull/1"}`)
	out, _, err := Parse([]string{"-"}, stdin, false, "", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out[0].PR != nil {
		t.Errorf("ref-only JSON should not pre-populate PR, got %+v", out[0].PR)
	}
}

func TestParse_BadJSONErrors(t *testing.T) {
	stdin := strings.NewReader(`not json at all`)
	_, _, err := Parse([]string{"-"}, stdin, false, "", "")
	if err == nil {
		t.Fatal("expected error for non-JSON stdin")
	}
}

func TestParse_StdinRequestedButNilReader(t *testing.T) {
	_, _, err := Parse([]string{"-"}, nil, false, "", "")
	if err == nil || !errors.Is(err, err) { // smoke: any non-nil error is fine
		t.Fatal("expected error when stdin requested but nil")
	}
}
