package issue_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/graph/graphtest"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

func TestParseRef(t *testing.T) {
	def := issue.Repo{Owner: "Trum-ok", Name: "gh-dojo"}
	tests := []struct {
		in   string
		ok   bool
		want string // as Ref.String(def) says it
	}{
		{"12", true, "#12"},
		{" #12 ", true, "#12"},
		{"acme/shop#7", true, "acme/shop#7"},
		{"trum-ok/GH-DOJO#7", true, "#7"},
		{"#0", false, ""},
		{"12a", false, ""},
		{"the API team", false, ""},
		{"acme/#7", false, ""},
		{"acme/shop.git#7", false, ""},
		{"99999999999", false, ""},
		{"https://github.com/Trum-ok/gh-dojo/issues/26", true, "#26"},
		{" https://github.com/acme/shop/issues/7/ ", true, "acme/shop#7"},
		{"https://ghe.example.com/acme/shop/issues/7", true, "acme/shop#7"},
		{"https://github.com/acme/shop/pull/7", false, ""},
		{"https://github.com/acme/shop/issues/7#issuecomment-1", false, ""},
		{"github.com/acme/shop/issues/7", false, ""},
		{"", false, ""},
	}
	for _, tt := range tests {
		r, ok := issue.ParseRef(tt.in, def)
		if ok != tt.ok || ok && r.String(def) != tt.want {
			t.Errorf("ParseRef(%q) = %q, %v; want %q, %v", tt.in, r.String(def), ok, tt.want, tt.ok)
		}
	}
	a, _ := issue.ParseRef("12", def)
	b, _ := issue.ParseRef("trum-ok/gh-dojo#12", def)
	if !a.Same(b) {
		t.Errorf("%v and %v should name the same issue", a, b)
	}
}

// links3 are the links the recorded fixtures read with issue #3: #2
// blocks it, #26 does not, #9999 does not exist, and neither does the
// repository Trum-ok/no-such-repo.
var links3 = issue.Links{
	{Repo: dojo, Number: 2},
	{Repo: dojo, Number: 26},
	{Repo: dojo, Number: 9999},
	{Repo: issue.Repo{Owner: "Trum-ok", Name: "no-such-repo"}, Number: 1},
}

// checkLinks checks what the recorded fixtures say about links3 for #3.
func checkLinks(t *testing.T, id string, links []*issue.Linked) {
	t.Helper()
	if len(links) < 2 {
		t.Fatalf("%d links, want at least 2", len(links))
	}
	if two := links[0]; two.Missing || two.ID == "" || !two.Blocks(id) {
		t.Errorf("#2 = %+v, want found and blocking #3", two)
	}
	if six := links[1]; six.Missing || six.ID == "" || six.Blocks(id) || !strings.HasPrefix(six.Title, "Блокеры") {
		t.Errorf("#26 = %+v, want found, not blocking #3", six)
	}
	for _, l := range links[2:] {
		if !l.Missing || l.ID != "" {
			t.Errorf("%v = %+v, want missing", l.Ref, l)
		}
	}
}

// TestReadLookupLinks: the links ride along with the card's one request;
// a missing issue or repository among them does not fail the card.
func TestReadLookupLinks(t *testing.T) {
	srv := graphtest.New(t, fixture("card_3_links"))
	req := dojoStatus
	req.Number = 3
	req.Lookup = issue.Lookup{Links: links3}
	card, err := issue.Read(context.Background(), srv.Client(), req)
	if err != nil {
		t.Fatal(err)
	}
	if n := srv.Requests(); n != 1 {
		t.Errorf("requests = %d, want exactly 1", n)
	}
	if card.Incomplete || card.Status() != "Ready" || card.Found == nil {
		t.Fatalf("card: incomplete %v, status %q, found %v", card.Incomplete, card.Status(), card.Found)
	}
	checkLinks(t, card.ID, card.Found.Links)
	err = issue.LinksError("--by", card.Found.Links, dojo)
	if got := render.FromError(err); got.Code != render.ExitNotFound ||
		got.What != "--by #9999, Trum-ok/no-such-repo#1: no such issue, or the token cannot see it" {
		t.Errorf("LinksError = %d %q", got.Code, got.What)
	}
}

