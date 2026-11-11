package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/effects"
	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/spf13/cobra"
)

// gh dojo block and unblock add and remove blocked-by links without
// changing the status.
//
// Requests: one read (issue.ReadLinks: the issue, and each --by issue with
// the issues it blocks) and one mutation with an aliased addBlockedBy or
// removeBlockedBy per link to change; none when every link is already as
// asked. The board is not read. A repeat is safe: a link already there
// (block) or already gone (unblock) is skipped, and the line says so.

func init() {
	register(func() (*cobra.Command, error) { return newLinkCmd(true), nil })
	register(func() (*cobra.Command, error) { return newLinkCmd(false), nil })
}

// byFlag is the flag of block and unblock that names the blockers.
const byFlag = "by"

func newLinkCmd(add bool) *cobra.Command {
	var bys []string
	name, short, long := "block", "Mark an issue blocked by other issues, without changing its status",
		`Mark issue N blocked by the issues --by names, without changing its status.

--by takes 12, "#12", owner/repo#12 or the issue's URL; repeat it, or
separate them with commas. A link already there is not an error: the line
says "already". An issue that does not exist, or a pull request, fails
before anything is written (exit code 3); so does, with exit code 1, a
blocker that N already blocks: GitHub refuses cycles.

A blocker stops blocking once it is closed or in a resolved status
(statuses.resolved); a verb with blockers = "warn" or "deny" checks that,
and gh dojo show lists the unresolved blockers first.`
	example := `  gh dojo block 42 --by 17
  gh dojo block 42 --by 17,18 --by acme/api#7`
	if !add {
		name, short, long = "unblock", "Remove blocked-by links of an issue, without changing its status",
			`Remove the blocked-by links between issue N and the issues --by names,
without changing its status.

--by takes 12, "#12", owner/repo#12 or the issue's URL; repeat it, or
separate them with commas. A link already gone is not an error: the line
says "already". An issue that does not exist fails before anything is
written (exit code 3).`
		example = `  gh dojo unblock 42 --by 17`
	}
	cmd := &cobra.Command{
		Use:     name + " <N> --by <M>...",
		Short:   short,
		Long:    long,
		Example: example,
		Args:    issueArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			n, _ := parseIssue(args[0]) // validated by issueArg
			line, err := runLinks(cmd, add, n, bys)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), line)
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&bys, byFlag, nil, "the blocking `issue`: 12, \"#12\", owner/repo#12 or its URL; repeatable, or comma-separated")
	return cmd
}

// splitRefs splits flag values naming issues, repeated or comma-separated,
// into the issue references (see issue.ParseRef), duplicates dropped, and
// the parts that are not one, trimmed. Empty parts, as in "12,", are
// skipped.
func splitRefs(values []string, def issue.Repo) (refs []issue.Ref, text []string) {
	for _, v := range values {
		for _, s := range strings.Split(v, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			ref, ok := issue.ParseRef(s, def)
			switch {
			case !ok:
				text = append(text, s)
			case !slices.ContainsFunc(refs, ref.Same):
				refs = append(refs, ref)
			}
		}
	}
	return refs, text
}

// parseBlockers parses the values of a flag (--by, --blocked-by) naming
// blocking issues: repeated, or comma-separated (see splitRefs). n itself
// (in def) is refused: an issue cannot block itself (n 0 skips that
// check). Errors are exit code 1 with help as the fix; none means no
// issue was named.
func parseBlockers(values []string, flag string, def issue.Repo, n int, help string) ([]issue.Ref, error) {
	refs, text := splitRefs(values, def)
	if len(text) > 0 {
		return nil, render.Usage("--%s %q is not an issue: pass %s", flag, text[0], issue.RefExamples).
			WithFix(help)
	}
	for _, ref := range refs {
		if n > 0 && ref.Same(issue.Ref{Repo: def, Number: n}) {
			return nil, selfBlock(n, "--"+flag)
		}
	}
	return refs, nil
}

