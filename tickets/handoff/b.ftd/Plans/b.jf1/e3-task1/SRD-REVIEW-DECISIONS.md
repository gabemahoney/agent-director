
### E3 Task 1 review corrections — coordinator, 2026-10-15

Source review R1–R4 is mandatory, with no ignored findings. Behavioral revalidation remains pending at this record.

- Capture a submission's cancellation epoch before admission, including any slow eligibility check; mailbox insertion cannot grant old admitted work new authority.
- Establish the first cancellation deadline for every consumed interruption or exit, including idle controls. Supervise protected control/cancellation records, waiting reports and finalization with a finite remaining budget. Preserve real-process termination evidence and never report ended after failed protected persistence.
- Treat the prompt-retired sentinel at either writer guard as known pre-write retirement, retaining the healthy transport for later explicit input. Other authority failures retain existing fail-closed behavior; actual partial writes, errors and timeouts remain uncertain and are never replayed.
- Track an accepted initial submission before runtime verification, input recording and UserPromptSubmit reporting. Retain queue ownership until dispatch transfers it to active ownership or a known-unstarted disposition completes.
- Use a narrow unexported transcript opener and minimal Stat/Write/Sync/Close file interface for deterministic persistence barriers. Public Run continues to use the real protected os.File opener with identical identity and sync checks. There is no environment option, alternate public path or production fault injection.

Evidence owner: tickets/handoff/b.ftd/Plans/b.jf1/e3-task1/review-fix/FINAL-report.json. Original failure and intermediate guard-classification failure remain preserved. Neither targeted regression passes nor these decisions imply the final unit or scheduler gate has passed.
