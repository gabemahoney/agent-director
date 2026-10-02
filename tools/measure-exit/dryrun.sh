#!/usr/bin/env bash
# dryrun.sh — measure-exit's offline dry run (Epic 21, t3.h98.w4.yu.k8).
# It runs ONLY inside the repo's Docker sandbox (make measure-exit-dryrun):
# no credential, no network call, no real Claude Code, no operator.
#
#   1. builds the v-next agent-director and the driver into a throwaway dir;
#   2. makes a fixture home holding a stand-in .agent-director/state.db and
#      ad-trail.jsonl, and takes guard.sh snapshots of it (quiet-host and
#      busy-host modes, --home override);
#   3. runs the driver in dry mode with the stub claude first on PATH, a
#      throwaway HOME and a private TMUX_TMPDIR: every RN-6 and RN-2 case
#      (raised budgets and the MCP path included), every RN-9 scenario with
#      RN-7's record, and the version probe; then the probe again with the
#      args-keeping and the args-dropping stubs; and once with a non-stub
#      `claude` first on PATH, which must be refused before anything runs;
#   4. checks the results (dry-run banner, every sample completed, every RN-9
#      scenario passed, the probe's two results, the run log's action kinds)
#      and that decide refuses a dry run;
#   4b. runs the host runner's real L0 path (run.sh probe --run) against a
#      fake container engine that runs the driver in probe mode, with a
#      throwaway HOME and per-version probe stubs (args kept from a set
#      version up, dropped below it), then runs decide with one -in per
#      probed version directory, exactly as an operator would, and checks
#      that decide accepts every L0 input (probe mode, finished, guard pass)
#      and decides the minimum; a version directory without the runner's
#      guard-status.txt must be refused;
#   5. runs guard.sh verify in both modes (identical checksums; none of the
#      runs' identifiers in the fixture trail);
#   6. runs the host runner's print-only mode for measure, rn9 and probe, and
#      fails on a forbidden mount, a forwarded TMUX, a credential value, an
#      ANTHROPIC_API_KEY forward, a -cases list other than the mode's ids or
#      an npm version listing off the host network; it must refuse another
#      mode's case in --cases and a staged layer that carries credentials;
#   7. checks the sandbox user's own home has no .agent-director and the
#      default tmux socket is as it was.
#
# Usage: dryrun.sh [--out DIR] [--keep]
#   --out DIR  put the runs' results directories in DIR (kept)
#   --keep     keep the throwaway directory
# Exit status: 0 when every check passed, 1 otherwise, 2 on bad usage.

set -uo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
readonly REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd -P)"
readonly GUARD="$SCRIPT_DIR/guard.sh"
readonly RUNNER="$SCRIPT_DIR/run.sh"
readonly STUB_DIR="$SCRIPT_DIR/stub"

fail_count=0
fail() {
    printf 'DRY RUN CHECK FAILED: %s\n' "$*" >&2
    fail_count=$((fail_count + 1))
}
ok() { printf 'ok: %s\n' "$*"; }

