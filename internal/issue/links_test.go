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
	target, links, err := issue.ReadLinks(context.Background(), srv.Client(), dojo, 3, links3[:2], "--by")
	if err != nil {
		t.Fatal(err)
	}
	if target.Number != 3 || target.ID == "" || target.Title == "" {
		t.Errorf("target = %+v", target)
	}
	checkLinks(t, target.ID, links)

	// A missing blocker is exit code 3 naming it, after the same one request.
	_, _, err = issue.ReadLinks(context.Background(), srv.Client(), dojo, 3, links3, "--by")
	if got := render.FromError(err); got.Code != render.ExitNotFound ||
		got.What != "--by #9999, Trum-ok/no-such-repo#1: no such issue, or the token cannot see it" {
		t.Errorf("missing links: %d %q", got.Code, got.What)
	}
	// A missing target is exit code 3 about the target.
	_, _, err = issue.ReadLinks(context.Background(), srv.Client(), dojo, 9998, links3[:1], "--by")
	if got := render.FromError(err); got.Code != render.ExitNotFound || got.What != "issue #9998 not found in Trum-ok/gh-dojo" {
		t.Errorf("missing target: %d %q", got.Code, got.What)
	}
	if n := srv.Requests(); n != 3 {
		t.Errorf("requests = %d, want 3", n)
	}
}
