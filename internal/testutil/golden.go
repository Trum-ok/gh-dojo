// Package testutil holds helpers shared by tests across packages.
package testutil

import (
	"os"
	"path/filepath"
	"testing"
)

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
