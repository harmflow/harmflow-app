package main

import (
	"runtime/debug"
	"testing"
)

// The commit go build stamps tells builds of the same version apart in Settings → About.
func TestCommitOf(t *testing.T) {
	const revision = "45a57bb0c3f1d2e4a5b6c7d8e9f0a1b2c3d4e5f6"
	cases := []struct {
		name     string
		settings []debug.BuildSetting
		want     string
	}{
		{"clean checkout", []debug.BuildSetting{{Key: "vcs", Value: "git"}, {Key: "vcs.revision", Value: revision},
			{Key: "vcs.modified", Value: "false"}}, "45a57bb"},
		{"uncommitted changes", []debug.BuildSetting{{Key: "vcs.modified", Value: "true"},
			{Key: "vcs.revision", Value: revision}}, "45a57bb-dirty"},
		{"no VCS stamp", []debug.BuildSetting{{Key: "-trimpath", Value: "true"}, {Key: "GOOS", Value: "darwin"}}, ""},
		{"modified without a revision", []debug.BuildSetting{{Key: "vcs.modified", Value: "true"}}, ""},
		{"short revision", []debug.BuildSetting{{Key: "vcs.revision", Value: "45a5"}}, "45a5"},
	}
	for _, c := range cases {
		if got := commitOf(c.settings); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// The commit make sets wins: go build stamps none in a git worktree.
func TestBuildCommitPrefersMake(t *testing.T) {
	defer func(saved string) { Commit = saved }(Commit)
	Commit = "45a57bb-dirty"
	if got := buildCommit(); got != "45a57bb-dirty" {
		t.Errorf("got %q", got)
	}
}
