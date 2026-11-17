package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/Trum-ok/gh-dojo/internal/batch"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/workflow"
	"github.com/spf13/cobra"
)

// Verbs are commands built from .github/dojo.toml.
//
// Config errors never break gh dojo itself: --help, --version and built-in
// commands keep working. When the config is missing or broken:
//
//   - no verb commands are registered;
//   - the root help ends with the config error (see configNote), so a broken
//     file is visible right where an agent looks for verbs;
//   - an unknown command that is not close to a built-in one (maybe a verb)
//     fails with "unknown command" plus the first line of the reason, the
//     built-ins as allowed values and gh dojo --help as the fix (see
//     verbsUnavailable and unknownCommand);
//   - built-in commands that need the config load it themselves through
//     loadConfig and return its error.

func init() { registerAll(newVerbCmds) }

// verbsGroup lists the verbs apart from built-in commands in the root help.
var verbsGroup = &cobra.Group{ID: "verbs", Title: "Process verbs (from " + config.FileName + "):"}

// loadConfig loads the config of the current directory, once per process.
// Tests replace it.
var loadConfig = sync.OnceValues(func() (*config.Config, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, render.Usage("cannot get the current directory: %v", err).WithCause(err)
	}
	return config.Load(wd)
})

// getenv reads the agent session variables. Tests replace it.
var getenv = os.Getenv

// Backend reads issues and applies plans; it is the only part of a verb that
// talks to GitHub. The GitHub one is githubBackend (backend.go).
//
// Requests: one query for ReadIssue and one mutation for Apply, so 2 with a
// warm board cache. An issue not on the board needs a second mutation:
// addProjectV2ItemById returns the item ID that the status update needs, so
// that case takes 3 requests.
//
// Retries: after a stale ID or a partial failure, the way to resume is to
// run ReadIssue → Plan → Apply again, never to replay the old plan: the
// fresh plan leaves out what is already done, including a comment already
// posted (see workflow.AlreadyPosted). runVerb does that once on a stale
// schema ID, also dropping what Apply confirmed in case the re-read lags
// behind it; after a partial failure the agent re-runs the command.
type Backend interface {
	// ReadIssues, Apply and Reset as batch runs them; githubBackend
	// documents what it reads and writes.
	batch.Backend
	// ReadIssue returns the state of issue ref for verb v, filled as
	// workflow.Issue documents (comments with viewerDidAuthor, labels,
	// assignees, and the viewer when v assigns or unassigns @me), with a
	// render error on failure (render.NotFound for a missing issue; a
	// config error for a label of v to add that is missing in the repo, a
	// label spelled in another case than the repo's, or an assignee who
	// cannot be assigned). The issue's repository is ref's, which a caller
	// resolves with Repo for a bare number.
	//
	// For a verb with blockers = "warn" or "deny" it fills the unresolved
	// blockers; for blockers = "link" with --by naming issues (a), those
	// issues (workflow.Issue.ByIssues), with exit code 3 when one does not
	// exist, and 1 when one is the issue itself, would close a cycle, or
	// --by mixes issues and text.
	ReadIssue(ctx context.Context, v config.Verb, ref issue.Ref, a workflow.Args) (workflow.Issue, error)
	// Repo is the default repository: where a bare number is, and what
	// output names issues relative to.
	Repo() (issue.Repo, error)
}

// newBackend returns the backend for cfg: GitHub through one App per run,
// which loads the same config. Tests replace it.
var newBackend = func(*config.Config) (Backend, error) { return &githubBackend{app: newApp()}, nil }

func newVerbCmds() ([]*cobra.Command, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, nil // see configNote and verbsUnavailable
	}
	cmds := make([]*cobra.Command, 0, len(cfg.Verbs))
	for _, v := range cfg.Verbs {
		cmds = append(cmds, newVerbCmd(cfg, v))
	}
	return cmds, nil
}

