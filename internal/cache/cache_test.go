package cache

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}

	owner, repo, ref := "github", "github", "main"
	etag := `"abc123def"`
	body := []byte("* @some/team\n/cmd/ @other/team\n")

	if c.HasBody(owner, repo, ref, etag) {
		t.Fatal("HasBody true before write")
	}
	if err := c.SaveBody(owner, repo, ref, etag, body); err != nil {
		t.Fatalf("SaveBody: %v", err)
	}
	if !c.HasBody(owner, repo, ref, etag) {
		t.Fatal("HasBody false after write")
	}
	got, err := c.ReadBody(owner, repo, ref, etag)
	if err != nil {
		t.Fatalf("ReadBody: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("ReadBody = %q, want %q", got, body)
	}
}

func TestSaveEmptyETag(t *testing.T) {
	c, _ := New(t.TempDir())
	if err := c.SaveBody("o", "r", "main", "", []byte("x")); err == nil {
		t.Fatal("expected error saving with empty ETag")
	}
}

func TestEtagFileKey(t *testing.T) {
	cases := map[string]string{
		`"abc123"`:      "abc123",
		`W/"weak-tag"`:  "weak-tag",
		`"a/b\"c"`:      "a_b_c",
		`  "spaced"  `:  "spaced",
		``:              "unknown",
		`"only_safe.1"`: "only_safe.1",
	}
	for in, want := range cases {
		if got := etagFileKey(in); got != want {
			t.Errorf("etagFileKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRefKey(t *testing.T) {
	cases := map[string]string{
		"":                "default",
		"main":            "main",
		"release/v2":      "release_v2",
		"feature/foo-bar": "feature_foo-bar",
		"68dfd87f47e3757e92953c3a0eaa42cf4c7d0d4f": "68dfd87f47e3757e92953c3a0eaa42cf4c7d0d4f",
	}
	for in, want := range cases {
		if got := refKey(in); got != want {
			t.Errorf("refKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBodyPathDeterministic(t *testing.T) {
	c, _ := New(t.TempDir())
	a := c.bodyPath("o", "r", "main", `"abc"`)
	b := c.bodyPath("o", "r", "main", `"abc"`)
	if a != b {
		t.Fatalf("non-deterministic path: %q vs %q", a, b)
	}
	if filepath.Base(a) == "" {
		t.Fatalf("empty filename: %q", a)
	}
}

func TestBodyPathRefPartitioning(t *testing.T) {
	c, _ := New(t.TempDir())
	a := c.bodyPath("o", "r", "main", `"abc"`)
	b := c.bodyPath("o", "r", "release/v2", `"abc"`)
	if a == b {
		t.Fatalf("same path across refs: %q", a)
	}
	if !strings.Contains(a, "main") || !strings.Contains(b, "release_v2") {
		t.Fatalf("ref segment missing: a=%q b=%q", a, b)
	}
}

func TestRefPartitionedRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c, _ := New(dir)

	owner, repo := "github", "github"
	etag := `"shared-etag"`

	bodyMain := []byte("* @main/team\n")
	bodyV2 := []byte("* @v2/team\n")

	if err := c.SaveBody(owner, repo, "main", etag, bodyMain); err != nil {
		t.Fatalf("SaveBody main: %v", err)
	}
	if err := c.SaveBody(owner, repo, "release/v2", etag, bodyV2); err != nil {
		t.Fatalf("SaveBody release/v2: %v", err)
	}
	gotMain, _ := c.ReadBody(owner, repo, "main", etag)
	gotV2, _ := c.ReadBody(owner, repo, "release/v2", etag)
	if string(gotMain) != string(bodyMain) {
		t.Fatalf("main body got %q want %q", gotMain, bodyMain)
	}
	if string(gotV2) != string(bodyV2) {
		t.Fatalf("v2 body got %q want %q", gotV2, bodyV2)
	}
}
