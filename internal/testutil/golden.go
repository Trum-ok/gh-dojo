// Package testutil holds helpers shared by tests across packages.
package testutil

import (
	"os"
	"path/filepath"
	"testing"
)

// usedEnv names a file that Golden appends the absolute path of every
// golden file it reads to, one per line; script/unused-golden.sh lists the
// golden files no test read.
const usedEnv = "GOLDEN_USED"

// recordUse appends path to the file usedEnv names, if set. One short
// O_APPEND write per line keeps the lines whole when the packages' test
// binaries run in parallel.
func recordUse(t testing.TB, path string) {
	t.Helper()
	log := os.Getenv(usedEnv)
	if log == "" {
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil { // as pwd -P in the script
		abs = real
	}
	f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(filepath.ToSlash(abs) + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
}

// updateEnv names the environment variable that rewrites golden files. An
// environment variable, unlike a test flag, works with `go test ./...`
// across packages that do not all define the flag.
const updateEnv = "UPDATE_GOLDEN"

// Golden compares got with testdata/<name>.golden in the calling package's
// directory. name may contain slashes to group files.
//
// To rewrite golden files run `UPDATE_GOLDEN=1 go test ./...` (make golden)
// and review the diff. Rewriting is refused when CI is set, so a CI run can
// never pass by accepting its own output.
func Golden(t testing.TB, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", filepath.FromSlash(name)+".golden")
	recordUse(t, path)
	if os.Getenv(updateEnv) != "" {
		if os.Getenv("CI") != "" {
			t.Fatalf("%s is set together with CI; golden files are never rewritten in CI", updateEnv)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden file: %v (run `make golden` to create it)", err)
	}
	if string(got) != string(want) {
		t.Errorf("output differs from %s (run `make golden` to accept, then review the diff)\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}
