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
//     WithNoPreTrust, WithRawNoPreTrust, WithLaunchIdentity, WithNoLaunchToken
//     (a row from before the release: no token, socket or identity, the same
//     as WithLaunchIdentity(store.LaunchIdentity{})), WithRawLabels,
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
