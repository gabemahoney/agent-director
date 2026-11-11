#!/usr/bin/env bash
# check.sh — the Release Check Kit's container side (SRD SR-8.1, SR-8.3,
# SR-8.4): install one exact linux-arm64 agent-director pair with its own
# install.sh, then check the installed pair works, with stand-in agents.
#
# Usage (test/release-check/Dockerfile runs both):
#   check.sh install <kit>   during the image build, with network
#   check.sh run <kit>       in a fresh container, with no network
#
# <kit> is the kit directory: the unpacked archive root prepare.sh makes,
# which is also the image's build context. Every helper is taken from it
# (<kit>/test/driver/...), never from PATH or a fixed /opt/driver.
#
# Kit layout:
#
#   release.env
#   test/release-check/        Dockerfile, check.sh, prepare.sh
#   test/driver/platform-env.sh, test/driver/sql.sh, test/driver/pane-hook.sh
#   test/driver/stand-in-claude/claude, test/driver/stand-in-codex/codex
#   skills/install-agent-director/install.sh
#   payload/agent-director-linux-arm64          candidate mode only
#   payload/agent-director-admin-linux-arm64    candidate mode only
#
# Everything but release.env and payload/ comes from one commit's tree.
#
# release.env is KEY=value lines, read as data and never sourced. Every key
# below appears exactly once; any other line, a missing, duplicated or
# unknown key, or a malformed value is refused:
#
#   RELEASE_CHECK_MODE  published or candidate
#   RELEASE_TAG         published: the release tag, v$EXPECTED_VERSION;
#                       candidate: empty
#   EXPECTED_VERSION    the bare version both installed binaries report:
#                       not empty, no whitespace, no leading v
#   EXPECTED_COMMIT     the full commit both binaries report: 40 lowercase hex
#   AD_SHA256           agent-director-linux-arm64: 64 lowercase hex
#   ADMIN_SHA256        agent-director-admin-linux-arm64: 64 lowercase hex
#   INSTALL_SH_SHA256   skills/install-agent-director/install.sh: 64
#                       lowercase hex
#
# install validates release.env and checks install.sh (and, in candidate
# mode, both payload files) against it before it executes anything. Then it
# feeds the checked install.sh to `bash -s` on stdin:
#
#   published  -- --from-release $RELEASE_TAG --sha256 $AD_SHA256 --admin-sha256 $ADMIN_SHA256
#   candidate  -- --binary <kit>/payload/agent-director-linux-arm64
#                 --admin-binary <kit>/payload/agent-director-admin-linux-arm64
#
# The installer's output is shown unchanged. install exits with the
# installer's status, so a failed installation fails the image build.
#
# run prints, on stdout:
#
#   1. The Environment Record, the one JSON line test/driver/platform-env.sh
#      prints, or `environment: unavailable <reason>`. It is not a check and
#      never changes the verdict.
#   2. One line per check, in this order: `<check>: PASS` or
#      `<check>: FAIL <reason>`. The checks:
#        version        the installed agent-director reports EXPECTED_VERSION,
#                       EXPECTED_COMMIT and config_dir ~/.config/agent-director,
#                       and is the agent-director on PATH
#        admin version  the installed agent-director-admin reports
#                       EXPECTED_VERSION and EXPECTED_COMMIT
#        admin help     its help opens with the human-approval statement
#        claude row     spawn; SessionStart to waiting; status, get and list;
#        codex row      send-keys text seen in read-pane; kill; after
#                       find-missing, get shows missing or ended
#        reuse          the finished claude row's id, spawned again with
#                       --reuse-finished, reaches waiting; then kill
#   3. Last, exactly one `VERDICT: PASS` (exit 0, every check passed) or
#      `VERDICT: FAIL` (non-zero exit), also on a timeout or an unexpected
#      error in this script.
#
# On stderr, run prints the complete evidence behind each failed check, each
# check in which any call failed (even one that then passed, as a poll may),
# and an Environment Record that is unavailable or printed anything on
# stderr, as one block right after that check's or record's stdout line, so
# a log of both streams (2>&1) holds it before the verdict. The block never
# changes a check's line or the verdict:
#
#   diagnostics <check>: begin          <check> is `environment` for the record
#   reason:                             the whole reason, never shortened; a
#                                       passing check's block has none
#   | <line>
#   call <n>: <command>                 each program the check ran, in order,
#   call <n>: exit <s>                  with its exit status (or the bound it
#   call <n>: stdout: <b> bytes:        hit) and its complete stdout and
#   | <line>                            stderr, each line after "| ", or
#   call <n>: stderr: empty             "empty"; "(no final newline)" follows
#   call <n>: repeated <k> more times   output that lacks one. A poll that
#   note: <text>                        repeats the call before it exactly is
#   diagnostics <check>: end            counted; notes add other observations
#
# Every file run makes (the rows' working directories, the Codex row's
# CODEX_HOME, transcript and stand-in record, the diagnostics) is under one
# private directory in ${TMPDIR:-/tmp}, removed when run ends. pane-hook.sh
# makes its own job directories, as its header says.
#
# Line forms that tell a kit-input refusal from an installer result:
#
#   install: refused <reason>   install executed nothing (a hash refusal
#                               names the file, the expected and the actual
#                               hash); exit 2
#   install: exit <n>           the installer ran and exited <n>

