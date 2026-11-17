package main

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

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
	sprint   string
	mine     bool
	all      bool
	limit    int

	// want are the statuses --status selects, "" for none; set by
	// checkStatuses.
	want []string
	// sprintIt is the iteration --sprint selects, nil for none (no
	// sprint); set by checkSprintFlag.
	sprintIt *board.Iteration
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
	if o.sprint != "" {
		names = append(names, "--sprint")
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
			"Issues in a resolved status that is not final (statuses.resolved, such as\n" +
			"Ready: done, waiting for a release) are one line of numbers; --status\n" +
			"naming that status, or --all, lists them with their titles.\n" +
			"Filters combine with AND; the issues they leave out are counted. Only\n" +
			"issues are listed: draft issues and pull requests on the board are counted.",
		Example: `  gh dojo list
  gh dojo list --status "In progress" --mine
  gh dojo list --status none
  gh dojo list --sprint current
  gh dojo list --label bug --label area:cli --limit 100`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runList(cmd, &o) },
	}
	f := cmd.Flags()
	f.StringArrayVar(&o.statuses, "status", nil, `only issues in this status ("none": without a status); repeat for several`)
	o.filterFlags(f)
	f.BoolVar(&o.all, "all", false, "also show closed issues and issues in a final status")
	f.IntVar(&o.limit, "limit", defaultListLimit, "print at most this many issues")
	return cmd, nil
}

// filterFlags adds the filters list and next share: --mine, --sprint and
// --label.
func (o *listOpts) filterFlags(f *pflag.FlagSet) {
	f.BoolVar(&o.mine, "mine", false, "only issues assigned to you")
	f.StringVar(&o.sprint, "sprint", "", `only issues in this sprint (fields.sprint): "current" (today, in board.timezone), a title, or "none"`)
	f.StringArrayVar(&o.labels, "label", nil, "only issues with this label; repeat to require several")
}

// noArgs rejects the arguments of list and next; an issue number points
// at show.
func noArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	if ref, ok := parseRef(args[0]); ok {
		return render.Usage("%s takes no arguments; to see one issue: gh dojo show %s", cmd.Name(), ref.Arg(issue.Repo{}))
	}
	return render.Usage("%s takes no arguments", cmd.Name()).WithFix(cmd.CommandPath() + " --help")
}

// checkLimit rejects a --limit below 1.
func checkLimit(cmd *cobra.Command, limit int) error {
	if limit < 1 {
		return render.Usage("invalid --limit %d: want 1 or greater", limit).WithFix(cmd.CommandPath() + " --help")
	}
	return nil
}

// refRepo is the default repository, which shortens refs to "#12" and
// spells them as the config does; "" when it is unknown, as list and next
// work without it.
func refRepo(a *App) string {
	if r, err := a.Repo(); err == nil {
		return r.String()
	}
	return ""
}

