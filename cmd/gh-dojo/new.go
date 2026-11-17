package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/effects"
	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/workflow"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// gh dojo new creates an issue with its labels, on the board, in the
// initial status.
//
// Its plan is separate from its execution, as a verb's: the read is
// issue.ReadNewContext, the checks and the plan workflow.CheckNew and
// workflow.PlanNew (no network), the mutations effects'; this file parses
// the flags, applies the plan (newRun.apply) and prints the result.
//
// Requests with a warm schema cache: one read (issue.ReadNewContext: repo
// ID, labels, open milestones, recent issues, the --blocked-by and
// --parent issues, and the --assignee users), createIssue (with its
// labels, milestone, parent and assignees), addProjectV2ItemById, then the
// status with the blocked-by links (addBlockedBy, aliased, in the same
// mutation): four. Without statuses.initial and --blocked-by the last
// write is skipped. --parent and --assignee cost no request: createIssue
// takes them (parentIssueId, assigneeIds), and only a resumed issue
// without them gets addSubIssue or addAssigneesToAssignable, in that last
// mutation. An --assignee who cannot be assigned costs one more read, for
// the allowed list, before failing.
//
// The board item is added explicitly rather than with createIssue's
// projectV2Ids: GitHub adds those asynchronously, so the createIssue answer
// often had no item yet, and the addProjectV2ItemById then sent was refused
// in live runs (issue #17), likely racing that add. addProjectV2ItemById is
// synchronous and returns the existing item for content already on the
// board. Should it still be refused (the board's auto-add workflow racing
// it, say), the issue's board items are re-read once (newRecheckDelay
// later) and an item found there is used.
//
// Retries are safe (principle 5): the body ends with a hidden marker
// <!-- gh-dojo:new:<hash> --> of the normalized title
// (workflow.NewDedupKey), and the read looks for it among the repo's
// issue.NewDedupWindow newest open issues, keeping those the viewer wrote.
// A match is resumed instead of created again: missing labels and
// --assignee users are added (assignees it has stay), the --milestone is
// set if it has none (a different one stays, with a warning), the issue is
// unarchived or put on the board and, if it has no status, given the
// initial one; blocked-by links it lacks are added, and so is the --parent
// if it has no parent (a different one stays, with a warning). So filing
// an open issue again with the same title within the window resumes it.
// An issue that dropped out of the window (issue.NewDedupWindow newer open
// issues in the repo, by anyone) or was closed is created again.
//
// Nothing is written twice within a run either: a lost createIssue answer
// is followed by one re-read (newRecheckDelay later), and a stale schema ID
// once the issue exists is exit code 5, never an in-process retry, since a
// lagging re-read could miss the new issue and create another.

func init() { register(newNewCmd) }

// newRecheckDelay is the pause before re-reading after a lost createIssue
// answer or a refused addProjectV2ItemById, so the issue or its board item,
// if there, shows up. Tests set it to 0.
var newRecheckDelay = time.Second

// newOptions are the arguments of gh dojo new.
type newOptions struct {
	Title  string
	Body   string
	Type   string // "" when --type is not given
	Areas  []string
	Labels []string
	// Milestone is the --milestone title, "" when not given.
	Milestone string
	// BlockedBy are the --blocked-by values as given; Blockers the issues
	// they name, parsed before any request.
	BlockedBy []string
	Blockers  []issue.Ref
	// Parent is the issue --parent names, parsed before any request; nil
	// without --parent.
	Parent *issue.Ref
	// Assignees are the logins --assignee names, parsed before any request
	// (see parseAssignees): without duplicates, config.Me for the
	// authenticated user.
	Assignees []string
	// Repo is the repository of --repo, one of board.repos; zero for the
	// default one.
	Repo issue.Repo
	// DryRun reads and checks everything and writes nothing: the run's
	// fields say what would be done (see newRun.dryRun).
	DryRun bool
}