func newVerbCmd(cfg *config.Config, v config.Verb) *cobra.Command {
	var (
		args         workflow.Args
		dryRun, show bool
	)
	cmd := &cobra.Command{
		GroupID: verbsGroup.ID,
		Use:     v.Name + " <N>...",
		Short:   v.When,
		Long:    verbHelp(cfg, v),
		Args:    issueArgs,
		RunE: func(cmd *cobra.Command, posArgs []string) error {
			args.HasComment = cmd.Flags().Changed(config.ArgComment)
			args.HasBy = cmd.Flags().Changed(config.ArgBy)
			args.HasAssignee = cmd.Flags().Changed(config.ArgAssignee)
			args.HasMilestone = cmd.Flags().Changed(config.ArgMilestone)
			args.HasOf = cmd.Flags().Changed(config.ArgOf)
			refs := argRefs(posArgs, issue.Repo{}) // validated by issueArgs
			if len(refs) == 1 {
				return runVerb(cmd, cfg, v, refs[0], args, dryRun, show)
			}
			return runBatch(cmd, cfg, v, refs, args, dryRun, show)
		},
	}
	f := cmd.Flags()
	f.StringVar(&args.Comment, config.ArgComment, "", "text for {comment} in the comment")
	byUsage := "value for {by} in the comment: an issue number (12 or #12) or free text"
	if v.Blockers == config.BlockersLink {
		byUsage = "value for {by} in the comment: issues (12, #12, owner/repo#12 or URLs, comma-separated), also marked as blocking this one, or free text"
	}
	f.StringVar(&args.By, config.ArgBy, "", byUsage)
	f.StringVar(&args.Assignee, config.ArgAssignee, "", "the user to assign: a login, or @me for yourself")
	f.StringVar(&args.Milestone, config.ArgMilestone, "", "the open milestone to set, by `title` or a start only its title has; "+workflow.NoMilestone+" takes the issue out of its milestone")
	f.StringVar(&args.Of, config.ArgOf, "", "the original issue this one duplicates (17, #17, owner/repo#17 or a URL): closed as its duplicate and linked to it")
	f.BoolVar(&dryRun, dryRunFlag, false, "print the plan without changing anything")
	f.BoolVar(&show, showFlag, false, "also print the issue's card, as gh dojo show N does, from the same read")
	f.BoolVar(&args.Yes, "yes", false, "the user agreed: run a verb that is the user's decision")
	// Flags the verb does not take stay registered but hidden, so passing
	// one gets workflow's precise error instead of "unknown flag".
	for _, arg := range config.Args() {
		if !v.Takes(arg) {
			_ = f.MarkHidden(arg)
		}
	}
	if !v.Confirm {
		_ = f.MarkHidden("yes")
	}
	return cmd
}

// runVerb runs verb v on issue ref. An error that read nothing, such as
// bad arguments or the network, is the command's alone; once the issue
// was read (or found missing), its JSON is a verb result for it, as for
// one issue of a batch, also when it is refused (outcome "failed").
// show prints the issue's card too (see shownCard).
func runVerb(cmd *cobra.Command, cfg *config.Config, v config.Verb, ref issue.Ref, args workflow.Args, dryRun, show bool) error {
	be, def, err := verbBackend(cfg, v, args, ref.Arg(issue.Repo{}))
	if err != nil {
		return err
	}
	ref = ref.In(def)
	ctx := cmd.Context()
	engine := withGetenv(workflow.New(cfg))
	it, err := batch.ReadPlan(ctx, be, engine, v, ref, args)
	if err != nil {
		return err
	}
	repo := backendRepo(be)
	// The text of a failed issue is its error, and the card with --show.
	card := newShownCard(ctx, cfg, be, def, v, it, show)
	if it.Writes == nil {
		return emitResult(cmd, card.json(verbJSON(v.Name, repo, dryRun, []*batch.Item{it})), card.alone(false), it.Err)
	}
	run, plan := it.Writes, it.Writes.First
	run.Stderr = cmd.ErrOrStderr()
	for _, w := range plan.Warnings {
		render.Warn(cmd.ErrOrStderr(), "%s", w)
	}
	if dryRun {
		return emit(cmd, card.json(verbJSON(v.Name, repo, true, []*batch.Item{it})), func(w io.Writer) {
			fmt.Fprintln(w, plan.DryRun())
			card.write(w, true, false)
		})
	}
	if it.Err = plan.Unconfirmed(); it.Err != nil {
		return emitResult(cmd, card.json(verbJSON(v.Name, repo, false, []*batch.Item{it})), card.alone(false), it.Err)
	}
	it.Err = run.ApplyFresh(ctx, be, func() (*workflow.Plan, error) { return readPlan(ctx, be, engine, v, ref, args) })
	// A partial write prints what it applied as JSON; its text is the error.
	return emitWrite(cmd, card.json(verbJSON(v.Name, repo, false, []*batch.Item{it})), func(w io.Writer) {
		if it.Err != nil {
			card.write(w, false, it.Wrote())
			return
		}
		fmt.Fprintln(w, run.Applied().Confirm())
		card.write(w, true, it.Wrote())
	}, it.Err, it.Wrote())
}