func runList(cmd *cobra.Command, o *listOpts) error {
	if err := checkLimit(cmd, o.limit); err != nil {
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	a := newApp()
	rb, err := readBoard(cmd, a, cfg, o, false)
	if err != nil {
		return err
	}
	field, items := rb.field, rb.items
	warnings := readWarnings(cmd, items, items.Incomplete)
	repo := refRepo(a)
	l := buildList(cfg, field.OptionNames(), items, o)
	res := l.result(o, items)
	res.Warnings = render.OrEmpty(warnings)
	return emit(cmd, res, func(w io.Writer) { l.write(w, repo, cfg, o) })
}

// readBoard reads the board's items for list and next: the schema, the
// --status and --sprint of o checked against it, every item in board order
// (with its sprint for --sprint, and its blockers when blockers is set),
// then --label checked against the items. See readWarnings for the
// warnings about the read.
//
// A cached schema may be out of date: a status added or renamed since, or
// the status field renamed. It is re-read once, which also surfaces the
// config error when the config no longer matches the board.
func readBoard(cmd *cobra.Command, a *App, cfg *config.Config, o *listOpts, blockers bool) (*boardRead, error) {
	ctx := cmd.Context()
	s, err := a.Schema(ctx)
	if err != nil {
		return nil, err
	}
	refreshed := false
	refresh := func() (*board.Field, error) {
		refreshed = true
		if err := a.ResetSchema(); err != nil {
			return nil, err
		}
		if s, err = a.Schema(ctx); err != nil {
			return nil, err
		}
		return checkFilters(s, cfg, o, a.Now())
	}
	field, err := checkFilters(s, cfg, o, a.Now())
	if err != nil && board.IsUnknown(err) && s.Cached {
		field, err = refresh()
	}
	if err != nil {
		return nil, err
	}
	opts := board.ItemsOptions{Blockers: blockers}
	if o.sprint != "" {
		opts.Sprint = cfg.SprintField()
	}
	items, err := board.ReadItemsWith(ctx, a.Client, s, cfg.StatusField(), opts)
	if err != nil {
		return nil, err
	}
	if !refreshed && s.Cached && staleStatus(field, items) {
		if field, err = refresh(); err != nil {
			return nil, err
		}
	}
	if err := checkLabels(items, o.labels); err != nil {
		return nil, err
	}
	if def := refRepo(a); def != "" {
		spellDefault(items, def)
	}
	return &boardRead{schema: s, field: field, items: items}, nil
}

// spellDefault names the default repository def as the config spells it
// in the items and their blockers, as show and the verbs do; GitHub's
// spelling may differ in case (gh dojo doctor warns then).
func spellDefault(items *board.Items, def string) {
	for i := range items.Items {
		it := &items.Items[i]
		if strings.EqualFold(it.Repo, def) {
			it.Repo = def
		}
		for j := range it.BlockedBy.Nodes {
			if b := &it.BlockedBy.Nodes[j]; strings.EqualFold(b.Repo, def) {
				b.Repo = def
			}
		}
	}
}

// boardRead is the board as readBoard read it.
type boardRead struct {
	schema *board.Schema
	field  *board.Field // the status field
	items  *board.Items
}

// readWarnings prints on stderr, and returns for the JSON, the warnings
// about a read of the board's items: incomplete says GitHub withheld parts
// of the items the command shows (or counts), and the board may have
// changed between pages.
func readWarnings(cmd *cobra.Command, items *board.Items, incomplete bool) []string {
	var warnings []string
	if incomplete {
		warnings = append(warnings, "GitHub withheld parts of some board items from this token; their lines may be incomplete")
	}
	if len(items.Items) != items.TotalCount {
		warnings = append(warnings, fmt.Sprintf("the board changed while it was read (%d items read, GitHub reports %d); run again for an exact list",
			len(items.Items), items.TotalCount))
	}
	for _, w := range warnings {
		render.Warn(cmd.ErrOrStderr(), "%s", w)
	}
	return warnings
}

// listResult is list's JSON: the issues printed, in the same order and
// cut to --limit, and the counts of those left out.
type listResult struct {
	// SprintFilter is null without --sprint, "none" for --sprint none
	// (issues without a sprint), else "iteration": the issues in Sprint.
	SprintFilter *string `json:"sprint_filter"`
	// Sprint is the iteration --sprint selects, null without --sprint or
	// for --sprint none.
	Sprint  *jsonIteration `json:"sprint"`
	Issues  []listIssue    `json:"issues"`
	Matched int            `json:"matched"` // issues that pass the filters; more than len(issues) when cut by --limit
	// Unchecked counts issues listed though a --label may be among their
	// labels not read.
	Unchecked int         `json:"unchecked"`
	Hidden    int         `json:"hidden"`   // closed or in a final status: --all shows them
	Filtered  int         `json:"filtered"` // not matching the filters
	Skipped   skippedJSON `json:"skipped"`
	// Incomplete: GitHub withheld parts of some items (warned on stderr).
	Incomplete bool `json:"incomplete"`
	// Warnings are also printed on stderr.
	Warnings []string `json:"warnings"`
}

// skippedJSON counts the items list and next skip, as they show issues
// only.
type skippedJSON struct {
	DraftIssues  int `json:"draft_issues"`
	PullRequests int `json:"pull_requests"`
	Unseen       int `json:"unseen"` // items the token cannot see
}

// sprintFilterJSON is the sprint_filter of list and next for o: see
// listResult.SprintFilter.
func sprintFilterJSON(o *listOpts) *string {
	switch {
	case o.sprintIt != nil:
		return render.OrNull("iteration")
	case o.sprint != "":
		return render.OrNull(sprintNone)
	}
	return nil
}

type listIssue struct {
	Repo           string   `json:"repo"`
	Number         int      `json:"number"`
	Title          string   `json:"title"`
	State          string   `json:"state"`  // open or closed
	Status         *string  `json:"status"` // null for no status
	Labels         []string `json:"labels"`
	LabelsTotal    int      `json:"labels_total"`
	Assignees      []string `json:"assignees"`
	AssigneesTotal int      `json:"assignees_total"`
}

// result is the list as JSON, cut to o.limit as write cuts it.
func (l *list) result(o *listOpts, items *board.Items) listResult {
	r := listResult{
		SprintFilter: sprintFilterJSON(o), Sprint: iterationJSON(o.sprintIt), Issues: []listIssue{}, Matched: l.shown,
		Unchecked: l.unchecked, Hidden: l.hidden, Filtered: l.filtered, Skipped: l.skippedJSON(),
		Incomplete: items.Incomplete, Warnings: []string{},
	}
	for _, g := range l.groups {
		for _, it := range g.items {
			if len(r.Issues) == o.limit {
				return r
			}
			r.Issues = append(r.Issues, listIssueOf(it))
		}
	}
	return r
}

// listIssueOf is an issue of the board as list's JSON has it.
func listIssueOf(it board.Item) listIssue {
	return listIssue{
		Repo: it.Repo, Number: it.Number, Title: render.SingleLine(it.Title), State: strings.ToLower(it.State),
		Status: render.OrNull(it.Status), Labels: render.OrEmpty(it.Labels.Nodes), LabelsTotal: max(it.Labels.TotalCount, len(it.Labels.Nodes)),
		Assignees: render.OrEmpty(it.Assignees.Nodes), AssigneesTotal: max(it.Assignees.TotalCount, len(it.Assignees.Nodes)),
	}
}

// checkFilters validates --status and --sprint against the board (see
// checkStatuses and checkSprintFlag) and returns the status field.
func checkFilters(s *board.Schema, cfg *config.Config, o *listOpts, now time.Time) (*board.Field, error) {
	f, err := checkStatuses(s, cfg, o)
	if err != nil {
		return nil, err
	}
	return f, checkSprintFlag(s, cfg, o, now)
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

// leftOut counts what list and next leave out, and the issues they show
// though a --label may be among their labels not read.
type leftOut struct {
	unchecked int // shown, but a --label may be among labels not read
	hidden    int // --all shows them
	filtered  int // don't match the filters
	drafts    int
	pulls     int
	unseen    int // items the token cannot see
}

// skip counts an item that is not an issue and reports whether it is not:
// list and next show issues only.
func (c *leftOut) skip(it board.Item) bool {
	switch it.Type {
	case board.ContentIssue:
		return false
	case board.ContentDraftIssue:
		c.drafts++
	case board.ContentPullRequest:
		c.pulls++
	default:
		c.unseen++
	}
	return true
}

func (c *leftOut) skippedJSON() skippedJSON {
	return skippedJSON{DraftIssues: c.drafts, PullRequests: c.pulls, Unseen: c.unseen}
}

// write prints a line for each kind of issue left out (principle 7). hidden
// says what the hidden issues are; cmd is the command's name.
func (c *leftOut) write(w io.Writer, cmd, hidden string, o *listOpts) {
	if c.unchecked > 0 {
		fmt.Fprintf(w, "%s not fully checked: more than %d labels\n",
			render.Plural(c.unchecked, "issue", "issues"), board.LabelsWindow)
	}
	if c.hidden > 0 {
		fmt.Fprintf(w, "hidden: %d %s, use --all\n", c.hidden, hidden)
	}
	if c.filtered > 0 {
		fmt.Fprintf(w, "filtered out: %s not matching %s\n",
			render.Plural(c.filtered, "issue", "issues"), strings.Join(o.filterNames(), " "))
	}
	var skipped []string
	if c.drafts > 0 {
		skipped = append(skipped, render.Plural(c.drafts, "draft issue", "draft issues"))
	}
	if c.pulls > 0 {
		skipped = append(skipped, render.Plural(c.pulls, "pull request", "pull requests"))
	}
	if c.unseen > 0 {
		skipped = append(skipped, render.Plural(c.unseen, "item the token cannot see", "items the token cannot see"))
	}
	if len(skipped) > 0 {
		fmt.Fprintf(w, "skipped: %s (%s shows issues only)\n", strings.Join(skipped, ", "), cmd)
	}
}

// writeSprint prints the sprint --sprint selects, the first line of list
// and next.
func writeSprint(w io.Writer, o *listOpts) {
	switch {
	case o.sprint == "":
	case o.sprintIt == nil:
		fmt.Fprintln(w, "sprint: none")
	default:
		fmt.Fprintln(w, "sprint: "+o.sprintIt.Span())
	}
}

// list is what list prints: the groups and the counts of what is left out.
// Every item on the board is in exactly one of shown, hidden (matching the
// filters but closed or in a final status), filtered, drafts, pulls and
// unseen.
type list struct {
	leftOut
	groups []*listGroup
	shown  int // issues in groups
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
		if l.skip(it) {
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
	if o.sprint != "" && (o.sprintIt == nil && it.SprintID != "" || o.sprintIt != nil && it.SprintID != o.sprintIt.ID) {
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
	writeSprint(w, o)
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
		if l.folded(g, cfg, o) {
			refs := make([]string, n)
			for i, it := range g.items[:n] {
				refs[i] = itemRef(it, repo)
			}
			fmt.Fprintf(w, "%s (%d; titles: --status %s): %s\n", statusName(g.status), len(g.items),
				statusArg(g.status), strings.Join(refs, " "))
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
	// With --status, final statuses not asked for are filtered out, so only
	// closed issues can be hidden.
	hidden := "closed"
	if len(cfg.Statuses.Final) > 0 && len(o.statuses) == 0 {
		hidden += " or " + strings.Join(cfg.Statuses.Final, "/")
	}
	l.leftOut.write(w, "list", hidden, o)
}

// folded reports whether group g prints as one line of numbers: its status
// is resolved but not final (done, its issues waiting for a release), and
// neither --status nor --all asks for it.
func (l *list) folded(g *listGroup, cfg *config.Config, o *listOpts) bool {
	return g.status != "" && !o.all && slices.Contains(cfg.Statuses.Resolved, g.status) &&
		!slices.Contains(cfg.Statuses.Final, g.status) && !slices.Contains(o.want, g.status)
}

// statusArg is status s as a --status value, quoted when it has a space.
func statusArg(s string) string {
	if strings.ContainsAny(s, " \t\"'") {
		return strconv.Quote(s)
	}
	return s
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
	b.WriteString(itemRef(it, repo) + " ")
	b.WriteString(render.SingleLine(it.Title))
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

// itemRef names an issue: "#12", owner/name#12 outside the default
// repository.
func itemRef(it board.Item, repo string) string {
	if !strings.EqualFold(it.Repo, repo) || repo == "" {
		return it.Repo + "#" + strconv.Itoa(it.Number)
	}
	return "#" + strconv.Itoa(it.Number)
}
