package main

import (
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

// TestRefArgs: an issue URL on another host than the client's
// (github.com in tests) is refused before any request, as an issue
// argument and in --by, --blocked-by or --parent: its number would name another
// issue here. A pull request's URL as the issue is exit code 3 at once.
func TestRefArgs(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		code render.Code
	}{
		{"show_other_host", []string{"show", "https://gitlab.com/cli/cli/issues/2"}, render.ExitUsage},
		{"verb_other_host", []string{"done", "3", "https://ghe.example.com/Trum-ok/gh-dojo/issues/3"}, render.ExitUsage},
		{"comment_other_host", []string{"comment", "https://gitlab.com/cli/cli/issues/2", "hi"}, render.ExitUsage},
		{"show_pull", []string{"show", "https://github.com/cli/cli/pull/1"}, render.ExitNotFound},
		{"start_pull", []string{"start", "https://github.com/cli/cli/pull/1#discussion_r1"}, render.ExitNotFound},
		{"block_by_other_host", []string{"block", "3", "--by", "17,https://gitlab.com/a/b/issues/1"}, render.ExitUsage},
		{"new_blocked_by_other_host", []string{"new", "Fix it", "--blocked-by", "https://gitlab.com/a/b/issues/1"}, render.ExitUsage},
		{"new_parent_other_host", []string{"new", "Fix it", "--parent", "https://gitlab.com/a/b/issues/1"}, render.ExitUsage},
	} {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, dojoConfig)
			srv := withApp(t, "testdata/graphql/none.json")
			code, out := runOut(tt.args...)
			if code != tt.code || srv.Requests() != 0 {
				t.Errorf("exit code = %d, requests %d; want %d, 0\n%s", code, srv.Requests(), tt.code, out)
			}
			testutil.Golden(t, "refs/"+tt.name, out)
		})
	}
}
