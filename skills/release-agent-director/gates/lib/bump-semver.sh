#!/usr/bin/env bash
# gates/lib/bump-semver.sh — SemVer bump helper.
#
# Sourceable as a library:
#   source "$(dirname "$0")/../lib/bump-semver.sh"
#   result=$(bump_semver 0.7.4 patch)    # → 0.7.5
#
# Or executable (prints to stdout):
#   ./gates/lib/bump-semver.sh 0.7.4 patch   # → 0.7.5
#
# Usage:
#   bump_semver <source-version> <bump-kind>
#
# bump-kind: patch | minor | major
#
# Prints the bumped version to stdout. Exits 1 on bad input.
# The source-version must be X.Y.Z (no leading 'v').

bump_semver() {
  local src="$1"
  local kind="$2"

  if [[ -z "$src" || -z "$kind" ]]; then
    printf 'bump_semver: usage: bump_semver <source-version> <patch|minor|major>\n' >&2
    return 1
  fi

  # Validate source is bare X.Y.Z semver.
  if ! [[ "$src" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
    printf 'bump_semver: invalid source version "%s" (expected X.Y.Z)\n' "$src" >&2
    return 1
  fi

  local major="${BASH_REMATCH[1]}"
  local minor="${BASH_REMATCH[2]}"
  local patch="${BASH_REMATCH[3]}"

  case "$kind" in
    patch)
      patch=$(( patch + 1 ))
      ;;
    minor)
      minor=$(( minor + 1 ))
      patch=0
      ;;
    major)
      major=$(( major + 1 ))
      minor=0
      patch=0
      ;;
    *)
      printf 'bump_semver: invalid bump-kind "%s" (expected patch, minor, or major)\n' "$kind" >&2
      return 1
      ;;
  esac

  printf '%d.%d.%d\n' "$major" "$minor" "$patch"
}

# When executed directly (not sourced), run as a CLI tool.
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  bump_semver "$@"
fi