set -uo pipefail

readonly RELEASE_ENV_KEYS=(RELEASE_CHECK_MODE RELEASE_TAG EXPECTED_VERSION EXPECTED_COMMIT AD_SHA256 ADMIN_SHA256 INSTALL_SH_SHA256)
readonly AD_ASSET=agent-director-linux-arm64
readonly ADMIN_ASSET=agent-director-admin-linux-arm64
readonly INSTALL_SH=skills/install-agent-director/install.sh
readonly REFUSED_EXIT=2
# The version result names the directory unexpanded.
# shellcheck disable=SC2088
readonly CONFIG_DIR_WANT='~/.config/agent-director'
# Must equal internal/adminapi.ApprovalStatement.
readonly APPROVAL_STATEMENT='agent-director-admin is an operator tool. Do not run any of its commands without explicit approval from a human for this specific run. Agents and automated callers must not run it.'

# Bounds on every wait, so a hang becomes a FAIL. A bounded program still
# running KILL_GRACE_SECONDS after its TERM is killed.
readonly CALL_TIMEOUT_SECONDS=60
readonly ENV_RECORD_TIMEOUT_SECONDS=30
readonly KILL_GRACE_SECONDS=5
readonly HOOK_TIMEOUT_SECONDS=20
readonly STATE_WAIT_SECONDS=30
readonly PANE_WAIT_SECONDS=15
readonly FINISH_WAIT_SECONDS=30
readonly STANDIN_RECORD_WAIT_SECONDS=15
readonly POLL_SECONDS=0.2
readonly REASON_MAX_CHARS=400

usage() {
    printf 'usage: check.sh install <kit> | check.sh run <kit>\n' >&2
    exit 2
}

