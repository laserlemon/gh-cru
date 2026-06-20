package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestStripJSONFlags(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		out  []string
	}{
		{
			name: "no json flags",
			in:   []string{"--state", "merged", "--limit", "10"},
			out:  []string{"--state", "merged", "--limit", "10"},
		},
		{
			name: "drop --json with separate value",
			in:   []string{"--json", "url,number", "--state", "open"},
			out:  []string{"--state", "open"},
		},
		{
			name: "drop --json=value",
			in:   []string{"--json=url,number", "--state", "open"},
			out:  []string{"--state", "open"},
		},
		{
			name: "drop --jq separate",
			in:   []string{"--jq", ".[].url", "--state", "open"},
			out:  []string{"--state", "open"},
		},
		{
			name: "drop --jq=value",
			in:   []string{"--jq=.[].url", "--state", "open"},
			out:  []string{"--state", "open"},
		},
		{
			name: "drop -q short form separate",
			in:   []string{"-q", ".[].url", "--state", "open"},
			out:  []string{"--state", "open"},
		},
		{
			name: "drop -q=value",
			in:   []string{"-q=.[].url", "--state", "open"},
			out:  []string{"--state", "open"},
		},
		{
			name: "--json with next-token flag (no value to consume)",
			in:   []string{"--json", "--state", "open"},
			out:  []string{"--state", "open"}, // --json eaten alone, --state preserved
		},
		{
			name: "multiple json flags",
			in:   []string{"--json=url", "--jq=.[].x", "-q", "v", "--state", "open"},
			out:  []string{"--state", "open"},
		},
		{
			name: "empty",
			in:   []string{},
			out:  []string{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := stripJSONFlags(tc.in)
			if !reflect.DeepEqual(got, tc.out) {
				t.Errorf("\n  in: %v\n got: %v\nwant: %v", tc.in, got, tc.out)
			}
		})
	}
}

func TestListJSONFieldsCovers(t *testing.T) {
	// listJSONFields needs everything score.Compute reads. Sanity-check
	// that the must-haves are present so a future refactor doesn't
	// silently drop a field and force an extra API call.
	required := []string{
		"url", "number", "additions", "deletions", "changedFiles",
		"baseRefName", "mergeCommit", "labels", "author", "state", "files",
	}
	set := make(map[string]bool, len(listJSONFields))
	for _, f := range listJSONFields {
		set[f] = true
	}
	for _, f := range required {
		if !set[f] {
			t.Errorf("listJSONFields missing required field %q", f)
		}
	}
}

// reset all package-level flag vars between extractRootFlags tests so
// they don't bleed across cases. Each test should set up its own state.
func resetRootFlags() {
	repoFlag = ""
	jsonFlag = false
	noOwnersFlag = false
	noPersonalFlag = false
	highRiskLabelsFlag = []string{"risk:high"}
	mediumRiskLabelsFlag = []string{"risk:medium"}
}

