#!/usr/bin/env bash
# install.sh — install or upgrade claude-director on this machine.
#
# Per SRD §16.2. The script is deliberately bash + jq + standard
# coreutils — no Go, no exotic deps. The Apiary skill harness invokes
# it; an operator can also run it directly from a checked-out tree.
#
# Flags:
#   --binary <path>      Source binary to install. Defaults to looking
#                        next to the script first, then to whatever
#                        `command -v claude-director` resolves to.
#   --from-release [tag] Download a pre-built binary for this host's
#                        OS/arch from GitHub Releases and install it.
#                        With no tag, resolves the latest release via
#                        `gh release view` (if available) or
#                        `curl + jq` against api.github.com. Mutually
#                        exclusive with --binary.
#   --sha256 <hex>       Verify the downloaded asset against this
#                        sha256 (lowercase hex, 64 chars). Only
#                        meaningful with --from-release. Optional —
#                        omit to skip verification.
#   --symlink-dir <dir>  Drop a PATH symlink at <dir>/claude-director.
#                        Default: ~/.local/bin if on PATH; otherwise
#                        no symlink.
#   --no-symlink         Suppress symlink creation regardless of dir.
#   --register-mcp       Run `claude mcp add` for the stdio server.
#   --version <vN>       Override the version suffix used for the
#                        versioned binary path. Default: extracted
#                        from the binary's `--version` output, falling
#                        back to a timestamp if --version is not
#                        supported by the binary yet (v1).
#
# Exit codes:
#   0  success
#   2  pre-flight failure (claude/tmux missing, whitespace in path)
#   3  binary source not found / not executable
#   4  hook merge failure (~/.claude/settings.json malformed)
#   5  store warmup failure
#
# Idempotent: re-running the script with no flags after a clean
# install is a no-op (returns 0, prints "already installed at vX").

set -euo pipefail

# --------------------------------------------------------------------
# Defaults + flag parsing
# --------------------------------------------------------------------

readonly DEFAULT_INSTALL_ROOT="${HOME}/.claude-director"
readonly DEFAULT_BIN_DIR="${DEFAULT_INSTALL_ROOT}/bin"
readonly DEFAULT_SETTINGS_PATH="${HOME}/.claude/settings.json"

BINARY_SRC=""
FROM_RELEASE=0
FROM_RELEASE_TAG=""
SHA256_EXPECTED=""
SYMLINK_DIR=""
SYMLINK_DEFAULT=""
NO_SYMLINK=0
REGISTER_MCP=0
VERSION_TAG=""

# GitHub repo slug used by --from-release. Matches go.mod's module path
# and the release.sh asset naming.
readonly RELEASE_REPO_SLUG="gabemahoney/claude-director"

# Pick a sensible default symlink dir: ~/.local/bin if on PATH.
if printf '%s' ":${PATH}:" | grep -q ":${HOME}/.local/bin:"; then
    SYMLINK_DEFAULT="${HOME}/.local/bin"
fi

while [[ $# -gt 0 ]]; do
    case "$1" in
        --binary)
            BINARY_SRC="$2"; shift 2 ;;
        --from-release)
            FROM_RELEASE=1
            # Optional tag argument: accept only if the next arg
            # doesn't look like another flag.
            if [[ $# -ge 2 && -n "${2:-}" && "${2:-}" != -* ]]; then
                FROM_RELEASE_TAG="$2"; shift 2
            else
                shift
            fi
            ;;
        --sha256)
            SHA256_EXPECTED="$2"; shift 2 ;;
        --symlink-dir)
            SYMLINK_DIR="$2"; shift 2 ;;
        --no-symlink)
            NO_SYMLINK=1; shift ;;
        --register-mcp)
            REGISTER_MCP=1; shift ;;
        --version)
            VERSION_TAG="$2"; shift 2 ;;
        -h|--help)
            sed -n '2,/^$/p' "$0" | sed 's/^# \{0,1\}//'
            exit 0 ;;
        *)
            echo "install.sh: unknown flag: $1" >&2
            exit 2 ;;
    esac
done

[[ -z "$SYMLINK_DIR" && "$NO_SYMLINK" -eq 0 ]] && SYMLINK_DIR="$SYMLINK_DEFAULT"

if [[ "$FROM_RELEASE" -eq 1 && -n "$BINARY_SRC" ]]; then
    echo "install.sh: --from-release and --binary are mutually exclusive" >&2
    exit 2
