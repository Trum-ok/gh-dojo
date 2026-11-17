package main

import (
	"context"
	"strings"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/graph/graphtest"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

// Organization boards. The org_*.json fixtures are hand-crafted from the
// recorded gh-dojo ones (see their comments): the board is organization
// acme's project 3, the repository acme/shop. The board fixtures with
// errors are internal/board's.
const (
	orgBoardFixture = "testdata/graphql/org_acme_board.json"
	orgCard3Fixture = "testdata/graphql/org_card_3.json"
)

// orgConfig is organization acme's board with its repository acme/shop.
const orgConfig = `
version = 1
preset = "kanban"

[board]
owner = "acme"
project = 3
repos = ["acme/shop"]

[session]
env = ["DOJO_SESSION"]
link = "claude://s/{id}"
`

// withOrgApp sets up acme's board with fixtures, the config and a session,
// and with warm set reads the schema into the cache first. It returns the
// server and its request count so far.
func withOrgApp(t *testing.T, config string, warm bool, fixtures ...string) (*graphtest.Server, int) {
	t.Helper()
	withConfig(t, config)
	withSession(t, "abc")
	srv := withApp(t, fixtures...)
	if warm {
		if _, err := newApp().Schema(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	return srv, srv.Requests()
}

// TestOrgCommands runs each command on an organization's board: the same
// requests as on a user's board, the same output but for the names.
func TestOrgCommands(t *testing.T) {
	tests := []struct {
		name     string
		fixtures []string
		warm     bool
		args     []string
		requests int
	}{
		// Cold cache: the schema, then the card or the items.
		{"show", []string{orgBoardFixture, orgCard3Fixture}, false, []string{"show", "3"}, 2},
		{"list", []string{orgBoardFixture, "testdata/graphql/org_items.json"}, false, []string{"list"}, 2},
		{"start_dry_run", []string{orgBoardFixture, "testdata/graphql/org_card_3_start.json"}, true, []string{"start", "3", "--dry-run"}, 1},
		{"start", []string{orgBoardFixture, "testdata/graphql/org_start_ok.json"}, true, []string{"start", "3"}, 2},
		{"comment", []string{orgBoardFixture, orgCard3Fixture, commentAddFixture}, true,
			[]string{"comment", "3", "Reproduced on main."}, 2},
		{"new", []string{orgBoardFixture, "testdata/graphql/org_new_success.json"}, true, newFixArgs, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, before := withOrgApp(t, orgConfig, tt.warm, tt.fixtures...)
			everyItem(t)
			code, out := runOut(tt.args...)
			if code != render.ExitOK {
				t.Errorf("exit code = %d, want 0\n%s", code, out)
			}
			if n := srv.Requests() - before; n != tt.requests {
				t.Errorf("requests = %d, want %d", n, tt.requests)
			}
			if strings.Contains(string(out), "Trum-ok/gh-dojo") {
				t.Errorf("output names the user's repository:\n%s", out)
			}
			testutil.Golden(t, "org/"+tt.name, out)
		})
	}
}

// TestCrossOwner: the board and the repository need not have the same
// owner; the card finds the board's item by the project's ID.
func TestCrossOwner(t *testing.T) {
	userRepoOnOrgBoard := strings.Replace(orgConfig, `"acme/shop"`, `"Trum-ok/gh-dojo"`, 1)
	orgRepoOnUserBoard := strings.Replace(verbDojoConfig, `"Trum-ok/gh-dojo"`, `"acme/shop"`, 1)
	tests := []struct {
		name     string
		config   string
		fixtures []string
		args     []string
		requests int
	}{
		{"user_repo_on_org_board_show", userRepoOnOrgBoard,
			[]string{orgBoardFixture, "testdata/graphql/org_board_user_repo_card_3.json"}, []string{"show", "3", "--brief"}, 1},
		{"user_repo_on_org_board_start", userRepoOnOrgBoard,
			[]string{orgBoardFixture, "testdata/graphql/org_board_user_repo_start_ok.json"}, []string{"start", "3"}, 2},
		{"org_repo_on_user_board_show", orgRepoOnUserBoard,
			[]string{boardFixture, "testdata/graphql/user_board_org_repo_card_3.json"}, []string{"show", "3", "--brief"}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, before := withOrgApp(t, tt.config, true, tt.fixtures...)
			code, out := runOut(tt.args...)
			if code != render.ExitOK {
				t.Errorf("exit code = %d, want 0\n%s", code, out)
			}
			if n := srv.Requests() - before; n != tt.requests {
				t.Errorf("requests = %d, want %d", n, tt.requests)
			}
			// stderr may name blockers not on the board (start warns).
			if stdout, _, _ := strings.Cut(string(out), "--- stderr"); strings.Contains(stdout, "not on the board") {
				t.Errorf("the board's item was not found:\n%s", out)
			}
			testutil.Golden(t, "org/"+tt.name, out)
		})
	}
}

// TestOrgAccessErrors: an organization that keeps the token out is exit
// code 4 with what to do (SAML SSO, OAuth app restrictions, a fine-grained
// token without access); a project GitHub won't show is exit code 3, which
// says the token may have no access to it.
func TestOrgAccessErrors(t *testing.T) {
	notFoundConfig := strings.NewReplacer(`owner = "acme"`, `owner = "github"`, "project = 3", "project = 1").Replace(orgConfig)
	tests := []struct {
		name     string
		config   string
		fixtures []string
		warm     bool
		args     []string
		code     render.Code
	}{
		{"saml_list", orgConfig, []string{boardFixtures + "org_board_saml.json"}, false, []string{"list"}, render.ExitNetwork},
		{"saml_start", orgConfig, []string{boardFixtures + "org_board_saml.json"}, false, []string{"start", "3"}, render.ExitNetwork},
		{"saml_show_repo", orgConfig, []string{orgBoardFixture, "testdata/graphql/org_card_3_saml.json"}, true,
			[]string{"show", "3"}, render.ExitNetwork},
		{"oauth_restricted_show", orgConfig, []string{boardFixtures + "org_board_oauth_restricted.json"}, false,
			[]string{"show", "3"}, render.ExitNetwork},
		{"forbidden_new", orgConfig, []string{boardFixtures + "org_board_forbidden.json"}, false, newFixArgs, render.ExitNetwork},
		{"not_found_list", notFoundConfig, []string{boardFixtures + "org_board_not_found.json"}, false,
			[]string{"list"}, render.ExitNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, before := withOrgApp(t, tt.config, tt.warm, tt.fixtures...)
			code, out := runOut(tt.args...)
			if code != tt.code {
				t.Errorf("exit code = %d, want %d\n%s", code, tt.code, out)
			}
			if n := srv.Requests() - before; n != 1 {
				t.Errorf("requests = %d, want 1", n)
			}
			if strings.Contains(string(out), graphtest.FakeToken) {
				t.Error("the token leaked into the output")
			}
			testutil.Golden(t, "org/"+tt.name, out)
		})
	}
}
