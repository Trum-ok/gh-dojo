// Package graphtest replays recorded GitHub GraphQL responses in tests, so
// `go test ./...` never touches the network.
//
// # Writing a test
//
//	func TestShow(t *testing.T) {
//		srv := graphtest.New(t, "testdata/graphql/board.json", "testdata/graphql/show_42.json")
//		c := srv.Client() // *graph.Client: fake token, instant retries
//
//		got, err := doSomething(context.Background(), c)
//		if err != nil {
//			t.Fatal(err)
//		}
//		if n := srv.Requests(); n != 2 {
//			t.Errorf("requests = %d, want 2", n)
//		}
//	}
//
// The test fails on a request that no fixture answers and, at cleanup, on a
// fixture interaction that was never requested. A fixture answers requests
// with the same operation name, query and variables, once each, in file
// order; mark an interaction "repeat": true to let it answer again (a 503
// that answers every retry, a schema read before and after a cache
// expiry). Name every operation and put it first in the document
// (query BoardSchema(...) {...} fragment ...).
//
// # Fixture files
//
// Fixtures live in the package's testdata/graphql/ directory:
//
//	{
//	  "comment": "what this is; for hand-crafted files, where the shape comes from",
//	  "handcrafted": true,
//	  "interactions": [
//	    {
//	      "operation": "BoardSchema",
//	      "query": "query BoardSchema(...) {...}",
//	      "variables": {"owner": "Trum-ok", "number": 5},
//	      "repeat": true,
//	      "status": 502,
//	      "headers": {"Retry-After": "1"},
//	      "response": {"data": {...}}
//	    }
//	  ]
//	}
//
// operation, query and response are required. The query must match the
// one the code sends (whitespace and commas ignored), so editing a query
// without updating its fixtures fails loudly instead of replaying a stale
// shape; hand-crafted fixtures copy the query too. Omitted variables match
// any variables; omitted status means 200.
//
// # Recording
//
// Successful reads are recorded from the real API, never written by hand:
//
//	GH_DOJO_RECORD=1 go test ./internal/board -run TestUserBoard
//
// In record mode requests go to GitHub with gh's own token (or GH_TOKEN) and
// the responses are written at cleanup, each to the first file that already
// has the same operation, else to the last file passed to New. Identical
// repeated answers are stored once with "repeat": true (never for
// mutations), and a "repeat" already in the file is kept. Only response
// bodies and the Retry-After / X-RateLimit-Remaining headers are stored;
// request headers (and so the token) never are, and a file that would
// contain the token is not written. Mutations are refused unless
// GH_DOJO_RECORD_MUTATIONS=1 is also set; only record them against a
// sandbox board. Tests that use a "handcrafted" file are skipped.
//
// Errors GitHub can't be asked to produce on demand (5xx, rate limits,
// missing scopes) are hand-crafted: set "handcrafted": true and say in
// "comment" where the shape comes from. Test the token never leaks by
// checking output for FakeToken.
package graphtest
