package main

import (
	"strings"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

// spelledConfig writes the default repository trum-ok/GH-Dojo, which
// GitHub spells Trum-ok/gh-dojo.
var spelledConfig = strings.Replace(dojoConfig, `repos = ["Trum-ok/gh-dojo"]`, `repos = ["trum-ok/GH-Dojo"]`, 1)

// TestDoctorSpelling: board.repos spelled other than GitHub spells it
// works, since GitHub matches names without case, but output names the
// default repository as the config spells it: a warning (exit code 0)
// with GitHub's spelling as the fix.
func TestDoctorSpelling(t *testing.T) {
	withConfig(t, spelledConfig)
	withApp(t, doctorFixtures+"spelling.json", boardFixture)
	code, out := runOut("doctor")
	if code != render.ExitOK {
		t.Errorf("exit code = %d, want 0\n%s", code, out)
	}
	testutil.Golden(t, "doctor/spelling", out)
	withApp(t, doctorFixtures+"spelling.json", boardFixture)
	code, out = runOut("doctor", "--jq", `.checks[] | select(.name == "repo")`)
	if code != render.ExitOK {
		t.Errorf("exit code = %d, want 0\n%s", code, out)
	}
	testutil.Golden(t, "doctor/spelling_json", out)
}

// TestListDefaultSpelling: list and next name the default repository as
// the config spells it, as show and the verbs do, in their issues and
// blockers; the text still names its issues #N.
func TestListDefaultSpelling(t *testing.T) {
	for _, tt := range []struct {
		name     string
		fixtures []string
		args     []string
	}{
		{"list", []string{boardFixture, itemsTwoPagesFixture}, []string{"list", "--all", "--jq", `[.issues[].repo] | unique`}},
		{"list_text", []string{boardFixture, itemsTwoPagesFixture}, []string{"list", "--limit", "3"}},
		{"next", []string{boardFixture, nextTwoPagesFixture},
			[]string{"next", "--all", "--jq", `[.issues[] | .repo, .unresolved_blockers[].ref, .unresolved_blockers[].repo] | unique`}},
		{"next_text", []string{boardFixture, nextTwoPagesFixture}, []string{"next", "--all"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, spelledConfig)
			withApp(t, tt.fixtures...)
			code, out := runOut(tt.args...)
			if code != render.ExitOK {
				t.Errorf("code = %d\n%s", code, out)
			}
			testutil.Golden(t, "list/default_spelling_"+tt.name, out)
		})
	}
}