// runLinks adds (block) or removes (unblock) the blocked-by links between
// issue n and the issues bys names, and returns the confirmation line:
//
//	#42 blocked by #17 and acme/api#7, already blocked by #18
//	#42 no longer blocked by #17, already not blocked by #18
func runLinks(cmd *cobra.Command, add bool, n int, bys []string) (string, error) {
	help := cmd.CommandPath() + " --help"
	noBy := func() error {
		return render.Usage("%s needs --%s with the blocking issue, like: gh dojo %s %d --%s 17",
			cmd.Name(), byFlag, cmd.Name(), n, byFlag).WithFix(help)
	}
	if len(bys) == 0 {
		return "", noBy()
	}
	ctx := cmd.Context()
	a := newApp()
	repo, err := a.Repo()
	if err != nil {
		return "", err
	}
	refs, err := parseBlockers(bys, byFlag, repo, n, help)
	if err != nil {
		return "", err
	}
	if len(refs) == 0 {
		return "", noBy() // --by "" or --by ","
	}
	target, links, err := issue.ReadLinks(ctx, a.Client, repo, n, refs, "--"+byFlag)
	if err != nil {
		return "", err
	}
	var change, same []*issue.Linked
	for _, l := range links {
		if l.ID == target.ID {
			return "", selfBlock(n, "--"+byFlag)
		}
		if target.BlockedBy(l) == add {
			same = append(same, l)
		} else {
			change = append(change, l)
		}
	}
	for _, l := range change {
		if add && l.BlockedBy(target.ID) {
			return "", cycleError(n, l.Ref.String(repo), "--"+byFlag)
		}
	}
	line := func(done []*issue.Linked) string { return linksConfirm(add, n, repo, done, same) }
	if len(change) == 0 {
		return line(nil), nil
	}

	vars := map[string]any{"issue": target.ID}
	for i, l := range change {
		vars[linkAlias(i)] = l.ID
	}
	var data map[string]json.RawMessage
	err = a.Client.Mutate(ctx, linksMutation(add, len(change)), vars, &data)
	var done, missed []*issue.Linked
	for i, l := range change {
		if raw, ok := data[linkAlias(i)]; ok && !isJSONNull(raw) {
			done = append(done, l)
		} else {
			missed = append(missed, l)
		}
	}
	switch {
	case len(missed) == 0 && err != nil:
		render.Warn(cmd.ErrOrStderr(), "GitHub reported an error after applying every change: %s", reason(err))
	case len(missed) == 0:
	case graph.IsCycle(err):
		// A cycle through other issues: re-running cannot help.
		what := "GitHub refused to mark #" + strconv.Itoa(n) + " blocked by " + joinRefs(missed, repo) + ": it would make a dependency cycle"
		if len(done) > 0 {
			what = line(done) + "; " + what
		}
		return "", render.Usage("%s", what).WithFix(fmt.Sprintf("gh dojo show %d", n)).WithCause(err)
	case len(done) == 0 && err != nil:
		return "", err // a lost answer says "may have been applied, re-run": a re-run skips what is done
	case len(done) == 0:
		return "", render.Network("GitHub returned no result for the write").WithFix("re-run the same command")
	default:
		what := "not blocked by "
		if !add {
			what = "still blocked by "
		}
		why, fix, next := "GitHub returned no result for it", "", "re-run the same command"
		if err != nil {
			why, fix = reason(err), strings.TrimSpace(render.FromError(err).Fix)
		}
		if fix != "" {
			next = fix + ", then " + next
		}
		return "", render.Partial("%s; %s%s: %s; %s", line(done), what, joinRefs(missed, repo), why, next).
			WithFix(fix).WithCause(err)
	}
	return line(done), nil
}

// linkAlias is the alias of the i-th link field of a mutation, and the
// name of the variable with the blocking issue's ID.
func linkAlias(i int) string { return fmt.Sprintf("link%d", i) }

// linksMutation adds (add) or removes n blocked-by links of $issue, one
// aliased field each:
//
//	mutation Block($issue: ID!, $link0: ID!) {
//	  link0: addBlockedBy(input: {issueId: $issue, blockingIssueId: $link0}) { issue { id } }
//	}
func linksMutation(add bool, n int) string {
	op := "Block"
	if !add {
		op = "Unblock"
	}
	decls := []string{"$issue: ID!"}
	var fields []string
	for i := range n {
		decls = append(decls, "$"+linkAlias(i)+": ID!")
		fields = append(fields, "  "+linkAlias(i)+": "+effects.BlockedByField(add, "issue", linkAlias(i)))
	}
	return "mutation " + op + "(" + strings.Join(decls, ", ") + ") {\n" + strings.Join(fields, "\n") + "\n}"
}

// linksConfirm is the line of block (add) or unblock for issue n: the
// links changed (done), then those found as asked already (same).
func linksConfirm(add bool, n int, repo issue.Repo, done, same []*issue.Linked) string {
	var parts []string
	switch {
	case len(done) > 0 && add:
		parts = append(parts, "blocked by "+joinRefs(done, repo))
	case len(done) > 0:
		parts = append(parts, "no longer blocked by "+joinRefs(done, repo))
	}
	switch {
	case len(same) > 0 && add:
		parts = append(parts, "already blocked by "+joinRefs(same, repo))
	case len(same) > 0:
		parts = append(parts, "already not blocked by "+joinRefs(same, repo))
	}
	return render.Confirm(fmt.Sprintf("#%d", n), "", parts...)
}

// joinRefs lists linked issues as "#12", "#12 and #13", "#12, #13 and #14".
func joinRefs(links []*issue.Linked, repo issue.Repo) string {
	refs := make([]string, len(links))
	for i, l := range links {
		refs[i] = l.Ref.String(repo)
	}
	if len(refs) <= 1 {
		return strings.Join(refs, "")
	}
	return strings.Join(refs[:len(refs)-1], ", ") + " and " + refs[len(refs)-1]
}

func isJSONNull(v json.RawMessage) bool {
	s := strings.TrimSpace(string(v))
	return s == "" || s == "null"
}
