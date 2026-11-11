package main

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

func init() { register(newListCmd) }

const (
	// defaultListLimit is how many issues list prints without --limit.
	defaultListLimit = 50
	// noStatus is the --status value for issues without a status.
	noStatus = "none"
	// maxLineLabels is how many labels an issue line shows before "+N more".
	maxLineLabels = 5
)

// listOpts are the flags of list.
type listOpts struct {
	statuses []string
	labels   []string
	mine     bool
	all      bool
	limit    int

	// want are the statuses --status selects, "" for none; set by
	// checkStatuses.
	want []string
}

// filterNames lists the filter flags in use, for the filtered-out note.
func (o *listOpts) filterNames() []string {
	var names []string
	if len(o.statuses) > 0 {
		names = append(names, "--status")
	}
	if o.mine {
		names = append(names, "--mine")
	}
	if len(o.labels) > 0 {
		names = append(names, "--label")
	}
	return names
}

func newListCmd() (*cobra.Command, error) {
	var o listOpts
	cmd := &cobra.Command{
		Use:   "list",
		Short: "See what is on the board: one line per open issue, grouped by status",
		Long: "Use it to see what is on the board: one line per open issue, grouped by status\n" +
			"in board order, issues without a status first.\n\n" +
			"Closed issues and issues in a final status (statuses.final) are hidden and\n" +
			"counted; --all shows them, and so does --status naming a final status.\n" +
			"Filters combine with AND; the issues they leave out are counted. Only\n" +
			"issues are listed: draft issues and pull requests on the board are counted.",
		Example: `  gh dojo list
  gh dojo list --status "In progress" --mine
  gh dojo list --status none
  gh dojo list --label bug --label area:cli --limit 100`,
		Args: listArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runList(cmd, &o) },
	}
	f := cmd.Flags()
	f.StringArrayVar(&o.statuses, "status", nil, `only issues in this status ("none": without a status); repeat for several`)
	f.BoolVar(&o.mine, "mine", false, "only issues assigned to you")
	f.StringArrayVar(&o.labels, "label", nil, "only issues with this label; repeat to require several")
	f.BoolVar(&o.all, "all", false, "also show closed issues and issues in a final status")
	f.IntVar(&o.limit, "limit", defaultListLimit, "print at most this many issues")
	return cmd, nil
}

// listArgs rejects arguments; an issue number points at show.
func listArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	if n, ok := parseIssue(args[0]); ok {
		return render.Usage("list takes no arguments; to see one issue: gh dojo show %d", n)
	}
	return render.Usage("list takes no arguments").WithFix(cmd.CommandPath() + " --help")
}

func runList(cmd *cobra.Command, o *listOpts) error {
	if o.limit < 1 {
		return render.Usage("invalid --limit %d: want 1 or greater", o.limit).WithFix(cmd.CommandPath() + " --help")
	}
	ctx := cmd.Context()
	a := newApp()
	cfg, err := a.Config()
	if err != nil {
		return err
	}
	s, err := a.Schema(ctx)
	if err != nil {
		return err
	}
	// A cached schema may be out of date: a status added or renamed since,
	// or the status field renamed. It is re-read once, which also surfaces
	// the config error when the config no longer matches the board.
	refreshed := false
	refresh := func() (*board.Field, error) {
		refreshed = true
		if err := a.ResetSchema(); err != nil {
			return nil, err
		}
		if s, err = a.Schema(ctx); err != nil {
			return nil, err
		}
		return checkStatuses(s, cfg, o)
	}
	field, err := checkStatuses(s, cfg, o)
	if err != nil && board.IsUnknown(err) && s.Cached {
		field, err = refresh()
	}
	if err != nil {
		return err
	}
	items, err := board.ReadItems(ctx, a.Client, s, cfg.StatusField())
	if err != nil {
		return err
	}
	if !refreshed && s.Cached && staleStatus(field, items) {
		if field, err = refresh(); err != nil {
			return err
		}
	}
	if err := checkLabels(items, o.labels); err != nil {
		return err
	}
	stderr := cmd.ErrOrStderr()
	if items.Incomplete {
		render.Warn(stderr, "GitHub withheld parts of some board items from this token; their lines may be incomplete")
	}
	if len(items.Items) != items.TotalCount {
		render.Warn(stderr, "the board changed while it was read (%d items read, GitHub reports %d); run again for an exact list",
			len(items.Items), items.TotalCount)
	}
	repo := ""
	if r, err := a.Repo(); err == nil { // only shortens refs; list works without it
		repo = r.String()
	}
	l := buildList(cfg, field.OptionNames(), items, o)
	l.write(cmd.OutOrStdout(), repo, cfg, o)
	return nil
}