func TestExtractRootFlags(t *testing.T) {
	tests := []struct {
		name             string
		in               []string
		wantOut          []string
		wantJSON         bool
		wantNoOwn        bool
		wantNoPers       bool
		wantRepo         string
		wantHighLabels   []string
		wantMediumLabels []string
	}{
		{
			name:             "no flags keeps risk-label defaults",
			in:               []string{"--state", "open"},
			wantOut:          []string{"--state", "open"},
			wantHighLabels:   []string{"risk:high"},
			wantMediumLabels: []string{"risk:medium"},
		},
		{
			name:             "--json strip",
			in:               []string{"--json", "--state", "open"},
			wantOut:          []string{"--state", "open"},
			wantJSON:         true,
			wantHighLabels:   []string{"risk:high"},
			wantMediumLabels: []string{"risk:medium"},
		},
		{
			name:             "--skip-ownership and --skip-personal strip",
			in:               []string{"--skip-ownership", "--state", "open", "--skip-personal"},
			wantOut:          []string{"--state", "open"},
			wantNoOwn:        true,
			wantNoPers:       true,
			wantHighLabels:   []string{"risk:high"},
			wantMediumLabels: []string{"risk:medium"},
		},
		{
			name:             "--high-risk-label single value replaces default",
			in:               []string{"--high-risk-label", "danger", "--state", "open"},
			wantOut:          []string{"--state", "open"},
			wantHighLabels:   []string{"danger"},
			wantMediumLabels: []string{"risk:medium"},
		},
		{
			name:             "--medium-risk-label single value replaces default",
			in:               []string{"--medium-risk-label", "watch", "--state", "open"},
			wantOut:          []string{"--state", "open"},
			wantHighLabels:   []string{"risk:high"},
			wantMediumLabels: []string{"watch"},
		},
		{
			name:             "--high-risk-label=value single replaces default",
			in:               []string{"--high-risk-label=danger", "--state", "open"},
			wantOut:          []string{"--state", "open"},
			wantHighLabels:   []string{"danger"},
			wantMediumLabels: []string{"risk:medium"},
		},
		{
			name:             "--high-risk-label repeated appends",
			in:               []string{"--high-risk-label", "danger", "--high-risk-label", "p0", "--state", "open"},
			wantOut:          []string{"--state", "open"},
			wantHighLabels:   []string{"danger", "p0"},
			wantMediumLabels: []string{"risk:medium"},
		},
		{
			name:             "--high-risk-label comma-separated splits",
			in:               []string{"--high-risk-label=danger,p0,critical", "--state", "open"},
			wantOut:          []string{"--state", "open"},
			wantHighLabels:   []string{"danger", "p0", "critical"},
			wantMediumLabels: []string{"risk:medium"},
		},
		{
			name:             "--medium-risk-label comma-separated splits",
			in:               []string{"--medium-risk-label=watch,careful", "--state", "open"},
			wantOut:          []string{"--state", "open"},
			wantHighLabels:   []string{"risk:high"},
			wantMediumLabels: []string{"watch", "careful"},
		},
		{
			name:             "both high and medium override default",
			in:               []string{"--high-risk-label=danger", "--medium-risk-label=warn,watch"},
			wantOut:          []string{},
			wantHighLabels:   []string{"danger"},
			wantMediumLabels: []string{"warn", "watch"},
		},
		{
			name:             "--high-risk-label mixed comma + repeat",
			in:               []string{"--high-risk-label=danger,p0", "--high-risk-label", "critical"},
			wantOut:          []string{},
			wantHighLabels:   []string{"danger", "p0", "critical"},
			wantMediumLabels: []string{"risk:medium"},
		},
		{
			name:             "--repo separate value preserves flag",
			in:               []string{"--repo", "o/r", "--state", "open"},
			wantOut:          []string{"--repo", "o/r", "--state", "open"},
			wantRepo:         "o/r",
			wantHighLabels:   []string{"risk:high"},
			wantMediumLabels: []string{"risk:medium"},
		},
		{
			name:             "--repo=value preserves flag",
			in:               []string{"--repo=o/r", "--state", "open"},
			wantOut:          []string{"--repo=o/r", "--state", "open"},
			wantRepo:         "o/r",
			wantHighLabels:   []string{"risk:high"},
			wantMediumLabels: []string{"risk:medium"},
		},
		{
			name:             "-R short form preserves flag",
			in:               []string{"-R", "o/r", "--state", "open"},
			wantOut:          []string{"-R", "o/r", "--state", "open"},
			wantRepo:         "o/r",
			wantHighLabels:   []string{"risk:high"},
			wantMediumLabels: []string{"risk:medium"},
		},
		{
			name:             "everything together",
			in:               []string{"--json", "--repo=o/r", "--state", "open", "--high-risk-label=critical,danger", "--medium-risk-label=watch", "--limit", "10"},
			wantOut:          []string{"--repo=o/r", "--state", "open", "--limit", "10"},
			wantJSON:         true,
			wantRepo:         "o/r",
			wantHighLabels:   []string{"critical", "danger"},
			wantMediumLabels: []string{"watch"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resetRootFlags()
			got := extractRootFlags(tc.in)
			if !reflect.DeepEqual(got, tc.wantOut) {
				t.Errorf("\n  in: %v\n got: %v\nwant: %v", tc.in, got, tc.wantOut)
			}
			if jsonFlag != tc.wantJSON {
				t.Errorf("jsonFlag = %v, want %v", jsonFlag, tc.wantJSON)
			}
			if noOwnersFlag != tc.wantNoOwn {
				t.Errorf("noOwnersFlag = %v, want %v", noOwnersFlag, tc.wantNoOwn)
			}
			if noPersonalFlag != tc.wantNoPers {
				t.Errorf("noPersonalFlag = %v, want %v", noPersonalFlag, tc.wantNoPers)
			}
			if repoFlag != tc.wantRepo {
				t.Errorf("repoFlag = %q, want %q", repoFlag, tc.wantRepo)
			}
			if !reflect.DeepEqual(highRiskLabelsFlag, tc.wantHighLabels) {
				t.Errorf("highRiskLabelsFlag = %v, want %v", highRiskLabelsFlag, tc.wantHighLabels)
			}
			if !reflect.DeepEqual(mediumRiskLabelsFlag, tc.wantMediumLabels) {
				t.Errorf("mediumRiskLabelsFlag = %v, want %v", mediumRiskLabelsFlag, tc.wantMediumLabels)
			}
		})
	}
}