// verbBackend checks the arguments of verb v on the issues named, as
// given, before anything is read, then returns the backend and its
// default repository.
func verbBackend(cfg *config.Config, v config.Verb, args workflow.Args, named ...string) (Backend, issue.Repo, error) {
	if err := workflow.CheckArgs(v, args, named...); err != nil {
		return nil, issue.Repo{}, err
	}
	be, err := newBackend(cfg)
	if err != nil {
		return nil, issue.Repo{}, err
	}
	def, err := be.Repo()
	if err != nil {
		return nil, issue.Repo{}, err
	}
	return be, def, nil
}

// withGetenv makes engine e read the session variables through getenv.
func withGetenv(e *workflow.Engine) *workflow.Engine {
	e.Getenv = getenv
	return e
}

// backendRepo is the default repository of be, owner/name, for the JSON;
// "" when not known.
func backendRepo(be Backend) string {
	r, err := be.Repo()
	if err != nil || r == (issue.Repo{}) {
		return ""
	}
	return r.String()
}

// readPlan reads issue ref and plans verb v on it.
func readPlan(ctx context.Context, be Backend, e *workflow.Engine, v config.Verb, ref issue.Ref, args workflow.Args) (*workflow.Plan, error) {
	is, err := be.ReadIssue(ctx, v, ref, args)
	if err != nil {
		return nil, err
	}
	return e.Plan(v, is, args)
}

// checkArgs checks issue arguments before anything is read: at least one,
// else "<command> needs <need>", and each as checkArg does, what being the
// example's noun.
func checkArgs(cmd *cobra.Command, args []string, need, what string) error {
	if len(args) == 0 {
		return render.Usage("%s needs %s, like %s", cmd.Name(), need, issueExamples).WithFix(cmd.CommandPath() + " --help")
	}
	for _, a := range args {
		if err := checkArg(cmd, a, what); err != nil {
			return err
		}
	}
	return nil
}

// issueArg accepts exactly one issue number.
func issueArg(cmd *cobra.Command, args []string) error {
	if len(args) > 1 {
		return render.Usage("%s takes one issue number, got %d; several at once are not supported yet",
			cmd.Name(), len(args)).WithFix(cmd.CommandPath() + " --help")
	}
	return checkArgs(cmd, args, "an issue number", "one")
}

// issueArgs accepts the issue numbers of a verb: one or more. A number
// repeated counts once; one number in different forms (38 and
// owner/repo#38) may be one issue of the default repository, which is not
// known yet: runBatch tells.
func issueArgs(cmd *cobra.Command, args []string) error {
	return checkArgs(cmd, args, "one or more issue numbers", "one")
}

// helpLine adds a line to a verb's help: label (without its colon, "" to
// continue the one above) in a column, then the text.
type helpLine func(label, format string, a ...any)

// labelVerbsHelp adds a help line per checked label that names the verbs
// adding it (remove false, for from_labels) or removing it (for
// refuse_labels): "resume removes it", "hold adds on-hold".
func labelVerbsHelp(cfg *config.Config, v config.Verb, labels []string, remove bool, line helpLine) {
	does := "adds"
	if remove {
		does = "removes"
	}
	for _, name := range labels {
		if verbs := labelVerbs(cfg, v, name, remove); len(verbs) > 0 {
			what := name
			if len(labels) == 1 {
				what = "it"
			}
			line("", "%s %s %s", render.JoinWords(verbs, "or"), does, what)
		}
	}
}

