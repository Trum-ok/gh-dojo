package batch

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// issueReason is why err failed one issue, for the summary: the first
// clause of its message (render.Reason), but the whole first line of a
// refusal, which says what the verb allows ("it starts only from Ready"),
// with its "; " read ", " since the summary separates issues with ";".
func issueReason(err error) string {
	e := render.FromError(err)
	if e.Code != render.ExitDenied {
		return render.Reason(err)
	}
	what, _, _ := strings.Cut(e.What, "\n")
	return strings.ReplaceAll(strings.TrimSpace(what), "; ", ", ")
}

// MaxSummaryRunes is how long a line of the summary error may grow: the
// error block cuts a line at render.MaxLineRunes, "error: " included, and
// the summary never relies on that cut, which would hide issues silently.
const MaxSummaryRunes = render.MaxLineRunes - 10

// issueRefRE matches an issue reference in a reason: "#9", "issue #9",
// "acme/api#9".
var issueRefRE = regexp.MustCompile(`(issue )?([A-Za-z0-9][A-Za-z0-9_-]*/[A-Za-z0-9._-]+)?#(\d+)\b`)

// generalReason words why, the reason issue ref failed, without naming
// it, so the issues that failed alike share one entry: "issue #9 not
// found" reads "issue not found", "#9 is closed" reads "it is closed". A
// bare #9 is the issue whatever its repository: "issue #9 not found in
// acme/api" names the repository apart.
func generalReason(why string, ref issue.Ref) string {
	num := strconv.Itoa(ref.Number)
	return issueRefRE.ReplaceAllStringFunc(why, func(m string) string {
		sub := issueRefRE.FindStringSubmatch(m)
		switch {
		case sub[3] != num:
			return m
		case sub[2] != "":
			if r, err := issue.ParseRepo(sub[2]); err != nil || !ref.Same(issue.Ref{Repo: r, Number: ref.Number}) {
				return m
			}
		}
		if sub[1] != "" {
			return "issue"
		}
		return "it"
	})
}

// failGroup is the issues that failed for one reason.
type failGroup struct {
	key, why string // the reason without naming the issue, and with it
	refs     []issue.Ref
	err      error // of the first issue
	code     render.Code
}

// reason is the group's reason in the summary.
func (g *failGroup) reason() string {
	if len(g.refs) > 1 {
		return g.key
	}
	return g.why
}

// next is the group's advice, the line after the first of its error
// ("ask the user how to proceed"), and its fix.
func (g *failGroup) next() (advice, fix string) {
	e := render.FromError(g.err)
	if _, a, ok := strings.Cut(e.What, "\n"); ok {
		advice = strings.TrimSpace(a)
		if len(g.refs) > 1 {
			advice = generalReason(advice, g.refs[0])
		}
	}
	return advice, strings.TrimSpace(e.Fix)
}

// retryable reports whether a re-run may finish the group's issues:
// network failures, partial or unconfirmed writes, issues not attempted.
func (g *failGroup) retryable() bool {
	return g.code == render.ExitNetwork || g.code == render.ExitPartial
}

