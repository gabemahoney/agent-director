

### E3 Task3 guarded no-match transaction — coordinator

A guarded decision UPDATE that returns no row is a precommit no-match outcome: roll its transaction back rather than COMMIT. Return false,nil to allow existing race-aware refusal classification when the callback/operation is still valid; retain helper ErrStoreBusy on precommit expiry. Only potentially applied verdicts attempt COMMIT and retain unconfirmed-outcome handling on COMMIT error.

Adopted Engineer decision. Source repair is permission.go-only; independent no-match regression and batched architecture note precede the next full run. Source and evidence: tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/legacy-commit-assessment/FINAL-report.json. This does not identify either historical09 failure cause.