// verbHelp is the long help of a verb: its when line, then what it checks,
// needs and does.
func verbHelp(cfg *config.Config, v config.Verb) string {
	var b strings.Builder
	line := func(label, format string, a ...any) {
		if label != "" {
			label += ":"
		}
		fmt.Fprintf(&b, "\n  %-10s %s", label, fmt.Sprintf(format, a...))
	}
	b.WriteString(v.When + "\n")
	statusHelp(cfg, v, line)
	argsHelp(v, line)
	effectsHelp(cfg, v, line)
	if v.Comment != "" {
		line("Comment", "posted unless it renders empty or is already the latest gh-dojo comment:")
		// The template as it will read in the issue, indented below the
		// label; blank lines stay blank, without trailing spaces.
		for _, l := range strings.Split(strings.Trim(v.Comment, "\r\n"), "\n") {
			if l = strings.TrimRight(l, " \t\r"); l == "" {
				b.WriteString("\n")
			} else {
				line("", "  %s", l)
			}
		}
	}
	return b.String()
}

// statusHelp adds the verb help lines of the statuses it moves an issue
// from and to, and of the labels it needs or refuses.
func statusHelp(cfg *config.Config, v config.Verb, line helpLine) {
	from := "any status"
	if len(v.From) > 0 {
		from = strings.Join(v.From, ", ")
		if initial := cfg.Statuses.Initial; slices.Contains(v.From, initial) {
			from += fmt.Sprintf(" (no status or not on the board counts as %s)", initial)
		}
	}
	line("From", "%s", from)
	if v.To != "" {
		line("To", "%s; an issue already there keeps it, so a repeat is safe", v.To)
	} else {
		line("To", "the status is not changed")
	}
	if n := len(v.FromLabels); n > 0 {
		them := "it"
		if n > 1 {
			them = "them"
		}
		line("Needs", "%s on the issue; refused (exit code 2) without %s: ask the user",
			labelNames(v.FromLabels, "and", nil), them)
		labelVerbsHelp(cfg, v, v.FromLabels, false, line)
	}
	if len(v.RefuseLabels) > 0 {
		line("Refused", "while the issue has %s (exit code 2): ask the user", labelNames(v.RefuseLabels, "or", nil))
		labelVerbsHelp(cfg, v, v.RefuseLabels, true, line)
	}
}

// argsHelp adds the verb help lines of its arguments, batch and confirm.
func argsHelp(v config.Verb, line helpLine) {
	required, oneOf, optional := verbArgs(v)
	if len(oneOf) > 0 {
		required = append(required, "at least one of "+strings.Join(oneOf, ", "))
	}
	if len(required) > 0 {
		line("Requires", "%s", strings.Join(required, "; "))
	} else {
		line("Requires", "nothing besides the issue numbers")
	}
	if len(optional) > 0 {
		line("Optional", "%s", strings.Join(optional, ", "))
	}
	batchHelp(v, line)
	if v.Confirm {
		line("Confirm", "the user's decision: ask the user first; runs only with --yes")
	}
}

