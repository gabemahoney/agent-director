package smoke_test

import (
	"context"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// TestSmokeAllVerbs is the comprehensive smoke driver for pkg/api.
//
// It iterates manifest.CallableVerbs() via t.Run(verb.Name, ...), and per
// subtest:
//
//  1. Asserts the seeders registry has a matching entry (startup check).
//  2. Opens a fresh storefix.OpenTempStore and a fresh tmuxfix.NewRecorder
//     — no state crosses verbs.
//  3. Applies the verb's SeedKind precondition via the appropriate
//     storefix helper.
//  4. Constructs an api.Client wired with the temp store path and the
//     recorder. CreateIfMissing is true so api.New reuses the store
//     file created by storefix.OpenTempStore.
//  5. Calls the verb's Happy closure and feeds the result into
//     AssertResultMatchesManifest.
//  6. Calls the verb's Error closure (when defined) and feeds the
//     returned error into AssertExpectedError.
//
// Crucial: pause uses a 2s context deadline, but its happy-path seed
// (seedEnded) makes the verb short-circuit before the poll-loop runs.
// The error-path uses a bogus id so ErrSpawnNotFound returns immediately
// — also no polling.
//
// No verb chaining: each subtest's seed is independent of every other.
func TestSmokeAllVerbs(t *testing.T) {
	// ── Step 1: startup check — every callable verb has a seeder ────────────
	callable := manifest.CallableVerbs()
	for _, vd := range callable {
		if _, ok := seeders[vd.Name]; !ok {
			t.Errorf("TestSmokeAllVerbs: missing seeder for callable verb %q "+
				"— add an entry to seeders.go (see the file's doc comment)", vd.Name)
		}
	}
	// Fail fast: don't run any subtests if the registry is incomplete.
	if t.Failed() {
		return
	}

	// ── Step 2: iterate callable verbs ──────────────────────────────────────
	for _, vd := range callable {
		vd := vd // capture loop var
		spec := seeders[vd.Name]

		t.Run(vd.Name, func(t *testing.T) {
			runVerbSubtest(t, vd, spec)
		})
	}
}

// runVerbSubtest is the per-verb driver. Each invocation gets a fresh
// store, recorder, and Client; cleanup is registered via t.Cleanup so
// resources are released even when assertions fail mid-test.
//
// The store value lives only in this function — its concrete type
// (*store.Store) is inferred from storefix.OpenTempStore so this file
// does not need an internal/store import. The import-graph test in
// import_graph_test.go enforces that invariant.
func runVerbSubtest(t *testing.T, vd manifest.VerbDef, spec seederSpec) {
	t.Helper()

	// Per-subtest HOME override: makes resume's JSONL placeholder and
	// make-template's templates/ directory land under a fresh per-subtest
	// temp directory. Without this, a second iteration under `go test
	// -count=2` would see leftover state from the first iteration —
	// most visibly, make-template would surface ErrTemplateExists on the
	// second call because the .toml file from the first call still exists.
	// t.Setenv restores the prior HOME on t.Cleanup automatically.
	t.Setenv("HOME", t.TempDir())

	// Fresh store and recorder per subtest. storefix.OpenTempStore
	// registers a t.Cleanup that closes the store internally.
	st, storePath := storefix.OpenTempStore(t)
	rec := tmuxfix.NewRecorder()

	// Apply the per-verb seed precondition. The switch is inline so
	// the typed *store.Store handle (st) stays local — keeping
	// seeders.go free of internal/store imports per the import-graph
	// guard. Each branch calls one storefix.Seed* helper.
	switch spec.SeedKind {
	case seedNone:
		// no row needed (spawn, find-missing, expire-empty,
		// make-template, version)
	case seedLive:
		storefix.SeedLiveSpawn(t, st, spec.SeedID)
	case seedWaiting:
		storefix.SeedPaused(t, st, spec.SeedID)
	case seedEnded:
		storefix.SeedKilled(t, st, spec.SeedID)
	case seedCheckPermission:
		storefix.SeedCheckPermission(t, st, spec.SeedID)
	case seedResumable:
		storefix.SeedResumable(t, st, spec.SeedID)
	case seedExpired:
		// 8 days back-dated — comfortably older than typical 7-day
		// retention so Expire(d=0) reaps it regardless.
		storefix.SeedExpiredCandidate(t, st, storePath, spec.SeedID,
			8*24*time.Hour)
	default:
		t.Fatalf("runVerbSubtest: unknown SeedKind %v for verb %q",
			spec.SeedKind, vd.Name)
	}

	// Per-verb recorder configuration. read-pane is the only verb that
	// reads a scripted pane response; other verbs use the recorder's
	// no-op defaults.
	if spec.PaneText != "" {
		rec.WithPaneOutput(spec.PaneText)
	}

	// Construct the Client. CreateIfMissing=true reuses the store file
	// storefix created. ConfigPath points at a non-existent file under
	// t.TempDir() so api.New's config.Load returns defaults (config.Load
	// gracefully handles a missing file).
	c, err := api.New(api.Options{
		StorePath:       storePath,
		ConfigPath:      storePath + ".config-does-not-exist.toml",
		TmuxClient:      rec,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	// ── Happy path ──────────────────────────────────────────────────────
	ctx, cancel := buildHappyCtx(spec)
	defer cancel()
	result, happyErr := spec.Happy(c, spec.SeedID, ctx)
	if happyErr != nil {
		t.Fatalf("happy-path %s: unexpected error: %v", vd.Name, happyErr)
	}
	AssertResultMatchesManifest(t, vd, result)

	// ── Error path (when declared) ──────────────────────────────────────
	if spec.Error != nil {
		errCtx, errCancel := buildHappyCtx(spec)
		defer errCancel()
		err := spec.Error(c, errCtx)
		AssertExpectedError(t, vd, err)
	}
}

// buildHappyCtx returns a context.Context appropriate for the verb's
// HappyCtxDeadline. Verbs with a zero deadline get a 5s safety-net
// deadline. pause uses 2s explicitly via the spec.
func buildHappyCtx(spec seederSpec) (context.Context, context.CancelFunc) {
	d := spec.HappyCtxDeadline
	if d == 0 {
		d = 5 * time.Second
	}
	return context.WithTimeout(context.Background(), d)
}
