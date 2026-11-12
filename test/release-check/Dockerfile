# The Release Check Kit's image (SRD SR-8.1, SR-8.3, SR-8.4): installs one
# exact linux-arm64 agent-director pair with its own install.sh at build
# time, then checks it at run time with no network. Build it from the kit
# directory prepare.sh makes, natively on a linux/arm64 engine; check.sh's
# header defines the kit layout, release.env and the output lines.
#
#   docker build --pull --progress=plain -f test/release-check/Dockerfile -t <tag> .
#   docker run --rm --network none --memory 8g <tag>
#
# Nothing from the host is mounted, copied or read but the kit directory: no
# HOME, config, credentials or Docker socket.
FROM node:24-bookworm

# Debian 12 on arm64 only (SRD SR-5.2). The tag is not digest-pinned, so if it
# ever moves to another Debian major, the base is not Debian, or the engine
# picked another architecture, stop here before anything is installed rather
# than silently check another platform. /etc/os-release is read as data,
# never sourced; test/Dockerfile runs the same Debian check.
RUN os_id=; os_version_id=; \
    if [ -r /etc/os-release ]; then \
        while IFS='=' read -r key value || [ -n "$key" ]; do \
            value=${value#[\"\']}; value=${value%[\"\']}; \
            case "$key" in \
                ID) os_id=$value ;; \
                VERSION_ID) os_version_id=$value ;; \
            esac; \
        done < /etc/os-release; \
        found="ID=${os_id:-<unset>} VERSION_ID=${os_version_id:-<unset>}"; \
    else \
        found="no readable /etc/os-release"; \
    fi; \
    if [ "$os_id" != debian ] || [ "$os_version_id" != 12 ]; then \
        echo "ERROR: base image is $found; this image requires Debian 12 (ID=debian VERSION_ID=12)" >&2; \
        exit 1; \
    fi; \
    dpkg_arch=$(dpkg --print-architecture 2>/dev/null || echo '<unknown>'); \
    machine=$(uname -m 2>/dev/null || echo '<unknown>'); \
    if [ "$dpkg_arch" != arm64 ] || [ "$machine" != aarch64 ]; then \
        echo "ERROR: base image is dpkg architecture $dpkg_arch, uname -m $machine; this image requires arm64/aarch64 on a native linux/arm64 engine" >&2; \
        exit 1; \
    fi

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8

# install.sh's prerequisites (docs/install.md "Requirements") the base may
# lack, from Debian 12's packages, and tini to reap orphaned pane processes.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        file \
        jq \
        procps \
        sqlite3 \
        tini \
        tmux \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --create-home --shell /bin/bash checker

# The kit stays root-owned, so the checking user cannot change the bytes
# check.sh verifies.
COPY . /opt/release-check
RUN chmod -R a+rX,go-w /opt/release-check

USER checker
# The stand-ins come first, so install.sh and every pane find them as claude
# and codex; ~/.local/bin is where install.sh links the installed
# agent-director.
ENV HOME=/home/checker \
    PATH=/opt/release-check/test/driver/stand-in-claude:/opt/release-check/test/driver/stand-in-codex:/home/checker/.local/bin:/usr/local/bin:/usr/bin:/bin
WORKDIR /home/checker
RUN mkdir -p /home/checker/.local/bin \
 && /opt/release-check/test/release-check/check.sh install /opt/release-check

CMD ["/usr/bin/tini", "--", "/opt/release-check/test/release-check/check.sh", "run", "/opt/release-check"]
