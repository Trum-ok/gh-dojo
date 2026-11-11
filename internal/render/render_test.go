package render

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Trum-ok/gh-dojo/internal/testutil"
	"github.com/cli/go-gh/v2/pkg/api"
)

func TestErrorFormat(t *testing.T) {
	statuses := []string{"Backlog", "Ready", "In progress", "Waiting", "Done"}
	many := make([]string, 25)
	for i := range many {
		many[i] = fmt.Sprintf("label-%02d", i+1)
	}
	tests := []struct {
		name string
		err  error
		code Code
	}{
		{"usage_full", Usage("unknown status %q", "Doing").WithAllowed(statuses...).WithFix("gh dojo describe"), ExitUsage},
		{"denied", Denied(`"drop" needs the user's confirmation; ask the user, then re-run with --yes`), ExitDenied},
		{"not_found", NotFound("issue #999 not found in acme/shop").WithFix("gh dojo list"), ExitNotFound},
		{"network_auth", Network("token lacks the project scope").WithFix(AuthFix), ExitNetwork},
		{"partial", Partial("applied 3 of 4, #17: status option \"QA\" missing").WithFix("gh dojo done 14 15 16 17"), ExitPartial},
		{"multiline_clipped", Usage("line one\nline two\nline three\nline four\nline five").WithAllowed("a").WithFix("gh dojo --help"), ExitUsage},
		{"long_allowed", Usage("unknown label %q", "bgu").WithAllowed(many...), ExitUsage},
		{"wrapped", fmt.Errorf("load config: %w", Usage("bad version")), ExitUsage},
		{"plain_error", errors.New("open .github/dojo.toml: permission denied\nsecond line hidden"), ExitUsage},
		{"http_404", &api.HTTPError{StatusCode: 404, Message: "Not Found", RequestURL: mustURL("https://api.github.com/x")}, ExitNotFound},
		{"http_401", &api.HTTPError{StatusCode: 401, Message: "Bad credentials", RequestURL: mustURL("https://api.github.com/x")}, ExitNetwork},
		{"http_rate_limit", &api.HTTPError{StatusCode: 403, Headers: http.Header{"X-Ratelimit-Remaining": {"0"}}, RequestURL: mustURL("https://api.github.com/x")}, ExitNetwork},
		{"http_502", &api.HTTPError{StatusCode: 502, Message: "<html>bad gateway</html>", RequestURL: mustURL("https://api.github.com/x")}, ExitNetwork},
		{"graphql_not_found", &api.GraphQLError{Errors: []api.GraphQLErrorItem{{Type: "NOT_FOUND", Message: "Could not resolve to a ProjectV2 with the number 3."}}}, ExitNotFound},
		{"graphql_scopes", &api.GraphQLError{Errors: []api.GraphQLErrorItem{{Type: "INSUFFICIENT_SCOPES", Message: "Your token has not been granted the required scopes."}}}, ExitNetwork},
		{"timeout", fmt.Errorf("post: %w", context.DeadlineExceeded), ExitNetwork},
		{"net_error", &url.Error{Op: "Post", URL: "https://api.github.com/graphql", Err: errors.New("dial tcp: no such host")}, ExitNetwork},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if code := WriteError(&buf, tt.err); code != tt.code {
				t.Errorf("code = %d, want %d", code, tt.code)
			}
			if n := strings.Count(buf.String(), "\n"); n > 5 {
				t.Errorf("error block has %d lines, want at most 5", n)
			}
			testutil.Golden(t, "errors/"+tt.name, buf.Bytes())
		})
	}
}

func TestWriteErrorNil(t *testing.T) {
	var buf bytes.Buffer
	if code := WriteError(&buf, nil); code != ExitOK || buf.Len() != 0 {
		t.Errorf("WriteError(nil) = %d, %q; want 0, empty", code, buf.String())
	}
}

func TestFromErrorKeepsCause(t *testing.T) {
	cause := &api.HTTPError{StatusCode: 404, RequestURL: mustURL("https://api.github.com/x")}
	var httpErr *api.HTTPError
	if !errors.As(FromError(cause), &httpErr) {
		t.Error("FromError lost the underlying *api.HTTPError")
	}
}

func TestConfirm(t *testing.T) {
	long := strings.Repeat("very long title ", 20)
	lines := []string{
		Confirm("#42", Transition("Backlog", "In progress"), "comment left"),
		Confirm("#42", Transition("In progress", "In progress"), "comment already present"),
		Confirm("#57", "created", Transition("", "Backlog"), "", "blocked by #42"),
		Confirm("#7", "comment left"),
		Confirm("#8"),
		Confirm("#9", "created: "+long),
		Confirm("#10", "title with\nnewline"),
	}
	for _, l := range lines {
		if n := utf8.RuneCountInString(l); n > MaxConfirmRunes {
			t.Errorf("confirmation is %d runes, want at most %d: %q", n, MaxConfirmRunes, l)
		}
	}
	testutil.Golden(t, "confirm", []byte(strings.Join(lines, "\n")+"\n"))
}

func TestWarn(t *testing.T) {
	var buf bytes.Buffer
	Warn(&buf, "issue #%d has %d open blockers:\n#3, #4", 42, 2)
	testutil.Golden(t, "warn", buf.Bytes())
}

func TestTruncate(t *testing.T) {
	items := make([]int, 73)
	var out strings.Builder
	show := func(label string, got []int, note string) {
		fmt.Fprintf(&out, "%s: %d items, note=%q\n", label, len(got), note)
	}

	got, note := Truncate(items, 50, 73, "")
	show("cut", got, note)
	got, note = Truncate(items[:50], 0, 73, "use --comments all to see all")
	show("api total larger", got, note)
	got, note = Truncate(items[:10], 50, 10, "")
	show("fits", got, note)
	got, note = Truncate(items, 0, 0, "")
	show("no limit", got, note)
	got, note = Truncate([]int(nil), 50, 0, "")
	show("empty", got, note)

	testutil.Golden(t, "truncate", []byte(out.String()))
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}