// Summary ends the run: "N of N done" on Stdout when every issue is done,
// else the error that names each issue not done (see Run). A dry run
// applies nothing: it reports only the issues that cannot be planned.
//
// The error's first line holds one entry per reason ("#17, #18: why"),
// those of the code that decides first, then in the order of the
// arguments. Entries that do not fit are named after "also not done", as
// many as fit, the rest counted ("and 5 more"). Then the next steps, at
// most two lines: the advice of each kind of failure ("ask the user how
// to proceed"), and for the issues a re-run may finish, "re-run the same
// command to retry #21", whatever the code; the fix line is the first
// failure's that has one.
func (b *Run) Summary(items []*Item, dry bool) error {
	var failed []*Item
	written := 0 // issues done with a write
	for _, it := range items {
		switch {
		case it.Err != nil:
			failed = append(failed, it)
		case it.Wrote():
			written++
		}
	}
	done := len(items) - len(failed)
	head := fmt.Sprintf("%d of %d done", done, len(items))
	if written < done {
		head += fmt.Sprintf(" (%d written)", written)
	}
	if dry {
		head = fmt.Sprintf("dry run: %d of %d can be applied", done, len(items))
	}
	if len(failed) == 0 {
		if !dry && !b.JSON {
			fmt.Fprintln(b.Stdout, head)
		}
		return nil
	}
	wrote := slices.ContainsFunc(items, (*Item).Wrote)
	groups := failGroups(failed, codeOrder(wrote))
	out := &render.Error{Code: batchCode(failed, wrote), What: summaryLine(head, groups, b.def)}

	// Next steps: the advice of each kind, the retry of the issues a re-run
	// may finish, and the first fix.
	var advice []string
	var retry []issue.Ref
	retryFix := ""
	for _, g := range groups {
		a, fix := g.next()
		if a != "" && !slices.Contains(advice, a) && render.RuneLen(strings.Join(append(advice, a), "; ")) <= MaxSummaryRunes {
			advice = append(advice, a)
		}
		if out.Fix == "" {
			out.Fix = fix
		}
		if g.retryable() {
			retry = append(retry, g.refs...)
			if retryFix == "" {
				retryFix = fix
			}
		}
	}
	var next []string
	if len(advice) > 0 {
		next = append(next, strings.Join(advice, "; "))
	}
	if len(retry) > 0 {
		slices.SortFunc(retry, issue.CompareRefs(b.def))
		lead, tail := "re-run the same command to retry ", ""
		if retryFix != "" && retryFix != out.Fix {
			lead = retryFix + ", then " + lead
		}
		if !dry {
			tail = ": it skips the issues done"
		}
		hint := lead + refsWithin(retry, MaxSummaryRunes-render.RuneLen(lead+tail), b.def) + tail
		if out.Code == render.ExitNetwork || out.Code == render.ExitPartial {
			next = append([]string{hint}, next...) // the step of the code first
		} else {
			next = append(next, hint)
		}
	}
	for _, l := range next {
		out.What += "\n" + l
	}
	return out
}

// failGroups sorts the failed issues into one group per reason, worded
// without their numbers (generalReason), those whose code comes first in
// order first, else in the order of the arguments.
func failGroups(failed []*Item, order []render.Code) []*failGroup {
	var groups []*failGroup
	for _, it := range failed {
		key := generalReason(it.Why, it.Ref)
		i := slices.IndexFunc(groups, func(g *failGroup) bool { return g.key == key })
		if i < 0 {
			groups = append(groups, &failGroup{key: key, why: it.Why, err: it.Err, code: render.FromError(it.Err).Code})
			i = len(groups) - 1
		}
		groups[i].refs = append(groups[i].refs, it.Ref)
	}
	rank := func(c render.Code) int {
		if i := slices.Index(order, c); i >= 0 {
			return i
		}
		return len(order)
	}
	slices.SortStableFunc(groups, func(a, b *failGroup) int { return rank(a.code) - rank(b.code) })
	return groups
}

// summaryLine is the first line of the summary error: head, then the
// entry of each group while it fits in MaxSummaryRunes with room left to
// name the issues after it; their issues follow "also not done", as many
// as fit, the rest counted. The first entry is always given: when it is
// too long, its issues are named as few as needed (at least one, the rest
// counted), then its reason is cut, leaving room to name a few issues
// after it.
func summaryLine(head string, groups []*failGroup, def issue.Repo) string {
	line := head
	for i, g := range groups {
		rest := groupRefs(groups[i+1:], def)
		why := g.reason()
		if entry := "; " + issue.RefNames(g.refs, def) + ": " + why; render.RuneLen(line+entry) <= MaxSummaryRunes-tailRunes(rest, 1, def) {
			line += entry
			continue
		}
		if i > 0 {
			return line + alsoNotDone(groupRefs(groups[i:], def), MaxSummaryRunes-render.RuneLen(line), def)
		}
		room := MaxSummaryRunes - render.RuneLen(line) - tailRunes(rest, 3, def)
		refs := refsWithin(g.refs, room-render.RuneLen("; : "+why), def)
		line += "; " + refs + ": " + render.Clip(why, max(room-render.RuneLen("; "+refs+": "), 2))
		return line + alsoNotDone(rest, MaxSummaryRunes-render.RuneLen(line), def)
	}
	return line
}

