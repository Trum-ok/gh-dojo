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
			"those with an unresolved blocker: open and not in a resolved status\n" +
			"(statuses.resolved), the rule of show and start. A blocker that is not on the\n" +
			"board, or that could not be read, counts as unresolved.\n\n" +
			"The blocked issues are counted; --all shows them too, with their blockers.\n" +
			"Filters combine with AND; the issues they leave out are counted. Only\n" +
			"issues are listed. gh dojo list shows the rest of the board.",
		Example: `  gh dojo next
  gh dojo next --mine --limit 1
  gh dojo next --label bug
  gh dojo next --sprint current
  gh dojo next --all`,
		Args: nextArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runNext(cmd, &o) },
	}
	f := cmd.Flags()
	f.BoolVar(&o.mine, "mine", false, "only issues assigned to you")
	f.StringVar(&o.sprint, "sprint", "", `only issues in this sprint (fields.sprint): "current" (today, in board.timezone), a title, or "none"`)
	f.StringArrayVar(&o.labels, "label", nil, "only issues with this label; repeat to require several")
	f.BoolVar(&o.all, "all", false, "also show issues with unresolved blockers, naming them")
	f.IntVar(&o.limit, "limit", defaultNextLimit, "print at most this many issues")
	return cmd, nil
}

// nextArgs rejects arguments; an issue number points at show.
func nextArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	if n, ok := parseIssue(args[0]); ok {
		return render.Usage("next takes no arguments; to see one issue: gh dojo show %d", n)
	}
	return render.Usage("next takes no arguments").WithFix(cmd.CommandPath() + " --help")
}

