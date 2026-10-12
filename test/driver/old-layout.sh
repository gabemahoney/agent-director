#!/usr/bin/env bash
# old-layout.sh — build an install of the PREVIOUS release in a given HOME, in
# its own layout (~/.agent-director), for the Docker cases that upgrade from it
# (epic-12-install: "upgrade from the old layout", "way back"; b.ssz).
#
# The previous release (/opt/previous-release, baked at image build; see
# test/Dockerfile for the tag, the sha256 pins and how to bump them) is
# installed with ITS OWN install.sh, the way an operator on that release did:
# --binary and --admin-binary on the baked assets, --register-mcp, hooks on,
# and the default PATH link in ~/.local/bin. Then, with that release's own
# verbs, it adds one non-live Claude row and two templates, and it sets
# disable_askuserquestion = true under [defaults] in config.toml.
#
# Usage:
#   /opt/driver/old-layout.sh <home> [--store-outside <dir>]
#
#   <home>              absolute path of an existing directory (the case's
#                       mktemp HOME); it must hold no ~/.agent-director yet.
#   --store-outside D   before the install, write ~/.agent-director/config.toml
#                       with `[store] db_path = "D/state.db"`, so the store
#                       lives outside the directory (D is created). The
#                       old install then merges its hook flag into that file.
#
# It sets HOME=<home> itself, puts ~/.local/bin (made if absent) first on its
# PATH so the default PATH link is made, and works from <home> as the
# working directory. Claude Code registers a local-scope MCP server per
# working directory: the case must run `claude mcp get agent-director` and
# the new install.sh from <home> too, to see and re-point that registration.
# The caller's own HOME and PATH are not used or changed.
#
# What it builds, under <home> (call it H):
#   H/.agent-director/bin/agent-director, admin/agent-director-admin
#                                  the previous release's binaries
#   H/.agent-director/config.toml  mode 0600: [defaults] holds
#                                  inject_help_hook = true and
#                                  disable_askuserquestion = true
#   H/.agent-director/state.db     (or D/state.db) at the previous release's
#                                  schema: ONE row, id old-layout-1, state
#                                  `ended` (never live, so the move is not
#                                  refused as agents-live)
#   H/.agent-director/templates/worker.toml    claude_args holds two entries
#   H/.agent-director/templates/reviewer.toml  [extra_env] and [labels] hold
#                                  keys that TOML quotes (they have a dot)
#   H/.local/bin/agent-director    symlink to the old canonical binary
#   H/.claude/settings.json        the previous release's help hooks
#   H/.claude.json                 the folder trust its spawn wrote, plus the
#                                  MCP registration (see below)
#
# The non-live row: the previous release's `kill` ends the agent but never
# changes the row, and `find-missing` marks a pending row only after a 60 s
# grace, which would add a minute to every case. So the row is made with
# `spawn` (stand-in claude, /opt/driver/stand-in-claude) and `kill`, and its
# state is then set to `ended` through /opt/driver/sql.sh: the one raw write.
#
# MCP: the previous release's install.sh registers with
# `claude mcp add agent-director <path> serve --stdio`, which real Claude Code
# may refuse (b.fom). The helper checks the registration with
# `claude mcp get`, and if it is not there registers by hand, in the same
# scope and from the same directory, with
# `claude mcp add agent-director -- <old path> serve --stdio`. `mcp=` below
# says which one it was. With neither working it prints `mcp=none` and goes on.
#
# Output: key=value lines on STDOUT, one per line (the install's own output and
# every message go to stderr). Redirect stdout to a file rather than a command
# substitution, as the helper starts a tmux server:
#   home=<H>            dir=<H>/.agent-director
#   bin=<old canonical binary>   admin=<old admin binary>
#   db=<store path>              instance_id=old-layout-1
#   link=<H>/.local/bin/agent-director
#   mcp=install|helper|none
#   templates=<H>/.agent-director/templates
# A later case reads the store only through /opt/driver/sql.sh, and takes the
# previous release's expected user_version and schema from a reference
# database that release made, never from a constant.
#
# With --store-outside the previous release's install merges into the
# pre-seeded config.toml, and leaves it mode 0644 (its backup 0600); a plain
# setup's config.toml, created by that install, is 0600. Both are what that
# release does, not something the helper sets.
#
# Setups it supports (each is one call):
#   plain          old-layout.sh <H>
#   store outside  old-layout.sh <H> --store-outside <D>
# "Failed migration write" is a plain setup that the case then upgrades with
# a config.toml or template made unwritable (see the way-back case).
#
# Exit status: 0; 1 when a step fails (the message names it); 2 on bad usage.

set -euo pipefail

PREV=/opt/previous-release

die() {
    echo "old-layout.sh: $*" >&2
    exit 1
}

