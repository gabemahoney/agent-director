

### E3 Task 2 pre-start cancellation decisions — coordinator, 2026-10-15

Engineer decisions adopted for the SRD; independent behavioral revalidation remains pending.

- A failed publication-hook Start is a terminal hook boundary requiring final reconciliation just as confirmed child exit does. Parent cancellation must not cancel the bounded authoritative read before its original deadline.
- A false committed query is not proof of the outgoing binding. Reconciliation must distinguish known unchanged outgoing, known committed fresh and unknown/error; only the first two permit consumed intentional shutdown to continue normal verified finalization.
- Keep evidence atomic in one real store snapshot. Reuse full runtime/launch/pane identity predicates and the existing full examined-row snapshot predicate for unchanged proof; require outgoing current-life history for fresh proof and its absence for unchanged proof.
- Preserve the committed-only ObserveCursorRotation reader while adding a production reconciliation result for the reporter. No new hook, public fault flag or test seam is introduced.
- Neither SQL identity knowledge nor hook launch failure proves runtime death or durable SessionEnd. Those remain the existing independently verified finalization obligations under the same original bound.

Evidence: `prestart-fix/FINAL-report.json`. The deterministic causal regression is reporter-level; an exact full-Run pre-start interleaving is not claimed.