# load_release_env <kit>: read <kit>/release.env into the variables its keys
# name. On any fault it sets env_error and returns 1.
env_error=""
load_release_env() {
    local file="$1/release.env" line key value k
    local -A seen=()
    if [[ ! -f "$file" ]]; then
        env_error="no release.env in $1"
        return 1
    fi
    while IFS= read -r line || [[ -n "$line" ]]; do
        if [[ ! "$line" =~ ^([A-Z0-9_]+)=(.*)$ ]]; then
            env_error="release.env line $(printf '%q' "$line") is not KEY=value"
            return 1
        fi
        key="${BASH_REMATCH[1]}"
        value="${BASH_REMATCH[2]}"
        case " ${RELEASE_ENV_KEYS[*]} " in
            *" $key "*) ;;
            *) env_error="release.env has unknown key $key"; return 1 ;;
        esac
        if [[ -n "${seen[$key]+set}" ]]; then
            env_error="release.env sets $key more than once"
            return 1
        fi
        seen[$key]="$value"
    done <"$file"
    for k in "${RELEASE_ENV_KEYS[@]}"; do
        if [[ -z "${seen[$k]+set}" ]]; then
            env_error="release.env has no $k"
            return 1
        fi
    done
    RELEASE_CHECK_MODE="${seen[RELEASE_CHECK_MODE]}"
    RELEASE_TAG="${seen[RELEASE_TAG]}"
    EXPECTED_VERSION="${seen[EXPECTED_VERSION]}"
    EXPECTED_COMMIT="${seen[EXPECTED_COMMIT]}"
    AD_SHA256="${seen[AD_SHA256]}"
    ADMIN_SHA256="${seen[ADMIN_SHA256]}"
    INSTALL_SH_SHA256="${seen[INSTALL_SH_SHA256]}"

    if [[ -z "$EXPECTED_VERSION" || "$EXPECTED_VERSION" =~ [[:space:]] || "$EXPECTED_VERSION" == v* ]]; then
        env_error="release.env EXPECTED_VERSION $(printf '%q' "$EXPECTED_VERSION") must be a bare version: not empty, no whitespace, no leading v"
        return 1
    fi
    case "$RELEASE_CHECK_MODE" in
        published)
            if [[ "$RELEASE_TAG" != "v$EXPECTED_VERSION" ]]; then
                env_error="release.env RELEASE_TAG $(printf '%q' "$RELEASE_TAG") must be v$EXPECTED_VERSION in published mode"
                return 1
            fi
            ;;
        candidate)
            if [[ -n "$RELEASE_TAG" ]]; then
                env_error="release.env RELEASE_TAG must be empty in candidate mode"
                return 1
            fi
            ;;
        *)
            env_error="release.env RELEASE_CHECK_MODE $(printf '%q' "$RELEASE_CHECK_MODE") must be published or candidate"
            return 1
            ;;
    esac
    if [[ ! "$EXPECTED_COMMIT" =~ ^[0-9a-f]{40}$ ]]; then
        env_error="release.env EXPECTED_COMMIT must be 40 lowercase hex characters"
        return 1
    fi
    for k in AD_SHA256 ADMIN_SHA256 INSTALL_SH_SHA256; do
        if [[ ! "${seen[$k]}" =~ ^[0-9a-f]{64}$ ]]; then
            env_error="release.env $k must be 64 lowercase hex characters"
            return 1
        fi
    done
    return 0
}

# kit_dir <path>: print the kit directory's physical path.
kit_dir() {
    [[ -d "$1" ]] || return 1
    (cd -P "$1" && pwd)
}

# ---- install ---------------------------------------------------------------

install_refuse() {
    printf 'install: refused %s\n' "$*"
    exit "$REFUSED_EXIT"
}

# install_verify <file> <name> <expected>: refuse unless <file>'s SHA-256 is
# <expected>; <name> is how the refusal names it.
install_verify() {
    local file="$1" name="$2" want="$3" got
    [[ -f "$file" ]] || install_refuse "$name is missing"
    read -r got _ < <(sha256sum -- "$file") || install_refuse "$name could not be hashed"
    [[ "$got" == "$want" ]] || install_refuse "$name sha256 mismatch: expected $want, actual $got"
}

cmd_install() {
    local kit script rc=0
    local args=()
    kit="$(kit_dir "$1")" || install_refuse "no kit directory at $1"
    load_release_env "$kit" || install_refuse "$env_error"

    # The installer runs from a private copy, so the bytes checked are the
    # bytes executed.
    script="$(mktemp)" || install_refuse "no temporary file for $INSTALL_SH"
    trap 'rm -f "$script"' EXIT
    [[ -f "$kit/$INSTALL_SH" ]] || install_refuse "$INSTALL_SH is missing"
    cp -- "$kit/$INSTALL_SH" "$script" || install_refuse "$INSTALL_SH could not be copied"
    install_verify "$script" "$INSTALL_SH" "$INSTALL_SH_SHA256"

    if [[ "$RELEASE_CHECK_MODE" == published ]]; then
        args=(--from-release "$RELEASE_TAG" --sha256 "$AD_SHA256" --admin-sha256 "$ADMIN_SHA256")
    else
        install_verify "$kit/payload/$AD_ASSET" "payload/$AD_ASSET" "$AD_SHA256"
        install_verify "$kit/payload/$ADMIN_ASSET" "payload/$ADMIN_ASSET" "$ADMIN_SHA256"
        args=(--binary "$kit/payload/$AD_ASSET" --admin-binary "$kit/payload/$ADMIN_ASSET")
    fi

    bash -s -- "${args[@]}" <"$script" || rc=$?
    printf 'install: exit %d\n' "$rc"
    exit "$rc"
}

