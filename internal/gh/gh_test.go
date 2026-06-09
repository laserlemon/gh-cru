package gh

import "testing"

// TestPRLOC verifies the LOC accessor: additions + deletions.
func TestPRLOC(t *testing.T) {
	cases := []struct {
		add, del, want int
	}{
		{0, 0, 0},
		{10, 0, 10},
		{0, 5, 5},
		{50, 50, 100},
	}
	for _, c := range cases {
		p := PR{Additions: c.add, Deletions: c.del}
		if got := p.LOC(); got != c.want {
			t.Errorf("PR{Add:%d Del:%d}.LOC() = %d, want %d", c.add, c.del, got, c.want)
		}
	}
}

// TestPRCodeownersRef verifies the ref-selection logic that decides
// which git ref CODEOWNERS should be fetched at for a given PR. This is
// the historical-vs-live distinction baked into v0.1.18.
func TestPRCodeownersRef(t *testing.T) {
	cases := []struct {
		name string
		pr   PR
		want string
	}{
		{
			name: "merged with SHA → SHA",
			pr:   PR{Merged: true, MergeCommitSHA: "abc123", BaseRef: "main"},
			want: "abc123",
		},
		{
			name: "merged without SHA → base ref (unlikely but defensive)",
			pr:   PR{Merged: true, MergeCommitSHA: "", BaseRef: "main"},
			want: "main",
		},
		{
			name: "open PR → base branch name",
			pr:   PR{Merged: false, BaseRef: "main"},
			want: "main",
		},
		{
			name: "open PR with non-default base → that base",
			pr:   PR{Merged: false, BaseRef: "feature/discussion"},
			want: "feature/discussion",
		},
		{
			name: "no ref at all → empty (caller treats as default branch)",
			pr:   PR{Merged: false, BaseRef: ""},
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.pr.CodeownersRef(); got != c.want {
				t.Errorf("CodeownersRef() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestContentsEndpoint verifies URL composition for the contents API,
// including the optional ?ref= suffix.
func TestContentsEndpoint(t *testing.T) {
	cases := []struct {
		owner, repo, path, ref string
		want                   string
	}{
		{
			owner: "acme", repo: "web", path: "CODEOWNERS", ref: "",
			want: "repos/acme/web/contents/CODEOWNERS",
		},
		{
			owner: "acme", repo: "web", path: "CODEOWNERS", ref: "main",
			want: "repos/acme/web/contents/CODEOWNERS?ref=main",
		},
		{
			owner: "acme", repo: "web", path: ".github/CODEOWNERS", ref: "abc123",
			want: "repos/acme/web/contents/.github/CODEOWNERS?ref=abc123",
		},
	}
	for _, c := range cases {
		got := contentsEndpoint(c.owner, c.repo, c.path, c.ref)
		if got != c.want {
			t.Errorf("contentsEndpoint(%q, %q, %q, %q) = %q, want %q",
				c.owner, c.repo, c.path, c.ref, got, c.want)
		}
	}
}

// TestIsLikelyBranchName verifies the heuristic that distinguishes a
// branch name from a 40-char commit SHA.
func TestIsLikelyBranchName(t *testing.T) {
	cases := []struct {
		ref  string
		want bool
	}{
		{"main", true},
		{"feature/discussion", true},
		{"release-2026", true},
		{"", true}, // empty technically isn't a SHA → treated as branch
		// 40-char hex strings → SHA → false
		{"abcdef0123456789abcdef0123456789abcdef01", false},
		{"68dfd87fabcd1234567890abcdef0123456789ab", false},
		// 40 chars but not all hex → still a branch (defensive)
		{"abcdef0123456789abcdef0123456789abcdefXY", true},
		// short SHAs (7-12 chars) → treated as branch
		{"abc1234", true},
		{"68dfd87f", true},
	}
	for _, c := range cases {
		if got := isLikelyBranchName(c.ref); got != c.want {
			t.Errorf("isLikelyBranchName(%q) = %v, want %v", c.ref, got, c.want)
		}
	}
}

// TestBase64Decode verifies the helper that decodes the contents-API
// base64 body (which uses standard encoding with potential newlines).
func TestBase64Decode(t *testing.T) {
	// "hello world" base64-encoded.
	got, err := base64Decode("aGVsbG8gd29ybGQ=")
	if err != nil {
		t.Fatalf("base64Decode error: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("base64Decode = %q, want %q", got, "hello world")
	}

	// Contents API sometimes wraps lines at 60 chars; verify newlines are
	// tolerated (Go stdlib's base64 doesn't tolerate them by default, so
	// the helper must strip them).
	wrapped := "aGVsbG8g\nd29ybGQ=\n"
	got2, err := base64Decode(wrapped)
	if err != nil {
		t.Fatalf("base64Decode wrapped error: %v", err)
	}
	if string(got2) != "hello world" {
		t.Errorf("base64Decode wrapped = %q, want %q", got2, "hello world")
	}
}