const alsoNotDonePrefix = "; also not done: "

// alsoNotDone names refs after the entries of the summary line, within
// budget runes; "" for none.
func alsoNotDone(refs []issue.Ref, budget int, def issue.Repo) string {
	if len(refs) == 0 {
		return ""
	}
	return alsoNotDonePrefix + refsWithin(refs, budget-render.RuneLen(alsoNotDonePrefix), def)
}

// tailRunes is the length of alsoNotDone of refs naming up to k of them.
func tailRunes(refs []issue.Ref, k int, def issue.Repo) int {
	if len(refs) == 0 {
		return 0
	}
	k = min(k, len(refs))
	return render.RuneLen(alsoNotDonePrefix + refsText(refs[:k], len(refs)-k, def))
}

// groupRefs lists the issues of groups in order (see issue.CompareRefs).
func groupRefs(groups []*failGroup, def issue.Repo) []issue.Ref {
	var refs []issue.Ref
	for _, g := range groups {
		refs = append(refs, g.refs...)
	}
	slices.SortFunc(refs, issue.CompareRefs(def))
	return refs
}

// refsWithin names refs as issue.RefNames does, as many as fit in budget
// runes (at least one), the rest counted: "#4, #5 and 12 more".
func refsWithin(refs []issue.Ref, budget int, def issue.Repo) string {
	for k := min(len(refs), issue.MaxRefNames); k > 1; k-- {
		if s := refsText(refs[:k], len(refs)-k, def); render.RuneLen(s) <= budget {
			return s
		}
	}
	return refsText(refs[:1], len(refs)-1, def)
}

// refsText is "#1, #2", then " and N more" for more issues not named.
func refsText(shown []issue.Ref, more int, def issue.Repo) string {
	s := issue.RefNames(shown, def)
	if more > 0 {
		s += fmt.Sprintf(" and %d more", more)
	}
	return s
}

// codeOrder is the order in which the codes of failed issues decide the
// code of a batch run (batchCode), wrote when it applied anything.
func codeOrder(wrote bool) []render.Code {
	if wrote {
		return []render.Code{render.ExitPartial, render.ExitNetwork, render.ExitDenied, render.ExitUsage, render.ExitNotFound}
	}
	return []render.Code{render.ExitDenied, render.ExitUsage, render.ExitNetwork, render.ExitNotFound, render.ExitPartial}
}

// batchCode is the exit code of a batch run with failed issues, wrote
// when it applied anything:
//
//   - 5 when it applied something and an issue failed in a way a re-run
//     may finish (network, an unconfirmed or partial write, not
//     attempted): re-run it;
//   - otherwise the code of the failures, the first of 2 (a process
//     refusal: ask the user), 1 (bad arguments), 4 (network, nothing
//     applied: a re-run may finish), 3 (not found).
//
// 0, every issue done, has no failed issue. Whatever the code, the summary
// names the issues a re-run may finish.
func batchCode(failed []*Item, wrote bool) render.Code {
	codes := map[render.Code]bool{}
	for _, it := range failed {
		codes[render.FromError(it.Err).Code] = true
	}
	for _, c := range codeOrder(wrote) {
		if !codes[c] {
			continue
		}
		if c == render.ExitNetwork && wrote {
			return render.ExitPartial
		}
		return c
	}
	return render.ExitPartial
}
