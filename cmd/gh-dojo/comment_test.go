package main

import (
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

// Hand-crafted comment fixtures; the card reads are recorded ones from
// internal/issue (comment_card_3_posted.json adds comments to card_3).
const (
	commentAddFixture   = "testdata/graphql/comment_add.json"
	commentStdinFixture = "testdata/graphql/comment_add_stdin.json"
	commentPostedCard   = "testdata/graphql/comment_card_3_posted.json"
	cardNotFoundFixture = "../../internal/issue/testdata/graphql/card_not_found.json"
)

func TestComment(t *testing.T) {
	withConfig(t, dojoConfig)
	srv := withApp(t, boardFixture, card3Fixture, commentAddFixture)
	before := warmSchema(t, srv)
	code, out := runOut("comment", "3", "  Reproduced on main.\n")
	testutil.Golden(t, "comment/left", out)
	if code != render.ExitOK {
		t.Errorf("code = %d", code)
	}
	if n := srv.Requests() - before; n != 2 {
		t.Errorf("requests with a warm schema = %d, want 2 (card, addComment)", n)
	}
}

// TestCommentRepeat: the latest gh-dojo comment already has the text, so
// nothing is posted (the fixtures have no AddComment to answer).
func TestCommentRepeat(t *testing.T) {
	withConfig(t, dojoConfig)
	withApp(t, boardFixture, commentPostedCard)
	code, out := runOut("comment", "#3", "Reproduced on main.")
	testutil.Golden(t, "comment/already_present", out)
	if code != render.ExitOK {
		t.Errorf("code = %d", code)
	}
}

// TestCommentAfterReply: the same text again after someone else's comment
// is a deliberate repeat and is posted.
func TestCommentAfterReply(t *testing.T) {
	withConfig(t, dojoConfig)
	withApp(t, boardFixture, "testdata/graphql/comment_card_3_replied.json")
	code, out := runOut("comment", "3", "Reproduced on main.")
	testutil.Golden(t, "comment/left", out)
	if code != render.ExitOK {
		t.Errorf("code = %d", code)
	}
}

func TestCommentStdin(t *testing.T) {
	withConfig(t, dojoConfig)
	withApp(t, boardFixture, card3Fixture, commentStdinFixture)
	code, out := runIn("Line one\nLine two\n", "comment", "3", "--body-file", "-")
	testutil.Golden(t, "comment/left", out)
	if code != render.ExitOK {
		t.Errorf("code = %d", code)
	}
}

func TestCommentNotFound(t *testing.T) {
	withConfig(t, dojoConfig)
	withApp(t, boardFixture, cardNotFoundFixture)
	code, out := runOut("comment", "9999", "Reproduced on main.")
	testutil.Golden(t, "comment/not_found", out)
	if code != render.ExitNotFound {
		t.Errorf("code = %d, want %d", code, render.ExitNotFound)
	}
}

// TestCommentUsage: bad arguments fail before any request (newApp is noApp).
func TestCommentUsage(t *testing.T) {
	withConfig(t, dojoConfig)
	tests := []struct {
		name  string
		stdin string
		args  []string
	}{
		{"no_text", "", []string{"comment", "3"}},
		{"two_sources", "", []string{"comment", "3", "text", "--body-file", "-"}},
		{"empty_text", "", []string{"comment", "3", "  "}},
		{"empty_stdin", "\n\n", []string{"comment", "3", "--body-file", "-"}},
		{"too_many_args", "", []string{"comment", "3", "Reproduced", "on", "main"}},
		{"bad_number", "", []string{"comment", "abc", "text"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out := runIn(tt.stdin, tt.args...)
			testutil.Golden(t, "comment/"+tt.name, out)
			if code != render.ExitUsage {
				t.Errorf("code = %d, want %d", code, render.ExitUsage)
			}
		})
	}
}

func TestCommentHelp(t *testing.T) {
	code, out := runOut("comment", "--help")
	testutil.Golden(t, "comment/help", out)
	if code != render.ExitOK {
		t.Errorf("code = %d", code)
	}
}