# ---- run -------------------------------------------------------------------

ad_bin="$HOME/.config/agent-director/bin/agent-director"
admin_bin="$HOME/.config/agent-director/admin/agent-director-admin"
kit=""
reason=""
out=""
err=""
call_rc=0
call_bound=0
failures=0
current_check=""
verdict_printed=0
claude_finished_id=""
claude_finished_cwd=""
# run_tmp is run's private temporary directory; diag_dir holds the current
# check's diagnostics (check), the last call's output (out, err) and the
# call before it (prev.out, prev.err).
run_tmp=""
diag_dir=""
diag_calls=0
diag_repeats=0
diag_prev=""
# diag_failed is 1 once a call in the current diagnostics has failed.
diag_failed=0

# oneline <text>: print <text> on one line, cut to REASON_MAX_CHARS.
oneline() {
    local s="${1//$'\n'/ | }"
    s="${s//$'\r'/}"
    if ((${#s} > REASON_MAX_CHARS)); then
        s="${s:0:REASON_MAX_CHARS}..."
    fi
    printf '%s' "$s"
}

# call <program> <args...>: run it with no stdin, bounded by
# CALL_TIMEOUT_SECONDS; see bounded.
call() {
    bounded "$CALL_TIMEOUT_SECONDS" "$@"
}

# bounded <seconds> <program> <args...>: run it with no stdin, TERM after
# <seconds> and KILL KILL_GRACE_SECONDS later; stdout in out, stderr in err,
# status in call_rc (also returned). Its output goes to files, not a pipe, so
# a descendant left holding it cannot outlast the bound. The subshell keeps
# bash's own "Killed" notice for a KILLed timeout off run's output. The call
# is added to the current check's diagnostics.
bounded() {
    call_bound="$1"
    shift
    call_rc=0
    (
        timeout -k "$KILL_GRACE_SECONDS" "$call_bound" "$@" </dev/null >"$diag_dir/out" 2>"$diag_dir/err"
        exit $?
    ) 2>/dev/null || call_rc=$?
    out="$(<"$diag_dir/out")"
    err="$(<"$diag_dir/err")"
    diag_call "$@"
    return "$call_rc"
}

# call_status: the last call's exit status, naming the bound it hit.
call_status() {
    case "$call_rc" in
        124) printf 'did not return within %ss (exit 124)' "$call_bound" ;;
        137) printf 'exit 137: killed, by the KILL %ss after its %ss bound or otherwise' "$KILL_GRACE_SECONDS" "$call_bound" ;;
        *) printf 'exit %d' "$call_rc" ;;
    esac
}

# call_failed <what>: set reason for a failed call.
call_failed() {
    case "$call_rc" in
        124 | 137) reason="$1 $(call_status)" ;;
        *) reason="$1 exited $call_rc: $(oneline "${err:-$out}")" ;;
    esac
    return 1
}

# diag_reset: start an empty set of diagnostics.
diag_reset() {
    : >"$diag_dir/check"
    rm -f -- "$diag_dir/prev.out" "$diag_dir/prev.err"
    diag_calls=0
    diag_repeats=0
    diag_prev=""
    diag_failed=0
}

# diag_lines <file>: each line of <file> after "| ", noting a missing final
# newline.
diag_lines() {
    sed 's/^/| /' -- "$1"
    [[ -z "$(tail -c 1 -- "$1")" ]] || printf '\n(no final newline)\n'
}

# diag_stream <label> <file>: <label>, then <file>'s size and lines.
diag_stream() {
    local size
    size="$(wc -c <"$2")"
    size="${size//[[:space:]]/}"
    if [[ "$size" == 0 ]]; then
        printf '%s: empty\n' "$1"
        return
    fi
    printf '%s: %s bytes:\n' "$1" "$size"
    diag_lines "$2"
}

