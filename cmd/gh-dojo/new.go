package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/workflow"
	"github.com/spf13/cobra"
)

// gh dojo new creates an issue with its labels, on the board, in the
// initial status.
//
// Requests with a warm schema cache: one read (newContextQuery: repo ID,
// labels, recent issues), createIssue (labels and board in one go), then
// the status. Without statuses.initial the status write is skipped.
//
// Retries are safe (principle 5): the body ends with a hidden marker
// <!-- gh-dojo:new:<hash> --> of the title and body, and the read looks for
// it among the repo's newDedupWindow most recent issues the viewer wrote.
// A match is resumed instead of created again: missing labels are added,
// the issue is put on the board and, if it has no status, given the
// initial one. An issue that dropped out of the window (newDedupWindow
// newer issues in the repo, by anyone) is created again.

func init() { register(newNewCmd) }

const (
	// newMarkerVerb names new's marker: <!-- gh-dojo:new:<hash> -->.
	newMarkerVerb = "new"
	// newDedupWindow is issues(last: N) of newContextQuery.
	newDedupWindow = 20
)

// newContextQuery reads what new needs before writing: the repo ID, its
// labels (by name, for the allowed lists) and the latest issues, to find one
// a failed run already created. $status is the board's status field name.
const newContextQuery = `query NewIssueContext($owner: String!, $repo: String!, $status: String!) {
  repository(owner: $owner, name: $repo) {
    id
    labels(first: 100, orderBy: {field: NAME, direction: ASC}) {
      totalCount
      pageInfo { hasNextPage endCursor }
      nodes { id name }
    }
    issues(last: 20, orderBy: {field: CREATED_AT, direction: ASC}) {
      nodes {
        id
        number
        body
        viewerDidAuthor
        labels(first: 50) { nodes { name } }
        projectItems(first: 20) {
          nodes {
            id
            project { id }
            fieldValueByName(name: $status) { ... on ProjectV2ItemFieldSingleSelectValue { name } }
          }
        }
      }
    }
  }
}`

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

// newCreateMutation creates the issue with its labels and on the board, and
// returns the board item: the status write needs its ID.
const newCreateMutation = `mutation CreateIssue($repo: ID!, $title: String!, $body: String!, $labels: [ID!], $projects: [ID!]) {
  createIssue(input: {repositoryId: $repo, title: $title, body: $body, labelIds: $labels, projectV2Ids: $projects}) {
    issue {
      id
      number
      projectItems(first: 20) { nodes { id project { id } } }
    }
  }
}`

// newAddLabelsMutation adds labels a resumed issue lacks.
const newAddLabelsMutation = `mutation AddLabels($issue: ID!, $labels: [ID!]!) {
  addLabelsToLabelable(input: {labelableId: $issue, labelIds: $labels}) { clientMutationId }
}`

