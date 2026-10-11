#!/usr/bin/env bash
# guard.sh — the measure-exit host-state guard (Epic 21). It proves a
# measurement run left the host's real agent-director store alone. It only
# READS two files under each of the two directories agent-director has kept its
# files in, <home>/.agent-director and <home>/.config/agent-director (a host may
# hold either): state.db and ad-trail.jsonl. It never opens them with sqlite, never locks, copies or writes anything
# under the home, and never runs agent-director, tmux or any Go artifact.
#
# Two modes, chosen at snapshot time and recorded in the state file:
#
#   quiet-host (default): SHA-256 of state.db and ad-trail.jsonl in each
#     directory before and after; verify prints both side by side and fails on any change,
#     creation or deletion. The snapshot takes two readings --settle seconds
#     apart and refuses to start if they already differ (the host is busy).
#
#   busy-host (--busy-host): for a host whose own agent-director sessions keep
#     writing the store. The snapshot records the trail's size and identity;
#     verify scans ONLY the bytes appended since then for the run's
#     identifiers (its store id, instance ids, private socket, run id; from
#     the driver's harness-ids.txt and --id) and fails on any hit. This is done
#     for each directory's trail. state.db is
#     reported "not checked on a busy host": a live WAL database is never
#     opened, not even read-only. A trail that shrank or was replaced (a new
#     inode) since the snapshot cannot be proven clean and fails. One case it
#     cannot see: a trail truncated in place and then grown past its old size
#     before verify (agent-director never truncates its trail).
#
# Usage:
#   guard.sh snapshot --state <file> [--home <dir>] [--busy-host] [--settle <secs>]
#   guard.sh verify   --state <file> [--home <dir>] [--ids-file <file>]... [--id <value>]...
#
# The home is the invoking user's passwd-entry home: the host's real store
# lives under it, and a redirected $HOME cannot move it (b.8dr). --home
# overrides it for dry runs and tests only, and the output says so. The state
# file must lie outside both <home>/.agent-director and
# <home>/.config/agent-director; snapshot refuses an existing one.
#
# Exit status: 0 pass; 1 the guard failed (a change, or an identifier found);
# 2 bad usage or an unreadable file; 3 snapshot refused (host not quiet).

set -euo pipefail

readonly DB_NAME=state.db
readonly TRAIL_NAME=ad-trail.jsonl
readonly WAL_NAME=state.db-wal
# The two directories the guard watches, relative to the home: the old one and
# the one agent-director moved to. A host may hold either.
readonly STORE_RELS=(.agent-director .config/agent-director)
readonly DEFAULT_SETTLE=3
# Shortest identifier verify accepts: a short value would match unrelated
# trail bytes.
readonly MIN_ID_LEN=8

die() {
    local rc="$1"
    shift
    printf 'guard: %s\n' "$*" >&2
    exit "$rc"
}

# passwd_home prints the invoking user's passwd-entry home.
passwd_home() {
    local uid home=""
    uid="$(id -u)"
    if command -v getent >/dev/null 2>&1; then
        home="$(getent passwd "$uid" | cut -d: -f6)"
    fi
    if [[ -z "$home" && -r /etc/passwd ]]; then
        home="$(awk -F: -v u="$uid" '$3 == u { print $6; exit }' /etc/passwd)"
    fi
    if [[ -z "$home" ]] && command -v dscl >/dev/null 2>&1; then
        home="$(dscl . -read "/Users/$(id -un)" NFSHomeDirectory 2>/dev/null | awk '{ print $2 }')"
    fi
    [[ -n "$home" ]] || die 2 "cannot resolve the passwd-entry home of uid $uid"
    printf '%s\n' "$home"
}

# checksum prints a file's SHA-256, or "absent".
checksum() {
    local f="$1" sum
    if [[ ! -e "$f" ]]; then
        printf 'absent\n'
        return
    fi
    [[ -f "$f" && -r "$f" ]] || die 2 "$f is not a readable regular file"
    if command -v sha256sum >/dev/null 2>&1; then
        sum="$(sha256sum <"$f")"
    else
        sum="$(shasum -a 256 <"$f")"
    fi
    printf '%s\n' "${sum%% *}"
}

