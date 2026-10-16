#!/usr/bin/env bash
# uninstall.sh — reverse an agent-director install.
#
# Per SRD §16.2. By default:
#   - Remove the two help hook entries from ~/.claude/settings.json
#     (preserving any other user hooks), and any entry whose hook
#     command starts with the old directory's bin/agent-director path
#     (~/.agent-director/bin/agent-director: the help hooks an install
#     before the move to ~/.config/agent-director wrote).
#   - Remove the schedule `install.sh --schedule` added (b.wur): the
#     crontab lines tagged `# agent-director-schedule` (every other line of
#     the crontab kept; the crontab itself removed only when nothing else was
#     in it), and the systemd user units agent-director-find-missing and
#     agent-director-expire (.service and .timer, in the user manager's unit
#     directory: its XDG_CONFIG_HOME's systemd/user, else its HOME's
#     .config/systemd/user, as `systemctl --user show-environment` tells; this
#     shell's ~/.config/systemd/user or $XDG_CONFIG_HOME/systemd/user when the
#     manager does not say), their timers disabled first. A unit
#     file is removed only when it carries that marker. Nothing else is
#     touched, and with none present it is a no-op. Failing to read or edit
#     the crontab (anything but "no crontab for <user>" or busybox's "No such file or directory") is reported on
#     stderr, with the lines to remove by hand, and does not change the exit
#     status. Only the job lines are counted in the "removed N" message; the
#     schedule's marked header comment goes with them. It also removes the copy of the
#     schedule files install.sh keeps in ~/.config/agent-director/schedule/
#     when it ran piped to bash and fetched them (its five files, then the
#     directory, which is left, with a note, when it holds other files).
#     Test-only switches (not in --help):
#     INSTALL_SH_TEST_CRONTAB and INSTALL_SH_TEST_SYSTEMCTL name the program
#     run in place of `crontab` (`-l`, `-`, `-r`) and `systemctl`.
#   - Remove the canonical binary and any `.prior` rollback snapshot
#     under ~/.config/agent-director/bin/.
#   - Remove the operator tool ~/.config/agent-director/admin/agent-director-admin,
#     any `.prior` rollback snapshot of it, and its ~/.config/agent-director/admin/
#     directory (b.vqr).
#   - Remove the PATH symlink (if found at any of the standard
#     locations or at --symlink-dir).
#   - Leave ~/.config/agent-director/ intact (the operator may want to
#     keep their templates / state.db history).
#
# It works on ~/.config/agent-director. When only ~/.agent-director is
# present (an install never moved to the new directory), it works on
# that directory instead, the same way. A ~/.agent-director that is a
# link to ~/.config/agent-director (a compatibility link) is no old
# directory.
#
# Without --purge it names, and leaves, every other AD directory it
# finds: each ~/.agent-director.pre-move-* (the old directory the move
# kept), each ~/.config/agent-director.after-upgrade-* and
# ~/.agent-director.recreated-* (left by the way back in
# operator-actions.md), a ~/.agent-director present beside the new
# directory, and each *.aside-* directory (set aside to settle a
# conflict). Delete the kept, after-upgrade and recreated ones once the
# upgrade is confirmed.
#
# Flags:
#   --purge              Also rm -rf ~/.config/agent-director (templates +
#                        state.db) and every other AD directory it finds:
#                        each ~/.agent-director.pre-move-*, each
#                        ~/.config/agent-director.after-upgrade-* and
#                        ~/.agent-director.recreated-*, a ~/.agent-director
#                        beside the new directory, and a compatibility link
#                        ~/.agent-director. A link is removed as a link,
#                        never what it points to. Every *.aside-*
#                        directory is left (it may hold data an operator
#                        did not choose), and named. Requires --force or
#                        an interactive confirmation, which names each
#                        directory, asked before anything is removed or
#                        changed.
#   --force              Skip the --purge confirmation prompt.
#   --mcp-also           Also run `claude mcp remove agent-director`
#                        through the Claude Code program install.sh runs:
#                        [launch] claude_path in the config.toml of the
#                        directory this run works on, when it names an
#                        executable file, else `claude` from PATH. A
#                        [launch] line it cannot read, or a claude_path
#                        naming no executable file, gets a note naming it
#                        and the run uses claude from PATH; it never stops
#                        or fails the run. With no program the step is
#                        skipped, with a line saying so, and the exit
#                        status is unchanged. A failure (other than "No MCP server named ...",
#                        which means there is nothing to remove) is reported
#                        with the command run, its exit status and its
#                        stderr; every other step still runs, and the
#                        script then exits 6.
#   --symlink-dir <dir>  Look for the PATH symlink at <dir>; default
#                        is ~/.local/bin.
#
# HOME must be set to the user's home directory, an absolute path: every
# path above is built from it. With HOME unset, empty or relative the script
# stops first, before anything is read or written, with exit 2 (the same
# class as a bad flag).

set -euo pipefail

# HOME first, before anything is read or written (see the header).
case "${HOME-}" in
    /*) ;;
    *)
        echo "uninstall.sh: HOME is unset, empty or not an absolute path (HOME='${HOME-}'). Run uninstall.sh with HOME set to the user's home directory, for example HOME=/home/alice." >&2
        exit 2
        ;;
esac

# AD's directory, and the one an earlier release kept its files in (SR-11.7).
# DEFAULT_INSTALL_ROOT, below the helper functions, is the one this run works
# on: the new directory, or the old one when only that is present.
readonly NEW_INSTALL_ROOT="${HOME}/.config/agent-director"
readonly OLD_INSTALL_ROOT="${HOME}/.agent-director"
readonly DEFAULT_SETTINGS_PATH="${HOME}/.claude/settings.json"
# The program every Claude Code command here runs.
CLAUDE_PROG=claude

PURGE=0
FORCE=0
MCP_ALSO=0
MCP_REMOVE_FAILED=0
SYMLINK_DIR="${HOME}/.local/bin"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --purge)       PURGE=1; shift ;;
        --force)       FORCE=1; shift ;;
        --mcp-also)    MCP_ALSO=1; shift ;;
        --symlink-dir) SYMLINK_DIR="$2"; shift 2 ;;
        -h|--help)
            sed -n '2,/^$/p' "$0" | sed 's/^# \{0,1\}//'
            exit 0 ;;
        *)
            echo "uninstall.sh: unknown flag: $1" >&2
            exit 2 ;;
    esac
done

# ad_mode_of, ad_resolve_link, ad_link_write_check, ad_replace_keeping_mode
# and ad_backup_keeping_mode are install.sh's functions of the same names
# (b.ojn, b.nw5); keep them in step.
#
# ad_mode_of <file> — print <file>'s permission bits in octal (stat -L: a
# symlink's target, not the link; -c is GNU stat, -f BSD stat).
ad_mode_of() {
    stat -L -c '%a' "$1" 2>/dev/null || stat -L -f '%Lp' "$1"
}

# ad_resolve_link <file> — print the path <file> resolves to through its
# chain of symlinks, or <file> itself when it is not a link: each relative
# target read against its own link's directory, by a loop over readlink
# (older macOS has no readlink -f). Fails after 40 links (a loop).
ad_resolve_link() {
    local path="$1" link hops=0
    while [[ -L "$path" ]]; do
        if (( ++hops > 40 )); then
            echo "uninstall.sh: $1: too many levels of symbolic links" >&2
            return 1
        fi
        link=$(readlink "$path") || return 1
        case "$link" in
            /*) path="$link" ;;
            *) path="$(dirname "$path")/$link" ;;
        esac
    done
    printf '%s\n' "$path"
}

# ad_link_write_check <file> — return 0 when <file> is not a symlink, or
# is one ad_replace_keeping_mode can write through: its links end, and
# the directory of the file they resolve to exists and can be written in.
# Otherwise set link_target to the file the links resolve to (empty when
# they loop) and link_why to one sentence saying why, and return 1.
ad_link_write_check() {
    local dir
    link_target="" link_why=""
    if [[ ! -L "$1" ]]; then
        return 0
    fi
    if ! link_target=$(ad_resolve_link "$1" 2>/dev/null); then
        link_target=""
        link_why="The links loop: following them never reaches a file (more than 40 links)."
        return 1
    fi
    dir=$(dirname "$link_target")
    if [[ ! -d "$dir" ]]; then
        link_why="There is no directory $dir to hold the target (a dotfiles repository not cloned yet, say)."
        return 1
    fi
    if [[ ! -w "$dir" || ! -x "$dir" ]]; then
        link_why="The target's directory, $dir, cannot be written in (a read-only file system, say, such as home-manager's /nix/store)."
        return 1
    fi
}

# ad_replace_keeping_mode <file> <text> — replace the existing <file> with
# <text> and a newline, keeping its permission bits: write <file>.new
# owner-only (umask 077), give it <file>'s mode, then mv it over <file>. A
# symlinked <file> is written through (b.nw5): the .new goes beside the
# file its link chain resolves to and replaces that file, so the link
# survives and its target gets the edit. A link this cannot write through
# was refused (ad_link_write_check) before any edit; ad_resolve_link's own
# loop failure stays as a backstop for a link changed since.
ad_replace_keeping_mode() {
    local target tmp
    target=$(ad_resolve_link "$1") || return
    tmp="${target}.new"
    rm -f "$tmp"
    (umask 077; printf '%s\n' "$2" >"$tmp")
    chmod "$(ad_mode_of "$target")" "$tmp"
    mv -f "$tmp" "$target"
}

# ad_backup_keeping_mode <file> <bak> — copy <file> to <bak> with <file>'s
# permission bits: remove any <bak> first, copy owner-only (umask 077),
# then chmod. cp reads through a symlinked <file>, so <bak> is a regular
# copy of its target, beside the link. Not cp -p, which also copies ACLs
# and xattrs and fails where the file system cannot take them (NFS homes;
# Ubuntu LP#2087769).
ad_backup_keeping_mode() {
    rm -f "$2"
    (umask 077; cp -f "$1" "$2")
    chmod "$(ad_mode_of "$1")" "$2"
}

# The config reader (b.2io, SR-11.4). uninstall.sh reads [launch] claude_path
# with it, to run `mcp remove` through the Claude Code program install.sh
# found. The block below is a verbatim copy of install.sh's block of the same
# name (it holds ad_clean_path as well, so uninstall.sh has no other copy):
# keep it in step with install.sh's, and change both together. uninstall.sh
# asks it for the "launch" lines only and never lets its refusal stop the
# run (see "Optional MCP deregistration" below). It is self-contained (bash
# 3.2 or later, builtins only), so a test can extract it by its marker lines,
# as it does install.sh's, and call ad_store_db_path alone.

# >>> ad_store_db_path (b.2io) >>>

# ad_clean_path <path> — print <path> cleaned as Go's filepath.Clean cleans
# it on Unix: repeated slashes and . elements go, each .. removes the
# element before it (and is dropped at the root), a trailing slash goes,
# and an empty result is ".".
ad_clean_path() {
    local rest="$1" out="" seg root=""
    if [[ "$rest" == /* ]]; then
        root=/
    fi
    while [[ -n "$rest" ]]; do
        seg="${rest%%/*}"
        if [[ "$seg" == "$rest" ]]; then rest=""; else rest="${rest#*/}"; fi
        case "$seg" in
            ""|.) ;;
            ..)
                if [[ -n "$out" && "$out" != .. && "$out" != */.. ]]; then
                    if [[ "$out" == */* ]]; then out="${out%/*}"; else out=""; fi
                elif [[ -z "$root" ]]; then
                    out="${out:+$out/}.."
                fi
                ;;
            *) out="${out:+$out/}$seg" ;;
        esac
    done
    out="${root}${out}"
    printf '%s\n' "${out:-.}"
}

# ad_sentinel_path <db> — print where the store looks for the migration
# sentinel that authorizes <db>: Go's filepath.Join(filepath.Dir(<db>),
# "migrate-authorized") (internal/store sentinelPath). Unlike dirname, both
# clean the directory, which matters for an absolute db_path that is not
# clean.
ad_sentinel_path() {
    if [[ "$1" == */* ]]; then
        ad_clean_path "${1%/*}/migrate-authorized"
    else
        printf '%s\n' migrate-authorized
    fi
}

