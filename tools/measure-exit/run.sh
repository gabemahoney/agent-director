#!/usr/bin/env bash
# run.sh — the measure-exit host runner (Epic 21, t3.h98.w4.yu.gm). Shell
# only: it never runs tmux, agent-director or any Go artifact on the host.
# It starts the measurement container (built FROM the test image with Claude
# Code under its own tag, default 2.1.280, the deployed version;
# tools/measure-exit/Dockerfile) once per run, wrapped in guard.sh.
#
#   run.sh measure [options]   L1: the RN-6 and RN-2 cases
#   run.sh rn9 [options]       L2: the RN-9 scenarios with RN-7's record
#   run.sh probe [options]     L0: the exec-form version probe: the deployed
#                              version explicitly, then the bisection
#
# PRINT-ONLY BY DEFAULT: without --run it prints the exact container command
# lines (credentials as names only; their values are never printed or
# logged), the guard calls and the number of Claude Code sessions, and
# executes nothing. --run is the gated live step: it starts PAID real Claude
# Code agents (measure, rn9) and needs the user's go-ahead.
#
# Credentials (user decisions 2026-10-01): measure and rn9 forward exactly
# ANTHROPIC_BASE_URL, ANTHROPIC_AUTH_TOKEN (the InferenceHub gateway),
# ANTHROPIC_MODEL and ANTHROPIC_SMALL_FAST_MODEL (the models pinned for the
# run), each by name (-e NAME), and refuse to run unless all four are set.
# Their values are never printed. Nothing else is forwarded: never
# ANTHROPIC_API_KEY, never ~/.claude credentials, never TMUX or a host Claude
# Code session variable. probe forwards no credential: its container has
# --network none and a dummy ANTHROPIC_AUTH_TOKEN literal (not a secret).
#
# Mounts: each staged settings copy read-only at the path Claude Code (or the
# driver) reads it, and one writable results directory. Never the host home,
# ~/.agent-director, ~/.claude, ~/.claude.json, a tmux socket directory, /tmp
# or the engine socket. The staging directory (copies of the operator's
# layers) is removed afterwards; the results directory is kept and printed.
#
# Options:
#   --run                     execute (default: print only)
#   --run-id ID               run id (default: mx-<UTC time>-<random>)
#   --results-root DIR        results go in DIR/<run id> (default:
#                             $HOME/measure-exit-results; never the worktree)
#   --image TAG               measurement image (default:
#                             agent-director-measure:cc-<--claude-code-version>)
#   --claude-code-version V   the measurement image's Claude Code (default
#                             2.1.280, the deployed version)
#   --base-image TAG          the test image it builds FROM (default agent-director-test)
#   --engine NAME             container engine (default docker, as test-image)
#   --samples N               measure: samples per case (default 22: the
#                             driver's 20-sample floor plus a buffer of 2;
#                             decide reads every completed sample once a
#                             case has 20, so one flaky sample forces no
#                             re-run)
#   --cases LIST              driver case ids, comma-separated, each one of the
#                             mode's (default: all of the mode's ids, the MCP
#                             cases included, which read "not run" without
#                             --mcp-config). measure and rn9 always pass -cases:
#                             the driver refuses real mode without it, so L1
#                             never runs L2's scenarios or L2 L1's cases. probe
#                             takes no --cases.
#   --user-settings F         deployment layers to stage (default: none, the
#   --managed-settings F        run is labelled "vanilla settings")
#   --project-settings F
#   --local-settings F
#   --mcp-config F            the deployment's MCP configuration (default: none;
#                             the MCP cases are then "not run")
#                             Every staged layer is refused (exit 2, print-only
#                             too) when it is, or links to, a .claude.json or a
#                             .credentials.json, when it is not JSON, or when
#                             any key in it looks like a credential (KEY, TOKEN,
#                             SECRET, PASSWORD, PASSWD, CREDENTIAL, OAUTH,
#                             AUTHORIZATION, COOKIE), or when an env object in it
#                             sets ANTHROPIC_BASE_URL, ANTHROPIC_CUSTOM_HEADERS, a
#                             CLAUDE_CODE_USE_* name or a name real mode refuses,
#                             or sets any name to a value that looks like a URL
#                             or an authorization header; the refusal names the
#                             key, never a value. The driver repeats all of these
#                             checks over the layer files in real mode.
#   --host-network            opt in to host networking (hosts with the bridge
#                             MTU problem, b.rx8); printed as a warning
#   --guard-mode busy|quiet   guard.sh mode (default busy: user decision)
#   --guard-home DIR          guard.sh --home override (dry runs and tests only)
#   probe only:
#   --versions "V ..."        pinned candidate Claude Code versions, X.Y.Z each
#   --versions-file F           (default: npm's list, fetched in --run mode in a
#                               credential-free base-image container on the
#                               host network, as the image builds are: the
#                               bridge network stalls on b.rx8 hosts)
#   --known-bad V             newest version known to ignore args (2.1.120)
#   --known-good V            oldest version known to run them (2.1.285)
#   --deployed V              the deployed version, probed explicitly before the
#                             bisection (default 2.1.280); its verdict is the
#                             first "deployed" line of probe-summary.txt, and
#                             it narrows the bisection
#
# Exit status: 0 ok; 1 the container run or the guard failed, or the probe
# could not decide the deployed version; 2 bad usage or a refusal (missing
# credential, missing image, a refused layer); 3 (probe) the deployed version
# does NOT run exec-form hooks: STOP and tell the user at once. After that
# verdict the probe stops: no bisection and no further build, only the guard
# verify, then exit 3. Under make, any non-zero exit becomes make's 2, so a
# caller reads <results>/deployed-verdict.txt, not make's exit status.

