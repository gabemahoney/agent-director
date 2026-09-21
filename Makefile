.PHONY: all build test generate lint err-coherence nondet-coverage \
        check-doccomments test-install-sh \
        test-image test-image-smoke test-docker test-docker-install-mode list-test-docker-epics \
        test-sandbox sandbox-shell sandbox \
        release-binaries release-binaries-smoke \
        release-shellcheck release-bats release-smoke \
        consumer-dryrun \
        ts-helper fake-tmux \
        agent-director envelope-diff-ts \
        verify-installed-pkg-full \
        verify-prerelease-linux

# Pinned Claude Code version. Per SRD §15.2 the harness's image must install
# *this* version of @anthropic-ai/claude-code; bumping it requires re-running
# the empirical notes under reference/*-research.md before merging.
CLAUDE_CODE_VERSION ?= 2.1.120

# Docker image tag the harness uses. Override-friendly so CI can publish
# under a different name without editing the file.
TEST_IMAGE ?= agent-director-test

# Version stamp embedded via -ldflags -X. Per SR-2.6 (b.ue3 / Epic 1):
# every non-release build stamps the dev sentinel literal `0.0.0-dev`.
#
# Release path: `make release-binaries` reads .version from
#               pkg/ts-bun-client/package.json via jq.
# Dev path:     `make build` (and all other targets) stamps 0.0.0-dev.
# Env override: set AGENT_DIRECTOR_BUILD_VERSION=X.Y.Z to stamp a custom
#               value on any target; takes precedence over both paths.
#               Any non-empty value is stamped verbatim; the caller is
#               responsible for passing a value the discovery pipeline
#               can parse.
VERSION_PKG     := github.com/gabemahoney/agent-director/internal/version
ifneq ($(strip $(AGENT_DIRECTOR_BUILD_VERSION)),)
VERSION_STR     := $(AGENT_DIRECTOR_BUILD_VERSION)
else
VERSION_STR     := 0.0.0-dev
endif
COMMIT_SHA      := $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
VERSION_LDFLAGS := -X $(VERSION_PKG).Version=$(VERSION_STR) -X $(VERSION_PKG).Commit=$(COMMIT_SHA)

# RELEASE_PKG_DIR is the directory holding the canonical ts-bun-client package
# (package.json + packable sources). It defaults to the real in-repo package.
# It is an isolation hook: a caller (or test) can substitute an isolated copy
# of pkg/ts-bun-client so a version rewrite cannot race the `release-binaries`
# / `bun pm pack` step of a concurrent run (b.aur). A trailing slash is not
# required.
RELEASE_PKG_DIR ?= pkg/ts-bun-client

# RELEASE_DIST_DIR is the output directory for the cross-compiled release
# binaries. It defaults to the repo-root dist/; tests point it at a per-test
# t.TempDir() so concurrent `make release-binaries` runs (and the cleanups that
# delete the dir) never collide on the shared repo-root dist/ (b.aur).
RELEASE_DIST_DIR ?= dist

# RELEASE_VERSION is lazily evaluated: only computed when a recipe expands it
# (so `make build` never invokes jq).
RELEASE_VERSION = $(shell jq -r .version $(RELEASE_PKG_DIR)/package.json)

# Target-scoped override: make release-binaries stamps from package.json.
# AGENT_DIRECTOR_BUILD_VERSION env still wins (env override is evaluated above).
release-binaries: VERSION_STR := $(if $(strip $(AGENT_DIRECTOR_BUILD_VERSION)),$(AGENT_DIRECTOR_BUILD_VERSION),$(RELEASE_VERSION))
release-binaries: VERSION_LDFLAGS := -X $(VERSION_PKG).Version=$(VERSION_STR) -X $(VERSION_PKG).Commit=$(COMMIT_SHA)

all: generate build

build:
	CGO_ENABLED=0 go build -ldflags="$(VERSION_LDFLAGS)" -o ./bin/agent-director ./cmd/agent-director

test: envelope-diff-ts test-install-sh
	go test ./...

# test-install-sh exercises install.sh's --from-release CDN-propagation
# retry path against a fake curl (b.kym). Pure shell; no docker, no
# network. Fast — total wall time is bounded by the in-script sleeps,
# and the scenarios pick small fail-first counts.
test-install-sh:
	bash test/install-sh/retry.sh

generate:
	go generate ./...

# surface-json regenerates pkg/api/manifest/surface.json from the manifest.
# Also run by 'make generate' via the //go:generate directive in pkg/api/manifest/doc.go.
surface-json:
	go generate ./pkg/api/manifest/...

# errnames-json regenerates pkg/api/errnames/catalog.json from the err_name catalog.
# Also run by 'make generate' via the //go:generate directive in pkg/api/errnames/doc.go.
errnames-json:
	go generate ./pkg/api/errnames/...

lint:
	go vet ./...

# err-coherence runs the five-way err_name coherence gate. It asserts that:
#   (a) handler-referenced sentinels ⊆ errnames.Catalog
#   (b) api-origin Catalog entries ⊆ pkg/api exported Err* vars
#   (c) callable-verb manifest ErrorNames ⊆ errnames.Catalog
#   (d) errnames.Catalog ⊆ callable-verb manifest ErrorNames
#   (e) catalog.json and surface.json match their generators (via sub-tests)
err-coherence:
	go test ./pkg/api/errnames/ -run "TestFiveWayCoherence|TestCatalogJSONUpToDate|TestSurfaceJSONUpToDate" -v

