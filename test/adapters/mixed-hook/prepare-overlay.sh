#!/usr/bin/env bash
# Build-only hook observer. Never edits SOURCE; pass the emitted overlay to Go.
set -euo pipefail
source_dir=$(realpath "$1")
mkdir -p "$2"
build_dir=$(realpath "$2")
case "$build_dir/" in "$source_dir/"*) echo 'overlay output must be outside source' >&2; exit 1 ;; esac
main="$source_dir/cmd/agent-director/main.go"
adapter="$source_dir/test/adapters/mixed-hook/observer.go.in"
test -f "$adapter"
# Fail if the hook Store-open boundary has moved instead of instrumenting a
# different Store. The production file and the production build stay unchanged.
awk '
  { print }
  $0 == "\tdefer st.Close()" {
    count++
    print "\tif err := observeMixedHookForTest(st); err != nil {"
    print "\t\tearlyFailClosed(fmt.Sprintf(\"mixed hook adapter: %v\", err))"
    print "\t\treturn hookExitCode"
    print "\t}"
  }
  END { if (count != 1) exit 1 }
' "$main" > "$build_dir/main.go"
jq -n --arg main "$main" --arg replacement "$build_dir/main.go" \
    --arg virtual "$source_dir/cmd/agent-director/mixed_hook_observer.go" --arg adapter "$adapter" \
    '{Replace: {($main): $replacement, ($virtual): $adapter}}' > "$build_dir/overlay.json"
printf '%s\n' "$build_dir/overlay.json"
