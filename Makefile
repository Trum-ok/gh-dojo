BIN := gh-dojo

# Stamped into the binary; `gh dojo --version` prints it ("dev" without it).
VERSION ?= $(shell git describe --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

# make install builds here, outside the working copy, and installs gh dojo
# from this directory. gh names a local extension after its directory, so the
# last path element must be gh-dojo.
INSTALL_DIR ?= $(HOME)/.local/share/gh-dojo/$(BIN)

.PHONY: build test vet golden agent dist install demo

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/gh-dojo

test:
	go test ./...

vet:
	go vet ./...

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
