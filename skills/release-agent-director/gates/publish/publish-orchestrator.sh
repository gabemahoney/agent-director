#!/usr/bin/env bash
# publish-orchestrator.sh — publish phase end-to-end for agent-director releases.
#
# Executes 6 substeps in order (--release mode) or emits "would do" lines and
# exits 0 (--dry-run mode, the default). Halts on the first substep failure with
# a structured SR-14 extended diagnostic and a partial run report. On success,
# writes dist/release-report.json and a human-readable terminal summary.
#
# Usage:
#   bash publish-orchestrator.sh \
#     --target <version>            # bare semver, e.g. 0.7.5 (no leading "v")
#     --bump-sha <commit-sha>       # SHA the tag should point at
#     --tarball <path-to-npm-tgz>   # tarball produced by the pack phase
#     --notes <path-to-notes.md>    # release notes file for gh release
#     --binaries <comma-sep-paths>  # CLI binaries to attach as gh release assets
#     [--release | --dry-run]       # default: dry-run
#     [--worktree-root <path>]      # defaults to "."
#     [--release-branch <name>]     # defaults to "release/v<target>"
#     [--prior-phases <json-file>]  # JSON array from earlier phases; default []
#     [--bump-kind <patch|minor|major>]  # for the report; default "unknown"
#     [--source-version <semver>]        # for the report; default "unknown"
#     [--simulate-failure-at <substep>]  # DEBUG: force a substep to exit 1 in
#                                        # --release mode without executing it
#
# "would do" lines (dry-run) go to stdout.
# SR-14 diagnostics and progress annotations go to stderr.
# dist/release-report.json is always written (even on failure, for triage).
#
# --simulate-failure-at accepts substep short names (without the "publish." prefix):
#   push-branch | create-tag | gh-release | npm-publish | fast-forward-main | delete-remote-branch
#
# Substeps (in order):
#   1. publish.push-branch          git push origin <release-branch>
#   2. publish.create-tag           git tag -a v<target> ... && git push origin v<target>
#   3. publish.gh-release           gh release create v<target> --notes-file <notes> <binaries...>
#   4. publish.npm-publish          npm publish <tarball>
#   5. publish.fast-forward-main    git fetch + checkout main + merge --ff-only + push
#   6. publish.delete-remote-branch git push origin --delete <release-branch>
#
# Exit codes:
#   0  success (all substeps passed, or dry-run)
#   1  substep failure (halt-on-failure; SR-14 diagnostic emitted to stderr)
#   2  argument error

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
DIST_DIR="${SKILL_ROOT}/dist"
mkdir -p "${DIST_DIR}"

# Split comma-separated binaries into an array
IFS=',' read -ra BINARY_PATHS <<< "${BINARIES}"

# ─── helpers ──────────────────────────────────────────────────────────────────
_now_iso() { date -u '+%Y-%m-%dT%H:%M:%SZ'; }

# JSON-escape: backslash → \\, double-quote → \", newline → \n
_jesc() {
  printf '%s' "$1" \
    | sed -e 's/\\/\\\\/g' \
          -e 's/"/\\"/g' \
          -e ':a;N;$!ba;s/\n/\\n/g'
}