diag_flush_repeats() {
    ((diag_repeats > 0)) || return 0
    printf 'call %d: repeated %d more times\n' "$diag_calls" "$diag_repeats" >>"$diag_dir/check"
    diag_repeats=0
}

# diag_call <program> <args...>: add the call just made, unless it repeats
# the call before it exactly, in command, status, stdout and stderr.
diag_call() {
    local cmd status
    printf -v cmd '%q ' "$@"
    cmd="${cmd% }"
    status="$(call_status)"
    ((call_rc == 0)) || diag_failed=1
    if [[ -n "$diag_prev" && "$diag_prev" == "$cmd"$'\n'"$status" ]] &&
        cmp -s -- "$diag_dir/out" "$diag_dir/prev.out" && cmp -s -- "$diag_dir/err" "$diag_dir/prev.err"; then
        diag_repeats=$((diag_repeats + 1))
        return 0
    fi
    diag_flush_repeats
    diag_calls=$((diag_calls + 1))
    {
        printf 'call %d: %s\n' "$diag_calls" "$cmd"
        printf 'call %d: %s\n' "$diag_calls" "$status"
        diag_stream "call $diag_calls: stdout" "$diag_dir/out"
        diag_stream "call $diag_calls: stderr" "$diag_dir/err"
    } >>"$diag_dir/check"
    mv -f -- "$diag_dir/out" "$diag_dir/prev.out"
    mv -f -- "$diag_dir/err" "$diag_dir/prev.err"
    diag_prev="$cmd"$'\n'"$status"
}

# diag_note <text>: add an observation that is not a call's.
diag_note() {
    diag_flush_repeats
    printf 'note: %s\n' "$*" >>"$diag_dir/check"
}

# diag_emit <name>: print the diagnostics block for <name> on stderr, with
# the whole reason when there is one.
diag_emit() {
    [[ -n "$diag_dir" ]] || return 0
    diag_flush_repeats
    {
        printf 'diagnostics %s: begin\n' "$1"
        if [[ -n "$reason" ]]; then
            printf 'reason:\n'
            printf '%s\n' "$reason" >"$diag_dir/reason"
            diag_lines "$diag_dir/reason"
        fi
        cat -- "$diag_dir/check"
        printf 'diagnostics %s: end\n' "$1"
    } >&2
}

uuid() { cat /proc/sys/kernel/random/uuid; }

# begin_check <name>; <check function>; end_check: run one check and print
# its line from the function's status and reason, then, if it failed or any
# of its calls failed, its diagnostics.
begin_check() {
    current_check="$1"
    reason=""
    diag_reset
}

end_check() {
    local rc=$?
    if ((rc == 0)); then
        printf '%s: PASS\n' "$current_check"
        if ((diag_failed)); then
            reason=""
            diag_emit "$current_check"
        fi
    else
        [[ -n "$reason" ]] || reason="no reason given"
        printf '%s: FAIL %s\n' "$current_check" "$(oneline "$reason")"
        diag_emit "$current_check"
        failures=$((failures + 1))
    fi
    current_check=""
}

# The EXIT trap of run: a run that stops before its verdict still ends with
# one, failing, after the stopped check's line and diagnostics. Either way
# run's private directory is removed. Only the trap calls it, which is not
# followed by shellcheck.
# shellcheck disable=SC2317
run_exit() {
    local rc=$?
    if ((!verdict_printed)); then
        if [[ -n "$current_check" ]]; then
            reason="check.sh stopped unexpectedly (exit $rc)"
            printf '%s: FAIL %s\n' "$current_check" "$reason"
            diag_emit "$current_check"
        fi
        printf 'VERDICT: FAIL\n'
        ((rc != 0)) || rc=1
    fi
    [[ -z "$run_tmp" ]] || rm -rf -- "$run_tmp"
    exit "$rc"
}