out_root=""
keep=0
while [[ $# -gt 0 ]]; do
    case "$1" in
        --out) [[ $# -ge 2 ]] || { echo "--out needs a directory" >&2; exit 2; }; out_root="$2"; shift 2 ;;
        --keep) keep=1; shift ;;
        *) echo "unknown option $1" >&2; exit 2 ;;
    esac
done

if [[ -z "${AGENT_DIRECTOR_TEST_SANDBOX:-}" ]]; then
    echo "dryrun.sh runs only inside the repo sandbox (make measure-exit-dryrun)" >&2
    exit 2
fi
for tool in go tmux jq; do
    command -v "$tool" >/dev/null 2>&1 || { echo "dryrun.sh needs $tool" >&2; exit 2; }
done
unset TMUX TMUX_PANE

tmp="$(mktemp -d /tmp/mx-dry.XXXXXX)"
[[ -n "$out_root" ]] || out_root="$tmp/out"
mkdir -p "$out_root"
sockets=()
cleanup() {
    local s
    for s in ${sockets[@]+"${sockets[@]}"}; do
        tmux -S "$s" kill-server >/dev/null 2>&1 || true
    done
    [[ "$keep" -eq 1 ]] || rm -rf -- "$tmp"
}
trap cleanup EXIT

uid="$(id -u)"
passwd_home="$(getent passwd "$uid" | cut -d: -f6)"
default_socket="${TMUX_TMPDIR:-/tmp}/tmux-$uid/default"
socket_before=absent
[[ -S "$default_socket" ]] && socket_before=present

# ---- 1. build ------------------------------------------------------------------
bin="$tmp/bin"
mkdir -p "$bin"
(cd "$REPO_ROOT" && go build -o "$bin/agent-director" ./cmd/agent-director && go build -o "$bin/measure-exit" ./tools/measure-exit) \
    || { echo "build failed" >&2; exit 1; }
ok "built agent-director and measure-exit into $bin"

# ---- 2. fixture home and guard snapshots -----------------------------------------
fixture="$tmp/fixture-home"
mkdir -p "$fixture/.agent-director"
printf 'stand-in state.db (dry run)\n' >"$fixture/.agent-director/state.db"
printf '{"event":"stand-in","ts":"2026-10-01T00:00:00.000Z"}\n' >"$fixture/.agent-director/ad-trail.jsonl"
"$GUARD" snapshot --state "$tmp/guard-quiet.state" --home "$fixture" --settle 1 || fail "guard quiet-host snapshot"
"$GUARD" snapshot --state "$tmp/guard-busy.state" --home "$fixture" --busy-host || fail "guard busy-host snapshot"

# ---- 3. driver runs ----------------------------------------------------------------
# run_driver NAME STUB_PATH_DIR [driver args...]: one dry run with its own
# HOME; prints the driver's exit status in $run_rc.
run_rc=0
run_driver() {
    local name="$1" stubdir="$2" home="$tmp/$1/home" out="$out_root/$1" s
    shift 2
    mkdir -p "$home"
    run_rc=0
    (cd "$tmp" && env -u TMUX HOME="$home" PATH="$stubdir:$bin:$PATH" \
        "$bin/measure-exit" run -mode dry -out "$out" -agent-director "$bin/agent-director" \
        -tmux-base "$tmp" -workdir "$home/work" "$@") || run_rc=$?
    if [[ -r "$out/harness-ids.txt" ]]; then
        while read -r kind s; do
            [[ "$kind" == socket ]] && sockets+=("$s")
        done <"$out/harness-ids.txt"
    fi
}

run_driver full "$STUB_DIR"
[[ "$run_rc" -eq 0 ]] && ok "full dry run exited 0" || fail "full dry run exited $run_rc"
run_driver probe-kept "$STUB_DIR/probe-args-kept" -cases probe.exec-form
[[ "$run_rc" -eq 0 ]] || fail "probe dry run (args kept) exited $run_rc"
run_driver probe-dropped "$STUB_DIR/probe-args-dropped" -cases probe.exec-form
[[ "$run_rc" -eq 0 ]] || fail "probe dry run (args dropped) exited $run_rc"

fake="$tmp/fake-real-claude"
mkdir -p "$fake"
printf '#!/bin/sh\necho "2.1.285 (Claude Code)"\n' >"$fake/claude"
chmod 0755 "$fake/claude"
run_driver refused "$fake"
if [[ "$run_rc" -eq 4 ]] && ! grep -q '"kind":"spawn"' "$out_root/refused/run-log.jsonl" 2>/dev/null; then
    ok "dry mode refused a non-stub claude (exit 4) before any spawn"
else
    fail "dry mode with a non-stub claude exited $run_rc (want 4, no spawn)"
fi

# ---- 4. results ----------------------------------------------------------------------
full="$out_root/full"
res="$full/results.json"
if [[ -r "$res" ]]; then
    jq -e '.dry_run == true and (.banner | test("DRY RUN"))' "$res" >/dev/null || fail "results.json lacks the dry-run banner"
    grep -q 'DRY RUN' "$full/results-table.txt" || fail "the table lacks the dry-run banner"
    jq -e '.isolation as $i | ($i.recorded_socket | type) == "string" and ($i.tmux_tmpdir | type) == "string"
        and ($i.tmux_tmpdir != "") and ($i.recorded_socket | startswith($i.tmux_tmpdir + "/"))' "$res" >/dev/null \
        || fail "results.json: isolation.recorded_socket is missing or outside the private TMUX_TMPDIR"
    for id in rn6.idle rn6.midturn rn6.mcp rn6.idle.raised-hook rn6.midturn.raised-hook rn6.mcp.raised-hook \
        rn6.idle.raised-env rn6.midturn.raised-env rn6.mcp.raised-env rn2.natural rn2.pause rn2.mcp; do
        jq -e --arg id "$id" '.cases[] | select(.id == $id) | .attempted > 0 and .completed == .attempted' "$res" >/dev/null \
            && ok "case $id: every sample completed" \
            || fail "case $id: missing or not every sample completed: $(jq -c --arg id "$id" '.cases[] | select(.id == $id) | [.attempted, .completed, .did_not_exit, .no_ended_at, .failed, ([.samples[].reason] | unique)]' "$res")"
    done
    for id in rn9.drive rn9.resume rn9.team-inprocess rn9.team-splitpane; do
        jq -e --arg id "$id" '.rn9.scenarios[] | select(.id == $id) | .verdict == "pass"' "$res" >/dev/null \
            && ok "RN-9 $id: pass" \
            || fail "RN-9 $id: $(jq -c --arg id "$id" '.rn9.scenarios[] | select(.id == $id) | [.verdict, .reason, .stop]' "$res")"
    done
    jq -e '[.rn9.scenarios[] | select(.id == "rn9.team-inprocess") | .hooks[] | select(.agent_id != null and .event == "SessionStart") | .reason] | index("subagent_event") != null' "$res" >/dev/null \
        || fail "the in-process teammate's SessionStart was not ignored as subagent_event"
    jq -e '[.rn9.scenarios[] | select(.id == "rn9.team-splitpane") | .hooks[] | select(.pid_match == false) | .reason] | index("pid_mismatch") != null' "$res" >/dev/null \
        || fail "no split-pane teammate hook was ignored as pid_mismatch"
    jq -e '.rn7_record.rows | length > 0' "$res" >/dev/null && ok "RN-7 record present" || fail "RN-7 record missing"
    jq -e '.probe.versions[0].result == "args_received"' "$res" >/dev/null || fail "full run: the probe against the main stub did not receive args"
    bad_kinds="$(jq -r 'select(.kind != "version" and .kind != "read" and .kind != "spawn" and .kind != "measured" and .kind != "drive" and .kind != "teardown") | .kind' "$full/run-log.jsonl" | sort -u)"
    [[ -z "$bad_kinds" ]] || fail "run log has unexpected action kinds: $bad_kinds"
    bad_drive="$(jq -r 'select(.kind == "drive") | .argv[1]' "$full/run-log.jsonl" | sort -u | grep -vxE 'send-keys|decide|pause' || true)"
    [[ -z "$bad_drive" ]] || fail "run log has drive actions other than send-keys, decide and pause: $bad_drive"
    rn9_measured="$(jq -r 'select((.case | startswith("rn9.")) and (.kind == "measured" or .kind == "teardown")) | .case' "$full/run-log.jsonl")"
    [[ -z "$rn9_measured" ]] || fail "an RN-9 scenario made a measured or teardown action"
    jq -se '[.[] | select((.case | startswith("rn6.")) and .kind == "measured") | .argv | index("kill-pane") != null] | length > 0 and all' \
        "$full/run-log.jsonl" >/dev/null \
        || fail "no RN-6 kill-pane in the run log"
    printf -- '--- dry-run results table ---\n'
    cat "$full/results-table.txt"
else
    fail "no results.json from the full dry run"
fi
jq -e '.probe.versions[0].result == "args_received"' "$out_root/probe-kept/results.json" >/dev/null \
    && ok "probe with the args-keeping stub: args_received" || fail "probe with the args-keeping stub"
jq -e '.probe.versions[0].result == "args_not_received"' "$out_root/probe-dropped/results.json" >/dev/null \
    && ok "probe with the args-dropping stub: args_not_received" || fail "probe with the args-dropping stub"
printf 'pass\n' >"$full/guard-status.txt"
decide_rc=0
"$bin/measure-exit" decide -in "$full" -in "$out_root/probe-kept" >"$tmp/decide.md" 2>&1 || decide_rc=$?
if [[ "$decide_rc" -eq 2 ]] && grep -q 'dry run' "$tmp/decide.md"; then
    ok "decide refuses a dry run (exit 2)"
else
    fail "decide on a dry run exited $decide_rc (want 2)"
fi
rm -f "$full/guard-status.txt"

# ---- 4b. the runner's L0 layout, composed with decide ---------------------------------
# The fake engine stands in for the container engine run.sh calls: `build`
# does nothing; `run` checks the probe container's flags (--network none, no
# credential forwarded by name), then runs the driver the image would run,
# with the container's -e values in an otherwise empty environment, the
# results mount's host directory as -out and a per-version probe stub first
# on PATH: args kept from MX_FAKE_PROBE_MIN up, dropped below it.
fake_engine="$tmp/fake-engine"
cat >"$fake_engine" <<'FAKE_ENGINE'
#!/usr/bin/env bash
set -u
printf '%s\n' "$*" >>"$MX_FAKE_LOG"
[[ "${1:-}" == run ]] || exit 0
shift
out="" ver="" prev="" net="" envs=() drv=() in_cmd=0
for a in "$@"; do
    if ((in_cmd)); then drv+=("$a"); continue; fi
    case "$prev" in
        -e) [[ "$a" == *=* ]] || { echo "fake engine: probe forwards $a by name" >&2; exit 125; }; envs+=("$a") ;;
        -v) [[ "$a" == *:/results ]] && out="${a%:/results}" ;;
        --network) net="$a" ;;
        *) [[ "$a" == agent-director-measure:probe-* ]] && { ver="${a#agent-director-measure:probe-}"; in_cmd=1; } ;;
    esac
    prev="$a"