# check-doccomments asserts that every exported identifier in pkg/api has a
# non-empty doc comment. Exits non-zero with per-identifier diagnostics if
# any are missing. Run this locally when adding a new exported symbol to
# ensure it is documented before pushing. Wired into the doc-drift CI gate.
check-doccomments:
	go run ./tools/check-doccomments -package ./pkg/api

# nondet-coverage checks that every callable verb in manifest.CallableVerbs()
# has a top-level key in test/envelope-diff/nondeterministic.json and vice
# versa. Exits non-zero with a descriptive message on any mismatch.
nondet-coverage:
	go run ./tools/check-nondet test/envelope-diff/nondeterministic.json

# Build the Docker test harness image. Always rebuilds the binary first so
# the image picks up the latest source.
test-image: build
	docker build \
		--build-arg CLAUDE_CODE_VERSION=$(CLAUDE_CODE_VERSION) \
		-t $(TEST_IMAGE) \
		-f test/Dockerfile \
		.

# Image smoke. Confirms the build succeeds, the pinned Claude version is the
# one we expect, agent-director help exits 0 from inside the container, and
# the driver script returns a clear failure for an unknown EPIC.
test-image-smoke: test-image
	@echo "[smoke] claude --version inside the image"
	docker run --rm $(TEST_IMAGE) claude --version | grep -F '$(CLAUDE_CODE_VERSION)' \
		|| (echo "ERROR: pinned claude version $(CLAUDE_CODE_VERSION) not reported"; exit 1)
	@echo "[smoke] agent-director help inside the image"
	docker run --rm $(TEST_IMAGE) agent-director help | jq -e '.verbs | length > 0' >/dev/null
	@echo "[smoke] driver rejects unknown EPIC"
	@if docker run --rm -e EPIC=nonexistent $(TEST_IMAGE) /opt/driver/run-testplan.sh 2>&1 | grep -q 'no such testplan'; then \
		echo "[smoke] OK"; \
	else \
		echo "ERROR: driver did not reject unknown EPIC with the expected message"; \
		exit 1; \
	fi

# test-docker is the canonical command form every functional Epic's
# Progression Contract references. Exact form is fixed here — changing it
# requires updating every Epic ticket that gates on it.
#
#   EPIC      — testplan slug (required). Resolved by the driver to the t1
#               collector whose title contains the slug.
#   DRIVER_MODE — "shell" (default for harness-smoke; no API calls) or
#                 "claude" (real driver-Claude; requires
#                 ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN to be set
#                 in the calling environment).
#
# Auth env vars are inherited from the host process — never hard-coded. CI
# sources them from secrets; see `.github/workflows/integration.yml` and
# docs/architecture.md "Test Harness" for the operator setup.
test-docker: test-image
	@if [ -z "$(EPIC)" ]; then \
		echo "ERROR: EPIC is required. Example: make test-docker EPIC=harness-smoke" >&2; \
		exit 2; \
	fi
	docker run --rm \
		-e EPIC=$(EPIC) \
		-e DRIVER_MODE=$${DRIVER_MODE:-shell} \
		-e ANTHROPIC_API_KEY \
		-e CLAUDE_CODE_OAUTH_TOKEN \
		-v "$(CURDIR)/tickets/testplans:/work/tickets/testplans:ro" \
		-v "$(CURDIR):/work/source:ro" \
		$(TEST_IMAGE)

# list-test-docker-epics emits one Docker harness EPIC slug per line on
# stdout, reading from test/docker-epics.txt (blank lines and comments
# stripped). Consumed by the /release skill's coverage gate (E5) to
# discover the test-docker EPIC set programmatically. Exits zero on
# success; non-zero only if the source file is missing.
.PHONY: list-test-docker-epics
list-test-docker-epics:
	@if [ ! -f test/docker-epics.txt ]; then \
		echo "ERROR: test/docker-epics.txt not found" >&2; \
		exit 1; \
	fi
	@grep -vE '^[[:space:]]*(#|$$)' test/docker-epics.txt

# test-docker-install-mode runs the b.r3j install-mode regression suite
# inside the harness container. Each scenario invokes install.sh under a
# per-scenario sandbox $HOME (and umask/--keep-prior variations) and
# asserts the canonical ~/.agent-director/bin/agent-director lands at
# literal mode 0755 via `stat -c %a` — not just `-x`. Also pins
# install.sh's defensive exit 3 on a 0644 source.
#
# Mounted read-only from the host so editing the script doesn't require
# an image rebuild. The script itself depends only on the bundled
# agent-director binary already staged at /usr/local/bin/agent-director
# and the install skill at /opt/skills/install-agent-director/ — both
# baked into the harness image.
test-docker-install-mode: test-image
	docker run --rm \
		-v "$(CURDIR)/test/install-mode:/opt/install-mode:ro" \
		--entrypoint /opt/install-mode/run.sh \
		$(TEST_IMAGE)

