#!/bin/bash
set -eu
proof_dir=$(mktemp -d)
cp /work/go.mod /work/go.sum "$proof_dir/"
cp -a /work/internal /work/pkg /work/cmd "$proof_dir/"
mkdir "$proof_dir/test"
cp -a /work/test/fake-tmux "$proof_dir/test/"
for source_path in cmd/agent-director/cursor_host_cmd.go cmd/agent-director/main.go internal/cursorhost/input.go internal/cursorhost/protocol.go internal/cursorhost/report.go internal/cursorhost/requests.go internal/cursorhost/session.go internal/cursorhost/transcript_linux.go internal/hook/handler.go; do
 git -C /work show "8fabf73e473630e2902f37d6060a5b84bd4daab0:$source_path" > "$proof_dir/$source_path"
done
cd "$proof_dir"
git apply /work/tickets/handoff/b.ftd/Plans/b.jf1/e3-task2/engineer/final-source.diff
# This appended observer did not exist in the exact initial source snapshot.
sed '/^\/\/ ObserveCursorRotation/,$d' /work/internal/store/cursor_rotation.go | sed '$d' > internal/store/cursor_rotation.go
sha256sum -c /work/tickets/handoff/b.ftd/Plans/b.jf1/e3-task2/revalidation/initial-source.sha256
# Old reporter has no observation callbacks; omit that new companion test only.
# All controller regression assertions and deadlines remain byte-identical.
sed '/^func TestReporterObservesCommittedRotationBeforeStalledHookReturns/,$d' /work/internal/cursorhost/report_linux_test.go | sed '/"context"/d' > internal/cursorhost/report_linux_test.go
env GORACE=atexit_sleep_ms=0 go test -race -count=1 -v ./internal/cursorhost -run 'TestCursorRotationCommittedHookStallDoesNotLoseShutdownIdentity/exit$|TestCursorRotationAdmissionParkedAcrossPublicationRemainsProvisional$|TestCursorRotationRetiredNumericResponsesKeepSessionBoundaries$'