// effectsHelp adds the verb help lines of what it does along the way,
// except its comment.
func effectsHelp(cfg *config.Config, v config.Verb, line helpLine) {
	if v.OnlyClearsSprint() {
		line("Board", "leaves an issue that is not there off the board")
	} else {
		line("Board", "adds the issue if it is not there")
	}
	if v.Reopen {
		line("Reopen", "reopens a closed issue")
	}
	sprintHelp(cfg, v, line)
	switch {
	case v.MilestoneArg:
		line("Milestone", "the open milestone --%s names, by title or a start only its title has;", config.ArgMilestone)
		line("", "an issue in it already keeps it, so a repeat is safe")
		line("", "no such milestone, or several, is exit code 1, with the open ones or the candidates;")
		line("", "--%s %s takes the issue out of its milestone", config.ArgMilestone, workflow.NoMilestone)
	case v.Milestone != "":
		line("Milestone", "%s (an open milestone, by title or a start only its title has);", v.Milestone)
		line("", "an issue in it already keeps it, so a repeat is safe")
	}
	if signs := labelSigns(v); signs != "" {
		line("Labels", "%s (+ adds, - removes)", signs)
	}
	assigneesHelp(v, line)
	switch v.Blockers {
	case config.BlockersWarn:
		line("Blockers", "warns about unresolved blockers (%s), then runs", unresolvedMeaning(cfg))
	case config.BlockersDeny:
		line("Blockers", "refused (exit code 2) while a blocker is unresolved (%s): ask the user", unresolvedMeaning(cfg))
	case config.BlockersLink:
		line("Blockers", "--by naming issues (12, #12, owner/repo#12 or URLs, comma-separated) also marks this one "+
			"blocked by them; a pull request is only named in the comment")
		line("", "free text in --by goes into the comment as is: on an issue of another repository,")
		line("", "GitHub links a #N in it to that issue's repository, not to the default one")
	}
	switch v.SubIssues {
	case config.BlockersWarn:
		line("Children", "warns about unresolved sub-issues (%s), then runs", unresolvedMeaning(cfg))
	case config.BlockersDeny:
		line("Children", "refused (exit code 2) while a sub-issue is unresolved (%s): ask the user", unresolvedMeaning(cfg))
	}
	switch {
	case v.ClosesDuplicate():
		line("Close", "closes the issue as a duplicate of the --%s issue (17, #17, owner/repo#17 or a URL),", config.ArgOf)
		line("", "which GitHub links it to; an issue already closed stays as it is")
	case v.Close != "":
		line("Close", "closes the issue as %s; an issue already closed stays as it is", workflow.ReasonText(v.Close))
	}
}

// assigneesHelp adds the verb help lines of whom it assigns and unassigns.
func assigneesHelp(v config.Verb, line helpLine) {
	stay := "; other assignees stay"
	if v.Unassign != "" {
		stay = "" // see the Unassign line
	}
	if who := assignWho(v); who != "" {
		switch {
		case v.AssignArg:
			who += " (a login, or @me for you)"
		case v.Assign == config.Me:
			who += " (the authenticated user)"
		}
		line("Assign", "%s%s", who, stay)
		if v.AssignArg {
			line("", "one who cannot be assigned issues in the repository is exit code 1, with who can")
		}
	}
	switch who := unassignWho(v); {
	case who == "":
	case v.Unassign == config.UnassignAll && v.Assigns():
		line("Unassign", "%s: the user it assigns stays the only assignee", who)
	case v.Unassign == config.UnassignAll:
		line("Unassign", "%s: the issue is left with no assignees", who)
	case v.Unassign == config.Me:
		line("Unassign", "%s (the authenticated user); other assignees stay", who)
	default:
		line("Unassign", "%s; other assignees stay", who)
	}
}

// verbArgs are the flags of the arguments v requires, needs at least one
// of, and takes optionally, as "--comment". The required and optional
// ones are in config.Args order, the one-of ones in config order.
func verbArgs(v config.Verb) (required, oneOf, optional []string) {
	for _, arg := range config.Args() {
		switch {
		case slices.Contains(v.Require, arg):
			required = append(required, "--"+arg)
		case slices.Contains(v.RequireOneOf, arg):
		case v.Takes(arg):
			optional = append(optional, "--"+arg)
		}
	}
	for _, arg := range v.RequireOneOf {
		oneOf = append(oneOf, "--"+arg)
	}
	return required, oneOf, optional
}

// labelSigns lists the labels v adds and removes: "+doing -blocked"; ""
// for none.
func labelSigns(v config.Verb) string {
	var signs []string
	for _, l := range v.AddLabels {
		signs = append(signs, "+"+l)
	}
	for _, l := range v.RemoveLabels {
		signs = append(signs, "-"+l)
	}
	return strings.Join(signs, " ")
}

// assignWho names whom v assigns: "the --assignee user", "you", a login;
// "" for no one.
func assignWho(v config.Verb) string {
	switch {
	case v.AssignArg:
		return "the --" + config.ArgAssignee + " user"
	case v.Assign == config.Me:
		return "you"
	}
	return v.Assign
}

