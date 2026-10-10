#!/usr/bin/env bash
# publish-orchestrator.sh — publish phase end-to-end for agent-director releases.
#
# Executes 6 substeps in order (--release mode) or emits "would do" lines
# (--dry-run mode, the default). In BOTH modes an artifact preflight
# (validate_publish_artifacts) runs first and exits 1 on a bad --tarball/--notes/
# --binaries path, so dry-run no longer unconditionally exits 0 — a bad artifact
# path fails fast in either mode. Halts on the first substep failure with a
# structured SR-14 extended diagnostic and a partial run report. On success,
# writes dist/release-report.json and a human-readable terminal summary.
#
# Usage:
#   bash publish-orchestrator.sh \
#     --target <version>            # bare semver, e.g. 0.7.5 (no leading "v")
#     --bump-sha <commit-sha>       # SHA the tag should point at
#     --tarball <path-to-npm-tgz>   # tarball produced by the pack phase
#     --notes <path-to-notes.md>    # release notes file for gh release
#     --binaries <comma-sep-paths>  # CLI binaries to attach as gh release assets
#
#   Path inputs (--tarball, --notes, --binaries, --prior-phases): relative paths
#   are resolved to absolute AT PARSE TIME against the caller's current working
#   directory (NOT the worktree root), before any substep cd's. The resolved
#   --tarball/--notes/--binaries paths must exist as readable files — the
#   artifact preflight (see below) checks this before any substep runs.
#     [--release | --dry-run]       # default: dry-run
#     [--worktree-root <path>]      # defaults to "."
#     [--release-branch <name>]     # defaults to "release/v<target>"
#     [--prior-phases <json-file>]  # JSON array from earlier phases; default [].
#                                   # When given, it must be a readable regular
#                                   # file holding exactly one JSON array, or
#                                   # the run exits 2 before any substep. It is
#                                   # read once, at startup; later changes to
#                                   # the file do not reach the report.
#     [--bump-kind <patch|minor|major>]  # for the report; default "unknown"
#     [--source-version <semver>]        # for the report; default "unknown"
#     [--simulate-failure-at <substep>]  # DEBUG: force a substep to exit 1 in
#                                        # --release mode without executing it
#
# "would do" lines (dry-run) go to stdout.
# SR-14 diagnostics and progress annotations go to stderr.
# <report-dir>/release-report.json is always written (even on failure, for
# triage). The report dir defaults to the skill's own dist/ but is overridable
# via RELEASE_REPORT_DIR (env) so concurrent test invocations do not share the
# report path (b.aur).
#
# --simulate-failure-at accepts substep short names (without the "publish." prefix):
#   push-branch | create-tag | gh-release | npm-publish | fast-forward-main | delete-remote-branch
#
# Substeps (in order):
#   0. artifact preflight           validate_publish_artifacts — verifies every
#                                   --tarball/--notes/--binaries path resolves to
#                                   a readable file; runs in both modes and halts
#                                   (exit 1) before substep 1 on any bad path.
#   1. publish.push-branch          git push origin <release-branch>
#   2. publish.create-tag           git tag -a v<target> ... && git push origin v<target>
#   3. publish.gh-release           gh release create v<target> --notes-file <notes> <binaries...>
#   4. publish.npm-publish          npm publish <tarball>
#   5. publish.fast-forward-main    git fetch + merge --ff-only + push, run in
#                                   the parent (main) worktree (SR-13.3)
#   6. publish.delete-remote-branch git push origin --delete <release-branch>
#
# Exit codes:
#   0  success (all substeps passed, or dry-run with all artifact paths valid)
#   1  artifact preflight failure (publish.preflight-publish-artifacts; a
#      --tarball/--notes/--binaries path is not a readable file — checked in both
#      --release and --dry-run before any substep) OR substep failure
#      (halt-on-failure). SR-14 diagnostic emitted to stderr in both cases.
#   2  argument error, including a --prior-phases file that is missing,
#      unreadable, not a regular file, or does not hold exactly one JSON array

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# ─── argument parsing ─────────────────────────────────────────────────────────
TARGET=""
BUMP_SHA=""
TARBALL=""
NOTES=""
BINARIES=""
MODE="dry-run"
WORKTREE_ROOT="."
RELEASE_BRANCH=""
PRIOR_PHASES_FILE=""
BUMP_KIND="unknown"
SOURCE_VERSION="unknown"
SIMULATE_FAILURE_AT=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --target)                TARGET="$2";              shift 2 ;;
    --bump-sha)              BUMP_SHA="$2";             shift 2 ;;
    --tarball)               TARBALL="$2";              shift 2 ;;
    --notes)                 NOTES="$2";               shift 2 ;;
    --binaries)              BINARIES="$2";             shift 2 ;;
    --release)               MODE="release";            shift   ;;
    --dry-run)               MODE="dry-run";            shift   ;;
    --worktree-root)         WORKTREE_ROOT="$2";        shift 2 ;;
    --release-branch)        RELEASE_BRANCH="$2";       shift 2 ;;
    --prior-phases)          PRIOR_PHASES_FILE="$2";    shift 2 ;;
    --bump-kind)             BUMP_KIND="$2";            shift 2 ;;
    --source-version)        SOURCE_VERSION="$2";       shift 2 ;;
    --simulate-failure-at)   SIMULATE_FAILURE_AT="$2";  shift 2 ;;
    *)
      printf 'publish-orchestrator.sh: unknown option: %s\n' "$1" >&2
      exit 2
      ;;
  esac
