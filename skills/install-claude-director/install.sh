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
#   --no-hooks           Skip the ~/.claude/settings.json hook
#                        injection step entirely. settings.json is
#                        left byte-identical (no .bak backup, no
#                        edit). Default OFF — defaulting to skip
#                        would defeat install.sh's main value over a
#                        bare binary copy.
#   --keep-prior         Before overwriting an existing binary,
#                        snapshot it to <target>.prior (overwriting
#                        any previous .prior). Roll back with
#                        `mv <target>.prior <target>`. Default OFF.
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
NO_HOOKS=0
KEEP_PRIOR=0

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
        --no-hooks)
            NO_HOOKS=1; shift ;;
        --keep-prior)
            KEEP_PRIOR=1; shift ;;
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

# Defaults to 1; cleared to 0 only when BINARY_SRC came from PATH
# (option (c)), since "whatever's on PATH" makes no claim about the
# operator's source tree.
VERSION_CHECK_REQUIRED=1

if [[ -z "$BINARY_SRC" ]]; then
    # Prefer the in-repo build (skills/install-claude-director sits two
    # levels under the repo root; bin/ is at the root).
    candidate="${SCRIPT_DIR}/../../bin/claude-director"
    if [[ -x "$candidate" ]]; then
        BINARY_SRC="$candidate"
    elif command -v claude-director >/dev/null 2>&1; then
        BINARY_SRC="$(command -v claude-director)"
        VERSION_CHECK_REQUIRED=0
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
# Source-tree version check
#
# When the operator points install.sh at a local binary (either via
# --binary or via the in-repo ./bin/claude-director fallback) AND
# install.sh itself lives inside a git checkout, refuse to install a
# binary whose embedded commit doesn't match the checkout's HEAD.
# Catches the "operator forgot to `make build` after pulling new
# code" footgun — installing a stale artifact silently is exactly
# what b.qag flagged.
#
# Skipped when:
#   - --from-release was used (the asset is by construction not the
#     operator's source tree)
#   - install.sh is not inside a git checkout (curled tarball case)
#   - BINARY_SRC came from `command -v` (option (c)): there's no
#     promise it was built from this tree, and the user explicitly
#     asked for "whatever's on PATH"
# --------------------------------------------------------------------

if [[ "$FROM_RELEASE" -eq 0 && "${VERSION_CHECK_REQUIRED:-1}" -eq 1 ]]; then
    # Find the script's repo root (walking up from SCRIPT_DIR). The
    # script-path is what tells us "this checkout"; CWD might be
    # somewhere unrelated.
    repo_root=""
    probe="$SCRIPT_DIR"
    while [[ "$probe" != "/" && -n "$probe" ]]; do
        if [[ -d "$probe/.git" ]]; then
            repo_root="$probe"; break
        fi
        probe="$(dirname "$probe")"
    done

    if [[ -n "$repo_root" ]] && head_sha=$(git -C "$repo_root" rev-parse HEAD 2>/dev/null); then
        # Run the binary's `version` verb. An older binary without the
        # verb will exit non-zero / emit an err_name envelope; jq -e
        # returns non-zero if .commit is absent or null. Either way we
        # land in the mismatch path with bin_commit empty.
        bin_commit=$("$BINARY_SRC" version 2>/dev/null \
            | jq -er '.commit // empty' 2>/dev/null \
            || true)

        if [[ -z "$bin_commit" || "$bin_commit" == "unknown" || "$bin_commit" != "$head_sha" ]]; then
            echo "install.sh: source-tree version check failed." >&2
            echo "  binary  : $BINARY_SRC" >&2
            if [[ -z "$bin_commit" ]]; then
                echo "  built from: <no version stamp — binary is older than this verb, or built without ldflags>" >&2
            elif [[ "$bin_commit" == "unknown" ]]; then
                echo "  built from: <unstamped — likely a plain 'go build' without -ldflags>" >&2
            else
                echo "  built from: $bin_commit" >&2
            fi
            echo "  HEAD    : $head_sha ($repo_root)" >&2
            echo "" >&2
            echo "  The binary at $BINARY_SRC was not built from this checkout's" >&2
            echo "  current HEAD. Installing it would silently substitute stale code" >&2
            echo "  for the source you're sitting on. Either:" >&2
            echo "    - rebuild it first:    make build" >&2
            echo "    - or download release: rerun with --from-release (omit --binary)" >&2
            exit 3
        fi
        echo "  version-check: binary commit matches HEAD ($head_sha)"
    fi
fi

