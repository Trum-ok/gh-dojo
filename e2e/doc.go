// Package e2e runs gh dojo against real GitHub boards: a sandbox repository
// and one Projects board per preset. The tests are behind the e2e build tag,
// so go test ./... never touches the network:
//
//	GH_DOJO_E2E_REPO=Trum-ok/gh-dojo-sandbox \
//	GH_DOJO_E2E_KANBAN=6 GH_DOJO_E2E_SPRINTS=7 \
//	go test -tags e2e -count=1 -v ./e2e
//
// The token is gh's (GH_TOKEN, else gh auth login) and needs the project and
// repo (or public_repo) scopes. Variables:
//
//	GH_DOJO_E2E_REPO     owner/name of the sandbox repository (required)
//	GH_DOJO_E2E_OWNER    login that owns the boards (default: the repository owner)
//	GH_DOJO_E2E_KANBAN   number of the board for the kanban preset
//	GH_DOJO_E2E_SPRINTS  number of the board for the sprints preset, with an
//	                     iteration field Sprint that covers today
//
// A preset whose board number is not set is skipped. Each board needs the
// Status options Backlog, In progress, Waiting, Ready and Done and must be
// linked to the repository.
//
// The repository is for the tests only: they create issues titled "e2e: …",
// delete them when done, and first delete any "e2e: …" issue an earlier run
// left behind. Deleting needs admin rights on the repository.
package e2e
