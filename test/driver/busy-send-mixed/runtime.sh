#!/usr/bin/env bash
# Shared sandbox/Docker generation builder. SOURCE is an immutable snapshot;
# output and any Go overlays belong to BUILD, never to the shared checkout.
set -euo pipefail
[[ ${AGENT_DIRECTOR_TEST_SANDBOX:-} == 1 ]] || { echo 'sandbox required' >&2; exit 1; }
source_dir=$1
build_dir=$2
mkdir -p "$build_dir"
overlay=$(bash "$source_dir/test/adapters/mixed-hook/prepare-overlay.sh" "$source_dir" "$build_dir/overlay")
# Observe the real notification entry without replacing its Chtimes behavior.
# Unlike filesystem event counts this records each invocation deterministically.
python3 - "$source_dir" "$build_dir" "$overlay" <<'PY'
from pathlib import Path
import json, sys
source, build, overlay = Path(sys.argv[1]), Path(sys.argv[2]), Path(sys.argv[3])
obj = json.loads(overlay.read_text())
route = source / 'internal/store/busy_send_route.go'
text = route.read_text()
needle = 'func TouchBusySendWake(path string) error {'
assert text.count(needle) == 1
replacement = build / 'wake-observed.go'
replacement.write_text(text.replace(needle, needle + '\n\tmixedWakeObserved(path)'))
observer = build / 'wake-observer.go'
observer.write_text("""package store
import ("encoding/json"; "os"; "github.com/gabemahoney/agent-director/internal/version")
func mixedObserved(variable string, event any) {
    audit := os.Getenv(variable)
    if audit == "" { return }
    file, err := os.OpenFile(audit, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
    if err != nil { panic(err) }
    defer file.Close()
    if err := json.NewEncoder(file).Encode(event); err != nil { panic(err) }
}
func mixedWakeObserved(path string) {
    mixedObserved("AD_MIXED_WAKE_AUDIT", map[string]any{"path":path, "pid":os.Getpid(), "version":version.Version})
}
func init() {
    executable, err := os.Executable()
    if err != nil { panic(err) }
    mixedObserved("AD_MIXED_PROCESS_AUDIT", map[string]any{"path":executable, "pid":os.Getpid(), "version":version.Version})
}
""")
obj['Replace'][str(route)] = str(replacement)
obj['Replace'][str(source / 'internal/store/mixed_wake_observer.go')] = str(observer)
overlay.write_text(json.dumps(obj))
# Only this separately built delegate has a synthetic authoring table.
cover = json.loads(json.dumps(obj))
coverage = source / 'internal/store/busy_send_coverage.go'
text = coverage.read_text()
needle = '[...]string{AgentClaude}'
assert text.count(needle) == 1
coverfile = build / 'covering-coverage.go'
coverfile.write_text(text.replace(needle, '[...]string{AgentClaude, AgentCodex}'))
cover['Replace'][str(coverage)] = str(coverfile)
setter = build / 'covering-table.go'
setter.write_text("""package store
func SetMixedCapabilities(c BusySendLaunchCapabilities) {
    busySendCapabilityTable = append(busySendCapabilityTable, BusySendCapabilities{Provider:AgentCodex, HeldCount:c.HeldCount, HeldBytes:c.HeldBytes, Modes:c.Modes, ConfirmFromTranscripts:BusySendConfirmCannotConfirm})
}
""")
cover['Replace'][str(source / 'internal/store/mixed_covering_table.go')] = str(setter)
main = build / 'covering-main.go'
main.write_text("""package main
import ("github.com/gabemahoney/agent-director/internal/store"; "github.com/gabemahoney/agent-director/internal/testsupport/storefix")
func init() { store.SetMixedCapabilities(*storefix.BusySendLaterCapabilities()) }
""")
for package in ['cmd/agent-director-admin', 'test/driver/busy-send-mixed']:
    cover['Replace'][str(source / package / 'mixed_covering_main.go')] = str(main)
(build / 'covering-overlay.json').write_text(json.dumps(cover))
PY
for generation in old new; do
    case "$generation" in
        old) stamp=9.0.0-mixed-old; commit=1111111111111111111111111111111111111111 ;;
        new) stamp=9.0.0-mixed-new; commit=2222222222222222222222222222222222222222 ;;
    esac
    mkdir -p "$build_dir/$generation"
    flags="-X github.com/gabemahoney/agent-director/internal/version.Version=$stamp -X github.com/gabemahoney/agent-director/internal/version.Commit=$commit"
    go -C "$source_dir" build -overlay "$overlay" -buildvcs=false -ldflags "$flags" -o "$build_dir/$generation/agent-director" ./cmd/agent-director
    go -C "$source_dir" build -buildvcs=false -ldflags "$flags" -o "$build_dir/$generation/agent-director-admin" ./cmd/agent-director-admin
    go -C "$source_dir" build -overlay "$overlay" -buildvcs=false -ldflags "$flags" -o "$build_dir/$generation/driver" ./test/driver/busy-send-mixed
    go -C "$source_dir" test -c -ldflags "$flags" -o "$build_dir/$generation/store-driver" ./internal/store
    "$build_dir/$generation/agent-director-admin" --home "$build_dir/retained-home" publish-version --binary "$build_dir/$generation/agent-director" > "$build_dir/$generation/publication.json"
done

# Synthetic later covering writer: same core, one test-only capability row.
# The retained old/new sender builds above have neither that row nor its route.
mkdir -p "$build_dir/covering"
flags="-X github.com/gabemahoney/agent-director/internal/version.Version=9.0.0-mixed-covering -X github.com/gabemahoney/agent-director/internal/version.Commit=3333333333333333333333333333333333333333"
go -C "$source_dir" build -overlay "$build_dir/covering-overlay.json" -buildvcs=false -ldflags "$flags" -o "$build_dir/covering/agent-director" ./test/driver/busy-send-mixed
go -C "$source_dir" build -overlay "$build_dir/covering-overlay.json" -buildvcs=false -ldflags "$flags" -o "$build_dir/covering/agent-director-admin" ./cmd/agent-director-admin
