# Sandbox execution image (b.nh2) — the container boundary that makes it
# safe to run the agent-director test suite (and ANY built artifact) without
# risking the developer's real ~/.agent-director.
#
# b.8dr incident: `bun test` on a schema-bumping branch opened and silently
# auto-migrated the LIVE ~/.agent-director/state.db (v2→v3), breaking every
# consumer of the installed binary. Host-side $HOME redirects don't hold —
# internal/store.expandTilde resolves the home via user.Current() (/etc/passwd),
# and the store chmods the DB 0600 on every open. A container whose HOME has
# NO .agent-director is the only boundary that holds. See the "Sandboxed
# execution" section of docs/engineering-guide.md.
#
# Distinct from test/Dockerfile (the Claude-Code testplan-driver harness):
# this image carries only the toolchain the go+bun suite needs and bakes NO
# host paths, so it is reusable by CI (GitHub Actions) unchanged. Run it via
# the Makefile's `make test-sandbox` / `make sandbox-shell` / `make sandbox`
# targets, which supply the pod-required flags and mounts.
#
# Runs as a non-root `sandbox` user (uid 1000). The Makefile invokes podman
# with --userns=keep-id, which maps this container uid directly to the invoking
# host user (also uid 1000), so the bind-mounted host caches/worktree are
# writable. Running non-root (rather than as container-root, which keep-id
# would also map to the host user) is deliberate: tests that assert a
# filesystem permission is DENIED (e.g. internal/trail's read-only-dir case)
# only behave correctly for an unprivileged process — root bypasses DAC checks.
# sandbox's HOME (/home/sandbox) contains no .agent-director, so the isolation
# invariant holds; the go smoke canary resolves that home via user.Current()
# (/etc/passwd) and stays green as long as nothing writes there.

FROM debian:bookworm-slim

# Go toolchain version. Pinned to match the go.mod `go` directive (go 1.22);
# 1.22.10 is the patch release the sibling test/Dockerfile also pins, so the
# in-tree SHA256s below are already vetted.
#
# Bump policy for GO_VERSION + GO_SHA256_AMD64 / _ARM64:
#   1. Pick the desired version on https://go.dev/dl/ (keep in lockstep with
#      the `go` directive in go.mod).
#   2. Update GO_VERSION here.
#   3. Fetch the per-arch SHA256s from
#      https://go.dev/dl/?mode=json&include=all (filter by version +
#      "linux-amd64.tar.gz" / "linux-arm64.tar.gz").
#   4. Replace GO_SHA256_AMD64 + GO_SHA256_ARM64 below.
#   5. Rebuild the image; the in-container sha256sum check verifies the
#      tarball before extraction, so a stale SHA fails loudly.
ARG GO_VERSION=1.22.10
ARG GO_SHA256_AMD64=736ce492a19d756a92719a6121226087ccd91b652ed5caec40ad6dbfb2252092
ARG GO_SHA256_ARM64=5213c5e32fde3bd7da65516467b7ffbfe40d2bb5a5f58105e387eef450583eec

# Bun runtime version. Pinned to match the sibling test/Dockerfile; the bun
# installer script is SHA-verified below before it is executed.
ARG BUN_VERSION=1.3.13
# SHA256 of https://bun.sh/install (the version-agnostic installer shell
# script — it downloads whichever BUN_VERSION is passed as its argument).
# Pinning it closes the "curl | bash" supply-chain hole: the script is
# fetched, verified, then run from a local file. The installer is versioned
# independently of BUN_VERSION, so a mismatch means the upstream installer
# changed — re-fetch, re-review, and update this pin (the build fails loudly
# on mismatch, so a stale pin is caught rather than silently trusted).
ARG BUN_INSTALL_SHA256=04882bf41679d49d9af108657a1e5515bf04fdf2940d12c0d0b1e5d79dc53be8