# file_size prints a file's size in bytes (GNU or BSD stat).
file_size() {
    stat -c %s "$1" 2>/dev/null || stat -f %z "$1"
}

# file_identity prints a file's device:inode (GNU or BSD stat).
file_identity() {
    stat -c %d:%i "$1" 2>/dev/null || stat -f %d:%i "$1"
}

# wal_note reports, for information only, whether state.db-wal exists in each
# watched directory.
wal_note() {
    local d
    for d in "${store_dirs[@]}"; do
        if [[ -e "$d/$WAL_NAME" ]]; then
            printf 'info: %s present (agent-director has written this store recently)\n' "$d/$WAL_NAME"
        else
            printf 'info: %s absent\n' "$d/$WAL_NAME"
        fi
    done
}

# state_get prints key's value from the state file.
state_get() {
    local key="$1" line
    while IFS= read -r line; do
        if [[ "${line%%=*}" == "$key" ]]; then
            printf '%s\n' "${line#*=}"
            return 0
        fi
    done <"$state"
    die 2 "state file $state has no $key: was it written by guard.sh snapshot?"
}

# resolve_home sets home and home_label from --home or the passwd entry.
resolve_home() {
    if [[ -n "$home_override" ]]; then
        [[ "$home_override" == /* ]] || die 2 "--home must be an absolute path"
        home="$home_override"
        home_label="override (dry run or test only; NOT the real home)"
    else
        home="$(passwd_home)"
        home_label="passwd entry of uid $(id -u)"
    fi
    local rel
    store_dirs=()
    watched=()
    for rel in "${STORE_RELS[@]}"; do
        store_dirs+=("$home/$rel")
        watched+=("$home/$rel/$DB_NAME" "$home/$rel/$TRAIL_NAME")
    done
}

# rel_of prints a watched path relative to the home, the key the state file
# records it under.
rel_of() {
    printf '%s\n' "${1#"$home"/}"
}

# check_state_path refuses a state file inside either watched directory.
check_state_path() {
    local dir store_real rel d
    dir="$(cd "$(dirname -- "$state")" 2>/dev/null && pwd -P)" \
        || die 2 "the state file's directory $(dirname -- "$state") does not exist"
    for d in "${store_dirs[@]}"; do
        store_real="$d"
        if [[ -d "$home" ]]; then
            rel="${d#"$home"/}"
            store_real="$(cd "$home" && pwd -P)/$rel"
        fi
        case "$dir/" in
            "$store_real"/* | "$d"/*) die 2 "the state file must not lie under $d" ;;
        esac
    done
}

snapshot_quiet() {
    local f i changed=0 sums1=() sums2=()
    printf 'warning: any agent-director activity on this host during the run makes verify fail; keep the host quiet\n'
    for f in "${watched[@]}"; do
        sums1+=("$(checksum "$f")")
    done
    sleep "$settle"
    for f in "${watched[@]}"; do
        sums2+=("$(checksum "$f")")
    done
    for i in "${!watched[@]}"; do
        if [[ "${sums1[$i]}" != "${sums2[$i]}" ]]; then
            printf 'changed: %s\n' "${watched[$i]}" >&2
            changed=1
        fi
    done
    if [[ "$changed" -ne 0 ]]; then
        die 3 "REFUSED: the host is not quiet (the store changed within ${settle}s before the run). Stop the host's agent-director activity, or use busy-host mode (snapshot --busy-host)."
    fi
    {
        printf 'mode=quiet\nhome=%s\n' "$home"
        for i in "${!watched[@]}"; do
            printf '%s=%s\n' "$(rel_of "${watched[$i]}")" "${sums1[$i]}"
        done
    } >"$state"
    for i in "${!watched[@]}"; do
        printf 'snapshot (quiet-host): %s %s\n' "$(rel_of "${watched[$i]}")" "${sums1[$i]}"
    done
}

snapshot_busy() {
    local d trail rel size ident
    printf 'mode=busy\nhome=%s\n' "$home" >"$state"
    for d in "${store_dirs[@]}"; do
        trail="$d/$TRAIL_NAME"
        rel="$(rel_of "$trail")"
        size=absent
        ident=absent
        if [[ -e "$trail" ]]; then
            [[ -f "$trail" && -r "$trail" ]] || die 2 "$trail is not a readable regular file"
            size="$(file_size "$trail")"
            ident="$(file_identity "$trail")"
        fi
        printf '%s.size=%s\n%s.identity=%s\n' "$rel" "$size" "$rel" "$ident" >>"$state"
        if [[ "$size" == absent ]]; then
            printf 'snapshot (busy-host): %s absent\n' "$rel"
        else
            printf 'snapshot (busy-host): %s size %s bytes\n' "$rel" "$size"
        fi
    done
    printf 'snapshot (busy-host): %s not checked on a busy host (a live database is never opened)\n' "$DB_NAME"
}

verify_quiet() {
    local failed=0 f rel before after
    for f in "${watched[@]}"; do
        rel="$(rel_of "$f")"
        before="$(state_get "$rel")"
        after="$(checksum "$f")"
        printf '%-34s before %s\n%-34s after  %s\n' "$rel" "$before" "$rel" "$after"
        if [[ "$before" != "$after" ]]; then
            printf 'GUARD FAILED: %s changed during the run (before %s, after %s)\n' "$f" "$before" "$after" >&2
            failed=1
        fi
    done
    if [[ "$failed" -ne 0 ]]; then
        exit 1
    fi
    printf 'guard passed: every watched file identical before and after\n'
}

# load_ids fills the ids array from --ids-file files ("<kind> <value>" or
# "<value>" per line) and --id values, refusing short or empty ones.
load_ids() {
    local f line value
    for f in ${ids_files[@]+"${ids_files[@]}"}; do
        [[ -r "$f" ]] || die 2 "identifiers file $f is not readable"
        while IFS= read -r line || [[ -n "$line" ]]; do
            [[ -n "${line//[[:space:]]/}" ]] || continue
            value="${line##* }"
            ids+=("$value")
        done <"$f"
    done
    ids+=(${id_values[@]+"${id_values[@]}"})
    [[ "${#ids[@]}" -gt 0 ]] || die 2 "busy-host verify needs identifiers (--ids-file or --id): nothing to scan for"
    for value in "${ids[@]}"; do
        [[ "${#value}" -ge "$MIN_ID_LEN" ]] \
            || die 2 "identifier \"$value\" is shorter than $MIN_ID_LEN characters and would match unrelated bytes"
    done
}

# scan_appended greps bytes [offset, end) of the trail for the identifiers
# and prints each one found with its count; it never prints trail content.
# Returns 0 when nothing is found, 1 on a hit.
scan_appended() {
    local trail="$1" offset="$2" end="$3" args=() value hits
    for value in "${ids[@]}"; do
        args+=(-e "$value")
    done
    # pipefail off: head stops reading at end while tail may still be writing
    # bytes the trail gained during the scan (SIGPIPE is expected).
    hits="$(set +o pipefail
        tail -c +"$((offset + 1))" "$trail" | head -c "$((end - offset))" \
        | { grep -a -F -o "${args[@]}" || true; } | sort | uniq -c)"
    if [[ -n "$hits" ]]; then
        printf 'GUARD FAILED: the host trail %s gained this run'"'"'s identifiers:\n%s\n' "$trail" "$hits" >&2
        return 1
    fi
    return 0
}

# verify_busy_trail judges one directory's trail against the snapshot:
# returns 0 when nothing of the run is in its appended bytes, 1 otherwise
# (having printed why).
verify_busy_trail() {
    local trail="$1" rel="$2" before ident size now_ident
    before="$(state_get "$rel.size")"
    ident="$(state_get "$rel.identity")"
    if [[ ! -e "$trail" ]]; then
        if [[ "$before" == absent ]]; then
            printf '%s absent before and after: nothing appended\n' "$rel"
            return 0
        fi
        printf 'GUARD FAILED: %s was deleted or rotated during the run; the bytes written after the snapshot cannot be scanned\n' "$trail" >&2
        return 1
    fi
    [[ -f "$trail" && -r "$trail" ]] || die 2 "$trail is not a readable regular file"
    size="$(file_size "$trail")"
    now_ident="$(file_identity "$trail")"
    if [[ "$before" == absent ]]; then
        printf '%s created during the run: scanning all %s bytes\n' "$rel" "$size"
        scan_appended "$trail" 0 "$size" || return 1
        return 0
    fi
    if [[ "$now_ident" != "$ident" || "$size" -lt "$before" ]]; then
        printf 'GUARD FAILED: %s was truncated or rotated during the run (size %s -> %s, identity %s -> %s); bytes written to it after the snapshot cannot be proven clean\n' \
            "$trail" "$before" "$size" "$ident" "$now_ident" >&2
        scan_appended "$trail" 0 "$size" || true
        return 1
    fi
    printf '%s size before %s, after %s: scanning %s appended bytes for %s ids\n' \
        "$rel" "$before" "$size" "$((size - before))" "${#ids[@]}"
    scan_appended "$trail" "$before" "$size" || return 1
    return 0
}

verify_busy() {
    local d failed=0
    load_ids
    printf '%s not checked on a busy host (a live database is never opened)\n' "$DB_NAME"
    for d in "${store_dirs[@]}"; do
        verify_busy_trail "$d/$TRAIL_NAME" "$(rel_of "$d/$TRAIL_NAME")" || failed=1
    done
    if [[ "$failed" -ne 0 ]]; then
        exit 1
    fi
    printf 'guard passed (busy-host): %s ids, none found in the appended bytes of either trail\n' "${#ids[@]}"
    return 0
}

[[ $# -ge 1 ]] || die 2 "usage: guard.sh snapshot|verify --state <file> [options]"
op="$1"
shift
state=""
home_override=""
busy=0
settle="$DEFAULT_SETTLE"
ids_files=()
id_values=()
ids=()
while [[ $# -gt 0 ]]; do
    case "$1" in
        --state) [[ $# -ge 2 ]] || die 2 "--state needs a file"; state="$2"; shift 2 ;;
        --home) [[ $# -ge 2 ]] || die 2 "--home needs a directory"; home_override="$2"; shift 2 ;;
        --busy-host) busy=1; shift ;;
        --settle)
            [[ $# -ge 2 && "$2" =~ ^[0-9]+$ ]] || die 2 "--settle needs whole seconds"
            settle="$2"; shift 2 ;;
        --ids-file) [[ $# -ge 2 ]] || die 2 "--ids-file needs a file"; ids_files+=("$2"); shift 2 ;;
        --id) [[ $# -ge 2 ]] || die 2 "--id needs a value"; id_values+=("$2"); shift 2 ;;
        *) die 2 "unknown option $1" ;;
    esac
done
[[ -n "$state" ]] || die 2 "--state is required"

case "$op" in
    snapshot)
        [[ "${#ids_files[@]}" -eq 0 && "${#id_values[@]}" -eq 0 ]] || die 2 "--ids-file and --id belong to verify"
        [[ ! -e "$state" ]] || die 2 "state file $state already exists; use a fresh path"
        resolve_home
        check_state_path
        printf 'home checked: %s (%s)\n' "$home" "$home_label"
        wal_note
        if [[ "$busy" -eq 1 ]]; then
            snapshot_busy
        else
            snapshot_quiet
        fi
        ;;
    verify)
        [[ "$busy" -eq 0 ]] || die 2 "--busy-host is chosen at snapshot time"
        [[ -r "$state" ]] || die 2 "no snapshot: state file $state is missing or unreadable (run guard.sh snapshot first)"
        resolve_home
        recorded="$(state_get home)"
        [[ "$recorded" == "$home" ]] || die 2 "the snapshot checked $recorded but verify resolves $home"
        printf 'home checked: %s (%s)\n' "$home" "$home_label"
        wal_note
        case "$(state_get mode)" in
            quiet) verify_quiet ;;
            busy) verify_busy ;;
            *) die 2 "state file $state has an unknown mode" ;;
        esac
        ;;
    *) die 2 "unknown operation $op (want snapshot or verify)" ;;
esac
