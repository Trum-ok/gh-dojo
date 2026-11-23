package main

import (
	"errors"
	"fmt"
	"testing"
)

func TestStopsRun(t *testing.T) {
	// go-gh's HTTPError text carries the URL, and the default repository's
	// name has "sandbox" in it.
	transient := "seed: HTTP 502: Bad Gateway (https://api.github.com/repos/Trum-ok/gh-dojo-sandbox/issues)"
	if stopsRun(agentResult{HarnessError: transient}) {
		t.Errorf("a transient HTTP error naming the sandbox repository stops the run")
	}

	err := fmt.Errorf("%w (OSStatus -26276), see t.jsonl", errSandboxBroke)
	if !errors.Is(err, errSandboxBroke) {
		t.Fatal("the wrapped error is not errSandboxBroke")
	}
	r := agentResult{HarnessError: err.Error(), broken: errors.Is(err, errSandboxBroke)}
	if !stopsRun(r) {
		t.Errorf("the sandbox sentinel does not stop the run")
	}

	if !stopsRun(agentResult{HarnessError: "Not logged in"}) {
		t.Errorf("a missing login does not stop the run")
	}
}
