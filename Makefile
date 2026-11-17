BIN := gh-dojo

# Stamped into the binary; `gh dojo --version` prints it ("dev" without it).
VERSION ?= $(shell git describe --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

# make install builds here, outside the working copy, and installs gh dojo
# from this directory. gh names a local extension after its directory, so the
# last path element must be gh-dojo.
INSTALL_DIR ?= $(HOME)/.local/share/gh-dojo/$(BIN)

.PHONY: build test vet lint vuln tools golden agent dist install demo \
	check fmt tidy e2e release

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/gh-dojo

test:
	go test ./...

# go vet on every system the code builds differently for, and on the e2e
# tests, as CI does.
vet:
	go vet ./...
	GOOS=windows go vet ./...
	GOOS=darwin go vet ./...
	go vet -tags e2e ./e2e

# Files gofmt would change; `gofmt -w .` fixes them.
fmt:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed on:"; echo "$$unformatted"; exit 1; \
	fi

# go.mod and go.sum, of gh-dojo and of tools/, as go mod tidy would write
# them.
tidy:
	go mod tidy -diff
	cd tools && go mod tidy -diff

# The checks of CI (.github/workflows/ci.yml) on this machine: format, tidy,
# vet, staticcheck, the release build and its agent install --check, then
# go test -race (failing on golden files no test reads) and go build. Not
# vuln, which needs the network, nor e2e, which needs a token and the
# sandbox boards. The tests run on this system only; CI runs them on Linux,
# macOS and Windows.
check: fmt tidy vet lint dist
	@bin="dist/$(BIN)_$(VERSION)_$$(go env GOOS)-$$(go env GOARCH)"; \
	sums="$(BIN)_$(VERSION)_checksums.txt"; set -ex; \
	"$$bin" --version | grep -Fx "gh dojo version $(VERSION)"; \
	(cd dist && shasum -a 256 -c --strict "$$sums"); \
	test "$$(wc -l <"dist/$$sums")" -eq 6; \
	"$$bin" agent install --check
	script/unused-golden.sh -race
	go build ./...

# The linters, at the versions pinned in tools/go.mod, built for this machine
# into .tools/ so they can check the code as other systems see it (GOOS).
TOOLS := $(CURDIR)/.tools

tools:
	go build -modfile=tools/go.mod -o $(TOOLS)/ \
		honnef.co/go/tools/cmd/staticcheck golang.org/x/vuln/cmd/govulncheck

# staticcheck on every system CI vets, and on the e2e tests.
lint: tools
	$(TOOLS)/staticcheck ./...
	GOOS=windows $(TOOLS)/staticcheck ./...
	GOOS=darwin $(TOOLS)/staticcheck ./...
	$(TOOLS)/staticcheck -tags e2e ./e2e

# Known vulnerabilities in the code gh-dojo calls, its modules and the Go
# standard library it is built with.
vuln: tools
	$(TOOLS)/govulncheck ./...

# The end-to-end tests against the sandbox boards (see e2e/doc.go), with
# gh's token; .github/workflows/e2e.yml runs them daily.
GH_DOJO_E2E_REPO ?= Trum-ok/gh-dojo-sandbox
GH_DOJO_E2E_KANBAN ?= 6
GH_DOJO_E2E_SPRINTS ?= 7

e2e:
	GH_DOJO_E2E_REPO=$(GH_DOJO_E2E_REPO) GH_DOJO_E2E_KANBAN=$(GH_DOJO_E2E_KANBAN) \
		GH_DOJO_E2E_SPRINTS=$(GH_DOJO_E2E_SPRINTS) \
		go test -tags e2e -count=1 -v -timeout 15m ./e2e

# Rewrite golden files under testdata/ with the current output; review the
# diff before committing. Refused when CI is set.
golden:
	UPDATE_GOLDEN=1 go test ./...

# Regenerate the agent skill from .github/dojo.toml with this branch's
# build, after changing the config, its notes or a command; CI runs
# agent install --check.
agent:
	go run ./cmd/gh-dojo agent install

# Cross-compile the release binaries into dist/ the way the release workflow
# does (script/build-release.sh), stamped with VERSION.
dist:
	script/build-release.sh $(VERSION)

# Release TAG from main: tag, push, wait for release.yml, make install, then
# move the release's issues to Done (script/release.sh; CONTRIBUTING.md,
# Releases). Only on the user's word for the tag and the issues.
release:
	@test -n "$(TAG)" || { echo "usage: make release TAG=vX.Y.Z" >&2; exit 1; }
	script/release.sh $(TAG)

# Render the README media (docs/media/*.gif) from its Remotion project; needs
# Node and ffmpeg. See docs/media/README.md.
demo:
	cd docs/media && { [ -d node_modules ] || npm ci; } && ./render.sh

# The installed gh dojo is a symlink to INSTALL_DIR, not to the working copy,
# so a broken branch never breaks it.
#
# The binary is built next to its final place and moved in atomically, so a
# failed build changes nothing. If gh's extension link already points at
# INSTALL_DIR, that is all. Otherwise any other gh dojo extension is parked
# aside, gh dojo is installed from INSTALL_DIR, and the parked one is deleted
# on success or put back on failure, so gh dojo is never left missing.
#
# The link lives where gh 2.x keeps extensions: GH_DATA_DIR is not honoured
# for extensions, so it is deliberately not used here.
install:
	@set -e; \
	dir="$(INSTALL_DIR)"; dir="$${dir%/}"; \
	if [ "$$(basename "$$dir")" != "$(BIN)" ]; then \
		echo "INSTALL_DIR must end in /$(BIN), gh names the extension after it: $$dir" >&2; \
		exit 1; \
	fi; \
	link="$${XDG_DATA_HOME:-$$HOME/.local/share}/gh/extensions/$(BIN)"; \
	mkdir -p "$$dir"; \
	go build -ldflags "$(LDFLAGS)" -o "$$dir/$(BIN).tmp" ./cmd/gh-dojo; \
	mv -f "$$dir/$(BIN).tmp" "$$dir/$(BIN)"; \
	if [ -L "$$link" ] && \
		[ "$$(cd "$$link" 2>/dev/null && pwd -P)" = "$$(cd "$$dir" && pwd -P)" ]; then \
		echo "gh dojo $(VERSION) updated in $$dir"; \
		exit 0; \
	fi; \
	parked=""; previous=""; \
	if [ -e "$$link" ] || [ -L "$$link" ]; then \
		previous="$$(readlink "$$link" 2>/dev/null || echo "$$link")"; \
		parked="$$(mktemp -d "$$(dirname "$$dir")/.previous-extension.XXXXXX")"; \
		mv "$$link" "$$parked/$(BIN)"; \
	fi; \
	if (cd "$$dir" && gh extension install .); then \
		if [ -n "$$parked" ]; then \
			rm -rf "$$parked"; \
			echo "removed the previous gh dojo extension: $$link -> $$previous"; \
		fi; \
		echo "gh dojo $(VERSION) installed from $$dir"; \
	else \
		if [ -n "$$parked" ]; then \
			if [ -e "$$link" ] || [ -L "$$link" ]; then \
				echo "install failed; the previous gh dojo extension is kept in $$parked" >&2; \
			else \
				mv "$$parked/$(BIN)" "$$link" && rmdir "$$parked"; \
				echo "install failed; the previous gh dojo extension is restored" >&2; \
			fi; \
		fi; \
		exit 1; \
	fi