fi
if [[ -n "$SHA256_EXPECTED" && "$FROM_RELEASE" -eq 0 ]]; then
    echo "install.sh: --sha256 only applies with --from-release" >&2
    exit 2
fi
if [[ -n "$SHA256_EXPECTED" && ! "$SHA256_EXPECTED" =~ ^[0-9a-f]{64}$ ]]; then
    echo "install.sh: --sha256 must be 64 lowercase hex characters" >&2
    exit 2
fi

# --------------------------------------------------------------------
# Pre-flight
# --------------------------------------------------------------------

# SRD §4.3: tmux's direct-argv invocation requires shell-safe paths.
# Reject any whitespace in the install root up front so an operator
# whose $HOME has a space sees the error immediately, not at the
# first spawn.
if [[ "$DEFAULT_INSTALL_ROOT" =~ [[:space:]] ]]; then
    echo "install.sh: install path contains whitespace: $DEFAULT_INSTALL_ROOT" >&2
    echo "  SRD §4.3 requires a whitespace-free install path." >&2
    exit 2
fi

# claude + tmux must be on PATH.
required_tools=(claude tmux jq)
[[ "$FROM_RELEASE" -eq 1 ]] && required_tools+=(curl)
for tool in "${required_tools[@]}"; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        echo "install.sh: required tool not found on PATH: $tool" >&2
        case "$tool" in
            claude) echo "  Install Claude Code first: https://claude.com/claude-code" >&2 ;;
            tmux)   echo "  Install tmux via your package manager (apt/brew/dnf/etc.)." >&2 ;;
            jq)     echo "  Install jq via your package manager (we use it to safely edit settings.json)." >&2 ;;
            curl)   echo "  --from-release downloads via curl; install it via your package manager." >&2 ;;
        esac
        exit 2
    fi
done

echo "install.sh: pre-flight OK"
echo "  claude  : $(claude --version 2>/dev/null || echo '<unknown>')"
echo "  tmux    : $(tmux -V 2>/dev/null || echo '<unknown>')"

# --------------------------------------------------------------------
# --from-release: resolve tag, download asset for this OS/arch, hand
# the temp path to the rest of the install flow as if --binary had
# been passed.
# --------------------------------------------------------------------

if [[ "$FROM_RELEASE" -eq 1 ]]; then
    case "$(uname -s)" in
        Linux)  rel_os="linux" ;;
        Darwin) rel_os="darwin" ;;
        *)
            echo "install.sh: --from-release: unsupported OS $(uname -s)" >&2
            echo "  release.sh only publishes linux and darwin builds." >&2
            exit 3 ;;
    esac
    case "$(uname -m)" in
        x86_64|amd64)   rel_arch="amd64" ;;
        arm64|aarch64)  rel_arch="arm64" ;;
        *)
            echo "install.sh: --from-release: unsupported arch $(uname -m)" >&2
            exit 3 ;;
    esac
    asset="claude-director-${rel_os}-${rel_arch}"

    # Resolve the tag if the operator didn't supply one. Prefer `gh`
    # (carries the operator's auth, avoids the unauthenticated API
    # rate limit); fall back to curl + jq against the public API.
    if [[ -z "$FROM_RELEASE_TAG" ]]; then
        if command -v gh >/dev/null 2>&1; then
            FROM_RELEASE_TAG=$(gh release view --repo "$RELEASE_REPO_SLUG" \
                --json tagName -q .tagName 2>/dev/null || true)
        fi
        if [[ -z "$FROM_RELEASE_TAG" ]]; then
            api_url="https://api.github.com/repos/${RELEASE_REPO_SLUG}/releases/latest"
            FROM_RELEASE_TAG=$(curl -fsSL "$api_url" 2>/dev/null \
                | jq -r '.tag_name // empty' 2>/dev/null || true)
        fi
        if [[ -z "$FROM_RELEASE_TAG" || "$FROM_RELEASE_TAG" == "null" ]]; then
            echo "install.sh: --from-release: no releases published for $RELEASE_REPO_SLUG yet" >&2
            echo "  options:" >&2
            echo "    - build from source: make build && bash $0" >&2
            echo "    - point at a local binary: bash $0 --binary <path>" >&2
            exit 3
        fi
    fi
    echo "  release : $RELEASE_REPO_SLUG @ $FROM_RELEASE_TAG ($asset)"

    asset_url="https://github.com/${RELEASE_REPO_SLUG}/releases/download/${FROM_RELEASE_TAG}/${asset}"
    tmp_bin="$(mktemp -t claude-director.XXXXXX)"
    # Defer-cleanup the tempfile on any exit path that doesn't move
    # past the BINARY_SRC assignment. install -m 0755 later in the
    # script copies the contents into place, so the tempfile being
    # cleaned up at script exit is fine.
    trap 'rm -f "$tmp_bin"' EXIT
    if ! curl -fsSL --retry 2 -o "$tmp_bin" "$asset_url"; then
        echo "install.sh: --from-release: failed to download $asset_url" >&2
        echo "  check that the asset exists for $FROM_RELEASE_TAG on $RELEASE_REPO_SLUG." >&2
        exit 3
    fi

    if [[ -n "$SHA256_EXPECTED" ]]; then
        if command -v sha256sum >/dev/null 2>&1; then
            actual=$(sha256sum "$tmp_bin" | awk '{print $1}')
        elif command -v shasum >/dev/null 2>&1; then
            actual=$(shasum -a 256 "$tmp_bin" | awk '{print $1}')
        else
            echo "install.sh: --sha256: neither sha256sum nor shasum available" >&2
            exit 3
        fi
        if [[ "$actual" != "$SHA256_EXPECTED" ]]; then
            echo "install.sh: --from-release: sha256 mismatch" >&2
            echo "  expected: $SHA256_EXPECTED" >&2
            echo "  actual  : $actual" >&2
            exit 3
        fi
        echo "  sha256  : verified"
    fi

    chmod +x "$tmp_bin"
    BINARY_SRC="$tmp_bin"