done
[[ "$net" == none ]] || { echo "fake engine: probe container without --network none" >&2; exit 125; }
[[ -n "$out" && -n "$ver" && "${drv[0]:-}" == measure-exit && "${drv[1]:-}" == run ]] \
    || { echo "fake engine: unexpected probe container: $*" >&2; exit 125; }
for i in "${!drv[@]}"; do
    [[ "${drv[i]}" == /results && "${drv[i - 1]}" == -out ]] && drv[i]="$out"
done
how=dropped stub_mode=drop
if [[ "$(printf '%s\n%s\n' "$MX_FAKE_PROBE_MIN" "$ver" | sort -V | head -n1)" == "$MX_FAKE_PROBE_MIN" ]]; then
    how=kept stub_mode=keep
fi
stub="$MX_FAKE_ROOT/stub-$ver"
home="$MX_FAKE_ROOT/home-$ver"
mkdir -p "$stub" "$home"
printf '#!/bin/bash\nif [[ "${1:-}" == --version ]]; then echo "%s (measure-exit dry-run stub: exec-form args %s)"; exit 0; fi\nexec %q %s "$@"\n' \
    "$ver" "$how" "$MX_FAKE_LIB/probe-stub.sh" "$stub_mode" >"$stub/claude"
chmod 0755 "$stub/claude"
cd "$home" || exit 125
exec env -i HOME="$home" PATH="$stub:$MX_FAKE_BIN:$MX_FAKE_PATH" LC_ALL=C USER="$(id -un)" "${envs[@]}" \
    "$MX_FAKE_BIN/measure-exit" "${drv[@]:1}" -agent-director "$MX_FAKE_BIN/agent-director" \
    -tmux-base "$MX_FAKE_ROOT" -workdir "$home/work" -ready-timeout 30s -step-timeout 30s
