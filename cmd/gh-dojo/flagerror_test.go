package main

import (
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

// An unknown flag lists the command's flags, the other commands that take
// it and a close flag of the command, when one is clearly closest (#63).
// Every case fails before any request.
func TestUnknownFlag(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		// A typo of the command's own flag.
		{"typo", []string{"start", "42", "--dryrun"}},
		{"typo_output_flag", []string{"start", "42", "--josn"}},
		// Another command's flag with the same last word: new's --blocked-by.
		{"last_word", []string{"wait", "42", "--blocked-by", "12"}},
		// Another command's flag with nothing close here.
		{"other_command", []string{"next", "--status", "Backlog"}},
		{"other_commands_cut", []string{"show", "42", "--dry-run"}},
		// --comment and --comments are as close as each other: no guess.
		{"tie", []string{"start", "42", "--commentz", "x"}},
		// A short name is close to nothing: --x is not --by.
		{"short", []string{"block", "42", "--x"}},
		{"nothing", []string{"start", "42", "--nope"}},
		// A shorthand keeps pflag's error: -1 is mostly a number, not a flag.
		{"shorthand", []string{"new", "Title", "-1"}},
		{"json", []string{"wait", "42", "--blocked-by", "12", "--json"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, verbsConfig)
			f := &fakeBackend{}
			withBackend(t, f)
			code, out := runOut(tt.args...)
			if code != render.ExitUsage {
				t.Errorf("exit code = %d, want %d\n%s", code, render.ExitUsage, out)
			}
			if f.reads != 0 {
				t.Errorf("reads = %d, want 0", f.reads)
			}
			testutil.Golden(t, "flagerror/"+tt.name, out)
		})
	}
}

func TestJoinCapped(t *testing.T) {
	for _, tc := range []struct {
		names []string
		want  string
	}{
		{[]string{"a"}, "a"},
		{[]string{"a", "b", "c"}, "a, b and c"},
		// One over the limit is named, not "and 1 more".
		{[]string{"a", "b", "c", "d"}, "a, b, c and d"},
		{[]string{"a", "b", "c", "d", "e"}, "a, b, c and 2 more"},
	} {
		if got := joinCapped(tc.names, 3); got != tc.want {
			t.Errorf("joinCapped(%q, 3) = %q, want %q", tc.names, got, tc.want)
		}
	}
}