func TestStripViewFlags(t *testing.T) {
	tests := []struct {
		name    string
		in      []string
		want    []string
		wantErr string // substring; "" means no error expected
	}{
		{
			name: "bare positional passes through",
			in:   []string{"1234"},
			want: []string{"1234"},
		},
		{
			name: "no args (current-branch case) stays empty",
			in:   []string{},
			want: []string{},
		},
		{
			name: "branch name passes through",
			in:   []string{"my-feature-branch"},
			want: []string{"my-feature-branch"},
		},
		{
			name: "--repo and value preserved",
			in:   []string{"--repo", "o/r", "1234"},
			want: []string{"--repo", "o/r", "1234"},
		},
		{
			name: "-R and value preserved",
			in:   []string{"-R", "o/r", "1234"},
			want: []string{"-R", "o/r", "1234"},
		},
		{
			name: "drop --json with value",
			in:   []string{"--json", "number,url", "1234"},
			want: []string{"1234"},
		},
		{
			name: "drop --json=value",
			in:   []string{"--json=number,url", "1234"},
			want: []string{"1234"},
		},
		{
			name: "drop --jq separate",
			in:   []string{"--jq", ".number", "1234"},
			want: []string{"1234"},
		},
		{
			name: "drop -q=value",
			in:   []string{"-q=.number", "1234"},
			want: []string{"1234"},
		},
		{
			name: "drop --template separate",
			in:   []string{"--template", "{{.number}}", "1234"},
			want: []string{"1234"},
		},
		{
			name: "drop -t=value",
			in:   []string{"-t={{.number}}", "1234"},
			want: []string{"1234"},
		},
		{
			name:    "--web rejected",
			in:      []string{"--web", "1234"},
			wantErr: "--web is not supported",
		},
		{
			name:    "-w rejected",
			in:      []string{"-w"},
			wantErr: "--web is not supported",
		},
		{
			name:    "--comments rejected",
			in:      []string{"--comments", "1234"},
			wantErr: "--comments is not supported",
		},
		{
			name:    "-c rejected",
			in:      []string{"-c", "1234"},
			wantErr: "--comments is not supported",
		},
		{
			name: "everything together: strip json, preserve repo + ref",
			in:   []string{"--json", "number", "--repo", "o/r", "my-branch"},
			want: []string{"--repo", "o/r", "my-branch"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := stripViewFlags(tc.in)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %q, want substring %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("\n  in: %v\n got: %v\nwant: %v", tc.in, got, tc.want)
			}
		})
	}
}