usage() {
    echo "usage: old-layout.sh <home> [--store-outside <dir>]" >&2
    exit 2
}

[[ $# -ge 1 ]] || usage
home="$1"
shift
outside=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --store-outside)
            [[ $# -ge 2 ]] || usage
            outside="$2"
            shift 2
            ;;
        *) usage ;;
    esac
done

[[ "$home" == /* ]] || { echo "old-layout.sh: home must be absolute: $home" >&2; exit 2; }
[[ -z "$outside" || "$outside" == /* ]] || { echo "old-layout.sh: --store-outside must be absolute: $outside" >&2; exit 2; }
[[ -d "$home" ]] || die "no such directory: $home"
[[ ! -e "$home/.agent-director" ]] || die "$home/.agent-director already exists"
for f in install.sh agent-director agent-director-admin; do
    [[ -x "$PREV/$f" ]] || die "$PREV/$f missing: the image holds no previous release"
done

dir="$home/.agent-director"
bin="$dir/bin/agent-director"
admin="$dir/admin/agent-director-admin"
cfg="$dir/config.toml"
iid=old-layout-1

export HOME="$home"
mkdir -p "$home/.local/bin"
export PATH="$home/.local/bin:/usr/local/bin:/usr/bin:/bin"
cd "$home"

db="$dir/state.db"
if [[ -n "$outside" ]]; then
    mkdir -p "$outside" "$dir"
    db="$outside/state.db"
    printf '[store]\ndb_path = "%s"\n' "$db" >"$cfg"
    chmod 0600 "$cfg"
fi

"$PREV/install.sh" --binary "$PREV/agent-director" --admin-binary "$PREV/agent-director-admin" \
    --register-mcp >&2 || die "the previous release's install.sh failed"
[[ -x "$bin" && -x "$admin" ]] || die "the install left no binaries under $dir"
[[ "$(readlink "$home/.local/bin/agent-director")" == "$bin" ]] || die "no PATH link to $bin"

# The MCP registration: by the install, or by hand, or none.
mcp=install
got="$(claude mcp get agent-director 2>/dev/null || true)"
if ! grep -qF -- "$bin" <<<"$got"; then
    mcp=helper
    claude mcp remove agent-director >&2 2>&1 || true
    if claude mcp add agent-director -- "$bin" serve --stdio >&2 2>&1; then
        got="$(claude mcp get agent-director 2>/dev/null || true)"
        grep -qF -- "$bin" <<<"$got" || mcp=none
    else
        mcp=none
    fi
fi
echo "old-layout.sh: mcp=$mcp" >&2

# A pending row, then its agent ended, then the row set to ended.
spawn_out="$(mktemp)"
PATH="/opt/driver/stand-in-claude:$PATH" "$bin" spawn --claude-instance-id "$iid" --cwd /tmp \
    >"$spawn_out" || { cat "$spawn_out" >&2; die "spawn failed"; }
grep -qF "$iid" "$spawn_out" || { cat "$spawn_out" >&2; die "spawn did not report $iid"; }
"$bin" kill --claude-instance-id "$iid" >"$spawn_out" || { cat "$spawn_out" >&2; die "kill failed"; }
rm -f "$spawn_out"
/opt/driver/sql.sh "$db" "UPDATE spawns SET state = 'ended', ended_at = CURRENT_TIMESTAMP WHERE claude_instance_id = '$iid';"
[[ "$(/opt/driver/sql.sh -readonly "$db" "SELECT state FROM spawns WHERE claude_instance_id = '$iid';")" == ended ]] \
    || die "the row did not become ended"

# Templates, with the previous release's make-template.
"$bin" make-template --name worker --claude-args=--model --claude-args=sonnet >/dev/null \
    || die "make-template worker failed"
"$bin" make-template --name reviewer --extra-env my.key=v --label a.b=c >/dev/null \
    || die "make-template reviewer failed"

# disable_askuserquestion = true under [defaults].
[[ -f "$cfg" ]] || die "the install left no $cfg"
grep -qx '\[defaults\]' "$cfg" || die "$cfg has no [defaults] table"
tmp="$(mktemp)"
awk '{ print } $0 == "[defaults]" { print "disable_askuserquestion = true" }' "$cfg" >"$tmp"
cat "$tmp" >"$cfg"
rm -f "$tmp"

printf 'home=%s\n' "$home"
printf 'dir=%s\n' "$dir"
printf 'bin=%s\n' "$bin"
printf 'admin=%s\n' "$admin"
printf 'db=%s\n' "$db"
printf 'instance_id=%s\n' "$iid"
printf 'link=%s\n' "$home/.local/bin/agent-director"
printf 'mcp=%s\n' "$mcp"
printf 'templates=%s\n' "$dir/templates"