done

# ─── required-argument guards ─────────────────────────────────────────────────
[[ -z "$TARGET" ]]   && { printf 'publish-orchestrator.sh: --target is required\n'   >&2; exit 2; }
[[ -z "$BUMP_SHA" ]] && { printf 'publish-orchestrator.sh: --bump-sha is required\n' >&2; exit 2; }
[[ -z "$TARBALL" ]]  && { printf 'publish-orchestrator.sh: --tarball is required\n'  >&2; exit 2; }
[[ -z "$NOTES" ]]    && { printf 'publish-orchestrator.sh: --notes is required\n'    >&2; exit 2; }
[[ -z "$BINARIES" ]] && { printf 'publish-orchestrator.sh: --binaries is required\n' >&2; exit 2; }

# Default release branch
[[ -z "$RELEASE_BRANCH" ]] && RELEASE_BRANCH="release/v${TARGET}"

# ─── global state ─────────────────────────────────────────────────────────────
INVOCATION_TS="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
START_EPOCH="$(date +%s)"

# Arrays accumulate per-substep results and diagnostics.
SUBSTEP_RESULTS=()
DIAGNOSTICS=()
SUCCEEDED_SUBSTEPS=()   # names of substeps that completed successfully (for diagnostics)

# Skill root: gates/publish -> gates -> release-agent-director
SKILL_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
# Report output dir: defaults to the skill's own dist/; tests point
# RELEASE_REPORT_DIR at an isolated per-test dir so concurrent runs never share
# the release-report.json path (b.aur). An absolute override is used verbatim.
DIST_DIR="${RELEASE_REPORT_DIR:-${SKILL_ROOT}/dist}"
mkdir -p "${DIST_DIR}"

# Split comma-separated binaries into an array
IFS=',' read -ra BINARY_PATHS <<< "${BINARIES}"

# ─── helpers ──────────────────────────────────────────────────────────────────
_now_iso() { date -u '+%Y-%m-%dT%H:%M:%SZ'; }

