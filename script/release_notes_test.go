// Package script holds no Go code: its tests run the shell scripts next to
// them on temporary repositories.
package script

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repo is a temporary git repository with commits made by commit.
type repo struct {
	t   *testing.T
	dir string
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("release-notes.sh runs in release.yml on Linux and on maintainers' machines")
	}
	for _, tool := range []string{"bash", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found", tool)
		}
	}
	r := &repo{t: t, dir: t.TempDir()}
	r.git("init", "-q")
	return r
}

func (r *repo) git(args ...string) {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func (r *repo) commit(message string) {
	r.t.Helper()
	r.git("commit", "-q", "--allow-empty", "-m", message)
}

// notes runs release-notes.sh <tag> in the repository.
func (r *repo) notes(tag string) (stdout, stderr string) {
	r.t.Helper()
	script, err := filepath.Abs("release-notes.sh")
	if err != nil {
		r.t.Fatal(err)
	}
	cmd := exec.Command("bash", script, tag)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), "GITHUB_REPOSITORY=acme/tool")
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		r.t.Fatalf("release-notes.sh %s: %v\n%s", tag, err, errOut.String())
	}
	return out.String(), errOut.String()
}

func TestReleaseNotes(t *testing.T) {
	r := newRepo(t)
	r.commit("first")
	r.git("tag", "v0.1.0")
	r.commit("verbs: --comments folds the card (Refs #95)\n\n" +
		"Changelog: added: `start` and the other verbs take `--comments N|all`")
	r.commit("board: one read path (Refs #94, #92)")
	r.commit("unknown flag: suggest a flag (Refs #63, #61)\n\n" +
		"An example in backticks is no reference: `Fixes #12`.\n\n" +
		"Changelog: changed: A wrong flag suggests the closest one: `--blocked-by` → `--by`\n" +
		"Changelog: fixed: the help of `new`\n  no longer lies (#77)")
	// Fixes #50 in the same paragraph: git's %(trailers) would read none.
	r.commit("config: drop the batch key\n\n" +
		"Fixes #50\n" +
		"Changelog: Breaking: the `batch` verb key is gone\n" +
		"Changelog: feature: a kind with a typo\n" +
		"Changelog: no kind at all")
	r.git("tag", "-a", "v0.2.0", "-m", "v0.2.0")
	r.commit("internal only")

	out, warnings := r.notes("v0.2.0")
	want := "### Breaking changes\n\n" +
		"- the `batch` verb key is gone (#50)\n" +
		"\n### Added\n\n" +
		"- `start` and the other verbs take `--comments N|all` (#95)\n" +
		"\n### Changed\n\n" +
		"- A wrong flag suggests the closest one: `--blocked-by` → `--by` (#63, #61)\n" +
		"\n### Fixed\n\n" +
		"- the help of `new` no longer lies (#77)\n" +
		"\n### Other\n\n" +
		"- a kind with a typo (#50)\n" +
		"- no kind at all (#50)\n" +
		"\nAlso 1 internal commit.\n" +
		"\n**Full Changelog**: https://github.com/acme/tool/compare/v0.1.0...v0.2.0\n"
	if out != want {
		t.Errorf("notes of v0.2.0:\n%s\nwant:\n%s", out, want)
	}
	for _, w := range []string{`kind "feature"`, `kind ""`} {
		if !strings.Contains(warnings, w) {
			t.Errorf("warnings %q lack %s", warnings, w)
		}
	}

	out, _ = r.notes("HEAD")
	want = "No user-facing changes.\n" +
		"\nAlso 1 internal commit.\n" +
		"\n**Full Changelog**: https://github.com/acme/tool/compare/v0.2.0...HEAD\n"
	if out != want {
		t.Errorf("notes of HEAD:\n%s\nwant:\n%s", out, want)
	}

	out, _ = r.notes("v0.1.0")
	if want := "No user-facing changes.\n\nAlso 1 internal commit.\n"; out != want {
		t.Errorf("notes of the first tag:\n%s\nwant:\n%s", out, want)
	}
}
