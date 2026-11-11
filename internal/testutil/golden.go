// Package testutil holds helpers shared by tests across packages.
package testutil

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files with the current output")

// Golden compares got with testdata/<name>.golden in the calling package's
// directory. name may contain slashes to group files.
//
// To rewrite golden files run `go test ./pkg -update` for one package, or
// `UPDATE_GOLDEN=1 go test ./...` (make golden) for all of them.
func Golden(t testing.TB, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", filepath.FromSlash(name)+".golden")
	if *update || os.Getenv("UPDATE_GOLDEN") != "" {
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
		t.Fatalf("read golden file: %v (run with -update to create it)", err)
	}
	if string(got) != string(want) {
		t.Errorf("output differs from %s (run with -update to accept)\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}