set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
readonly REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd -P)"
readonly GUARD="$SCRIPT_DIR/guard.sh"
readonly MARKER_ENV=AGENT_DIRECTOR_MEASURE_CONTAINER
readonly FORWARDED=(ANTHROPIC_BASE_URL ANTHROPIC_AUTH_TOKEN ANTHROPIC_MODEL ANTHROPIC_SMALL_FAST_MODEL)
readonly PROBE_DUMMY_TOKEN="mx-probe-dummy-token-not-a-secret"
readonly CONTAINER_HOME=/home/tester
readonly LAYER_DIR=/opt/measure-exit/layers
readonly RESULTS_MOUNT=/results
# DEPLOYED_CLAUDE_CODE is the deployed version: the default measurement
# image, and the version L0 probes explicitly.
readonly DEPLOYED_CLAUDE_CODE=2.1.280
# LAYER_REFUSED_ENV are the names a staged layer's env object may not set:
# the gateway's own routing (ANTHROPIC_BASE_URL, ANTHROPIC_CUSTOM_HEADERS),
# which a layer would override, and every name in the driver's
# realModeRefusedEnv (env.go; keep the two in step, and in step with
# layerenv.go's layerEnvNameRefused). Any CLAUDE_CODE_USE_* name (a provider
# switch) is refused as well, and the key check refuses the other
# credential names (ANTHROPIC_AUTH_TOKEN and the like) first
# (refuse_credential_layer).
readonly LAYER_REFUSED_ENV=(ANTHROPIC_BASE_URL ANTHROPIC_CUSTOM_HEADERS
    ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN CLAUDE_CODE_USE_BEDROCK
    AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN AWS_BEARER_TOKEN_BEDROCK
    AWS_REGION AWS_PROFILE AWS_DEFAULT_REGION)

# Case ids per mode (the driver's registry: rn6.go, rn2.go, rn9.go). Session
# counts below are computed from them.
readonly RN6_KINDS=(idle midturn mcp)
readonly RN6_SUFFIXES=("" .raised-hook .raised-env)
readonly RN2_CASES=(rn2.natural rn2.pause rn2.mcp)
readonly RN9_CASES=(rn9.drive rn9.resume rn9.team-inprocess rn9.team-splitpane)

die() {
    local rc="$1"
    shift
    printf 'run.sh: %s\n' "$*" >&2
    exit "$rc"
}

# q prints its arguments shell-quoted, space-separated; a word of only
# characters no shell treats specially (a comma-separated case list
# included) is printed as it is.
q() {
    local out="" a
    for a in "$@"; do
        if [[ "$a" =~ ^[A-Za-z0-9_@%+=:,./-]+$ ]]; then
            out+="$a "
        else
            out+="$(printf '%q' "$a") "
        fi
    done
    printf '%s\n' "${out% }"
}

# ---- options ---------------------------------------------------------------
[[ $# -ge 1 ]] || die 2 "usage: run.sh measure|rn9|probe [options] (see the header)"
mode="$1"
shift
case "$mode" in measure | rn9 | probe) ;; *) die 2 "unknown mode $mode (want measure, rn9 or probe)" ;; esac
execute=0
run_id=""
results_root="${HOME:?HOME is not set}/measure-exit-results"
cc_version="$DEPLOYED_CLAUDE_CODE"
image=""
base_image=agent-director-test
engine=docker
samples=22
cases=""
host_network=0
guard_mode=busy
guard_home=""
versions=""
versions_file=""
known_bad=2.1.120
known_good=2.1.285
deployed="$DEPLOYED_CLAUDE_CODE"
declare -A layer_src=([user]="" [managed]="" [project]="" [local]="" [mcp]="")
need() { [[ $# -ge 2 && -n "$2" ]] || die 2 "$1 needs a value"; }
while [[ $# -gt 0 ]]; do
    case "$1" in
        --run) execute=1; shift ;;
        --print-only) execute=0; shift ;;
        --run-id) need "$@"; run_id="$2"; shift 2 ;;
        --results-root) need "$@"; results_root="$2"; shift 2 ;;
        --image) need "$@"; image="$2"; shift 2 ;;
        --claude-code-version) need "$@"; cc_version="$2"; shift 2 ;;
        --base-image) need "$@"; base_image="$2"; shift 2 ;;
        --engine) need "$@"; engine="$2"; shift 2 ;;
        --samples) need "$@"; samples="$2"; shift 2 ;;
        --cases) need "$@"; cases="$2"; shift 2 ;;
        --user-settings) need "$@"; layer_src[user]="$2"; shift 2 ;;
        --managed-settings) need "$@"; layer_src[managed]="$2"; shift 2 ;;
        --project-settings) need "$@"; layer_src[project]="$2"; shift 2 ;;
        --local-settings) need "$@"; layer_src[local]="$2"; shift 2 ;;
        --mcp-config) need "$@"; layer_src[mcp]="$2"; shift 2 ;;
        --host-network) host_network=1; shift ;;
        --guard-mode) need "$@"; guard_mode="$2"; shift 2 ;;
        --guard-home) need "$@"; guard_home="$2"; shift 2 ;;
        --versions) need "$@"; versions="$2"; shift 2 ;;
        --versions-file) need "$@"; versions_file="$2"; shift 2 ;;
        --known-bad) need "$@"; known_bad="$2"; shift 2 ;;
        --known-good) need "$@"; known_good="$2"; shift 2 ;;
        --deployed) need "$@"; deployed="$2"; shift 2 ;;
        *) die 2 "unknown option $1" ;;
    esac