FAKE_ENGINE
chmod 0755 "$fake_engine"
l0_root="$tmp/l0-results"
l0_id="mx-dry-l0-probe"
l0="$l0_root/$l0_id"
mkdir -p "$tmp/l0-fake"
l0_rc=0
env MX_FAKE_LOG="$tmp/l0-engine.log" MX_FAKE_ROOT="$tmp/l0-fake" MX_FAKE_BIN="$bin" MX_FAKE_LIB="$STUB_DIR/lib" \
    MX_FAKE_PATH="$PATH" MX_FAKE_PROBE_MIN=2.1.200 \
    "$RUNNER" probe --run --engine "$fake_engine" --results-root "$l0_root" --run-id "$l0_id" \
    --guard-home "$fixture" --versions "2.1.120 2.1.200 2.1.280 2.1.285" >"$tmp/l0-runner.txt" 2>&1 || l0_rc=$?
for d in "$l0"/*/; do
    [[ -r "$d/harness-ids.txt" ]] || continue
    while read -r kind s; do
        [[ "$kind" == socket ]] && sockets+=("$s")
    done <"$d/harness-ids.txt"
done
if [[ "$l0_rc" -eq 0 ]]; then
    ok "runner L0 (probe --run, fake engine) exited 0"
else
    fail "runner L0 (probe --run, fake engine) exited $l0_rc: $(tail -n 20 "$tmp/l0-runner.txt")"
fi
want_summary=$'probe 2.1.280: args_received\ndeployed 2.1.280: RUNS exec-form hooks (args_received)\nprobe 2.1.120: args_not_received\nprobe 2.1.200: args_received\nminimum: 2.1.200 (newest that ignores args: 2.1.120)'
[[ "$(cat "$l0/probe-summary.txt" 2>/dev/null)" == "$want_summary" ]] \
    || fail "runner L0 probe-summary.txt: $(cat "$l0/probe-summary.txt" 2>/dev/null)"
decide_in=()
for v in 2.1.120 2.1.200 2.1.280; do
    [[ -r "$l0/$v/results.json" ]] || fail "runner L0: no $v/results.json"
    [[ "$(cat "$l0/$v/guard-status.txt" 2>/dev/null)" == pass ]] || fail "runner L0: $v/guard-status.txt is not the run's pass"
    decide_in+=(-in "$l0/$v")
done
l0_decide_rc=0
"$bin/measure-exit" decide "${decide_in[@]}" >"$tmp/l0-decide.md" 2>&1 || l0_decide_rc=$?
l0_outcome="$(sed -n '/^## Outcome/,$p' "$tmp/l0-decide.md")"
l0_ok=1
for v in 2.1.120 2.1.200 2.1.280; do
    grep -qE -- "^- $l0/$v: run $l0_id, mode probe, Claude Code $v .*, guard pass\$" "$tmp/l0-decide.md" || l0_ok=0
done
grep -qF 'Minimum: 2.1.200 (newest that ignores args: 2.1.120).' "$tmp/l0-decide.md" || l0_ok=0
# L1 and L2 are not part of this run, so decide still exits 2 for their
# missing cases and scenarios; nothing else may be invalid.
other_invalid="$(grep -E '^- ' <<<"$l0_outcome" | grep -vE '^- STOP: ' \
    | grep -vE '^- (case rn6\.[a-z.-]+ is missing|RN-6: no default-budget case has a measured time|RN-9 scenario rn9\.[a-z-]+ is missing)$' || true)"
[[ -z "$other_invalid" ]] || l0_ok=0
if [[ "$l0_ok" -eq 1 && "$l0_decide_rc" -eq 2 ]]; then
    ok "decide accepts the runner's L0 layout (one -in per probed version; guard pass; minimum 2.1.200)"
else
    fail "decide on the runner's L0 layout (exit $l0_decide_rc; unexpected: ${other_invalid:-none}): $(cat "$tmp/l0-decide.md")"
fi
mkdir -p "$tmp/l0-noguard"
cp -r -- "$l0/2.1.280" "$tmp/l0-noguard/2.1.280"
rm -f -- "$tmp/l0-noguard/2.1.280/guard-status.txt"
"$bin/measure-exit" decide -in "$tmp/l0-noguard/2.1.280" >"$tmp/l0-noguard.md" 2>&1
if grep -qF "$tmp/l0-noguard/2.1.280: the host guard did not pass (missing)" "$tmp/l0-noguard.md"; then
    ok "decide refuses an L0 version directory without guard-status.txt"
else
    fail "decide did not refuse an L0 version directory without guard-status.txt"
fi

# ---- 5. guard verify ----------------------------------------------------------------------
"$GUARD" verify --state "$tmp/guard-quiet.state" --home "$fixture" || fail "guard quiet-host verify"
ids=()
for d in "$out_root"/*/; do
    [[ -r "$d/harness-ids.txt" ]] && ids+=(--ids-file "$d/harness-ids.txt")