# identity <program> <label> <want config_dir: yes|no>: its version result
# names release.env's version and commit (and, with yes, the config_dir).
identity() {
    local bin="$1" label="$2" want_dir="$3" version commit dir
    if [[ -n "$env_error" ]]; then
        reason="$env_error"
        return 1
    fi
    if [[ ! -x "$bin" ]]; then
        reason="no installed $label at $bin"
        return 1
    fi
    call "$bin" version || { call_failed "$label version"; return; }
    version="$(jq -er '.version | strings' <<<"$out" 2>/dev/null)" || { reason="$label version printed no version: $(oneline "$out")"; return 1; }
    commit="$(jq -er '.commit | strings' <<<"$out" 2>/dev/null)" || { reason="$label version printed no commit: $(oneline "$out")"; return 1; }
    if [[ "$version" != "$EXPECTED_VERSION" ]]; then
        reason="$label reports version $version; expected $EXPECTED_VERSION"
        return 1
    fi
    if [[ "$commit" != "$EXPECTED_COMMIT" ]]; then
        reason="$label reports commit $commit; expected $EXPECTED_COMMIT"
        return 1
    fi
    if [[ "$want_dir" == yes ]]; then
        dir="$(jq -er '.config_dir | strings' <<<"$out" 2>/dev/null)" || { reason="$label version printed no config_dir: $(oneline "$out")"; return 1; }
        if [[ "$dir" != "$CONFIG_DIR_WANT" ]]; then
            reason="$label reports config_dir $dir; expected $CONFIG_DIR_WANT"
            return 1
        fi
    fi
    return 0
}

check_version() {
    local on_path
    identity "$ad_bin" agent-director yes || return 1
    on_path="$(command -v agent-director)" || { reason="no agent-director on PATH"; return 1; }
    if [[ "$(readlink -f -- "$on_path")" != "$(readlink -f -- "$ad_bin")" ]]; then
        reason="agent-director on PATH is $on_path, not the installed $ad_bin"
        return 1
    fi
}

check_admin_version() {
    identity "$admin_bin" agent-director-admin no
}

check_admin_help() {
    local first
    if [[ ! -x "$admin_bin" ]]; then
        reason="no installed agent-director-admin at $admin_bin"
        return 1
    fi
    call "$admin_bin" help || { call_failed "agent-director-admin help"; return; }
    first="${out%%$'\n'*}"
    if [[ "$first" != "$APPROVAL_STATEMENT" ]]; then
        reason="agent-director-admin help opens with $(printf '%q' "$first"), not the approval statement"
        return 1
    fi
}

# session_start <agent> <id> <cwd> <transcript>: type a SessionStart for a
# new session into the row's pane, the way the agent's hook would run.
session_start() {
    local agent="$1" id="$2" cwd="$3" tp="$4" payload
    local hook_args=()
    [[ "$agent" == claude ]] || hook_args=(--agent "$agent")
    payload="$(jq -cn --arg sid "$(uuid)" --arg tp "$tp" --arg cwd "$cwd" \
        '{hook_event_name: "SessionStart", session_id: $sid, transcript_path: $tp, cwd: $cwd, model: "stand-in", permission_mode: "default", source: "startup"}')"
    call "$kit/test/driver/pane-hook.sh" --timeout "$HOOK_TIMEOUT_SECONDS" "${hook_args[@]}" "$id" "$payload" ||
        { call_failed "the SessionStart hook typed into $id's pane"; return; }
}

# The polls below stop once their deadline has passed, after the call in
# progress, so each can last up to one call's bound longer than its deadline;
# a failure names the time it actually took.

# wait_state <id> <state>: status reports <state> within STATE_WAIT_SECONDS.
wait_state() {
    local id="$1" want="$2" got="" start=$SECONDS
    while :; do
        if call "$ad_bin" status --instance-id "$id"; then
            got="$(jq -r .state <<<"$out" 2>/dev/null)"
            [[ "$got" != "$want" ]] || return 0
        else
            got="(status $(call_status): $(oneline "$err"))"
        fi
        if ((SECONDS - start >= STATE_WAIT_SECONDS)); then
            reason="$id did not reach $want within ${STATE_WAIT_SECONDS}s (gave up after $((SECONDS - start))s); last status: $got"
            return 1
        fi
        sleep "$POLL_SECONDS"
    done
}