// checkStatuses validates the --status values against the board, sets
// o.want and returns the status field.
func checkStatuses(s *board.Schema, cfg *config.Config, o *listOpts) (*board.Field, error) {
	f, err := s.Field(cfg.StatusField())
	if err != nil {
		return nil, err
	}
	o.want = o.want[:0]
	for _, st := range o.statuses {
		if strings.EqualFold(st, noStatus) && !slices.Contains(f.OptionNames(), st) {
			o.want = append(o.want, "")
			continue
		}
		if _, err := f.Option(st); err != nil {
			var e *render.Error
			if errors.As(err, &e) {
				e.Allowed = append(slices.Clone(e.Allowed), noStatus)
			}
			return nil, err
		}
		o.want = append(o.want, st)
	}
	return f, nil
}

// staleStatus reports whether the items show that the schema's status
// field is out of date: another field ID, or a status it does not have.
func staleStatus(f *board.Field, items *board.Items) bool {
	if items.StatusFieldID != f.ID {
		return true
	}
	options := f.OptionNames()
	for _, it := range items.Items {
		if it.Status != "" && !slices.Contains(options, it.Status) {
			return true
		}
	}
	return false
}

// checkLabels fails on a --label that no issue on the board has, closed
// ones included, suggesting a label that some issue does have. An issue with
// more labels than the window read may have it, so then nothing is checked.
func checkLabels(items *board.Items, labels []string) error {
	if len(labels) == 0 {
		return nil
	}
	var known []string
	for _, it := range items.Items {
		if it.Type != board.ContentIssue {
			continue
		}
		if it.Labels.Truncated() {
			return nil
		}
		for _, l := range it.Labels.Nodes {
			if !containsFold(known, l) {
				known = append(known, l)
			}
		}
	}
	for _, want := range labels {
		if containsFold(known, want) {
			continue
		}
		msg := fmt.Sprintf("no issue on the board has label %q", want)
		if s := board.Suggest(want, known); s != "" {
			msg += fmt.Sprintf("; did you mean %q?", s)
		}
		return render.Usage("%s", msg)
	}
	return nil
}

// listGroup is the issues of one status, in board order.
type listGroup struct {
	status string // "" for issues without a status
	items  []board.Item
}

// list is what list prints: the groups and the counts of what is left out.
// Every item on the board is in exactly one of shown, hidden, filtered,
// drafts, pulls and unseen.
type list struct {
	groups    []*listGroup
	shown     int // issues in groups
	unchecked int // shown, but a --label may be among labels not read
	hidden    int // match the filters but closed or in a final status
	filtered  int // don't match the filters
	drafts    int
	pulls     int
	unseen    int // items the token cannot see
}

// buildList sorts the board's items into status groups: issues without a
// status first, then the board's statuses in board order, then statuses the
// board no longer has. Filters apply first, so --all shows exactly the
// hidden ones.
func buildList(cfg *config.Config, options []string, items *board.Items, o *listOpts) *list {
	l := &list{}
	groups := map[string]*listGroup{"": {status: ""}}
	order := []string{""}
	for _, opt := range options {
		groups[opt] = &listGroup{status: opt}
		order = append(order, opt)
	}
	for _, it := range items.Items {
		switch it.Type {
		case board.ContentIssue:
		case board.ContentDraftIssue:
			l.drafts++
			continue
		case board.ContentPullRequest:
			l.pulls++
			continue
		default:
			l.unseen++
			continue
		}
		ok, unsure := o.matches(it, items.Viewer)
		if !ok {
			l.filtered++
			continue
		}
		final := slices.Contains(cfg.Statuses.Final, it.Status)
		if !o.all && (it.State == issue.StateClosed || final) && !(final && slices.Contains(o.want, it.Status)) {
			l.hidden++
			continue
		}
		g := groups[it.Status]
		if g == nil {
			g = &listGroup{status: it.Status}
			groups[it.Status] = g
			order = append(order, it.Status)
		}
		g.items = append(g.items, it)
		l.shown++
		if unsure {
			l.unchecked++
		}
	}
	for _, st := range order {
		if g := groups[st]; len(g.items) > 0 {
			l.groups = append(l.groups, g)
		}
	}
	return l
}