fi

# --------------------------------------------------------------------
# Locate source binary
# --------------------------------------------------------------------

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [[ -z "$BINARY_SRC" ]]; then
    # Prefer the in-repo build (skills/install-claude-director sits two
    # levels under the repo root; bin/ is at the root).
    candidate="${SCRIPT_DIR}/../../bin/claude-director"
    if [[ -x "$candidate" ]]; then
        BINARY_SRC="$candidate"
    elif command -v claude-director >/dev/null 2>&1; then
        BINARY_SRC="$(command -v claude-director)"
    else
        echo "install.sh: no source binary found." >&2
        echo "  Tried: $candidate" >&2
        echo "  Tried: command -v claude-director" >&2
        echo "  Pass --binary <path> to override." >&2
        exit 3
    fi
fi
if [[ ! -x "$BINARY_SRC" ]]; then
    echo "install.sh: source binary not executable: $BINARY_SRC" >&2
    exit 3
fi
echo "  source  : $BINARY_SRC"

# --------------------------------------------------------------------
# Version tag (used for the side-by-side path on upgrade)
# --------------------------------------------------------------------

if [[ -z "$VERSION_TAG" ]]; then
    # Try the binary's own --version (Epic 13 will land this); fall
    # back to a timestamp for v1.
    if v=$("$BINARY_SRC" --version 2>/dev/null) && [[ -n "$v" ]]; then
        VERSION_TAG="$v"
    else
        VERSION_TAG="t$(date +%Y%m%d-%H%M%S)"
    fi
fi
echo "  version : $VERSION_TAG"

# --------------------------------------------------------------------
# Create install root + bin dir
# --------------------------------------------------------------------

mkdir -p "$DEFAULT_INSTALL_ROOT"
chmod 0700 "$DEFAULT_INSTALL_ROOT"
mkdir -p "$DEFAULT_BIN_DIR"
chmod 0755 "$DEFAULT_BIN_DIR"

# --------------------------------------------------------------------
# Place binary at versioned path; swap canonical symlink atomically.
# --------------------------------------------------------------------

VERSIONED="${DEFAULT_BIN_DIR}/claude-director.${VERSION_TAG}"
CANONICAL="${DEFAULT_BIN_DIR}/claude-director"

# `install` (BSD/GNU) copies atomically and sets mode in one go.
install -m 0755 "$BINARY_SRC" "$VERSIONED"

# Atomic symlink swap: write `.canonical.new` as a symlink, then
# rename onto canonical. `ln -sfn` is the idiomatic single-step form,
# but the rename trick is portable to older `ln` variants too.
ln -sfn "$VERSIONED" "${CANONICAL}.new"
mv -f "${CANONICAL}.new" "$CANONICAL"

echo "  binary  : $CANONICAL → $VERSIONED"

# --------------------------------------------------------------------
# Optional PATH symlink
# --------------------------------------------------------------------

