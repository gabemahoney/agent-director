.PHONY: all build test generate lint err-coherence nondet-coverage \
        check-doccomments test-install-sh \
        test-image test-image-smoke test-docker test-docker-install-mode list-test-docker-epics \
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

# RELEASE_VERSION is lazily evaluated: only computed when a recipe expands it
# (so `make build` never invokes jq).
RELEASE_VERSION = $(shell jq -r .version pkg/ts-bun-client/package.json)

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

# release-binaries cross-compiles the three supported targets into ./dist/.
# CGO_ENABLED=0 + modernc.org/sqlite (pure Go SQLite) yields fully static
# binaries on linux/* and standalone Mach-O on darwin/*. The -s -w
# ldflags strip the symbol + debug tables to halve the artifact size.
#
# Per SRD §16.1: mac + linux only. Windows is not supported.
# darwin/amd64 was dropped from v1 on 2026-05-24.
release-binaries:
	@mkdir -p dist
	@echo "[release] building 3 binaries into ./dist/"
	@for target in linux/amd64 linux/arm64 darwin/arm64; do \
		os=$${target%/*}; arch=$${target#*/}; \
		out="dist/agent-director-$${os}-$${arch}"; \
		echo "  -> $${out}"; \
		CGO_ENABLED=0 GOOS=$${os} GOARCH=$${arch} \
			go build -trimpath -ldflags="-s -w $(VERSION_LDFLAGS)" \
			-o "$${out}" ./cmd/agent-director || exit 1; \
	done
	@echo "[release] sizes:"
	@du -h dist/agent-director-* | sed 's/^/  /'

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
		out="dist/agent-director-$${os}-$${arch}"; \
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
		out="dist/agent-director-linux-$${arch}"; \
		if ldd "$${out}" 2>&1 | grep -q "not a dynamic executable"; then \
			echo "  $${out}: statically linked"; \
		else \
			echo "  FAIL: $${out} is not statically linked"; \
			ldd "$${out}" 2>&1 | sed 's/^/    /'; \
			exit 1; \
		fi; \
	done; \
	echo "[smoke] host-arch exec (linux-amd64 help)"; \
	./dist/agent-director-linux-amd64 help | jq -e '.verbs | length > 0' >/dev/null \
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
# Built like any other binary (no special build tags); source lives entirely
# under test/smoke/ts-helper/. modernc.org/sqlite is pure Go; CGO_ENABLED=0
# suffices. The target is incremental: it depends on every source file that
# feeds the binary, so make skips the build when nothing has changed.
TS_HELPER_SRCS := $(wildcard test/smoke/ts-helper/*.go)

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
		find skills/release-agent-director/gates -name '*.sh' | sort | xargs shellcheck -s bash; \
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