# ─── _resolve_abs_path ────────────────────────────────────────────────────────
# Resolve a path to an absolute path against the CURRENT working directory.
#
# This MUST be called at argument-parse time, before any substep `cd`s into a
# subdirectory (e.g. _do_npm_publish enters pkg/ts-bun-client). A relative
# --tarball such as `dist/agent-director-0.9.0.tgz` (the form the pack phase
# emits) would otherwise resolve against the wrong directory inside the subshell
# and npm publish would fail after the tag and GitHub Release are already public
# (b.mjd).
#
# An empty input is echoed back unchanged (required-arg guards handle emptiness).
# An already-absolute path is echoed back unchanged. A relative path is prefixed
# with $PWD. The path need not exist yet — existence is enforced separately by
# preflight validation (validate_publish_artifacts).
_resolve_abs_path() {
  local path="$1"
  [[ -z "$path" ]] && { printf '%s' "$path"; return 0; }
  case "$path" in
    /*) printf '%s' "$path" ;;
    *)  printf '%s/%s' "$PWD" "$path" ;;
  esac
}

# Resolve every file-input argument to an absolute path NOW, at the top level,
# while $PWD is still the caller's CWD (no substep has cd'd anywhere yet). This
# makes the values independent of the pkg/ts-bun-client subshell cd in
# _do_npm_publish and of any future cd (b.mjd). Existence of the artifacts is
# validated later by validate_publish_artifacts, and of --prior-phases by the
# snapshot below; here we only make the paths absolute.
#
# CALLER_CWD is the directory relative inputs were resolved against — captured
# here, while $PWD is still the caller's CWD. validate_publish_artifacts surfaces
# it in its failure diagnostic so an operator who passed a worktree-relative path
# from outside the worktree sees exactly which base was used (b.mjd).
CALLER_CWD="${PWD}"
TARBALL="$(_resolve_abs_path "${TARBALL}")"
NOTES="$(_resolve_abs_path "${NOTES}")"
for _i in "${!BINARY_PATHS[@]}"; do
  BINARY_PATHS[_i]="$(_resolve_abs_path "${BINARY_PATHS[_i]}")"
done
unset _i
PRIOR_PHASES_FILE="$(_resolve_abs_path "${PRIOR_PHASES_FILE}")"

# ─── --prior-phases snapshot ──────────────────────────────────────────────────
# A named --prior-phases file must be a readable regular file holding exactly
# one JSON array. Otherwise the run exits 2 here — before the artifact preflight
# and any substep, writing no report — instead of the file being dropped from,
# or emptying, the report after the irreversible substeps have run (b.2wr).
#
# The file is read exactly once, here: PRIOR_PHASES_JSON holds its array
# (compact JSON) and is the only copy write_report and emit_terminal_summary
# use, so a file deleted, emptied or rewritten while the substeps run cannot
# shift or empty the report. jq reads the file itself, and the copy reaches
# later jq calls through printf (a bash builtin) on stdin; it is never passed
# as an argument or exported, so Linux's 128 KiB MAX_ARG_STRLEN does not limit
# its size. Without --prior-phases the phases are [].
PRIOR_PHASES_JSON="[]"
if [[ -n "${PRIOR_PHASES_FILE}" ]]; then
  if [[ ! -f "${PRIOR_PHASES_FILE}" || ! -r "${PRIOR_PHASES_FILE}" ]]; then
    printf 'publish-orchestrator.sh: --prior-phases is not a readable regular file: %s\n' \
      "${PRIOR_PHASES_FILE}" >&2
    exit 2
  fi
  if ! PRIOR_PHASES_JSON="$(jq -sc \
      'if length == 1 and (.[0] | type) == "array" then .[0]
       else error("not exactly one JSON array") end' \
      "${PRIOR_PHASES_FILE}")"; then
    printf 'publish-orchestrator.sh: --prior-phases file does not hold exactly one JSON array: %s\n' \
      "${PRIOR_PHASES_FILE}" >&2
    exit 2
  fi
fi

# ─── emit_publish_diagnostic ──────────────────────────────────────────────────
# Emits an extended SR-14 diagnostic (publish-phase fields) to stderr as one
# compact JSON line and appends the same line to the DIAGNOSTICS array.
#
# Usage: emit_publish_diagnostic <substep-short-name> <description> \
#                                <corrective_action> <upstream_verbatim> \
#                                [offending_file_or_artifact]
# The 5th argument is optional; when omitted (or empty) the
# offending_file_or_artifact field is emitted as JSON null, preserving the
# behaviour of every call site that does not pass it. Callers that know the
# bad path (e.g. validate_publish_artifacts) pass the resolved absolute path so
# operators can consult the recovery-cheatsheet field directly (b.mjd).
#
# The four caller-supplied strings reach jq on stdin, never as arguments (b.nsa,
# the same rule emit_diagnostic follows since b.v46): upstream_verbatim is raw
# npm/gh/git output and the description and corrective action can embed paths,
# and one argument over Linux's MAX_ARG_STRLEN (128 KiB) makes the exec of jq
# fail with "Argument list too long", so no diagnostic at all would be written.
# Each string is turned into one JSON string by its own `jq -Rs .` (printf is a
# bash builtin, so the pipe has no size limit; -Rs keeps the value unchanged,
# trailing newlines included) and the final jq slurps the four of them as an
# array. Only the gate name and the list of succeeded substep names, both
# short, go as arguments.
emit_publish_diagnostic() {
  local substep="$1"
  local description="$2"
  local corrective="$3"
  local upstream_verbatim="$4"
  local offending="${5:-}"

  # Build prior_substeps_succeeded JSON array (at most six substep names).
  local prior_json="[]"
  if [[ ${#SUCCEEDED_SUBSTEPS[@]} -gt 0 ]]; then
    prior_json="$(printf '%s\n' "${SUCCEEDED_SUBSTEPS[@]}" | jq -R . | jq -sc '.')"
  fi

  # offending_file_or_artifact: empty → JSON null; non-empty → JSON string.
  local diag
  diag="$(
    {
      printf '%s' "$offending"         | jq -Rs .
      printf '%s' "$description"       | jq -Rs .
      printf '%s' "$corrective"        | jq -Rs .
      printf '%s' "$upstream_verbatim" | jq -Rs .
    } | jq -cs \
      --arg     gate                     "publish.${substep}" \
      --arg     which_substep_failed     "publish.${substep}" \
      --argjson prior_substeps_succeeded "$prior_json" \
      '. as [$offending, $description, $corrective_action, $upstream_response_verbatim]
      | {
          gate:                       $gate,
          offending_file_or_artifact: (if $offending == "" then null else $offending end),
          description:                $description,
          corrective_action:          $corrective_action,
          which_substep_failed:       $which_substep_failed,
          prior_substeps_succeeded:   $prior_substeps_succeeded,
          upstream_response_verbatim: $upstream_response_verbatim
        }'
  )"

  printf '%s\n' "$diag" >&2
  DIAGNOSTICS+=("$diag")
}

# ─── record_substep ───────────────────────────────────────────────────────────
# Appends a substep result JSON object to SUBSTEP_RESULTS.
#
# Usage: record_substep <full-name> <outcome> <command> <started_at> <response_excerpt>
#
# response_excerpt is a substep's raw output (on failure, the same text as the
# diagnostic's upstream_response_verbatim), so it reaches jq on stdin rather
# than as an argument, which Linux caps at 128 KiB (b.nsa).
record_substep() {
  local name="$1"
  local outcome="$2"
  local command="$3"
  local started_at="$4"
  local response_excerpt="$5"

  SUBSTEP_RESULTS+=("$(printf '%s' "$response_excerpt" | jq -Rs \
    --arg name             "$name" \
    --arg outcome          "$outcome" \
    --arg command          "$command" \
    --arg started_at       "$started_at" \
    '{
      name:             $name,
      outcome:          $outcome,
      command:          $command,
      started_at:       $started_at,
      response_excerpt: .
    }')")
}

# ─── recovery_commands ────────────────────────────────────────────────────────
# Returns the corrective-action string for each substep failure scenario.
recovery_commands() {
  local substep="$1"
  case "$substep" in
    push-branch)
      printf 'git push origin --delete %s' "${RELEASE_BRANCH}"
      ;;
    create-tag)
      printf 'git push origin --delete v%s && git tag -d v%s && git push origin --delete %s' \
        "${TARGET}" "${TARGET}" "${RELEASE_BRANCH}"
      ;;
    gh-release)
      printf 'gh release delete v%s --yes && git push origin --delete v%s && git tag -d v%s && git push origin --delete %s' \
        "${TARGET}" "${TARGET}" "${TARGET}" "${RELEASE_BRANCH}"
      ;;
    npm-publish)
      # npm publishes are permanent — no version rollback possible.
      printf 'git checkout main && git merge --ff-only %s && git push origin main && git push origin --delete %s' \
        "${RELEASE_BRANCH}" "${RELEASE_BRANCH}"
      ;;
    fast-forward-main)
      printf 'git push origin --delete %s' "${RELEASE_BRANCH}"
      ;;
    delete-remote-branch)
      printf '# Release complete. Retry branch deletion: git push origin --delete %s' \
        "${RELEASE_BRANCH}"
      ;;
    *)
      printf '# No specific recovery guidance for substep: publish.%s' "$substep"
      ;;
  esac
}

# ─── failure_description ──────────────────────────────────────────────────────
failure_description() {
  local substep="$1"
  local rc="$2"
  case "$substep" in
    push-branch)
      printf 'git push of release branch %s failed (exit %s).' \
        "${RELEASE_BRANCH}" "$rc"
      ;;
    create-tag)
      printf 'git tag or push of v%s failed (exit %s).' "${TARGET}" "$rc"
      ;;
    gh-release)
      printf 'gh release create for v%s failed (exit %s).' "${TARGET}" "$rc"
      ;;
    npm-publish)
      printf 'npm publish of tarball failed (exit %s). NOTE: npm publishes are irreversible — do NOT retry with the same version.' "$rc"
      ;;
    fast-forward-main)
      printf 'git fast-forward merge of %s into main failed (exit %s). Must be --ff-only.' \
        "${RELEASE_BRANCH}" "$rc"
      ;;
    delete-remote-branch)
      printf 'git push --delete of %s failed (exit %s). Release is otherwise complete; branch cleanup only.' \
        "${RELEASE_BRANCH}" "$rc"
      ;;
    *)
      printf 'publish.%s failed (exit %s).' "$substep" "$rc"
      ;;
  esac
}

# ─── write_report ─────────────────────────────────────────────────────────────
# Writes dist/release-report.json with all collected substep results.
#
# The prior phases, substeps and diagnostics arrays carry gate and substep
# output verbatim, so they reach jq on stdin as three JSON values rather than
# as --argjson, which Linux caps at 128 KiB per argument (b.nsa, b.2wr). The
# prior phases come from PRIOR_PHASES_JSON, the copy taken at startup, never
# from re-reading the file. jq refuses anything but exactly three arrays, so
# a missing or extra value can never shift the fields.
#
# If jq or the write fails, the partial report is removed and the failure is
# reported on stderr in place of the "written" line, and write_report returns
# non-zero.
write_report() {
  local elapsed_seconds="$1"
  local report="${DIST_DIR}/release-report.json"

  local substeps_json="[]"
  if [[ ${#SUBSTEP_RESULTS[@]} -gt 0 ]]; then
    substeps_json="$(printf '%s\n' "${SUBSTEP_RESULTS[@]}" | jq -sc '.')"
  fi

  local diagnostics_json="[]"
  if [[ ${#DIAGNOSTICS[@]} -gt 0 ]]; then
    diagnostics_json="$(printf '%s\n' "${DIAGNOSTICS[@]}" | jq -sc '.')"
  fi

  local rc=0
  printf '%s\n%s\n%s\n' "${PRIOR_PHASES_JSON}" "${substeps_json}" "${diagnostics_json}" | jq -s \
    --arg      invocation_timestamp "${INVOCATION_TS}" \
    --arg      mode                 "${MODE}" \
    --arg      bump_kind            "${BUMP_KIND}" \
    --arg      source_version       "${SOURCE_VERSION}" \
    --arg      target_version       "${TARGET}" \
    --argjson  elapsed_seconds      "${elapsed_seconds}" \
    'if length != 3 or any(.[]; type != "array") then
       error("want 3 JSON arrays (phases, publish_substeps, diagnostics), got \(map(type))")
     else . end
    | . as [$phases, $publish_substeps, $diagnostics]
    | {
      invocation_timestamp: $invocation_timestamp,
      mode:                 $mode,
      bump_kind:            $bump_kind,
      source_version:       $source_version,
      target_version:       $target_version,
      phases:               $phases,
      publish_substeps:     $publish_substeps,
      diagnostics:          $diagnostics,
      elapsed_seconds:      $elapsed_seconds
    }' > "${report}" || rc=$?

  if [[ ${rc} -ne 0 ]]; then
    rm -f "${report}"
    printf 'publish-orchestrator.sh: ERROR: release-report NOT written: jq or the write to %s failed (exit %s)\n' \
      "${report}" "${rc}" >&2
    return "${rc}"
  fi

  printf 'release-report written → %s\n' "${report}" >&2
}

# ─── emit_terminal_summary ────────────────────────────────────────────────────
emit_terminal_summary() {
  local elapsed="$1"
  local mode_label="${MODE}"

  # phases line from the --prior-phases copy taken at startup ([] without it)
  local phases_line
  phases_line="$(printf '%s' "${PRIOR_PHASES_JSON}" \
    | jq -r '[.[] | "\(.name)=\(.outcome)"] | join(", ")' 2>/dev/null || echo "(none)")"
  [[ -z "$phases_line" ]] && phases_line="(none)"

  # publish substeps line
  local publish_line=""
  local i
  for i in "${!SUBSTEP_RESULTS[@]}"; do
    local short_name outcome
    short_name="$(printf '%s' "${SUBSTEP_RESULTS[$i]}" | jq -r '.name' | sed 's/^publish\.//')"
    outcome="$(printf '%s' "${SUBSTEP_RESULTS[$i]}" | jq -r '.outcome')"
    if [[ -z "$publish_line" ]]; then
      publish_line="${short_name}=${outcome}"
    else
      publish_line="${publish_line}, ${short_name}=${outcome}"
    fi
  done

  printf 'Release v%s (%s): completed in %ss\n' \
    "${TARGET}" "${mode_label}" "${elapsed}"
  printf '  phases:    %s\n' "${phases_line}"
  printf '  publish:   %s\n' "${publish_line}"
  printf '  diagnostics: %d\n' "${#DIAGNOSTICS[@]}"
}

# ─── run_substep ──────────────────────────────────────────────────────────────
# The core execution engine for each substep.
#
# Usage: run_substep <substep-short-name> <command-display-string> <fn-name>
#
# In dry-run mode: emits "[publish.<name>] would do: <cmd-display>" to stdout,
#   records outcome=skipped, returns 0.
# In release mode:
#   - If --simulate-failure-at matches this substep: emits diagnostic, writes
#     report, exits 1 WITHOUT executing the real command (safe testing).
#   - Otherwise: calls <fn-name>, captures stderr, records outcome.
#   - On non-zero exit: emits SR-14 extended diagnostic, writes report, exits 1.
run_substep() {
  local substep="$1"
  local cmd_display="$2"
  local fn="$3"

  local started_at
  started_at="$(_now_iso)"

  # ── dry-run path ────────────────────────────────────────────────────────────
  if [[ "${MODE}" == "dry-run" ]]; then
    printf '[publish.%s] would do: %s\n' "${substep}" "${cmd_display}"
    record_substep "publish.${substep}" "skipped" "${cmd_display}" "${started_at}" "(dry-run)"
    SUCCEEDED_SUBSTEPS+=("publish.${substep}")
    return 0
  fi

  # ── release path ────────────────────────────────────────────────────────────

  # Simulate-failure-at: force exit 1 without running the real command.
  if [[ -n "${SIMULATE_FAILURE_AT}" && "${SIMULATE_FAILURE_AT}" == "${substep}" ]]; then
    local verbatim="[--simulate-failure-at] forced failure at substep publish.${substep} — real command NOT executed"
    printf '[publish.%s] SIMULATED FAILURE (--simulate-failure-at)\n' "${substep}" >&2
    local recovery description
    recovery="$(recovery_commands "${substep}")"
    description="Simulated failure at publish.${substep} (--simulate-failure-at flag). No side-effects occurred for this substep."
    emit_publish_diagnostic "${substep}" "${description}" "${recovery}" "${verbatim}"
    record_substep "publish.${substep}" "failed" "${cmd_display}" "${started_at}" "${verbatim}"
    local elapsed=$(( $(date +%s) - START_EPOCH ))
    write_report "${elapsed}"
    emit_terminal_summary "${elapsed}"
    exit 1
  fi

  # Execute the substep function, capture stderr for the diagnostic.
  printf '[publish.%s] executing: %s\n' "${substep}" "${cmd_display}" >&2
  local stderr_tmp
  stderr_tmp="$(mktemp)"
  local rc=0
  "${fn}" 2>"${stderr_tmp}" || rc=$?

  if [[ $rc -ne 0 ]]; then
    # Capture last 50 lines of stderr for the upstream_response_verbatim field.
    local upstream_verbatim
    upstream_verbatim="$(tail -n 50 "${stderr_tmp}")"
    rm -f "${stderr_tmp}"

    local recovery description
    recovery="$(recovery_commands "${substep}")"
    description="$(failure_description "${substep}" "${rc}")"

    emit_publish_diagnostic "${substep}" "${description}" "${recovery}" "${upstream_verbatim}"
    record_substep "publish.${substep}" "failed" "${cmd_display}" "${started_at}" "${upstream_verbatim}"
    local elapsed=$(( $(date +%s) - START_EPOCH ))
    write_report "${elapsed}"
    emit_terminal_summary "${elapsed}"
    exit 1
  fi

  local response_excerpt
  response_excerpt="$(tail -n 5 "${stderr_tmp}")"
  rm -f "${stderr_tmp}"

  printf '[publish.%s] OK\n' "${substep}" >&2
  record_substep "publish.${substep}" "succeeded" "${cmd_display}" "${started_at}" "${response_excerpt}"
  SUCCEEDED_SUBSTEPS+=("publish.${substep}")
}

# ─── validate_publish_artifacts ───────────────────────────────────────────────
# Preflight validation: every file the publish phase will consume as input must
# exist and be readable BEFORE any irreversible substep runs (b.mjd, AC2/AC4).
#
# The three preceding substeps (push-branch, create-tag, gh-release) are all
# irreversible and externally visible, so discovering a missing tarball at
# substep 4 strands a half-shipped release. This check runs first — before
# push-branch — and halts loudly on the first bad path, in both --release and
# --dry-run mode, so a bad artifact path is caught regardless of mode.
#
# Artifacts consumed by the publish phase:
#   - TARBALL       → npm publish            (_do_npm_publish, substep 4)
#   - NOTES         → gh release --notes-file (_do_gh_release, substep 3)
#   - BINARY_PATHS  → gh release assets       (_do_gh_release, substep 3)
#
# All three are absolute by this point (resolved at parse time). The release
# assets come in pairs (b.vqr): every agent-director-<os>-<arch> in
# BINARY_PATHS needs agent-director-admin-<os>-<arch> beside it in the list,
# and the other way round, so a release never ships one binary of a platform
# without the other (unpaired_release_binary). On failure it emits an SR-14
# diagnostic, writes the report, prints the terminal summary, and exits 1 —
# matching the substep-failure exit path, but with no substep having run
# (SUCCEEDED_SUBSTEPS is empty, so prior_substeps_succeeded is []).
validate_publish_artifacts() {
  local bad_path="" bad_kind=""

  if [[ ! -f "${TARBALL}" || ! -r "${TARBALL}" ]]; then
    bad_path="${TARBALL}"; bad_kind="--tarball (npm publish)"
  elif [[ ! -f "${NOTES}" || ! -r "${NOTES}" ]]; then
    bad_path="${NOTES}"; bad_kind="--notes (gh release --notes-file)"
  else
    local b
    for b in "${BINARY_PATHS[@]}"; do
      if [[ ! -f "${b}" || ! -r "${b}" ]]; then
        bad_path="${b}"; bad_kind="--binaries (gh release asset)"
        break
      fi
    done
  fi

  if [[ -n "${bad_path}" ]]; then
    fail_publish_artifacts "${bad_path}" \
      "preflight.publish-artifacts: ${bad_kind} path is not a readable file: ${bad_path}. Relative --tarball/--notes/--binaries inputs are resolved against the caller's working directory (${CALLER_CWD}), so this is the absolute path that was checked. Halting before any irreversible substep (push-branch/create-tag/gh-release) runs." \
      "Ensure the ${bad_kind} artifact exists and is readable at ${bad_path}. Relative inputs resolve against the caller's CWD (${CALLER_CWD}), NOT the worktree root — if you passed a worktree-relative path from outside the worktree, either cd into the worktree first or pass an absolute path, then re-run the publish phase. No release actions have been taken." \
      "artifact path does not resolve to a readable file: ${bad_path}"
  fi

  local unpaired sibling
  unpaired="$(unpaired_release_binary)"
  [[ -z "${unpaired}" ]] && return 0
  sibling="${unpaired#*|}"; unpaired="${unpaired%%|*}"
  fail_publish_artifacts "${unpaired}" \
    "preflight.publish-artifacts: --binaries lists ${unpaired} but not its pair ${sibling}: every release uploads agent-director and agent-director-admin for each platform it ships. Halting before any irreversible substep (push-branch/create-tag/gh-release) runs." \
    "Pass both ${unpaired##*/} and ${sibling} in --binaries ('make release-binaries' builds both), then re-run the publish phase. No release actions have been taken." \
    "release binary without its pair: ${unpaired} (missing ${sibling})"
}