func runNext(cmd *cobra.Command, o *listOpts) error {
	if o.limit < 1 {
		return render.Usage("invalid --limit %d: want 1 or greater", o.limit).WithFix(cmd.CommandPath() + " --help")
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
	_, items, warnings, err := readBoard(cmd, a, cfg, o, true)
	if err != nil {
		return err
	}
	repo := ""
	if r, err := a.Repo(); err == nil { // only shortens refs; next works without it
		repo = r.String()
	}
	n := buildNext(cfg, items, o)
	res := n.result(o, repo, items)
	res.Warnings = orEmpty(warnings)
	return emit(cmd, res, func(w io.Writer) { n.write(w, repo, cfg, o) })
}

// nextIssue is an issue next may show, with its unresolved blockers.
type nextIssue struct {
	board.Item
	blockers []nextBlocker // unresolved, in GitHub's order
	unread   int           // blockers not read: withheld from the token
}

// blocked reports whether the issue has an unresolved blocker; one not
// read counts as unresolved.
func (n nextIssue) blocked() bool { return len(n.blockers) > 0 || n.unread > 0 }

// nextBlocker is an unresolved blocker: open, and not on the board in a
// resolved status.
type nextBlocker struct {
	board.Blocker
	onBoard bool
	status  string // its status on the board; "" for none, or not on the board
}

// ref is "#12", or "owner/name#12" outside repo.
func (b nextBlocker) ref(repo string) string {
	if b.Repo != repo || repo == "" {
		return b.Repo + "#" + strconv.Itoa(b.Number)
	}
	return "#" + strconv.Itoa(b.Number)
}

// place says where it stands on the board, as show does.
func (b nextBlocker) place() string {
	switch {
	case !b.onBoard:
		return "not on the board"
	case b.status == "":
		return "on the board, no status"
	}
	return b.status
}

// next is what next prints: the issues, in board order, and the counts of
// what is left out. Every item in the initial status or without a status,
// except closed issues, is in exactly one of issues, hidden, filtered,
// drafts, pulls and unseen. Closed issues and other statuses are not
// candidates, so not counted: list shows them.
type next struct {
	issues    []nextIssue // with --all, the blocked ones too
	unchecked int         // shown, but a --label may be among labels not read
	hidden    int         // blocked: --all shows them
	filtered  int         // don't match the filters
	drafts    int
	pulls     int
	unseen    int // items the token cannot see
}

// buildNext picks the open issues in the initial status or without one and
// sorts out the blocked. A blocker's status is its item's on the board;
// one not among the board's items (not on it, or archived) has none, so it
// is unresolved unless closed.
func buildNext(cfg *config.Config, items *board.Items, o *listOpts) *next {
	onBoard := map[string]board.Item{}
	for _, it := range items.Items {
		if it.Type == board.ContentIssue {
			onBoard[it.Repo+"#"+strconv.Itoa(it.Number)] = it
		}
	}
	n := &next{}
	for _, it := range items.Items {
		if it.Status != "" && it.Status != cfg.Statuses.Initial {
			continue
		}
		switch it.Type {
		case board.ContentIssue:
		case board.ContentDraftIssue:
			n.drafts++
			continue
		case board.ContentPullRequest:
			n.pulls++
			continue
		default:
			n.unseen++
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
		ni := nextIssue{Item: it, unread: max(0, it.BlockedBy.TotalCount-len(it.BlockedBy.Nodes))}
		for _, b := range it.BlockedBy.Nodes {
			if b.State == issue.StateClosed {
				continue
			}
			bi, on := onBoard[b.Repo+"#"+strconv.Itoa(b.Number)]
			if on && bi.Status != "" && slices.Contains(cfg.Statuses.Resolved, bi.Status) {
				continue
			}
			ni.blockers = append(ni.blockers, nextBlocker{Blocker: b, onBoard: on, status: bi.Status})
		}
		if ni.blocked() && !o.all {
			n.hidden++
			continue
		}
		n.issues = append(n.issues, ni)
		if unsure {
			n.unchecked++
		}
	}
	return n
}

// write prints the issues, cut to o.limit, then a line for each kind of
// issue left out (principle 7).
func (n *next) write(w io.Writer, repo string, cfg *config.Config, o *listOpts) {
	switch {
	case o.sprint == "":
	case o.sprintIt == nil:
		fmt.Fprintln(w, "sprint: none")
	default:
		fmt.Fprintln(w, "sprint: "+o.sprintIt.Span())
	}
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
		if n.hidden == 0 && n.filtered == 0 {
			what += fmt.Sprintf(": no open issue is in %s or without a status; gh dojo list shows the board", cfg.Statuses.Initial)
		}
		fmt.Fprintln(w, what)
	}
	if note := render.Shown(len(shown), len(n.issues), "use --limit "+strconv.Itoa(len(n.issues))); note != "" {
		fmt.Fprintln(w, note)
	}
	if n.unchecked > 0 {
		fmt.Fprintf(w, "%s not fully checked: more than %d labels\n",
			plural(n.unchecked, "issue", "issues"), board.LabelsWindow)
	}
	if n.hidden > 0 {
		fmt.Fprintf(w, "hidden: %d blocked, use --all\n", n.hidden)
	}
	if n.filtered > 0 {
		fmt.Fprintf(w, "filtered out: %s not matching %s\n",
			plural(n.filtered, "issue", "issues"), strings.Join(o.filterNames(), " "))
	}
	var skipped []string
	if n.drafts > 0 {
		skipped = append(skipped, plural(n.drafts, "draft issue", "draft issues"))
	}
	if n.pulls > 0 {
		skipped = append(skipped, plural(n.pulls, "pull request", "pull requests"))
	}
	if n.unseen > 0 {
		skipped = append(skipped, plural(n.unseen, "item the token cannot see", "items the token cannot see"))
	}
	if len(skipped) > 0 {
		fmt.Fprintf(w, "skipped: %s (next shows issues only)\n", strings.Join(skipped, ", "))
	}
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
		names = append(names, b.ref(repo)+" ("+b.place()+")")
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
	Sprint       *jsonIteration  `json:"sprint"`
	Issues       []nextIssueJSON `json:"issues"`
	Matched      int             `json:"matched"` // issues to show; more than len(issues) when cut by --limit
	// Unchecked counts issues listed though a --label may be among their
	// labels not read.
	Unchecked int `json:"unchecked"`
	Hidden    int `json:"hidden"`   // blocked: --all shows them
	Filtered  int `json:"filtered"` // not matching the filters
	Skipped   struct {
		DraftIssues  int `json:"draft_issues"`
		PullRequests int `json:"pull_requests"`
		Unseen       int `json:"unseen"` // items the token cannot see
	} `json:"skipped"`
	// Incomplete: GitHub withheld parts of some items (warned on stderr).
	Incomplete bool `json:"incomplete"`
	// Warnings are also printed on stderr.
	Warnings []string `json:"warnings"`
}

// nextIssueJSON is list's issue with its unresolved blockers, which only
// --all shows.
type nextIssueJSON struct {
	listIssue
	UnresolvedBlockers []nextBlockerJSON `json:"unresolved_blockers"`
	UnreadBlockers     int               `json:"unread_blockers"` // withheld from the token: unresolved
}

type nextBlockerJSON struct {
	Ref     string  `json:"ref"` // #12, or owner/name#12 in another repository
	Repo    string  `json:"repo"`
	Number  int     `json:"number"`
	OnBoard bool    `json:"on_board"`
	Status  *string `json:"status"` // null: no status, or not on the board
}

// result is the issues as JSON, cut to o.limit as write cuts them.
func (n *next) result(o *listOpts, repo string, items *board.Items) nextResult {
	r := nextResult{
		Sprint: iterationJSON(o.sprintIt), Issues: []nextIssueJSON{}, Matched: len(n.issues),
		Unchecked: n.unchecked, Hidden: n.hidden, Filtered: n.filtered, Incomplete: items.Incomplete,
		Warnings: []string{},
	}
	switch {
	case o.sprintIt != nil:
		r.SprintFilter = orNull("iteration")
	case o.sprint != "":
		r.SprintFilter = orNull(sprintNone)
	}
	r.Skipped.DraftIssues, r.Skipped.PullRequests, r.Skipped.Unseen = n.drafts, n.pulls, n.unseen
	for _, ni := range n.issues[:min(len(n.issues), o.limit)] {
		j := nextIssueJSON{listIssue: listIssueOf(ni.Item), UnresolvedBlockers: []nextBlockerJSON{}, UnreadBlockers: ni.unread}
		for _, b := range ni.blockers {
			j.UnresolvedBlockers = append(j.UnresolvedBlockers, nextBlockerJSON{
				Ref: b.ref(repo), Repo: b.Repo, Number: b.Number, OnBoard: b.onBoard, Status: orNull(b.status),
			})
		}
		r.Issues = append(r.Issues, j)
	}
	return r
}
