package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/workflow"
	"github.com/spf13/cobra"
)

func init() { register(newDescribeCmd) }

func newDescribeCmd() (*cobra.Command, error) {
	return &cobra.Command{
		Use:   "describe",
		Short: "Learn this project's process: verbs, statuses and exit codes in a few lines",
		Long: `Print this project's process, short enough for an agent's context: when to
use each verb, what it needs, what it checks and does along the way, the
lifecycle (from → to), the initial, final and resolved statuses, and the exit
codes. gh dojo <verb> --help has the details of one verb.

It reads only ` + config.FileName + `, never the network. A missing or broken
config is printed as an error (exit code 1).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), describeProcess(cfg).text())
			return nil
		},
	}, nil
}

// process is what describe prints, as data (for --json later).
type process struct {
	Config    string            `json:"config"`
	Preset    string            `json:"preset,omitempty"`
	Board     string            `json:"board"`           // owner/number
	Repos     []string          `json:"repos,omitempty"` // the first is the default; none: the current repository
	Verbs     []processVerb     `json:"verbs"`
	Statuses  config.Statuses   `json:"statuses"`
	ExitCodes []processExitCode `json:"exitCodes"`

	// sprint and fields say some verb sets the sprint or other board
	// fields, for the sentence on repeats.
	sprint, fields bool
}

// processVerb is one verb as describe shows it.
type processVerb struct {
	Name          string   `json:"name"`
	When          string   `json:"when"`
	From          []string `json:"from"`         // empty: any status
	To            string   `json:"to,omitempty"` // empty: the status is not changed
	Requires      []string `json:"requires,omitempty"`
	RequiresOneOf []string `json:"requiresOneOf,omitempty"`
	Optional      []string `json:"optional,omitempty"`
	Checks        []string `json:"checks,omitempty"`  // beyond from and the arguments
	Effects       []string `json:"effects,omitempty"` // along the way, beyond the status
}

type processExitCode struct {
	Code    int    `json:"code"`
	Meaning string `json:"meaning"`
	Next    string `json:"next"`
}

// verbChecks and verbEffects say, in order, what a verb checks and does
// beyond its from → to and its arguments, as short phrases joined into its
// describe line. Each returns "" when the verb does not have the key. A new
// verb key (blockers, batch) gets an entry in one of them.
var (
	verbChecks = []func(cfg *config.Config, v config.Verb) string{
		func(cfg *config.Config, v config.Verb) string {
			if v.To != "" && slices.Contains(cfg.Statuses.Final, v.To) || v.Close != "" || v.Reopen {
				return "also on closed issues"
			}
			return ""
		},
		func(_ *config.Config, v config.Verb) string {
			if v.Confirm {
				return "the user's decision: ask first, then --yes"
			}
			return ""
		},
		func(_ *config.Config, v config.Verb) string {
			switch v.Blockers {
			case config.BlockersWarn:
				return "warns about unresolved blockers"
			case config.BlockersDeny:
				return "refused while a blocker is unresolved"
			}
			return ""
		},
	}
	verbEffects = []func(cfg *config.Config, v config.Verb) string{
		func(_ *config.Config, v config.Verb) string {
			if v.Reopen {
				return "reopens"
			}
			return ""
		},
		describeIteration,
		describeFields,
		func(_ *config.Config, v config.Verb) string {
			var signs []string
			for _, l := range v.AddLabels {
				signs = append(signs, "+"+l)
			}
			for _, l := range v.RemoveLabels {
				signs = append(signs, "-"+l)
			}
			if len(signs) == 0 {
				return ""
			}
			return "labels " + strings.Join(signs, " ")
		},
		func(_ *config.Config, v config.Verb) string {
			switch v.Assign {
			case "":
				return ""
			case config.Me:
				return "assigns you"
			}
			return "assigns " + v.Assign
		},
		func(_ *config.Config, v config.Verb) string {
			if v.Blockers == config.BlockersLink {
				return "--by N also marks it blocked by #N"
			}
			return ""
		},
		func(_ *config.Config, v config.Verb) string {
			if v.Comment == "" {
				return ""
			}
			return `comments "` + clipRunes(singleLine(v.Comment), maxTemplateRunes) + `"`
		},
		func(_ *config.Config, v config.Verb) string {
			if v.Close == "" {
				return ""
			}
			return "closes as " + workflow.ReasonText(v.Close)
		},
	}
)

// wrapText breaks text into lines of at most width bytes at spaces; a
// longer word gets a line of its own.
func wrapText(text string, width int) string {
	var b strings.Builder
	n := 0
	for _, w := range strings.Fields(text) {
		switch {
		case n == 0:
		case n+1+len(w) > width:
			b.WriteByte('\n')
			n = 0
		default:
			b.WriteByte(' ')
			n++
		}
		b.WriteString(w)
		n += len(w)
	}
	return b.String()
}

// maxTemplateRunes is how much of a comment template describe shows.
const maxTemplateRunes = 60

// describeProcess collects what describe shows about cfg.
func describeProcess(cfg *config.Config) *process {
	p := &process{
		Config:   cfg.Display,
		Preset:   cfg.Preset,
		Board:    fmt.Sprintf("%s/%d", cfg.Board.Owner, cfg.Board.Project),
		Repos:    cfg.Board.Repos,
		Statuses: cfg.Statuses,
	}
	for _, v := range cfg.Verbs {
		pv := processVerb{Name: v.Name, When: v.When, From: v.From, To: v.To}
		for _, arg := range config.Args() {
			switch {
			case slices.Contains(v.Require, arg):
				pv.Requires = append(pv.Requires, "--"+arg)
			case !slices.Contains(v.RequireOneOf, arg) && v.Uses(arg):
				pv.Optional = append(pv.Optional, "--"+arg)
			}
		}
		for _, arg := range v.RequireOneOf { // in config order, as in the verb's help
			pv.RequiresOneOf = append(pv.RequiresOneOf, "--"+arg)
		}
		for _, f := range verbChecks {
			if s := f(cfg, v); s != "" {
				pv.Checks = append(pv.Checks, s)
			}
		}
		for _, f := range verbEffects {
			if s := f(cfg, v); s != "" {
				pv.Effects = append(pv.Effects, s)
			}
		}
		p.Verbs = append(p.Verbs, pv)
		p.sprint = p.sprint || v.Iteration != ""
		p.fields = p.fields || len(v.Fields) > 0
	}
	for _, c := range render.ExitCodes {
		p.ExitCodes = append(p.ExitCodes, processExitCode{int(c.Code), c.Meaning, c.Next})
	}
	return p
}

// text renders the process. The lifecycle and the exit codes are the root
// help's sections, so the two never disagree:
//
//	Process from .github/dojo.toml (preset kanban): board acme/3, issues in acme/shop.
//	Run a verb as gh dojo <verb> <N>; --dry-run shows its plan first.
//
//	Verbs:
//	  wait        Work is stuck: waiting for another issue or an outside decision
//	              needs --by or --comment; comments "Waiting for {by}. {comment}"
//
//	Every verb ...
//
//	Lifecycle (from → to):
//	  ...
func (p *process) text() string {
	head := "Process from " + p.Config
	if p.Preset != "" {
		head += " (preset " + p.Preset + ")"
	}
	issues := "the current repository"
	if len(p.Repos) > 0 {
		issues = strings.Join(p.Repos, ", ")
	}
	sections := []string{fmt.Sprintf("%s: board %s, issues in %s.\n"+
		"Run a verb as gh dojo <verb> <N>; --dry-run shows its plan first.", head, p.Board, issues)}

	if len(p.Verbs) == 0 {
		sections = append(sections, "No verbs: add them under [verbs] in "+p.Config+".")
	} else {
		width := 11 // as in the root help
		for _, v := range p.Verbs {
			width = max(width, len(v.Name))
		}
		var b strings.Builder
		b.WriteString("Verbs:")
		for _, v := range p.Verbs {
			fmt.Fprintf(&b, "\n  %-*s %s", width, v.Name, v.When)
			if facts := v.facts(); facts != "" {
				fmt.Fprintf(&b, "\n  %-*s %s", width, "", facts)
			}
		}
		effects := []string{"status"}
		if p.sprint {
			effects = append(effects, "sprint")
		}
		if p.fields {
			effects = append(effects, "fields")
		}
		effects = append(effects, "comment", "labels", "assignee", "blocked-by link", "closed or open")
		sections = append(sections, b.String(), wrapText("Every verb adds the issue to the board if it is missing and refuses a "+
			"closed issue unless noted. A repeat is safe: an effect already in place ("+
			strings.Join(effects, ", ")+") is skipped.", 72))
	}
	// The root help's sections, each starting with a blank line; the
	// lifecycle is empty without verbs and statuses.
	for _, s := range []string{processHelp(), exitCodesHelp()} {
		if s = strings.TrimLeft(s, "\n"); s != "" {
			sections = append(sections, s)
		}
	}
	return strings.Join(sections, "\n\n") + "\n"
}

// facts is the second describe line of a verb: its arguments, checks and
// effects, or "" when it has none.
func (v processVerb) facts() string {
	var parts []string
	if len(v.Requires) > 0 {
		parts = append(parts, "needs "+strings.Join(v.Requires, " and "))
	}
	if len(v.RequiresOneOf) > 0 {
		parts = append(parts, "needs "+strings.Join(v.RequiresOneOf, " or "))
	}
	if len(v.Optional) > 0 {
		parts = append(parts, "optional "+strings.Join(v.Optional, ", "))
	}
	parts = append(parts, v.Checks...)
	parts = append(parts, v.Effects...)
	return strings.Join(parts, "; ")
}