# unpaired_release_binary prints "<path>|<pair basename>" for the first
# BINARY_PATHS entry named agent-director-<os>-<arch> or
# agent-director-admin-<os>-<arch> whose pair (the other binary of the same
# platform) no entry is named, and prints nothing when every such entry has
# its pair. Entries with other names are not release binaries and are skipped.
unpaired_release_binary() {
  local b base pair names=" "
  for b in "${BINARY_PATHS[@]}"; do
    names+="${b##*/} "
  done
  for b in "${BINARY_PATHS[@]}"; do
    base="${b##*/}"
    if [[ "${base}" =~ ^agent-director-admin-([a-z0-9]+-[a-z0-9]+)$ ]]; then
      pair="agent-director-${BASH_REMATCH[1]}"
    elif [[ "${base}" =~ ^agent-director-([a-z0-9]+-[a-z0-9]+)$ ]]; then
      pair="agent-director-admin-${BASH_REMATCH[1]}"
    else
      continue
    fi
    if [[ "${names}" != *" ${pair} "* ]]; then
      printf '%s|%s' "${b}" "${pair}"
      return 0
    fi
  done
}

# fail_publish_artifacts <offending> <description> <corrective> <upstream>
# halts the publish phase on a bad artifact input: it prints the FAILED line,
# emits the SR-14 diagnostic, records the failed preflight substep, writes the
# report, prints the terminal summary and exits 1.
fail_publish_artifacts() {
  local offending="$1" description="$2" corrective="$3" upstream="$4"
  printf '[preflight.publish-artifacts] FAILED: %s (resolved against caller CWD %s)\n' "${offending}" "${CALLER_CWD}" >&2
  emit_publish_diagnostic "preflight-publish-artifacts" "${description}" "${corrective}" "${upstream}" "${offending}"
  record_substep "publish.preflight-publish-artifacts" "failed" "validate publish artifact paths" "$(_now_iso)" "${upstream}"
  local elapsed=$(( $(date +%s) - START_EPOCH ))
  write_report "${elapsed}"
  emit_terminal_summary "${elapsed}"
  exit 1
}

