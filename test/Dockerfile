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
ARG GO_VERSION=1.22.10

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        git \
        gnupg \
        jq \
        make \
        sqlite3 \
        tmux \
 && curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
 && apt-get install -y --no-install-recommends nodejs \
 && rm -rf /var/lib/apt/lists/* \
 && npm install -g "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
 && arch=$(dpkg --print-architecture) \
 && curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${arch}.tar.gz" \
      | tar -C /usr/local -xz \
 && ln -s /usr/local/go/bin/go /usr/local/bin/go \
 && useradd --create-home --shell /bin/bash --uid 1000 tester

# Pre-built binary + driver assets are produced by the host Makefile.
COPY bin/claude-director /usr/local/bin/claude-director
COPY test/driver /opt/driver
# The install skill ships alongside the binary so the epic-12-install
# testplan can exercise it without a separate mount. Path mirrors the
# repo layout (`skills/install-claude-director/`) so test scripts can
# reference it portably.
COPY skills /opt/skills
RUN chmod 0755 /usr/local/bin/claude-director \
 && chmod -R 0755 /opt/driver \
 && chown -R tester:tester /opt/driver \
 && chmod -R 0755 /opt/skills \
 && chown -R tester:tester /opt/skills

USER tester
ENV HOME=/home/tester \
    PATH=/usr/local/bin:/usr/bin:/bin
WORKDIR /home/tester

CMD ["/opt/driver/run-testplan.sh"]