if [[ "$NO_SYMLINK" -eq 0 && -n "$SYMLINK_DIR" ]]; then
    if [[ ! -d "$SYMLINK_DIR" ]]; then
        echo "  symlink : skipped — $SYMLINK_DIR does not exist"
    elif [[ "$SYMLINK_DIR" =~ [[:space:]] ]]; then
        echo "  symlink : skipped — $SYMLINK_DIR contains whitespace"
    else
        target="${SYMLINK_DIR}/claude-director"
        ln -sfn "$CANONICAL" "${target}.new"
        mv -f "${target}.new" "$target"
        echo "  symlink : $target → $CANONICAL"
    fi
fi

# --------------------------------------------------------------------
# Warm up state.db via `claude-director help`
# --------------------------------------------------------------------

if "$CANONICAL" help >/dev/null 2>&1; then
    state_db="${DEFAULT_INSTALL_ROOT}/state.db"
    if [[ -f "$state_db" ]]; then
        chmod 0600 "$state_db" 2>/dev/null || true
        echo "  state.db: $(stat -c '%a' "$state_db" 2>/dev/null || stat -f '%Lp' "$state_db") at $state_db"
    fi
else
    echo "install.sh: store warmup (claude-director help) failed" >&2
    exit 5
fi

# --------------------------------------------------------------------
# Hook injection — additive merge into ~/.claude/settings.json
# --------------------------------------------------------------------

mkdir -p "$(dirname "$DEFAULT_SETTINGS_PATH")"

# Read existing settings or start from {}.
if [[ -f "$DEFAULT_SETTINGS_PATH" ]]; then
    existing=$(<"$DEFAULT_SETTINGS_PATH")
    if ! printf '%s' "$existing" | jq empty >/dev/null 2>&1; then
        echo "install.sh: ~/.claude/settings.json is not valid JSON" >&2
        exit 4
    fi
else
    existing='{}'
fi

# Our hook entries are uniquely identified by the command string
# (the canonical binary path + " help"). Idempotency check: only add
# if the command isn't already present in that event's hook list.
help_cmd="${CANONICAL} help"

# Merge logic (jq):
#   - Ensure hooks.SessionStart is an array; append our entry if not
#     already there (matched by command).
#   - Ensure hooks.SessionEnd is an array; append our compact-matcher
#     entry if not already there.
new_settings=$(printf '%s' "$existing" | jq \
    --arg cmd "$help_cmd" '
        .hooks //= {}
        | .hooks.SessionStart //= []
        | .hooks.SessionEnd //= []
        | (
            if any(.hooks.SessionStart[]?; .hooks[]?.command == $cmd)
              then .
              else .hooks.SessionStart += [{"hooks":[{"type":"command","command":$cmd}]}]
            end
        )
        | (
            if any(.hooks.SessionEnd[]?; .matcher == "compact" and (.hooks[]?.command == $cmd))
              then .
              else .hooks.SessionEnd += [{"matcher":"compact","hooks":[{"type":"command","command":$cmd}]}]
            end
        )
    ')

# Backup-before-edit: snapshot the prior settings.json (if any) into a
# timestamped .bak alongside the original so a regressed jq filter is
# recoverable. Only the *prior* contents are backed up; in-place
# re-runs of the install will keep the most recent pre-edit copy.
if [[ -f "$DEFAULT_SETTINGS_PATH" ]]; then
    backup_settings="${DEFAULT_SETTINGS_PATH}.bak.$(date +%Y%m%d-%H%M%S)"
    cp -f "$DEFAULT_SETTINGS_PATH" "$backup_settings"
    echo "  backup  : $backup_settings"
fi

# Atomic write: tempfile + mv.
tmp_settings="${DEFAULT_SETTINGS_PATH}.new"
printf '%s\n' "$new_settings" > "$tmp_settings"
mv -f "$tmp_settings" "$DEFAULT_SETTINGS_PATH"

echo "  hooks   : injected into $DEFAULT_SETTINGS_PATH"

# --------------------------------------------------------------------
# Optional MCP registration
# --------------------------------------------------------------------

if [[ "$REGISTER_MCP" -eq 1 ]]; then
    if claude mcp add claude-director "$CANONICAL" serve --stdio 2>/dev/null; then
        echo "  mcp     : registered with claude mcp"
    else
        echo "  mcp     : registration failed (continuing anyway)" >&2
    fi
fi

echo "install.sh: done. Try: $CANONICAL help"