func newNewCmd() (*cobra.Command, error) {
	var (
		o        newOptions
		bodyFile string
		repoName string
		parent   string
		assign   []string
	)
	cmd := &cobra.Command{
		Use:   "new <title>",
		Short: "Create an issue with labels, on the board, in the initial status",
		Long:  newHelp(),
		Example: `  gh dojo new "Fix login redirect" --type bug --area cli
  gh dojo new "Support org boards" --label enhancement --body-file - < notes.md
  gh dojo new "Release v1.2" --milestone v1.2 --blocked-by 17 --blocked-by 18
  gh dojo new "Document the export" --type documentation --parent 40
  gh dojo new "Fix login redirect" --type bug --assignee @me
  gh dojo new "Rate limit the export" --repo acme/api --type bug
  gh dojo new "Fix login redirect" --type bug --blocked-by 17 --dry-run
  gh dojo new --type bug -- "-v is ignored by list"`,
		Args: newArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			o.Title = strings.Join(strings.Fields(args[0]), " ")
			if o.Title == "" {
				return render.Usage("the title is empty").WithFix(cmd.CommandPath() + " --help")
			}
			f := cmd.Flags()
			switch {
			case f.Changed("body") && f.Changed(bodyFileFlag):
				return render.Usage("give the body either with --body or with --%s, not both", bodyFileFlag).
					WithFix(cmd.CommandPath() + " --help")
			case f.Changed(bodyFileFlag):
				body, err := readBodyFile(cmd, bodyFile)
				if err != nil {
					return err
				}
				o.Body = body
			default:
				o.Body = strings.TrimSpace(o.Body)
			}
			if f.Changed("milestone") {
				switch o.Milestone = strings.TrimSpace(o.Milestone); {
				case o.Milestone == "":
					return render.Usage("--milestone is empty").WithFix(cmd.CommandPath() + " --help")
				case strings.EqualFold(o.Milestone, workflow.NoMilestone):
					o.Milestone = "" // no milestone, as for a verb
				}
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if err := workflow.CheckNewFlags(cfg, workflow.NewLabels{Type: o.Type, Areas: o.Areas, Labels: o.Labels}); err != nil {
				return err // before any request
			}
			if f.Changed(assigneeFlag) {
				if o.Assignees, err = parseAssignees(assign, cmd.CommandPath()+" --help"); err != nil {
					return err // before any request
				}
			}
			if f.Changed("repo") {
				if o.Repo, err = newRepo(cfg, repoName); err != nil {
					return err // before any request
				}
			}
			a := newApp()
			if len(o.BlockedBy) > 0 || f.Changed(parentFlag) {
				repo, err := a.Repo()
				if err != nil {
					return err
				}
				if o.Blockers, err = parseBlockers(o.BlockedBy, blockedByFlag, repo, issue.Ref{}, cmd.CommandPath()+" --help"); err != nil {
					return err // before any request
				}
				if f.Changed(parentFlag) {
					if o.Parent, err = parseParent(parent, repo, cmd.CommandPath()+" --help"); err != nil {
						return err // before any request
					}
				}
			}
			r, err := runNew(cmd.Context(), a, cfg, o, cmd.ErrOrStderr())
			if r == nil { // nothing created
				return err
			}
			// A run that stopped after creating the issue prints it as JSON
			// too; its text is the error.
			return emitWrite(cmd, r.result(), func(w io.Writer) {
				switch {
				case err != nil:
				case o.DryRun:
					fmt.Fprintln(w, r.dryRun())
				default:
					fmt.Fprintln(w, r.confirm())
				}
			}, err, r.wrote())
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.Type, "type", "", newTypeUsage())
	f.StringArrayVar(&o.Areas, "area", nil, "area `name`: adds the label <labels.area_prefix><name>; repeatable")
	f.StringArrayVar(&o.Labels, "label", nil, "any existing repo `label`; repeatable")
	f.StringVar(&o.Milestone, "milestone", "", "an open milestone of the repo, by `title`; none for no milestone")
	f.StringVar(&o.Body, "body", "", "issue body (markdown); one starting with - goes as --body=...")
	f.StringVar(&bodyFile, bodyFileFlag, "", "read the body from `file`; - reads stdin")
	f.StringArrayVar(&o.BlockedBy, blockedByFlag, nil,
		"the blocking `issue`: 12, \"#12\", owner/repo#12 or its URL; repeatable, or comma-separated")
	f.StringVar(&parent, parentFlag, "", "make it a sub-issue of the parent `issue`: 12, \"#12\", owner/repo#12 or its URL")
	f.StringArrayVar(&assign, assigneeFlag, nil, "assign a `login`, @me for yourself; repeatable, or comma-separated")
	f.StringVar(&repoName, "repo", "", "create the issue in `owner/repo`, one of board.repos (default: the first)")
	f.BoolVar(&o.DryRun, dryRunFlag, false, "check everything and print the plan without creating or changing anything")
	// --by, as wait and block name the blocking issue, is --blocked-by
	// here: agents carry the name over (#61). Not in the help: one name
	// is shown.
	f.SetNormalizeFunc(func(_ *pflag.FlagSet, name string) pflag.NormalizedName {
		if name == byFlag {
			name = blockedByFlag
		}
		return pflag.NormalizedName(name)
	})
	return cmd, nil
}

// blockedByFlag is new's flag naming the blocking issues; --by is taken
// for it too.
const blockedByFlag = workflow.NewBlockedByFlag

// parentFlag is new's flag naming the parent issue.
const parentFlag = workflow.NewParentFlag

// assigneeFlag is new's flag naming the users to assign, as a verb's
// --assignee.
const assigneeFlag = workflow.NewAssigneeFlag

// parseAssignees parses the --assignee values: logins, repeated or
// comma-separated, config.Me for the authenticated user, without
// duplicates (ignoring case). A value with nothing in it, a login with @, or
// one that is not a GitHub login is exit code 1 with help as the fix; so
// are more than GitHub allows on an issue.
func parseAssignees(values []string, help string) ([]string, error) {
	var out []string
	for _, v := range values {
		empty := true
		for _, login := range strings.Split(v, ",") {
			login = strings.TrimSpace(login)
			if login == "" {
				continue
			}
			if err := config.AssigneeArgError(assigneeFlag, login); err != nil {
				return nil, err.WithFix(help)
			}
			empty = false
			if !slices.ContainsFunc(out, func(l string) bool { return strings.EqualFold(l, login) }) {
				out = append(out, login)
			}
		}
		if empty {
			return nil, render.Usage("--%s is empty: pass a login, like --%s octocat, or %s for yourself", assigneeFlag, assigneeFlag, config.Me).
				WithFix(help)
		}
	}
	if len(out) > workflow.MaxAssignees {
		return nil, render.Usage("--%s names %d users; GitHub allows %d assignees per issue", assigneeFlag, len(out), workflow.MaxAssignees).
			WithFix(help)
	}
	return out, nil
}

// parseParent parses --parent: one issue (see splitRefs), a bare number in
// def. Anything else is exit code 1 with help as the fix.
func parseParent(value string, def issue.Repo, help string) (*issue.Ref, error) {
	refs, text, err := splitRefs([]string{value}, def)
	switch {
	case err != nil:
		return nil, err
	case len(text) > 0:
		return nil, render.Usage("--%s %q is not an issue: pass %s", parentFlag, text[0], issue.RefExamples).WithFix(help)
	case len(refs) == 0:
		return nil, render.Usage("--%s is empty: pass the parent issue, like --%s 12", parentFlag, parentFlag).WithFix(help)
	case len(refs) > 1:
		return nil, render.Usage("--%s %q names %d issues: an issue has one parent", parentFlag, strings.TrimSpace(value), len(refs)).WithFix(help)
	}
	return &refs[0], nil
}

// newRepo checks --repo, without a request: a repository of board.repos,
// as the config spells it. Any other is exit code 1 with board.repos as
// the allowed values; so is any --repo with board.repos empty, where the
// current repository is the only one.
func newRepo(cfg *config.Config, name string) (issue.Repo, error) {
	name = strings.TrimSpace(name)
	for _, r := range cfg.Board.Repos {
		if strings.EqualFold(r, name) {
			return issue.ParseRepo(r)
		}
	}
	add := fmt.Sprintf("add %q to repos under [board] in %s", name, cfg.Display)
	if len(cfg.Board.Repos) == 0 {
		return issue.Repo{}, render.Usage("--repo %q: board.repos is empty, so gh dojo new creates issues only in the current repository", name).
			WithFix("drop --repo, or " + add)
	}
	fix := "pass one of board.repos, or " + add
	if _, err := issue.ParseRepo(name); err != nil {
		fix = "pass one of board.repos as owner/repo"
	}
	return issue.Repo{}, render.Usage("--repo %q is not in board.repos: gh dojo new creates issues only in the board's repositories", name).
		WithAllowed(cfg.Board.Repos...).WithFix(fix)
}

// newTypeUsage is the help of --type, for this project when the config
// loads.
func newTypeUsage() string {
	cfg, err := loadConfig()
	if err != nil {
		return "issue type: adds the label <labels.type_prefix><type>; any repo label outside the areas when the prefix is empty"
	}
	rule, _ := newTypeRule(cfg.Labels, "<type>")
	return "issue type: " + rule
}

// newTypeRule says what --type takes with labels: "adds the label
// <type_prefix><value>", for which takesValue is true, or which repo
// labels it may be.
func newTypeRule(labels config.Labels, value string) (rule string, takesValue bool) {
	switch {
	case labels.TypePrefix != "":
		return "adds the label " + labels.TypePrefix + value, true
	case labels.AreaPrefix != "":
		return "any repo label not starting with " + labels.AreaPrefix, false
	}
	return "any repo label", false
}

// newArgs accepts exactly one title.
func newArgs(cmd *cobra.Command, args []string) error {
	help := cmd.CommandPath() + " --help"
	switch {
	case len(args) == 0:
		return render.Usage(`new needs a title, like: gh dojo new "Fix login redirect"`).WithFix(help)
	case len(args) > 1:
		return render.Usage("new takes one title, got %d arguments; %s", len(args),
			extraArgsHint(cmd, args, "quote the title")).WithFix(help)
	}
	return nil
}

// extraArgsHint is the advice for too many arguments: hint, or, when a flag
// was put after --, where it is an argument, to put the flags before --.
func extraArgsHint(cmd *cobra.Command, args []string, hint string) string {
	at := cmd.ArgsLenAtDash()
	if at < 0 {
		return hint
	}
	for _, a := range args[at+1:] {
		if strings.HasPrefix(a, "-") && !strings.ContainsAny(a, " \t") {
			return fmt.Sprintf("%s after -- is taken as an argument: put the flags before --", a)
		}
	}
	return hint
}

// newHelp is the long help of new, with this project's labels and initial
// status when the config loads.
func newHelp() string {
	var b strings.Builder
	b.WriteString(`Create an issue, label it, add it to the board and set its initial status
(statuses.initial) in one command.

Labels: --type X adds the label <labels.type_prefix>X, or with no prefix
any repo label outside the areas; --area X adds the label
<labels.area_prefix>X; a value may also carry its prefix. The types and
areas are the repo's labels with the prefix: the config lists none. --label
adds any existing label. An unknown one fails before anything is created,
listing the allowed values.

--milestone puts the issue in an open milestone of the repo, by title
(ignoring case) or a start only its title has: v0.3 for "v0.3 — extensibility".
An unknown or ambiguous one fails before anything is created, listing the open
milestones or the candidates. --milestone none is no milestone, as for a verb.

--assignee assigns users by login, @me for yourself (repeatable, or
comma-separated; GitHub allows ` + strconv.Itoa(workflow.MaxAssignees) + `). One who cannot be assigned issues in
the repo fails before anything is created, listing who can.

The body comes from --body or --body-file (a file, or - for stdin). A title
starting with - goes after --, and every flag before the --, since anything
after it is taken as the title: gh dojo new --type bug -- "-v is ignored".

--blocked-by marks the new issue blocked by other issues (12, "#12",
owner/repo#12 or the issue's URL; repeatable, or comma-separated). An issue
that does not exist, or a pull request, fails before anything is created
(exit code 3).

--parent makes the new issue a sub-issue of another one (GitHub's
sub-issues; 12, "#12", owner/repo#12 or the issue's URL), set as it is
created. A parent that does not exist, or a pull request, fails before
anything is created (exit code 3).

--repo owner/repo creates the issue in another repository of board.repos
than the default one (board.repos[0]); labels, milestones, assignees and
the repeat check are that repository's, and the issue is named owner/repo#N.
A bare number in --blocked-by and --parent is still in the default
repository.
A repository not in board.repos is exit code 1, listing them.

A repeat is safe: the body ends with a hidden marker of the title, and a run
that finds it on one of the repo's ` + strconv.Itoa(issue.NewDedupWindow) + ` newest open issues, written by you,
finishes that issue (labels, assignees, milestone, board, status, blocked-by
links, parent) instead of creating another; assignees it has stay, and a
different milestone or parent it (or one the token cannot read) already has
is kept.
So filing an open issue again with the same title (ignoring case and
spacing) resumes it. If a run fails after creating the issue, it exits with
code 5: run the same command again.

--dry-run reads and checks everything a run does (labels, assignees,
milestone, blockers, parent, an issue an earlier run created) and prints the
plan: the issue it would create, or what finishing the earlier one would do.
Nothing is created or changed.`)
	cfg, err := loadConfig()
	if err != nil {
		return b.String()
	}
	b.WriteString("\n\nThis project:")
	if rule, takesValue := newTypeRule(cfg.Labels, "X"); takesValue {
		b.WriteString("\n  --type X  " + rule)
	} else {
		b.WriteString("\n  --type    " + rule)
	}
	if cfg.Labels.AreaPrefix != "" {
		fmt.Fprintf(&b, "\n  --area X  adds the label %sX", cfg.Labels.AreaPrefix)
	} else {
		b.WriteString("\n  --area    unavailable: labels.area_prefix is not set")
	}
	if cfg.Statuses.Initial != "" {
		fmt.Fprintf(&b, "\n  status    %s", cfg.Statuses.Initial)
	} else {
		b.WriteString("\n  status    none: statuses.initial is not set")
	}
	return b.String()
}

// runNew creates the issue described by o, or finishes the one a failed
// run left, and returns the run: nil when no issue exists, else with
// r.partial's error when it stopped after the issue existed. Warnings go
// to warn.
func runNew(ctx context.Context, a *App, cfg *config.Config, o newOptions, warn io.Writer) (*newRun, error) {
	for attempt := 1; ; attempt++ {
		r := &newRun{App: a, cfg: cfg, opts: o}
		err := r.run(ctx)
		for _, w := range r.warnings {
			render.Warn(warn, "%s", w)
		}
		if err == nil {
			return r, nil
		}
		stale := a.SchemaCached() && board.IsStale(err)
		if r.number == 0 {
			// Nothing written yet: a schema ID GitHub rejected is fixed by
			// re-reading the board and trying once more.
			if stale && attempt == 1 && a.ResetSchema() == nil {
				continue
			}
			return nil, err
		}
		if stale {
			// The issue exists; retrying here could miss it in a lagging
			// read and create another. The re-run reads the board afresh.
			_ = a.ResetSchema()
		}
		return r, r.partial(err)
	}
}

// newResult is new's JSON: the issue created or resumed, and how far the
// run got.
type newResult struct {
	// Number is null in a dry run that would create the issue.
	Number *int   `json:"number"`
	Repo   string `json:"repo"`
	Title  string `json:"title"`
	// DryRun: nothing was written, and the fields say what would be: the
	// issue created or resumed, the ones ending in _set and blocked_by what
	// would be set, unarchived what would be unarchived.
	DryRun bool `json:"dry_run"`
	// Created is false when an earlier run created the issue and this one
	// found it by its marker and finished it.
	Created bool     `json:"created"`
	Labels  []string `json:"labels"`
	// Status is the issue's status on the board, null for none; StatusSet
	// says this run set it.
	Status     *string `json:"status"`
	StatusSet  bool    `json:"status_set"`
	Unarchived bool    `json:"unarchived"`
	// Milestone is the issue's milestone, nil for none; MilestoneSet says
	// this run set it. A resumed issue keeps a different milestone it had.
	Milestone    *jsonMilestone `json:"milestone"`
	MilestoneSet bool           `json:"milestone_set"`
	// BlockedBy are the blocked-by links added now, AlreadyBlockedBy those
	// a resumed issue had.
	BlockedBy        []string `json:"blocked_by"`
	AlreadyBlockedBy []string `json:"already_blocked_by"`
	// Parent is the issue's parent, owner/name#12, null for none;
	// ParentSet says this run made it a sub-issue of --parent. A resumed
	// issue keeps a different parent it had.
	Parent    *string `json:"parent"`
	ParentSet bool    `json:"parent_set"`
	// Assigned are the --assignee logins assigned now, AlreadyAssigned
	// those a resumed issue had; other assignees it has are not listed.
	Assigned        []string `json:"assigned"`
	AlreadyAssigned []string `json:"already_assigned"`
	// What stopped a run after the issue existed (exit code 5, or 1 for a
	// dependency cycle) is the top-level "error": re-run the same command.
}

// wrote reports whether this run wrote anything: created the issue, or
// finished one an earlier run created.
func (r *newRun) wrote() bool {
	if r.opts.DryRun {
		return false
	}
	return !r.resumed || r.added || r.statusSet || r.unarchived || r.milestoneSet || len(r.assigned) > 0 ||
		len(r.linked) > 0 || r.parentSet
}

// issueTitle is the issue's title on GitHub when found, else the one given.
func (r *newRun) issueTitle() string { return cmp.Or(r.title, r.opts.Title) }

// result is the run as JSON.
func (r *newRun) result() newResult {
	var number *int
	if r.number != 0 {
		number = &r.number
	}
	return newResult{
		Number: number, Repo: r.repo.String(), Title: render.SingleLine(r.issueTitle()), DryRun: r.opts.DryRun, Created: !r.resumed,
		Labels: render.OrEmpty(r.labels), Status: render.OrNull(r.status), StatusSet: r.statusSet, Unarchived: r.unarchived,
		Milestone: milestoneJSON(r.milestone), MilestoneSet: r.milestoneSet,
		BlockedBy: refList(r.linked, r.def), AlreadyBlockedBy: refList(r.linkedFound, r.def),
		Parent: r.parentJSON(), ParentSet: r.parentSet,
		Assigned: render.OrEmpty(r.assigned), AlreadyAssigned: render.OrEmpty(r.assignedFound),
	}
}

// parentJSON is the issue's parent in full, owner/name#12; nil for none.
func (r *newRun) parentJSON() *string {
	if r.parent == nil {
		return nil
	}
	return render.OrNull(fullRef(r.parent.In(r.def)))
}

// newRun is one attempt of new; its fields after run say how far it got.
type newRun struct {
	*App
	cfg  *config.Config
	opts newOptions

	warnings []string // for stderr

	number  int    // the issue's number, once it exists
	id      string // its node ID
	title   string // its title on GitHub when found, "" when created now
	resumed bool   // an earlier run created it; this one found it by its marker
	labels  []string
	// labelsMissing are the labels a resumed issue lacks, for a dry run.
	labelsMissing []string

	assigned      []string // the --assignee logins assigned now, as GitHub spells them
	assignedFound []string // those a resumed issue had already

	itemID     string
	added      bool   // this run added the issue to the board
	unarchived bool   // the board item was archived when found, and this run restored it
	status     string // the status found, "" for none
	statusSet  bool   // the status was set to the initial one now
	noInitial  bool   // statuses.initial is empty: no status was set

	milestone     *issue.OpenMilestone // the issue's milestone, nil for none
	milestoneSet  bool                 // set to --milestone now
	milestoneKept string               // the --milestone not applied: a resumed issue had another

	repo        issue.Repo      // the issue's repository: --repo, or the default one
	def         issue.Repo      // the default repository, which output names issues relative to
	linked      []*issue.Linked // blocked-by links added now
	linkedFound []*issue.Linked // blocked-by links a resumed issue had already

	parent       *issue.Ref // the issue's parent, nil for none or not known
	parentSet    bool       // made a sub-issue of --parent now
	parentKept   *issue.Ref // the --parent not applied: a resumed issue had another
	parentHidden bool       // a resumed issue has a parent the token cannot read
	parentErr    error      // GitHub refused the parent of createIssue for good (parentRefused)
}

// newStepError says which write failed, as "setting status Backlog failed".
type newStepError struct {
	step string
	err  error
	// parent: the step is the --parent link, which GitHub refused for good
	// (parentRefused): re-running cannot help.
	parent bool
}

func (e *newStepError) Error() string { return e.step + ": " + e.err.Error() }
func (e *newStepError) Unwrap() error { return e.err }

func (r *newRun) run(ctx context.Context) error {
	s, err := r.Schema(ctx)
	if err != nil {
		return err
	}
	def, err := r.Repo()
	if err != nil {
		return err
	}
	repo := def
	if r.opts.Repo != (issue.Repo{}) {
		repo = r.opts.Repo
	}
	statusField, err := s.Field(r.cfg.StatusField())
	if err != nil {
		return err
	}
	var initial *board.Option
	if name := r.cfg.Statuses.Initial; name != "" {
		if initial, err = statusField.Option(name); err != nil {
			return err
		}
	} else {
		r.noInitial = true
	}

	r.repo, r.def = repo, def
	nc, err := issue.ReadNewContext(ctx, r.Client, r.contextRequest(repo, statusField.Name, r.opts.Blockers))
	if err != nil {
		return err
	}
	labels, err := r.resolveLabels(ctx, nc)
	if err != nil {
		return err
	}
	var milestone *issue.OpenMilestone
	if r.opts.Milestone != "" {
		all, err := nc.AllMilestones(ctx, r.Client, repo)
		if err != nil {
			return err
		}
		if milestone, err = issue.MatchMilestone(r.opts.Milestone, all, repo); err != nil {
			return err
		}
	}
	assignees, err := r.resolveAssignees(ctx, nc)
	if err != nil {
		return err
	}
	if err := issue.LinksError("--"+blockedByFlag, nc.Links, def); err != nil {
		return err
	}
	if err := parentError(nc.Parent, def); err != nil {
		return err
	}
	r.labels = issue.LabelNames(labels)

	in := newInput{nc: nc, labels: labels, milestone: milestone, assignees: assignees, initial: initial}
	marker := workflow.NewMarker(r.opts.Title)
	found := nc.Find(marker, s.ID)
	req, state := r.request(in, found), r.foundState(found, false)
	if err := workflow.CheckNew(req, state); err != nil {
		return err
	}
	plan := workflow.PlanNew(r.cfg, req, state)
	r.resumed = plan.Resumed
	if plan.Create && !r.opts.DryRun {
		if err := r.create(ctx, nc.RepoID, withMarker(r.opts.Body, marker), in); err != nil {
			if r.number != 0 || !errors.Is(err, graph.ErrUnknownOutcome) {
				return err
			}
			// The answer was lost: look once whether the issue was created,
			// and finish it if so.
			if found = r.recheck(ctx, repo, statusField.Name, marker, s.ID); found == nil {
				return err
			}
			r.warnings = append(r.warnings, fmt.Sprintf("GitHub's answer to creating the issue was lost; found %s on a re-read", issue.Ref{Repo: repo, Number: found.Number}.String(def)))
			plan = workflow.PlanNew(r.cfg, r.request(in, found), r.foundState(found, true))
		}
	}
	r.warnings = append(r.warnings, plan.Warnings...)
	if found != nil {
		r.number, r.id, r.title = found.Number, found.ID, found.Title
		r.itemID, r.status = found.ItemID, found.Status
		r.milestone = found.Milestone
		r.assignedFound = plan.AlreadyAssigned
		r.labelsMissing = plan.Labels
		r.milestoneKept = plan.MilestoneKept
		if p := nc.Parent; p != nil {
			r.parent = found.Parent
			if plan.ParentKept {
				r.parentKept, r.parentHidden = &p.Ref, plan.ParentHidden
			}
		}
	}
	if r.opts.DryRun {
		r.planDry(plan, in)
		return nil
	}
	return r.apply(ctx, s, statusField, plan, in)
}

// newInput is what new resolved against the repository: the context read,
// the labels, the --milestone (nil for none), the --assignee users and the
// initial status (nil for none).
type newInput struct {
	nc        *issue.NewContext
	labels    []issue.Label
	milestone *issue.OpenMilestone
	assignees []*issue.User
	initial   *board.Option
}

// resolveLabels maps --type, --area and --label to labels of the issue's
// repository (see workflow.ResolveNewLabels), reading all of them only
// when one is not among the first page.
func (r *newRun) resolveLabels(ctx context.Context, nc *issue.NewContext) ([]issue.Label, error) {
	want := workflow.NewLabels{Type: r.opts.Type, Areas: r.opts.Areas, Labels: r.opts.Labels}
	labels, complete := nc.Labels.Nodes, !nc.Labels.PageInfo.HasNextPage
	for {
		names, more, err := workflow.ResolveNewLabels(r.cfg, want, issue.LabelNames(labels), complete, r.repo.String())
		if err != nil {
			return nil, err
		}
		if !more {
			out := make([]issue.Label, len(names))
			for i, n := range names {
				out[i] = labelNamed(labels, n)
			}
			return out, nil
		}
		if labels, err = nc.AllLabels(ctx, r.Client, r.repo); err != nil {
			return nil, err
		}
		complete = true
	}
}

// resolveAssignees resolves the --assignee logins to users who can be
// assigned issues in the issue's repository (see
// issue.NewContext.MatchAssignees). Logins that cannot be assigned are one
// usage error (exit code 1) naming them all, with the repository's
// assignable users as the allowed values, which takes one more read.
func (r *newRun) resolveAssignees(ctx context.Context, nc *issue.NewContext) ([]*issue.User, error) {
	users, unknown, noViewer := nc.MatchAssignees(r.opts.Assignees, config.Me)
	if noViewer {
		return nil, render.Network("GitHub did not return the authenticated user for --%s %s", assigneeFlag, config.Me).
			WithFix(render.AuthFix)
	}
	if len(unknown) == 0 {
		return users, nil
	}
	e := notAssignable(render.Usage, "--"+assigneeFlag, unknown, r.repo)
	if logins, err := assignableUsers(ctx, r.Client, r.repo); err == nil {
		e = e.WithAllowed(logins...)
	}
	return nil, e
}

// labelNamed is the label of labels with name, which is among them.
func labelNamed(labels []issue.Label, name string) issue.Label {
	return labels[slices.IndexFunc(labels, func(l issue.Label) bool { return l.Name == name })]
}

// contextRequest is what issue.ReadNewContext reads for the run in repo:
// the blockers (none for a re-read), the --parent and the --assignee
// users.
func (r *newRun) contextRequest(repo issue.Repo, statusField string, blockers []issue.Ref) issue.NewRequest {
	logins, viewer := newAssigneeLogins(r.opts.Assignees)
	return issue.NewRequest{Repo: repo, StatusField: statusField, Blockers: blockers, Parent: r.opts.Parent,
		Logins: logins, Viewer: viewer}
}

// newAssigneeLogins splits the --assignee values into the logins to look
// up among the assignable users, in order, and whether config.Me is among
// them (the viewer is read for it).
func newAssigneeLogins(assignees []string) (logins []string, viewer bool) {
	for _, a := range assignees {
		if a == config.Me {
			viewer = true
		} else {
			logins = append(logins, a)
		}
	}
	return logins, viewer
}

// request is what the run asks for, for workflow.CheckNew and PlanNew, the
// links related to found (nil for none).
func (r *newRun) request(in newInput, found *issue.Created) workflow.NewRequest {
	req := workflow.NewRequest{Repo: r.repo.String(), Labels: issue.LabelNames(in.labels), Assignees: loginsOf(in.assignees)}
	if in.milestone != nil {
		req.Milestone = in.milestone.Title
	}
	if in.initial != nil {
		req.Initial = in.initial.Name
	}
	for _, l := range in.nc.Links {
		req.Blockers = append(req.Blockers, r.link(l, found))
	}
	if p := in.nc.Parent; p != nil {
		l := r.link(p, found)
		if found != nil && found.ParentID != "" && !l.ParentOf {
			// For the warning that the issue keeps its parent: gh issue
			// edit takes the new parent's number in the issue's
			// repository, else its URL.
			l.EditArg = fmt.Sprintf("https://%s/%s/issues/%d", clientHost(), p.Ref.Repo, p.Ref.Number)
			if p.Ref.Repo.Same(r.repo) {
				l.EditArg = strconv.Itoa(p.Ref.Number)
			}
		}
		req.Parent = &l
	}
	return req
}

// link is l for the plan, related to found (nil for none).
func (r *newRun) link(l *issue.Linked, found *issue.Created) workflow.NewLink {
	out := workflow.NewLink{Name: l.Ref.String(r.def), Arg: l.Ref.Arg(r.def),
		SubIssues: l.SubIssues, Full: l.SubIssues >= issue.MaxSubIssues, Ref: l}
	if found != nil {
		out.Found = l.ID == found.ID
		out.Blocks, out.BlockedBy = l.Blocks(found.ID), l.BlockedBy(found.ID)
		out.ParentOf = found.ParentID != "" && found.ParentID == l.ID
	}
	return out
}

// foundState is found for the plan; nil for nil. rechecked: this run
// created it, and found it again after the answer was lost.
func (r *newRun) foundState(found *issue.Created, rechecked bool) *workflow.NewFound {
	if found == nil {
		return nil
	}
	ref := issue.Ref{Repo: r.repo, Number: found.Number}
	out := &workflow.NewFound{Name: ref.String(r.def), Arg: ref.Arg(r.def), Number: found.Number,
		Labels: found.Labels, Assignees: found.Assignees, AssigneesTotal: found.AssigneesTotal,
		OnBoard: found.ItemID != "", Archived: found.Archived, Status: found.Status,
		ParentHidden: found.ParentHidden, Rechecked: rechecked}
	if found.Milestone != nil {
		out.Milestone = found.Milestone.Title
	}
	if found.ParentID != "" {
		out.Parent = r.parentName(found.Parent)
	}
	return out
}

// linksOf returns the issues the plan's links keep (workflow.NewLink.Ref).
func linksOf(links []workflow.NewLink) []*issue.Linked {
	var out []*issue.Linked
	for _, l := range links {
		out = append(out, l.Ref.(*issue.Linked))
	}
	return out
}

// usersOf returns the users of in with logins, in order.
func usersOf(in newInput, logins []string) []*issue.User {
	var out []*issue.User
	for _, login := range logins {
		if i := slices.IndexFunc(in.assignees, func(u *issue.User) bool { return u.Login == login }); i >= 0 {
			out = append(out, in.assignees[i])
		}
	}
	return out
}

// apply writes what plan has left once the issue exists (createIssue sets
// its labels, milestone, assignees and parent): a resumed issue's labels
// and milestone, one mutation each, then the board item, then the rest in
// one mutation (see finish), recording each step as it takes effect.
func (r *newRun) apply(ctx context.Context, s *board.Schema, statusField *board.Field, plan *workflow.NewPlan, in newInput) error {
	if !plan.Create && len(plan.Labels) > 0 {
		var ids []string
		for _, n := range plan.Labels {
			ids = append(ids, labelNamed(in.labels, n).ID)
		}
		vars := map[string]any{"issue": r.id, "labels": ids}
		if err := r.Client.Mutate(ctx, effects.AddLabelsMutation, vars, nil); err != nil {
			return &newStepError{step: "adding labels " + strings.Join(r.labels, ", ") + " failed", err: err}
		}
	}
	if !plan.Create && plan.Milestone != "" {
		vars := map[string]any{"issue": r.id, "milestone": in.milestone.ID}
		if err := r.Client.Mutate(ctx, effects.SetMilestoneMutation, vars, nil); err != nil {
			return &newStepError{step: "setting milestone " + render.SingleLine(in.milestone.Title) + " failed", err: err}
		}
		r.milestone, r.milestoneSet = in.milestone, true
	}
	if plan.Unarchive {
		vars := map[string]any{"project": s.ID, "item": r.itemID}
		if err := r.Client.Mutate(ctx, effects.UnarchiveMutation, vars, nil); err != nil {
			return &newStepError{step: "unarchiving it on board " + s.Ref().String() + " failed", err: err}
		}
		r.unarchived = true
	}
	if plan.AddToBoard {
		if err := r.addToBoard(ctx, s.ID, statusField.Name); err != nil {
			return &newStepError{step: "adding it to board " + s.Ref().String() + " failed", err: err}
		}
	}
	// A resumed issue keeps a status it already has, even one found only
	// while adding it to the board: someone may have moved it on purpose.
	initial := in.initial
	if plan.Status == "" || plan.Resumed && r.status != "" {
		initial = nil
	}
	r.linkedFound = linksOf(plan.AlreadyLinked)
	var assign []*issue.User
	var parent *issue.Linked
	if !plan.Create {
		assign = usersOf(in, plan.Assign)
		if plan.Parent {
			parent = in.nc.Parent
		}
	}
	if pending := linksOf(plan.Link); len(pending) > 0 || parent != nil || len(assign) > 0 {
		if err := r.finish(ctx, s.ID, statusField.ID, initial, assign, pending, parent); err != nil {
			return err
		}
	} else if initial != nil {
		vars := map[string]any{"project": s.ID, "item": r.itemID, "field": statusField.ID, "option": initial.ID}
		if err := r.Client.Mutate(ctx, effects.SetStatusMutation, vars, nil); err != nil {
			return &newStepError{step: "setting status " + initial.Name + " failed", err: err}
		}
		r.status, r.statusSet = initial.Name, true
	}
	if r.parentErr != nil {
		// The rest is done; createIssue refused the parent for good.
		return &newStepError{step: "making it a sub-issue of " + in.nc.Parent.Ref.String(r.def) + " failed", err: r.parentErr, parent: true}
	}
	return nil
}

// finish sets the status initial (nil leaves it alone), assigns the users
// assign, adds the blocked-by links and makes the issue a sub-issue of
// parent (nil for none) in one mutation (see effects.NewFinish). What took
// effect is recorded (r.statusSet, r.assigned, r.linked, r.parentSet) even
// when the rest failed; an error after everything took effect is only a
// warning.
func (r *newRun) finish(ctx context.Context, projectID, fieldID string, initial *board.Option, assign []*issue.User, links []*issue.Linked, parent *issue.Linked) error {
	f := effects.NewFinish{Status: initial != nil, Assignees: userIDs(assign)}
	ids := effects.IDs{Project: projectID, Issue: r.id, Item: r.itemID, StatusField: fieldID}
	if initial != nil {
		ids.StatusOption = initial.ID
	}
	for _, l := range links {
		f.Links = append(f.Links, l.ID)
	}
	if parent != nil {
		f.Parent = parent.ID
	}
	m := f.Build(ids)
	var data map[string]json.RawMessage
	err := r.Client.Mutate(ctx, m.Query, m.Vars, &data)
	var steps []string
	if initial != nil {
		if effects.Answered(data, effects.AliasStatus) {
			r.status, r.statusSet = initial.Name, true
		} else {
			steps = append(steps, "setting status "+initial.Name)
		}
	}
	if len(assign) > 0 {
		if effects.Answered(data, effects.AliasAssign) {
			r.assigned = loginsOf(assign)
		} else {
			steps = append(steps, "assigning "+render.JoinWords(loginsOf(assign), "and"))
		}
	}
	var missed []*issue.Linked
	for i, l := range links {
		if effects.Answered(data, effects.LinkAlias(i)) {
			r.linked = append(r.linked, l)
		} else {
			missed = append(missed, l)
		}
	}
	if len(missed) > 0 {
		steps = append(steps, "marking it blocked by "+joinRefs(missed, r.def))
	}
	parentMissed := false
	if parent != nil {
		if effects.Answered(data, effects.AliasParent) {
			r.parent, r.parentSet = &parent.Ref, true
		} else {
			steps = append(steps, "making it a sub-issue of "+parent.Ref.String(r.def))
			parentMissed = true
		}
	}
	if parentMissed && len(steps) == 1 && parentRefused(err, effects.AliasParent, false) {
		return &newStepError{step: steps[0] + " failed", err: err, parent: true}
	}
	switch {
	case len(steps) == 0 && err != nil:
		r.warnings = append(r.warnings, "GitHub reported an error after applying every change: "+render.Reason(err))
	case len(steps) > 0 && err == nil:
		err = render.Network("GitHub returned no result for part of the write")
		fallthrough
	case len(steps) > 0:
		return &newStepError{step: strings.Join(steps, " and ") + " failed", err: err}
	}
	return nil
}

// parentRefused reports whether err is GitHub refusing the --parent link
// for good, so that re-running cannot help: every GraphQL error of err is
// at field (the alias of the write), of type UNPROCESSABLE or FORBIDDEN, or
// an input error without a type; with named, its message must also name
// the parent or sub-issues (createIssue, whose other parts may fail too).
// A parent with as many sub-issues as GitHub allows, one 8 levels deep, a
// cycle, or no access to the parent's repository are such refusals.
//
// The words named looks for are a guess: GitHub's messages for these
// refusals of createIssue were not checked (they cannot be produced
// read-only). At worst a refusal they miss costs one more run: it is exit
// code 5, and the re-run finds the issue and sends addSubIssue, whose
// refusal is told by its path alone.
func parentRefused(err error, field string, named bool) bool {
	items := graph.ErrorItems(err)
	if errors.Is(err, graph.ErrUnknownOutcome) || len(items) == 0 {
		return false
	}
	for _, item := range items {
		if len(item.Path) == 0 || item.Path[0] != field {
			return false
		}
		switch item.Type {
		case "UNPROCESSABLE", "FORBIDDEN", "":
		default:
			return false
		}
		if named && !containsAnyFold(item.Message, "parent", "sub-issue", "sub issue", "subissue") {
			return false
		}
	}
	return true
}

// containsAnyFold reports whether s contains any of words, ignoring case.
func containsAnyFold(s string, words ...string) bool {
	s = strings.ToLower(s)
	return slices.ContainsFunc(words, func(w string) bool { return strings.Contains(s, w) })
}

// addToBoard puts the issue on the board projectID and sets r.itemID (and
// r.status, when a re-read finds the item with one). A refusal that is not
// a lost answer is followed by one re-read of the issue's board items
// (newRecheckDelay later): the item may be there all the same, added by
// GitHub or the board's automation.
func (r *newRun) addToBoard(ctx context.Context, projectID, statusField string) error {
	var out struct {
		Add *struct {
			Item *struct {
				ID string `json:"id"`
			} `json:"item"`
		} `json:"addProjectV2ItemById"`
	}
	vars := map[string]any{"project": projectID, "issue": r.id}
	err := r.Client.Mutate(ctx, effects.AddToBoardMutation, vars, &out)
	if err == nil {
		if out.Add == nil || out.Add.Item == nil || out.Add.Item.ID == "" {
			return render.Internal("addProjectV2ItemById returned no item")
		}
		r.itemID, r.added = out.Add.Item.ID, true
		return nil
	}
	if errors.Is(err, graph.ErrUnknownOutcome) || ctx.Err() != nil {
		// A lost answer or an interrupted run: the re-run finds the item.
		return err
	}
	if !recheckPause(ctx) {
		return err
	}
	it := issue.ReadBoardItem(ctx, r.Client, r.id, statusField, projectID)
	if it == nil {
		return err
	}
	r.itemID = it.ID
	if it.Status != nil {
		r.status = it.Status.Name
	}
	r.warnings = append(r.warnings, fmt.Sprintf("GitHub refused adding %s to the board, but a re-read found it there", r.name()))
	return nil
}

// recheckPause waits newRecheckDelay before a re-read; false when ctx ends
// first.
func recheckPause(ctx context.Context) bool {
	if newRecheckDelay <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(newRecheckDelay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// noIssueCreated is the error for a createIssue answer with no issue and
// no error: GitHub answers so when its limit on creating content is hit
// (seen on this repository, #67, while addComment got the limit's error).
// So it is exit code 4 with the limit's wait, not an internal error to
// re-run at once. Should GitHub have created the issue after all, a
// re-run finds it by its marker.
func noIssueCreated() *render.Error {
	e := render.ContentLimited("")
	e.What = "GitHub created no issue and gave no reason, as it does when its limit on creating issues and comments is hit"
	return e
}

// recheck re-reads the newest issues after a lost createIssue answer and
// returns the issue with marker, or nil when it is not there or the read
// fails.
func (r *newRun) recheck(ctx context.Context, repo issue.Repo, statusField, marker, projectID string) *issue.Created {
	if !recheckPause(ctx) {
		return nil
	}
	nc, err := issue.ReadNewContext(ctx, r.Client, r.contextRequest(repo, statusField, nil))
	if err != nil {
		return nil
	}
	return nc.Find(marker, projectID)
}

// create sends createIssue with what in resolved: the labels, the
// milestone and the parent unless nil, and the assignees. On an error with
// the issue in the payload (a part of the input GitHub refused) the issue
// exists: r.number is set and the error is returned for a partial result.
func (r *newRun) create(ctx context.Context, repoID, body string, in newInput) error {
	milestone, parent, assignees := in.milestone, in.nc.Parent, in.assignees
	ids := make([]string, len(in.labels))
	for i, l := range in.labels {
		ids[i] = l.ID
	}
	var out struct {
		Create *struct {
			Issue *struct {
				ID     string `json:"id"`
				Number int    `json:"number"`
			} `json:"issue"`
		} `json:"createIssue"`
	}
	vars := map[string]any{
		"repo": repoID, "title": r.opts.Title, "body": body, "labels": ids,
	}
	if milestone != nil {
		vars["milestone"] = milestone.ID
	}
	if parent != nil {
		vars["parent"] = parent.ID
	}
	if len(assignees) > 0 {
		vars["assignees"] = userIDs(assignees)
	}
	err := r.Client.Mutate(ctx, effects.CreateIssueMutation, vars, &out)
	if out.Create == nil || out.Create.Issue == nil || out.Create.Issue.Number == 0 {
		if err == nil {
			err = noIssueCreated()
		}
		return err
	}
	is := out.Create.Issue
	r.number, r.id = is.Number, is.ID
	// A parent GitHub refused for good leaves the issue with the rest: the
	// run goes on, and ends with this refusal (see apply).
	parentErr := err != nil && parent != nil && parentRefused(err, "createIssue", true)
	if err != nil && !parentErr {
		parts := []string{"labels"}
		if milestone != nil {
			parts = append(parts, "milestone")
		}
		if parent != nil {
			parts = append(parts, "parent")
		}
		if len(assignees) > 0 {
			parts = append(parts, "assignees")
		}
		return &newStepError{step: "GitHub refused part of the request (" + render.JoinWords(parts, "or") + ")", err: err}
	}
	r.milestone, r.milestoneSet = milestone, milestone != nil
	if len(assignees) > 0 {
		r.assigned = loginsOf(assignees)
	}
	switch {
	case parentErr:
		r.parentErr = err
	case parent != nil:
		r.parent, r.parentSet = &parent.Ref, true
	}
	return nil
}

// confirm is the confirmation line of a finished run:
//
//	#57 "Fix login redirect" created, labels bug, area:cli, no status → Backlog
//	#57 "Fix login redirect" created, milestone v1.2, no status → Backlog, blocked by #12 and #13
//	#57 "Fix login redirect" already created, labels bug, unarchived, Backlog (unchanged)
//	#57 "Fix login redirect" already created, milestone v1.1 (kept, not v1.2), Backlog (unchanged)
//	#57 "Fix login redirect" created, no status → Backlog, sub-issue of #40
//	#57 "Fix login redirect" already created, assigned monalisa, already assigned octocat, Backlog (unchanged)
func (r *newRun) confirm() string {
	effects := []string{"created"}
	if r.resumed {
		effects[0] = "already created"
	}
	if len(r.labels) > 0 {
		effects = append(effects, "labels "+strings.Join(r.labels, ", "))
	}
	if len(r.assigned) > 0 {
		effects = append(effects, "assigned "+render.JoinWords(r.assigned, "and"))
	}
	if len(r.assignedFound) > 0 {
		effects = append(effects, "already assigned "+render.JoinWords(r.assignedFound, "and"))
	}
	if r.milestone != nil {
		m := "milestone " + render.SingleLine(r.milestone.Title)
		if r.milestoneKept != "" {
			m += " (kept, not " + render.SingleLine(r.milestoneKept) + ")"
		}
		effects = append(effects, m)
	}
	if r.unarchived {
		effects = append(effects, "unarchived")
	}
	switch {
	case r.statusSet:
		effects = append(effects, render.Transition("", r.status))
	case r.status != "":
		effects = append(effects, render.Transition(r.status, r.status))
	case r.noInitial:
		effects = append(effects, "on the board with no status (statuses.initial is not set)")
	}
	if len(r.linked) > 0 {
		effects = append(effects, "blocked by "+joinRefs(r.linked, r.def))
	}
	if len(r.linkedFound) > 0 {
		effects = append(effects, "already blocked by "+joinRefs(r.linkedFound, r.def))
	}
	switch {
	case r.opts.Parent == nil:
	case r.parentKept != nil:
		of := r.parentName(r.parent)
		if r.parentHidden {
			of = "a parent not readable"
		}
		effects = append(effects, "sub-issue of "+of+" (kept, not "+r.parentKept.String(r.def)+")")
	case r.parentSet:
		effects = append(effects, "sub-issue of "+r.parentName(r.parent))
	case r.parent != nil:
		effects = append(effects, "already a sub-issue of "+r.parentName(r.parent))
	}
	return render.Confirm(r.name(), r.issueTitle(), effects...)
}

// planDry fills the run's fields as applying plan would leave them, for a
// dry run: the issue created with the labels, milestone, parent and
// assignees, or the resumed one finished; on the board, unarchived, in the
// initial status unless a resumed issue has one, and blocked by the links
// it lacks (linked) apart from those it has (linkedFound).
func (r *newRun) planDry(plan *workflow.NewPlan, in newInput) {
	r.assigned = slices.Clone(plan.Assign)
	if plan.Milestone != "" {
		r.milestone, r.milestoneSet = in.milestone, true
	}
	if plan.Parent {
		r.parent, r.parentSet = &in.nc.Parent.Ref, true
	}
	r.linked, r.linkedFound = linksOf(plan.Link), linksOf(plan.AlreadyLinked)
	r.added = plan.AddToBoard
	r.unarchived = plan.Unarchive
	if plan.Status != "" {
		r.status, r.statusSet = plan.Status, true
	}
}

// dryRun is the plan a dry run prints, from the fields planDry filled:
//
//	dry run: new "Fix login redirect"
//	  create: in Trum-ok/gh-dojo
//	  labels: bug, area:cli
//	  assignees: octocat and monalisa
//	  milestone: v1.2
//	  add to the board
//	  status: no status → Backlog
//	  blocked by: #12 and #13
//	  sub-issue of: #40
//
// and for an issue an earlier run created, what finishing it would do:
//
//	dry run: new #57 "Fix login redirect"
//	  create: already created by an earlier run; finishes it
//	  labels: +area:cli (bug already in place)
//	  assignees: +monalisa (octocat already assigned)
//	  milestone: v1.1 kept, not v1.2
//	  status: Backlog (unchanged)
//	  blocked by: #13 (#12 already linked)
func (r *newRun) dryRun() string {
	var b strings.Builder
	line := func(s string) { b.WriteString("\n  " + s) }
	if r.resumed {
		b.WriteString(render.Confirm("dry run: new "+r.name(), r.title))
		line("create: already created by an earlier run; finishes it")
	} else {
		b.WriteString(render.Confirm("dry run: new", r.opts.Title))
		line("create: in " + r.repo.String())
	}
	inPlace := len(r.labels) - len(r.labelsMissing)
	switch {
	case !r.resumed && len(r.labels) > 0:
		line("labels: " + strings.Join(r.labels, ", "))
	case len(r.labelsMissing) > 0 && inPlace > 0:
		var have []string
		for _, l := range r.labels {
			if !slices.Contains(r.labelsMissing, l) {
				have = append(have, l)
			}
		}
		line("labels: +" + strings.Join(r.labelsMissing, " +") + " (" + strings.Join(have, ", ") + " already in place)")
	case len(r.labelsMissing) > 0:
		line("labels: +" + strings.Join(r.labelsMissing, " +"))
	case len(r.labels) > 0:
		line("labels: " + strings.Join(r.labels, ", ") + " already in place")
	}
	switch {
	case !r.resumed && len(r.assigned) > 0:
		line("assignees: " + render.JoinWords(r.assigned, "and"))
	case len(r.assigned) > 0 && len(r.assignedFound) > 0:
		line("assignees: +" + strings.Join(r.assigned, " +") + " (" + render.JoinWords(r.assignedFound, "and") + " already assigned)")
	case len(r.assigned) > 0:
		line("assignees: +" + strings.Join(r.assigned, " +"))
	case len(r.assignedFound) > 0:
		line("assignees: " + render.JoinWords(r.assignedFound, "and") + " already assigned")
	}
	switch {
	case r.milestoneKept != "" && r.milestone != nil:
		line("milestone: " + render.SingleLine(r.milestone.Title) + " kept, not " + render.SingleLine(r.milestoneKept))
	case r.milestoneSet && r.resumed:
		line("milestone: none → " + render.SingleLine(r.milestone.Title))
	case r.milestoneSet:
		line("milestone: " + render.SingleLine(r.milestone.Title))
	case r.opts.Milestone != "" && r.milestone != nil:
		line("milestone: " + render.SingleLine(r.milestone.Title) + " already set")
	}
	if r.added {
		line(workflow.AddToBoard{}.Describe())
	}
	if r.unarchived {
		line(workflow.Unarchive{}.Describe())
	}
	switch {
	case r.statusSet:
		line("status: " + render.Transition("", r.status))
	case r.status != "":
		line("status: " + render.Transition(r.status, r.status))
	case r.noInitial:
		line("status: none set (statuses.initial is not set)")
	}
	switch {
	case len(r.linked) > 0 && len(r.linkedFound) > 0:
		line("blocked by: " + joinRefs(r.linked, r.def) + " (" + joinRefs(r.linkedFound, r.def) + " already linked)")
	case len(r.linked) > 0:
		line("blocked by: " + joinRefs(r.linked, r.def))
	case len(r.linkedFound) > 0:
		line("blocked by: " + joinRefs(r.linkedFound, r.def) + " already linked")
	}
	switch {
	case r.opts.Parent == nil:
	case r.parentKept != nil && r.parentHidden:
		line("sub-issue of: a parent not readable, kept, not " + r.parentKept.String(r.def))
	case r.parentKept != nil:
		line("sub-issue of: " + r.parentName(r.parent) + " kept, not " + r.parentKept.String(r.def))
	case r.parentSet:
		line("sub-issue of: " + r.parentName(r.parent))
	case r.parent != nil:
		line("sub-issue of: " + r.parentName(r.parent) + " already")
	}
	return b.String()
}

// name is how output names the issue: "#57", or "owner/repo#57" outside
// the default repository.
func (r *newRun) name() string { return issue.Ref{Repo: r.repo, Number: r.number}.String(r.def) }

// parentName names a parent issue as output names issues; "?" for nil.
func (r *newRun) parentName(p *issue.Ref) string {
	if p == nil {
		return "?"
	}
	return p.String(r.def)
}

// parentError is the error for a --parent issue that cannot be a parent,
// exit code 3: missing, or a pull request; nil for nil or an issue.
func parentError(p *issue.Linked, def issue.Repo) error {
	switch {
	case p == nil:
		return nil
	case p.PullRequest:
		return render.NotFound("--%s %s: a pull request, not an issue; only an issue can have sub-issues", parentFlag, p.Ref.String(def))
	}
	return issue.LinksError("--"+parentFlag, []*issue.Linked{p}, def)
}

// partial is the exit-code-5 error of a run that stopped after the issue
// existed: what failed, why, and that re-running finishes it. A cause with
// its own fix (auth, say) keeps it, followed by the re-run. A blocked-by
// link GitHub refused as a dependency cycle is exit code 1 instead: a
// re-run would fail the same way.
func (r *newRun) partial(err error) error {
	step, cause := "finishing it failed", err
	var se *newStepError
	if errors.As(err, &se) {
		step, cause = se.step, se.err
	}
	what := "created"
	if r.resumed {
		what = "found (created earlier)"
	}
	e := render.FromError(cause)
	why, _, _ := strings.Cut(e.What, "\n")
	if se != nil && se.parent {
		// Re-running cannot help a parent GitHub refuses: full, too deep, a
		// cycle, or out of the token's reach. GitHub's own words say which.
		items := graph.ErrorItems(cause)
		forbidden := false
		if len(items) > 0 {
			why = "GitHub: " + render.SingleLine(items[0].Message)
			forbidden = slices.ContainsFunc(items, func(i api.GraphQLErrorItem) bool { return i.Type == "FORBIDDEN" })
		}
		fix := "pass another --" + parentFlag + " to make it a sub-issue (gh issue edit " + strconv.Itoa(r.number) +
			" -R " + r.repo.String() + " --parent <issue>), or leave it without one"
		again := "; re-running will not help"
		if forbidden {
			// The token may lack access to the parent's repository.
			again = "; re-running with the same token will not help"
			fix = "if the token lacks access to the parent's repository: " + render.AuthFix +
				", then re-run the same command; else " + fix
		}
		return render.Usage("%s %s, but %s%s\n%s", r.name(), what, step, again, why).
			WithFix(fix).WithCause(err)
	}
	if graph.IsCycle(cause) {
		// Nor a blocked-by link that makes a cycle through other issues.
		return render.Usage("%s %s, but %s\n%s", r.name(), what, step, why).
			WithFix("gh dojo show " + strings.TrimPrefix(r.name(), "#")).WithCause(err)
	}
	fix := fmt.Sprintf("re-run the same gh dojo new command: it finds %s and finishes it", r.name())
	switch {
	case e.Wait != "": // a rate limit: its fix already says to wait, then re-run
		fix = "wait " + e.Wait + ", then " + fix
	case e.Fix != "" && e.Fix != render.InternalFix:
		fix = e.Fix + "; then re-run the same gh dojo new command"
	}
	return render.Partial("%s %s, but %s\n%s", r.name(), what, step, why).WithFix(fix).WithCause(err)
}

// milestoneJSON is the milestone in new's JSON result, nil for none.
func milestoneJSON(m *issue.OpenMilestone) *jsonMilestone {
	if m == nil {
		return nil
	}
	out := &jsonMilestone{Title: render.SingleLine(m.Title)}
	if m.DueOn != nil {
		if t, err := time.Parse(time.RFC3339, *m.DueOn); err == nil {
			out.DueOn = render.OrNull(cardDay(t))
		}
	}
	return out
}

// loginsOf returns the logins of users, as GitHub spells them.
func loginsOf(users []*issue.User) []string {
	out := make([]string, len(users))
	for i, u := range users {
		out[i] = u.Login
	}
	return out
}

// userIDs returns the node IDs of users, for a mutation.
func userIDs(users []*issue.User) []string {
	out := make([]string, len(users))
	for i, u := range users {
		out[i] = u.ID
	}
	return out
}
