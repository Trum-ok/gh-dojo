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
	// A closed set longer than both cuts: 25 values, over 400 runes.
	keys := make([]string, 25)
	for i := range keys {
		keys[i] = fmt.Sprintf("a_long_config_key_%02d", i+1)
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
		{"long_allowed_all", fmt.Errorf("load config: %w", Usage("unknown key verbs.start.x").WithAllowedAll(keys...).WithFix("fix the key")), ExitUsage},
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
		{"canceled", fmt.Errorf("post: %w", context.Canceled), ExitNetwork},
		{"internal", Internal("assignment to entry in nil map"), ExitUsage},
		{"typed_nil", error((*Error)(nil)), ExitUsage},
		{"typed_nil_wrapped", fmt.Errorf("load: %w", (*Error)(nil)), ExitUsage},
		{"blank_lines", Usage("line one\n\n  \nline two\n\nline three\nline four").WithFix("gh dojo --help"), ExitUsage},
		{"allowed_newlines", Usage("unknown status %q", "Doing").WithAllowed("In\nprogress", "Done\r\n", "a\nb\nc\nd").WithFix("gh dojo describe"), ExitUsage},
		{"long_fix_kept", Usage("bad title").WithFix("gh dojo new " + strings.Repeat("x", 250)), ExitUsage},
		{"too_long_fix_dropped", Usage("bad title").WithFix("gh dojo new " + strings.Repeat("x", 500)), ExitUsage},
		{"graphql_unknown_type", &api.GraphQLError{Errors: []api.GraphQLErrorItem{{Message: "Could not resolve to a node with the global id of 'PVTF_lADOBn1234567zM4A'"}}}, ExitUsage},
		{"graphql_validation", &api.GraphQLError{Errors: []api.GraphQLErrorItem{{Type: "UNPROCESSABLE", Message: "Field 'singleSelectOptionId' got invalid value 'f75ad846' for PVTSSF_lADOBn1234567zgABC"}}}, ExitUsage},
		{"graphql_not_found_id", &api.GraphQLError{Errors: []api.GraphQLErrorItem{{Type: "NOT_FOUND", Message: "Could not resolve to a node with the global id of 'PVTI_lADOBn1234567zgXYZ'."}}}, ExitNotFound},
		{"graphql_not_found_legacy_id", &api.GraphQLError{Errors: []api.GraphQLErrorItem{{Type: "NOT_FOUND", Message: "Could not resolve to a node with the global id of 'MDU6SXNzdWUxMjM0NTY3ODk='."}}}, ExitNotFound},
		{"graphql_scopes_second", &api.GraphQLError{Errors: []api.GraphQLErrorItem{{Message: "Something else"}, {Type: "INSUFFICIENT_SCOPES", Message: "Your token has not been granted the required scopes."}}}, ExitNetwork},
		{"graphql_not_found_second", &api.GraphQLError{Errors: []api.GraphQLErrorItem{{Message: "Something else"}, {Type: "NOT_FOUND", Message: "Could not resolve to an Issue with the number of 999."}}}, ExitNotFound},
		{"graphql_rate_limited", &api.GraphQLError{Errors: []api.GraphQLErrorItem{{Type: "NOT_FOUND", Message: "x"}, {Type: "RATE_LIMITED", Message: "API rate limit exceeded"}}}, ExitNetwork},
		{"graphql_wrapped", fmt.Errorf("move #42: %w", &api.GraphQLError{Errors: []api.GraphQLErrorItem{{Message: "Argument 'fieldId' on InputObject has an invalid value (PVTF_lADOBn1234567zM4A)"}}}), ExitUsage},
		{"http_secondary_rate_limit", &api.HTTPError{StatusCode: 403, Message: "You have exceeded a secondary rate limit. Please wait a few minutes before you try again.", RequestURL: mustURL("https://api.github.com/graphql")}, ExitNetwork},
		// GitHub's limit on creating content: UNPROCESSABLE "was submitted
		// too quickly" in a 200 (cli/cli#4801), a 403 over HTTP.
		{"graphql_content_limit", &api.GraphQLError{Errors: []api.GraphQLErrorItem{{Type: "UNPROCESSABLE", Message: "was submitted too quickly", Path: []any{"addComment"}}}}, ExitNetwork},
		{"graphql_secondary_rate_limit", &api.GraphQLError{Errors: []api.GraphQLErrorItem{{Message: "You have exceeded a secondary rate limit. Please wait a few minutes before you try again."}}}, ExitNetwork},
		{"http_content_limit", &api.HTTPError{StatusCode: 403, Message: "You have exceeded a secondary rate limit and have been temporarily blocked from content creation. Please retry your request again later.", Headers: http.Header{"Retry-After": {"60"}}, RequestURL: mustURL("https://api.github.com/graphql")}, ExitNetwork},
		{"http_content_limit_no_retry_after", &api.HTTPError{StatusCode: 403, Message: "You have exceeded a secondary rate limit and have been temporarily blocked from content creation. Please retry your request again later.", RequestURL: mustURL("https://api.github.com/graphql")}, ExitNetwork},
		{"http_retry_after", &api.HTTPError{StatusCode: 403, Message: "Forbidden", Headers: http.Header{"Retry-After": {"60"}}, RequestURL: mustURL("https://api.github.com/graphql")}, ExitNetwork},
		{"http_403_forbidden", &api.HTTPError{StatusCode: 403, Message: "Resource not accessible by integration", RequestURL: mustURL("https://api.github.com/graphql")}, ExitNetwork},
		{"http_422_id", &api.HTTPError{StatusCode: 422, Message: "Validation failed for PVT_kwHOAbc12345", RequestURL: mustURL("https://api.github.com/x")}, ExitUsage},
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

// TestErrorNeverLeaksIDs checks that node IDs and raw GraphQL text of unknown
// error types never reach the output.
func TestErrorNeverLeaksIDs(t *testing.T) {
	raw := "Field 'x' doesn't exist on type 'ProjectV2' PVTF_lADOBn1234567zM4A"
	for _, err := range []error{
		&api.GraphQLError{Errors: []api.GraphQLErrorItem{{Message: raw}}},
		&api.GraphQLError{Errors: []api.GraphQLErrorItem{{Type: "NOT_FOUND", Message: raw}}},
		&api.GraphQLError{Errors: []api.GraphQLErrorItem{{Type: "FORBIDDEN", Message: raw}}},
		&api.GraphQLError{Errors: []api.GraphQLErrorItem{{Type: "INSUFFICIENT_SCOPES", Message: raw}}},
		&api.HTTPError{StatusCode: 403, Message: raw, RequestURL: mustURL("https://api.github.com/x")},
		&api.HTTPError{StatusCode: 401, Message: raw, RequestURL: mustURL("https://api.github.com/x")},
		&api.HTTPError{StatusCode: 400, Message: raw, RequestURL: mustURL("https://api.github.com/x")},
	} {
		text := FromError(err).Text()
		if strings.Contains(text, "PVTF_") || strings.Contains(text, "doesn't exist") {
			t.Errorf("raw API text leaked for %T: %q", err, text)
		}
	}
}

func TestErrorTextLimits(t *testing.T) {
	values := []string{"a\nb", "c\r\nd", "e"}
	text := Usage("one\n\n\ntwo\n\nthree\nfour").WithAllowed(values...).WithFix("gh dojo\n--help").Text()
	lines := strings.Split(text, "\n")
	if len(lines) != 5 {
		t.Fatalf("got %d lines, want 5:\n%s", len(lines), text)
	}
	if lines[2] != "three" {
		t.Errorf("blank lines took the message budget: line 3 = %q, want %q", lines[2], "three")
	}
	if lines[3] != "allowed: a b, c d, e" {
		t.Errorf("allowed line = %q", lines[3])
	}
}

func TestClipGraphemes(t *testing.T) {
	family := "\U0001F468\u200D\U0001F469\u200D\U0001F467" // one cluster, five runes
	accent := "e\u0301"                                    // é as e + combining acute: one cluster, two runes
	for _, tt := range []struct{ s, want string }{
		{strings.Repeat("x", 6) + family, "xxxxxx…"},
		{strings.Repeat("x", 8) + accent + "yy", "xxxxxxxx…"},
		{strings.Repeat("x", 7) + accent + "yy", "xxxxxxx" + accent + "…"},
		{"short", "short"},
		{"abcdefgh ijk", "abcdefgh…"},
	} {
		if got := Clip(tt.s, 10); got != tt.want {
			t.Errorf("Clip(%q, 10) = %q, want %q", tt.s, got, tt.want)
		}
		if n := RuneLen(Clip(tt.s, 10)); n > 10 {
			t.Errorf("Clip(%q, 10) has %d runes", tt.s, n)
		}
	}
}

func TestErrorFixNeverCut(t *testing.T) {
	fix := "gh dojo new " + strings.Repeat("y", 300)
	text := Usage("x").WithFix(fix).Text()
	if !strings.HasSuffix(text, "fix: "+fix) {
		t.Errorf("fix line was changed:\n%s", text)
	}
	if text := Usage("x").WithFix(fix + strings.Repeat("y", 200)).Text(); strings.Contains(text, "fix:") {
		t.Errorf("too long fix printed:\n%s", text)
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
	effects := []string{"created", Transition("", "Backlog"), "labels bug, area:api", "blocked by #42, #43"}
	lines := []string{
		Confirm("#42", "", Transition("Backlog", "In progress"), "comment left"),
		Confirm("#42", "", Transition("In progress", "In progress"), "comment already present"),
		Confirm("#57", "Fix login redirect", "created", Transition("", "Backlog"), "", "blocked by #42"),
		Confirm("#7", "", "comment left"),
		Confirm("#8", ""),
		Confirm("#8", "Only a title"),
		Confirm("#9", long, effects...),
		Confirm("#10", "title with\nnewline", "created"),
		Confirm("#11", "Заголовок задачи "+strings.Repeat("é", 200), "created"),
		Confirm("#12", long, strings.Repeat("effect, ", 18)+"last effect"),
	}
	for _, l := range lines[:len(lines)-1] {
		if n := utf8.RuneCountInString(l); n > maxConfirmRunes {
			t.Errorf("confirmation is %d runes, want at most %d: %q", n, maxConfirmRunes, l)
		}
	}
	// Effects are never cut, even past the limit; the title is dropped then.
	for _, l := range lines[len(lines)-2:] {
		if !strings.HasSuffix(l, "last effect") && !strings.HasSuffix(l, "created") {
			t.Errorf("effects were cut: %q", l)
		}
	}
	if l := Confirm("#9", long, effects...); !strings.HasSuffix(l, strings.Join(effects, ", ")) {
		t.Errorf("effects were cut: %q", l)
	}
	testutil.Golden(t, "confirm", []byte(strings.Join(lines, "\n")+"\n"))
}

func TestTransition(t *testing.T) {
	for _, tt := range []struct{ from, to, want string }{
		{"Backlog", "In progress", "Backlog → In progress"},
		{"Done", "Done", "Done (unchanged)"},
		{"", "Backlog", "no status → Backlog"},
		{"Backlog", "", "Backlog → no status"},
		{"", "", "no status (unchanged)"},
	} {
		if got := Transition(tt.from, tt.to); got != tt.want {
			t.Errorf("Transition(%q, %q) = %q, want %q", tt.from, tt.to, got, tt.want)
		}
	}
}

func TestWarn(t *testing.T) {
	var buf bytes.Buffer
	Warn(&buf, "issue #%d has %d open blockers:\n#3, #4", 42, 2)
	testutil.Golden(t, "warn", buf.Bytes())
}

func TestShown(t *testing.T) {
	for _, tt := range []struct {
		shown, total int
		hint, want   string
	}{
		{50, 73, "", "showing 50 of 73"},
		{50, 73, "use --comments all to see all", "showing 50 of 73, use --comments all to see all"},
		{10, 10, "use --limit 10", ""},
		{0, 0, "", ""},
	} {
		if got := Shown(tt.shown, tt.total, tt.hint); got != tt.want {
			t.Errorf("Shown(%d, %d, %q) = %q, want %q", tt.shown, tt.total, tt.hint, got, tt.want)
		}
	}
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

// TestReason: the first clause of the first line of an error's message.
func TestReason(t *testing.T) {
	for _, tt := range []struct {
		err  error
		want string
	}{
		{Usage("status not changed; ask the user\nfix it"), "status not changed"},
		{Denied(" done cannot move #4 \nthis is a process rule"), "done cannot move #4"},
		{errors.New("plain"), "plain"},
	} {
		if got := Reason(tt.err); got != tt.want {
			t.Errorf("Reason(%q) = %q, want %q", tt.err, got, tt.want)
		}
	}
}
