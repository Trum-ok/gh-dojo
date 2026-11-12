package main

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/issue"
)

// Issues on the command line and in output (spec F12). Every command that
// takes an issue takes 12, "#12", owner/repo#12 or the issue's URL; a bare
// number is in the default repository (board.repos[0], see App.Repo), and
// any other repository can be named, in board.repos or not. Output names
// an issue of the default repository #12, and any other owner/repo#12: in
// the text and in the JSON's refs alike.

// parseRef parses an issue argument, up to GitHub's maximum number (a
// GraphQL Int, 2^31-1): 12, "#12", owner/repo#12 or the issue's URL. A
// bare number names no repository yet: resolve it with Ref.In.
func parseRef(s string) (issue.Ref, bool) { return issue.ParseRef(s, issue.Repo{}) }

// issueExamples shows how to pass an issue. A bare #42 is a comment in the
// shell, so the # form is quoted; owner/repo#42 needs no quotes.
const issueExamples = `42, "#42" or owner/repo#42`

// argRefs parses the validated issue arguments, a bare number in def,
// dropping repeats. With an empty def a bare number stays without a
// repository, and owner/repo#12 differs from 12.
func argRefs(args []string, def issue.Repo) []issue.Ref {
	var refs []issue.Ref
	for _, a := range args {
		if r, ok := parseRef(a); ok {
			refs = appendRef(refs, r.In(def))
		}
	}
	return refs
}

// inRepo resolves refs parsed without a repository to def, dropping the
// repeats that makes ("12 owner/repo#12" with owner/repo the default).
func inRepo(refs []issue.Ref, def issue.Repo) []issue.Ref {
	var out []issue.Ref
	for _, r := range refs {
		out = appendRef(out, r.In(def))
	}
	return out
}

// appendRef appends r to refs unless it is there already.
func appendRef(refs []issue.Ref, r issue.Ref) []issue.Ref {
	if slices.ContainsFunc(refs, r.Same) {
		return refs
	}
	return append(refs, r)
}

// refArgs is refs as command-line arguments ("12", "owner/repo#12"), for
// the example command of an error.
func refArgs(refs []issue.Ref, def issue.Repo) []string {
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.Arg(def)
	}
	return out
}

// refKey identifies an issue in a map: GitHub's repository names compare
// without case.
func refKey(r issue.Ref) string {
	return strings.ToLower(r.Repo.String()) + "#" + strconv.Itoa(r.Number)
}

// compareRefs orders issues of the default repository first, by number,
// then the others by repository and number.
func compareRefs(def issue.Repo) func(a, b issue.Ref) int {
	return func(a, b issue.Ref) int {
		ad, bd := a.Same(issue.Ref{Repo: def, Number: a.Number}), b.Same(issue.Ref{Repo: def, Number: b.Number})
		switch {
		case ad != bd && ad:
			return -1
		case ad != bd:
			return 1
		}
		if c := cmp.Compare(strings.ToLower(a.Repo.String()), strings.ToLower(b.Repo.String())); c != 0 {
			return c
		}
		return cmp.Compare(a.Number, b.Number)
	}
}

// maxErrorRefs is how many issues an error line names.
const maxErrorRefs = 10

// refNames lists issues as "#9, acme/api#10", naming at most maxErrorRefs.
func refNames(refs []issue.Ref, def issue.Repo) string {
	names := make([]string, 0, min(len(refs), maxErrorRefs))
	for _, r := range refs[:min(len(refs), maxErrorRefs)] {
		names = append(names, r.String(def))
	}
	s := strings.Join(names, ", ")
	if n := len(refs) - len(names); n > 0 {
		s += fmt.Sprintf(" and %d more", n)
	}
	return s
}

// linkRef names a linked issue of card c as output does: relative to the
// default repository def, not to the card's.
func linkRef(c *issue.Card, l issue.Link, def issue.Repo) string {
	return issue.Ref{Repo: l.Repo, Number: l.Number}.In(c.Repo).String(def)
}