# ─────────────────────────────────────────────────────────────────────────
# Sandboxed execution (b.nh2 — isolation for the b.8dr incident)
#
# Every executed artifact — the full test suite, one-off builds, `go generate`,
# bun scripts, ad-hoc binary runs — runs inside a container whose HOME has NO
# .agent-director. This is the ONLY boundary that holds:
# internal/store.expandTilde resolves the home via user.Current() (/etc/passwd),
# so a host-side $HOME redirect does not stop the store from opening (and, on a
# schema-bumping branch, silently auto-migrating) the real
# ~/.agent-director/state.db. b.8dr: a `bun test` run did exactly that,
# breaking every consumer of the installed binary.
#
# The worker-facing rule: EDIT on the host freely; NEVER execute built
# artifacts on the host — run via `make test-sandbox` / `make sandbox-shell` /
# `make sandbox CMD="…"`. See docs/engineering-guide.md "Sandboxed execution".
#
# This block owns ALL environment detection so the image, the run-tests skill,
# and the CLAUDE.md note stay engine- and host-generic. It picks the container
# engine, the uid-mapping flag, and the network/pid namespace flags to match
# the host it runs on (a laptop with docker, a rootless-podman box, or this
# k8s pod), and honors a manual $(SANDBOX_FLAGS) override. See
# docs/engineering-guide.md "Sandboxed execution" for the per-host rationale.
#
# Targets:
#   test-sandbox        full suite (go test ./... AND bun test)
#   sandbox-shell       interactive bash in the container+mounts
#   sandbox CMD="…"     run an arbitrary command in the container+mounts
# ─────────────────────────────────────────────────────────────────────────

# SANDBOX_IMAGE is the image tag for the sandbox. Override-friendly so CI can
# publish under a different name without editing the file.
SANDBOX_IMAGE ?= agent-director-sandbox

# CONTAINER_ENGINE — prefer podman, else docker. Override to force one:
#   make test-sandbox CONTAINER_ENGINE=docker
CONTAINER_ENGINE ?= $(shell command -v podman >/dev/null 2>&1 && echo podman || echo docker)

# _SANDBOX_UIDMAP — uid-mapping flag(s), engine-specific:
#   podman: --userns=keep-id maps the container's non-root user to the invoking
#           host user, so host-owned mounts are writable AND the process stays
#           unprivileged (filesystem-permission tests behave; container-root
#           would bypass DAC checks). keep-id is podman-only.
#   docker: --user $(id -u):$(id -g) runs the container as the host uid/gid
#           directly, so files written stay host-owned. `--group-add 0` also
#           joins group 0: the image's HOME is gid-0-writable, and the host gid
#           is almost never 0, so without this the container process could not
#           populate GOPATH/GOCACHE/bun cache under HOME. keep-id (podman)
#           already lands on the image's uid-1000 owner, so it needs neither.
_SANDBOX_UIDMAP := $(if $(filter podman,$(CONTAINER_ENGINE)),--userns=keep-id,--user $(shell id -u):$(shell id -g) --group-add 0)

# _SANDBOX_NET — network namespace flag. Always --network=host (b.rx8), for both
# the image build and every run target. Host networking sidesteps the bridge-vs-
# uplink MTU blackhole seen 2026-09-19 on this GKE pod: docker's default bridge
# (docker0 MTU 1500) sits above the pod uplink (eth0 MTU 1460), so large inbound
# TLS segments from Fastly (release-assets.githubusercontent.com, serving the
# bun release asset) exceed the uplink MTU and the PMTUD ICMP is dropped, so the
# handshake stalls forever and the bun download in _sandbox-build hangs. The
# host netns has no such MTU step, and also covers the original b.nh2 case (no
# /dev/net/tun → rootless network backend fails), so the old tun detection
# is superseded — host networking is unconditionally correct on every supported
# host. Safe: the sandbox boundary is the filesystem/HOME, "not a security
# boundary" (test/sandbox/Dockerfile header), and no sandbox target publishes
# ports.
_SANDBOX_NET := --network=host

# _SANDBOX_ENV — engine-specific leading env. For podman the host's stale
# DOCKER_CONFIG (which may point at a nonexistent ~/.docker and abort the run)
# is neutralized; docker needs it intact for auth, so leave it alone there.
# Defined before _SANDBOX_PID because the pid probe uses it.
_SANDBOX_ENV := $(if $(filter podman,$(CONTAINER_ENGINE)),DOCKER_CONFIG=,)

# _SANDBOX_PID — pid namespace flag. Default (isolated) is correct on a normal
# host. Where /proc is masked (this k8s pod), the container's fresh /proc mount
# in a new PID namespace gets EPERM at start, so fall back to --pid=host (which
# reuses the host /proc). This is the only detection that needs a real container
# probe, so it is CACHED in a per-engine tmp marker written by _sandbox-build
# (probe once, right after the image exists). Every sandbox target depends on
# _sandbox-build, and _SANDBOX_PID is expanded LAZILY (recursive `=`) inside the
# recipe, so by the time it is read the marker is present. Repeat `make` runs
# read the marker with no container start and no latency; delete it to re-probe.
_SANDBOX_PID_CACHE := $(shell printf '%s/agent-director-sandbox-pidflag-%s' "$${TMPDIR:-/tmp}" "$(CONTAINER_ENGINE)")
_SANDBOX_PID = $(shell cat '$(_SANDBOX_PID_CACHE)' 2>/dev/null)

