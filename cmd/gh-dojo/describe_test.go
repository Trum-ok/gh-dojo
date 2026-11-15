package main

import (
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

// customConfig overrides the kanban preset and adds verbs of its own: one
// without a status, one with a required argument, one from any status.
const customConfig = `
version = 1
preset = "kanban"

[board]
owner = "acme"
project = 3
repos = ["acme/shop", "acme/api"]

[statuses]
resolved = ["Done"]

[verbs.start]
when = "берёшь задачу в работу"
comment = """
Взял в работу. Сессия: {session}

{comment}"""

[verbs.ready]
disabled = true

[verbs.done]
sub_issues = "deny"

[verbs.note]
when = "Leave a progress note without moving the issue"
require = ["comment"]
comment = "Progress: {comment}"

[verbs.park]
when = "Put the issue back"
to = "Backlog"
`

// noPresetConfig defines every verb itself: no statuses, no comments.
const noPresetConfig = `
version = 1

[board]
owner = "acme"
project = 3

[fields]
status = "Stage"

[verbs.go]
when = "Go"
to = "Doing"
`

// TestDescribe runs describe without a GitHub client: it reads only the
// config (newApp is noApp outside withApp).
func TestDescribe(t *testing.T) {
	tests := []struct {
		name   string
		config string
		code   render.Code
	}{
		{"kanban", dojoConfig, render.ExitOK},
		{"repos", reposConfig, render.ExitOK}, // a bare number is in the first
		{"sprints", sprintsPresetConfig, render.ExitOK},
		{"custom", customConfig, render.ExitOK},
		{"no_preset", noPresetConfig, render.ExitOK},
		{"effects", effectsDojoConfig, render.ExitOK}, // confirm, close, reopen, labels, assign
		{"milestone", milestoneDojoConfig, render.ExitOK},
		{"broken", dojoConfig + "\n[verbs.start]\nto = 3\n", render.ExitUsage},
		{"no_verbs", "\nversion = 1\n[board]\nowner = \"acme\"\nproject = 3\n[fields]\nstatus = \"Status\"\n", render.ExitOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, tt.config)
			code, out := runOut("describe")
			if code != tt.code {
				t.Errorf("exit code = %d, want %d", code, tt.code)
			}
			testutil.Golden(t, "describe/"+tt.name, out)
		})
	}
}

func TestDescribeNoConfig(t *testing.T) {
	code, out := runOut("describe") // TestMain's directory has no config
	if code != render.ExitUsage {
		t.Errorf("exit code = %d, want %d", code, render.ExitUsage)
	}
	testutil.Golden(t, "describe/no_config", out)
}

func TestDescribeArgs(t *testing.T) {
	withConfig(t, dojoConfig)
	code, out := runOut("describe", "start")
	if code != render.ExitUsage {
		t.Errorf("exit code = %d, want %d", code, render.ExitUsage)
	}
	testutil.Golden(t, "describe/args", out)
}

// TestDescribeShorter keeps describe well under the help of every verb
// together, which it summarizes.
func TestDescribeShorter(t *testing.T) {
	for _, cfg := range []string{dojoConfig, customConfig} {
		withConfig(t, cfg)
		_, describe := runOut("describe")
		helps := 0
		c, err := loadConfig()
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range c.Verbs {
			_, help := runOut(v.Name, "--help")
			helps += len(help)
		}
		if len(describe)*3 > helps*2 {
			t.Errorf("describe is %d bytes, over 2/3 of the verbs' help (%d bytes)", len(describe), helps)
		}
	}
}
