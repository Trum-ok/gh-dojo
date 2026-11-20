package main

import (
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

func init() { register(newNextCmd) }

// defaultNextLimit is how many issues next prints without --limit: enough
// to choose from, short enough for an agent's context.
const defaultNextLimit = 5

func newNextCmd() (*cobra.Command, error) {
	var o listOpts
	cmd := &cobra.Command{
		Use:   "next",
		Short: "Find issues to take: initial status, no unresolved blockers, in board order",
		Long: "Use it to choose what to work on next: one line per open issue in the initial\n" +
			"status (statuses.initial) or without a status, in board order, leaving out\n" +
			"those with an unresolved blocker: open and not on the board in a resolved\n" +
			"status (statuses.resolved), archived or not, the rule of show and start. A\n" +
			"blocker that is not on the board, or whose place could not be read, counts\n" +
			"as unresolved.\n\n" +
			"The blocked issues are counted; --all shows them too, with their blockers.\n" +
			"Filters combine with AND; the issues they leave out are counted. Only\n" +
			"issues are listed. gh dojo list shows the rest of the board.\n\n" +
			"GitHub lists a board's items a few seconds behind writes: an issue just\n" +
			"created or moved may be missing or in its old status. gh dojo show N reads\n" +
			"the issue itself and is up to date at once.",
		Example: `  gh dojo next
  gh dojo next --mine --limit 1
  gh dojo next --label bug
  gh dojo next --sprint current
  gh dojo next --all`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runNext(cmd, &o) },
	}
	f := cmd.Flags()
	o.filterFlags(f)
	f.BoolVar(&o.all, "all", false, "also show issues with unresolved blockers, naming them")
	f.IntVar(&o.limit, "limit", defaultNextLimit, "print at most this many issues")
	return cmd, nil
}