# GIT_COMMON_DIR resolves the real .git directory even when CURDIR is a git
# worktree (where .git is a file, not a directory). Tests that inspect git
# history need the common dir present inside the container at the SAME absolute
# path the worktree's .git pointer references.
# git rev-parse --git-common-dir prints a path RELATIVE to cwd when the common
# dir is under cwd (git <=2.34), so absolutize it. Guard the empty/non-repo
# case: $(abspath) of an empty string must stay empty (a bare $(abspath .)
# would collapse to CURDIR and inject a bogus -v mount).
_GIT_COMMON_DIR_RAW := $(shell git rev-parse --git-common-dir 2>/dev/null)
GIT_COMMON_DIR := $(if $(_GIT_COMMON_DIR_RAW),$(abspath $(_GIT_COMMON_DIR_RAW)),)

# _SANDBOX_GIT_MOUNT adds a -v for the common git dir when it lives outside the
# worktree (i.e. when working in a git worktree). Empty for a plain clone
# (common dir == the .git subdir of CURDIR, already covered by the /work mount).
_SANDBOX_GIT_MOUNT := $(if $(filter-out $(CURDIR)/.git,$(GIT_COMMON_DIR)),-v "$(GIT_COMMON_DIR):$(GIT_COMMON_DIR)",)

# _SANDBOX_RUN is the common container-run invocation shared by every sandbox
# target. Detected flags (uid map, network, pid) come first; the caller-
# supplied $(SANDBOX_FLAGS) is appended LAST so it wins over detection. The
# recipe supplies the command (and any extra flags such as -it) as the trailing
# arguments.
#
# Mounts (all read-write so `go test`'s setup.ts builds and the module/build
# caches can be populated; the container user's HOME holds the caches):
#   <worktree>       → /work                repo source (edit on host; build here)
#   <git common dir> → <same absolute path> worktree .git pointer target
#   ~/go/pkg/mod     → /go/pkg/mod          host Go module cache (speed)
#   ~/.cache/go-build→ …/.cache/go-build    host Go build cache (speed)
#   ~/.bun           → …/.bun               host bun cache (speed)
# The cache mount targets resolve to the container user's HOME, which the image
# sets to a directory containing no .agent-director.
_SANDBOX_HOME := /home/sandbox
# AGENT_DIRECTOR_TEST_SANDBOX=1 marks the container environment. The test
# suites' TestMain guards and the bun preload refuse to run without it, so a
# host-side `go test`/`bun test` fails fast instead of touching the real
# ~/.agent-director (b.nh2 / absorbed b.4v7). It is an accident-prevention gate,
# not a security boundary. Shared here so every sandbox target sets it.
_SANDBOX_RUN = $(_SANDBOX_ENV) $(CONTAINER_ENGINE) run --rm \
		$(_SANDBOX_NET) \
		$(_SANDBOX_PID) \
		$(_SANDBOX_UIDMAP) \
		-e AGENT_DIRECTOR_TEST_SANDBOX=1 \
		-v "$(CURDIR)":/work \
		$(_SANDBOX_GIT_MOUNT) \
		-v "$(HOME)/go/pkg/mod":/go/pkg/mod \
		-v "$(HOME)/.cache/go-build":$(_SANDBOX_HOME)/.cache/go-build \
		-v "$(HOME)/.bun":$(_SANDBOX_HOME)/.bun \
		$(SANDBOX_FLAGS) \
		-w /work \
		$(SANDBOX_IMAGE)

# _sandbox-preflight aborts early with a clear message if no engine is present.
# _sandbox-build (re)builds the image; the engine's layer cache makes it a fast
# no-op once built. On podman under a masked-/proc host the build's RUN steps
# need BUILDAH_ISOLATION=chroot; it is harmless (ignored) elsewhere and on
# docker. After the image exists it seeds the pid-namespace probe marker (once,
# then cached) and prints the detected configuration so a run is self-
# documenting — done here, not in preflight, because the pid flag can only be
# probed after the image is built.
.PHONY: _sandbox-preflight _sandbox-build
_sandbox-preflight:
	@if ! command -v $(CONTAINER_ENGINE) >/dev/null 2>&1; then \
		echo "ERROR: container engine '$(CONTAINER_ENGINE)' not found on PATH." >&2; \
		echo "       Install podman or docker, or set CONTAINER_ENGINE=<engine>." >&2; \
		exit 1; \
	fi

_sandbox-build: _sandbox-preflight
	BUILDAH_ISOLATION=chroot $(CONTAINER_ENGINE) build \
		$(_SANDBOX_NET) \
		-t $(SANDBOX_IMAGE) \
		-f test/sandbox/Dockerfile \
		test/sandbox
	@f='$(_SANDBOX_PID_CACHE)'; \
	if [ ! -f "$$f" ]; then \
		if $(_SANDBOX_ENV) $(CONTAINER_ENGINE) run --rm $(_SANDBOX_NET) $(SANDBOX_IMAGE) true >/dev/null 2>&1; then \
			flag=""; \
		else \
			flag="--pid=host"; \
		fi; \
		printf '%s' "$$flag" > "$$f" || { \
			echo "ERROR: cannot write pid-probe cache marker '$$f' (unwritable TMPDIR?)." >&2; \
			echo "       Set TMPDIR to a writable dir, or pass the flag explicitly, e.g. SANDBOX_FLAGS=\"$$flag\"." >&2; \
			exit 1; \
		}; \
	fi; \
	pid="$$(cat "$$f" 2>/dev/null)"; \
	echo "[sandbox] engine=$(CONTAINER_ENGINE) net='$(_SANDBOX_NET)' pid='$$pid' uidmap='$(_SANDBOX_UIDMAP)'$(if $(strip $(SANDBOX_FLAGS)), extra='$(SANDBOX_FLAGS)',)"

