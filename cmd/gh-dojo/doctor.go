package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/doctor"
)

func init() { register(newDoctorCmd) }

func newDoctorCmd() (*cobra.Command, error) {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Find what is broken in the setup: config, token scope, board, repositories",
		Long: `Check the setup and print one line per check: ok, problem, warn, or skip
when an earlier problem keeps the check from running. Each problem and
warning is followed by the command that fixes it.

  version  the installed gh-dojo version
  config   ` + config.FileName + ` loads
  token    gh is logged in and the token has the project scope
  board    the board exists and has every field and status the config names
  repo     each repository of board.repos (else the current one) is visible
           and has issues enabled; warns when board.repos spells one otherwise
           than GitHub does
  labels   with labels.type_prefix or labels.area_prefix set, in each of
           those repositories: how many labels start with each prefix
           (none: warn when the config sets the prefix, not a preset, since
           --type or --area finds nothing; past 100 labels a count is a
           lower bound)
  sprint   with fields.sprint set: the field is an iteration field and an
           iteration covers today
  verbs    the labels verbs add or remove exist in the default repository,
           spelled as there, the users they assign can be assigned, and
           each milestone a verb names is an open one there, matched as the
           verb matches it (its title, or a start only that title has)
  agent    with [agent] set or a file agent install wrote: those files are
           up to date with the config and this gh-dojo (out of date: warn)

It sends one request for the token and the repositories, one more for the
board when its schema is not cached (an hour), one for the labels when
labels.type_prefix or labels.area_prefix is set, and one for the verbs' labels, assignees and milestones when a verb has them
(and, past 100 open milestones, those page by page).

Exit codes: 0 when every check is ok or warns; 4 when a check failed on auth or the
network; else 1.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r := runDoctor(cmd.Context(), cmd.Root())
			err := r.Err()
			r.OK = err == nil
			return emitResult(cmd, r, func(w io.Writer) { writeDoctor(w, r) }, err)
		},
	}, nil
}

// writeDoctor prints one line per check, the details of a problem
// indented below it:
//
//	ok       config   .github/dojo.toml: preset kanban, 4 verbs
//	problem  token    github.com as octocat: the token lacks the project scope (has repo)
//	                  fix: gh auth refresh -s project
func writeDoctor(w io.Writer, r *doctor.Report) {
	const indent = "                  " // past the result and the name
	for _, c := range r.Checks {
		// render's error block keeps long values short and drops a fix too
		// long to be a command; its first line is the detail.
		text := c.Block().Text()
		text = strings.TrimPrefix(text, "error: ")
		fmt.Fprintf(w, "%-8s %-8s %s\n", c.Result, c.Name, strings.ReplaceAll(text, "\n", "\n"+indent))
	}
}

// runDoctor runs doctor.Run with this run's config and App, then the agent
// check, which needs the command tree; see doctor.Run for the requests.
func runDoctor(ctx context.Context, root *cobra.Command) *doctor.Report {
	cfg, err := loadConfig()
	if err != nil {
		cfg = nil
	}
	a := newApp()
	r := doctor.Run(ctx, doctor.Env{
		Version: version, Config: cfg, ConfigErr: err,
		Client: a.Client, Boards: a.Loader, CurrentRepo: a.Repo, Now: a.Now,
	})
	if cfg != nil {
		checkAgentFiles(r, cfg, root)
	}
	return r
}
