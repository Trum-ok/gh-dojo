package main

import (
	"io"
	"slices"

	"github.com/spf13/cobra"

	"github.com/Trum-ok/gh-dojo/internal/batch"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/workflow"
)

// runBatch runs verb v on several issues, refs, with batch.Run: each issue
// is read, planned and applied on its own, a failed one is skipped, and a
// summary ends the run (see batch.Run for the output and exit codes). One
// issue, as when 12 and owner/repo#12 name the same, runs as runVerb.
func runBatch(cmd *cobra.Command, cfg *config.Config, v config.Verb, refs []issue.Ref, args workflow.Args, dryRun, show bool) error {
	if err := workflow.CheckArgs(v, args, refArgs(refs, issue.Repo{})...); err != nil {
		return err
	}
	be, err := newBackend(cfg)
	if err != nil {
		return err
	}
	def, err := be.Repo()
	if err != nil {
		return err
	}
	if refs = inRepo(refs, def); len(refs) == 1 { // 12 and owner/repo#12 of the default repository
		return runVerb(cmd, cfg, v, refs[0], args, dryRun, show)
	}
	if !v.Batch { // issueArgs let one number in two forms through
		return notBatch(cmd, cfg, v, len(refs))
	}
	if show {
		return showOne(len(refs))
	}
	b := newBatchRun(cmd, be, cfg, v, args, def)
	items, err := b.Do(cmd.Context(), refs, dryRun)
	if err != nil {
		return err
	}
	// The text was printed issue by issue as it went; JSON comes at the
	// end, for every issue, also when the summary is an error.
	return emitWrite(cmd, verbJSON(v.Name, backendRepo(be), dryRun, items), func(io.Writer) {},
		b.Summary(items, dryRun), slices.ContainsFunc(items, (*batch.Item).Wrote))
}

// newBatchRun prepares a run of verb v on several issues through be; def
// is be's default repository.
func newBatchRun(cmd *cobra.Command, be Backend, cfg *config.Config, v config.Verb, args workflow.Args, def issue.Repo) *batch.Run {
	b := batch.New(be, cfg, v, args, def)
	b.Engine.Getenv = getenv
	b.Stdout, b.Stderr, b.JSON = cmd.OutOrStdout(), cmd.ErrOrStderr(), jsonMode(cmd)
	return b
}

// batchHelp is the Batch section of the help of a verb with batch = true.
func batchHelp(v config.Verb, line func(label, format string, a ...any)) {
	if !v.Batch {
		return
	}
	line("Batch", "several numbers at once: gh dojo %s 12 13 14; each issue is applied", v.Name)
	line("", "on its own, a failed one is skipped, and a summary ends the run:")
	line("", "\"3 of 4 done; #17: <reason>\"; a re-run skips what is done")
	line("", "exit code: 0 all done; 5 some written and a re-run may finish the rest;")
	line("", "else the failures' code, 2 first, then 1, 4, 3")
}