// newAddToBoardMutation adds the issue to the board, or returns its item if
// it is there already.
const newAddToBoardMutation = `mutation AddToBoard($project: ID!, $issue: ID!) {
  addProjectV2ItemById(input: {projectId: $project, contentId: $issue}) { item { id } }
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
}

func newNewCmd() (*cobra.Command, error) {
	var (
		o        newOptions
		bodyFile string
	)
	cmd := &cobra.Command{
		Use:   "new <title>",
		Short: "Create an issue with labels, on the board, in the initial status",
		Long:  newHelp(),
		Example: `  gh dojo new "Fix login redirect" --type bug --area cli
  gh dojo new "Support org boards" --label enhancement --body-file - < notes.md`,
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
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if err := checkNewType(cfg, o.Type); err != nil {
				return err // before any request
			}
			line, err := runNew(cmd.Context(), newApp(), cfg, o)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), line)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.Type, "type", "", "issue type: a label from labels.types")
	f.StringArrayVar(&o.Areas, "area", nil, "area `name`: adds the label <area_prefix><name>; repeatable")
	f.StringArrayVar(&o.Labels, "label", nil, "any existing repo `label`; repeatable")
	f.StringVar(&o.Body, "body", "", "issue body (markdown)")
	f.StringVar(&bodyFile, bodyFileFlag, "", "read the body from `file`; - reads stdin")
	return cmd, nil
}

// newArgs accepts exactly one title.
func newArgs(cmd *cobra.Command, args []string) error {
	help := cmd.CommandPath() + " --help"
	switch {
	case len(args) == 0:
		return render.Usage(`new needs a title, like: gh dojo new "Fix login redirect"`).WithFix(help)
	case len(args) > 1:
		return render.Usage("new takes one title, got %d arguments; quote the title", len(args)).WithFix(help)
	}
	return nil
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

The body comes from --body or --body-file (a file, or - for stdin).

A repeat is safe: the body ends with a hidden marker of the title and body,
and a run that finds it among the repo's 20 newest issues finishes that issue
(labels, board, status) instead of creating another. If a run fails after
creating the issue, it exits with code 5: run the same command again.`)
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
	}
	if cfg.Statuses.Initial != "" {
		fmt.Fprintf(&b, "\n  status    %s", cfg.Statuses.Initial)
	} else {
		b.WriteString("\n  status    none: statuses.initial is not set")
	}
	return b.String()
}

// runNew creates the issue described by o, or finishes the one a failed
// run left, and returns the confirmation line.
func runNew(ctx context.Context, a *App, cfg *config.Config, o newOptions) (string, error) {
	created := false // by an earlier attempt of this run
	for attempt := 1; ; attempt++ {
		r := &newRun{App: a, cfg: cfg, opts: o}
		err := r.run(ctx)
		if created {
			r.resumed = false
		}
		if err == nil {
			return r.confirm(), nil
		}
		// A schema ID GitHub rejected: drop the cache and run again. The
		// re-read finds the issue if it was created, so nothing is doubled.
		if attempt == 1 && r.schemaCached && board.IsStale(err) && a.ResetSchema() == nil {
			created = r.number != 0 && !r.resumed
			continue
		}
		if r.number == 0 {
			return "", err
		}
		return "", r.partial(err)
	}
}

// checkNewType rejects a --type outside labels.types without a request.
func checkNewType(cfg *config.Config, typ string) error {
	if typ == "" || len(cfg.Labels.Types) == 0 {
		return nil
	}
	if slices.ContainsFunc(cfg.Labels.Types, func(t string) bool { return strings.EqualFold(t, typ) }) {
		return nil
	}
	return render.Usage("unknown type %q", typ).WithAllowed(cfg.Labels.Types...).
		WithFix("gh dojo new --help")
}

// newRun is one attempt of new; its fields after run say how far it got.
type newRun struct {
	*App
	cfg  *config.Config
	opts newOptions

	schemaCached bool // the board schema came from the cache

	number  int    // the issue's number, once it exists
	id      string // its node ID
	resumed bool   // it was found by its marker, not created now
	labels  []string

	itemID    string
	status    string // the status found, "" for none
	statusSet bool   // the status was set to the initial one now
	noInitial bool   // statuses.initial is empty: no status was set
}

// newStepError says which write failed, as "setting status Backlog failed".
type newStepError struct {
	step string
	err  error
}

func (e *newStepError) Error() string { return e.step + ": " + e.err.Error() }
func (e *newStepError) Unwrap() error { return e.err }