# ─── emit_publish_diagnostic ──────────────────────────────────────────────────
# Emits an extended SR-14 diagnostic (publish-phase fields) to stderr and
# appends the JSON string to the DIAGNOSTICS array.
#
# Usage: emit_publish_diagnostic <substep-short-name> <description> \
#                                <corrective_action> <upstream_verbatim>
emit_publish_diagnostic() {
  local substep="$1"
  local description="$2"
  local corrective="$3"
  local upstream_verbatim="$4"

  # Build prior_substeps_succeeded JSON array
  local prior_json="[]"
  if [[ ${#SUCCEEDED_SUBSTEPS[@]} -gt 0 ]]; then
    prior_json="$(printf '%s\n' "${SUCCEEDED_SUBSTEPS[@]}" | jq -R . | jq -sc '.')"
  fi

  local diag
  diag="$(jq -n \
    --arg gate                    "publish.${substep}" \
    --arg description             "$description" \
    --arg corrective_action       "$corrective" \
    --arg which_substep_failed    "publish.${substep}" \
    --argjson prior_substeps_succeeded "$prior_json" \
    --arg upstream_response_verbatim   "$upstream_verbatim" \
    '{
      gate:                       $gate,
      offending_file_or_artifact: null,
      description:                $description,
      corrective_action:          $corrective_action,
      which_substep_failed:       $which_substep_failed,
      prior_substeps_succeeded:   $prior_substeps_succeeded,
      upstream_response_verbatim: $upstream_response_verbatim
    }')"

  printf '%s\n' "$diag" >&2
  DIAGNOSTICS+=("$diag")
}

# ─── record_substep ───────────────────────────────────────────────────────────
# Appends a substep result JSON object to SUBSTEP_RESULTS.
#
# Usage: record_substep <full-name> <outcome> <command> <started_at> <response_excerpt>
record_substep() {
  local name="$1"
  local outcome="$2"
  local command="$3"
  local started_at="$4"
  local response_excerpt="$5"

  SUBSTEP_RESULTS+=("$(jq -n \
    --arg name             "$name" \
    --arg outcome          "$outcome" \
    --arg command          "$command" \
    --arg started_at       "$started_at" \
    --arg response_excerpt "$response_excerpt" \
    '{
      name:             $name,
      outcome:          $outcome,
      command:          $command,
      started_at:       $started_at,
      response_excerpt: $response_excerpt
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
write_report() {
  local elapsed_seconds="$1"

  local substeps_json="[]"
  if [[ ${#SUBSTEP_RESULTS[@]} -gt 0 ]]; then
    substeps_json="$(printf '%s\n' "${SUBSTEP_RESULTS[@]}" | jq -sc '.')"
  fi

  local diagnostics_json="[]"
  if [[ ${#DIAGNOSTICS[@]} -gt 0 ]]; then
    diagnostics_json="$(printf '%s\n' "${DIAGNOSTICS[@]}" | jq -sc '.')"
  fi

  local prior_phases_json="[]"
  if [[ -n "${PRIOR_PHASES_FILE}" && -f "${PRIOR_PHASES_FILE}" ]]; then
    prior_phases_json="$(cat "${PRIOR_PHASES_FILE}")"
  fi

  jq -n \
    --arg      invocation_timestamp "${INVOCATION_TS}" \
    --arg      mode                 "${MODE}" \
    --arg      bump_kind            "${BUMP_KIND}" \
    --arg      source_version       "${SOURCE_VERSION}" \
    --arg      target_version       "${TARGET}" \
    --argjson  phases               "${prior_phases_json}" \
    --argjson  publish_substeps     "${substeps_json}" \
    --argjson  diagnostics          "${diagnostics_json}" \
    --argjson  elapsed_seconds      "${elapsed_seconds}" \
    '{
      invocation_timestamp: $invocation_timestamp,
      mode:                 $mode,
      bump_kind:            $bump_kind,
      source_version:       $source_version,
      target_version:       $target_version,
      phases:               $phases,
      publish_substeps:     $publish_substeps,
      diagnostics:          $diagnostics,
      elapsed_seconds:      $elapsed_seconds
    }' > "${DIST_DIR}/release-report.json"

  printf 'release-report written → %s/release-report.json\n' "${DIST_DIR}" >&2
}

# ─── emit_terminal_summary ────────────────────────────────────────────────────
emit_terminal_summary() {
  local elapsed="$1"
  local mode_label="${MODE}"

  # phases line from prior-phases file
  local phases_line="(none)"
  if [[ -n "${PRIOR_PHASES_FILE}" && -f "${PRIOR_PHASES_FILE}" ]]; then
    phases_line="$(jq -r '[.[] | "\(.name)=\(.outcome)"] | join(", ")' \
      "${PRIOR_PHASES_FILE}" 2>/dev/null || echo "(none)")"
    [[ -z "$phases_line" ]] && phases_line="(none)"
  fi

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

_do_fast_forward_main() {
  git -C "${WORKTREE_ROOT}" fetch origin \
    && git -C "${WORKTREE_ROOT}" checkout main \
    && git -C "${WORKTREE_ROOT}" merge --ff-only "${RELEASE_BRANCH}" \
    && git -C "${WORKTREE_ROOT}" push origin main
}

_do_delete_remote_branch() {
  git -C "${WORKTREE_ROOT}" push origin --delete "${RELEASE_BRANCH}"
}

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
  "gh release create v${TARGET} --notes-file ${NOTES} ${BINARIES}" \
  _do_gh_release

# 4. publish.npm-publish
run_substep "npm-publish" \
  "cd pkg/ts-bun-client && npm publish ${TARBALL}" \
  _do_npm_publish

# 5. publish.fast-forward-main
run_substep "fast-forward-main" \
  "git fetch origin && git checkout main && git merge --ff-only ${RELEASE_BRANCH} && git push origin main" \
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
