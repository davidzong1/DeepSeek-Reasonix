VERSION := $(shell git describe --tags --always 2>/dev/null || echo dev)
BUILD_TIME_UTC := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
GIT_COMMIT := $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.gitCommit=$(GIT_COMMIT) \
	-X main.buildTimeUTC=$(BUILD_TIME_UTC) \
	-X reasonix/internal/boot.builtProjectRoot=$(CURDIR)
GOEXE := $(shell go env GOEXE)
# One pin for the Makefile and the CI lint job; see .github/workflows/ci.yml.
GOLANGCI_VERSION := $(shell cat .golangci-version)
WAILS_VERSION := $(shell tr -d '[:space:]' < .wails-version)

# User-local install layout. PREFIX defaults to $(HOME)/.local so a plain
# `make install` never needs root; override for a system-wide install, e.g.
#   sudo make install PREFIX=/usr/local
# DESTDIR stages a tree for packaging: the binary lands at
# $(DESTDIR)$(PREFIX)/bin/reasonix and the team skills at
# $(DESTDIR)$(TEAM_SKILLS_DIR). Install manages ONLY the reasonix binary, the
# checked-in team role playbooks and their branch skeleton — it never creates or
# deletes anything else under $(HOME)/.reasonix (or REASONIX_HOME), and never
# touches the self-updater's .<base>.new / .<base>.old transient files next to
# an installed binary. uninstall removes the binary and leaves every user file
# alone.
PREFIX ?= $(HOME)/.local
DESTDIR ?=

# User-global team skills root: the base Go's config.UserStateDir() resolves
# (REASONIX_STATE_HOME, else REASONIX_HOME, else $(HOME)/.reasonix), joined with
# team/skills. A team session reads its role playbooks from there whatever
# directory reasonix was launched from. The order must stay identical to the Go
# side — a split would install into one root and read another.
TEAM_STATE_ROOT ?= $(if $(REASONIX_STATE_HOME),$(REASONIX_STATE_HOME),$(if $(REASONIX_HOME),$(REASONIX_HOME),$(HOME)/.reasonix))
TEAM_SKILLS_DIR ?= $(TEAM_STATE_ROOT)/team/skills

.PHONY: build vet fmt lint lint-go lint-install lint-cross lint-update wails-install test desktop-test desktop-test-short desktop-test-times sdk-test sdk-test-race hooks cross clean install install-team-skills uninstall

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/reasonix$(GOEXE) ./cmd/reasonix
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/reasonix-plugin-example$(GOEXE) ./cmd/reasonix-plugin-example

vet:
	go vet ./...

fmt:
	gofmt -w .

# Both gates CI runs, at the version CI pins. Skipping golangci-lint locally
# trades a second here for a ten-minute CI round trip: `modernize` findings in
# particular never surface in `go vet`.
lint: lint-go
	go run ./tools/repolint
	bash scripts/check-wails-pin.sh
	bash scripts/check-wails-pin.test.sh

lint-go:
	@command -v golangci-lint >/dev/null || { echo "golangci-lint not installed; run: make lint-install"; exit 1; }
	@have=$$(golangci-lint version --short 2>/dev/null); want=$$(echo "$(GOLANGCI_VERSION)" | sed 's/^v//'); \
		[ "$$have" = "$$want" ] || echo "warning: local golangci-lint $$have, CI pins $$want (make lint-install)"
	golangci-lint run --timeout=5m ./...

# CGO_ENABLED=0 keeps the install working where a stray clang on PATH shadows
# the toolchain and breaks runtime/cgo.
lint-install:
	CGO_ENABLED=0 go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)

lint-update:
	go run ./tools/repolint -update

wails-install:
	bash scripts/check-wails-pin.sh
	go install "github.com/wailsapp/wails/v2/cmd/wails@$(WAILS_VERSION)"

# Linting one GOOS leaves every //go:build windows and darwin file unchecked.
lint-cross:
	@for t in "linux ." "darwin ." "windows ." "linux desktop" "windows desktop"; do \
		set -- $$t; \
		echo "== golangci-lint GOOS=$$1 ($$2)"; \
		(cd $$2 && GOOS=$$1 golangci-lint run --timeout=5m ./...) || exit 1; \
	done

test:
	go test ./...

desktop-test:
	cd desktop && go test .

desktop-test-short:
	cd desktop && go test -short .

desktop-test-times:
	cd desktop && go test -count=1 -json . | python3 ../scripts/desktop-test-times.py

sdk-test:
	cd sdk/go && go test ./...

sdk-test-race:
	cd sdk/go && go test -race ./...

hooks:
	@git config core.hooksPath .githooks
	@echo "installed: core.hooksPath -> .githooks (pre-push runs go vet)"