# test-sandbox runs the FULL suite (go test ./... then bun test) in the
# container. The suite's exit code propagates and output streams live.
# Both suites always run (the go result does NOT short-circuit bun, so one
# invocation reports both), and the combined exit is non-zero if EITHER fails.
test-sandbox: _sandbox-build
	$(_SANDBOX_RUN) \
		bash -c 'rc=0; (cd /work && go test ./...) || rc=1; (cd /work/pkg/ts-bun-client && bun test) || rc=1; exit $$rc'

# sandbox-shell drops you into an interactive bash inside the container with the
# same mounts as the test targets — the place to run builds, `go generate`,
# one-off binary runs, and bun scripts during development.
.PHONY: sandbox-shell sandbox
sandbox-shell: _sandbox-build
	$(subst $(CONTAINER_ENGINE) run,$(CONTAINER_ENGINE) run -it,$(_SANDBOX_RUN)) bash

# --- sandbox CMD transport: hardening (b.ay3) -------------------------------
# These two file-scope directives are what make the CMD value reach the
# container as INERT DATA rather than as text a HOST shell or Make re-executes.
# They must be file-scope, not target-specific (a target-specific
# `override MAKEOVERRIDES =` does NOT work — MAKEFLAGS/MAKEOVERRIDES is computed
# with global scope):
#
#   * `unexport CMD` stops Make from auto-exporting the command-line variable
#     CMD into recipe environments (where its `$(…)` would be re-expanded).
#   * `MAKEOVERRIDES =` empties the variable Make folds into MAKEFLAGS to carry
#     command-line definitions to sub-makes. That fold is the residual host-side
#     expansion channel: Make expands the raw `CMD=$(shell …)` definition text
#     while building EVERY recipe's environment (even recipes that never mention
#     CMD), so `$(shell …)` in CMD would run on the host before any container
#     exists. Emptying MAKEOVERRIDES closes that channel.
#
# Verified safe for this Makefile: the only sub-make is `$(MAKE) test-image`
# (verify-prerelease-linux), and command-line overrides of its `?=` vars
# (TEST_IMAGE, CLAUDE_CODE_VERSION) still reach it — Make also passes
# command-line definitions as real environment variables, independent of
# MAKEOVERRIDES. If you add a sub-make that must inherit an arbitrary
# command-line variable via MAKEFLAGS specifically, revisit this.
unexport CMD
MAKEOVERRIDES =

# sandbox runs an arbitrary command in the container+mounts, e.g.
#   make sandbox CMD="go build ./..."
#   make sandbox CMD="go generate ./..."
# CMD is passed to `bash -c` INSIDE the container, so shell syntax (cd, &&,
# pipes, inner quotes) works and the command's exit code propagates.
#
# CMD reaches the container VERBATIM (see `$(value CMD)` below): the bytes you
# pass are the bytes the container shell sees. So a shell variable that must
# expand in the container is written with a SINGLE `$` — `CMD='echo "$HOME"'`
# prints the container HOME. (This is the opposite of the historical `$$`
# convention: because the value is no longer run through Make's `$$`→`$`
# collapse, a literal `$$` now reaches bash as `$$` = the shell PID. b.ay3.)
#
# CMD is threaded to the container through the ENVIRONMENT, never interpolated
# into the recipe's shell line. The `export` line places CMD's value into the
# recipe process's environment; the extra `-e AGENT_DIRECTOR_SANDBOX_CMD`
# (name-only form, threaded via SANDBOX_FLAGS so it lands before the image name)
# forwards it into the container untouched, where
# `bash -c "$AGENT_DIRECTOR_SANDBOX_CMD"` runs it.
#
# SAFETY: nothing in CMD executes on the host.
#   * SHELL METACHARACTERS are safe: the value reaches the container shell as
#     one environment string, never re-tokenized by a HOST shell, so quotes,
#     `&&`, `;`, `|`, backslashes etc. stay data. This is the original b.ay3
#     fix — the old `bash -c '$(CMD)'` interpolated CMD into a single-quoted
#     HOST wrapper, so an inner single quote closed the wrapper early and a
#     trailing `&& …`/`; …` ran on the HOST (the near-miss behind b.8dr's guard).
#   * MAKE `$(…)` SYNTAX is safe too: `$(value CMD)` takes CMD's RAW UNEXPANDED
#     text (so `$(shell …)` is never expanded when the recipe env is built), and
#     the `unexport CMD` + `MAKEOVERRIDES =` directives above close the MAKEFLAGS
#     re-expansion channel. `CMD='$(shell touch /tmp/x)true'` passes the literal
#     `$(shell …)` text to the container shell, which has no `shell` command, so
#     it is inert; shell command substitution is written `$(…)` and runs in the
#     container.
#
# `override` on the SANDBOX_FLAGS addition is required: a command-line
# `make sandbox SANDBOX_FLAGS=… CMD=…` would otherwise win over a plain
# target-specific `+=` (GNU Make ignores non-override makefile assignments to
# command-line variables), dropping the `-e` forward so the container silently
# runs `bash -c ""` and reports success without running anything (b.ay3 round-2).
# `override` is target-specific here, so it does not leak the `-e` into other
# targets. As a belt-and-braces fail-closed, the in-container `:?` aborts with a
# clear message if the forward is ever missing, rather than exiting 0.
sandbox: export AGENT_DIRECTOR_SANDBOX_CMD = $(value CMD)
sandbox: override SANDBOX_FLAGS += -e AGENT_DIRECTOR_SANDBOX_CMD
sandbox: _sandbox-build
	@if [ -z "$$AGENT_DIRECTOR_SANDBOX_CMD" ]; then \
		echo 'ERROR: CMD is required. Example: make sandbox CMD="go build ./..."' >&2; \
		exit 2; \
	fi
	$(_SANDBOX_RUN) bash -c 'exec bash -c "$${AGENT_DIRECTOR_SANDBOX_CMD:?not forwarded into the container (the -e AGENT_DIRECTOR_SANDBOX_CMD flag was dropped) — refusing to run an empty command}"'