# ─── substep functions ────────────────────────────────────────────────────────
# Each _do_* function runs the actual side-effecting command(s) for one substep.
# They must redirect only stderr externally (run_substep handles that); stdout
# from these functions is unredirected (passes through for progress visibility).

_do_push_branch() {
  git -C "${WORKTREE_ROOT}" push origin "${RELEASE_BRANCH}"
}

_do_create_tag() {
  git -C "${WORKTREE_ROOT}" tag -a "v${TARGET}" -m "release v${TARGET}" "${BUMP_SHA}" \
    && git -C "${WORKTREE_ROOT}" push origin "v${TARGET}"
}

_do_gh_release() {
  gh release create "v${TARGET}" \
    --notes-file "${NOTES}" \
    "${BINARY_PATHS[@]}"
}

_do_npm_publish() {
  (cd "${WORKTREE_ROOT}/pkg/ts-bun-client" && npm publish "${TARBALL}")
}

# _do_fast_forward_main
#
# main is ALREADY checked out in the parent (main) worktree, so
# `git checkout main` from inside the release worktree fails with
# "fatal: 'main' is already checked out at '<parent>'" (b.jqj). Instead of
# switching branches here, derive the parent worktree path and fast-forward
# main in place there — no checkout needed.
#
# Parent derivation: the first `worktree` entry of `git worktree list
# --porcelain` is always the main worktree (git lists the primary worktree
# first, the linked ones after). WORKTREE_ROOT is a linked worktree, so this
# yields the parent that has main checked out.
#
# SR-13.3: the parent-worktree layout is an invariant, not a guess. If the
# derived parent is empty, is not a git worktree, or does not have main
# checked out, we halt loudly rather than silently correcting or falling back
# to the old checkout behavior.
_do_fast_forward_main() {
  git -C "${WORKTREE_ROOT}" fetch origin || return $?

  local parent
  parent="$(git -C "${WORKTREE_ROOT}" worktree list --porcelain 2>/dev/null \
    | awk '/^worktree /{sub(/^worktree /,""); print; exit}')"

  if [[ -z "${parent}" ]]; then
    # shellcheck disable=SC2016 # backticks are literal prose in the message, not a command substitution
    printf 'SR-13.3: could not derive the parent (main) worktree from release worktree %s — `git worktree list --porcelain` yielded no worktree entry. Cannot fast-forward main.\n' \
      "${WORKTREE_ROOT}" >&2
    return 1
  fi

  if ! git -C "${parent}" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    printf 'SR-13.3: derived parent worktree %s is not a git worktree. Cannot fast-forward main.\n' \
      "${parent}" >&2
    return 1
  fi

  local parent_branch
  parent_branch="$(git -C "${parent}" symbolic-ref --short -q HEAD)"
  if [[ "${parent_branch}" != "main" ]]; then
    printf 'SR-13.3: derived parent worktree %s does not have main checked out (HEAD is on %s). Refusing to fast-forward — no silent correction.\n' \
      "${parent}" "${parent_branch:-<detached>}" >&2
    return 1
  fi

  git -C "${parent}" merge --ff-only "${RELEASE_BRANCH}" \
    && git -C "${parent}" push origin main
}