# Node.js version. Required even though the client runtime is bun: the TS
# README-snippet test typechecks via `tsc`, whose shebang is `/usr/bin/env
# node`. Installed from the official nodejs.org tarball, SHA-pinned per arch —
# same verify-then-extract pattern as the Go toolchain above (no curl|bash).
#
# Bump policy (mirror the Go block): pick a version on https://nodejs.org/dist/,
# update NODE_VERSION, then take the linux-x64 / linux-arm64 `.tar.xz` hashes
# from https://nodejs.org/dist/v<version>/SHASUMS256.txt into the two vars.
# The in-container sha256sum check verifies the tarball before extraction, so a
# stale SHA fails loudly.
ARG NODE_VERSION=20.19.5
ARG NODE_SHA256_AMD64=315046739a513a70e03a4a55a8afda8cf979f30852e576075c340084e3f8ac0f
ARG NODE_SHA256_ARM64=d462267863ae8ee556039ebdf559055a8ec562c633889ef1403f3adb449ba1dd

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        build-essential \
        ca-certificates \
        curl \
        git \
        jq \
        make \
        procps \
        sqlite3 \
        tmux \
        unzip \
        xz-utils \
 && rm -rf /var/lib/apt/lists/* \
 # ── Node.js (SHA-pinned tarball per arch; no curl|bash) ──────────────────
 && arch=$(dpkg --print-architecture) \
 && case "$arch" in \
        amd64) node_arch="x64";   node_sha256="${NODE_SHA256_AMD64}" ;; \
        arm64) node_arch="arm64"; node_sha256="${NODE_SHA256_ARM64}" ;; \
        *) echo "ERROR: no pinned NODE_SHA256 for arch=$arch" >&2; exit 1 ;; \
    esac \
 && curl -fsSL -o /tmp/node.tar.xz "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${node_arch}.tar.xz" \
 && echo "${node_sha256}  /tmp/node.tar.xz" | sha256sum -c - \
 && tar -C /usr/local --strip-components=1 -xJf /tmp/node.tar.xz \
 && rm /tmp/node.tar.xz \
 && node --version \
 # ── Go toolchain (SHA-pinned per arch; reuses $arch from above) ──────────
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
 && go version \
 # ── Bun runtime (installer SHA-verified before execution) ───────────────
 && curl -fsSL -o /tmp/bun-install.sh https://bun.sh/install \
 && echo "${BUN_INSTALL_SHA256}  /tmp/bun-install.sh" | sha256sum -c - \
 && BUN_INSTALL=/opt/bun bash /tmp/bun-install.sh "bun-v${BUN_VERSION}" \
 && rm /tmp/bun-install.sh \
 && ln -s /opt/bun/bin/bun /usr/local/bin/bun \
 && bun --version \
 # Non-root user with a real /etc/passwd home that has NO .agent-director.
 # uid 1000 matches the host user the podman leg maps in (--userns=keep-id).
 && useradd --create-home --shell /bin/bash --uid 1000 sandbox \
 # Pre-create the HOME cache dirs and make the HOME tree group-writable by
 # group 0 (root group). The docker leg runs the container as an arbitrary host
 # uid that is not 1000, so it cannot write this uid-1000-owned HOME by user
 # perms; the Makefile pairs its `--user` with `--group-add 0`, and these gid-0
 # write bits then let that process populate GOPATH/GOCACHE/bun cache. (The
 # podman leg maps to uid 1000 via keep-id and owns HOME outright, so it does
 # not rely on this.) Engine-agnostic: no host or engine specifics are baked in;
 # the bind-mounted caches are host-owned at runtime.
 && mkdir -p /home/sandbox/go /home/sandbox/.cache/go-build /home/sandbox/.bun/install/cache \
 && chgrp -R 0 /home/sandbox \
 && chmod -R g=u /home/sandbox

USER sandbox
# HOME=/home/sandbox has no .agent-director (see header). The Go/bun cache env
# vars point at the mount targets the Makefile supplies so a warm host cache
# makes the suite fast; the module cache is mounted read-write.
ENV HOME=/home/sandbox \
    PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin \
    GOPATH=/home/sandbox/go \
    GOMODCACHE=/go/pkg/mod \
    GOCACHE=/home/sandbox/.cache/go-build \
    BUN_INSTALL_CACHE_DIR=/home/sandbox/.bun/install/cache
WORKDIR /work

CMD ["/bin/bash"]