# release-binaries cross-compiles the three supported targets into
# $(RELEASE_DIST_DIR) (default ./dist/; override for test isolation — b.aur).
# CGO_ENABLED=0 + modernc.org/sqlite (pure Go SQLite) yields fully static
# binaries on linux/* and standalone Mach-O on darwin/*. The -s -w
# ldflags strip the symbol + debug tables to halve the artifact size.
#
# Per SRD §16.1: mac + linux only. Windows is not supported.
# darwin/amd64 was dropped from v1 on 2026-05-24.
release-binaries:
	@mkdir -p "$(RELEASE_DIST_DIR)"
	@echo "[release] building 3 binaries into $(RELEASE_DIST_DIR)/"
	@for target in linux/amd64 linux/arm64 darwin/arm64; do \
		os=$${target%/*}; arch=$${target#*/}; \
		out="$(RELEASE_DIST_DIR)/agent-director-$${os}-$${arch}"; \
		echo "  -> $${out}"; \
		CGO_ENABLED=0 GOOS=$${os} GOARCH=$${arch} \
			go build -trimpath -ldflags="-s -w $(VERSION_LDFLAGS)" \
			-o "$${out}" ./cmd/agent-director || exit 1; \
	done
	@echo "[release] sizes:"
	@du -h "$(RELEASE_DIST_DIR)"/agent-director-* | sed 's/^/  /'

# release-binaries-smoke runs static-linkage + magic-byte + host-arch
# runnability checks. We avoid `file(1)` because it's not in the
# minimal harness image; instead, read the first 4 magic bytes via
# od and match against ELF (0x7F454C46) or Mach-O 64-bit LE
# (0xCFFAEDFE). Arch-within-format is checked by exec where possible
# and skipped for cross-arch (cross-exec needs QEMU).
#
# All steps run inside one shell so an early `exit 1` stops the
# whole recipe (Make's default is one-shell-per-line, which would
# silently swallow the failure).
release-binaries-smoke: release-binaries
	@set -eu; \
	echo "[smoke] magic-byte check on each artifact"; \
	for target in linux/amd64 linux/arm64 darwin/arm64; do \
		os=$${target%/*}; arch=$${target#*/}; \
		out="$(RELEASE_DIST_DIR)/agent-director-$${os}-$${arch}"; \
		magic=$$(od -A n -t x1 -N 4 "$${out}" | tr -d ' '); \
		case "$${os}_$${magic}" in \
			linux_7f454c46)  echo "  $${out}: ELF (OK)" ;; \
			darwin_cffaedfe) echo "  $${out}: Mach-O 64 LE (OK)" ;; \
			darwin_feedfacf) echo "  $${out}: Mach-O 64 BE (OK)" ;; \
			*) echo "  FAIL: unexpected magic $${magic} for $${out} (os=$${os})"; exit 1 ;; \
		esac; \
	done; \
	echo "[smoke] static-link check on linux binaries (ldd → 'not a dynamic executable')"; \
	for arch in amd64 arm64; do \
		out="$(RELEASE_DIST_DIR)/agent-director-linux-$${arch}"; \
		if ldd "$${out}" 2>&1 | grep -q "not a dynamic executable"; then \
			echo "  $${out}: statically linked"; \
		else \
			echo "  FAIL: $${out} is not statically linked"; \
			ldd "$${out}" 2>&1 | sed 's/^/    /'; \
			exit 1; \
		fi; \
	done; \
	echo "[smoke] host-arch exec (linux-amd64 help)"; \
	"$(RELEASE_DIST_DIR)/agent-director-linux-amd64" help | jq -e '.verbs | length > 0' >/dev/null \
		|| { echo "FAIL: linux-amd64 help did not return a non-empty verb list"; exit 1; }; \
	echo "[smoke] OK — all 3 binaries built, linked, and the host-arch one runs"

# consumer-dryrun builds the tools/consumer-dryrun mini-module, which imports
# pkg/api from a separate Go module via a replace directive. A clean build
# proves that external consumers can compile against pkg/api without
# referencing any internal/* package directly. Go's visibility rules enforce
# this: any attempt to import internal/* from outside the module would fail.
consumer-dryrun:
	cd tools/consumer-dryrun && go build ./...

