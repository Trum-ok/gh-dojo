package main

import (
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
// Requests with a warm schema cache: one read (newContextQuery: repo ID,
// labels, open milestones, recent issues, the --blocked-by and --parent
// issues, and the --assignee users), createIssue (with its labels,
// milestone, parent and assignees), addProjectV2ItemById, then the status
// with the blocked-by links (addBlockedBy, aliased, in the same mutation):
// four. Without statuses.initial and --blocked-by the last write is
// skipped. --parent and --assignee cost no request: createIssue takes them
// (parentIssueId, assigneeIds), and only a resumed issue without them gets
// addSubIssue or addAssigneesToAssignable, in that last mutation. An
// --assignee who cannot be assigned costs one more read, for the allowed
// list, before failing.
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
// <!-- gh-dojo:new:<hash> --> of the normalized title (newDedupKey), and the
// read looks for it among the repo's newDedupWindow newest open issues,
// keeping those the viewer wrote. A match is resumed instead of created
// again: missing labels and --assignee users are added (assignees it has
// stay), the --milestone is set if it has none (a
// different one stays, with a warning), the issue is unarchived or put on
// the board and, if it has no status, given the initial one; blocked-by
// links it lacks are added, and so is the --parent if it has no parent (a
// different one stays, with a warning). So filing an open
// issue again with the same title within the window resumes it. An issue
// that dropped out of the window (newDedupWindow newer open issues in the
// repo, by anyone) or was closed is created again.
//
// Nothing is written twice within a run either: a lost createIssue answer
// is followed by one re-read (newRecheckDelay later), and a stale schema ID
// once the issue exists is exit code 5, never an in-process retry, since a
// lagging re-read could miss the new issue and create another.

func init() { register(newNewCmd) }

const (
	// newMarkerVerb names new's marker: <!-- gh-dojo:new:<hash> -->.
	newMarkerVerb = "new"
	// newDedupWindow is how many of the repo's newest open issues are
	// searched for the marker ($window of newContextQuery).
	newDedupWindow = 20
)

// newRecheckDelay is the pause before re-reading after a lost createIssue
// answer or a refused addProjectV2ItemById, so the issue or its board item,
// if there, shows up. Tests set it to 0.
var newRecheckDelay = time.Second

// newContextQuery reads what new needs before writing: the repo ID, its
// labels (by name, for the allowed lists), its open milestones (for
// --milestone) and the newest open issues, to find one a failed run already
// created. $status is the board's status field name.
const newContextQuery = `query NewIssueContext($owner: String!, $repo: String!, $status: String!, $window: Int!) {
  repository(owner: $owner, name: $repo) {
    id
    labels(first: 100, orderBy: {field: NAME, direction: ASC}) {
      totalCount
      pageInfo { hasNextPage endCursor }
      nodes { id name }
    }
    ` + issue.MilestonesSelection + `
    issues(last: $window, states: OPEN, orderBy: {field: CREATED_AT, direction: ASC}) {
      nodes {
        id
        number
        title
        body
        viewerDidAuthor
        labels(first: 50) { nodes { name } }
        milestone { title dueOn }
        projectItems(first: 20) {
          nodes {
            id
            isArchived
            project { id }
            fieldValueByName(name: $status) { ... on ProjectV2ItemFieldSingleSelectValue { name } }
          }
        }
      }
    }
  }
}`

// newContextQueryWith is newContextQuery that also reads links, the
// --blocked-by and --parent issues (see issue.Links), and, with parent
// (the last link), how many sub-issues it has and the parent of each
// recent issue, so a resumed one shows whether it has one; with assignees
// (the --assignee logins), each login among the repository's assignable
// users (aliased assignee0, assignee1...), the viewer for config.Me, and
// the assignees of each recent issue. Without any of them it is
// newContextQuery itself.
//
//	query NewIssueContext(..., $assignee0: String!, $link0Owner: String!, ...) {
//	  repository(owner: $owner, name: $repo) {
//	    ...
//	    assignee0: assignableUsers(query: $assignee0, first: 100) { nodes { id login } }
//	    issues(...) { nodes { ... assignees(first: 10) { totalCount nodes { login } } ... } }
//	  }
//	  link0: repository(...) { ... }
//	  viewer { id login }
//	}
func newContextQueryWith(links issue.Links, parent bool, assignees []string) string {
	q := newContextQuery
	issueFields := newIssueMilestone
	sub := -1
	if parent {
		issueFields += newIssueParent
		sub = len(links) - 1
	}
	if len(assignees) > 0 {
		issueFields += newIssueAssignees
	}
	q = strings.Replace(q, newIssueMilestone, issueFields, 1)
	logins, viewer := newAssigneeLogins(assignees)
	var decls, repoFields strings.Builder
	for i := range logins {
		alias := assigneeAlias(i)
		fmt.Fprintf(&decls, ", $%s: String!", alias)
		fmt.Fprintf(&repoFields, "    %s: assignableUsers(query: $%s, first: %d) { nodes { id login } }\n", alias, alias, newAssignableWindow)
	}
	decls.WriteString(links.Decls())
	if decls.Len() > 0 {
		q = strings.Replace(q, "$window: Int!)", "$window: Int!"+decls.String()+")", 1)
	}
	if repoFields.Len() > 0 {
		q = strings.Replace(q, newIssuesField, repoFields.String()+newIssuesField, 1)
	}
	if len(links) == 0 && !viewer {
		return q
	}
	q = strings.TrimSuffix(q, "}") + links.FieldsCountingSubIssues(sub)
	if viewer {
		q += "  viewer { id login }\n"
	}
	return q + "}"
}

// newIssueMilestone is the milestone of a recent issue in newContextQuery;
// newIssueParent its parent, read after it for --parent, and
// newIssueAssignees its assignees (GitHub allows 10), for --assignee.
// newIssuesField starts the recent issues, which the assignable users for
// --assignee go before.
const (
	newIssueMilestone = "        milestone { title dueOn }\n"
	newIssueParent    = "        parent { id number repository { nameWithOwner } }\n"
	newIssueAssignees = "        assignees(first: 10) { totalCount nodes { login } }\n"
	newIssuesField    = "    issues(last: $window"
)

// newAssignableWindow is how many assignable users matching an --assignee
// login are read; the login is then matched among them, ignoring case.
const newAssignableWindow = 100

// assigneeAlias is the alias of the i-th --assignee login's lookup in
// newContextQueryWith, and the name of its variable.
func assigneeAlias(i int) string { return fmt.Sprintf("assignee%d", i) }

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

// newLabelsQuery reads all repo labels; only needed past the first 100.
const newLabelsQuery = `query RepoLabels($owner: String!, $repo: String!, $cursor: String) {
  repository(owner: $owner, name: $repo) {
    labels(first: 100, after: $cursor, orderBy: {field: NAME, direction: ASC}) {
      totalCount
      pageInfo { hasNextPage endCursor }
      nodes { id name }
    }
  }
}`

// newCreateMutation creates the issue with its labels, milestone, parent
// and assignees ($milestone, $parent and $assignees omitted for none). It
// is put on the board by newAddToBoardMutation, not projectV2Ids (see the
// top of the file).
const newCreateMutation = `mutation CreateIssue($repo: ID!, $title: String!, $body: String!, $labels: [ID!], $milestone: ID, $parent: ID, $assignees: [ID!]) {
  createIssue(input: {repositoryId: $repo, title: $title, body: $body, labelIds: $labels, milestoneId: $milestone, parentIssueId: $parent, assigneeIds: $assignees}) {
    issue { id number }
  }
}`

// newAddLabelsMutation adds labels a resumed issue lacks.
const newAddLabelsMutation = `mutation AddLabels($issue: ID!, $labels: [ID!]!) {
  addLabelsToLabelable(input: {labelableId: $issue, labelIds: $labels}) { clientMutationId }
}`

// newSetMilestoneMutation sets the milestone of a resumed issue that has
// none.
const newSetMilestoneMutation = `mutation SetMilestone($issue: ID!, $milestone: ID!) {
  updateIssue(input: {id: $issue, milestoneId: $milestone}) { issue { id } }
}`

// newUnarchiveMutation restores a resumed issue's archived board item.
const newUnarchiveMutation = `mutation Unarchive($project: ID!, $item: ID!) {
  unarchiveProjectV2Item(input: {projectId: $project, itemId: $item}) { item { id } }
}`

// newAddToBoardMutation adds the issue to the board, or returns its item if
// it is there already.
const newAddToBoardMutation = `mutation AddToBoard($project: ID!, $issue: ID!) {
  addProjectV2ItemById(input: {projectId: $project, contentId: $issue}) { item { id } }
}`

// newBoardItemsQuery re-reads an issue's board items after
// addProjectV2ItemById was refused. $status is the board's status field name.
const newBoardItemsQuery = `query IssueBoardItems($issue: ID!, $status: String!) {
  node(id: $issue) {
    ... on Issue {
      projectItems(first: 20) {
        nodes {
          id
          isArchived
          project { id }
          fieldValueByName(name: $status) { ... on ProjectV2ItemFieldSingleSelectValue { name } }
        }
      }
    }
  }
}`

// newSetStatusMutation sets the status of a board item.
const newSetStatusMutation = `mutation SetStatus($project: ID!, $item: ID!, $field: ID!, $option: String!) {
  updateProjectV2ItemFieldValue(input: {projectId: $project, itemId: $item, fieldId: $field, value: {singleSelectOptionId: $option}}) {
    projectV2Item { id }
  }
}`

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
			if err := checkNewFlags(cfg, o); err != nil {
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
const blockedByFlag = "blocked-by"

// parentFlag is new's flag naming the parent issue.
const parentFlag = "parent"

// assigneeFlag is new's flag naming the users to assign, as a verb's
// --assignee.
const assigneeFlag = config.ArgAssignee

// maxAssignees is how many assignees GitHub allows on an issue.
const maxAssignees = 10

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
			switch {
			case login == "":
				continue
			case login == config.Me || config.IsLogin(login):
			case strings.HasPrefix(login, "@"):
				return nil, render.Usage("--%s %q: write the login without @, or %s for yourself", assigneeFlag, login, config.Me).
					WithFix(help)
			default:
				return nil, render.Usage("--%s %q is not a GitHub login; pass a login, or %s for yourself", assigneeFlag, login, config.Me).
					WithFix(help)
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
	if len(out) > maxAssignees {
		return nil, render.Usage("--%s names %d users; GitHub allows %d assignees per issue", assigneeFlag, len(out), maxAssignees).
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
	switch {
	case err != nil:
		return "issue type: a label from labels.types, or any repo label outside the areas when it is empty"
	case len(cfg.Labels.Types) > 0:
		return "issue type: one of " + strings.Join(cfg.Labels.Types, ", ")
	case cfg.Labels.AreaPrefix != "":
		return "issue type: any repo label not starting with " + cfg.Labels.AreaPrefix
	}
	return "issue type: any repo label"
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

Labels: --type takes one of labels.types, or any repo label outside the areas
when labels.types is empty; --area X adds the label <labels.area_prefix>X;
--label adds any existing label. An unknown one fails before anything is
created, listing the allowed values.

--milestone puts the issue in an open milestone of the repo, by title
(ignoring case) or a start only its title has: v0.3 for "v0.3 — extensibility".
An unknown or ambiguous one fails before anything is created, listing the open
milestones or the candidates. --milestone none is no milestone, as for a verb.

--assignee assigns users by login, @me for yourself (repeatable, or
comma-separated; GitHub allows 10). One who cannot be assigned issues in
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
that finds it on one of the repo's 20 newest open issues, written by you,
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
	switch {
	case len(cfg.Labels.Types) > 0:
		fmt.Fprintf(&b, "\n  --type    %s", strings.Join(cfg.Labels.Types, ", "))
	case cfg.Labels.AreaPrefix != "":
		fmt.Fprintf(&b, "\n  --type    any repo label not starting with %s", cfg.Labels.AreaPrefix)
	default:
		b.WriteString("\n  --type    any repo label")
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
		stale := r.schemaCached && board.IsStale(err)
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

// result is the run as JSON.
func (r *newRun) result() newResult {
	title := r.title
	if title == "" {
		title = r.opts.Title
	}
	var number *int
	if r.number != 0 {
		number = &r.number
	}
	return newResult{
		Number: number, Repo: r.repo.String(), Title: singleLine(title), DryRun: r.opts.DryRun, Created: !r.resumed,
		Labels: orEmpty(r.labels), Status: orNull(r.status), StatusSet: r.statusSet, Unarchived: r.unarchived,
		Milestone: milestoneJSON(r.milestone), MilestoneSet: r.milestoneSet,
		BlockedBy: refList(r.linked, r.def), AlreadyBlockedBy: refList(r.linkedFound, r.def),
		Parent: r.parentJSON(), ParentSet: r.parentSet,
		Assigned: orEmpty(r.assigned), AlreadyAssigned: orEmpty(r.assignedFound),
	}
}

// parentJSON is the issue's parent in full, owner/name#12; nil for none.
func (r *newRun) parentJSON() *string {
	if r.parent == nil {
		return nil
	}
	return orNull(fullRef(r.parent.In(r.def)))
}

// checkNewFlags rejects, without a request, a --type outside labels.types
// and --area without labels.area_prefix.
func checkNewFlags(cfg *config.Config, o newOptions) error {
	if len(o.Areas) > 0 && cfg.Labels.AreaPrefix == "" {
		return cfg.Errorf("--area needs labels.area_prefix, which is not set; pass the label with --label instead").
			WithFix("gh dojo new --help")
	}
	if o.Type == "" || len(cfg.Labels.Types) == 0 {
		return nil
	}
	if slices.ContainsFunc(cfg.Labels.Types, func(t string) bool { return strings.EqualFold(t, o.Type) }) {
		return nil
	}
	return render.Usage("unknown type %q", o.Type).WithAllowed(cfg.Labels.Types...).
		WithFix("gh dojo new --help")
}

// newDedupKey is what new's marker hashes: the title with whitespace
// collapsed and case folded.
func newDedupKey(title string) string {
	return strings.ToLower(strings.Join(strings.Fields(title), " "))
}

// newRun is one attempt of new; its fields after run say how far it got.
type newRun struct {
	*App
	cfg  *config.Config
	opts newOptions

	schemaCached bool     // the board schema came from the cache
	warnings     []string // for stderr

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
	archived   bool   // the board item was archived when found
	unarchived bool   // and this run restored it
	status     string // the status found, "" for none
	statusSet  bool   // the status was set to the initial one now
	noInitial  bool   // statuses.initial is empty: no status was set

	milestone     *newMilestone // the issue's milestone, nil for none
	milestoneSet  bool          // set to --milestone now
	milestoneKept string        // the --milestone not applied: a resumed issue had another

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
	r.schemaCached = s.Cached
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
	nc, err := readNewContext(ctx, r.Client, repo, statusField.Name, r.opts.Blockers, r.opts.Parent, r.opts.Assignees)
	if err != nil {
		return err
	}
	labels, err := nc.resolve(ctx, r.Client, r.cfg, repo, r.opts)
	if err != nil {
		return err
	}
	var milestone *newMilestone
	if r.opts.Milestone != "" {
		if milestone, err = nc.milestone(ctx, r.Client, repo, r.opts.Milestone); err != nil {
			return err
		}
	}
	assignees, err := nc.assignees(ctx, r.Client, repo, r.opts.Assignees)
	if err != nil {
		return err
	}
	if err := issue.LinksError("--"+blockedByFlag, nc.Links, def); err != nil {
		return err
	}
	if err := parentError(nc.Parent, def); err != nil {
		return err
	}
	for _, l := range labels {
		r.labels = append(r.labels, l.Name)
	}

	marker := workflow.Marker(newMarkerVerb, newDedupKey(r.opts.Title))
	found := nc.find(marker, s.ID)
	// A parent with as many sub-issues as GitHub allows takes no more: no
	// re-run would help, so nothing is written.
	if p := nc.Parent; p != nil && p.SubIssues >= issue.MaxSubIssues &&
		(found == nil || found.parentID == "" && !found.parentHidden) {
		return render.Usage("--%s %s has %d sub-issues, as many as GitHub allows", parentFlag, p.Ref.String(def), p.SubIssues).
			WithFix("pass another parent, or drop --" + parentFlag)
	}
	if found != nil {
		for _, l := range nc.Links {
			switch {
			case l.ID == found.ID:
				return selfBlock(issue.Ref{Repo: repo, Number: found.Number}, def, "--"+blockedByFlag)
			case l.BlockedBy(found.ID):
				return cycleError(issue.Ref{Repo: repo, Number: found.Number}, def, l.Ref.String(def), "--"+blockedByFlag)
			}
		}
		if p := nc.Parent; p != nil && p.ID == found.ID {
			name := issue.Ref{Repo: repo, Number: found.Number}.String(def)
			return render.Usage("--%s names %s itself; an issue cannot be its own parent", parentFlag, name).
				WithFix("pass the issue " + name + " is a part of")
		}
		// An issue with as many assignees as GitHub allows takes no more: no
		// re-run would help, so nothing is written.
		if missing, _ := found.missingAssignees(assignees); len(missing) > 0 && found.assigneesTotal+len(missing) > maxAssignees {
			name := issue.Ref{Repo: repo, Number: found.Number}.String(def)
			return render.Usage("%s has %d assignees; assigning %s would make more than the %d GitHub allows",
				name, found.assigneesTotal, joinWords(loginsOf(missing), "and"), maxAssignees).
				WithFix(fmt.Sprintf("unassign someone first (gh issue edit %d -R %s --remove-assignee <login>), or drop --%s",
					found.Number, repo, assigneeFlag))
		}
		r.resumed = true
	} else if !r.opts.DryRun {
		if err := r.create(ctx, nc.RepoID, withMarker(r.opts.Body, marker), labels, milestone, nc.Parent, assignees); err != nil {
			if r.number != 0 || !errors.Is(err, graph.ErrUnknownOutcome) {
				return err
			}
			// The answer was lost: look once whether the issue was created.
			if found = r.recheck(ctx, repo, statusField.Name, marker, s.ID); found == nil {
				return err
			}
			r.warnings = append(r.warnings, fmt.Sprintf("GitHub's answer to creating the issue was lost; found %s on a re-read", issue.Ref{Repo: repo, Number: found.Number}.String(def)))
		}
	}
	var missing []string // label IDs the found issue lacks
	setMilestone := false
	var addParent *issue.Linked // the --parent a found issue lacks
	var assign []*issue.User    // the --assignee users a found issue lacks
	if found != nil {
		r.number, r.id, r.title = found.Number, found.ID, found.title
		r.itemID, r.archived, r.status = found.itemID, found.archived, found.status
		r.milestone = found.milestone
		// Assignees it has stay: --assignee only adds.
		assign, r.assignedFound = found.missingAssignees(assignees)
		for _, l := range labels {
			if !slices.ContainsFunc(found.labels, func(n string) bool { return strings.EqualFold(n, l.Name) }) {
				missing = append(missing, l.ID)
				r.labelsMissing = append(r.labelsMissing, l.Name)
			}
		}
		// A milestone the issue already has stays, like its status: someone
		// may have set it on purpose.
		switch {
		case milestone == nil:
		case found.milestone == nil:
			setMilestone = true
		case found.milestone.Title != milestone.Title:
			r.milestoneKept = milestone.Title
			r.warnings = append(r.warnings, fmt.Sprintf(
				"#%d already has milestone %q; kept it, --milestone %q not applied (to change it: %s)",
				r.number, found.milestone.Title, milestone.Title, r.milestoneFix(repo, milestone.Title)))
		}
		// So does a parent, like the milestone.
		if p := nc.Parent; p != nil {
			r.parent = found.parent
			switch {
			case found.parentID == p.ID:
			case found.parentHidden:
				// It has a parent the token cannot read: addSubIssue would
				// fail, or move it from a parent nobody here can see.
				r.parentKept, r.parentHidden = &p.Ref, true
				r.warnings = append(r.warnings, fmt.Sprintf(
					"%s has a parent the token cannot read; kept it, --%s %s not applied (to see it: gh issue view %d -R %s)",
					r.name(), parentFlag, p.Ref.Arg(def), r.number, repo))
			case found.parentID == "":
				addParent = p
			default:
				r.parentKept = &p.Ref
				// gh takes the parent's number in the issue's repository,
				// else its URL.
				arg := fmt.Sprintf("https://%s/%s/issues/%d", clientHost(), p.Ref.Repo, p.Ref.Number)
				if p.Ref.Same(issue.Ref{Repo: repo, Number: p.Ref.Number}) {
					arg = strconv.Itoa(p.Ref.Number)
				}
				r.warnings = append(r.warnings, fmt.Sprintf(
					"%s is already a sub-issue of %s; kept it, --%s %s not applied (to move it: gh issue edit %d -R %s --parent %s)",
					r.name(), r.parentName(found.parent), parentFlag, p.Ref.Arg(def), r.number, repo, arg))
			}
		}
	}
	if r.opts.DryRun {
		r.planDry(milestone, setMilestone, initial, nc.Links, nc.Parent, addParent, assignees, assign)
		return nil
	}

	if len(missing) > 0 {
		vars := map[string]any{"issue": r.id, "labels": missing}
		if err := r.Client.Mutate(ctx, newAddLabelsMutation, vars, nil); err != nil {
			return &newStepError{step: "adding labels " + strings.Join(r.labels, ", ") + " failed", err: err}
		}
	}
	if setMilestone {
		vars := map[string]any{"issue": r.id, "milestone": milestone.ID}
		if err := r.Client.Mutate(ctx, newSetMilestoneMutation, vars, nil); err != nil {
			return &newStepError{step: "setting milestone " + singleLine(milestone.Title) + " failed", err: err}
		}
		r.milestone, r.milestoneSet = milestone, true
	}
	if r.archived {
		vars := map[string]any{"project": s.ID, "item": r.itemID}
		if err := r.Client.Mutate(ctx, newUnarchiveMutation, vars, nil); err != nil {
			return &newStepError{step: "unarchiving it on board " + s.Ref().String() + " failed", err: err}
		}
		r.unarchived = true
	}
	if r.itemID == "" {
		if err := r.addToBoard(ctx, s.ID, statusField.Name); err != nil {
			return &newStepError{step: "adding it to board " + s.Ref().String() + " failed", err: err}
		}
	}
	// A resumed issue keeps a status it already has: someone may have
	// moved it on purpose.
	if initial != nil && r.resumed && r.status != "" {
		initial = nil
	}
	var pending []*issue.Linked // links the issue lacks
	for _, l := range nc.Links {
		if l.Blocks(r.id) {
			r.linkedFound = append(r.linkedFound, l)
		} else {
			pending = append(pending, l)
		}
	}
	if len(pending) > 0 || addParent != nil || len(assign) > 0 {
		if err := r.finish(ctx, s.ID, statusField.ID, initial, assign, pending, addParent); err != nil {
			return err
		}
	} else if initial != nil {
		vars := map[string]any{"project": s.ID, "item": r.itemID, "field": statusField.ID, "option": initial.ID}
		if err := r.Client.Mutate(ctx, newSetStatusMutation, vars, nil); err != nil {
			return &newStepError{step: "setting status " + initial.Name + " failed", err: err}
		}
		r.status, r.statusSet = initial.Name, true
	}
	if r.parentErr != nil {
		// The rest is done; createIssue refused the parent for good.
		return &newStepError{step: "making it a sub-issue of " + nc.Parent.Ref.String(def) + " failed", err: r.parentErr, parent: true}
	}
	return nil
}

// newFinishMutation sets the status (status), assigns $assignees
// (assign), adds n blocked-by links of $issue, one aliased field each, and
// makes $issue a sub-issue of $parent (parent) (see newRun.finish).
func newFinishMutation(status, assign bool, n int, parent bool) string {
	decls := []string{"$issue: ID!"}
	var fields []string
	if status {
		decls = append(decls, "$project: ID!", "$item: ID!", "$field: ID!", "$option: String!")
		fields = append(fields, "  status: updateProjectV2ItemFieldValue(input: {projectId: $project, itemId: $item, "+
			"fieldId: $field, value: {singleSelectOptionId: $option}}) { projectV2Item { id } }")
	}
	if assign {
		decls = append(decls, "$assignees: [ID!]!")
		fields = append(fields, "  assign: addAssigneesToAssignable(input: {assignableId: $issue, assigneeIds: $assignees}) { clientMutationId }")
	}
	for i := range n {
		decls = append(decls, "$"+linkAlias(i)+": ID!")
		fields = append(fields, "  "+linkAlias(i)+": "+effects.BlockedByField(true, "issue", linkAlias(i)))
	}
	if parent {
		decls = append(decls, "$parent: ID!")
		fields = append(fields, "  parent: addSubIssue(input: {issueId: $parent, subIssueId: $issue}) { issue { id } }")
	}
	return "mutation NewFinish(" + strings.Join(decls, ", ") + ") {\n" + strings.Join(fields, "\n") + "\n}"
}

// finish sets the status initial (nil leaves it alone), assigns the users
// assign, adds the blocked-by links and makes the issue a sub-issue of
// parent (nil for none) in one mutation, the status first:
//
//	mutation NewFinish($issue: ID!, $project: ID!, $item: ID!, $field: ID!, $option: String!, $assignees: [ID!]!, $link0: ID!) {
//	  status: updateProjectV2ItemFieldValue(...) { projectV2Item { id } }
//	  assign: addAssigneesToAssignable(input: {assignableId: $issue, assigneeIds: $assignees}) { clientMutationId }
//	  link0: addBlockedBy(input: {issueId: $issue, blockingIssueId: $link0}) { issue { id } }
//	  parent: addSubIssue(input: {issueId: $parent, subIssueId: $issue}) { issue { id } }
//	}
//
// What took effect is recorded (r.statusSet, r.assigned, r.linked,
// r.parentSet) even when the rest failed; an error after everything took
// effect is only a warning.
func (r *newRun) finish(ctx context.Context, projectID, fieldID string, initial *board.Option, assign []*issue.User, links []*issue.Linked, parent *issue.Linked) error {
	vars := map[string]any{"issue": r.id}
	if parent != nil {
		vars["parent"] = parent.ID
	}
	if len(assign) > 0 {
		vars["assignees"] = userIDs(assign)
	}
	for i, l := range links {
		vars[linkAlias(i)] = l.ID
	}
	if initial != nil {
		vars["project"], vars["item"], vars["field"], vars["option"] = projectID, r.itemID, fieldID, initial.ID
	}
	var data map[string]json.RawMessage
	err := r.Client.Mutate(ctx, newFinishMutation(initial != nil, len(assign) > 0, len(links), parent != nil), vars, &data)
	var steps []string
	if initial != nil {
		if raw, ok := data["status"]; ok && !isJSONNull(raw) {
			r.status, r.statusSet = initial.Name, true
		} else {
			steps = append(steps, "setting status "+initial.Name)
		}
	}
	if len(assign) > 0 {
		if raw, ok := data["assign"]; ok && !isJSONNull(raw) {
			r.assigned = loginsOf(assign)
		} else {
			steps = append(steps, "assigning "+joinWords(loginsOf(assign), "and"))
		}
	}
	var missed []*issue.Linked
	for i, l := range links {
		if raw, ok := data[linkAlias(i)]; ok && !isJSONNull(raw) {
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
		if raw, ok := data["parent"]; ok && !isJSONNull(raw) {
			r.parent, r.parentSet = &parent.Ref, true
		} else {
			steps = append(steps, "making it a sub-issue of "+parent.Ref.String(r.def))
			parentMissed = true
		}
	}
	if parentMissed && len(steps) == 1 && parentRefused(err, "parent", false) {
		return &newStepError{step: steps[0] + " failed", err: err, parent: true}
	}
	switch {
	case len(steps) == 0 && err != nil:
		r.warnings = append(r.warnings, "GitHub reported an error after applying every change: "+reason(err))
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
	var gqlErr *api.GraphQLError
	if errors.Is(err, graph.ErrUnknownOutcome) || !errors.As(err, &gqlErr) || gqlErr == nil || len(gqlErr.Errors) == 0 {
		return false
	}
	for _, item := range gqlErr.Errors {
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
// a lost answer is followed by one re-read of the issue's board items: the
// item may be there all the same, added by GitHub or the board's automation.
func (r *newRun) addToBoard(ctx context.Context, projectID, statusField string) error {
	var out struct {
		Add *struct {
			Item *struct {
				ID string `json:"id"`
			} `json:"item"`
		} `json:"addProjectV2ItemById"`
	}
	vars := map[string]any{"project": projectID, "issue": r.id}
	err := r.Client.Mutate(ctx, newAddToBoardMutation, vars, &out)
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
	it := r.recheckItem(ctx, projectID, statusField)
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

// recheckItem re-reads the issue's board items after a refused
// addProjectV2ItemById and returns its unarchived item on projectID, or nil
// when there is none or the read fails.
func (r *newRun) recheckItem(ctx context.Context, projectID, statusField string) *newItemJSON {
	if !recheckPause(ctx) {
		return nil
	}
	var out struct {
		Node *struct {
			ProjectItems struct {
				Nodes []*newItemJSON `json:"nodes"`
			} `json:"projectItems"`
		} `json:"node"`
	}
	vars := map[string]any{"issue": r.id, "status": statusField}
	if err := r.Client.Query(ctx, newBoardItemsQuery, vars, &out); err != nil || out.Node == nil {
		return nil
	}
	for _, it := range out.Node.ProjectItems.Nodes {
		if it != nil && !it.IsArchived && it.Project != nil && it.Project.ID == projectID && it.ID != "" {
			return it
		}
	}
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

// recheck re-reads the newest issues after a lost createIssue answer and
// returns the issue with marker, or nil when it is not there or the read
// fails.
func (r *newRun) recheck(ctx context.Context, repo issue.Repo, statusField, marker, projectID string) *foundIssue {
	if !recheckPause(ctx) {
		return nil
	}
	nc, err := readNewContext(ctx, r.Client, repo, statusField, nil, r.opts.Parent, r.opts.Assignees)
	if err != nil {
		return nil
	}
	return nc.find(marker, projectID)
}

// create sends createIssue, with milestone and parent unless nil, and the
// assignees. On an
// error with the issue in the payload (a part of the input GitHub refused)
// the issue exists: r.number is set and the error is returned for a
// partial result.
func (r *newRun) create(ctx context.Context, repoID, body string, labels []newLabel, milestone *newMilestone, parent *issue.Linked, assignees []*issue.User) error {
	ids := make([]string, len(labels))
	for i, l := range labels {
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
	err := r.Client.Mutate(ctx, newCreateMutation, vars, &out)
	if out.Create == nil || out.Create.Issue == nil || out.Create.Issue.Number == 0 {
		if err == nil {
			err = render.Internal("createIssue returned no issue")
		}
		return err
	}
	is := out.Create.Issue
	r.number, r.id = is.Number, is.ID
	if err != nil && parent != nil && parentRefused(err, "createIssue", true) {
		// The issue exists with the rest: the run goes on, and ends with
		// this refusal (see run).
		r.parentErr = err
		r.milestone, r.milestoneSet = milestone, milestone != nil
		r.assigned = loginsOf(assignees)
		return nil
	}
	if err != nil {
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
		return &newStepError{step: "GitHub refused part of the request (" + joinWords(parts, "or") + ")", err: err}
	}
	r.milestone, r.milestoneSet = milestone, milestone != nil
	if parent != nil {
		r.parent, r.parentSet = &parent.Ref, true
	}
	if len(assignees) > 0 {
		r.assigned = loginsOf(assignees)
	}
	return nil
}

// joinWords lists words as "a", "a or b", "a, b or c".
func joinWords(words []string, conj string) string {
	if len(words) <= 1 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " " + conj + " " + words[len(words)-1]
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
		effects = append(effects, "assigned "+joinWords(r.assigned, "and"))
	}
	if len(r.assignedFound) > 0 {
		effects = append(effects, "already assigned "+joinWords(r.assignedFound, "and"))
	}
	if r.milestone != nil {
		m := "milestone " + singleLine(r.milestone.Title)
		if r.milestoneKept != "" {
			m += " (kept, not " + singleLine(r.milestoneKept) + ")"
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
	title := r.title
	if title == "" {
		title = r.opts.Title
	}
	return render.Confirm(r.name(), title, effects...)
}

// planDry fills the run's fields as a run that writes would leave them,
// for a dry run: the issue created with the labels, milestone, parent and
// assignees, or the resumed one finished as run would finish it (assign:
// the assignees it lacks); on the board,
// unarchived, in the initial status unless a resumed issue has one, and
// blocked by links, those it lacks (linked) apart from those it has
// (linkedFound).
func (r *newRun) planDry(milestone *newMilestone, setMilestone bool, initial *board.Option, links []*issue.Linked, parent, addParent *issue.Linked, assignees, assign []*issue.User) {
	if !r.resumed {
		r.assigned = loginsOf(assignees)
		r.milestone, r.milestoneSet = milestone, milestone != nil
		if parent != nil {
			r.parent, r.parentSet = &parent.Ref, true
		}
		r.linked = links
	} else {
		r.assigned = loginsOf(assign)
		if setMilestone {
			r.milestone, r.milestoneSet = milestone, true
		}
		if addParent != nil {
			r.parent, r.parentSet = &addParent.Ref, true
		}
		for _, l := range links {
			if l.Blocks(r.id) {
				r.linkedFound = append(r.linkedFound, l)
			} else {
				r.linked = append(r.linked, l)
			}
		}
	}
	r.added = r.itemID == ""
	r.unarchived = r.archived
	if initial != nil && (!r.resumed || r.status == "") {
		r.status, r.statusSet = initial.Name, true
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
		line("assignees: " + joinWords(r.assigned, "and"))
	case len(r.assigned) > 0 && len(r.assignedFound) > 0:
		line("assignees: +" + strings.Join(r.assigned, " +") + " (" + joinWords(r.assignedFound, "and") + " already assigned)")
	case len(r.assigned) > 0:
		line("assignees: +" + strings.Join(r.assigned, " +"))
	case len(r.assignedFound) > 0:
		line("assignees: " + joinWords(r.assignedFound, "and") + " already assigned")
	}
	switch {
	case r.milestoneKept != "" && r.milestone != nil:
		line("milestone: " + singleLine(r.milestone.Title) + " kept, not " + singleLine(r.milestoneKept))
	case r.milestoneSet && r.resumed:
		line("milestone: none → " + singleLine(r.milestone.Title))
	case r.milestoneSet:
		line("milestone: " + singleLine(r.milestone.Title))
	case r.opts.Milestone != "" && r.milestone != nil:
		line("milestone: " + singleLine(r.milestone.Title) + " already set")
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
		var gqlErr *api.GraphQLError
		forbidden := false
		if errors.As(cause, &gqlErr) && gqlErr != nil && len(gqlErr.Errors) > 0 {
			why = "GitHub: " + singleLine(gqlErr.Errors[0].Message)
			forbidden = slices.ContainsFunc(gqlErr.Errors, func(i api.GraphQLErrorItem) bool { return i.Type == "FORBIDDEN" })
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
	if e.Fix != "" && e.Fix != render.InternalFix {
		fix = e.Fix + "; then re-run the same gh dojo new command"
	}
	return render.Partial("%s %s, but %s\n%s", r.name(), what, step, why).WithFix(fix).WithCause(err)
}

// newContext is what newContextQuery read.
type newContext struct {
	RepoID    string
	Labels    graph.Connection[newLabel]
	Issues    []*newIssueJSON
	allLabels []newLabel // all repo labels, once read past the first page
	// Milestones are the repo's open milestones: the first page.
	Milestones    graph.Connection[newMilestone]
	allMilestones []newMilestone // all of them, once read past the first page
	// Links are the --blocked-by issues, in order (see issue.Links).
	Links []*issue.Linked
	// Parent is the --parent issue, read as a link; nil without --parent.
	Parent *issue.Linked
	// Assignable are the assignable users read for each --assignee login
	// but config.Me, in the order of newAssigneeLogins; Viewer is the
	// authenticated user, read for config.Me.
	Assignable [][]*issue.User
	Viewer     *issue.User
}

type newLabel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// newMilestone is a repo milestone: an open one (see issue.OpenMilestone),
// or the milestone of a recent issue, whose ID is not read.
type newMilestone = issue.OpenMilestone

// milestoneJSON is the milestone in new's JSON result, nil for none.
func milestoneJSON(m *newMilestone) *jsonMilestone {
	if m == nil {
		return nil
	}
	out := &jsonMilestone{Title: singleLine(m.Title)}
	if m.DueOn != nil {
		if t, err := time.Parse(time.RFC3339, *m.DueOn); err == nil {
			out.DueOn = orNull(cardDay(t))
		}
	}
	return out
}

type newItemJSON struct {
	ID         string `json:"id"`
	IsArchived bool   `json:"isArchived"`
	Project    *struct {
		ID string `json:"id"`
	} `json:"project"`
	Status *struct {
		Name string `json:"name"`
	} `json:"fieldValueByName"`
}

type newIssueJSON struct {
	ID              string `json:"id"`
	Number          int    `json:"number"`
	Title           string `json:"title"`
	Body            string `json:"body"`
	ViewerDidAuthor bool   `json:"viewerDidAuthor"`
	Labels          struct {
		Nodes []*struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	Milestone *newMilestone `json:"milestone"`
	// Parent is read with --parent only (newIssueParent).
	Parent *struct {
		ID         string `json:"id"`
		Number     int    `json:"number"`
		Repository *struct {
			NameWithOwner string `json:"nameWithOwner"`
		} `json:"repository"`
	} `json:"parent"`
	// Assignees are read with --assignee only (newIssueAssignees).
	Assignees *struct {
		TotalCount int `json:"totalCount"`
		Nodes      []*struct {
			Login string `json:"login"`
		} `json:"nodes"`
	} `json:"assignees"`
	ProjectItems struct {
		Nodes []*newItemJSON `json:"nodes"`
	} `json:"projectItems"`
	parentHidden bool // GitHub withheld its parent (hiddenParents)
}

// readNewContext reads what new needs before writing, with the issues of
// blockers (the --blocked-by ones) and parent (--parent, nil for none) as
// links, and with parent each recent issue's parent: a missing link is
// issue.Linked.Missing, not an error. With assignees (the --assignee
// logins) it reads who of them can be assigned in repo, the viewer for
// config.Me and each recent issue's assignees.
func readNewContext(ctx context.Context, q graph.Querier, repo issue.Repo, statusField string, blockers []issue.Ref, parent *issue.Ref, assignees []string) (*newContext, error) {
	links := issue.Links(slices.Clone(blockers))
	if parent != nil {
		links = append(links, *parent) // the last one
	}
	var r *struct {
		ID         string                         `json:"id"`
		Labels     graph.Connection[newLabel]     `json:"labels"`
		Milestones graph.Connection[newMilestone] `json:"milestones"`
		Issues     struct {
			Nodes []*newIssueJSON `json:"nodes"`
		} `json:"issues"`
	}
	vars := map[string]any{"owner": repo.Owner, "repo": repo.Name, "status": statusField, "window": newDedupWindow}
	links.AddVars(vars)
	logins, _ := newAssigneeLogins(assignees)
	for i, login := range logins {
		vars[assigneeAlias(i)] = login
	}
	var data map[string]json.RawMessage
	err := q.Query(ctx, newContextQueryWith(links, parent != nil, assignees), vars, &data)
	var assignable [][]*issue.User
	if raw, ok := data["repository"]; ok {
		if uerr := json.Unmarshal(raw, &r); uerr != nil {
			return nil, render.Internal("cannot decode GitHub's answer").WithCause(uerr)
		}
		if r != nil && len(logins) > 0 {
			var uerr error
			if assignable, uerr = decodeAssignable(raw, len(logins)); uerr != nil {
				return nil, render.Internal("cannot decode GitHub's answer").WithCause(uerr)
			}
		}
	}
	hidden, others := hiddenParents(err)
	if err != nil && r != nil && (others == nil || issue.OnlyLinkErrors(others)) {
		err = nil // a missing --blocked-by issue (LinksError names it), or a parent withheld
	}
	if r == nil && (err == nil || render.FromError(err).Code == render.ExitNotFound) {
		return nil, render.NotFound("repository %s not found, or the token cannot see it", repo).
			WithFix("gh repo view " + repo.String()).WithCause(err)
	}
	if err != nil {
		return nil, err
	}
	nc := &newContext{RepoID: r.ID, Labels: r.Labels, Milestones: r.Milestones, Issues: r.Issues.Nodes}
	for i, is := range nc.Issues {
		if is != nil && hidden[i] {
			is.parentHidden = true
		}
	}
	if len(links) > 0 {
		if nc.Links, err = links.Decode(data); err != nil {
			return nil, render.Internal("cannot decode GitHub's answer").WithCause(err)
		}
	}
	if parent != nil {
		nc.Links, nc.Parent = nc.Links[:len(blockers)], nc.Links[len(blockers)]
	}
	nc.Assignable = assignable
	if raw, ok := data["viewer"]; ok && !isJSONNull(raw) {
		var v issue.User
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, render.Internal("cannot decode GitHub's answer").WithCause(err)
		}
		if v.ID != "" {
			nc.Viewer = &v
		}
	}
	return nc, nil
}

// decodeAssignable decodes the n assignable-user lookups of a context
// read (assignee0, assignee1...) from the repository object raw; a lookup
// GitHub left out is empty.
func decodeAssignable(raw json.RawMessage, n int) ([][]*issue.User, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	out := make([][]*issue.User, n)
	for i := range n {
		f, ok := fields[assigneeAlias(i)]
		if !ok || isJSONNull(f) {
			continue
		}
		var c struct {
			Nodes []*issue.User `json:"nodes"`
		}
		if err := json.Unmarshal(f, &c); err != nil {
			return nil, err
		}
		out[i] = c.Nodes
	}
	return out, nil
}

// assignees resolves the --assignee logins to users who can be assigned
// issues in repo, in order and without duplicates (config.Me and the
// viewer's own login are one user): config.Me is the viewer, a login is
// matched among the assignable users read for it, ignoring case. Logins
// that cannot be assigned are one usage error (exit code 1) naming them
// all, with the repository's assignable users as the allowed values, which
// takes one more read.
func (nc *newContext) assignees(ctx context.Context, q graph.Querier, repo issue.Repo, assignees []string) ([]*issue.User, error) {
	var out []*issue.User
	var unknown []string
	add := func(u *issue.User) {
		if !slices.ContainsFunc(out, func(x *issue.User) bool { return x.ID == u.ID }) {
			out = append(out, u)
		}
	}
	i := 0 // index among the logins looked up
	for _, login := range assignees {
		if login == config.Me {
			if nc.Viewer == nil {
				return nil, render.Network("GitHub did not return the authenticated user for --%s %s", assigneeFlag, config.Me).
					WithFix(render.AuthFix)
			}
			add(nc.Viewer)
			continue
		}
		var found *issue.User
		if i < len(nc.Assignable) {
			for _, u := range nc.Assignable[i] {
				if u != nil && u.ID != "" && strings.EqualFold(u.Login, login) {
					found = u
					break
				}
			}
		}
		i++
		if found == nil {
			unknown = append(unknown, login)
			continue
		}
		add(found)
	}
	if len(unknown) == 0 {
		return out, nil
	}
	fix := fmt.Sprintf("check the login, or give %s access to %s", unknown[0], repo)
	if len(unknown) > 1 {
		fix = "check the logins, or give them access to " + repo.String()
	}
	e := render.Usage("--%s: %s cannot be assigned issues in %s", assigneeFlag, joinWords(unknown, "and"), repo).WithFix(fix)
	if logins, err := assignableUsers(ctx, q, repo); err == nil {
		e = e.WithAllowed(logins...)
	}
	return nil, e
}

// hiddenParents returns the indexes of the recent issues whose parent
// GitHub withheld from the token (NOT_FOUND or FORBIDDEN at
// repository.issues.nodes.<i>.parent), and err without those errors: nil
// when they were all of them, or when err is not a GraphQL error.
func hiddenParents(err error) (map[int]bool, error) {
	var gqlErr *api.GraphQLError
	if !errors.As(err, &gqlErr) || gqlErr == nil {
		return nil, err
	}
	hidden := map[int]bool{}
	var rest []api.GraphQLErrorItem
	for _, item := range gqlErr.Errors {
		p := item.Path
		if len(p) == 5 && p[0] == "repository" && p[1] == "issues" && p[2] == "nodes" && p[4] == "parent" &&
			(item.Type == "NOT_FOUND" || item.Type == "FORBIDDEN") {
			if i, ok := p[3].(float64); ok {
				hidden[int(i)] = true
				continue
			}
		}
		rest = append(rest, item)
	}
	if len(rest) == 0 {
		return hidden, nil
	}
	return hidden, &api.GraphQLError{Errors: rest}
}

// foundIssue is an issue an earlier run created, as find returns it.
type foundIssue struct {
	ID, itemID, status string
	title              string
	archived           bool
	Number             int
	labels             []string
	milestone          *newMilestone // nil for none
	// parentID is the node ID of its parent, "" for none, and parent names
	// it; read with --parent only.
	parentID string
	parent   *issue.Ref
	// parentHidden: it has a parent GitHub withheld from the token.
	parentHidden bool
	// assignees are the logins of its assignees, and assigneesTotal how
	// many it has; read with --assignee only.
	assignees      []string
	assigneesTotal int
}

// missingAssignees splits users into those f lacks, to assign, and the
// logins of those it has already.
func (f *foundIssue) missingAssignees(users []*issue.User) (missing []*issue.User, have []string) {
	for _, u := range users {
		if slices.ContainsFunc(f.assignees, func(l string) bool { return strings.EqualFold(l, u.Login) }) {
			have = append(have, u.Login)
		} else {
			missing = append(missing, u)
		}
	}
	return missing, have
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

// find returns the newest open issue the viewer wrote whose body ends with
// marker, with its item on the board projectID, or nil.
func (nc *newContext) find(marker, projectID string) *foundIssue {
	for i := len(nc.Issues) - 1; i >= 0; i-- {
		is := nc.Issues[i]
		if is == nil || !is.ViewerDidAuthor || !strings.HasSuffix(strings.TrimSpace(is.Body), marker) {
			continue
		}
		f := &foundIssue{ID: is.ID, Number: is.Number, title: is.Title, milestone: is.Milestone, parentHidden: is.parentHidden}
		if p := is.Parent; p != nil && p.ID != "" && p.Repository != nil {
			if repo, err := issue.ParseRepo(p.Repository.NameWithOwner); err == nil {
				f.parentID, f.parent = p.ID, &issue.Ref{Repo: repo, Number: p.Number}
			}
		}
		for _, l := range is.Labels.Nodes {
			if l != nil {
				f.labels = append(f.labels, l.Name)
			}
		}
		if a := is.Assignees; a != nil {
			for _, u := range a.Nodes {
				if u != nil {
					f.assignees = append(f.assignees, u.Login)
				}
			}
			f.assigneesTotal = max(a.TotalCount, len(f.assignees))
		}
		for _, it := range is.ProjectItems.Nodes {
			if it != nil && it.Project != nil && it.Project.ID == projectID {
				f.itemID, f.archived = it.ID, it.IsArchived
				if it.Status != nil {
					f.status = it.Status.Name
				}
			}
		}
		return f
	}
	return nil
}

// labels returns the repo labels: the first page read with the context, or
// all of them when there are more and more is asked.
func (nc *newContext) labels(ctx context.Context, q graph.Querier, repo issue.Repo, all bool) ([]newLabel, error) {
	if !all || !nc.Labels.PageInfo.HasNextPage {
		return nc.Labels.Nodes, nil
	}
	if nc.allLabels == nil {
		type resp struct {
			Repository *struct {
				Labels graph.Connection[newLabel] `json:"labels"`
			} `json:"repository"`
		}
		vars := map[string]any{"owner": repo.Owner, "repo": repo.Name}
		page, err := graph.Paginate(ctx, q, newLabelsQuery, vars, 0, func(r *resp) *graph.Connection[newLabel] {
			if r.Repository == nil {
				return nil
			}
			return &r.Repository.Labels
		})
		if err != nil {
			return nil, err
		}
		nc.allLabels = page.Nodes
	}
	return nc.allLabels, nil
}

// resolve maps --type, --area and --label to repo labels, without
// duplicates. An unknown one is a usage error with the allowed values.
func (nc *newContext) resolve(ctx context.Context, q graph.Querier, cfg *config.Config, repo issue.Repo, o newOptions) ([]newLabel, error) {
	prefix := cfg.Labels.AreaPrefix
	type want struct {
		flag, value, name string
	}
	var wants []want
	if o.Type != "" {
		name := o.Type
		for _, t := range cfg.Labels.Types {
			if strings.EqualFold(t, o.Type) {
				name = t
			}
		}
		wants = append(wants, want{"type", o.Type, name})
	}
	for _, a := range o.Areas {
		wants = append(wants, want{"area", a, prefix + a})
	}
	for _, l := range o.Labels {
		wants = append(wants, want{"label", l, l})
	}

	var out []newLabel
	for _, w := range wants {
		if strings.TrimSpace(w.value) == "" {
			return nil, render.Usage("--%s is empty", w.flag).WithFix("gh dojo new --help")
		}
		l, err := nc.lookup(ctx, q, repo, w.name)
		if err != nil {
			return nil, err
		}
		if l == nil {
			all, err := nc.labels(ctx, q, repo, true)
			if err != nil {
				return nil, err
			}
			return nil, unknownNewLabel(cfg, repo, w.flag, w.value, w.name, all)
		}
		if w.flag == "type" && len(cfg.Labels.Types) == 0 && prefix != "" && hasPrefixFold(l.Name, prefix) {
			return nil, render.Usage("--type %q is an area label; pass --area %s instead", w.value, l.Name[len(prefix):]).
				WithFix("gh dojo new --help")
		}
		if !slices.ContainsFunc(out, func(x newLabel) bool { return x.ID == l.ID }) {
			out = append(out, *l)
		}
	}
	return out, nil
}

// lookup finds a repo label by name, ignoring case as GitHub does.
func (nc *newContext) lookup(ctx context.Context, q graph.Querier, repo issue.Repo, name string) (*newLabel, error) {
	find := func(ls []newLabel) *newLabel {
		for i := range ls {
			if strings.EqualFold(ls[i].Name, name) {
				return &ls[i]
			}
		}
		return nil
	}
	if l := find(nc.Labels.Nodes); l != nil {
		return l, nil
	}
	all, err := nc.labels(ctx, q, repo, true)
	if err != nil {
		return nil, err
	}
	return find(all), nil
}

// unknownNewLabel is the error for a --type, --area or --label value with
// no matching repo label.
func unknownNewLabel(cfg *config.Config, repo issue.Repo, flag, value, name string, all []newLabel) error {
	prefix := cfg.Labels.AreaPrefix
	var allowed []string
	switch flag {
	case "type":
		if len(cfg.Labels.Types) > 0 {
			// The type is in labels.types (checkNewType), so the repo lacks it.
			return cfg.Errorf("labels.types has %q, but %s has no such label", name, repo).
				WithFix(fmt.Sprintf("gh label create %q -R %s", name, repo))
		}
		for _, l := range all {
			if prefix == "" || !hasPrefixFold(l.Name, prefix) {
				allowed = append(allowed, l.Name)
			}
		}
	case "area":
		for _, l := range all {
			if prefix != "" && hasPrefixFold(l.Name, prefix) {
				allowed = append(allowed, l.Name[len(prefix):])
			}
		}
		if len(allowed) == 0 {
			return render.Usage("unknown area %q: %s has no labels starting with %q", value, repo, prefix).
				WithFix("gh label list -R " + repo.String())
		}
	default:
		for _, l := range all {
			allowed = append(allowed, l.Name)
		}
	}
	return render.Usage("unknown %s %q: no such label in %s", flag, value, repo).WithAllowed(allowed...).
		WithFix("gh label list -R " + repo.String())
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// milestoneFix is the command that puts issue r.number of repo in the
// milestone title: a verb that only sets the --milestone milestone (see
// onlySetsMilestone) and starts from the issue's status (r.status, or
// statuses.initial without one), else gh issue edit.
func (r *newRun) milestoneFix(repo issue.Repo, title string) string {
	status := r.status
	if status == "" {
		status = r.cfg.Statuses.Initial
	}
	for _, v := range r.cfg.Verbs {
		if onlySetsMilestone(v) && (len(v.From) == 0 || slices.Contains(v.From, status)) {
			def, err := r.App.Repo()
			if err != nil {
				def = issue.Repo{}
			}
			return fmt.Sprintf("gh dojo %s %s --%s %q", v.Name, issue.Ref{Repo: repo, Number: r.number}.Arg(def),
				config.ArgMilestone, title)
		}
	}
	return fmt.Sprintf("gh issue edit %d -R %s --milestone %q", r.number, repo, title)
}

// onlySetsMilestone reports whether v does nothing but set the
// --milestone milestone: no status, comment, labels, assignees, close,
// reopen, sprint or fields; it needs nothing besides --milestone, not the
// user's go-ahead, and checks neither labels nor, to refuse, blockers or
// sub-issues. Such a verb is a safe way to change an issue's milestone.
func onlySetsMilestone(v config.Verb) bool {
	return v.MilestoneArg && v.To == "" && v.Comment == "" && len(v.AddLabels)+len(v.RemoveLabels) == 0 &&
		!v.Assigns() && v.Unassign == "" && v.Close == "" && !v.Reopen && v.Iteration == "" && len(v.Fields) == 0 &&
		!v.Confirm && slices.Equal(v.Require, []string{config.ArgMilestone}) && len(v.RequireOneOf) == 0 &&
		!v.ChecksLabels() && v.Blockers != config.BlockersDeny && v.SubIssues != config.BlockersDeny
}

// milestone returns the open milestone title names, matched as a verb's
// milestone effect matches it (see issue.MatchMilestone): the title
// regardless of case, else the only one it starts. An unknown or ambiguous
// title is a usage error listing the open milestones or the candidates.
func (nc *newContext) milestone(ctx context.Context, q graph.Querier, repo issue.Repo, title string) (*newMilestone, error) {
	all := nc.Milestones.Nodes
	if nc.Milestones.PageInfo.HasNextPage {
		if nc.allMilestones == nil {
			rest, err := issue.ReadMilestones(ctx, q, repo, nc.Milestones.PageInfo.EndCursor)
			if err != nil {
				return nil, err
			}
			nc.allMilestones = append(slices.Clone(nc.Milestones.Nodes), rest...)
		}
		all = nc.allMilestones
	}
	return issue.MatchMilestone(title, all, repo)
}
