package board

import "testing"

// SetProjectsPageSize makes ListProjects read n projects per request for
// the rest of the test.
func SetProjectsPageSize(t *testing.T, n int) {
	t.Helper()
	saved := projectsPageSize
	t.Cleanup(func() { projectsPageSize = saved })
	projectsPageSize = n
}