func runNext(cmd *cobra.Command, o *listOpts) error {
	if err := checkLimit(cmd, o.limit); err != nil {
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if cfg.Statuses.Initial == "" {
		// Without it only issues without a status would qualify, and every
		// other open issue would be left out without a word.
		return cfg.Errorf("next needs statuses.initial, the status of issues not yet taken").
			WithFix(`add initial = "<status>" under [statuses] in ` + cfg.Display)
	}
	a := newApp()
	rb, err := readBoard(cmd, a, cfg, o, true, nextFilter(cfg, o))
	if err != nil {
		return err
	}
	n := selectNext(cfg, rb.items, o)
	if rb.filter != nil {
		n.addRest(rb.filter, rb.items)
	}
	// Read without their place, blockers not among the items read are not
	// on the board, or archived there: one more request reads their place,
	// as show does.
	var places map[board.IssueKey]board.Place
	if keys := n.offBoard(); len(keys) > 0 {
		if places, err = board.ReadPlaces(cmd.Context(), a.Client, rb.schema, cfg.StatusField(), rb.items.StatusFieldID, keys); err != nil {
			return err
		}
	}
	n.resolve(cfg, o, places)
	warnings := readWarnings(cmd, rb.items, n.incomplete)
	repo := refRepo(a)
	res := n.result(o)
	res.Warnings = render.OrEmpty(warnings)
	return emit(cmd, res, func(w io.Writer) { n.write(w, repo, cfg, o) })
}

// nextIssue is an issue next may show, with its unresolved blockers.
type nextIssue struct {
	board.Item
	unsure   bool          // a --label may be among its labels not read
	blockers []nextBlocker // unresolved, in GitHub's order
	unread   int           // blockers not read: withheld from the token
}

// blocked reports whether the issue has an unresolved blocker; one not
// read counts as unresolved.
func (n nextIssue) blocked() bool { return len(n.blockers) > 0 || n.unread > 0 }

// nextBlocker is an unresolved blocker: open, and not on the board in a
// resolved status, or with a place that could not be read.
type nextBlocker struct {
	board.Blocker
	// link is its state and place on the board as show has a linked
	// issue's: State, OnBoard, Archived, Status and StatusUnknown.
	link issue.Link
}

// ref is "#12", or "owner/name#12" outside repo.
func (b nextBlocker) ref(repo string) string {
	if !strings.EqualFold(b.Repo, repo) || repo == "" {
		return b.Repo + "#" + strconv.Itoa(b.Number)
	}
	return "#" + strconv.Itoa(b.Number)
}

// next is what next prints: the issues, in board order, and the counts of
// what is left out. Every item in the initial status or without a status,
// except closed issues, is in exactly one of candidates, filtered, drafts,
// pulls and unseen, and every candidate in one of issues and hidden
// (blocked). Closed issues and other statuses are not candidates, so not
// counted: list shows them.
type next struct {
	leftOut
	candidates []nextIssue                   // open issues that pass the filters
	onBoard    map[board.IssueKey]board.Item // the board's issues
	issues     []nextIssue                   // with --all, the blocked ones too
	// incomplete: GitHub withheld part of a candidate or of its blockers.
	incomplete bool
}

// selectNext picks the open issues in the initial status or without one
// that pass the filters, in board order.
func selectNext(cfg *config.Config, items *board.Items, o *listOpts) *next {
	n := &next{onBoard: map[board.IssueKey]board.Item{}}
	for _, it := range items.Items {
		if it.Type == board.ContentIssue {
			n.onBoard[board.IssueKey{Repo: it.Repo, Number: it.Number}] = it
		}
	}
	for _, it := range items.Items {
		if it.Status != "" && it.Status != cfg.Statuses.Initial {
			continue
		}
		if n.skip(it) {
			continue
		}
		if it.State == issue.StateClosed {
			continue
		}
		ok, unsure := o.matches(it, items.Viewer)
		if !ok {
			n.filtered++
			continue
		}
		n.candidates = append(n.candidates, nextIssue{Item: it, unsure: unsure})
	}
	return n
}

// nextFilter is next's itemsFilter: the open issues in the initial status
// or without one that pass the filters; counted, the items in the initial
// status or without one by kind, and the open issues there before the
// filters.
func nextFilter(cfg *config.Config, o *listOpts) func(*board.Schema, *board.Field) *itemsFilter {
	if readEveryItem {
		return nil
	}
	return func(s *board.Schema, field *board.Field) *itemsFilter {
		terms, labels, ok := o.userTerms(s, cfg)
		if !ok {
			return nil
		}
		var others []string
		for _, st := range field.OptionNames() {
			if st != cfg.Statuses.Initial {
				others = append(others, st)
			}
		}
		var scope board.Filter
		if len(others) > 0 {
			t, ok := board.FieldTerm(cfg.StatusField(), others, true)
			if !ok {
				return nil
			}
			scope = board.Filter{t}
		}
		open := scope.And(board.FilterIssues, board.FilterOpen)
		q := newItemsFilter(scope, open.And(terms...))
		if len(terms) > 0 {
			q.count("open", open)
		}
		if len(labels) > 0 {
			q.count("labeled", board.Filter{board.FilterIssues}.And(labels...))
		}
		return q
	}
}

// addRest counts the items next's filter left out: the open issues in
// the initial status or without one that were not read do not pass the
// filters, and the draft issues, pull requests and unseen items there are
// skipped. GitHub matched each --label against all of an issue's labels,
// so none is unchecked.
func (n *next) addRest(q *itemsFilter, items *board.Items) {
	if open, ok := q.get(items, "open"); ok {
		n.filtered += max(0, open-len(items.Items))
	}
	n.addSkipped(q, items)
	for i := range n.candidates {
		n.candidates[i].unsure = false
	}
}

// offBoard lists the candidates' open blockers that are not among the
// board's items read and were read without their place, each once.
func (n *next) offBoard() []board.IssueKey {
	var keys []board.IssueKey
	for _, c := range n.candidates {
		for _, b := range c.BlockedBy.Nodes {
			k := board.IssueKey{Repo: b.Repo, Number: b.Number}
			if _, on := n.onBoard[k]; b.State != issue.StateClosed && b.Place == nil && !on && !slices.Contains(keys, k) {
				keys = append(keys, k)
			}
		}
	}
	return keys
}

// resolve sorts out the blocked candidates, by the rule of show and the
// blockers check: a blocker is resolved when it is closed or on the board
// in a resolved status, archived or not. Its place is the one read with
// it, else its item's among the board's items, else the one ReadPlaces
// read; one that could not be read is unresolved.
func (n *next) resolve(cfg *config.Config, o *listOpts, places map[board.IssueKey]board.Place) {
	for _, c := range n.candidates {
		c.unread = max(0, c.BlockedBy.TotalCount-len(c.BlockedBy.Nodes))
		n.incomplete = n.incomplete || c.Incomplete || c.unread > 0
		for _, b := range c.BlockedBy.Nodes {
			if b.State == issue.StateClosed {
				continue
			}
			k := board.IssueKey{Repo: b.Repo, Number: b.Number}
			nb := nextBlocker{Blocker: b, link: issue.Link{State: b.State}}
			if bi, on := n.onBoard[k]; on && b.Place == nil {
				nb.link.OnBoard, nb.link.Status = true, bi.Status
			} else {
				p, read := places[k]
				if b.Place != nil {
					p, read = *b.Place, true
				}
				if !read {
					p = board.Place{Unknown: "not read"}
				}
				if p.State == issue.StateClosed {
					continue
				}
				nb.link.OnBoard, nb.link.Archived, nb.link.Status, nb.link.StatusUnknown = p.OnBoard, p.Archived, p.Status, p.Unknown
				n.incomplete = n.incomplete || p.Withheld
			}
			if nb.link.Resolved(cfg.Statuses.Resolved) {
				continue
			}
			c.blockers = append(c.blockers, nb)
		}
		if c.blocked() && !o.all {
			n.hidden++
			continue
		}
		n.issues = append(n.issues, c)
		if c.unsure {
			n.unchecked++
		}
	}
}

// write prints the issues, cut to o.limit, then a line for each kind of
// issue left out (principle 7).
func (n *next) write(w io.Writer, repo string, cfg *config.Config, o *listOpts) {
	writeSprint(w, o)
	shown := n.issues[:min(len(n.issues), o.limit)]
	for _, ni := range shown {
		line := itemLine(ni.Item, repo)
		if ni.blocked() {
			line += " — blocked by " + n.blockersText(ni, repo)
		}
		fmt.Fprintln(w, line)
	}
	if len(n.issues) == 0 {
		what := "no issues to take"
		switch {
		case n.hidden > 0 || n.filtered > 0:
		case n.drafts+n.pulls+n.unseen > 0:
			what += fmt.Sprintf(": in %s or without a status there are only items next skips; gh dojo list shows the board", cfg.Statuses.Initial)
		default:
			what += fmt.Sprintf(": no open issue is in %s or without a status; gh dojo list shows the board", cfg.Statuses.Initial)
		}
		fmt.Fprintln(w, what)
	}
	if note := render.Shown(len(shown), len(n.issues), "use --limit "+strconv.Itoa(len(n.issues))); note != "" {
		fmt.Fprintln(w, note)
	}
	n.leftOut.write(w, "next", "blocked", o)
}

// maxNextBlockers is how many blockers a line of next --all names.
const maxNextBlockers = 3

// blockersText names an issue's unresolved blockers with their place:
// "#12 (In progress), acme/api#3 (not on the board), 1 not read".
func (n *next) blockersText(ni nextIssue, repo string) string {
	var names []string
	for i, b := range ni.blockers {
		if i == maxNextBlockers {
			names = append(names, fmt.Sprintf("%d more", len(ni.blockers)-maxNextBlockers))
			break
		}
		names = append(names, b.ref(repo)+" ("+linkPlace(b.link)+")")
	}
	if ni.unread > 0 {
		names = append(names, fmt.Sprintf("%d not read", ni.unread))
	}
	return strings.Join(names, ", ")
}

// nextResult is next's JSON: the issues printed, in the same order and
// cut to --limit, and the counts of those left out.
type nextResult struct {
	// SprintFilter and Sprint are list's: null without --sprint.
	SprintFilter *string         `json:"sprint_filter"`
	Sprint       *iterationJSON  `json:"sprint"`
	Issues       []nextIssueJSON `json:"issues"`
	Matched      int             `json:"matched"` // issues to show; more than len(issues) when cut by --limit
	// Unchecked counts issues listed though a --label may be among their
	// labels not read.
	Unchecked int         `json:"unchecked"`
	Hidden    int         `json:"hidden"`   // blocked: --all shows them
	Filtered  int         `json:"filtered"` // not matching the filters
	Skipped   skippedJSON `json:"skipped"`
	// Incomplete: GitHub withheld parts of some items (warned on stderr).
	Incomplete bool `json:"incomplete"`
	// Warnings are also printed on stderr.
	Warnings []string `json:"warnings"`
}

// nextIssueJSON is list's issue with its unresolved blockers, which only
// --all shows.
type nextIssueJSON struct {
	listIssueJSON
	UnresolvedBlockers []nextBlockerJSON `json:"unresolved_blockers"`
	UnreadBlockers     int               `json:"unread_blockers"` // withheld from the token: unresolved
}

type nextBlockerJSON struct {
	Ref      string  `json:"ref"` // owner/name#12, always in full
	Repo     string  `json:"repo"`
	Number   int     `json:"number"`
	OnBoard  bool    `json:"on_board"`
	Status   *string `json:"status"` // null: no status, not on the board, or not read
	Archived bool    `json:"archived"`
	// StatusUnknownReason says why its place on the board could not be
	// read, as in show; null when on_board and status are accurate.
	StatusUnknownReason *string `json:"status_unknown_reason"`
}

// result is the issues as JSON, cut to o.limit as write cuts them.
func (n *next) result(o *listOpts) nextResult {
	r := nextResult{
		SprintFilter: sprintFilterJSON(o), Sprint: toIterationJSON(o.sprintIt), Issues: []nextIssueJSON{}, Matched: len(n.issues),
		Unchecked: n.unchecked, Hidden: n.hidden, Filtered: n.filtered, Skipped: n.toSkippedJSON(),
		Incomplete: n.incomplete, Warnings: []string{},
	}
	for _, ni := range n.issues[:min(len(n.issues), o.limit)] {
		j := nextIssueJSON{listIssueJSON: listIssueOf(ni.Item), UnresolvedBlockers: []nextBlockerJSON{}, UnreadBlockers: ni.unread}
		for _, b := range ni.blockers {
			j.UnresolvedBlockers = append(j.UnresolvedBlockers, nextBlockerJSON{
				Ref: b.Repo + "#" + strconv.Itoa(b.Number), Repo: b.Repo, Number: b.Number,
				OnBoard: b.link.OnBoard, Status: render.OrNull(b.link.Status), Archived: b.link.Archived,
				StatusUnknownReason: render.OrNull(b.link.StatusUnknown),
			})
		}
		r.Issues = append(r.Issues, j)
	}
	return r
}