done
[[ "$samples" =~ ^[0-9]+$ && "$samples" -ge 1 ]] || die 2 "--samples must be a positive whole number"
[[ "$guard_mode" == busy || "$guard_mode" == quiet ]] || die 2 "--guard-mode must be busy or quiet"
for v in "$deployed" "$known_bad" "$known_good"; do
    [[ "$v" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die 2 "--deployed, --known-bad and --known-good must be versions X.Y.Z (got $v)"
done
if [[ -n "$versions_file" ]]; then
    [[ -r "$versions_file" && -f "$versions_file" ]] || die 2 "--versions-file $versions_file is not a readable file"
    versions="$(cat -- "$versions_file")"
fi
for v in $versions; do
    [[ "$v" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die 2 "--versions and --versions-file take versions X.Y.Z only (got $v)"
done
if [[ "$mode" != probe && -n "$versions" ]]; then
    die 2 "--versions and --versions-file are for probe mode only"
fi
if [[ "$mode" == probe && -n "$cases" ]]; then
    die 2 "probe mode runs its one case; --cases is for measure and rn9"
fi
[[ "$results_root" == /* ]] || die 2 "--results-root must be an absolute path"
case "$results_root/" in
    "$REPO_ROOT"/*) die 2 "--results-root must lie outside the worktree ($REPO_ROOT)" ;;
    "$HOME/.agent-director/"* | "$HOME/.claude"*) die 2 "--results-root must lie outside ~/.agent-director and ~/.claude*" ;;
esac
if [[ -z "$run_id" ]]; then
    run_id="mx-$(date -u +%Y%m%dT%H%M%SZ)-$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')"
fi
[[ "$run_id" =~ ^[A-Za-z0-9._-]{8,64}$ ]] || die 2 "--run-id must be 8 to 64 characters of [A-Za-z0-9._-]"
[[ -n "$image" ]] || image="agent-director-measure:cc-$cc_version"
results="$results_root/$run_id"
stage="$results_root/.stage-$run_id"

# ---- staged layers -----------------------------------------------------------
# layer_target prints where a staged layer is mounted in the container.
layer_target() {
    case "$1" in
        user) printf '%s\n' "$CONTAINER_HOME/.claude/settings.json" ;;
        managed) printf '%s\n' /etc/claude-code/managed-settings.json ;;
        project) printf '%s\n' "$LAYER_DIR/project-settings.json" ;;
        local) printf '%s\n' "$LAYER_DIR/local-settings.json" ;;
        mcp) printf '%s\n' "$LAYER_DIR/mcp.json" ;;
    esac
}

# refuse_credential_layer refuses (exit 2) staging layer $1 from file $2
# when it is, or resolves to, Claude Code's state file (.claude.json) or its
# credentials file (.credentials.json), when it is not JSON, or when any key
# anywhere in it looks like a credential: env blocks, MCP server env and
# headers, and helpers such as apiKeyHelper. Its value would be mounted into
# the container, and the run bills to the gateway only. It also refuses a
# layer with an env object (at any depth: settings env, MCP server env) that
# sets a LAYER_REFUSED_ENV or CLAUDE_CODE_USE_* name, which would take the
# agents off the gateway, or that sets any name to a value that looks like a
# URL or an authorization header (://, "bearer ", "authorization:", any
# case). The value test runs inside jq, so no value reaches the shell. The
# refusal names the key, never a value. The driver repeats every one of
# these checks over the layer files in real mode (layerenv.go's
# checkLayerFiles), for a container started by hand; the file names and the
# key pattern below are kept in step with its layerRefusedFileNames and
# layerCredentialKeyParts.
refuse_credential_layer() {
    local k="$1" src="$2" real base key keys entries flag name refused n
    real="$(readlink -f -- "$src" 2>/dev/null || printf '%s' "$src")"
    for base in "${src##*/}" "${real##*/}"; do
        case "$base" in
            .claude.json | .credentials.json)
                die 2 "refusing the $k layer $src: it is (or links to) Claude Code's $base, which holds credentials or account state; stage a settings file instead (nothing was built or run)" ;;
        esac
    done
    command -v jq >/dev/null 2>&1 \
        || die 2 "refusing the $k layer $src: jq is needed to check it for credential-like keys and was not found (nothing was built or run)"
    keys="$(jq -r '[.. | objects | keys[]] | unique | .[]' <"$src" 2>/dev/null)" \
        || die 2 "refusing the $k layer $src: not a JSON document, so it cannot be checked for credentials (nothing was built or run)"
    while IFS= read -r key; do
        case "${key^^}" in
            *KEY* | *TOKEN* | *SECRET* | *PASSWORD* | *PASSWD* | *CREDENTIAL* | *OAUTH* | *AUTHORIZATION* | *COOKIE*)
                die 2 "refusing the $k layer $src: it holds the credential-like key $key (its value would be mounted into the container; values are never printed); remove it from the copy you stage (nothing was built or run)" ;;
        esac
    done <<<"$keys"
    # One line per env entry: "url" or "-" (whether its value looks like a
    # URL or an authorization header), a tab, then the name.
    entries="$(jq -r '.. | objects | select(has("env")) | .env | objects | to_entries[]
        | "\(if (.value | tostring | test("://|\\bbearer\\s|authorization\\s*:"; "i")) then "url" else "-" end)\t\(.key | gsub("[\\t\\r\\n]"; " "))"' \
        <"$src" 2>/dev/null)" \
        || die 2 "refusing the $k layer $src: its env objects could not be read, so it cannot be checked (nothing was built or run)"
    while IFS=$'\t' read -r flag name; do
        refused=0
        case "${name^^}" in
            CLAUDE_CODE_USE_*) refused=1 ;;
            *)
                for n in "${LAYER_REFUSED_ENV[@]}"; do
                    if [[ "${name^^}" == "$n" ]]; then refused=1; fi
                done ;;
        esac
        if [[ "$refused" -eq 1 ]]; then
            die 2 "refusing the $k layer $src: an env object in it sets $name, which would take the agents off the gateway (the run bills to the gateway only; values are never printed); remove it from the copy you stage (nothing was built or run)"
        fi
        if [[ "$flag" == url ]]; then
            die 2 "refusing the $k layer $src: an env object in it sets $name to a value that looks like a URL or an authorization header (values are never printed); remove it from the copy you stage (nothing was built or run)"
        fi
    done <<<"$entries"
}

