# The measure-exit measurement image (Epic 21, t3.h98.w4.yu.gm). Built FROM
# the Docker test image (test/Dockerfile, tag agent-director-test, which
# carries the v-next agent-director, tmux, jq and procps), with Claude Code
# reinstalled at CLAUDE_CODE_VERSION under its own tag. The test image's own
# pin (2.1.120, which ignores exec-form hook args) stays as it is for the
# Docker test plans. L1 and L2 measure 2.1.280 by default, the deployed
# version (the driver's real-mode floor), and the version probe builds one
# image per probed version.
#
# The driver compiles in the build stage, so nothing runs Go on the host
# beyond the test image's existing `make build`. The dry-run stub is NOT in
# this image: here `claude` is only ever the real Claude Code.
#
#   docker build --build-arg CLAUDE_CODE_VERSION=2.1.280 \
#       -t agent-director-measure:cc-2.1.280 -f tools/measure-exit/Dockerfile .
#
# The host runner (run.sh) starts it; see that script for mounts and
# credentials.

ARG BASE_IMAGE=agent-director-test

FROM ${BASE_IMAGE} AS build
USER root
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal ./internal
COPY pkg ./pkg
COPY tools/measure-exit ./tools/measure-exit
RUN CGO_ENABLED=0 go build -trimpath -o /out/measure-exit ./tools/measure-exit

FROM ${BASE_IMAGE}
ARG CLAUDE_CODE_VERSION=2.1.280
# Claude Code 2.1.280 and 2.1.285 both declare node >=22.0.0 (npm registry
# engines field); the test image carries Node 20 (its own pin, for its
# 2.1.120), so the measurement image moves to Node 22.
ARG NODE_MAJOR=22
# REQUIRE_NATIVE_CLAUDE=1 (the default; L1 and L2's image) fails the build
# unless `claude` on PATH is the native binary: the package's postinstall
# copies the platform binary over bin/claude.exe, and when it fails the
# small placeholder stays, a launcher that starts the native binary as its
# child. The pane process would then be the launcher, every hook's parent
# the child, and every hook an ignored pid_mismatch, so no row would ever
# report in. The version probe's builds (L0) pass 0: older versions in its
# bisection ship a JavaScript CLI, and the probe reads only whether a hook
# got its args. Either way the finding is in
# /opt/measure-exit/claude-launcher.txt ("native" or "not native: ...").
ARG REQUIRE_NATIVE_CLAUDE=1
USER root
# Claude Code at the measured version; ~/.claude and /etc/claude-code exist
# before any read-only file is mounted into them, so the agent's home stays
# writable by the agent (transcripts) and the mounts create no root-owned
# directory.
RUN curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | bash - \
 && apt-get install -y --no-install-recommends nodejs \
 && rm -rf /var/lib/apt/lists/* \
 && node --version \
 && npm install -g "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
 && claude_bin="$(readlink -f "$(command -v claude)")" \
 && claude_exe="$(readlink -f "$(npm root -g)/@anthropic-ai/claude-code/bin/claude.exe" || true)" \
 && launcher="not native: claude resolves to $claude_bin" \
 && if [ "$claude_bin" = "$claude_exe" ] \
        && [ "$(od -An -N4 -tx1 "$claude_bin" | tr -d ' \n')" = 7f454c46 ] \
        && [ "$(stat -c %s "$claude_bin")" -gt 1048576 ]; then \
        launcher=native; \
    fi \
 && mkdir -p /opt/measure-exit \
 && printf '%s\n' "$launcher" > /opt/measure-exit/claude-launcher.txt \
 && echo "claude launcher: $launcher" \
 && if [ "$REQUIRE_NATIVE_CLAUDE" = 1 ] && [ "$launcher" != native ]; then \
        echo "ERROR: Claude Code ${CLAUDE_CODE_VERSION}: bin/claude.exe is not the native binary ($launcher; did the package's postinstall fail?). Every hook would be ignored as pid_mismatch; refusing to build the measurement image." >&2; \
        exit 1; \
    fi \
 && claude --version \
 && mkdir -p /home/tester/.claude /etc/claude-code /opt/measure-exit/layers \
 && chown tester:tester /home/tester/.claude \
 && test ! -e /home/tester/.agent-director
COPY --from=build /out/measure-exit /usr/local/bin/measure-exit
RUN chmod 0755 /usr/local/bin/measure-exit
LABEL org.agent-director.measure-exit.claude-code-version="${CLAUDE_CODE_VERSION}"
USER tester
ENV HOME=/home/tester \
    PATH=/usr/local/bin:/usr/bin:/bin
WORKDIR /home/tester
CMD ["measure-exit"]