func (r *newRun) run(ctx context.Context) error {
	s, err := r.Schema(ctx)
	if err != nil {
		return err
	}
	r.schemaCached = s.Cached
	repo, err := r.Repo()
	if err != nil {
		return err
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

	nc, err := readNewContext(ctx, r.Client, repo, statusField.Name)
	if err != nil {
		return err
	}
	labels, err := nc.resolve(ctx, r.Client, r.cfg, repo, r.opts)
	if err != nil {
		return err
	}
	for _, l := range labels {
		r.labels = append(r.labels, l.Name)
	}

	marker := workflow.Marker(newMarkerVerb, r.opts.Title+"\n"+r.opts.Body)
	var missing []string // label IDs a resumed issue lacks
	if found := nc.find(marker, s.ID); found != nil {
		r.resumed = true
		r.number, r.id = found.Number, found.ID
		r.itemID, r.status = found.itemID, found.status
		for _, l := range labels {
			if !slices.ContainsFunc(found.labels, func(n string) bool { return strings.EqualFold(n, l.Name) }) {
				missing = append(missing, l.ID)
			}
		}
	} else if err := r.create(ctx, nc.RepoID, withMarker(r.opts.Body, marker), labels, s.ID); err != nil {
		return err
	}

	if len(missing) > 0 {
		vars := map[string]any{"issue": r.id, "labels": missing}
		if err := r.Client.Mutate(ctx, newAddLabelsMutation, vars, nil); err != nil {
			return &newStepError{"adding labels " + strings.Join(r.labels, ", ") + " failed", err}
		}
	}
	if r.itemID == "" {
		var out struct {
			Add *struct {
				Item *struct {
					ID string `json:"id"`
				} `json:"item"`
			} `json:"addProjectV2ItemById"`
		}
		vars := map[string]any{"project": s.ID, "issue": r.id}
		err := r.Client.Mutate(ctx, newAddToBoardMutation, vars, &out)
		if err == nil && (out.Add == nil || out.Add.Item == nil || out.Add.Item.ID == "") {
			err = render.Internal("addProjectV2ItemById returned no item")
		}
		if err != nil {
			return &newStepError{"adding it to board " + s.Ref().String() + " failed", err}
		}
		r.itemID = out.Add.Item.ID
	}
	// A resumed issue keeps a status it already has: someone may have
	// moved it on purpose.
	if initial != nil && (!r.resumed || r.status == "") {
		vars := map[string]any{"project": s.ID, "item": r.itemID, "field": statusField.ID, "option": initial.ID}
		if err := r.Client.Mutate(ctx, newSetStatusMutation, vars, nil); err != nil {
			return &newStepError{"setting status " + initial.Name + " failed", err}
		}
		r.status, r.statusSet = initial.Name, true
	}
	return nil
}

// create sends createIssue. On an error with the issue in the payload (a
// part of the input GitHub refused) the issue exists: r.number is set and
// the error is returned for a partial result.
func (r *newRun) create(ctx context.Context, repoID, body string, labels []newLabel, projectID string) error {
	ids := make([]string, len(labels))
	for i, l := range labels {
		ids[i] = l.ID
	}
	var out struct {
		Create *struct {
			Issue *struct {
				ID           string `json:"id"`
				Number       int    `json:"number"`
				ProjectItems struct {
					Nodes []*newItemJSON `json:"nodes"`
				} `json:"projectItems"`
			} `json:"issue"`
		} `json:"createIssue"`
	}
	vars := map[string]any{
		"repo": repoID, "title": r.opts.Title, "body": body, "labels": ids, "projects": []string{projectID},
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
	for _, it := range is.ProjectItems.Nodes {
		if it != nil && it.Project != nil && it.Project.ID == projectID {
			r.itemID = it.ID
		}
	}
	if err != nil {
		return &newStepError{"GitHub refused part of the request (labels or board)", err}
	}
	return nil
}

// confirm is the confirmation line of a finished run:
//
//	#57 "Fix login redirect" created, labels bug, area:cli, no status → Backlog
//	#57 "Fix login redirect" already created, labels bug, Backlog (unchanged)
func (r *newRun) confirm() string {
	effects := []string{"created"}
	if r.resumed {
		effects[0] = "already created"
	}
	if len(r.labels) > 0 {
		effects = append(effects, "labels "+strings.Join(r.labels, ", "))
	}
	switch {
	case r.statusSet:
		effects = append(effects, render.Transition("", r.status))
	case r.status != "":
		effects = append(effects, render.Transition(r.status, r.status))
	case r.noInitial:
		effects = append(effects, "on the board with no status (statuses.initial is not set)")
	}
	return render.Confirm(fmt.Sprintf("#%d", r.number), r.opts.Title, effects...)
}

// partial is the exit-code-5 error of a run that stopped after the issue
// existed: what failed, why, and that re-running finishes it.
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
	why, _, _ := strings.Cut(render.FromError(cause).What, "\n")
	return render.Partial("#%d %s, but %s\n%s", r.number, what, step, why).
		WithFix(fmt.Sprintf("re-run the same gh dojo new command: it finds #%d and finishes it", r.number)).
		WithCause(err)
}

// newContext is what newContextQuery read.
type newContext struct {
	RepoID    string
	Labels    graph.Connection[newLabel]
	Issues    []*newIssueJSON
	allLabels []newLabel // all repo labels, once read past the first page
}

type newLabel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type newItemJSON struct {
	ID      string `json:"id"`
	Project *struct {
		ID string `json:"id"`
	} `json:"project"`
	Status *struct {
		Name string `json:"name"`
	} `json:"fieldValueByName"`
}

type newIssueJSON struct {
	ID              string `json:"id"`
	Number          int    `json:"number"`
	Body            string `json:"body"`
	ViewerDidAuthor bool   `json:"viewerDidAuthor"`
	Labels          struct {
		Nodes []*struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	ProjectItems struct {
		Nodes []*newItemJSON `json:"nodes"`
	} `json:"projectItems"`
}

func readNewContext(ctx context.Context, q graph.Querier, repo issue.Repo, statusField string) (*newContext, error) {
	var out struct {
		Repository *struct {
			ID     string                     `json:"id"`
			Labels graph.Connection[newLabel] `json:"labels"`
			Issues struct {
				Nodes []*newIssueJSON `json:"nodes"`
			} `json:"issues"`
		} `json:"repository"`
	}
	vars := map[string]any{"owner": repo.Owner, "repo": repo.Name, "status": statusField}
	err := q.Query(ctx, newContextQuery, vars, &out)
	if out.Repository == nil && (err == nil || render.FromError(err).Code == render.ExitNotFound) {
		return nil, render.NotFound("repository %s not found, or the token cannot see it", repo).
			WithFix("gh repo view " + repo.String()).WithCause(err)
	}
	if err != nil {
		return nil, err
	}
	r := out.Repository
	return &newContext{RepoID: r.ID, Labels: r.Labels, Issues: r.Issues.Nodes}, nil
}

// foundIssue is an issue a failed run created, as find returns it.
type foundIssue struct {
	ID, itemID, status string
	Number             int
	labels             []string
}

// find returns the newest issue the viewer wrote whose body ends with
// marker, with its item on the board projectID, or nil. The marker hashes
// the title and the body, so a match is the same issue.
func (nc *newContext) find(marker, projectID string) *foundIssue {
	for i := len(nc.Issues) - 1; i >= 0; i-- {
		is := nc.Issues[i]
		if is == nil || !is.ViewerDidAuthor || !strings.HasSuffix(strings.TrimSpace(is.Body), marker) {
			continue
		}
		f := &foundIssue{ID: is.ID, Number: is.Number}
		for _, l := range is.Labels.Nodes {
			if l != nil {
				f.labels = append(f.labels, l.Name)
			}
		}
		for _, it := range is.ProjectItems.Nodes {
			if it != nil && it.Project != nil && it.Project.ID == projectID {
				f.itemID = it.ID
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
				WithFix(fmt.Sprintf("gh label create %q -R %s", name, repo))
		}
	default:
		for _, l := range all {
			allowed = append(allowed, l.Name)
		}
	}
	return render.Usage("unknown %s %q: no such label in %s", flag, value, repo).WithAllowed(allowed...)
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}
