package main

import (
	"context"
	"strings"
	"sync"

	"github.com/cli/go-gh/v2/pkg/repository"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/workflow"
)

// App is the plumbing commands share: the config, the GraphQL client, the
// board schema checked against the config (#6), the default repository and
// issue cards. Everything is lazy, so a command pays only for what it uses.
// A command gets one App per run from newApp, inside RunE:
//
//	a := newApp()
//	card, err := a.Card(cmd.Context(), ref, 0) // config, schema from cache, 1 query
//	if err != nil { return err }                 // render errors, right exit codes
//
// Requests with a warm schema cache: Schema sends none, Card sends one.
// Tests swap newApp with withApp (app_test.go) for recorded responses, a
// private cache and a fixed clock, and the config with withConfig.
type App struct {
	// Client sends GraphQL queries and mutations.
	Client graph.Doer
	// Loader reads board schemas through the cache; its Client is Client.
	Loader *board.Loader
	// CurrentRepo returns "owner/name" of the current repository: GH_REPO
	// when set, else the current directory's GitHub remote. It is used only
	// when board.repos is empty.
	CurrentRepo func() (string, error)

	mu     sync.Mutex
	schema *board.Schema
}

// newApp returns the App for one command run. Tests replace it.
var newApp = func() *App {
	c := graph.New(graph.Options{})
	return &App{Client: c, Loader: board.NewLoader(c), CurrentRepo: gitRepo}
}

// gitRepo is the default App.CurrentRepo: GH_REPO, else the git remotes.
func gitRepo() (string, error) {
	r, err := repository.Current()
	if err != nil {
		return "", err
	}
	return r.Owner + "/" + r.Name, nil
}

// Config returns the config, loaded once per process by loadConfig: the
// same one verbs are built from.
func (a *App) Config() (*config.Config, error) { return loadConfig() }

// Schema returns the board schema, checked against the config: every
// fields.* and every status the config names exist on the board. It comes
// from the cache when fresh (no request); a mismatch on a cached schema
// re-reads the board once before failing with exit code 1. The result is
// kept for the rest of the run.
func (a *App) Schema(ctx context.Context) (*board.Schema, error) {
	cfg, err := a.Config()
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.schema != nil {
		return a.schema, nil
	}
	s, err := a.Loader.LoadChecked(ctx, cfg)
	if err != nil {
		return nil, err
	}
	a.schema = s
	return s, nil
}

// ResetSchema drops the schema, cached on disk and kept by the App, so the
// next Schema re-reads the board. Call it once when GitHub rejects a schema
// ID (board.IsStale), then rebuild what used the old one and retry.
//
// The cache file is removed under the lock Schema holds while loading, so
// a concurrent Schema either finishes before (and its result is dropped) or
// starts after and re-reads the board: it never keeps the old cache.
func (a *App) ResetSchema() error {
	cfg, err := a.Config()
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.schema = nil
	return a.Loader.Invalidate(board.RefOf(cfg))
}

// Repo returns the default repository: board.repos[0] when set, else the
// current repository (CurrentRepo: GH_REPO, else the git remote). The
// config wins: GH_REPO does not override board.repos. When neither gives a
// repository it is a config error (exit code 1) that says why the current
// one is unknown, e.g. a malformed GH_REPO.
func (a *App) Repo() (issue.Repo, error) {
	cfg, err := a.Config()
	if err != nil {
		return issue.Repo{}, err
	}
	if len(cfg.Board.Repos) > 0 {
		return issue.ParseRepo(cfg.Board.Repos[0])
	}
	name, err := a.CurrentRepo()
	if err != nil {
		return issue.Repo{}, cfg.Errorf("board.repos is empty and the current repository is unknown: %v", err).
			WithFix(`add repos = ["owner/name"] under [board] in ` + cfg.Display).WithCause(err)
	}
	return issue.ParseRepo(name)
}

// Card reads issue ref with its item on the board: one GraphQL request
// with a warm schema cache. A ref without a repository (a bare number) is
// in the default repository. comments is how many of the latest comments
// to read; 0 means issue.DefaultComments. A missing issue is exit code 3.
// The default repository is resolved first, so a config without one sends
// no request.
//
// The status is matched by the status field's ID from the schema, so a
// field renamed since the schema was cached still reads right.
func (a *App) Card(ctx context.Context, ref issue.Ref, comments int) (*issue.Card, error) {
	res, err := a.Cards(ctx, []issue.Ref{ref}, comments) // one issue: issue.Read's request
	if err != nil {
		return nil, err
	}
	if res[0].Err != nil {
		return nil, res[0].Err
	}
	return res[0].Card, nil
}

