package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestFindStopsAtRepoRoot(t *testing.T) {
	for _, dotGitIsFile := range []bool{false, true} {
		outer := writeConfig(t, presetOnly) // a config above the repository
		repo := filepath.Join(outer, "repo")
		sub := filepath.Join(repo, "sub")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(repo, ".git")
		var err error
		if dotGitIsFile {
			err = os.WriteFile(marker, []byte("gitdir: /elsewhere\n"), 0o644)
		} else {
			err = os.Mkdir(marker, 0o755)
		}
		if err != nil {
			t.Fatal(err)
		}
		if path, err := find(sub); err == nil {
			t.Errorf(".git file=%v: found %s outside the repository", dotGitIsFile, path)
		}
		// The repository root itself is still searched.
		if err := os.MkdirAll(filepath.Join(repo, ".github"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, ".github", "dojo.toml"), []byte(presetOnly), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := find(sub); err != nil {
			t.Errorf(".git file=%v: %v", dotGitIsFile, err)
		}
	}
}

func TestFindSkipsGithubFile(t *testing.T) {
	root := writeConfig(t, presetOnly)
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, ".github"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := find(sub); err != nil {
		t.Errorf("a .github file must not stop the search: %v", err)
	}
}

func TestPresetVersion(t *testing.T) {
	for _, data := range []string{"[fields]\nstatus = \"S\"\n", "version = 2\n"} {
		_, err := parseRaw([]byte(data), "preset test", true)
		if err == nil || !strings.Contains(err.Error(), "version must be 1") {
			t.Errorf("%q: got %v, want a version error", data, err)
		}
	}
	if _, err := parseRaw([]byte("version = 1\n[board]\nowner = \"x\"\n"), "preset test", true); err == nil {
		t.Error("a preset with [board] must be rejected")
	}
}

func TestVerbUses(t *testing.T) {
	v := Verb{Comment: "Waiting for {by}. {session}"}
	if !v.Uses(ArgBy) || v.Uses(ArgComment) || !v.Uses(VarSession) {
		t.Errorf("Uses is wrong for %q", v.Comment)
	}
	c := mustLoad(t, presetOnly)
	for _, v := range c.Verbs {
		for _, arg := range append(slices.Clone(v.Require), v.RequireOneOf...) {
			if !v.Uses(arg) {
				t.Errorf("%s requires %s but does not use it", v.Name, arg)
			}
		}
		if !v.Uses(ArgComment) {
			t.Errorf("kanban verb %s should accept --comment", v.Name)
		}
	}
}

// TestEnterpriseManagedLogins: an EMU login ("_shortcode" suffix) is a
// login everywhere one is read: the config's assignees and the flag.
func TestEnterpriseManagedLogins(t *testing.T) {
	const emu = "mona-cat_octo"
	for _, s := range []string{"octocat", "a", emu, "acme_ent", strings.Repeat("a", 39) + "_" + strings.Repeat("b", 20)} {
		if !IsLogin(s) {
			t.Errorf("IsLogin(%q) = false", s)
		}
		if err := AssigneeArgError("assignee", s); err != nil {
			t.Errorf("AssigneeArgError(%q) = %v", s, err)
		}
	}
	for _, s := range []string{"", "-octo", "_octo", "mona cat", "me@x", "@octo", strings.Repeat("a", 101)} {
		if IsLogin(s) {
			t.Errorf("IsLogin(%q) = true", s)
		}
		if AssigneeArgError("assignee", s) == nil {
			t.Errorf("AssigneeArgError(%q) = nil", s)
		}
	}

	cfg := strings.Replace(presetOnly, "owner = \"Trum-ok\"\n", "owner = \"acme_ent\"\nassignee = \""+emu+"\"\n", 1) +
		"\n[verbs.start]\nassign = \"" + emu + "\"\nunassign = \"" + emu + "x\"\n"
	if _, err := Parse([]byte(cfg), "emu test"); err != nil {
		t.Errorf("EMU logins in the config: %v", err)
	}
}

func TestSessionConfigured(t *testing.T) {
	if (Session{}).Configured() {
		t.Error("empty session is configured")
	}
	if !(Session{Env: []string{"X"}}).Configured() {
		t.Error("session with env is not configured")
	}
}

func TestArgsIsACopy(t *testing.T) {
	Args()[0] = "x"
	if Args()[0] != ArgComment {
		t.Error("Args must return a copy")
	}
}
