#!/usr/bin/env bash
# gate:     install.clean-env + install.verify-pkg
# checks:   (1) tarball installs cleanly into a fresh npm environment;
#           (2) the installed package's main export resolves and exports Client.
# usage:    bash install-verify.sh [--worktree-root <path>] --tarball <path-to-tgz>
# pass:     both sub-checks pass, exit 0, temp dir cleaned up
# fail:     SR-14 diagnostic to stderr, exit 1
#
# NOTE: verify-installed-pkg.ts has a complex invocation contract — its
# --smoke mode constructs a real Client that spawns the platform CLI binary,
# which is not present in a clean install env. This gate therefore uses a
# minimal bun import check instead:
#   bun --eval "import { Client } from 'agent-director'; if (typeof Client !== 'function') throw new Error('Client not a constructor');"
# run from INSTALL_DIR, so resolution is scoped to the freshly-installed
# node_modules. This confirms the dist/index.js is parseable and exports
# Client correctly — the structural guarantee install.verify-pkg needs.

set -uo pipefail

GATE_LIB="$(cd "$(dirname "$0")/../lib" && pwd)"
# shellcheck source=../lib/emit-diagnostic.sh
source "${GATE_LIB}/emit-diagnostic.sh"

# ─── argument parsing ─────────────────────────────────────────────────────────
WORKTREE_ROOT="."
TARBALL=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --worktree-root)
      WORKTREE_ROOT="$2"
      shift 2
      ;;
    --tarball)
      TARBALL="$2"
      shift 2
      ;;
    *)
      printf 'install-verify.sh: unknown option: %s\n' "$1" >&2
      exit 2
      ;;
  esac
done

if [[ -z "$TARBALL" ]]; then
  printf 'install-verify.sh: --tarball is required\n' >&2
  exit 2
fi

# Resolve tarball to an absolute path before we cd anywhere.
REPO_ROOT="$(cd "$WORKTREE_ROOT" && pwd)"
if [[ "$TARBALL" = /* ]]; then
  TARBALL_ABS="$TARBALL"
else
  TARBALL_ABS="${REPO_ROOT}/${TARBALL}"
fi

if [[ ! -f "$TARBALL_ABS" ]]; then
  emit_diagnostic \
    "install.clean-env" \
    "$TARBALL" \
    "tarball not found at '${TARBALL_ABS}'" \
    "Run pack-first.sh to produce the tarball before running install-verify.sh."
  exit 1
fi

# ─── create isolated install environment ──────────────────────────────────────
# Placed outside the repo so it cannot pollute the worktree or be picked up
# by bun's node_modules resolution walking upward from the repo root.
INSTALL_DIR="$(mktemp -d)"

cleanup_install() {
  rm -rf "$INSTALL_DIR"
}
trap cleanup_install EXIT

# ─── gate: install.clean-env ──────────────────────────────────────────────────
# Minimal package.json required so `npm install` treats the dir as a package.
(cd "$INSTALL_DIR" && npm init -y >/dev/null 2>&1)

NPM_OUT="$(cd "$INSTALL_DIR" && npm install "$TARBALL_ABS" --no-package-lock --no-save 2>&1)"
NPM_EXIT=$?

if [[ $NPM_EXIT -ne 0 ]]; then
  emit_diagnostic \
    "install.clean-env" \
    "$TARBALL" \
    "npm install failed (exit ${NPM_EXIT}): $(printf '%s' "$NPM_OUT" | head -5 | tr '\n' ' ')" \
    "Inspect the tarball with 'tar -tzf ${TARBALL_ABS}' and confirm it contains a valid package.json and dist/."
  # Preserve INSTALL_DIR for operator inspection — cancel the cleanup trap.
  trap - EXIT
  printf 'install-verify.sh: preserved install dir for inspection: %s\n' "$INSTALL_DIR" >&2
  exit 1
fi

# ─── gate: install.verify-pkg ─────────────────────────────────────────────────
# Verify the installed package's main export resolves and exports Client.
# We use bun --eval run from INSTALL_DIR so the bare "agent-director" import
# resolves against the freshly-installed node_modules, not the repo tree.
#
# verify-installed-pkg.ts --smoke was not used here because its --smoke mode
# spawns the real CLI binary (for client.version()), which is absent in a
# clean install env without platform-package dependencies. The import check
# below provides the structural guarantee this gate requires.
VERIFY_OUT="$(cd "$INSTALL_DIR" && bun --eval \
  "import { Client } from 'agent-director'; if (typeof Client !== 'function') throw new Error('Client is not a constructor — got: ' + typeof Client);" 2>&1)"
VERIFY_EXIT=$?

if [[ $VERIFY_EXIT -ne 0 ]]; then
  emit_diagnostic \
    "install.verify-pkg" \
    "$TARBALL" \
    "installed package export check failed: $(printf '%s' "$VERIFY_OUT" | head -5 | tr '\n' ' ')" \
    "Ensure dist/index.js is included in the packed tarball's 'files' list and exports Client. Run 'bun run build' in pkg/ts-bun-client and repack."
  # Preserve INSTALL_DIR for operator inspection.
  trap - EXIT
  printf 'install-verify.sh: preserved install dir for inspection: %s\n' "$INSTALL_DIR" >&2
  exit 1
fi

# ─── pass ─────────────────────────────────────────────────────────────────────
exit 0
