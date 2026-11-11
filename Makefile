BIN := gh-dojo

# make install builds here, outside the working copy, and installs gh dojo
# from this directory. gh names a local extension after its directory, so the
# last path element must be gh-dojo.
INSTALL_DIR ?= $(HOME)/.local/share/gh-dojo/$(BIN)

.PHONY: build test vet golden install

build:
	go build -o $(BIN) ./cmd/gh-dojo

test:
	go test ./...

vet:
	go vet ./...

# Rewrite golden files under testdata/ with the current output.
golden:
	UPDATE_GOLDEN=1 go test ./...

# The installed gh dojo is a symlink to INSTALL_DIR, not to the working copy,
# so a broken branch never breaks it. Re-running replaces the binary in place
# (atomically, via mv) and only re-links the extension if it points elsewhere.
install:
	mkdir -p "$(INSTALL_DIR)"
	go build -o "$(INSTALL_DIR)/$(BIN).tmp" ./cmd/gh-dojo
	mv -f "$(INSTALL_DIR)/$(BIN).tmp" "$(INSTALL_DIR)/$(BIN)"
	@link="$${GH_DATA_DIR:-$${XDG_DATA_HOME:-$$HOME/.local/share}/gh}/extensions/$(BIN)"; \
	if [ "$$(readlink "$$link")" = "$(INSTALL_DIR)" ]; then \
		echo "gh dojo already installed from $(INSTALL_DIR); binary updated"; \
	else \
		gh extension remove dojo >/dev/null 2>&1 || true; \
		cd "$(INSTALL_DIR)" && gh extension install . && \
		echo "gh dojo installed from $(INSTALL_DIR)"; \
	fi