// unassignWho names whom v unassigns: "everyone else" (all but whom it
// assigns), "everyone", "you", a login; "" for no one.
func unassignWho(v config.Verb) string {
	switch v.Unassign {
	case config.UnassignAll:
		if v.Assigns() {
			return "everyone else"
		}
		return "everyone"
	case config.Me:
		return "you"
	}
	return v.Unassign
}

// unresolvedMeaning says when a blocker is unresolved under cfg's
// statuses.resolved: "open and not in Ready or Done".
func unresolvedMeaning(cfg *config.Config) string {
	if len(cfg.Statuses.Resolved) == 0 {
		return "open"
	}
	return "open and not in " + render.JoinWords(cfg.Statuses.Resolved, "or")
}

// configNote is appended to the root help when there is no config or it
// cannot be loaded.
func configNote() string {
	_, err := loadConfig()
	if err == nil {
		return ""
	}
	if errors.Is(err, config.ErrNoConfig) {
		return fmt.Sprintf("\n\nNo process verbs: no %s found (create one: gh dojo init; format: %s).", config.FileName, config.DocsURL)
	}
	text := strings.ReplaceAll(render.FromError(err).Text(), "\n", "\n  ")
	return "\n\nNo process verbs: " + config.FileName + " could not be loaded:\n  " + text
}

// minNameWidth is the least width of the verb name column in the root
// help's lifecycle and in describe: cobra's minimum, so the names line up
// with the command list.
const minNameWidth = 11

// processHelp is the lifecycle section of the root help: the statuses each
// verb moves an issue from and to, in config order, then the initial,
// final and resolved statuses. A verb that closes or reopens the issue, or
// asks the user first, says so after its target. It is empty when the
// config cannot be loaded (see configNote).
//
//	Lifecycle (from → to):
//	  start       Backlog, Waiting, Ready → In progress
//	  done        Ready, In progress → Done; closes as completed
//	  Initial:    Backlog (new issues; also an issue with no status or not on the board)
//	  Final:      Done (gh dojo list hides it unless --all)
//	  Resolved:   Ready, Done (a blocker in these no longer blocks)
func processHelp() string {
	cfg, err := loadConfig()
	if err != nil {
		return ""
	}
	width := minNameWidth
	for _, v := range cfg.Verbs {
		width = max(width, len(v.Name))
	}
	var b strings.Builder
	line := func(name, format string, a ...any) {
		fmt.Fprintf(&b, "\n  %-*s %s", width, name, fmt.Sprintf(format, a...))
	}
	for _, v := range cfg.Verbs {
		from, to := "any status", "status unchanged"
		if len(v.From) > 0 {
			from = strings.Join(v.From, ", ")
		}
		if v.To != "" {
			to = v.To
		}
		var also []string
		switch {
		case v.Reopen:
			also = append(also, "reopens")
		case v.Close != "":
			also = append(also, "closes as "+workflow.ReasonText(v.Close))
		}
		if v.Confirm {
			also = append(also, "asks the user first (--yes)")
		}
		if len(also) > 0 {
			to += "; " + strings.Join(also, ", ")
		}
		line(v.Name, "%s → %s", from, to)
	}
	if s := cfg.Statuses.Initial; s != "" {
		line("Initial:", "%s (new issues; also an issue with no status or not on the board)", s)
	}
	if s := cfg.Statuses.Final; len(s) > 0 {
		line("Final:", "%s (gh dojo list hides it unless --all)", strings.Join(s, ", "))
	}
	if s := cfg.Statuses.Resolved; len(s) > 0 {
		line("Resolved:", "%s (a blocker in these no longer blocks)", strings.Join(s, ", "))
	}
	if b.Len() == 0 {
		return ""
	}
	return "\n\nLifecycle (from → to):" + b.String()
}

// verbsUnavailable says in one line why no verbs are loaded, or returns ""
// when the config loads. The root help has the full error.
func verbsUnavailable() string {
	_, err := loadConfig()
	switch {
	case err == nil:
		return ""
	case errors.Is(err, config.ErrNoConfig):
		return "no " + config.FileName + " found"
	}
	what, _, _ := strings.Cut(render.FromError(err).What, "\n")
	return what
}