# report_layers lists each layer as staged or missing, and refuses (exit 2)
# a staged layer that carries credentials (refuse_credential_layer).
report_layers() {
    local k src
    vanilla=1
    for k in user managed project local mcp; do
        src="${layer_src[$k]}"
        if [[ -z "$src" ]]; then
            printf 'layer %-8s none\n' "$k"
            continue
        fi
        if [[ ! -r "$src" || ! -f "$src" ]]; then
            printf 'layer %-8s MISSING: %s (not staged)\n' "$k" "$src"
            layer_src[$k]=""
            continue
        fi
        refuse_credential_layer "$k" "$src"
        vanilla=0
        printf 'layer %-8s staged from %s -> %s (read-only)\n' "$k" "$src" "$(layer_target "$k")"
    done
    if [[ "$vanilla" -eq 1 ]]; then
        printf 'settings: vanilla (no deployment layer staged); label the results "vanilla settings"\n'
    fi
}

# stage_layers copies each staged layer into the staging directory.
stage_layers() {
    local k
    mkdir -p "$stage/layers"
    chmod 0700 "$stage"
    for k in user managed project local mcp; do
        [[ -n "${layer_src[$k]}" ]] || continue
        cp -- "${layer_src[$k]}" "$stage/layers/$k.json"
        chmod 0444 "$stage/layers/$k.json"
    done
}

# ---- the container command ---------------------------------------------------
# container_cmd fills the array cmd with the engine run command for this
# mode; $1 is the results directory to mount, $2 the image.
container_cmd() {
    local out="$1" img="$2" k
    cmd=("$engine" run --rm --name "measure-exit-$run_id")
    if [[ "$mode" == probe ]]; then
        cmd+=(--network none)
    elif [[ "$host_network" -eq 1 ]]; then
        cmd+=(--network host)
    fi
    cmd+=(-e "$MARKER_ENV=1" -e DISABLE_AUTOUPDATER=1)
    if [[ "$mode" == probe ]]; then
        cmd+=(-e "ANTHROPIC_AUTH_TOKEN=$PROBE_DUMMY_TOKEN")
    else
        for k in "${FORWARDED[@]}"; do
            cmd+=(-e "$k")
        done
    fi
    for k in user managed project local mcp; do
        [[ -n "${layer_src[$k]}" ]] || continue
        cmd+=(-v "$stage/layers/$k.json:$(layer_target "$k"):ro")
    done
    cmd+=(-v "$out:$RESULTS_MOUNT" "$img" measure-exit run)
    case "$mode" in
        measure) cmd+=(-mode real -samples "$samples") ;;
        rn9) cmd+=(-mode real) ;;
        probe) cmd+=(-mode probe) ;;
    esac
    cmd+=(-out "$RESULTS_MOUNT" -run-id "$run_id")
    # measure and rn9 always name their cases (resolve_cases): the driver
    # refuses real mode without -cases.
    [[ "$mode" == probe ]] || cmd+=(-cases "$cases")
    [[ -n "${layer_src[mcp]}" ]] && cmd+=(-mcp-config "$(layer_target mcp)")
    [[ -n "${layer_src[project]}" ]] && cmd+=(-project-settings "$(layer_target project)")
    [[ -n "${layer_src[local]}" ]] && cmd+=(-local-settings "$(layer_target local)")
    return 0
}