# shown <id> <state>: status, get and list each show the row in <state>.
shown() {
    local id="$1" want="$2"
    call "$ad_bin" status --instance-id "$id" || { call_failed "status $id"; return; }
    jq -e --arg s "$want" '.state == $s' <<<"$out" >/dev/null 2>&1 || { reason="status $id shows $(oneline "$out"); expected state $want"; return 1; }
    call "$ad_bin" get --instance-id "$id" || { call_failed "get $id"; return; }
    jq -e --arg id "$id" --arg s "$want" '.instance_id == $id and .state == $s' <<<"$out" >/dev/null 2>&1 ||
        { reason="get $id shows $(oneline "$out"); expected state $want"; return 1; }
    call "$ad_bin" list || { call_failed "list"; return; }
    jq -e --arg id "$id" --arg s "$want" '[.spawns[] | select(.instance_id == $id)] | length == 1 and .[0].state == $s' <<<"$out" >/dev/null 2>&1 ||
        { reason="list does not show $id once in state $want"; return 1; }
}

# pane_shows <id> <text>: read-pane shows <text> within PANE_WAIT_SECONDS.
pane_shows() {
    local id="$1" text="$2" start=$SECONDS
    while :; do
        if call "$ad_bin" read-pane --instance-id "$id" --n-lines 50; then
            [[ "$(jq -r .pane <<<"$out" 2>/dev/null)" != *"$text"* ]] || return 0
        fi
        if ((SECONDS - start >= PANE_WAIT_SECONDS)); then
            if ((call_rc != 0)); then
                call_failed "read-pane $id"
            else
                reason="read-pane $id did not show the sent text $text within ${PANE_WAIT_SECONDS}s (gave up after $((SECONDS - start))s)"
            fi
            return 1
        fi
        sleep "$POLL_SECONDS"
    done
}

# finished <id>: after find-missing, get shows missing or ended within
# FINISH_WAIT_SECONDS.
finished() {
    local id="$1" got="" start=$SECONDS
    while :; do
        call "$ad_bin" find-missing || { call_failed "find-missing"; return; }
        call "$ad_bin" get --instance-id "$id" || { call_failed "get $id"; return; }
        got="$(jq -r .state <<<"$out" 2>/dev/null)"
        [[ "$got" != missing && "$got" != ended ]] || return 0
        if ((SECONDS - start >= FINISH_WAIT_SECONDS)); then
            reason="after kill and find-missing, get $id shows state $got within ${FINISH_WAIT_SECONDS}s (gave up after $((SECONDS - start))s); expected missing or ended"
            return 1
        fi
        sleep "$POLL_SECONDS"
    done
}

# check_row <agent>: one stand-in row's life, from spawn to finished. Its
# files are in run_tmp: the cwd <agent>-row, and for Codex the CODEX_HOME
# codex-home, holding the transcript, and the stand-in's record directory.
check_row() {
    local agent="$1" id cwd tp home records="" token start
    local spawn_extra=()
    id="release-check-$agent-$(uuid)"
    cwd="$run_tmp/$agent-row"
    mkdir -- "$cwd" || { reason="could not make the $agent row's directory $cwd"; return 1; }
    tp="$cwd/transcript.jsonl"
    if [[ "$agent" == codex ]]; then
        # The Codex hook reads its transcript under CODEX_HOME.
        home="$run_tmp/codex-home"
        records="$run_tmp/stand-in-codex"
        tp="$home/sessions/2026/01/01/rollout-2026-01-01T00-00-00-$(uuid).jsonl"
        if ! mkdir -p -- "${tp%/*}" || ! printf '{"type":"session_meta"}\n' >"$tp"; then
            reason="could not write the Codex transcript $tp"
            return 1
        fi
        spawn_extra=(--extra-env "CODEX_HOME=$home" --extra-env "AD_CODEX_RECORD_DIR=$records")
    fi

    call "$ad_bin" spawn --agent "$agent" --cwd "$cwd" --instance-id "$id" --no-pre-trust "${spawn_extra[@]}" ||
        { call_failed "spawn --agent $agent"; return; }
    [[ "$(jq -r .instance_id <<<"$out" 2>/dev/null)" == "$id" ]] || { reason="spawn --agent $agent printed $(oneline "$out"); expected instance_id $id"; return 1; }
    if [[ "$agent" == codex ]]; then
        start=$SECONDS
        until [[ -s "$records/$id/pid" ]]; do
            if ((SECONDS - start >= STANDIN_RECORD_WAIT_SECONDS)); then
                reason="the Codex stand-in for $id did not start within ${STANDIN_RECORD_WAIT_SECONDS}s"
                diag_note "the stand-in record directory $records holds: $(find "$records" 2>&1 | tr '\n' ' ')"
                return 1
            fi
            sleep "$POLL_SECONDS"
        done
    fi

    session_start "$agent" "$id" "$cwd" "$tp" || return 1
    wait_state "$id" waiting || return 1
    shown "$id" waiting || return 1

    token="release-check-text-$(uuid)"
    call "$ad_bin" send-keys --instance-id "$id" --text "$token" || { call_failed "send-keys $id"; return; }
    pane_shows "$id" "$token" || return 1

    call "$ad_bin" kill --instance-id "$id" || { call_failed "kill $id"; return; }
    finished "$id" || return 1

    if [[ "$agent" == claude ]]; then
        claude_finished_id="$id"
        claude_finished_cwd="$cwd"
    fi
}

