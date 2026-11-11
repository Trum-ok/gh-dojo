package main

import (
	"fmt"
	"strings"

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
	var bodyFile string
	cmd := &cobra.Command{
		Use:   "comment <N> [text]",
		Short: "Comment on an issue without changing its status",
		Long: `Comment on an issue without changing its status.

The text comes from the second argument or from --body-file (a file, or - for
stdin), exactly one of them; empty text is an error. Use --body-file - for
long or multi-line text.

A repeat is safe: the comment carries a hidden marker, and when the latest
gh-dojo comment on the issue already has the same text it is not posted again.`,
		Example: `  gh dojo comment 42 "Reproduced on main; the redirect drops the query string"
  gh dojo comment 42 --body-file notes.md
  echo "Waiting for the API team" | gh dojo comment 42 --body-file -`,
		Args: commentArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			n, _ := parseIssue(args[0]) // validated by commentArgs
			text, err := commentText(cmd, args[1:], bodyFile)
			if err != nil {
				return err
			}
			line, err := runComment(cmd, n, text)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), line)
			return nil
		},
	}
	cmd.Flags().StringVar(&bodyFile, bodyFileFlag, "", "read the text from `file`; - reads stdin")
	return cmd, nil
}

// commentArgs accepts an issue number and optional text.
func commentArgs(cmd *cobra.Command, args []string) error {
	if len(args) > 2 {
		return render.Usage("comment takes an issue number and one text argument, got %d arguments; quote the text",
			len(args)).WithFix(cmd.CommandPath() + " --help")
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

// runComment posts text on issue n unless the latest gh-dojo comment there
// already has it, and returns the confirmation line. Requests: the card
// (one with a warm schema cache) and addComment.
func runComment(cmd *cobra.Command, n int, text string) (string, error) {
	ctx := cmd.Context()
	a := newApp()
	card, err := a.Card(ctx, n, 0)
	if err != nil {
		return "", err
	}
	post := workflow.AddComment{Body: text, Marker: workflow.Marker(commentMarkerVerb, text)}
	subject := fmt.Sprintf("#%d", n)
	if workflow.AlreadyPosted(workflowIssue(card).Comments, post.Marker) {
		return render.Confirm(subject, "", "comment already present"), nil
	}
	vars := map[string]any{"subject": card.ID, "body": post.Text()}
	if err := a.Client.Mutate(ctx, addCommentMutation, vars, nil); err != nil {
		return "", err // a lost answer says "may have been applied, re-run": the marker makes that safe
	}
	return render.Confirm(subject, "", post.Effect()), nil
}
