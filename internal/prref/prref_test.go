package prref

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		in         string
		defOwner   string
		defRepo    string
		wantOwner  string
		wantRepo   string
		wantNumber int
		wantErr    bool
	}{
		{"123", "myorg", "myrepo", "myorg", "myrepo", 123, false},
		{"#123", "myorg", "myrepo", "myorg", "myrepo", 123, false},
		{"123", "", "", "", "", 0, true},
		{"acme/web#9999", "", "", "acme", "web", 9999, false},
		{"owner/name#1", "ignored", "ignored", "owner", "name", 1, false},
		{"https://github.com/acme/web/pull/12345", "", "", "acme", "web", 12345, false},
		{"https://github.com/acme/web/pull/12345/files", "", "", "acme", "web", 12345, false},
		{"http://github.com/cli/cli/pull/9876", "", "", "cli", "cli", 9876, false},
		{"garbage", "", "", "", "", 0, true},
		{"", "x", "y", "", "", 0, true},
	}
	for _, c := range cases {
		got, err := Parse(c.in, c.defOwner, c.defRepo)
		if (err != nil) != c.wantErr {
			t.Errorf("Parse(%q): err=%v, wantErr=%v", c.in, err, c.wantErr)
			continue
		}
		if c.wantErr {
			continue
		}
		if got.Owner != c.wantOwner || got.Repo != c.wantRepo || got.Number != c.wantNumber {
			t.Errorf("Parse(%q) = %v, want %s/%s#%d", c.in, got,
				c.wantOwner, c.wantRepo, c.wantNumber)
		}
	}
}