// TestReadLinks reads what gh dojo block needs in one request.
func TestReadLinks(t *testing.T) {
	srv := graphtest.New(t, fixture("links_3"))
	target, links, err := issue.ReadLinks(context.Background(), srv.Client(), dojo, 3, dojo, links3[:2], "--by")
	if err != nil {
		t.Fatal(err)
	}
	if target.Number != 3 || target.ID == "" || target.Title == "" {
		t.Errorf("target = %+v", target)
	}
	checkLinks(t, target.ID, links)
	if !target.BlockedBy(links[0]) || target.BlockedBy(links[1]) {
		t.Errorf("target blocked by #2 %v, #26 %v; want true, false", target.BlockedBy(links[0]), target.BlockedBy(links[1]))
	}
	// Nothing #3 blocks is among them: no cycle.
	if links[0].BlockedBy(target.ID) || links[1].BlockedBy(target.ID) {
		t.Errorf("#2 or #26 read as blocked by #3")
	}

	// A missing blocker is exit code 3 naming it, after the same one request.
	_, _, err = issue.ReadLinks(context.Background(), srv.Client(), dojo, 3, dojo, links3, "--by")
	if got := render.FromError(err); got.Code != render.ExitNotFound ||
		got.What != "--by #9999, Trum-ok/no-such-repo#1: no such issue, or the token cannot see it" {
		t.Errorf("missing links: %d %q", got.Code, got.What)
	}
	// A missing target is exit code 3 about the target.
	_, _, err = issue.ReadLinks(context.Background(), srv.Client(), dojo, 9998, dojo, links3[:1], "--by")
	if got := render.FromError(err); got.Code != render.ExitNotFound || got.What != "issue #9998 not found in Trum-ok/gh-dojo" {
		t.Errorf("missing target: %d %q", got.Code, got.What)
	}
	if n := srv.Requests(); n != 3 {
		t.Errorf("requests = %d, want 3", n)
	}
}

// TestLinksError names every link that cannot block in one message: the
// missing ones, then the pull requests. "gh dojo list" is the fix only when
// all of them are in the default repository.
func TestLinksError(t *testing.T) {
	ref := func(repo issue.Repo, n int) issue.Ref { return issue.Ref{Repo: repo, Number: n} }
	acme := issue.Repo{Owner: "acme", Name: "api"}
	tests := []struct {
		links     []*issue.Linked
		what, fix string
	}{
		{[]*issue.Linked{{Ref: ref(dojo, 2)}}, "", ""},
		{[]*issue.Linked{{Ref: ref(dojo, 98), Missing: true}, {Ref: ref(dojo, 40), PullRequest: true}, {Ref: ref(dojo, 99), Missing: true}},
			"--by #98, #99: no such issue in Trum-ok/gh-dojo, or the token cannot see it; #40: a pull request, not an issue; only issues can block",
			issue.ListFix},
		{[]*issue.Linked{{Ref: ref(dojo, 40), PullRequest: true}, {Ref: ref(dojo, 41), PullRequest: true}},
			"--by #40, #41: pull requests, not issues; only issues can block", issue.ListFix},
		{[]*issue.Linked{{Ref: ref(acme, 7), Missing: true}},
			"--by acme/api#7: no such issue, or the token cannot see it", ""},
		{[]*issue.Linked{{Ref: ref(dojo, 98), Missing: true}, {Ref: ref(acme, 8), PullRequest: true}},
			"--by #98: no such issue in Trum-ok/gh-dojo, or the token cannot see it; acme/api#8: a pull request, not an issue; only issues can block", ""},
	}
	for _, tt := range tests {
		err := issue.LinksError("--by", tt.links, dojo)
		if tt.what == "" {
			if err != nil {
				t.Errorf("LinksError = %v, want nil", err)
			}
			continue
		}
		if got := render.FromError(err); got.Code != render.ExitNotFound || got.What != tt.what || got.Fix != tt.fix {
			t.Errorf("LinksError = %d %q (fix %q)\nwant %q (fix %q)", got.Code, got.What, got.Fix, tt.what, tt.fix)
		}
	}
}