_do_delete_remote_branch() {
  git -C "${WORKTREE_ROOT}" push origin --delete "${RELEASE_BRANCH}"
}

# ─── preflight: validate publish-phase artifact paths ─────────────────────────
# Runs BEFORE the first irreversible substep. Halts fast on any tarball / notes
# / binary path that is not a readable file, so no public tag or Release can be
# created for a run that would fail at npm-publish on a bad input (b.mjd).
validate_publish_artifacts

# ─── 6-substep pipeline ───────────────────────────────────────────────────────

# 1. publish.push-branch
run_substep "push-branch" \
  "git push origin ${RELEASE_BRANCH}" \
  _do_push_branch

# 2. publish.create-tag
run_substep "create-tag" \
  "git tag -a v${TARGET} -m 'release v${TARGET}' ${BUMP_SHA} && git push origin v${TARGET}" \
  _do_create_tag

# 3. publish.gh-release
run_substep "gh-release" \
  "gh release create v${TARGET} --notes-file ${NOTES} ${BINARY_PATHS[*]}" \
  _do_gh_release

# 4. publish.npm-publish
run_substep "npm-publish" \
  "cd pkg/ts-bun-client && npm publish ${TARBALL}" \
  _do_npm_publish

# 5. publish.fast-forward-main
# No `git checkout main`: main is already checked out in the parent worktree,
# so the fast-forward runs there via `git -C <parent>` (b.jqj). <parent> is
# derived at run time from `git worktree list`, hence the placeholder here.
run_substep "fast-forward-main" \
  "git fetch origin && git -C <parent-worktree> merge --ff-only ${RELEASE_BRANCH} && git -C <parent-worktree> push origin main" \
  _do_fast_forward_main

# 6. publish.delete-remote-branch
run_substep "delete-remote-branch" \
  "git push origin --delete ${RELEASE_BRANCH}" \
  _do_delete_remote_branch

# ─── success: write final report + terminal summary ──────────────────────────
END_EPOCH="$(date +%s)"
ELAPSED=$(( END_EPOCH - START_EPOCH ))

write_report "${ELAPSED}"
emit_terminal_summary "${ELAPSED}"
