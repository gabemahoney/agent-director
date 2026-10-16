# Dry-run raw capture

One authorized fresh canonical sandbox targeted test reproduced `TestDryRun` failure in 14.948s, with 29 failed checks. Canonical HEAD f0e9b48f2ef03d0d5804b215d77d16f78108c5db remains unchanged; git status is clean. No statuses or commits changed.

Only an ignored copy of `tools/measure-exit/agent.go` was modified, adding scrubbed `%q` stdout/stderr to the existing missing-session error. `GOFLAGS=-overlay=/work/.../_snapshot/overlay.json` was inherited by the child helper build: the new marker in `test.log` proves this. Checks, deadlines, timing and retries were untouched. Fresh HOME=/home/sandbox, TMPDIR unset, umask022, isolated run-local global git config and cleared inherited environment match the canonical setup. No Bun prerequisites were needed for the existing targeted test.

`test.log:176` (also `raw-example.txt`) contains the simple one-session response:

```text
stdout="$0_ad1 90ec2573956d99e7 $0 496ea019-8b42-44a5-b737-ae225946474d 26d7195ff959fdd1\n" stderr=""
```

The expected instance is `496ea019-8b42-44a5-b737-ae225946474d`. Its complete five-field owner value is present and agrees with session `$0`. The separator between session ID and owner is ASCII underscore, not tab. `identify` asks for `#{session_id}\t#{@ad_owner}` but then `strings.Cut(line, "\t")` finds no tab and every row is rejected. This proves a response-format/parser mismatch; the label is not missing. `osExec` in runlog.go returns subprocess buffer bytes without rewriting, and the requested scrubber only replaces credential values. The capture does not establish the underlying tmux implementation reason for delimiter conversion.

The next engineering step is to correct the helper's format/parser contract and inspect the analogous list-panes format in identify (agent.go:292-301), then validate unchanged existing tests. No canonical fix or further test was authorized for this capture-only lane. The historical COMMIT deadline failure remains separate and unproven; this finding must not be described as its cause.

Artifacts: run.sh, overlay.json, overlay.patch, copied agent.go, provenance.json, environment.log, sandbox.log, test.log, test.exit, raw-example.txt, FINAL-report.json and artifact-manifest.json. All artifacts remain under this ignored `_snapshot` directory. No .claude files were read, copied or hashed; no whole HOME or broad tmp directory was archived. No full suite or extra helper invocation ran. No questions.
