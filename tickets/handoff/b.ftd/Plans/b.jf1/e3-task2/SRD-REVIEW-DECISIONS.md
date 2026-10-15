

### E3 Task 2 source-review corrections — coordinator, 2026-10-15

These decisions supersede the initial one-shot publication-observation claim. Existing S1–S3 causal regressions now pass; exact R1/R2 revalidation is still pending. No external acceptance is claimed.

- Supersede the original Task2 claim that a one-shot buffered hook completion is sufficient publication observation. Observe actual atomic runtime/row/required-history snapshots while the hook is alive; observing commit and completing the hook/report are distinct.
- Cancel the hook on consumed intentional exit; reconcile after confirmed hook exit and carry actual observed committed identity into owned-runtime finalization. All observation/join/store acquisition remains bounded by the original first-control deadline; exhaustion remains uncertainty/failure rather than rollback or successful ended.
- Treat provisional status as part of the consumption-time admission snapshot, alongside epoch and session, and preserve it through insertion. Promotion alone transfers to admitted fresh-session queue ownership after existing durable guards.
- Every known unstarted queue owner gets a visible per-ID cancellation even when protected persistence has already failed. No successful protected disposition or terminal state is fabricated.
- Known-retired protected records retain their actual known conversation; retired-response origin is bounded by per-conversation numeric request boundaries. Unknown origin stays unknown.