cross:
	@mkdir -p dist
	@for p in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "build $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -ldflags "$(LDFLAGS)" -o dist/reasonix-$$os-$$arch$$ext ./cmd/reasonix; \
	done

clean:
	rm -rf bin dist

# install places the real reasonix binary (never a symlink) so the self-updater
# can replace it in place; a read-only bin dir fails with a hint instead of a
# cryptic permission error and a half-written install. The team skills install
# runs from the recipe, after that precheck, so a rejected prefix rejects the
# whole install: as a prerequisite it would write the state root first and then
# fail on the binary, which the abort message could not honestly deny.
install: build
	@set -e; \
	bin="$(DESTDIR)$(PREFIX)/bin"; \
	mkdir -p "$$bin" 2>/dev/null || { \
		echo "error: cannot create $$bin"; \
		echo "hint: default is 'PREFIX?=$(HOME)/.local' (no root). For system-wide: 'sudo make install PREFIX=/usr/local'. Prefer your package manager when reasonix ships there."; \
		exit 1; \
	}; \
	if [ ! -w "$$bin" ]; then \
		echo "error: $$bin is not writable by $$(id -un) — install aborted, no file was installed and no user data was touched"; \
		echo "hint: use the per-user default 'make install PREFIX=$(HOME)/.local' or a writable prefix, or run 'sudo make install PREFIX=/usr/local'; self-update ('reasonix upgrade') needs a writable install dir."; \
		exit 1; \
	fi; \
	$(MAKE) --no-print-directory install-team-skills; \
	install -m 0755 bin/reasonix$(GOEXE) "$$bin/reasonix$(GOEXE)"; \
	echo "installed: $$bin/reasonix$(GOEXE)"

# install-team-skills copies the repository's team role playbooks to
# $(TEAM_SKILLS_DIR), the user-global tree a team session reads from any
# directory. Files are installed one at a time over whatever is already there:
# a rerun is byte-identical, skills you authored yourself are left alone, and
# nothing under the destination is ever removed. The branch skeleton
# (base/<role>, shared, special/<role>) is created even where the source holds
# no file, because git cannot carry an empty directory and the role layout is
# what a session and the gap diagnostic read; an existing directory keeps its
# own mode. A staged DESTDIR install writes under the package root and never
# touches a real $HOME; a destination that cannot be created aborts before
# anything is written. scripts/check-team-skills-install.sh guards the chain.
install-team-skills:
	@set -e; \
	src="team/skills"; \
	dest="$(DESTDIR)$(TEAM_SKILLS_DIR)"; \
	if [ ! -d "$$src" ]; then \
		echo "error: $$src not found — run from the repository root"; \
		exit 1; \
	fi; \
	mkdir -p "$$dest" 2>/dev/null || { \
		echo "error: cannot create $$dest"; \
		echo "hint: TEAM_STATE_ROOT (default $(HOME)/.reasonix) must be writable, or stage with DESTDIR=/tmp/pkg."; \
		exit 1; \
	}; \
	if [ ! -w "$$dest" ]; then \
		echo "error: $$dest is not writable by $$(id -un) — install aborted, no file was installed"; \
		echo "hint: set TEAM_STATE_ROOT to a writable state root, or run 'sudo make install TEAM_STATE_ROOT=/usr/local/share/reasonix'."; \
		exit 1; \
	fi; \
	cd "$$src"; \
	for d in base/leader base/member shared special/leader special/member; do \
		[ -d "$$dest/$$d" ] || install -d -m 0755 "$$dest/$$d"; \
	done; \
	find . -type f | while IFS= read -r f; do \
		install -d -m 0755 "$$dest/$$(dirname "$$f")"; \
		install -m 0644 "$$f" "$$dest/$$f"; \
	done; \
	n=$$(find . -type f | wc -l | tr -d ' '); \
	echo "installed team skills: $$dest ($$n file(s), branch skeleton ensured, existing files kept)"

# uninstall removes only the reasonix binary installed by `make install`; it
# never removes $(PREFIX) wholesale and never touches user data under
# $(HOME)/.reasonix (or REASONIX_HOME). The installed team skills are left in
# place: you may have edited them, and a team session without them simply lists
# no team skill.
uninstall:
	@set -e; \
	bin="$(DESTDIR)$(PREFIX)/bin"; \
	f="$$bin/reasonix$(GOEXE)"; \
	if [ -e "$$f" ] || [ -L "$$f" ]; then \
		rm -f -- "$$f"; \
		echo "removed: $$f"; \
	else \
		echo "nothing to remove: $$f"; \
	fi; \
	echo "note: user data under $(HOME)/.reasonix (or REASONIX_HOME) was left untouched, including $(TEAM_SKILLS_DIR)"