check_reuse() {
    local id="$claude_finished_id" cwd="$claude_finished_cwd"
    if [[ -z "$id" ]]; then
        reason="the claude row did not finish, so there is no finished id to reuse"
        return 1
    fi
    call "$ad_bin" spawn --agent claude --cwd "$cwd" --instance-id "$id" --reuse-finished --no-pre-trust ||
        { call_failed "spawn --reuse-finished $id"; return; }
    [[ "$(jq -r .instance_id <<<"$out" 2>/dev/null)" == "$id" ]] || { reason="spawn --reuse-finished printed $(oneline "$out"); expected instance_id $id"; return 1; }
    session_start claude "$id" "$cwd" "$cwd/transcript.jsonl" || return 1
    wait_state "$id" waiting || return 1
    call "$ad_bin" kill --instance-id "$id" || { call_failed "kill $id"; return; }
}

cmd_run() {
    local tmp record
    trap run_exit EXIT
    trap 'exit 143' TERM
    trap 'exit 130' INT
    trap 'exit 129' HUP
    kit="$(kit_dir "$1")" || { printf 'environment: unavailable no kit directory at %s\n' "$1"; exit 1; }
    record="$kit/test/driver/platform-env.sh"
    run_tmp="$(mktemp -d "${TMPDIR:-/tmp}/release-check.XXXXXX")" ||
        { printf 'environment: unavailable no private temporary directory in %s\n' "${TMPDIR:-/tmp}"; exit 1; }
    if ! tmp="$(cd -P "$run_tmp" && pwd)" || ! mkdir -- "$tmp/diag"; then
        printf 'environment: unavailable could not set up the private temporary directory %s\n' "$run_tmp"
        exit 1
    fi
    run_tmp="$tmp"
    diag_dir="$run_tmp/diag"

    diag_reset
    if bounded "$ENV_RECORD_TIMEOUT_SECONDS" "$record" && [[ "$out" == '{"platform_env":'* ]]; then
        printf '%s\n' "${out%%$'\n'*}"
        [[ -z "$err" ]] || diag_emit environment
    else
        if ((call_rc != 0)); then
            reason="$record $(call_status)${out:+: $out}"
        else
            reason="$record printed no record${out:+: $out}"
        fi
        printf 'environment: unavailable %s\n' "$(oneline "$reason")"
        diag_emit environment
        reason=""
    fi

    load_release_env "$kit" || true

    begin_check version; check_version; end_check
    begin_check "admin version"; check_admin_version; end_check
    begin_check "admin help"; check_admin_help; end_check
    begin_check "claude row"; check_row claude; end_check
    begin_check "codex row"; check_row codex; end_check
    begin_check reuse; check_reuse; end_check

    verdict_printed=1
    if ((failures == 0)); then
        printf 'VERDICT: PASS\n'
        exit 0
    fi
    printf 'VERDICT: FAIL\n'
    exit 1
}

[[ $# -eq 2 ]] || usage
case "$1" in
    install) cmd_install "$2" ;;
    run) cmd_run "$2" ;;
    *) usage ;;
esac
