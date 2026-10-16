

### E3 Task3 replacement-pool ownership — coordinator

Adopted final Engineer decisions after source review closed N1/N1b/N2. This supersedes the intermediate ErrBadConn-refusal proposal, which failed multi-waiter source review and is not the final design. Independent regression validation remains pending.

handoff: callerOwnedConnector returns a resource-free lazySQLiteConn for an unmarked cancelable context, including database/sql's replacement opener. It returns nil error with real pool capacity, so no synthetic ErrBadConn enters the fixed acquisition retry budget. Concurrent queued callers receive a legitimate lease regardless of which request the pool selects; subsequent SQL or session reset opens and initializes once on that actual borrower's execution path. No goroutine, queue, mutable latest caller context or foreign marker is added.

ownership: Marked explicit acquisition keeps the existing eager path. A lazy replacement stores only the connector/native connection/error/closed flag, never a caller context. Bounded borrower SQL/reset executes on its already-admitted per-pool operation worker; if native Open, initialization or disposal blocks past cancellation, that worker retains its operation slot and the sql lease/reservation until completion. The background opener can only allocate or close an unopened logical connection, with zero native work. Legacy unbounded borrowers run synchronously as before.

deadline: Initialization uses sqliteConnector with the real borrowing method context. The zero-wait provenance marker maps the 1ms acquisition context to the existing fixed 1s SQL work context, intersected with its original enclosing context; no deadline is minted or extended. sqliteConnector checks cancellation after native open/before each initialization PRAGMA and closes failed initialization synchronously on its caller. Every borrower SQL method checks ctx.Err after initialization before starting SQL. Existing transaction commitStarted/abandoned synchronization and matched-COMMIT uncertainty remain untouched.

interfaces: Fresh pool replacements needReset=false in Go1.22.10, so ResetSession alone is insufficient. lazySQLiteConn implements context-aware Exec, Query, Prepare, BeginTx and Ping as well as mandatory Prepare/Begin/Close, SessionResetter and Validator. Context methods forward to the real modernc interfaces; missing Exec/Query methods return ErrSkip for database/sql's statement fallback; missing context prepare/begin follow database/sql cancellation/transaction-option fallbacks. Actual modernc1.34.0 supports all context methods, Pinger, ResetSession and Validator and has no NamedValueChecker; default argument conversion and returned native statements/rows/transactions stay intact.

raw_close_validation: Raw remains database/sql's wrapper exposure and executes no initialization by itself. Production Raw uses only return driver.ErrBadConn for disposal, never a concrete native type assertion; unopened Raw-discard/Close performs no native I/O. Opened Close forwards native disposal on the existing caller, failed initialization is invalid and cannot be lent as a healthy connection, and unopened IsValid remains true without opening. SessionResetter forwards actual driver reset after initialization. No worker can outlive its ownership slot because of background replacement.

contextless_methods: Mandatory driver Prepare and Begin supply Background only for explicitly contextless direct driver calls, matching the legacy driver API. database/sql always selects our PrepareContext/BeginTx methods, including its own contextless APIs whose actual caller context is Background; the pool replacement opener invokes neither. No initialization SQL runs on the pool's Background context.

N2: Existing CheckCursorPrompt error-first false,err publication retained byte-identically. No result-slot or unrelated call-site changes.

rotation: ReconcileCursorRotation and its explicit context marker remain byte-identical; its established synchronous context behavior is preserved. Queued lazy acquisition forwards its actual QueryContext/ExecContext context.

Source: tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/native-multiwaiter-fix/FINAL-report.json. Source review: native-lazy-code-review.json. Existing original deadlines, uncertainty, zero-wait split, bounded CLI audit teardown and platform limitations remain intact.