# --------------------------------------------------------------------
# Create install root + bin dir
# --------------------------------------------------------------------

mkdir -p "$DEFAULT_INSTALL_ROOT"
chmod 0700 "$DEFAULT_INSTALL_ROOT"
mkdir -p "$DEFAULT_BIN_DIR"
chmod 0755 "$DEFAULT_BIN_DIR"

# --------------------------------------------------------------------
# Atomic install: write to a sibling temp path, then mv over the target.
#
# `mv` within the same filesystem is atomic at the inode level —
# concurrent readers see either the old binary or the new, never half.
# A running process holds the old inode reference, so an in-flight
# exec is unaffected by the swap.
#
# This is the standard pattern for single-binary CLI installers
# (gh, kubectl, terraform). The version-manager pattern (canonical
# symlink → versioned files) is only worth the complexity when you
# actually manage multiple concurrent versions; we don't.
# --------------------------------------------------------------------

CANONICAL="${DEFAULT_BIN_DIR}/claude-director"
PRIOR="${CANONICAL}.prior"
TMP="${CANONICAL}.tmp.$$"

if [[ "$KEEP_PRIOR" -eq 1 && -f "$CANONICAL" ]]; then
    cp -f "$CANONICAL" "$PRIOR"
    chmod 0755 "$PRIOR"
    echo "  prior   : snapshotted to $PRIOR"
fi

cp "$BINARY_SRC" "$TMP"
chmod 0755 "$TMP"
mv "$TMP" "$CANONICAL"

echo "  binary  : $CANONICAL"

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
#
# Skipped entirely under --no-hooks: settings.json is not read, not
# backed up, not written. There's no edit, so there's nothing to back
# up — leaving settings.json byte-identical to its pre-install state.
# --------------------------------------------------------------------

if [[ "$NO_HOOKS" -eq 1 ]]; then
    echo "  hooks   : skipped (--no-hooks)"
else
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
fi

# --------------------------------------------------------------------
# inject_help_hook config flag — opt-in dynamic per-Spawn help hook.
#
# Driven by the same Q4 (inject persistent help hooks?) signal: when
# the operator picked "yes" (i.e. did NOT pass --no-hooks),
# claude-director should also tag its own Spawns with a help hook
# regardless of the Spawn's CLAUDE_CONFIG_DIR. install.sh sets the
# flag here; the binary reads it at spawn-synth time.
#
# Q4=no (--no-hooks) leaves config.toml untouched — the flag stays at
# its zero-value default of false.
# --------------------------------------------------------------------

if [[ "$NO_HOOKS" -eq 0 ]]; then
    CONFIG_TOML="${DEFAULT_INSTALL_ROOT}/config.toml"
    if [[ -f "$CONFIG_TOML" ]]; then
        backup_cfg="${CONFIG_TOML}.bak.$(date +%Y%m%d-%H%M%S)"
        cp -f "$CONFIG_TOML" "$backup_cfg"
        # awk merge: rewrite an existing inject_help_hook line under
        # [defaults] to =true; if [defaults] exists but lacks the key,
        # append it inside the section; if no [defaults] section exists
        # at all, add one at end of file. Preserves every other key
        # and section verbatim.
        merged=$(awk '
            BEGIN { written = 0; in_defaults = 0 }
            /^\[/ {
                if (in_defaults && !written) {
                    print "inject_help_hook = true"
                    written = 1
                }
                in_defaults = ($0 ~ /^\[defaults\][[:space:]]*$/) ? 1 : 0
                print
                next
            }
            in_defaults && /^[[:space:]]*inject_help_hook[[:space:]]*=/ {
                print "inject_help_hook = true"
                written = 1
                next
            }
            { print }
            END {
                if (in_defaults && !written) {
                    print "inject_help_hook = true"
                    written = 1
                }
                if (!written) {
                    print ""
                    print "[defaults]"
                    print "inject_help_hook = true"
                }
            }
        ' "$CONFIG_TOML")
        tmp_cfg="${CONFIG_TOML}.new"
        printf '%s\n' "$merged" > "$tmp_cfg"
        mv -f "$tmp_cfg" "$CONFIG_TOML"
        echo "  config  : merged inject_help_hook=true into $CONFIG_TOML (backup $backup_cfg)"
    else
        printf '[defaults]\ninject_help_hook = true\n' > "$CONFIG_TOML"
        chmod 0600 "$CONFIG_TOML"
        echo "  config  : created $CONFIG_TOML with inject_help_hook=true"
    fi
fi

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