# build_cmd fills cmd with the version probe's image build for version $1
# and tag $2 (FROM the test image; the driver compiles in the build stage).
# REQUIRE_NATIVE_CLAUDE=0: the probe reads only whether a hook got its args,
# and older versions in the bisection ship no native binary (the Dockerfile
# records which); L1 and L2's image (make measure-image) requires it.
build_cmd() {
    cmd=("$engine" build --network=host --build-arg "BASE_IMAGE=$base_image"
        --build-arg "CLAUDE_CODE_VERSION=$1" --build-arg REQUIRE_NATIVE_CLAUDE=0
        -t "$2" -f "$SCRIPT_DIR/Dockerfile" "$REPO_ROOT")
}

# versions_list_cmd fills cmd with the npm listing of Claude Code versions:
# a credential-free base-image container on the host network, as the image
# builds use (the bridge network stalls on hosts with the MTU mismatch,
# b.rx8). Not used when --versions or --versions-file pins the list.
versions_list_cmd() {
    cmd=("$engine" run --rm --network host "$base_image" npm view @anthropic-ai/claude-code versions --json)
}

# ---- sessions ----------------------------------------------------------------
# default_cases prints the mode's case ids (the driver's registry), the MCP
# cases included: with no MCP configuration staged they are reported "not
# run" and start no agent, and decide accepts them as such.
default_cases() {
    local k s
    case "$mode" in
        measure)
            for s in "${RN6_SUFFIXES[@]}"; do
                for k in "${RN6_KINDS[@]}"; do printf 'rn6.%s%s\n' "$k" "$s"; done
            done
            printf '%s\n' "${RN2_CASES[@]}"
            ;;
        rn9) printf '%s\n' "${RN9_CASES[@]}" ;;
    esac
}

# resolve_cases sets cases, for measure and rn9, to the mode's ids when
# --cases was not given, and refuses (exit 2) an id that is not one of the
# mode's: L1 must never start L2's scenarios, nor L2 L1's cases.
resolve_cases() {
    local c ok known
    [[ "$mode" != probe ]] || return 0
    if [[ -z "$cases" ]]; then
        cases="$(default_cases | paste -sd, -)"
        return 0
    fi
    for c in ${cases//,/ }; do
        ok=0
        while IFS= read -r known; do
            [[ "$c" == "$known" ]] && ok=1
        done < <(default_cases)
        [[ "$ok" -eq 1 ]] || die 2 "--cases: $c is not a $mode case (want some of: $(default_cases | paste -sd' ' -))"
    done
}

# session_count sets agents and prompted for the resolved cases (an MCP
# case without --mcp-config starts no agent) and teammates, the agents the
# rn9 team leads start themselves (about 2 each).
session_count() {
    local c
    agents=0 prompted=0 teammates=0
    for c in ${cases//,/ }; do
        if [[ "$c" == *mcp* && -z "${layer_src[mcp]}" ]]; then
            continue
        fi
        case "$c" in
            rn6.midturn*) agents=$((agents + samples)); prompted=$((prompted + samples)) ;;
            rn6.* | rn2.*) agents=$((agents + samples)) ;;
            rn9.drive) agents=$((agents + 1)); prompted=$((prompted + 1)) ;;
            rn9.resume) agents=$((agents + 2)); prompted=$((prompted + 2)) ;;
            rn9.team-*) agents=$((agents + 1)); prompted=$((prompted + 1)); teammates=$((teammates + 2)) ;;
        esac
    done
}

print_sessions() {
    local agents prompted teammates
    if [[ "$mode" == probe ]]; then
        printf 'sessions: one idle Claude Code start per probed version: the deployed %s first, then the bisection (about 5 to 11 in all; none after a STOP verdict), no prompt, no API (--network none)\n' "$deployed"
        return
    fi
    printf 'cases: %s\n' "$cases"
    session_count
    printf 'sessions: %d real Claude Code agents (%d of them prompted), one at a time' "$agents" "$prompted"
    if ((teammates > 0)); then
        printf ', plus the teammates the team leads start (about 2 each): about %d Claude Code sessions in all' $((agents + teammates))
    fi
    if [[ "$mode" == measure && -z "${layer_src[mcp]}" ]]; then
        printf '; MCP cases not measured (no --mcp-config; their rows read "not run")'
    fi
    printf '\n'
}

