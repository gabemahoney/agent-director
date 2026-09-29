// Package apitest provides test fixture helpers extracted from pkg/api/*_test.go
// for use in cross-package test programs (e.g. test/envelope-diff).
//
// All helpers live in regular (non-_test.go) Go files so they can be imported
// from any package, not just from within pkg/api. The package depends only on
// internal/store, internal/spawn, internal/config (for the [tmux] key
// definitions), stdlib, the sqlite driver and github.com/BurntSushi/toml (to
// encode the config file) — it does NOT import pkg/api, which avoids any
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