// matches reports whether an issue passes the filters. A --label not among
// the labels read of an issue with more is given the benefit of the doubt:
// the issue matches, unsure. --mine looks at the first 10 assignees.
func (o *listOpts) matches(it board.Item, viewer string) (ok, unsure bool) {
	if len(o.want) > 0 && !slices.Contains(o.want, it.Status) {
		return false, false
	}
	if o.mine && (viewer == "" || !containsFold(it.Assignees.Nodes, viewer)) {
		return false, false
	}
	for _, want := range o.labels {
		if containsFold(it.Labels.Nodes, want) {
			continue
		}
		if !it.Labels.Truncated() {
			return false, false
		}
		unsure = true
	}
	return true, unsure
}

func containsFold(list []string, s string) bool {
	return slices.ContainsFunc(list, func(x string) bool { return strings.EqualFold(x, s) })
}

// write prints the groups, cut to o.limit issues, then a line for each kind
// of issue left out (principle 7).
func (l *list) write(w io.Writer, repo string, cfg *config.Config, o *listOpts) {
	left := o.limit
	var cut []string // groups not printed in full
	for _, g := range l.groups {
		n := min(left, len(g.items))
		left -= n
		if n < len(g.items) {
			cut = append(cut, fmt.Sprintf("%s %d", statusName(g.status), len(g.items)-n))
		}
		if n == 0 {
			continue
		}
		fmt.Fprintf(w, "%s (%d)\n", statusName(g.status), len(g.items))
		for _, it := range g.items[:n] {
			fmt.Fprintln(w, "  "+itemLine(it, repo))
		}
	}
	if l.shown == 0 {
		fmt.Fprintln(w, "no issues to show")
	}
	if note := render.Shown(min(l.shown, o.limit), l.shown, "use --limit "+strconv.Itoa(l.shown)); note != "" {
		fmt.Fprintln(w, note+"; not shown: "+strings.Join(cut, ", "))
	}
	if l.unchecked > 0 {
		fmt.Fprintf(w, "%s not fully checked: more than %d labels\n",
			plural(l.unchecked, "issue", "issues"), board.LabelsWindow)
	}
	if l.hidden > 0 {
		// With --status, final statuses not asked for are filtered out, so
		// only closed issues can be hidden.
		what := "closed"
		if len(cfg.Statuses.Final) > 0 && len(o.statuses) == 0 {
			what += " or " + strings.Join(cfg.Statuses.Final, "/")
		}
		fmt.Fprintf(w, "hidden: %d %s, use --all\n", l.hidden, what)
	}
	if l.filtered > 0 {
		fmt.Fprintf(w, "filtered out: %s not matching %s\n",
			plural(l.filtered, "issue", "issues"), strings.Join(o.filterNames(), " "))
	}
	var skipped []string
	if l.drafts > 0 {
		skipped = append(skipped, plural(l.drafts, "draft issue", "draft issues"))
	}
	if l.pulls > 0 {
		skipped = append(skipped, plural(l.pulls, "pull request", "pull requests"))
	}
	if l.unseen > 0 {
		skipped = append(skipped, plural(l.unseen, "item the token cannot see", "items the token cannot see"))
	}
	if len(skipped) > 0 {
		fmt.Fprintf(w, "skipped: %s (list shows issues only)\n", strings.Join(skipped, ", "))
	}
}

func statusName(s string) string {
	if s == "" {
		return "No status"
	}
	return s
}

// itemLine is one issue: "#12 Title [label, label] @user", with
// owner/name#12 for an issue outside the default repository and at most
// maxLineLabels labels.
func itemLine(it board.Item, repo string) string {
	var b strings.Builder
	if it.Repo != repo || repo == "" {
		b.WriteString(it.Repo)
	}
	b.WriteString("#" + strconv.Itoa(it.Number) + " ")
	b.WriteString(strings.Join(strings.Fields(it.Title), " "))
	if it.State == issue.StateClosed {
		b.WriteString(" (closed)")
	}
	if total := max(it.Labels.TotalCount, len(it.Labels.Nodes)); total > 0 {
		labels := it.Labels.Nodes[:min(len(it.Labels.Nodes), maxLineLabels)]
		if more := total - len(labels); more > 0 {
			labels = append(slices.Clone(labels), fmt.Sprintf("+%d more", more))
		}
		b.WriteString(" [" + strings.Join(labels, ", ") + "]")
	}
	for _, a := range it.Assignees.Nodes {
		b.WriteString(" @" + a)
	}
	if more := it.Assignees.TotalCount - len(it.Assignees.Nodes); more > 0 {
		fmt.Fprintf(&b, " +%d more", more)
	}
	return b.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
