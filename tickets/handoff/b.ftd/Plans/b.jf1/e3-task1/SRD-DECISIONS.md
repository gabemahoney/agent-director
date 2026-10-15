
## E3 Task 1 technical decisions — coordinator, 2026-10-15

Task t2.jf1.r8.dt extends ordered input and finite cancellation. These are implementation decisions, subject to behavioral verification and review before the local commit; no integration or authenticated acceptance is claimed.

- Bound the waiting FIFO to 32 submissions and 8 MiB of text, in addition to one dispatched/active prompt. Preserve the existing 1 MiB individual input limit. Refuse excess input explicitly rather than replacing accepted work.
- Give each accepted submission an identity independent of its ACP request ID. Protected lifecycle records distinguish admission, dispatch start, cancellation, uncertain termination, updates and outcomes. Allocate the ACP request before synchronizing its start record, but gate outbound bytes on successful persistence.
- Keep controls independent of ordinary input capacity and retire affected prompt/decision authority immediately. Preserve the first active cancellation deadline. Known retirement before any write permits later explicit input in the same conversation; partial or uncertain output is never replayed.
- Verify prompt dispatch through an internal bounded CheckCursorPrompt authority transaction. An empty in-memory request map is insufficient evidence that durable permission/plan holds are gone. Preserve existing unknown-origin proof requirements and bounded answer guards.
- Separate bounded pane rendering from input consumption so slow rendering does not block controls. Rendering loss produces an inspection notice. Persisted protected records remain the inspection owner; no screen-derived state is introduced.
- Allow late, exactly correlated tool proof to discharge only its own retained hold after turn completion. Completion alone supplies no proof.
- Keep clear/new refused until Task 2. No public control API, schema migration, resume/load/reuse or life switching is added here.

Model assessment: existing hold models do not represent the new accepted FIFO, cancellation epochs or transcript-to-writer gate. This is a coverage gap, not a counterexample or a claim of proof. Assess the settled design and record any justified model work. Only actual changed spec/tla inputs warrant affected-model scheduler checks; unchanged baseline suites are not requested.
