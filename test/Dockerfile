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

# The previous release, baked in whole under /opt/previous-release for the
# epic-12-install cases that upgrade from, and go back to, the old layout
# (the directory ~/.agent-director, before AD moved to ~/.config/agent-director;
# b.ssz). It holds the release's own install.sh, fetched from its tag, and
# both release assets for the image's architecture, the file names the
# release skill publishes (agent-director-linux-<arch>,
# agent-director-admin-linux-<arch>). None of it is on PATH, so no other
# slug sees it. test/driver/old-layout.sh is the one helper that uses it.
#
# Bump policy for PREV_RELEASE_TAG and the five sha256 pins below: v0.11.1 is
# the last release that keeps its files in ~/.agent-director, and the cases
# model that layout, so do not bump the tag unless the new previous release
# still does. To change it:
#   1. Set PREV_RELEASE_TAG.
#   2. PREV_INSTALL_SH_SHA256: `git show <tag>:skills/install-agent-director/install.sh | sha256sum`.
#   3. The per-arch asset pins: `gh release view <tag> --repo gabemahoney/agent-director
#      --json assets -q '.assets[]|[.name,.digest]|@tsv'` (the digest is the sha256).
#   4. Rebuild the image; the in-container sha256sum checks fail loudly on a
#      stale pin. Then re-run epic-12-install: the old-layout helper's
#      assumptions (verbs, flags, files) come from this release.
ARG PREV_RELEASE_TAG=v0.11.1
ARG PREV_INSTALL_SH_SHA256=c9fd83e3660c9b3cb0f4fd92c841d7cc227bd8ec008f667a338c6619f539b873
ARG PREV_SHA256_AMD64=5a39cbaf3be9ef11e616032a21b291ecc0a41b1e83891f23e8be09c5df458a20
ARG PREV_SHA256_ARM64=e8280209940ef26f348ce7befeca84314c602119ec5510c87850753b2117fdba
ARG PREV_ADMIN_SHA256_AMD64=b315a7657338f9fe89ede1a6fee6dff832e4d8351f46a6af9814596138ec829c
ARG PREV_ADMIN_SHA256_ARM64=100155f1006f5f4b03e6b59844c831f94fa0fe384b461152543095c9d4a8d369

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
        procps \
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

# The previous release (see PREV_RELEASE_TAG above): its install.sh and both
# assets, each checked against its pin before it is kept. Its own files, so a
# case runs the release exactly as an operator on it would.
RUN arch=$(dpkg --print-architecture) \
 && case "$arch" in \
        amd64) prev_sha="${PREV_SHA256_AMD64}"; prev_admin_sha="${PREV_ADMIN_SHA256_AMD64}" ;; \
        arm64) prev_sha="${PREV_SHA256_ARM64}"; prev_admin_sha="${PREV_ADMIN_SHA256_ARM64}" ;; \
        *) echo "ERROR: no pinned PREV_SHA256 for arch=$arch" >&2; exit 1 ;; \
    esac \
 && base="https://github.com/gabemahoney/agent-director/releases/download/${PREV_RELEASE_TAG}" \
 && mkdir -p /opt/previous-release \
 && curl -fsSL -o /opt/previous-release/install.sh \
        "https://raw.githubusercontent.com/gabemahoney/agent-director/${PREV_RELEASE_TAG}/skills/install-agent-director/install.sh" \
 && echo "${PREV_INSTALL_SH_SHA256}  /opt/previous-release/install.sh" | sha256sum -c - \
 && curl -fsSL -o /opt/previous-release/agent-director "${base}/agent-director-linux-${arch}" \
 && echo "${prev_sha}  /opt/previous-release/agent-director" | sha256sum -c - \
 && curl -fsSL -o /opt/previous-release/agent-director-admin "${base}/agent-director-admin-linux-${arch}" \
 && echo "${prev_admin_sha}  /opt/previous-release/agent-director-admin" | sha256sum -c - \
 && printf '%s\n' "${PREV_RELEASE_TAG}" > /opt/previous-release/TAG \
 && chmod 0755 /opt/previous-release /opt/previous-release/install.sh /opt/previous-release/agent-director /opt/previous-release/agent-director-admin \
 && chmod 0644 /opt/previous-release/TAG

# Pre-built binary + driver assets are produced by the host Makefile.
COPY bin/agent-director /usr/local/bin/agent-director
# The operator tool agent-director-admin (b.vqr), from the same `make build`
# so its version stamp matches, kept off PATH as install.sh keeps it. At
# /opt/bin, install.sh's in-repo fallback (<script dir>/../../bin) finds it
# for the bundled /opt/skills copy, as /work/source/bin holds it for the
# source mount's; test plans run it by this full path.
COPY bin/agent-director-admin /opt/bin/agent-director-admin
COPY test/driver /opt/driver
# The install skill ships alongside the binary so the epic-12-install
# testplan can exercise it without a separate mount. Path mirrors the
# repo layout (`skills/install-agent-director/`) so test scripts can
# reference it portably.
COPY skills /opt/skills
RUN chmod 0755 /usr/local/bin/agent-director /opt/bin/agent-director-admin \
 && chmod -R 0755 /opt/driver \
 && chown -R tester:tester /opt/driver \
 && chmod -R 0755 /opt/skills \
 && chown -R tester:tester /opt/skills

USER tester
# AGENT_DIRECTOR_TEST_HARNESS=1 marks this container as the Docker test
# harness. /opt/driver/db-reset.sh, which ends every tmux server the user runs
# and deletes ~/.config/agent-director/state.db, refuses to run without it, so it
# cannot do that on a host (b.8yq). Like the sandbox's
# AGENT_DIRECTOR_TEST_SANDBOX it is an accident-prevention gate, not a
# security boundary.
ENV HOME=/home/tester \
    PATH=/usr/local/bin:/usr/bin:/bin \
    AGENT_DIRECTOR_TEST_HARNESS=1
WORKDIR /home/tester

CMD ["/opt/driver/run-testplan.sh"]
