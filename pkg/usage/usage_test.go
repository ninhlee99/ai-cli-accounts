package usage

import (
	"testing"
	"time"

	"ai-cli-accounts/pkg/types"
)

func TestUsage_Commas(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{123, "123"},
		{1234, "1,234"},
		{1234567, "1,234,567"},
		{-1234, "-1,234"},
	}
	for _, tc := range cases {
		got := Commas(tc.in)
		if got != tc.want {
			t.Errorf("Commas(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestUsage_Labels(t *testing.T) {
	if got := ProjectLabel("/Users/foo/project-bar"); got != "project-bar" {
		t.Errorf("ProjectLabel mismatch: %q", got)
	}
	if got := ProjectLabel(""); got != "-" {
		t.Errorf("ProjectLabel empty mismatch: %q", got)
	}

	if got := SessionLabel("1234567890abcdef"); got != "12345678" {
		t.Errorf("SessionLabel mismatch: %q", got)
	}
	if got := SessionLabel(""); got != "-" {
		t.Errorf("SessionLabel empty mismatch: %q", got)
	}
}

func TestUsage_Aggregations(t *testing.T) {
	entries := []types.UsageEntry{
		{
			Time:    time.Now(),
			Account: "acc1",
			Model:   "claude-3-5",
			Project: "/path/to/myproj",
			Session: "sess-123456",
			Input:   100,
			Output:  200,
		},
		{
			Time:    time.Now(),
			Account: "acc1",
			Model:   "claude-3-5",
			Project: "/path/to/myproj",
			Session: "sess-123456",
			Input:   50,
			Output:  100,
		},
	}

	byDay := map[string]*usageAgg{}
	var totalIn, totalOut, totalReqs int
	for _, e := range entries {
		bump(byDay, e.Time.Local().Format("2006-01-02"), e)
		totalIn += e.Input
		totalOut += e.Output
		totalReqs++
	}

	if totalIn != 150 || totalOut != 300 || totalReqs != 2 {
		t.Errorf("agg mismatch: in=%d, out=%d, reqs=%d", totalIn, totalOut, totalReqs)
	}
}