# ts-helper builds the fixture-seeding CLI used by TypeScript smoke tests.
# Built like any other binary (no special build tags); source lives under
# test/smoke/ts-helper/, but the CLI also imports pkg/api/apitest (SeedSpawn)
# to seed stores. modernc.org/sqlite is pure Go; CGO_ENABLED=0 suffices. The
# target is incremental: it depends on every source file that feeds the
# binary — including the apitest seeder sources — so make rebuilds when a
# seeder changes and skips the build when nothing has changed. Without the
# apitest prereqs, a stale binary can seed pre-migration stores (b.93m).
TS_HELPER_SRCS := $(wildcard test/smoke/ts-helper/*.go) $(wildcard pkg/api/apitest/*.go)

bin/ts-helper: $(TS_HELPER_SRCS)
	CGO_ENABLED=0 go build -o bin/ts-helper ./test/smoke/ts-helper/

ts-helper: bin/ts-helper

# fake-tmux builds the test-only tmux stub used by TypeScript smoke tests.
# The stub records argv calls and exits 0 so spawn/send-keys/read-pane/kill
# can be exercised end-to-end without a real tmux. Compiled with CGO_ENABLED=0
# (pure Go, no libc dependency).
test/fake-tmux/tmux: test/fake-tmux/main.go
	CGO_ENABLED=0 go build -o test/fake-tmux/tmux ./test/fake-tmux/ && chmod 755 test/fake-tmux/tmux

fake-tmux: test/fake-tmux/tmux

# agent-director is a focused alias for `make build` used by the TS
# envelope-diff harness and setup.ts.  Incremental: re-running with no source
# changes is a fast no-op because `build` itself is not phony (the binary
# exists and is up-to-date).  Listed in .PHONY above so `make agent-director`
# always delegates to the build recipe.
agent-director: build

# envelope-diff-ts runs the TS-side envelope-diff regression suite.
#
# Dependencies:
#   agent-director — ensures bin/agent-director is built
#   ts-helper      — ensures bin/ts-helper is built
#   fake-tmux      — ensures test/fake-tmux/tmux is built
#
# The test runner is invoked from the pkg/ts-bun-client directory so that
# bunfig.toml and the local package.json are in scope.
envelope-diff-ts: agent-director ts-helper fake-tmux
	cd pkg/ts-bun-client && bun test test/envelope-diff.test.ts test/envelope-diff-invariants.test.ts

# release-shellcheck runs shellcheck against gate scripts under
# skills/release-agent-director/gates/. The target is a no-op when
# shellcheck is not installed locally so that bare `make` runs do not
# require it. Add `SC2086` etc. to the disable list inline in the
# respective script rather than globally here.
release-shellcheck:
	@if command -v shellcheck >/dev/null 2>&1; then \
		echo "[release-shellcheck] shellcheck skills/release-agent-director/gates/**/*.sh"; \
		find skills/release-agent-director/gates -name '*.sh' | sort | xargs shellcheck -x -P SCRIPTDIR -s bash; \
	else \
		echo "[release-shellcheck] shellcheck not installed — skipping"; \
	fi

# release-smoke runs the synthetic-regression test suite that replaced the
# legacy test-*.sh harnesses (E10 retirement). Each test covers one gate or
# phase invariant from the /release skill.
release-smoke:
	go test ./skills/release-agent-director/tests/synthetic-regressions/... -count=1

# release-bats was retired alongside the cabi-matrix removal — the only
# bats tests under skills/release-agent-director/tests/ exercised the
# deleted cabi-collection paths. The target is kept as a no-op so any
# stale CI lane that still calls it stays green.
release-bats:
	@echo "[release-bats] no release bats tests in tree — skipping"

# verify-installed-pkg-full performs a self-contained end-to-end install
# verification of the ts-bun-client package against a real packed tarball.
# It builds the release binaries, stages the host CLI into the platform
# sub-packages, packs the umbrella tarball, installs it into an isolated
# consumer project, and runs the --full makeTemplate gauntlet driver.
# Temp HOME and consumer project dir are cleaned up via EXIT trap.
verify-installed-pkg-full: SHELL = /bin/bash
verify-installed-pkg-full: release-binaries
	@set -eu; \
	REPO_ROOT="$$(pwd)"; \
	log() { local lvl="$$1"; shift; echo "[$$lvl] $$*"; }; \
	_OS=$$(uname -s | tr '[:upper:]' '[:lower:]'); \
	_ARCH=$$(uname -m); \
	case "$${_OS}-$${_ARCH}" in \
		linux-x86_64)  _HOST_CROSS="linux-amd64"; _HOST_PKG="linux-x64" ;; \
		darwin-arm64)  _HOST_CROSS="darwin-arm64"; _HOST_PKG="darwin-arm64" ;; \
		*) echo "unsupported host: $${_OS}-$${_ARCH}" >&2; exit 1 ;; \
	esac; \
	echo "[verify-installed-pkg-full] staging CLI into platform packages"; \
	_STAGE_SRC="$$REPO_ROOT/dist/agent-director-$${_HOST_CROSS}"; \
	_STAGE_DEST="$$REPO_ROOT/pkg/ts-bun-client/platforms/$${_HOST_PKG}/bin"; \
	if [[ ! -f "$$_STAGE_SRC" ]]; then echo "missing $$_STAGE_SRC — was make release-binaries run?" >&2; exit 1; fi; \
	mkdir -p "$$_STAGE_DEST"; \
	cp "$$_STAGE_SRC" "$$_STAGE_DEST/agent-director"; \
	chmod 0755 "$$_STAGE_DEST/agent-director"; \
	log verify-installed-pkg-full "staged $$_STAGE_SRC → $$_STAGE_DEST/agent-director"; \
	TMP_STAGING=$$(mktemp -d); \
	TMP_HOME=$$(mktemp -d); \
	TMP_CONSUMER=$$(mktemp -d); \
	trap 'rm -rf "$$TMP_STAGING" "$$TMP_HOME" "$$TMP_CONSUMER"' EXIT; \
	echo "[verify-installed-pkg-full] installing devDependencies (bun-types, typescript) for build"; \
	cd "$$REPO_ROOT/pkg/ts-bun-client" && bun install --no-progress >/dev/null; \
	echo "[verify-installed-pkg-full] packing umbrella tarball"; \
	cd "$$REPO_ROOT/pkg/ts-bun-client" && bun run build && bun pm pack --destination "$$TMP_STAGING"; \
	TARBALL=$$(ls "$$TMP_STAGING"/*.tgz); \
	echo "[verify-installed-pkg-full] installing into consumer project"; \
	cd "$$TMP_CONSUMER"; \
	printf '{"name":"verify-consumer","version":"1.0.0","type":"module"}\n' > package.json; \
	HOME="$$TMP_HOME" bun add "$$TARBALL"; \
	HOME="$$TMP_HOME" bun add "file:$$REPO_ROOT/pkg/ts-bun-client/platforms/$$_HOST_PKG"; \
	echo "[verify-installed-pkg-full] running --full gauntlet"; \
	cp "$$REPO_ROOT/pkg/ts-bun-client/scripts/verify-installed-pkg.ts" "$$TMP_CONSUMER/"; \
	HOME="$$TMP_HOME" bun "$$TMP_CONSUMER/verify-installed-pkg.ts" --full

# verify-prerelease-linux runs the pre-release Linux Docker verify gate.
# Stages the linux-amd64 CLI binary, packs the umbrella tarball on the host,
# copies the linux-x64 platform sub-package into the staging tmpdir, then mounts
# everything into the test container and runs the consumer-install + --full flow.
# OTQ-1 resolution: test/Dockerfile already pins Bun (BUN_VERSION=1.3.13) and
# installs it; this recipe reuses $(TEST_IMAGE) from make test-image —
# no new Dockerfile added.
verify-prerelease-linux: SHELL = /bin/bash
verify-prerelease-linux: release-binaries
	@set -eu; \
	REPO_ROOT="$$(pwd)"; \
	log() { local lvl="$$1"; shift; echo "[$$lvl] $$*"; }; \
	TMP_STAGING=$$(mktemp -d); \
	trap 'rm -rf "$$TMP_STAGING"' EXIT; \
	log verify-prerelease-linux "staging linux-x64 CLI binary"; \
	_STAGE_SRC="$$REPO_ROOT/dist/agent-director-linux-amd64"; \
	_STAGE_DEST="$$REPO_ROOT/pkg/ts-bun-client/platforms/linux-x64/bin"; \
	if [[ ! -f "$$_STAGE_SRC" ]]; then printf 'FAIL stage-cli: missing %s\n' "$$_STAGE_SRC" >&2; exit 1; fi; \
	mkdir -p "$$_STAGE_DEST"; \
	cp "$$_STAGE_SRC" "$$_STAGE_DEST/agent-director"; \
	chmod 0755 "$$_STAGE_DEST/agent-director" \
		|| { printf 'FAIL stage-cli\n' >&2; exit 1; }; \
	log verify-prerelease-linux "packing umbrella tarball → $$TMP_STAGING"; \
	( cd "$$REPO_ROOT/pkg/ts-bun-client" && bun run build && bun pm pack --destination "$$TMP_STAGING" ) \
		|| { printf 'FAIL bun-pack\n' >&2; exit 1; }; \
	log verify-prerelease-linux "copying linux-x64 platform sub-package into staging dir"; \
	mkdir -p "$$TMP_STAGING/platforms"; \
	cp -r "$$REPO_ROOT/pkg/ts-bun-client/platforms/linux-x64" "$$TMP_STAGING/platforms/linux-x64" \
		|| { printf 'FAIL copy-platform\n' >&2; exit 1; }; \
	log verify-prerelease-linux "building/reusing $(TEST_IMAGE)"; \
	$(MAKE) test-image \
		|| { printf 'FAIL test-image\n' >&2; exit 1; }; \
	VERIFY_SCRIPT="$$REPO_ROOT/pkg/ts-bun-client/scripts/verify-installed-pkg.ts"; \
	INNER_CMD="set -eu; C=\$$(mktemp -d); cd \$$C && jq -n '{name:\"verify-consumer\",version:\"1.0.0\",type:\"module\"}' > package.json && bun add /staging/*.tgz && bun add file:/staging/platforms/linux-x64 && bun /verify.ts --full"; \
	if [[ -n "$${VERIFY_PRERELEASE_DRY_RUN:-}" ]]; then \
		echo "docker run --rm -v \"$$TMP_STAGING\":/staging:ro -v \"$$VERIFY_SCRIPT\":/verify.ts:ro $(TEST_IMAGE) bash -c \"$$INNER_CMD\""; \
		exit 0; \
	fi; \
	docker run --rm \
		-v "$$TMP_STAGING":/staging:ro \
		-v "$$VERIFY_SCRIPT":/verify.ts:ro \
		$(TEST_IMAGE) \
		bash -c "$$INNER_CMD" \
		|| { printf 'FAIL docker-run\n' >&2; exit 1; }
