set -eu
umask 077
out=tickets/handoff/b.ftd/Plans/b.jf1/e3-integration-fixes/list-engineer
go test ./internal/cursorhost -run '^(TestCursorE3ToolUpdateBeforePlanAdmission|TestCursorE3FatalRunClosesMissing|TestCursorE3PublicReadsReconcileKilledGateway)$' -count=1 -v -timeout 90s > "$out/original-green.log" 2>&1
go test ./pkg/api -run '^(TestCursorReadGatewayVerdicts|TestCursorReadLostGatewayRefusesUnsafeSuccess|TestCursorReadConcurrentLaunchChange|TestClientSettledReads|TestClientSettledReadsKeepRawExtras|TestListFilters|TestListFailures|TestListShowsRowAgent|TestListRowKeySetUnchanged|TestLivenessFieldsGetAndList|TestGetAndListHideCodexConfigFolder|TestTmuxSocketStatusAndListOmit)$' -count=1 -v -timeout 90s > "$out/api-targeted-green.log" 2>&1
go test ./internal/store -run '^TestCursorMissingRefusesReplacedAuthority$' -count=1 -v -timeout 90s > "$out/store-guards-green.log" 2>&1
cat "$out/original-green.log" "$out/api-targeted-green.log" "$out/store-guards-green.log"