done
"$GUARD" verify --state "$tmp/guard-busy.state" --home "$fixture" "${ids[@]}" || fail "guard busy-host verify"

# ---- 6. runner print-only --------------------------------------------------------------------
layers="$tmp/layers"
mkdir -p "$layers"
printf '{"env": {"MX_DRY_PLAIN": "mx-sentinel-layer-value"}, "hooks": {}}\n' >"$layers/user.json"
printf '{"mcpServers": {"mx-dry-noop": {"command": "true"}}}\n' >"$layers/mcp.json"
sentinel_key="mx-sentinel-api-key-0123456789abcdef"
sentinel_token="mx-sentinel-auth-token-0123456789"
for m in measure rn9 probe; do
    printed="$(env TMUX=/tmp/tmux-sentinel,1,0 CLAUDECODE=1 CLAUDE_CONFIG_DIR=/nonexistent \
        ANTHROPIC_API_KEY="$sentinel_key" ANTHROPIC_AUTH_TOKEN="$sentinel_token" \
        ANTHROPIC_BASE_URL=https://gateway.invalid ANTHROPIC_MODEL=mx-dry-model \
        ANTHROPIC_SMALL_FAST_MODEL=mx-dry-small-fast-model \
        "$RUNNER" "$m" --results-root "$tmp/results" --run-id "mx-dry-$m-run" \
        --user-settings "$layers/user.json" --mcp-config "$layers/mcp.json" 2>&1)" || fail "runner print-only ($m) exited non-zero"
    [[ "$m" == measure ]] && printf -- '--- runner print-only (measure) ---\n%s\n' "$printed"
    for bad in "$sentinel_key" "$sentinel_token" "mx-sentinel-layer-value" "mx-dry-model" "mx-dry-small-fast-model" \
        "https://gateway.invalid" "-e TMUX" "TMUX=" "-e ANTHROPIC_API_KEY" \
        "CLAUDECODE" "CLAUDE_CONFIG_DIR" "docker.sock"; do
        grep -qF -- "$bad" <<<"$printed" && fail "runner print-only ($m) shows forbidden text: $bad"
    done
    while read -r src; do
        case "$src" in
            "$HOME" | "$HOME/" | "$HOME/.agent-director"* | "$HOME/.claude"* | /tmp | /tmp/ | /tmp/tmux-* | /run/* | /var/run/*)
                fail "runner print-only ($m) mounts a forbidden path: $src" ;;
        esac
    done < <(grep -oE -- '-v [^ ]+' <<<"$printed" | sed -e 's/^-v //' -e 's/:.*//')
    if [[ "$m" == probe ]]; then
        grep -qF -- '--network none' <<<"$printed" || fail "runner print-only (probe) lacks --network none"
        grep -qE -- ' run --rm --network host [^ ]+ npm view @anthropic-ai/claude-code versions' <<<"$printed" \
            || fail "runner print-only (probe): the npm version listing is not on the host network"
    else
        for name in ANTHROPIC_BASE_URL ANTHROPIC_AUTH_TOKEN ANTHROPIC_MODEL ANTHROPIC_SMALL_FAST_MODEL; do
            grep -qE -- "-e $name( |$)" <<<"$printed" || fail "runner print-only ($m) does not forward $name by name"
        done
    fi
    # The container line names exactly the mode's cases (B1): L1 never
    # starts L2's scenarios, nor L2 L1's cases.
    case "$m" in
        measure) want_cases="rn6.idle,rn6.midturn,rn6.mcp,rn6.idle.raised-hook,rn6.midturn.raised-hook,rn6.mcp.raised-hook,rn6.idle.raised-env,rn6.midturn.raised-env,rn6.mcp.raised-env,rn2.natural,rn2.pause,rn2.mcp" ;;
        rn9) want_cases="rn9.drive,rn9.resume,rn9.team-inprocess,rn9.team-splitpane" ;;
        probe) want_cases="" ;;
    esac
    got_cases="$(grep -oE -- ' -cases [^ ]+' <<<"$printed" | sed 's/^ -cases //' | sort -u)"
    [[ "$got_cases" == "$want_cases" ]] || fail "runner print-only ($m) passes -cases '$got_cases', want '$want_cases'"
