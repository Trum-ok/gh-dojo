// Command gh-dojo is a GitHub CLI extension that gives AI agents process
// verbs over GitHub Projects v2. Run it as `gh dojo`.
package main

import "os"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}
