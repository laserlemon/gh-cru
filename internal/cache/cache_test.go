package cache

import (
	"path/filepath"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Base() != dir {
		t.Fatalf("Base = %q, want %q", c.Base(), dir)
	}

	owner, repo := "github", "github"
	etag := `"abc123def"`
	body := []byte("* @some/team\n/cmd/ @other/team\n")

	if c.HasBody(owner, repo, etag) {
		t.Fatal("HasBody true before write")
	}
	if err := c.SaveBody(owner, repo, etag, body); err != nil {
		t.Fatalf("SaveBody: %v", err)
	}
	if !c.HasBody(owner, repo, etag) {
		t.Fatal("HasBody false after write")
	}
	got, err := c.ReadBody(owner, repo, etag)
	if err != nil {
		t.Fatalf("ReadBody: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("ReadBody = %q, want %q", got, body)
	}
}

func TestSaveEmptyETag(t *testing.T) {
	c, _ := New(t.TempDir())
	if err := c.SaveBody("o", "r", "", []byte("x")); err == nil {
		t.Fatal("expected error saving with empty ETag")
	}
}

func TestEtagFileKey(t *testing.T) {
	cases := map[string]string{
		`"abc123"`:       "abc123",
		`W/"weak-tag"`:   "weak-tag",
		`"a/b\"c"`:       "a_b_c",
		`  "spaced"  `:   "spaced",
		``:               "unknown",
		`"only_safe.1"`:  "only_safe.1",
	}
	for in, want := range cases {
		if got := etagFileKey(in); got != want {
			t.Errorf("etagFileKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBodyPathDeterministic(t *testing.T) {
	c, _ := New(t.TempDir())
	a := c.bodyPath("o", "r", `"abc"`)
	b := c.bodyPath("o", "r", `"abc"`)
	if a != b {
		t.Fatalf("non-deterministic path: %q vs %q", a, b)
	}
	if filepath.Base(a) == "" {
		t.Fatalf("empty filename: %q", a)
	}
}