# ---- guard -------------------------------------------------------------------
guard_args() {
    garg=()
    [[ -n "$guard_home" ]] && garg+=(--home "$guard_home")
    return 0
}

guard_snapshot_cmd() {
    guard_args
    cmd=("$GUARD" snapshot --state "$stage/guard.state" "${garg[@]}")
    [[ "$guard_mode" == busy ]] && cmd+=(--busy-host)
    return 0
}

guard_verify_cmd() {
    local f
    guard_args
    cmd=("$GUARD" verify --state "$stage/guard.state" "${garg[@]}" --id "$run_id")
    for f in "$@"; do
        [[ -r "$f" ]] && cmd+=(--ids-file "$f")
    done
    return 0
}

# ---- print-only --------------------------------------------------------------
print_plan() {
    printf '== measure-exit runner: %s (print only; nothing is executed) ==\n' "$mode"
    printf 'run id:   %s\nresults:  %s\nstaging:  %s (removed after the run)\nimage:    %s\n' \
        "$run_id" "$results" "$stage" "$image"
    report_layers
    if [[ "$mode" != probe ]]; then
        local k missing=()
        for k in "${FORWARDED[@]}"; do
            [[ -n "${!k:-}" ]] || missing+=("$k")
        done
        printf 'credentials: forwarded by name only: %s (values never printed)\n' "${FORWARDED[*]}"
        if ((${#missing[@]})); then
            printf 'note: --run would refuse: %s not set\n' "${missing[*]}"
        fi
        if [[ "$host_network" -eq 1 ]]; then
            printf 'WARNING: host networking (--network host) opted in (b.rx8)\n'
        fi
    else
        printf 'credentials: none forwarded; dummy ANTHROPIC_AUTH_TOKEN literal; --network none\n'
    fi
    print_sessions
    printf '\n# 1. guard snapshot\n'
    guard_snapshot_cmd
    q "${cmd[@]}"
    if [[ "$mode" == probe ]]; then
        printf '\n# 2a. the deployed version %s, probed explicitly first: build, then run\n' "$deployed"
        printf '#     deployed-verdict.txt then says whether %s runs exec-form hooks; if it does NOT: no bisection, no further build,\n' "$deployed"
        printf '#     only the guard verify (3), then run.sh exits 3: STOP and tell the user\n'
        build_cmd "$deployed" "agent-director-measure:probe-$deployed"
        q "${cmd[@]}"
        container_cmd "$results/$deployed" "agent-director-measure:probe-$deployed"
        q "${cmd[@]}"
        if [[ -n "$versions" ]]; then
            printf '\n# 2b. candidate versions: pinned (--versions): %s\n' "$(printf '%s\n' $versions | sort -uV | paste -sd' ' -)"
        else
            printf '\n# 2b. candidate versions: listed from npm in a credential-free %s container on the host network (b.rx8)\n' "$base_image"
            versions_list_cmd
            q "${cmd[@]}"
        fi
        printf '\n# 2c. per further probed VERSION (bisect %s..%s, narrowed by the deployed result): build, then run\n' "$known_bad" "$known_good"
        build_cmd VERSION "agent-director-measure:probe-VERSION"
        q "${cmd[@]}"
        container_cmd "$results/VERSION" "agent-director-measure:probe-VERSION"
        q "${cmd[@]}"
    else
        printf '\n# 2. container run\n'
        container_cmd "$results" "$image"
        q "${cmd[@]}"
    fi
    printf '\n# 3. guard verify (runs even when the container fails)\n'
    guard_verify_cmd "$results/harness-ids.txt"
    q "${cmd[@]}" --ids-file "$results/harness-ids.txt"
}

# ---- real mode -----------------------------------------------------------------
require_credentials() {
    local k missing=()
    for k in "${FORWARDED[@]}"; do
        [[ -n "${!k:-}" ]] || missing+=("$k")
    done
    ((${#missing[@]} == 0)) || die 2 "refusing to run: ${missing[*]} not set (the gateway pair and both pinned models are required; nothing was built or run)"
}

# run_guarded_container runs one container into results dir $1 with image
# $2 and records its exit status in $1/container-status.txt.
run_container() {
    local out="$1" img="$2" rc=0
    mkdir -p "$out"
    chmod 0700 "$out"
    container_cmd "$out" "$img"
    printf '+ %s\n' "$(q "${cmd[@]}")"
    "${cmd[@]}" || rc=$?
    printf '%s\n' "$rc" >"$out/container-status.txt"
    return "$rc"
}

# probe_one builds and runs the probe for version $1 and prints its result.
probe_one() {
    local v="$1" tag="agent-director-measure:probe-$1" out="$results/$1" res
    build_cmd "$v" "$tag"
    printf '+ %s\n' "$(q "${cmd[@]}")" >&2
    "${cmd[@]}" >&2 || { printf 'build_failed\n'; return 0; }
    run_container "$out" "$tag" >&2 || true
    res="$(grep -o '"result": *"[a-z_]*"' "$out/results.json" 2>/dev/null | head -n1 | sed 's/.*"\([a-z_]*\)"$/\1/')"
    printf '%s\n' "${res:-did_not_start}"
}

# probed caches each probed version's result, so the deployed version is
# never built or run twice.
declare -A probed=()

# probe_at probes version $1 once (a repeat reuses the first result), logs
# it to probe-summary.txt and leaves the result in the caller's r.
probe_at() {
    if [[ -z "${probed[$1]:-}" ]]; then
        probed[$1]="$(probe_one "$1")"
        printf 'probe %s: %s\n' "$1" "${probed[$1]}" | tee -a "$results/probe-summary.txt"
    fi
    r="${probed[$1]}"
}

# ver_lt reports whether version $1 is older than version $2 (both X.Y.Z).
ver_lt() {
    local -a a b
    local i
    IFS=. read -r -a a <<<"$1"
    IFS=. read -r -a b <<<"$2"
    for i in 0 1 2; do
        if ((10#${a[i]} != 10#${b[i]})); then
            ((10#${a[i]} < 10#${b[i]}))
            return
        fi
    done
    return 1
}

# deployed_status is the deployed version's verdict: ok (it runs exec-form
# hooks), stop (it does not) or undecided.
deployed_status=undecided

# probe_deployed probes the deployed version explicitly, before the
# bisection, and states whether it runs exec-form hooks: on the first
# "deployed" line of probe-summary.txt and in deployed-verdict.txt.
probe_deployed() {
    local r line
    probe_at "$deployed"
    case "$r" in
        args_received)
            deployed_status=ok
            line="deployed $deployed: RUNS exec-form hooks ($r)" ;;
        args_not_received)
            deployed_status=stop
            line="deployed $deployed: does NOT run exec-form hooks ($r). STOP: tell the user at once; rc.1's hooks are ignored on this fleet, and L1 and L2 must not run at $deployed" ;;
        *)
            deployed_status=undecided
            line="deployed $deployed: UNDECIDED ($r); L0 has not shown whether $deployed runs exec-form hooks" ;;
    esac
    printf '%s\n' "$line" | tee -a "$results/probe-summary.txt"
    printf '%s\n' "$line" >"$results/deployed-verdict.txt"
}

# candidate_versions sets the array cand to the versions between $1 and $2
# inclusive, oldest first, without pre-releases: the pinned list
# (--versions, --versions-file), else npm's listing (versions_list_cmd). It
# returns 1, with the reason in probe-summary.txt, when npm's listing fails
# or lists nothing: the two ends alone would otherwise pass for a bisection.
candidate_versions() {
    local list lo_v="$1" hi_v="$2"
    cand=()
    if [[ -n "$versions" ]]; then
        list="$versions"
    else
        versions_list_cmd
        printf '+ %s\n' "$(q "${cmd[@]}")" >&2
        if ! list="$("${cmd[@]}" | grep -o '"[0-9][0-9.]*"' | tr -d '"')" || [[ -z "$list" ]]; then
            printf 'bisect stopped: the npm version listing failed or listed nothing; pin the candidates with --versions\n' | tee -a "$results/probe-summary.txt"
            return 1
        fi
    fi
    mapfile -t cand < <(printf '%s\n' $list "$lo_v" "$hi_v" | grep -E '^[0-9]+\.[0-9]+\.[0-9]+$' | sort -uV \
        | awk -v lo="$lo_v" -v hi="$hi_v" '
            function cmp(a, b,   x, y, i) { split(a, x, "."); split(b, y, ".");
                for (i = 1; i <= 3; i++) { if (x[i] + 0 != y[i] + 0) return (x[i] + 0 < y[i] + 0) ? -1 : 1 } return 0 }
            cmp($0, lo) >= 0 && cmp($0, hi) <= 0')
}

# bisect probes both ends, then halves the interval until the oldest
# version that runs exec-form args is next to the newest that does not. A
# deployed version strictly between the ends, already probed, narrows the
# interval: it becomes the known-good end if it ran the args, the known-bad
# end if it did not.
bisect() {
    local -a list cand
    local lo hi mid r lo_v="$known_bad" hi_v="$known_good"
    if ver_lt "$known_bad" "$deployed" && ver_lt "$deployed" "$known_good"; then
        case "${probed[$deployed]:-}" in
            args_received) hi_v="$deployed" ;;
            args_not_received) lo_v="$deployed" ;;
        esac
    fi
    candidate_versions "$lo_v" "$hi_v" || return 1
    list=("${cand[@]}")
    if ((${#list[@]} < 2)); then
        printf 'bisect stopped: fewer than two candidate versions between %s and %s\n' "$lo_v" "$hi_v" | tee -a "$results/probe-summary.txt"
        return 1
    fi
    lo=0
    hi=$((${#list[@]} - 1))
    probe_at "${list[lo]}"
    [[ "$r" == args_not_received ]] || { printf 'bisect stopped: the known-bad end %s gave %s\n' "${list[lo]}" "$r" | tee -a "$results/probe-summary.txt"; return 1; }
    probe_at "${list[hi]}"
    [[ "$r" == args_received ]] || { printf 'bisect stopped: the known-good end %s gave %s\n' "${list[hi]}" "$r" | tee -a "$results/probe-summary.txt"; return 1; }
    while ((hi - lo > 1)); do
        mid=$(((lo + hi) / 2))
        probe_at "${list[mid]}"
        case "$r" in
            args_received) hi=$mid ;;
            args_not_received) lo=$mid ;;
            *) printf 'bisect stopped at %s: %s\n' "${list[mid]}" "$r" | tee -a "$results/probe-summary.txt"; return 1 ;;
        esac
    done
    printf 'minimum: %s (newest that ignores args: %s)\n' "${list[hi]}" "${list[lo]}" | tee -a "$results/probe-summary.txt"
}

# copy_guard_status copies the run's guard-status.txt into each probed
# version's directory. decide reads results.json and guard-status.txt from
# one directory, and L0 writes one results.json per version, so each version
# directory needs the (one, run-wide) guard verdict beside it: decide then
# takes one -in <results>/<version> per probed version. measure and rn9
# write both files to <results> itself. A failed copy only warns: decide
# then reads that version's guard as missing and refuses it, and the
# deployed verdict below must still be reported.
copy_guard_status() {
    local d
    for d in "$results"/*/; do
        [[ -d "$d" ]] || continue
        cp -- "$results/guard-status.txt" "${d%/}/guard-status.txt" \
            || printf 'run.sh: warning: guard-status.txt not copied into %s; decide will refuse it\n' "${d%/}" >&2
    done
}

run_real() {
    local rc=0 grc=0 ids=()
    [[ "$mode" == probe ]] || require_credentials
    report_layers
    command -v "$engine" >/dev/null 2>&1 || die 2 "container engine $engine not found"
    if [[ "$mode" != probe ]]; then
        "$engine" image inspect "$image" >/dev/null 2>&1 \
            || die 2 "image $image not found; build it with make measure-image"
    fi
    [[ ! -e "$results" && ! -e "$stage" ]] || die 2 "run id $run_id already used under $results_root"
    mkdir -p "$results_root"
    print_sessions
    stage_layers
    trap 'rm -rf -- "$stage"' EXIT
    guard_snapshot_cmd
    "${cmd[@]}" || die 2 "guard snapshot failed; nothing was run"
    mkdir -p "$results"
    chmod 0700 "$results"
    if [[ "$mode" == probe ]]; then
        probe_deployed
        if [[ "$deployed_status" == stop ]]; then
            # STOP: nothing more is built or probed; the guard verify
            # below still runs, then run.sh exits 3.
            printf 'bisect skipped: the deployed %s does NOT run exec-form hooks (STOP)\n' "$deployed" | tee -a "$results/probe-summary.txt"
            rc=1
        else
            bisect || rc=1
            [[ "$deployed_status" == ok ]] || rc=1
        fi
        for d in "$results"/*/; do ids+=("$d/harness-ids.txt"); done
    else
        run_container "$results" "$image" || rc=$?
        ids=("$results/harness-ids.txt")
    fi
    guard_verify_cmd "${ids[@]}"
    "${cmd[@]}" 2>&1 | tee "$results/guard-verify.txt" || grc=${PIPESTATUS[0]}
    if [[ "$grc" -eq 0 ]]; then
        printf 'pass\n' >"$results/guard-status.txt"
    else
        printf 'fail %s\n' "$grc" >"$results/guard-status.txt"
    fi
    if [[ "$mode" == probe ]]; then
        copy_guard_status
    fi
    if [[ -r "$results/results-table.txt" ]]; then
        cat -- "$results/results-table.txt"
    fi
    printf 'results: %s\n' "$results"
    if [[ "$mode" == rn9 ]]; then
        # The agent team scenarios' evidence (the driver's capture.go): pane
        # captures of a cut-short team run and Claude's debug logs, kept in
        # the results because the container runs with --rm.
        for f in "$results"/rn9.*-pane-*.txt "$results"/rn9.*-claude-debug-*; do
            if [[ -e "$f" ]]; then printf 'evidence: %s\n' "$f"; fi
        done
    fi
    if [[ "$mode" == probe ]]; then
        cat -- "$results/deployed-verdict.txt"
        if [[ "$deployed_status" == stop ]]; then
            printf 'run.sh: STOP: Claude Code %s (deployed) does NOT run exec-form hooks; tell the user at once\n' "$deployed" >&2
            exit 3
        fi
    fi
    [[ "$rc" -eq 0 && "$grc" -eq 0 ]] || exit 1
}

resolve_cases
if [[ "$execute" -eq 1 ]]; then
    run_real
else
    print_plan
fi