# ad_toml_name <part> — print the name agent-director's TOML decoder matches
# for one key or table-name part, bare, "..." or '...' (re_part below): the
# part without its quotes, with each U+017F (long s) and U+212A (Kelvin sign)
# written as the s and k they match, since the decoder compares names with
# Go's strings.EqualFold. Called only inside ad_store_db_path's subshell.
ad_toml_name() {
    local name="$1" long_s=$'\xc5\xbf' kelvin=$'\xe2\x84\xaa'
    case "$name" in
        \"*\"|\'*\') name="${name:1:$((${#name} - 2))}" ;;
    esac
    name="${name//$long_s/s}"
    name="${name//$kelvin/k}"
    printf '%s\n' "$name"
}

# ad_db_path_refuse <config> <line number> <line> <reason>... — report a
# config file the reader cannot read, with the offending line (none when
# <line number> is 0) and <reason>s saying what to change, then exit 1.
# Called only inside ad_store_db_path's subshell, which sets want_log and
# about_launch (1 while the refusal is about a [launch] header, key or value,
# whose headline and closing note name [launch], not the store).
ad_db_path_refuse() {
    local config="$1" n="$2" line="$3" reason
    shift 3
    if [[ "${about_launch:-0}" -eq 1 ]]; then
        echo "install.sh: cannot tell which programs agent-director launches (the [launch] table); refusing to install." >&2
    elif [[ "${want_log:-0}" -eq 1 ]]; then
        echo "install.sh: cannot tell which store database or error log agent-director opens; refusing to install." >&2
    else
        echo "install.sh: cannot tell which store database agent-director opens; refusing to install." >&2
    fi
    echo "  config  : $config" >&2
    if [[ "$n" -gt 0 ]]; then
        printf '  line %s  : %s\n' "$n" "$line" >&2
    fi
    for reason in "$@"; do
        echo "  $reason" >&2
    done
    if [[ "$n" -gt 0 ]]; then
        if [[ "${about_launch:-0}" -eq 1 ]]; then
            echo "  install.sh reads [launch] claude_path, codex_path and cursor_path itself, to choose the" >&2
            echo "  Claude Code program it runs and to see which agents are installed, and" >&2
            echo "  reads only this form of the file: blank lines, # comments, [name]" >&2
            echo "  headers and name = value lines, a name being letters, digits, _ and -," >&2
            echo "  with claude_path, codex_path and cursor_path under [launch] set to a one-line \"...\"" >&2
            echo "  holding no backslash, or '...', and an optional # comment after it. A" >&2
            echo "  value is an absolute path or starts with ~/; an empty \"\" counts as unset." >&2
        elif [[ "${want_log:-0}" -eq 1 ]]; then
            echo "  install.sh reads [store] db_path and [log] error_log_path itself, to" >&2
            echo "  check, migrate and verify the database agent-director opens and to move" >&2
            echo "  the paths with it, and reads only this form of the" >&2
            echo "  file: blank lines, # comments, [name] headers and name = value lines," >&2
            echo "  a name being letters, digits, _ and -, with db_path under [store] and" >&2
            echo "  error_log_path under [log] set to a one-line \"...\" holding no" >&2
            echo "  backslash, or '...', and an optional # comment after it." >&2
        else
            echo "  install.sh reads [store] db_path itself, to check, migrate and verify" >&2
            echo "  the database agent-director opens, and reads only this form of the" >&2
            echo "  file: blank lines, # comments, [name] headers and name = value lines," >&2
            echo "  a name being letters, digits, _ and -, with db_path under [store] set" >&2
            echo "  to a one-line \"...\" holding no backslash, or '...', and an optional" >&2
            echo "  # comment after it." >&2
        fi
    fi
    echo "  Nothing was installed or changed. Re-run this install after the change." >&2
    exit 1
}

# ad_store_db_path <config> <home> <base> [log] [launch] — print the store
# database agent-director opens for config file <config>, home directory
# <home> (non-empty) and base directory <base> (the directory the config
# file's relative paths are joined onto; the caller says which directory that
# is, so the same reader serves a config file before and after it moves),
# resolved as above; a missing <config> gives the default. After <base>, the
# words "log" and "launch" (either, both, in either order) ask for more
# lines, printed after the store's in this order:
#   log     [log] error_log_path, resolved the same way: empty when the file
#           does not set it or sets it empty (there is then nothing to move
#           or check);
#   launch  [launch] claude_path, then codex_path and cursor_path, resolved as above: each
#           empty when unset or empty.
# Every line is printed, an empty one too, so a caller reads them by
# position (command substitution drops trailing empty lines: read from a
# here-string with `|| true` after each read). A '?' is refused in db_path
# only. On a file the reader cannot read, print why on stderr and return 1,
# printing nothing on stdout. The body is a subshell under the C locale, so
# the matching is bytewise whatever the caller's locale.
ad_store_db_path() (
    LC_ALL=C
    local config="$1" home="$2" base="$3" want_log=0 want_launch=0 n=0 line table=""
    local about_launch=0 seen_store=0 seen_log=0 seen_launch=0 have_value=0 have_log_value=0
    local have_claude=0 have_codex=0 have_cursor=0 claude_value="" codex_value="" cursor_value=""
    local value="" log_value="" key rest resolved name t k which parsed seen arg
    local -a tables=(store)
    shift 3
    for arg in "$@"; do
        case "$arg" in
            log) want_log=1; tables+=(log) ;;
            launch) want_launch=1; tables+=(launch) ;;
        esac
    done
    local re_skip='^[[:blank:]]*(#.*)?$'
    local re_header='^[[:blank:]]*\[[[:blank:]]*([A-Za-z0-9_-]+)[[:blank:]]*\][[:blank:]]*(#.*)?$'
    local re_bracket='^[[:blank:]]*\['
    local re_keyval='^[[:blank:]]*([A-Za-z0-9_-]+)[[:blank:]]*=[[:blank:]]*(.*)$'
    # A "..." value's body runs to the next unescaped ", so a backslash
    # before the first " after the opening one is an escape.
    local re_escape='^[[:blank:]]*"[^"]*\\'
    local re_basic='^[[:blank:]]*"([^"]*)"[[:blank:]]*(#.*)?$'
    local re_literal="^[[:blank:]]*'([^']*)'[[:blank:]]*(#.*)?\$"
    local re_store='^[Ss][Tt][Oo][Rr][Ee]$'
    local re_log='^[Ll][Oo][Gg]$'
    local re_db_path='^[Dd][Bb]_[Pp][Aa][Tt][Hh]$'
    local re_elp='^[Ee][Rr][Rr][Oo][Rr]_[Ll][Oo][Gg]_[Pp][Aa][Tt][Hh]$'
    local re_launch='^[Ll][Aa][Uu][Nn][Cc][Hh]$'
    local re_claude='^[Cc][Ll][Aa][Uu][Dd][Ee]_[Pp][Aa][Tt][Hh]$'
    local re_codex='^[Cc][Oo][Dd][Ee][Xx]_[Pp][Aa][Tt][Hh]$'
    local re_cursor='^[Cc][Uu][Rr][Ss][Oo][Rr]_[Pp][Aa][Tt][Hh]$'
    local re_cntrl='[[:cntrl:]]'
    # Only to word a refusal: the line forms the reader refuses, as TOML
    # writes them. A key or table-name part is a bare name, a "..." holding no
    # backslash, or a '...' (first part in BASH_REMATCH[1]).
    local re_part='([A-Za-z0-9_-]+|"[^"\\]*"|'"'[^']*'"')'
    local re_dot='[[:blank:]]*\.[[:blank:]]*'
    local re_end='[[:blank:]]*(#.*)?$'
    local re_bare='^[A-Za-z0-9_-]+$'
    local re_aot='^[[:blank:]]*\[\[[[:blank:]]*'"$re_part"'('"$re_dot$re_part"')*[[:blank:]]*\]\]'"$re_end"
    local re_aot_one='^[[:blank:]]*\[\[[[:blank:]]*'"$re_part"'[[:blank:]]*\]\]'"$re_end"
    local re_header_one='^[[:blank:]]*\[[[:blank:]]*'"$re_part"'[[:blank:]]*\]'"$re_end"
    local re_header_dotted='^[[:blank:]]*\[[[:blank:]]*'"$re_part"'('"$re_dot$re_part"')+[[:blank:]]*\]'"$re_end"
    local re_key_one='^[[:blank:]]*'"$re_part"'[[:blank:]]*='
    local re_key_two='^[[:blank:]]*'"$re_part$re_dot$re_part"'[[:blank:]]*='
    local re_key_dotted='^[[:blank:]]*'"$re_part"'('"$re_dot$re_part"')+[[:blank:]]*='
    # Ends the advice for a line before any header that belongs under one. The
    # line goes under its table's header, or under one added at the end of the
    # file: a header added in its place would take in every line after it, up
    # to the next header, and agent-director would then read those lines
    # differently (ignore a store.db_path = ... line under [defaults], say).
    local -a not_here=(
        "Add no header in this line's place: the lines below it, up to the next"
        "header, would fall under that header too."
    )

    # is_table <name> <table>: is <name> the table's name in some letter
    # case; key_of <table>: the key the reader reads under it.
    # The launch table holds three keys, so is_key takes any of them there,
    # and key_of names the one the (dynamically scoped) name holds, claude_path
    # as the default; key_of also takes claude, codex and cursor, the three
    # launch keys by their agent name.
    is_table() {
        case "$2" in
            store) [[ "$1" =~ $re_store ]] ;;
            log) [[ "$1" =~ $re_log ]] ;;
            launch) [[ "$1" =~ $re_launch ]] ;;
        esac
    }
    is_key() {
        case "$2" in
            store) [[ "$1" =~ $re_db_path ]] ;;
            log) [[ "$1" =~ $re_elp ]] ;;
            launch) [[ "$1" =~ $re_claude || "$1" =~ $re_codex || "$1" =~ $re_cursor ]] ;;
        esac
    }
    key_of() {
        case "$1" in
            store) k=db_path ;;
            log) k=error_log_path ;;
            claude) k=claude_path ;;
            codex) k=codex_path ;;
            cursor) k=cursor_path ;;
            launch)
                if [[ "${name:-}" =~ $re_cursor ]]; then k=cursor_path
                elif [[ "${name:-}" =~ $re_codex ]]; then k=codex_path
                else k=claude_path; fi
                ;;
        esac
    }
    # set_about <table>: the refusal that follows is about [launch] when <table>
    # is launch (its headline and closing note then name [launch]).
    set_about() {
        if [[ "$1" == launch || "$1" == claude || "$1" == codex || "$1" == cursor ]]; then
            about_launch=1
        else
            about_launch=0
        fi
    }
    # The advice for a table key before any header (store = { ... }), bare or
    # quoted.
    refuse_top() {
        local kdesc="$k" kset="set $k"
        set_about "$1"
        if [[ "$1" == launch ]]; then
            kdesc="claude_path, codex_path or cursor_path"
            kset="set it"
        fi
        local -a reasons=(
            "This sets $1 as a key (an inline table, say) rather than under a"
            "[$1] header. Remove this line. If it sets $kdesc, $kset under"
            "the file's [$1] header instead, adding that header at the end of the"
            "file if the file has none."
            "${not_here[@]}"
        )
        ad_db_path_refuse "$config" "$n" "$line" "${reasons[@]}"
    }

    if [[ -e "$config" ]]; then
        if [[ ! -f "$config" || ! -r "$config" ]]; then
            ad_db_path_refuse "$config" 0 "" \
                "It is not a readable file (a directory, say, or a file without read" \
                "permission), so agent-director cannot load it either. Make it one."
        fi
        while IFS= read -r line || [[ -n "$line" ]]; do
            n=$((n + 1))
            # The TOML decoder skips a UTF-8 byte-order mark and a CRLF's CR too.
            if [[ "$n" -eq 1 ]]; then
                line="${line#$'\xef\xbb\xbf'}"
            fi
            line="${line%$'\r'}"
            if [[ "$line" =~ $re_skip ]]; then
                continue
            fi
            # The decoder also skips a UTF-16 byte-order mark; the reader
            # does not.
            if [[ "$n" -eq 1 && ( "$line" == $'\xff\xfe'* || "$line" == $'\xfe\xff'* ) ]]; then
                ad_db_path_refuse "$config" "$n" "$line" \
                    "This line starts with the bytes FF FE or FE FF, a UTF-16 byte-order" \
                    "mark, which install.sh does not read. Remove those two bytes:" \
                    "agent-director skips them, so it reads the file the same without them."
            fi
            if [[ "$line" == *'"""'* || "$line" == *"'''"* ]]; then
                ad_db_path_refuse "$config" "$n" "$line" \
                    "This line holds \"\"\" or ''', which install.sh reads as the start of a" \
                    "multi-line string. Write each value on one line, as a \"...\" or '...'" \
                    "string, with no \"\"\" or ''' anywhere on the line."
            fi
            if [[ "$line" =~ $re_header ]]; then
                table="${BASH_REMATCH[1]}"
                for t in "${tables[@]}"; do
                    if is_table "$table" "$t"; then
                        set_about "$t"
                        if [[ "$table" != "$t" ]]; then
                            ad_db_path_refuse "$config" "$n" "$line" \
                                "agent-director reads this header as [$t]: its TOML decoder matches" \
                                "names regardless of letter case. Write it as [$t]."
                        fi
                        seen=$seen_store
                        if [[ "$t" == log ]]; then seen=$seen_log; fi
                        if [[ "$t" == launch ]]; then seen=$seen_launch; fi
                        if [[ "$seen" -eq 1 ]]; then
                            ad_db_path_refuse "$config" "$n" "$line" \
                                "This is a second [$t] header, and agent-director refuses a table" \
                                "defined twice. Move the lines under it, up to the next header, to" \
                                "under the first [$t] header, then remove this header."
                        fi
                        case "$t" in
                            log) seen_log=1 ;;
                            launch) seen_launch=1 ;;
                            *) seen_store=1 ;;
                        esac
                        about_launch=0
                    fi
                done
                continue
            fi
            # A refused header is never to be removed alone: the lines under it
            # would fall under the header before it, [store] perhaps.
            if [[ "$line" =~ $re_bracket ]]; then
                if [[ "$line" =~ $re_aot ]]; then
                    name="$(ad_toml_name "${BASH_REMATCH[1]}")"
                    if [[ "$line" =~ $re_aot_one ]]; then
                        for t in "${tables[@]}"; do
                            if is_table "$name" "$t"; then
                                set_about "$t"
                                ad_db_path_refuse "$config" "$n" "$line" \
                                    "This makes $t an array of tables ([[name]]), and agent-director" \
                                    "reads [$t] only as one table, so it refuses the file. Write it" \
                                    "as [$t]."
                            fi
                        done
                    fi
                    ad_db_path_refuse "$config" "$n" "$line" \
                        "This is an array of tables ([[name]]), which agent-director does not" \
                        "read: it ignores the lines under this header, or refuses the file." \
                        "Remove the header and the lines under it, up to the next header."
                fi
                if [[ "$line" =~ $re_header_one ]]; then
                    name="$(ad_toml_name "${BASH_REMATCH[1]}")"
                    for t in "${tables[@]}"; do
                        if is_table "$name" "$t"; then
                            set_about "$t"
                            ad_db_path_refuse "$config" "$n" "$line" \
                                "agent-director reads this header as [$t]: a quoted name is the" \
                                "same as the bare one, and its TOML decoder matches names regardless" \
                                "of letter case. Write it as [$t]."
                        fi
                    done
                    if [[ "$name" =~ $re_bare ]]; then
                        ad_db_path_refuse "$config" "$n" "$line" \
                            "A quoted table name is the same as the bare one. Write it as" \
                            "[$name], without the quotes."
                    fi
                    ad_db_path_refuse "$config" "$n" "$line" \
                        "agent-director reads no table with this name, so it ignores the lines" \
                        "under this header. Remove the header and the lines under it, up to" \
                        "the next header."
                fi
                if [[ "$line" =~ $re_header_dotted ]]; then
                    ad_db_path_refuse "$config" "$n" "$line" \
                        "This header names a table within a table ([a.b]), which" \
                        "agent-director does not read: it ignores the lines under this header," \
                        "or refuses the file. Remove the header and the lines under it, up to" \
                        "the next header."
                fi
                ad_db_path_refuse "$config" "$n" "$line" \
                    "This line starts with [ but is not a table header install.sh can read:" \
                    "a missing ], say, a quoted name holding an escape (\\), or part of an" \
                    "array value that spans lines. Correct the header to [name], with the" \
                    "name bare, or write the array on one line."
            fi
            if [[ ! "$line" =~ $re_keyval ]]; then
                if [[ "$line" =~ $re_key_one ]]; then
                    name="$(ad_toml_name "${BASH_REMATCH[1]}")"
                    for t in "${tables[@]}"; do
                        key_of "$t"
                        if [[ -z "$table" ]] && is_table "$name" "$t"; then
                            refuse_top "$t"
                        fi
                        if [[ "$table" == "$t" ]] && is_key "$name" "$t"; then
                            set_about "$t"
                            ad_db_path_refuse "$config" "$n" "$line" \
                                "agent-director reads this key as $k: a quoted key is the same" \
                                "as the bare one, and its TOML decoder matches names regardless of" \
                                "letter case. Write it as $k."
                        fi
                    done
                    if [[ "$name" =~ $re_bare ]]; then
                        ad_db_path_refuse "$config" "$n" "$line" \
                            "A quoted key is the same as the bare one. Write it as $name, without" \
                            "the quotes."
                    fi
                    ad_db_path_refuse "$config" "$n" "$line" \
                        "agent-director reads no key with this name, so it ignores this line." \
                        "Remove it."
                fi
                # Before any header, a.b = value sets b under [a]; under one, a
                # dotted key sets a key in a table within that table.
                if [[ -z "$table" && "$line" =~ $re_key_two ]]; then
                    name="$(ad_toml_name "${BASH_REMATCH[1]}")"
                    for t in "${tables[@]}"; do
                        key_of "$t"
                        if is_table "$name" "$t"; then
                            set_about "$t"
                            ad_db_path_refuse "$config" "$n" "$line" \
                                "This sets a $t key as a dotted key ($t.$k = ..., say)" \
                                "rather than under a [$t] header. Move this line, without the" \
                                "$t. prefix, to under the file's [$t] header, adding that header" \
                                "at the end of the file if the file has none." \
                                "${not_here[@]}"
                        fi
                    done
                    if [[ "$name" =~ $re_bare ]]; then
                        ad_db_path_refuse "$config" "$n" "$line" \
                            "Before any header, a dotted key a.b = value sets b under [a]. Move this" \
                            "line, without the $name. prefix, to under the file's [$name] header," \
                            "adding that header at the end of the file if the file has none." \
                            "${not_here[@]}"
                    fi
                    ad_db_path_refuse "$config" "$n" "$line" \
                        "agent-director reads no table with this key's first name, so it" \
                        "ignores this line. Remove it."
                fi
                if [[ "$line" =~ $re_key_dotted ]]; then
                    ad_db_path_refuse "$config" "$n" "$line" \
                        "This dotted key sets a key in a table within a table, which" \
                        "agent-director does not read: it ignores this line, or refuses the" \
                        "file. Remove it."
                fi
                ad_db_path_refuse "$config" "$n" "$line" \
                    "This line is not a blank line, a # comment, a [name] header or a" \
                    "name = value line: part of a value that spans lines (an array, say), a" \
                    "quoted name holding an escape (\\), or a typo. Write each value on one" \
                    "line, write the name bare, without quotes or escapes, or correct the" \
                    "typo."
            fi
            key="${BASH_REMATCH[1]}"
            rest="${BASH_REMATCH[2]}"
            for t in "${tables[@]}"; do
                key_of "$t"
                if [[ -z "$table" ]] && is_table "$key" "$t"; then
                    refuse_top "$t"
                fi
            done
            which=""
            if [[ "$table" == store ]] && is_key "$key" store; then
                which=store
            elif [[ "$want_log" -eq 1 && "$table" == log ]] && is_key "$key" log; then
                which=log
            elif [[ "$want_launch" -eq 1 && "$table" == launch ]]; then
                if [[ "$key" =~ $re_claude ]]; then
                    which=claude
                elif [[ "$key" =~ $re_codex ]]; then
                    which=codex
                elif [[ "$key" =~ $re_cursor ]]; then
                    which=cursor
                fi
            fi
            if [[ -z "$which" ]]; then
                continue
            fi
            set_about "$which"
            key_of "$which"
            if [[ "$key" != "$k" ]]; then
                ad_db_path_refuse "$config" "$n" "$line" \
                    "agent-director reads this key as $k: its TOML decoder matches" \
                    "names regardless of letter case. Write it as $k."
            fi
            seen=$have_value
            case "$which" in
                log) seen=$have_log_value ;;
                claude) seen=$have_claude ;;
                codex) seen=$have_codex ;;
                cursor) seen=$have_cursor ;;
            esac
            if [[ "$seen" -eq 1 ]]; then
                ad_db_path_refuse "$config" "$n" "$line" \
                    "This sets $k a second time. Keep one."
            fi
            if [[ "$rest" =~ $re_escape ]]; then
                ad_db_path_refuse "$config" "$n" "$line" \
                    "$k's \"...\" value holds a backslash, which starts an escape." \
                    "Write the path without escapes; in single quotes ('...') a backslash" \
                    "or a double quote is literal."
            elif [[ "$rest" =~ $re_basic || "$rest" =~ $re_literal ]]; then
                parsed="${BASH_REMATCH[1]}"
            else
                ad_db_path_refuse "$config" "$n" "$line" \
                    "$k's value is not a one-line \"...\" or '...' string, optionally" \
                    "followed by a # comment. Write it in that form."
            fi
            if [[ "$parsed" =~ $re_cntrl ]]; then
                if [[ "$about_launch" -eq 1 ]]; then
                    ad_db_path_refuse "$config" "$n" "$line" \
                        "$k's value holds a control character, such as a tab. install.sh" \
                        "cannot install with one in the program's path, so remove it." \
                        "agent-director then launches the path without it."
                elif [[ "$which" == log ]]; then
                    ad_db_path_refuse "$config" "$n" "$line" \
                        "$k's value holds a control character, such as a tab. install.sh" \
                        "cannot install with one in the path, so remove it. agent-director" \
                        "then uses the path without it, not the file it already keeps at" \
                        "this one."
                else
                    ad_db_path_refuse "$config" "$n" "$line" \
                        "$k's value holds a control character, such as a tab. install.sh" \
                        "cannot install with one in the store path, so remove it. agent-director" \
                        "then opens the path without it, not any store it already keeps at this" \
                        "one."
                fi
            fi
            if [[ "$which" == store ]]; then
                if [[ "$parsed" == *\?* ]]; then
                    ad_db_path_refuse "$config" "$n" "$line" \
                        "db_path's value holds a '?'. agent-director's store open reads" \
                        "everything from the first '?' on as SQLite options, so it would not" \
                        "open this path. Use a path without '?'."
                fi
                value="$parsed"
                have_value=1
            elif [[ "$which" == log ]]; then
                log_value="$parsed"
                have_log_value=1
            else
                # Go's config load refuses any launch value but empty, an
                # absolute path and ~/...: a blank value, a relative path, ~
                # alone and ~user/... included.
                case "$parsed" in
                    ""|/*|"~/"*) ;;
                    *)
                        ad_db_path_refuse "$config" "$n" "$line" \
                            "$k's value is not an absolute path or a path starting with ~/, which" \
                            "agent-director refuses (a blank value, a relative path, ~ alone and" \
                            "~user/... are refused too). Write the program's absolute path, or one" \
                            "starting with ~/, or remove this line to launch ${which/cursor/agent} by its name" \
                            "on the PATH; an empty \"\" counts as unset."
                        ;;
                esac
                if [[ "$which" == claude ]]; then
                    claude_value="$parsed"
                    have_claude=1
                elif [[ "$which" == cursor ]]; then
                    cursor_value="$parsed"
                    have_cursor=1
                else
                    codex_value="$parsed"
                    have_codex=1
                fi
            fi
            about_launch=0
        done <"$config"
    fi

    case "$value" in
        "")    resolved="$(ad_clean_path "${base}/state.db")" ;;
        "~/"*) resolved="$(ad_clean_path "${home}/${value:2}")" ;;
        /*)    resolved="$value" ;;
        *)     resolved="$(ad_clean_path "${base}/${value}")" ;;
    esac
    printf '%s\n' "$resolved"
    if [[ "$want_log" -eq 1 ]]; then
        case "$log_value" in
            "")    resolved="" ;;
            "~/"*) resolved="$(ad_clean_path "${home}/${log_value:2}")" ;;
            /*)    resolved="$log_value" ;;
            *)     resolved="$(ad_clean_path "${base}/${log_value}")" ;;
        esac
        printf '%s\n' "$resolved"
    fi
    if [[ "$want_launch" -eq 1 ]]; then
        # Only "", an absolute path or ~/... got here: an absolute path stays
        # as written and ~/... is joined onto the home directory.
        for value in "$claude_value" "$codex_value" "$cursor_value"; do
            case "$value" in
                "~/"*) resolved="$(ad_clean_path "${home}/${value:2}")" ;;
                *)     resolved="$value" ;;
            esac
            printf '%s\n' "$resolved"
        done
    fi
)

# <<< ad_store_db_path (b.2io) <<<

# --------------------------------------------------------------------
# Which AD directory this run works on (SR-11.4, SR-11.7).
#
# ~/.agent-director is present when anything is at that name (a dangling link
# included), except a compatibility link: a link whose raw target, read
# against $HOME when relative and cleaned, is ~/.config/agent-director,
# whether or not that target exists. That link counts as no old directory.
# A run works on ~/.config/agent-director; when only ~/.agent-director is
# present (never moved) it works on that directory instead.
#
# The presence rule and the compatibility-link test below are inlined copies
# of install.sh's (OLD_PRESENT, ad_is_compat_link): keep in step with
# install.sh's OLD_PRESENT test and ad_is_compat_link, and change both
# together.
# --------------------------------------------------------------------

old_present=0
compat_link=0
if [[ -e "$OLD_INSTALL_ROOT" || -L "$OLD_INSTALL_ROOT" ]]; then
    old_present=1
    if [[ -L "$OLD_INSTALL_ROOT" ]]; then
        compat_target=$(readlink "$OLD_INSTALL_ROOT" 2>/dev/null || true)
        case "$compat_target" in
            /*) ;;
            *) compat_target="${HOME}/${compat_target}" ;;
        esac
        if [[ "$(ad_clean_path "$compat_target")" == "$(ad_clean_path "$NEW_INSTALL_ROOT")" ]]; then
            old_present=0
            compat_link=1
        fi
    fi
fi
new_present=0
if [[ -e "$NEW_INSTALL_ROOT" || -L "$NEW_INSTALL_ROOT" ]]; then
    new_present=1
fi
if [[ "$old_present" -eq 1 && "$new_present" -eq 0 ]]; then
    DEFAULT_INSTALL_ROOT="$OLD_INSTALL_ROOT"
else
    DEFAULT_INSTALL_ROOT="$NEW_INSTALL_ROOT"
fi
DEFAULT_BIN_DIR="${DEFAULT_INSTALL_ROOT}/bin"
DEFAULT_ADMIN_DIR="${DEFAULT_INSTALL_ROOT}/admin"

# The other AD directories this run names, and removes under --purge. A
# kept, after-upgrade or recreated directory, a ~/.agent-director beside the
# new directory, and a compatibility link are purge_extras (removed by
# --purge; a link as a link); every *.aside-* directory is an aside (named,
# never removed). purge_extra_kinds holds one line of text per purge_extras
# entry, saying what it is.
purge_extras=()
purge_extra_kinds=()
asides=()
for f in "${HOME}"/.agent-director.pre-move-*; do
    [[ -e "$f" || -L "$f" ]] || continue
    purge_extras+=("$f")
    purge_extra_kinds+=("the old directory an upgrade kept")
done
for f in "${HOME}"/.config/agent-director.after-upgrade-*; do
    [[ -e "$f" || -L "$f" ]] || continue
    purge_extras+=("$f")
    purge_extra_kinds+=("the directory the way back set aside")
done
for f in "${HOME}"/.agent-director.recreated-*; do
    [[ -e "$f" || -L "$f" ]] || continue
    purge_extras+=("$f")
    purge_extra_kinds+=("a recreated old directory the way back set aside")
done
if [[ "$DEFAULT_INSTALL_ROOT" == "$NEW_INSTALL_ROOT" && "$old_present" -eq 1 ]]; then
    purge_extras+=("$OLD_INSTALL_ROOT")
    purge_extra_kinds+=("an old directory recreated beside the new one")
fi
if [[ "$compat_link" -eq 1 ]]; then
    purge_extras+=("$OLD_INSTALL_ROOT")
    purge_extra_kinds+=("a compatibility link to the new directory")
fi
for f in "${HOME}"/.agent-director.aside-* "${HOME}"/.config/agent-director.aside-*; do
    [[ -e "$f" || -L "$f" ]] || continue
    asides+=("$f")
done

# --------------------------------------------------------------------
# Remove hook entries from ~/.claude/settings.json.
# Match by a command starting with the install root's binary path
# (hook_prefix) or the old directory's (old_hook_prefix: the help hooks an
# install before the move to ~/.config/agent-director wrote, SR-11.4), so
# the script only removes ITS entries — other user hooks survive verbatim.
# The new contents are worked out here and written after the symlink check
# below (b.nw5), as config.toml's are.
#
# The filter writes one result per JSON document it reads, so the file's
# documents are counted first (b.zbg), as install.sh counts them. A file
# holding none (empty, or only whitespace) holds no hooks to remove and is
# left alone; one holding several is left alone with a note, as one that
# is not valid JSON is, rather than written back as several documents.
#
# The filter also needs the outer shape install.sh's merge needs (b.ak5):
# an object whose hooks is an object holding SessionStart and SessionEnd
# lists. A hooks, SessionStart or SessionEnd that is missing, null or
# false passes, filled in as the merge fills it in, as does a document
# that is null. A document of another shape (an array or a string, hooks
# a list, or an event list an object, say), which install.sh refuses
# (exit 4, b.cfq), is left alone with a note too, and the uninstall goes
# on: on it the filter would fail, jq's error stopping this script under
# set -e before any binary was removed, or would write an object event
# list's values back as a list. The check runs the merge's own fill-ins
# and then asks for lists, so the two scripts agree on that outer shape
# (the document, hooks, and the two event lists) and on nothing inside
# the lists: install.sh's merge also fails, exit 4, on some values there
# (an entry that is a string, as in {"hooks":{"SessionStart":["x"]}}, or
# a hooks list holding a string, say), which the filter takes and keeps
# verbatim, as below.
#
# Inside the event lists the filter takes any value, so no shape there
# fails it (b.ak5). An entry is agent-director's (ours) only when it is
# an object whose hooks is a list holding an object whose command is a
# string starting with hook_prefix; any other value (an entry that is a
# string, a hooks list holding a number, a command that is not a string,
# say) is kept verbatim, never indexed or read as a string. Such values
# can sit beside agent-director's entries (install.sh's merge passes a
# command that is not a string, and a hand edit can add any of them
# after the install), so leaving such a file alone would leave those
# entries running a removed binary. Should jq fail on the file anyway,
# it is left alone with a note under jq's error, and the uninstall goes
# on.
# --------------------------------------------------------------------

hook_prefix="${NEW_INSTALL_ROOT}/bin/agent-director"
old_hook_prefix="${OLD_INSTALL_ROOT}/bin/agent-director"
# ours, for the filter and the symlink branch's count below: a command
# starting with either directory's bin/agent-director path.
hook_ours_jq='
    def ours:
      type == "object"
      and (.hooks | type) == "array"
      and any(.hooks[]; type == "object"
        and (.command | type) == "string"
        and ((.command | startswith($prefix)) or (.command | startswith($oldprefix))));
'
settings_edit=0
if [[ -f "$DEFAULT_SETTINGS_PATH" ]]; then
    if ! command -v jq >/dev/null 2>&1; then
        echo "uninstall.sh: jq is required to safely edit settings.json" >&2
        exit 2
    fi
    existing=$(<"$DEFAULT_SETTINGS_PATH")
    if ! settings_docs=$(printf '%s' "$existing" | jq -n '[inputs] | length' 2>/dev/null); then
        echo "uninstall.sh: ~/.claude/settings.json is not valid JSON; leaving it alone" >&2
    elif [[ "$settings_docs" -gt 1 ]]; then
        echo "uninstall.sh: ~/.claude/settings.json holds $settings_docs JSON documents, not one; leaving it alone" >&2
    elif [[ "$settings_docs" -eq 1 ]] && ! printf '%s' "$existing" | jq -e '
            .hooks //= {}
            | .hooks.SessionStart //= []
            | .hooks.SessionEnd //= []
            | (.hooks.SessionStart | type) == "array"
              and (.hooks.SessionEnd | type) == "array"
        ' >/dev/null 2>&1; then
        echo "uninstall.sh: ~/.claude/settings.json is valid JSON, but not an object whose hooks hold event lists; leaving it alone" >&2
    elif [[ "$settings_docs" -eq 1 ]]; then
        if new=$(printf '%s' "$existing" | jq \
            --arg prefix "$hook_prefix" --arg oldprefix "$old_hook_prefix" "$hook_ours_jq"'
            .hooks //= {}
            | .hooks.SessionStart //= []
            | .hooks.SessionEnd //= []
            | .hooks.SessionStart |= [.[] | select(ours | not)]
            | .hooks.SessionEnd |= [.[] | select(ours | not)]
        '); then
            settings_edit=1
        else
            echo "uninstall.sh: cannot remove agent-director's hook entries from ~/.claude/settings.json (jq's error is above); leaving it alone" >&2
        fi
    fi
fi

# --------------------------------------------------------------------
# Reverse install.sh's defaults.inject_help_hook merge: drop that key
# from config.toml. If [defaults] is left empty (no other keys, only
# blank lines or comments), drop the section header too. Symmetric
# with install.sh's Q4=yes config merge, and headers are matched as
# that merge matches them (b.onv): blanks before, inside and after the
# brackets, a trailing # comment, a CRLF's CR, and a UTF-8 byte-order
# mark on line 1. A header missed here leaves the key install wrote
# under it. The header name and the key are matched regardless of
# ASCII letter case too, as that merge matches them ([Defaults],
# INJECT_HELP_HOOK; b.hhk): the merge rewrites a key in any case to
# inject_help_hook, under whichever such header set it, and adds it
# under the first such header when none did. As for settings.json, the
# new contents are written after the symlink check below.
# --------------------------------------------------------------------

CONFIG_TOML="${DEFAULT_INSTALL_ROOT}/config.toml"
config_edit=0
if [[ -f "$CONFIG_TOML" ]]; then
    cleaned=$(LC_ALL=C awk '
        function flush_defaults() {
            has_content = 0
            for (i = 1; i <= n; i++) {
                stripped = lines[i]
                sub(/^[[:space:]]+/, "", stripped)
                if (stripped != "" && stripped !~ /^#/) {
                    has_content = 1
                    break
                }
            }
            if (has_content) {
                print header
                for (i = 1; i <= n; i++) print lines[i]
            }
            delete lines
            n = 0
            header = ""
        }
        BEGIN { in_defaults = 0; n = 0; header = "" }
        { line = $0 }
        NR == 1 { sub(/^\357\273\277/, "", line) }
        line ~ /^[[:blank:]]*\[/ {
            if (in_defaults) {
                flush_defaults()
                in_defaults = 0
            }
            if (line ~ /^[[:blank:]]*\[[[:blank:]]*[Dd][Ee][Ff][Aa][Uu][Ll][Tt][Ss][[:blank:]]*\][[:space:]]*(#.*)?$/) {
                in_defaults = 1
                header = $0
                next
            }
            print
            next
        }
        in_defaults {
            if ($0 ~ /^[[:space:]]*[Ii][Nn][Jj][Ee][Cc][Tt]_[Hh][Ee][Ll][Pp]_[Hh][Oo][Oo][Kk][[:space:]]*=/) {
                next
            }
            lines[++n] = $0
            next
        }
        { print }
        END {
            if (in_defaults) flush_defaults()
        }
    ' "$CONFIG_TOML")
    original=$(<"$CONFIG_TOML")
    if [[ "$cleaned" != "$original" ]]; then
        config_edit=1
    fi
fi

# --------------------------------------------------------------------
# Symlinked settings.json and config.toml (b.nw5)
#
# The edits write through a symlinked settings.json or config.toml to
# the file its links resolve to, keeping the link
# (ad_replace_keeping_mode). That write fails when that file's directory
# cannot be written in (home-manager links into a read-only /nix/store,
# say), so each file about to be edited is checked first, before
# anything is removed or changed, as install.sh checks them in
# pre-flight: settings.json here, config.toml after the --purge
# confirmation below, which decides what its check does. A file that
# cannot be written through is refused (exit 2), naming what to remove
# where it comes from; this check never sees a loop or a missing
# directory, as each edited file resolved to a regular file (-f) above.
# A settings.json that cannot be written through and holds none of
# agent-director's hook entries is left alone instead: it would be
# rewritten (every settings.json holding one JSON document of the shape
# checked above is) with nothing removed, and a refusal would then stop
# every uninstall until the link went. A settings.json left alone while
# its edit is worked out above (not valid JSON, not one document, the
# wrong outer shape, or the filter failing) is not edited, so its
# symlink is not checked. The count of entries to remove below runs
# only after the check has refused the link; should jq fail on it, the
# file is left alone too, after that check.
# --------------------------------------------------------------------

# ad_link_refuse <file> <what> <remedy>... — report a symlinked <file>
# that ad_link_write_check refused, from link_target and link_why, while
# uninstall.sh has <what> to do to it, with <remedy> lines saying what to
# remove where the file comes from, then exit 2.
ad_link_refuse() {
    local file="$1" what="$2" remedy
    shift 2
    echo "uninstall.sh: cannot $what $file through its symlink; refusing to uninstall." >&2
    echo "  link    : $file" >&2
    if [[ -n "$link_target" ]]; then
        echo "  target  : $link_target" >&2
    fi
    echo "  $link_why" >&2
    echo "  uninstall.sh writes its edits into the file a symlinked settings.json or" >&2
    echo "  config.toml resolves to, keeping the link. Fix the link so it reaches a" >&2
    echo "  file in a directory you can write in, or, where the file comes from (your" >&2
    echo "  dotfiles or home-manager configuration, say), remove:" >&2
    for remedy in "$@"; do
        echo "  $remedy" >&2
    done
    echo "  Nothing was removed or changed. Re-run this uninstall after the change." >&2
    exit 2
}

if [[ "$settings_edit" -eq 1 ]] && ! ad_link_write_check "$DEFAULT_SETTINGS_PATH"; then
    # The entries the filter above removes: those it marks ours. Only a
    # file the filter ran on gets here, so this count does not fail
    # either; should jq fail anyway, the file is left alone with the
    # filter's note and the uninstall goes on (b.ak5).
    if ! removed=$(printf '%s' "$existing" | jq \
        --arg prefix "$hook_prefix" --arg oldprefix "$old_hook_prefix" "$hook_ours_jq"'
        .hooks //= {}
        | [(.hooks.SessionStart // []), (.hooks.SessionEnd // []) | .[] | select(ours)]
        | length
    '); then
        settings_edit=0
        echo "uninstall.sh: cannot remove agent-director's hook entries from ~/.claude/settings.json (jq's error is above); leaving it alone" >&2
    elif [[ "$removed" -eq 0 ]]; then
        settings_edit=0
        echo "uninstall.sh: left $DEFAULT_SETTINGS_PATH alone: it holds no agent-director hook entries, and its symlink cannot be written through"
    else
        ad_link_refuse "$DEFAULT_SETTINGS_PATH" "remove agent-director's hook entries from" \
            "each SessionStart and SessionEnd entry holding a hook whose command starts" \
            "with $hook_prefix or $old_hook_prefix."
    fi
fi

# --------------------------------------------------------------------
# --purge confirmation (b.nw5). Asked here, before anything is removed
# or changed, as config.toml's symlink check below depends on it; and
# after settings.json's check, which a purge does not waive (that file
# lives outside ~/.config/agent-director), so no refusal follows a "y". From
# here PURGE is 1 only for a confirmed purge. An aborted purge is a plain
# uninstall: it goes on, and ends with "--purge aborted" (exit 0) where
# the purge would have run.
# --------------------------------------------------------------------

purge_aborted=0
if [[ "$PURGE" -eq 1 && "$FORCE" -eq 0 ]]; then
    purge_prompt="uninstall.sh: --purge will rm -rf ${DEFAULT_INSTALL_ROOT}"
    if [[ "${#purge_extras[@]}" -gt 0 ]]; then
        purge_prompt="${purge_prompt}, and also remove these other AD directories (a link is removed as a link, never what it points to):"
        for ((i = 0; i < ${#purge_extras[@]}; i++)); do
            purge_prompt="${purge_prompt} ${purge_extras[$i]} (${purge_extra_kinds[$i]})"
            if [[ "$i" -lt $((${#purge_extras[@]} - 1)) ]]; then
                purge_prompt="${purge_prompt},"
            fi
        done
    fi
    printf "%s — proceed? [y/N] " "$purge_prompt"
    read -r answer
    case "$answer" in
        y|Y|yes|YES) ;;
        *) PURGE=0; purge_aborted=1 ;;
    esac
fi

# config.toml's symlink check (see "Symlinked settings.json and
# config.toml" above). Under a confirmed purge a config.toml that cannot
# be written through is left alone, with a note, instead of refused: the
# purge's rm -rf removes the link (never the file it resolves to), so a
# refusal would only stop the purge. That file keeps inject_help_hook,
# and the note names it. A config.toml that can be written through is
# edited as without --purge, so a target outside ~/.config/agent-director (a
# dotfiles repo, say) loses the key install.sh wrote there.
if [[ "$config_edit" -eq 1 ]] && ! ad_link_write_check "$CONFIG_TOML"; then
    if [[ "$PURGE" -eq 1 ]]; then
        config_edit=0
        echo "uninstall.sh: left $CONFIG_TOML alone: its symlink cannot be written through, and --purge removes the link; its target, $link_target, keeps inject_help_hook"
    else
        ad_link_refuse "$CONFIG_TOML" "clear inject_help_hook from" \
            "inject_help_hook from [defaults], and the [defaults] header too when only" \
            "blank lines and comments are left under it."
    fi
fi

if [[ "$settings_edit" -eq 1 ]]; then
    # Backup-before-edit (symmetric with install.sh) so a regressed
    # jq filter is recoverable from a timestamped .bak.
    backup_settings="${DEFAULT_SETTINGS_PATH}.bak.$(date +%Y%m%d-%H%M%S)"
    ad_backup_keeping_mode "$DEFAULT_SETTINGS_PATH" "$backup_settings"
    ad_replace_keeping_mode "$DEFAULT_SETTINGS_PATH" "$new"
    echo "uninstall.sh: backed up prior settings to $backup_settings"
    echo "uninstall.sh: removed help hook entries from $DEFAULT_SETTINGS_PATH"
fi
if [[ "$config_edit" -eq 1 ]]; then
    backup_cfg="${CONFIG_TOML}.bak.$(date +%Y%m%d-%H%M%S)"
    ad_backup_keeping_mode "$CONFIG_TOML" "$backup_cfg"
    ad_replace_keeping_mode "$CONFIG_TOML" "$cleaned"
    echo "uninstall.sh: cleared inject_help_hook from $CONFIG_TOML (backup $backup_cfg)"
fi

# --------------------------------------------------------------------
# Remove the schedule install.sh --schedule added (b.wur): the crontab lines
# that carry the marker, and the systemd user units of the two sweeps. Done
# before the binaries go, so nothing runs a binary that is gone. A run with
# none present prints nothing and changes nothing. The marker, the unit names
# and the two test switches are install.sh's (its "The schedule" section);
# keep them in step.
# --------------------------------------------------------------------

readonly SCHEDULE_MARK="agent-director-schedule"
SCHEDULE_UNIT_FILES=(
    agent-director-find-missing.service agent-director-find-missing.timer
    agent-director-expire.service agent-director-expire.timer
)
SCHEDULE_TIMERS=(agent-director-find-missing.timer agent-director-expire.timer)

# ad_sched_mgr_read — ask the systemd user manager, once, whether it answers
# and what its HOME and XDG_CONFIG_HOME are (`systemctl --user show-environment`),
# as install.sh does: sets SCHED_MGR_OK, SCHED_MGR_HOME and SCHED_MGR_XDG (each
# empty unless an absolute path). Called in the shell itself, not in a $( ).
SCHED_MGR_READ=0
SCHED_MGR_OK=0
SCHED_MGR_HOME=""
SCHED_MGR_XDG=""
ad_sched_mgr_read() {
    local sc env line
    [[ "$SCHED_MGR_READ" -eq 0 ]] || return 0
    SCHED_MGR_READ=1
    sc="${INSTALL_SH_TEST_SYSTEMCTL:-systemctl}"
    command -v "$sc" >/dev/null 2>&1 || return 0
    env="$("$sc" --user show-environment 2>/dev/null </dev/null)" || return 0
    SCHED_MGR_OK=1
    while IFS= read -r line; do
        case "$line" in
            HOME=/*) SCHED_MGR_HOME="${line#HOME=}" ;;
            XDG_CONFIG_HOME=/*) SCHED_MGR_XDG="${line#XDG_CONFIG_HOME=}" ;;
        esac
    done <<<"$env"
}

# ad_sched_unit_dir — the directory the systemd user manager reads the user's
# units from, by install.sh's rule: the manager's own XDG_CONFIG_HOME's
# systemd/user when that is an absolute path, else its HOME's
# .config/systemd/user; with neither known (the manager does not answer, or its
# answer has neither), this shell's rule: an absolute $XDG_CONFIG_HOME's
# systemd/user, else $HOME/.config/systemd/user.
ad_sched_unit_dir() {
    ad_sched_mgr_read
    if [[ "$SCHED_MGR_OK" -eq 1 ]]; then
        if [[ -n "$SCHED_MGR_XDG" ]]; then
            printf '%s' "${SCHED_MGR_XDG}/systemd/user"
            return 0
        elif [[ -n "$SCHED_MGR_HOME" ]]; then
            printf '%s' "${SCHED_MGR_HOME%/}/.config/systemd/user"
            return 0
        fi
    fi
    case "${XDG_CONFIG_HOME:-}" in
        /*) printf '%s' "${XDG_CONFIG_HOME}/systemd/user" ;;
        *) printf '%s' "${HOME}/.config/systemd/user" ;;
    esac
}

# ad_sched_remove_cron — remove the marked lines from the user's crontab, every
# other line kept. Prints what it did. Nothing to remove (no crontab program,
# no crontab, no marked line) is a silent no-op; a `crontab -l` that fails for
# any reason but "no crontab for <user>" is reported on stderr (the exit status
# is not changed), as install.sh's schedule step does.
ad_sched_remove_cron() {
    local cmd text l t kept="" jobs=0 notes=0 rc=0 lrc=0 err
    cmd="${INSTALL_SH_TEST_CRONTAB:-crontab}"
    command -v "$cmd" >/dev/null 2>&1 || return 0
    text="$("$cmd" -l 2>/dev/null </dev/null)" || lrc=$?
    if [[ "$lrc" -ne 0 ]]; then
        err="$("$cmd" -l 2>&1 >/dev/null </dev/null || true)"
        case "$err" in
            *"no crontab"*|*"No such file or directory"*) return 0 ;;
        esac
        {
            echo "uninstall.sh: could not read the crontab to remove the schedule (crontab -l failed, exit $lrc): ${err:-no output}"
            echo "  If install.sh --schedule added lines tagged # $SCHEDULE_MARK to it, remove them by hand (crontab -e)."
        } >&2
        return 0
    fi
    [[ -n "$text" ]] || return 0
    while IFS= read -r l; do
        if [[ "$l" == *"$SCHEDULE_MARK"* ]]; then
            # The header comment of the schedule is removed with the jobs but is
            # not a schedule line: only the jobs are counted.
            t="${l#"${l%%[![:space:]]*}"}"
            if [[ "$t" == "#"* ]]; then
                notes=$((notes + 1))
            else
                jobs=$((jobs + 1))
            fi
        else
            kept="${kept}${l}"$'\n'
        fi
    done <<<"$text"
    [[ $((jobs + notes)) -gt 0 ]] || return 0
    if [[ -z "${kept//[[:space:]]/}" ]]; then
        err="$("$cmd" -r 2>&1 >/dev/null </dev/null)" || rc=$?
    else
        err="$(printf '%s' "$kept" | "$cmd" - 2>&1 >/dev/null)" || rc=$?
    fi
    if [[ "$rc" -ne 0 ]]; then
        {
            echo "uninstall.sh: could not remove the schedule from the crontab (exit $rc): ${err:-no output}"
            echo "  Remove the $((jobs + notes)) line(s) tagged # $SCHEDULE_MARK by hand (crontab -e)."
        } >&2
        return 0
    fi
    if [[ "$jobs" -gt 0 ]]; then
        echo "uninstall.sh: removed $jobs schedule line(s) tagged # $SCHEDULE_MARK from the crontab"
    else
        echo "uninstall.sh: removed the schedule's marker comment from the crontab"
    fi
}

# ad_sched_remove_units — disable the two timers and remove the units, each
# file only when it carries the marker. Prints what it did; none present is a
# silent no-op.
ad_sched_remove_units() {
    local dir sc f t found=0 usable=0
    ad_sched_mgr_read
    dir="$(ad_sched_unit_dir)"
    for f in "${SCHEDULE_UNIT_FILES[@]}"; do
        if [[ -f "${dir}/${f}" ]] && grep -q -F "$SCHEDULE_MARK" "${dir}/${f}"; then
            found=1
        fi
    done
    [[ "$found" -eq 1 ]] || return 0
    sc="${INSTALL_SH_TEST_SYSTEMCTL:-systemctl}"
    if [[ "$SCHED_MGR_OK" -eq 1 ]]; then
        usable=1
        "$sc" --user disable --now "${SCHEDULE_TIMERS[@]}" >/dev/null 2>&1 </dev/null || true
    fi
    for f in "${SCHEDULE_UNIT_FILES[@]}"; do
        if [[ -f "${dir}/${f}" ]] && grep -q -F "$SCHEDULE_MARK" "${dir}/${f}"; then
            rm -f "${dir}/${f}"
        fi
    done
    for t in "${SCHEDULE_TIMERS[@]}"; do
        if [[ -L "${dir}/timers.target.wants/${t}" ]]; then
            rm -f "${dir}/timers.target.wants/${t}"
        fi
    done
    if [[ "$usable" -eq 1 ]]; then
        "$sc" --user daemon-reload >/dev/null 2>&1 </dev/null || true
        echo "uninstall.sh: disabled the agent-director systemd user timers and removed their units from $dir"
    else
        echo "uninstall.sh: removed the agent-director systemd user units from $dir (the user manager was not reachable, so the timers were not disabled; they stop at its next start)"
    fi
}

ad_sched_remove_cron
ad_sched_remove_units

# ad_sched_remove_local — remove the copy of the five schedule files install.sh
# keeps in ~/.config/agent-director/schedule/ when it ran piped to bash and
# fetched them, then the directory; a directory that holds other files stays,
# with a note. A fetch's leftover temp directory (.schedule.*) goes too. None
# present is a silent no-op.
ad_sched_remove_local() {
    local dir="${DEFAULT_INSTALL_ROOT}/schedule" f
    for f in "${DEFAULT_INSTALL_ROOT}"/.schedule.*; do
        if [[ -d "$f" && ! -L "$f" ]]; then
            rm -rf -- "$f"
        fi
    done
    [[ -d "$dir" && ! -L "$dir" ]] || return 0
    for f in agent-director.cron "${SCHEDULE_UNIT_FILES[@]}"; do
        rm -f -- "${dir}/${f}"
    done
    if rmdir "$dir" 2>/dev/null; then
        echo "uninstall.sh: removed the fetched schedule files and $dir"
    else
        echo "uninstall.sh: removed the fetched schedule files; left $dir, which holds other files"
    fi
}

ad_sched_remove_local

# --------------------------------------------------------------------
# Remove binaries (canonical + .prior snapshot + any legacy
# versioned-binary siblings from pre-b.43y installs).
# --------------------------------------------------------------------

if [[ -d "$DEFAULT_BIN_DIR" ]]; then
    for f in "$DEFAULT_BIN_DIR"/agent-director "$DEFAULT_BIN_DIR"/agent-director.*; do
        [[ -e "$f" || -L "$f" ]] || continue
        rm -f "$f"
    done
    echo "uninstall.sh: removed binaries under $DEFAULT_BIN_DIR"
fi

# --------------------------------------------------------------------
# Remove the operator tool agent-director-admin (and its .prior
# rollback snapshot and any install tempfile beside it), then its
# directory (b.vqr). A directory someone put other files in is left in
# place, with a note.
# --------------------------------------------------------------------

if [[ -d "$DEFAULT_ADMIN_DIR" ]]; then
    for f in "$DEFAULT_ADMIN_DIR"/agent-director-admin "$DEFAULT_ADMIN_DIR"/agent-director-admin.*; do
        [[ -e "$f" || -L "$f" ]] || continue
        rm -f "$f"
    done
    if rmdir "$DEFAULT_ADMIN_DIR" 2>/dev/null; then
        echo "uninstall.sh: removed $DEFAULT_ADMIN_DIR"
    else
        echo "uninstall.sh: removed agent-director-admin; left $DEFAULT_ADMIN_DIR, which holds other files"
    fi
fi

# --------------------------------------------------------------------
# Remove PATH symlink (if any).
# --------------------------------------------------------------------

if [[ -L "${SYMLINK_DIR}/agent-director" ]]; then
    rm -f "${SYMLINK_DIR}/agent-director"
    echo "uninstall.sh: removed symlink ${SYMLINK_DIR}/agent-director"
fi

# --------------------------------------------------------------------
# Optional MCP deregistration.
# --------------------------------------------------------------------

# The Claude Code program is the one install.sh runs (SR-11.1): [launch]
# claude_path from the config.toml of the directory this run works on, when
# it names an executable file, else `claude` from PATH. The file is read
# here, before --purge below removes the directory (and after the
# inject_help_hook edit above, which leaves [launch] alone). A [launch] line
# the reader cannot read, or a claude_path naming no executable file, never
# stops uninstall: it gets a note naming the line and the run falls back to
# `claude` from PATH. With no program at all the step is skipped, with a
# line saying so, and the exit status is as without the step.
if [[ "$MCP_ALSO" -eq 1 ]]; then
    launch_claude=""
    if launch_out="$(ad_store_db_path "$CONFIG_TOML" "$HOME" "$DEFAULT_INSTALL_ROOT" launch 2>&1)"; then
        # Lines: the store path, claude_path, codex_path, cursor_path (any may be empty).
        { IFS= read -r launch_line || true; IFS= read -r launch_claude || true; } <<<"$launch_out"
    else
        # The reader's refusal: its headline, then config, line and reason
        # lines, then a closing paragraph about install.sh that does not
        # apply here. Name the line and the reason; fall back to PATH.
        {
            echo "uninstall.sh: could not read [launch] claude_path from $CONFIG_TOML; using claude from PATH."
            while IFS= read -r launch_line; do
                case "$launch_line" in
                    "install.sh:"*) continue ;;
                    "  install.sh reads"*|"  Nothing was installed"*) break ;;
                esac
                printf '%s\n' "$launch_line"
            done <<<"$launch_out"
        } >&2
    fi
    mcp_how=path
    if [[ -n "$launch_claude" ]]; then
        if [[ -f "$launch_claude" && -x "$launch_claude" ]]; then
            CLAUDE_PROG="$launch_claude"
            mcp_how=launch
        else
            echo "uninstall.sh: [launch] claude_path = \"$launch_claude\" in $CONFIG_TOML names no executable file; using claude from PATH" >&2
        fi
    fi
    if [[ "$mcp_how" == path ]] && ! command -v "$CLAUDE_PROG" >/dev/null 2>&1; then
        if [[ -n "$launch_claude" ]]; then
            launch_why="[launch] claude_path in $CONFIG_TOML names no executable file"
        else
            launch_why="[launch] claude_path is not set"
        fi
        echo "uninstall.sh: --mcp-also skipped: no Claude Code program was found (claude is not on PATH, and $launch_why), so agent-director was not deregistered from Claude Code"
    else
        mcp_rc=0
        mcp_err="$("$CLAUDE_PROG" mcp remove agent-director 2>&1 >/dev/null </dev/null)" || mcp_rc=$?
        if [[ "$mcp_rc" -eq 0 ]]; then
            echo "uninstall.sh: deregistered agent-director from MCP"
        elif [[ "$mcp_err" == "No MCP server named"* ]]; then
            echo "uninstall.sh: no agent-director registration found from this directory; nothing to deregister"
        else
            MCP_REMOVE_FAILED=1
            {
                echo "uninstall.sh: deregistering agent-director from MCP failed; nothing was removed from Claude Code. Everything else here went as reported, and --purge (if given) still runs."
                printf '  command run: %q mcp remove agent-director\n' "$CLAUDE_PROG"
                echo "  exit status: $mcp_rc"
                echo "  stderr:"
                if [[ -z "$mcp_err" ]]; then
                    echo "    (none)"
                else
                    while IFS= read -r mcp_line; do
                        printf '    %s\n' "$mcp_line"
                    done <<<"$mcp_err"
                fi
                echo "  Remove it by hand, in the scope that 'mcp get agent-director' shows (a name registered in several scopes needs -s):"
                printf '    %q mcp remove agent-director -s <scope>\n' "$CLAUDE_PROG"
            } >&2
        fi
    fi
fi

# --------------------------------------------------------------------
# --purge: full directory removal, confirmed above. rm -rf removes a
# symlink under ~/.config/agent-director, not the file it points to.
# --------------------------------------------------------------------

# Without a confirmed --purge, every other AD directory found is kept and
# named, with the advice to delete it once the upgrade is confirmed.
if [[ "$PURGE" -eq 0 ]]; then
    for ((i = 0; i < ${#purge_extras[@]}; i++)); do
        # A compatibility link is no directory to delete once the upgrade is
        # confirmed; only --purge removes it, so it is not named here.
        if [[ "${purge_extra_kinds[$i]}" == "a compatibility link"* ]]; then
            continue
        fi
        echo "uninstall.sh: kept ${purge_extras[$i]} (${purge_extra_kinds[$i]}); delete it once the upgrade is confirmed, or run --purge"
    done
fi
# Every aside is left, under --purge too: an operator set it aside to settle
# a conflict, and it may hold the data not chosen.
for f in ${asides[@]+"${asides[@]}"}; do
    echo "uninstall.sh: left $f (set aside to settle a conflict; it may hold data you did not choose, so --purge leaves it); delete it yourself once you are sure it is not needed"
done

if [[ "$purge_aborted" -eq 1 ]]; then
    echo "uninstall.sh: --purge aborted"
    exit "$(( MCP_REMOVE_FAILED == 1 ? 6 : 0 ))"
fi
if [[ "$PURGE" -eq 1 ]]; then
    rm -rf "$DEFAULT_INSTALL_ROOT"
    echo "uninstall.sh: purged $DEFAULT_INSTALL_ROOT"
    for ((i = 0; i < ${#purge_extras[@]}; i++)); do
        rm -rf "${purge_extras[$i]}"
        echo "uninstall.sh: purged ${purge_extras[$i]} (${purge_extra_kinds[$i]})"
    done
fi

if [[ "$MCP_REMOVE_FAILED" -eq 1 ]]; then
    echo "uninstall.sh: done, except the MCP deregistration failed (see above); exit 6" >&2
    exit 6
fi
echo "uninstall.sh: done"
