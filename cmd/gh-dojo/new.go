package main

import (
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/newissue"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/workflow"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// gh dojo new creates an issue with its labels, on the board, in the
// initial status. The run is newissue.Run (see the package for its
// requests and retries); this file parses and checks the flags before any
// request, calls it and emits the result.

func init() { register(newNewCmd) }

func newNewCmd() (*cobra.Command, error) {
	var (
		o         newissue.Options
		bodyFile  string
		repoName  string
		blockedBy []string
		parent    string
		assign    []string
	)
	cmd := &cobra.Command{
		Use:   "new <title>",
		Short: "Create an issue with labels, on the board, in the initial status",
		Long:  newHelp(),
		Example: `  gh dojo new "Fix login redirect" --type bug --area cli
  gh dojo new "Support org boards" --label enhancement --body-file - < notes.md
  gh dojo new "Release v1.2" --milestone v1.2 --blocked-by 17 --blocked-by 18
  gh dojo new "Document the export" --type documentation --parent 40 --assignee @me
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
			if len(blockedBy) > 0 || f.Changed(parentFlag) {
				repo, err := a.Repo()
				if err != nil {
					return err
				}
				if o.Blockers, err = parseBlockers(blockedBy, blockedByFlag, repo, issue.Ref{}, cmd.CommandPath()+" --help"); err != nil {
					return err // before any request
				}
				if f.Changed(parentFlag) {
					if o.Parent, err = parseParent(parent, repo, cmd.CommandPath()+" --help"); err != nil {
						return err // before any request
					}
				}
			}
			is, err := newissue.Run(cmd.Context(), newissue.Env{
				Config: cfg, Client: a.Client, Board: a, Repo: a.Repo, Host: clientHost, Warn: cmd.ErrOrStderr(),
			}, o)
			if is == nil { // nothing created
				return err
			}
			// A run that stopped after creating the issue prints it as JSON
			// too; its text is the error.
			return emitWrite(cmd, newJSON(is, o.DryRun), func(w io.Writer) {
				switch {
				case err != nil:
				case o.DryRun:
					fmt.Fprintln(w, is.DryRunText())
				default:
					fmt.Fprintln(w, is.Confirm())
				}
			}, err, is.Wrote())
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.Type, "type", "", newTypeUsage())
	f.StringArrayVar(&o.Areas, "area", nil, "area `name`: adds the label <labels.area_prefix><name>; repeatable")
	f.StringArrayVar(&o.Labels, "label", nil, "any existing repo `label`; repeatable")
	f.StringVar(&o.Milestone, "milestone", "", "an open milestone of the repo, by `title`; none for no milestone")
	f.StringVar(&o.Body, "body", "", "issue body (markdown); one starting with - goes as --body=...")
	f.StringVar(&bodyFile, bodyFileFlag, "", "read the body from `file`; - reads stdin")
	f.StringArrayVar(&blockedBy, blockedByFlag, nil,
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

Every value is checked before anything is created: an unknown label,
milestone, assignee or repository fails listing the allowed ones or the
candidates; a --blocked-by or --parent issue that does not exist, or is a
pull request, is exit code 3.

--type X adds <labels.type_prefix>X, or with no prefix any repo label
outside the areas; --area X adds <labels.area_prefix>X; a value may carry
its prefix. The types and areas are the repo's labels with the prefix.
--milestone takes a title, ignoring case, or a start only one title has:
v0.3 for "v0.3 — extensibility". --assignee takes up to ` + strconv.Itoa(workflow.MaxAssignees) + ` logins. --repo
is another repository of board.repos, whose labels, milestones and
assignees count; a bare number in --blocked-by and --parent stays in the
default one. A title starting with - goes after --, every flag before it.

A repeat is safe: the body ends with a hidden marker of the title, and a
run that finds it on one of the repo's ` + strconv.Itoa(issue.NewDedupWindow) + ` newest open issues, written by
you (the same title, ignoring case and spacing), finishes that issue
instead of creating another; a milestone, parent or assignees it has stay.
A run that fails after creating the issue exits with code 5: run it again.
--dry-run prints the plan, the issue it would create or what finishing the
earlier one would do, after the same checks.`)
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
	Milestone    *milestoneJSON `json:"milestone"`
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

// newJSON is the issue new created or resumed (dry: would create or
// resume) as JSON.
func newJSON(is *newissue.Issue, dry bool) newResult {
	var number *int
	if is.Number != 0 {
		number = &is.Number
	}
	var parent *string
	if is.Parent != nil {
		parent = render.OrNull(fullRef(is.Parent.In(is.Def)))
	}
	return newResult{
		Number: number, Repo: is.Repo.String(), Title: render.SingleLine(is.Title()), DryRun: dry, Created: !is.Resumed,
		Labels: render.OrEmpty(is.Labels), Status: render.OrNull(is.Status), StatusSet: is.StatusSet, Unarchived: is.Unarchived,
		Milestone: toMilestoneJSON(is.Milestone), MilestoneSet: is.MilestoneSet,
		BlockedBy: refList(is.Linked, is.Def), AlreadyBlockedBy: refList(is.LinkedFound, is.Def),
		Parent: parent, ParentSet: is.ParentSet,
		Assigned: render.OrEmpty(is.Assigned), AlreadyAssigned: render.OrEmpty(is.AssignedFound),
	}
}

// toMilestoneJSON is the milestone in new's JSON result, nil for none.
func toMilestoneJSON(m *issue.OpenMilestone) *milestoneJSON {
	if m == nil {
		return nil
	}
	out := &milestoneJSON{Title: render.SingleLine(m.Title)}
	if m.DueOn != nil {
		if t, err := time.Parse(time.RFC3339, *m.DueOn); err == nil {
			out.DueOn = render.OrNull(cardDay(t))
		}
	}
	return out
}
