// Package apitest provides test fixture helpers extracted from pkg/api/*_test.go
// for use in cross-package test programs (e.g. test/envelope-diff).
//
// All helpers live in regular (non-_test.go) Go files so they can be imported
// from any package, not just from within pkg/api. The package depends only on
// internal/store, internal/spawn, stdlib, and the sqlite driver — it does NOT
// import pkg/api, which avoids any import cycle.
//
// # Migrated helpers and their prior locations
//
//   - SeedListFixture   ← pkg/api/list_test.go     :: seedListFixture
//   - SeedDeleteFixture ← pkg/api/delete_test.go   :: seedDeleteFixture
//   - SeedDecideFixture ← pkg/api/decide_test.go   :: seedDecideFixture
//   - SeedPermissionRow ← pkg/api/decide_test.go   :: seedPermissionRow
//   - SeedExpireFixture ← pkg/api/expire_test.go   :: seedExpireFixture
//   - SeedJsonl         ← pkg/api/resume_test.go   :: seedJsonl
//   - SeedStore         ← pkg/api/client_test.go   :: seedStore
//   - OpenStoreWithRow  ← pkg/api/sendkeys_test.go :: openStoreWithRow
package apitest
