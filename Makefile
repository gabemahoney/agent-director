.PHONY: all build test generate lint test-image test-image-smoke

# Pinned Claude Code version. Per SRD §15.2 the harness's image must install
# *this* version of @anthropic-ai/claude-code; bumping it requires re-running
# the empirical notes under reference/*-research.md before merging.
CLAUDE_CODE_VERSION ?= 2.1.120

# Docker image tag the harness uses. Override-friendly so CI can publish
# under a different name without editing the file.
TEST_IMAGE ?= claude-director-test

all: generate build

build:
	CGO_ENABLED=0 go build -o ./bin/claude-director ./cmd/claude-director

test:
	go test ./...

generate:
	go generate ./...

lint:
	go vet ./...

# Build the Docker test harness image. Always rebuilds the binary first so
# the image picks up the latest source.
test-image: build
	docker build \
		--build-arg CLAUDE_CODE_VERSION=$(CLAUDE_CODE_VERSION) \
		-t $(TEST_IMAGE) \
		-f test/Dockerfile \
		.

# Image smoke. Confirms the build succeeds, the pinned Claude version is the
# one we expect, claude-director help exits 0 from inside the container, and
# the driver script returns a clear failure for an unknown EPIC.
test-image-smoke: test-image
	@echo "[smoke] claude --version inside the image"
	docker run --rm $(TEST_IMAGE) claude --version | grep -F '$(CLAUDE_CODE_VERSION)' \
		|| (echo "ERROR: pinned claude version $(CLAUDE_CODE_VERSION) not reported"; exit 1)
	@echo "[smoke] claude-director help inside the image"
	docker run --rm $(TEST_IMAGE) claude-director help | jq -e '.verbs | length > 0' >/dev/null
	@echo "[smoke] driver rejects unknown EPIC"
	@if docker run --rm -e EPIC=nonexistent $(TEST_IMAGE) /opt/driver/run-testplan.sh 2>&1 | grep -q 'no such testplan'; then \
		echo "[smoke] OK"; \
	else \
		echo "ERROR: driver did not reject unknown EPIC with the expected message"; \
		exit 1; \
	fi

# test-docker (canonical command form for every functional Epic's gate) is
# added in Task 3 of Epic 2 once the harness-smoke testplan exists.