// Cards reads issues refs like Card: one request per issue.MaxBatch
// issues with a warm schema cache, whatever their repositories (for a
// single issue, the request Card sends). A missing issue or a pull request
// number fails only its own issue.Result; see issue.ReadRefs.
func (a *App) Cards(ctx context.Context, refs []issue.Ref, comments int) ([]issue.Result, error) {
	return a.cards(ctx, refs, comments, issue.Lookup{})
}

// cards reads issues refs like Cards, with what lookup names (labels, an
// assignable user, the viewer, linked issues) once per request:
// card.Found.
func (a *App) cards(ctx context.Context, refs []issue.Ref, comments int, lookup issue.Lookup) ([]issue.Result, error) {
	repo, err := a.Repo()
	if err != nil {
		return nil, err
	}
	s, err := a.Schema(ctx)
	if err != nil {
		return nil, err
	}
	cfg, err := a.Config()
	if err != nil {
		return nil, err
	}
	req := issue.Request{Repo: repo, Board: s, StatusField: cfg.StatusField(), Comments: comments, Lookup: lookup}
	if f, err := s.Field(req.StatusField); err == nil { // Schema checked it
		req.StatusFieldID = f.ID
	}
	res, err := issue.ReadRefs(ctx, a.Client, req, refs)
	for i := range res {
		// The default repository as the config spells it, the others as
		// GitHub does.
		r := issue.Ref{Repo: res[i].Repo, Number: res[i].Number}.In(repo)
		res[i].Repo = r.Repo
		if res[i].Card != nil {
			res[i].Card.Repo = r.Repo
		}
	}
	return res, err
}

// statusAutomated reports whether the board's workflows set the status of
// c's item (workflow.Issue.StatusAutomated): its last status change, made
// since the item was added (an earlier change belongs to an item since
// removed), is into the status and automated.
func statusAutomated(c *issue.Card) bool {
	if !c.OnBoard() || len(c.History) == 0 {
		return false
	}
	last := c.History[len(c.History)-1]
	if c.Item != nil && !c.Item.CreatedAt.IsZero() && last.At.Before(c.Item.CreatedAt) {
		return false
	}
	return last.Automated && last.To == c.Status()
}

// workflowIssue is the state workflow plans with. Ref keeps the card, so an
// executor finds the node IDs it needs there: card.ID for the issue,
// card.ItemID() for the board item ("" when not on the board: add it with
// addProjectV2ItemById first, which returns the item ID).
//
// workflow.Issue cannot say "unknown": when card.StatusUnknown() (board
// membership or status could not be read) it shows the issue off the board
// or without a status, so check that before planning. An archived item
// (card.Archived()) is OnBoard. An issue outside def, the default
// repository, is named owner/repo#N.
func workflowIssue(c *issue.Card, def issue.Repo) workflow.Issue {
	is := workflow.Issue{
		Number:    c.Number,
		Display:   c.Ref().String(def),
		Title:     c.Title,
		Closed:    c.Closed(),
		OnBoard:   c.OnBoard(),
		Status:    c.Status(),
		Archived:  c.Archived(),
		Labels:    c.Labels.Nodes,
		Assignees: c.Assignees.Nodes,
		Ref:       c,
		// With Lookup.AllLabels the window is GitHub's own limit per issue.
		LabelsTruncated: c.Labels.TotalCount > len(c.Labels.Nodes),
	}
	if !c.Ref().Same(issue.Ref{Repo: def, Number: c.Number}) && def != (issue.Repo{}) {
		is.DefaultRepo = def.String()
	}
	if c.Closed() && c.StateReason != "REOPENED" {
		// COMPLETED, NOT_PLANNED, DUPLICATE: the config's close reasons.
		is.StateReason = strings.ToLower(c.StateReason)
	}
	if f := c.Found; f != nil {
		if f.Viewer != nil {
			is.Viewer = f.Viewer.Login
		}
		if f.User != nil {
			is.AssignLogin = f.User.Login // as GitHub spells it
		}
		if f.Milestone != nil {
			is.SetMilestone = f.Milestone.Title // as the repository spells it
		}
	}
	if c.Milestone != nil {
		is.Milestone = c.Milestone.Title
	}
	is.StatusAutomated = statusAutomated(c)
	for _, cm := range c.Comments.Nodes {
		is.Comments = append(is.Comments, workflow.Comment{Body: cm.Body, ViewerDidAuthor: cm.ViewerDidAuthor})
	}
	return is
}