done
[[ "$(env ANTHROPIC_BASE_URL=x ANTHROPIC_AUTH_TOKEN=x ANTHROPIC_MODEL=x ANTHROPIC_SMALL_FAST_MODEL=x \
    "$RUNNER" measure --results-root "$tmp/results" --run-id mx-dry-cross-run --cases rn6.idle,rn9.drive >/dev/null 2>&1; echo $?)" == 2 ]] \
    && ok "runner refuses an L2 scenario in L1's --cases" || fail "runner accepted rn9.drive in measure's --cases"
# Staged layers that carry credentials are refused, print-only included
# (N7); the refusal names the key and never prints a value.
printf '{"env": {"MX_DRY_SECRET_TOKEN": "mx-sentinel-cred-value"}}\n' >"$layers/cred-user.json"
printf '{}\n' >"$layers/.credentials.json"
printf '{}\n' >"$tmp/.claude.json"
ln -s "$tmp/.claude.json" "$layers/innocent.json"
for bad_layer in "--user-settings $layers/cred-user.json" "--user-settings $layers/.credentials.json" \
    "--project-settings $layers/innocent.json"; do
    # shellcheck disable=SC2086 # the option and its file split on purpose
    refused_out="$("$RUNNER" measure --results-root "$tmp/results" --run-id mx-dry-cred-run $bad_layer 2>&1)"
    refused_rc=$?
    if [[ "$refused_rc" -eq 2 ]] && grep -q 'refusing the' <<<"$refused_out" && ! grep -qF 'mx-sentinel-cred-value' <<<"$refused_out"; then
        ok "runner refuses the staged layer ($bad_layer)"
    else
        fail "runner did not refuse the staged layer ($bad_layer): exit $refused_rc: $refused_out"
    fi
done
grep -qF 'MX_DRY_SECRET_TOKEN' <<<"$("$RUNNER" measure --results-root "$tmp/results" --run-id mx-dry-cred-run \
    --user-settings "$layers/cred-user.json" 2>&1)" || fail "the credential-layer refusal does not name the key"
[[ ! -e "$tmp/results" ]] || fail "runner print-only created $tmp/results"

# ---- 7. the sandbox user's own home and tmux ---------------------------------------------------
[[ ! -e "$passwd_home/.agent-director" ]] || fail "$passwd_home/.agent-director exists after the dry run"
socket_after=absent
[[ -S "$default_socket" ]] && socket_after=present
[[ "$socket_before" == "$socket_after" ]] || fail "the default tmux socket changed: $socket_before -> $socket_after"

if [[ "$fail_count" -eq 0 ]]; then
    printf 'DRY RUN PASSED (results under %s)\n' "$out_root"
    exit 0
fi
printf 'DRY RUN FAILED: %d check(s)\n' "$fail_count" >&2
exit 1
