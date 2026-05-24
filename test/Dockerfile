FROM debian:bookworm-slim

# Pinned Claude Code version. Per SRD §15.2 the empirical behaviors the
# harness leans on (env-var auth, settings merge, hook surface) were validated
# against this version, so bumping it requires re-running the notes under
# reference/*-research.md before merging. See docs/architecture.md.
ARG CLAUDE_CODE_VERSION=2.1.120

# Go toolchain version. Used by the epic-13-release testplan, which
# runs `make release-binaries` inside the container to verify the
# cross-compile pipeline produces the four supported artifacts. Bump
# in lockstep with the project's go.mod `go` directive.
#
# Bump policy for GO_VERSION + GO_SHA256_AMD64 / _ARM64:
#   1. Pick the desired version on https://go.dev/dl/
#   2. Update GO_VERSION here AND `go` directive in go.mod
#   3. Fetch the per-arch SHA256s from
#      https://go.dev/dl/?mode=json&include=all (filter by version +
#      "linux-amd64.tar.gz" / "linux-arm64.tar.gz")
#   4. Replace GO_SHA256_AMD64 + GO_SHA256_ARM64 below
#   5. Rebuild the image; the in-container sha256sum check verifies
#      the tarball before extraction, so a stale SHA fails loudly.
ARG GO_VERSION=1.22.10
ARG GO_SHA256_AMD64=736ce492a19d756a92719a6121226087ccd91b652ed5caec40ad6dbfb2252092
ARG GO_SHA256_ARM64=5213c5e32fde3bd7da65516467b7ffbfe40d2bb5a5f58105e387eef450583eec

# Bun runtime version. Used by the ts-bun-lib testplan and any future Epic
# that exercises the TypeScript/Bun client library inside the container.
# Pinned for reproducibility; bump requires re-running ts-bun-lib testplan.
ARG BUN_VERSION=1.3.13

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        binutils \
        build-essential \
        ca-certificates \
        curl \
        file \
        git \
        gnupg \
        jq \
        make \
        sqlite3 \
        tmux \
        unzip \
 && curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
 && apt-get install -y --no-install-recommends nodejs \
 && mkdir -p -m 755 /etc/apt/keyrings \
 && curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
        | tee /etc/apt/keyrings/githubcli-archive-keyring.gpg > /dev/null \
 && chmod go+r /etc/apt/keyrings/githubcli-archive-keyring.gpg \
 && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
        > /etc/apt/sources.list.d/github-cli.list \
 && apt-get update \
 && apt-get install -y --no-install-recommends gh \
 && rm -rf /var/lib/apt/lists/* \
 && npm install -g "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
 && curl -fsSL https://bun.sh/install | BUN_INSTALL=/opt/bun bash -s "bun-v${BUN_VERSION}" \
 && ln -s /opt/bun/bin/bun /usr/local/bin/bun \
 && bun --version \
 && arch=$(dpkg --print-architecture) \
 && case "$arch" in \
        amd64) go_sha256="${GO_SHA256_AMD64}" ;; \
        arm64) go_sha256="${GO_SHA256_ARM64}" ;; \
        *) echo "ERROR: no pinned GO_SHA256 for arch=$arch" >&2; exit 1 ;; \
    esac \
 && curl -fsSL -o /tmp/go.tgz "https://go.dev/dl/go${GO_VERSION}.linux-${arch}.tar.gz" \
 && echo "${go_sha256}  /tmp/go.tgz" | sha256sum -c - \
 && tar -C /usr/local -xzf /tmp/go.tgz \
 && rm /tmp/go.tgz \
 && ln -s /usr/local/go/bin/go /usr/local/bin/go \
 && useradd --create-home --shell /bin/bash --uid 1000 tester

# Pre-built binary + driver assets are produced by the host Makefile.
COPY bin/agent-director /usr/local/bin/agent-director
COPY test/driver /opt/driver
# The install skill ships alongside the binary so the epic-12-install
# testplan can exercise it without a separate mount. Path mirrors the
# repo layout (`skills/install-agent-director/`) so test scripts can
# reference it portably.
COPY skills /opt/skills
RUN chmod 0755 /usr/local/bin/agent-director \
 && chmod -R 0755 /opt/driver \
 && chown -R tester:tester /opt/driver \
 && chmod -R 0755 /opt/skills \
 && chown -R tester:tester /opt/skills

USER tester
ENV HOME=/home/tester \
    PATH=/usr/local/bin:/usr/bin:/bin
WORKDIR /home/tester

CMD ["/opt/driver/run-testplan.sh"]
