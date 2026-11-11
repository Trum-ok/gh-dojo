package testutil

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestGoldenRefusesUpdateInCI runs the helper in a child process, since it
// must call t.Fatal.
func TestGoldenRefusesUpdateInCI(t *testing.T) {
	if os.Getenv("GOLDEN_CHILD") != "" {
		Golden(t, "never_written", []byte("x"))
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestGoldenRefusesUpdateInCI$")
	cmd.Env = append(os.Environ(), "GOLDEN_CHILD=1", updateEnv+"=1", "CI=true")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("Golden with %s and CI set passed; want a failure\n%s", updateEnv, out)
	}
	if !strings.Contains(string(out), "never rewritten in CI") {
		t.Errorf("unexpected failure output:\n%s", out)
	}
	if _, err := os.Stat("testdata/never_written.golden"); !os.IsNotExist(err) {
		t.Errorf("golden file was written in CI (stat err: %v)", err)
		os.RemoveAll("testdata")
	}
}
