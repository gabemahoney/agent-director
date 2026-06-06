#!/usr/bin/env bash
# gate:        preflight.invariant-no-custom-build-tags
# checks:      every //go:build tag identifier in the repo is a standard Go
#              build constraint (OS, arch, cgo, unix, go1.X, ignore, or the
#              boolean operators &&/||/!)
# pass:        silent exit 0
# fail:        one SR-14 JSON line per unrecognized identifier, exit 1
#
# Standard identifiers allowed:
#   OS:    linux darwin windows freebsd openbsd netbsd dragonfly plan9
#          solaris illumos ios js wasip1 android aix hurd nacl
#   arch:  amd64 arm64 386 arm mips mips64 mips64le mipsle ppc64 ppc64le
#          riscv64 s390x sparc64 wasm loong64
#   misc:  cgo unix ignore
#   ver:   go1.N or go1.N.M  (e.g. go1.21, go1.21.0)

set -euo pipefail

GATE_LIB="$(cd "$(dirname "$0")/../lib" && pwd)"
# shellcheck source=../lib/emit-diagnostic.sh
source "${GATE_LIB}/emit-diagnostic.sh"

REPO_ROOT="$(git rev-parse --show-toplevel)"

# Regex matching every allowed bare identifier.
ALLOWED_RE='^(linux|darwin|windows|freebsd|openbsd|netbsd|dragonfly|plan9|solaris|illumos|ios|js|wasip1|android|aix|hurd|nacl|amd64|arm64|386|arm|mips|mips64|mips64le|mipsle|ppc64|ppc64le|riscv64|s390x|sparc64|wasm|loong64|cgo|unix|ignore)$'

# go1.N or go1.N.M
GOVER_RE='^go1\.[0-9]+(\.[0-9]+)?$'

FAILED=0

# git grep exits 1 when no matches; that is not an error here.
while IFS= read -r match; do
  # match format:  path/to/file.go:LINE://go:build <expr>
  fileloc="${match%%://go:build*}"
  expr="${match#*://go:build }"

  # Strip boolean operators and punctuation to isolate bare identifiers.
  # Handles: && || ! ( ) and any stray commas.
  idents="$(printf '%s' "$expr" | sed 's/[()!,]/ /g; s/&&/ /g; s/||/ /g')"

  for ident in $idents; do
    [ -z "$ident" ] && continue

    if printf '%s' "$ident" | grep -qE "$GOVER_RE"; then
      continue
    fi
    if printf '%s' "$ident" | grep -qE "$ALLOWED_RE"; then
      continue
    fi

    # Unrecognized identifier — emit SR-14 diagnostic.
    emit_diagnostic \
      "preflight.invariant-no-custom-build-tags" \
      "$fileloc" \
      "unrecognized build-tag identifier '${ident}' in ${fileloc}" \
      "Remove or replace '${ident}'. Only standard Go OS/arch/cgo/unix/go1.X/ignore constraints are permitted."
    FAILED=1
  done
done < <(git -C "$REPO_ROOT" grep -nE '^//go:build [^ ]' -- '*.go' 2>/dev/null || true)

[ "$FAILED" -eq 0 ] || exit 1
