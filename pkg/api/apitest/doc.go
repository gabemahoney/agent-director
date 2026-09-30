// Package apitest provides test fixture helpers extracted from pkg/api/*_test.go
// for use in cross-package test programs (e.g. test/envelope-diff).
//
// All helpers live in regular (non-_test.go) Go files so they can be imported
// from any package, not just from within pkg/api. The package depends only on
// stdlib, internal/store, internal/spawn, internal/config (for the [tmux] key
// definitions), internal/testsupport/storefix, the leaf fixture-value
// packages internal/testsupport/launchfix (the test socket and the default
// pane) and internal/testsupport/procstarttimefix, github.com/google/uuid,
// github.com/BurntSushi/toml (to encode the config file) and the
// modernc.org/sqlite driver — it does NOT import pkg/api, which avoids any
// import cycle.
//
// # Config writer
//
//   - WriteTmuxConfig writes a config file whose [tmux] table holds the given
//     TmuxSetting values (TmuxInt, and TmuxFloat / TmuxString / TmuxBool for
//     the malformed cases), keyed by config.TmuxKey so no test spells a
//     [tmux] key name (SR-20.3). config.TmuxKeys() lists the nine keys in
//     table order. It is the only way pkg/api, CLI and MCP tests write
//     [tmux] settings; see its doc comment for the usage rules.
//
// # Schema-v5 seeding and store reads (SR-20.2, SR-20.3)
//
//   - SeedSpawn options for the v5 columns and raw values: WithTmuxSessionName,
//     WithStartedAt / WithEndedAt (a time.Time or raw text), WithLaunchStartedAt,
//     WithRawLaunchStartedAt, WithNoLaunchStartedAt, WithLifeNumber,
//     WithNoPreTrust, WithRawNoPreTrust, WithLaunchIdentity, WithTmuxSocket
//     (the row's recorded socket in place of TestSocket), WithNoLaunchToken
//     (a row from before the release: no token, socket or identity, the same
//     as WithLaunchIdentity(store.LaunchIdentity{})), WithNoPane (a live row
//     with no pane identity that keeps its token and socket), WithRawLabels,
//     WithRawClaudeArgs, WithRawExtraEnv, and WithSessionHistory (one archived
//     session_history entry per use, a SessionHistorySeed: session id,
//     transcript path or NULL, the life it belongs to and an optional
//     recorded_at). SeedSpawn's doc comment states the
//     SR-20.3 defaults; TestSocket is the default socket, and TestPaneID /
//     TestPanePID the default pane of a live row. To make the Recorder hold
//     a seeded row's own session (the row's socket and pane, labelled valid
//     with the row's id and token and the store's id), use
//     tmuxfix.Recorder.SeedRowSession (internal/testsupport/tmuxfix), so no
//     test copies a token or store id by hand.
//   - ReadSpawnColumns returns one row's columns raw (NULL distinguishable,
//     storage class kept), for columns no verb shows.
//   - ReadSessionHistoryAllLives returns an id's history entries from every
//     life, with each entry's life number and recorded_at, newest first.
//   - The store id (store_meta's store_id; SR-5.1, WD 2026-09-29 STORE):
//     ReadStoreID reads the file's value raw (never creating a store file;
//     ErrNoStoreID when the table or row is missing); SeedStoreID pins a
//     well-formed id (16 lowercase hex) for deterministic labels, before the
//     client or store under test opens, since an open store keeps the id it
//     read; OtherStoreID returns a well-formed id certain to differ from a
//     given one, for another store's labels.
//
// # Hooks through the gate (SR-22.9)
//
//   - Every hook write is gated on the hook's parent process being the row's
//     recorded pane process. ApplyAgentHook fires one hook event at a row as
//     its own agent (the parent is the row's pane process) and
//     ApplyForeignHook as another process carrying the row's id; both return
//     the store's store.HookApplied, so no test builds a store.HookGate by
//     hand. SeedSessionID records a session id on an existing row through
//     the gated soft refresh. The seeders make their hook writes the same
//     way, through storefix.WithSeedPane (a seed pane, written back
//     afterwards) and storefix.SeedAgentWrites; storefix has the equivalents
//     of both helpers for a store handle (storefix.ApplyAgentHook,
//     storefix.ApplyForeignHook).
//
// # Description helper (SR-20.2)
//
//   - AssertDescription checks an error description against a DescCase (a
//     Desc* constructor holding an SR-1.4 case's required and must-not
//     phrases) and always rejects SR-1.4's session-ending command forms (only
//     "retry kill later", and only for an ErrTmuxKillFailed case, excepted),
//     the tmux attach commands (attach-session, and tmux attach as whole
//     words), the opt-in's SR-6.8 spellings, a label value and the caller's
//     forbid values. AssertAgentText is the forbidden-only check for
//     manifest-like texts (the same session-ending, attach and opt-in forms,
//     so no agent-visible text tells an agent to run a tmux command; the
//     ad.launch.name_held trail and the README's "Operator actions" are not
//     checked); AssertAgentTextCase adds
//     a DescCase's phrases for a rule such a text must state (for example
//     DescSpawnLaunchTimeoutRule, DescSpawnScanRefusal). No test spells these
//     phrases or forms itself; a new case is a new Desc* constructor in
//     descriptions.go, or in a sibling file of one verb's cases
//     (descriptions_resume.go, whose DescCase.AfterResumeRestore adds the
//     restore's result a resume launch error ends with; descriptions_kill.go,
//     whose DescCase.AfterKillSent states a kill was sent), of plain spawn's
//     errors after "duplicate session" (descriptions_held.go, whose
//     DescCase.AfterHeldName adds the requested name, the holder's tmux id
//     and the end write's row sentence, never "retry later", and on an
//     unanswered re-lookup the retry guidance by the same result: the reuse
//     opt-in once the name is free when the row was ended, else the
//     launch-timeout rule), of the
//     lookup's shared Can't tell cases (descriptions_lookup.go), or of the
//     live-row sequence (descriptions_live_row.go: DescLiveRowSequence, its
//     short form, which only kill's manifest description states;
//     DescLiveRowPointer and LiveRowPointer, the one sentence that ends the
//     find-missing and spawn descriptions in its place and restates no step;
//     LiveRowSequenceCount and LiveRowPointerCount, which count statements
//     of the sequence and pointer sentences in a text separately), or of
//     find-missing's manifest texts (descriptions_find_missing.go:
//     DescFindMissingGrace, its pending-grace rule; DescFindMissingManifest,
//     its liveness and same-environment rules with SR-18.7's two
//     consequences, whose phrases DescKillManifest shares;
//     DescFindMissingField, its ids and unverified_ids result fields;
//     DescMissingNotProof, SR-18.2's full statement for a text that states
//     it in full, and DescMissingNotProofShort, its short form for the
//     kill, resume, pause, expire and delete descriptions (decision-0930e);
//     and FindMissingOwnText, which cuts find-missing's own text
//     before the pointer).
//
// Rules for tests (the same as docs/architecture.md, "apitest Seed* factory
// contract (reusable test fixtures)"):
//
//   - New tests contain no inline SQL. They seed rows through SeedSpawn and
//     its options, OpenStoreWithRow, SeedExpireFixture or the storefix
//     seeders, and read columns no verb shows through ReadSpawnColumns and
//     history through ReadSessionHistoryAllLives.
//   - They seed session history only through WithSessionHistory or the hook
//     path (a session rotation), never by writing session_history.
//   - They assert row_version deltas (after minus before, both read through
//     ReadSpawnColumns), never absolute values, except for a row the test
//     inserted itself. A seeded row's starting version depends on the seed:
//     a pending row seeded with no session id is at 0 and every other seed
//     starts above 0 (SeedSpawn's doc comment, "Seeding and row_version").
//   - Labels of this store end with its id, taken from ReadStoreID, the
//     store's StoreID() or an id pinned with SeedStoreID; another store's
//     labels use OtherStoreID. No test writes store_meta any other way.
//   - Concrete-store write failures come only from
//     storefix.InjectWriteFailure or its white-box counterpart in
//     internal/store. Writes behind a store interface fail through a failing
//     wrapper of that interface.
//
// # Migrated helpers and their prior locations
//
//   - SeedListFixture   ← pkg/api/list_test.go     :: seedListFixture
//   - SeedDeleteFixture ← pkg/api/delete_test.go   :: seedDeleteFixture
//   - SeedDecideFixture ← pkg/api/decide_test.go   :: seedDecideFixture
//   - SeedPermissionRow ← pkg/api/decide_test.go   :: seedPermissionRow
//   - SeedExpireFixture ← pkg/api/expire_test.go   :: seedExpireFixture
//   - SeedJsonl         ← pkg/api/resume_test.go   :: seedJsonl
//   - SeedJsonlUnder    ← pkg/api/resume_test.go   :: seedJsonlUnder (b.1ba)
//   - SeedStore         ← pkg/api/client_test.go   :: seedStore
//   - OpenStoreWithRow  ← pkg/api/sendkeys_test.go :: openStoreWithRow
//
// # Migrated from pkg/api/export_for_helper.go (helper-tag retirement, b.wvr E1)
//
//   - SeedSpawn              ← HelperSeedSpawn
//   - SeedParentChild        ← HelperSeedParentChild
//   - SeedPermissionRequest  ← HelperSeedPermissionRequest
//   - PermissionRequestSeed  ← PermissionRequestSeed (result type for SeedPermissionRequest)
//   - SeedTemplate           ← HelperSeedTemplate
//   - InitStore              ← HelperInitStore
package apitest
