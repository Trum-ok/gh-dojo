package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/workflow"
	"github.com/spf13/cobra"
)

func init() { register(newCommentCmd) }

// commentMarkerVerb names comment's marker: <!-- gh-dojo:comment:<hash> -->.
const commentMarkerVerb = "comment"

// addCommentMutation posts a comment. It returns nothing worth reading:
// the confirmation line needs only the issue number.
const addCommentMutation = `mutation AddComment($subject: ID!, $body: String!) {
  addComment(input: {subjectId: $subject, body: $body}) {
    commentEdge { node { id } }
  }
}`

func newCommentCmd() (*cobra.Command, error) {
	var (
		bodyFile string
		dryRun   bool
	)
	cmd := &cobra.Command{
		Use:   "comment <N> [text]",
		Short: "Comment on an issue without changing its status",
		Long: `Comment on an issue without changing its status.

The issue is 42, "#42" (the default repository, board.repos[0]),
owner/repo#42 or its URL.

The text comes from the second argument or from --body-file (a file, or - for
stdin), exactly one of them; empty text is an error. Use --body-file - for
long or multi-line text. Text starting with - goes after --, and every flag
before the --, since anything after it is taken as text:
gh dojo comment 42 --json -- "-1, still broken".

A repeat is safe: the comment carries a hidden marker, and when the issue's
last comment is yours with the same text it is not posted again. The same
text after someone else's comment is posted.

--dry-run reads the issue and prints what would be posted, without posting.`,
		Example: `  gh dojo comment 42 "Reproduced on main; the redirect drops the query string"
  gh dojo comment 42 --body-file notes.md
  echo "Waiting for the API team" | gh dojo comment 42 --body-file -
  gh dojo comment 42 --json -- "-1, still broken"
  gh dojo comment 42 "Reproduced on main" --dry-run`,
		Args: commentArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, _ := parseRef(args[0]) // validated by commentArgs
			text, err := commentText(cmd, args[1:], bodyFile)
			if err != nil {
				return err
			}
			res, err := runComment(cmd, ref, text, dryRun)
			if err != nil {
				return err
			}
			return emitWrite(cmd, res, res.write, nil, res.Posted && !res.DryRun)
		},
	}
	cmd.Flags().StringVar(&bodyFile, bodyFileFlag, "", "read the text from `file`; - reads stdin")
	cmd.Flags().BoolVar(&dryRun, dryRunFlag, false, "print the plan without changing anything")
	return cmd, nil
}

// commentArgs accepts an issue number and optional text.
func commentArgs(cmd *cobra.Command, args []string) error {
	if len(args) > 2 {
		return render.Usage("comment takes an issue number and one text argument, got %d arguments; %s",
			len(args), extraArgsHint(cmd, args, "quote the text")).WithFix(cmd.CommandPath() + " --help")
	}
	return issueArg(cmd, args[:min(len(args), 1)])
}

// commentText returns the text from the argument or --body-file: exactly
// one of them, not empty.
func commentText(cmd *cobra.Command, args []string, bodyFile string) (string, error) {
	help := cmd.CommandPath() + " --help"
	fromFile := cmd.Flags().Changed(bodyFileFlag)
	switch {
	case len(args) > 0 && fromFile:
		return "", render.Usage("give the text either as an argument or with --%s, not both", bodyFileFlag).WithFix(help)
	case len(args) == 0 && !fromFile:
		return "", render.Usage("no comment text: pass it as the second argument or with --%s (- reads stdin)",
			bodyFileFlag).WithFix(help)
	}
	text := ""
	if fromFile {
		var err error
		if text, err = readBodyFile(cmd, bodyFile); err != nil {
			return "", err
		}
	} else {
		text = strings.TrimSpace(args[0])
	}
	if text == "" {
		return "", render.Usage("the comment text is empty").WithFix(help)
	}
	return text, nil
}

// commentResult is what comment did, and its JSON.
type commentResult struct {
	Number int    `json:"number"`
	Repo   string `json:"repo"`
	// DryRun: nothing was posted, and Posted says whether it would be.
	DryRun bool `json:"dry_run"`
	// Posted is false when the issue's last comment was this one already.
	Posted bool `json:"posted"`

	name  string // "#42", or "owner/repo#42" outside the default repository
	title string // the issue's title, for a dry run
	text  string // the comment's text, for a dry run
}

// write prints the confirmation line, "#42 comment left", or the plan of a
// dry run:
//
//	dry run: comment #42 "Fix login redirect"
//	  comment: "Reproduced on main"
func (r *commentResult) write(w io.Writer) {
	if r.DryRun {
		plan := workflow.AddComment{Body: r.text}.Describe()
		if !r.Posted {
			plan = "comment: already the last comment, not posted again"
		}
		fmt.Fprintln(w, render.Confirm("dry run: comment "+r.name, r.title)+"\n  "+plan)
		return
	}
	effect := "comment already present"
	if r.Posted {
		effect = workflow.AddComment{}.Effect()
	}
	fmt.Fprintln(w, render.Confirm(r.name, "", effect))
}

// runComment posts text on issue ref unless the issue's last comment is the
// viewer's with the same text (marker). Requests: the card (one with a
// warm schema cache) and addComment, which a dry run does not send.
//
// Unlike a verb's comment (workflow.AlreadyPosted, the latest gh-dojo
// comment), only the very last comment counts: a plain comment repeated on
// purpose after others replied is posted.
func runComment(cmd *cobra.Command, ref issue.Ref, text string, dryRun bool) (*commentResult, error) {
	ctx := cmd.Context()
	a := newApp()
	card, err := a.Card(ctx, ref, 0)
	if err != nil {
		return nil, err
	}
	def, err := a.Repo() // Card resolved it already
	if err != nil {
		return nil, err
	}
	res := &commentResult{Number: card.Number, Repo: card.Repo.String(), DryRun: dryRun,
		name: card.Ref().String(def), title: card.Title, text: text}
	post := workflow.AddComment{Body: text, Marker: workflow.Marker(commentMarkerVerb, text)}
	if comments := workflowIssue(card, def).Comments; len(comments) > 0 &&
		workflow.AlreadyPosted(comments[len(comments)-1:], post.Marker) {
		return res, nil
	}
	if dryRun {
		res.Posted = true // would be
		return res, nil
	}
	vars := map[string]any{"subject": card.ID, "body": post.Text()}
	if err := a.Client.Mutate(ctx, addCommentMutation, vars, nil); err != nil {
		return nil, err // a lost answer says "may have been applied, re-run": the marker makes that safe
	}
	res.Posted = true
	return res, nil
}
