
### E3 Task 1 recovery epoch and input observation — coordinator, 2026-10-15

Recovery may clear the first cancellation deadline only while it still owns the captured mailbox epoch and no control is pending. A control arriving with report completion retains the original deadline and is processed before recovery clears authority. This closes review finding R5; its deterministic behavioral regression remains pending at this record.

A narrow unexported input-starter dependency supplies the real controller's existing admission and emit callbacks to tests. Public Run retains terminalLoop and the protected transcript opener. Existing runWithTranscript callers remain compatible. This permits controlled admission-before-insertion interleavings and efficient complete large submissions through real admission/mailbox handling, without copied controller logic, per-rune stress, public options or production fault hooks.

Evidence: tickets/handoff/b.ftd/Plans/b.jf1/e3-task1/epoch-input-fix/FINAL-report.json. Targeted checks pass; final behavior, documentation, full sandbox and scheduler gates remain pending. No model inputs changed.
