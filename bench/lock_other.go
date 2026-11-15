//go:build !unix

package main

import "os"

// tryLock refuses: the benchmark locks the board with flock, which only Unix
// has.
func tryLock(*os.File) error {
	return errNoLock
}
