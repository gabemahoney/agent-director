// Package smoke_test — verb-seeder registry.
//
// Each entry in the seeders map associates a callable verb name (from
// manifest.CallableVerbs()) with everything the driver needs to exercise it
// against a fresh per-test store:
//
//   - Manifest: the VerbDef, used by the asserts.go helpers to know which
//     result fields and error names to expect.
//   - SeedKind: a coarse-grained tag the driver dispatches on to call the
//     right storefix.Seed* helper (or apitest.SeedSpawn for a pending
//     row). This indirection keeps seeders.go free of internal/store
//     imports (only stdlib + pkg/api + manifest + internal/testsupport/*
//     are allowed per the import-graph guard).
//   - SeedID: the claude_instance_id the seeded row will carry. The Happy
//     closure references the same id when calling the verb method.
//   - PaneText: scripted CapturePane response, set on the recorder before
//     read-pane runs.
//   - Happy: invokes the verb method on the supplied Client in its happy
//     path. Returns the result struct and any error; the driver feeds the
//     result into AssertResultMatchesManifest.
//   - Error: invokes the verb method with deliberately bad input so the
//     driver can feed the returned error into AssertExpectedError.
//   - LaunchStartedAt: for status, get and list, reads the launch start the
//     Happy result shows for the seeded row; the driver checks it.
//   - PreTrust: for spawn and resume, reads the Happy result's pre_trust; the
//     driver plants HOME/.claude.json first and checks it is "ok".
//   - TmuxSocket: for get, reads the Happy result's tmux_socket; the driver
//     checks it is the socket the seeded row records.
//   - KillSent: for kill, reads the Happy result's kill_sent; the driver
//     checks it is true.
//
// Adding a new callable verb to the manifest requires adding a matching
// entry here. The driver's startup check fails the build with a clear
// message naming any verb in manifest.CallableVerbs() that lacks an entry.
package smoke_test

import (
	"context"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// seedKind enumerates the precondition shapes the driver knows how to set
// up. Each value maps to one storefix.Seed* or apitest.SeedSpawn call in
// the driver's setup switch.
type seedKind int

const (
	// seedNone means no DB row is seeded. Used for verbs whose happy path
	// does not require any pre-existing row (find-missing, expire, version,
	// make-template).
	seedNone seedKind = iota

	// seedLive seeds a spawn in StateWorking — a live, interactive row
	// usable by kill, read-pane and delete.
	seedLive

	// seedWaiting seeds a spawn in StateWaiting — used by send-keys
	// happy paths that need an interactive non-working state. (pause's
	// happy-path uses seedEnded instead; see the pause spec.)
	seedWaiting

	// seedEnded seeds a spawn in StateEnded — used by pause (which
	// short-circuits to no-op success when the row is already terminal).
	seedEnded

	// seedCheckPermission seeds a spawn in StateCheckPermission with
	// relay_mode=on and an open permission_requests row. Used by decide.
	seedCheckPermission

	// seedResumable seeds a spawn in StateEnded with claude_session_id
	// populated AND writes a JSONL placeholder on disk so the resume
	// verb's os.Stat pre-flight passes. HOME must be redirected first
	// (the smoke TestMain does this).
	seedResumable

	// seedExpired seeds a terminal row whose ended_at is back-dated 8
	// days, so it qualifies for expiry under the default retention
	// window. Used by expire's happy path.
	seedExpired

	// seedPendingLaunch seeds a pending row whose launch start is
	// smokeLaunchStartMillis, through apitest.SeedSpawn and
	// WithLaunchStartedAt. Used by status, get and list (SR-22.2).
	seedPendingLaunch

	// seedLiveSession seeds a working row through apitest.SeedSpawn (its
	// SR-20.3 defaults: apitest.TestSocket, a launch token and the pane
	// apitest.TestPaneID, whose pid reads gone) and the row's own labelled
	// session into the Recorder (SeedRowSession). Used by kill, so its happy
	// path finds the session, sends the kills and sees the agent gone.
	seedLiveSession
)

// smokeLaunchStartMillis is the launch start seedPendingLaunch records, in
// ms since the Unix epoch; its non-zero millisecond part (.123) checks that
// the verbs keep millisecond precision.
const smokeLaunchStartMillis int64 = 1790000000123

// seederSpec carries everything the driver needs to exercise one verb.
type seederSpec struct {
	// Manifest is the VerbDef from manifest.CallableVerbs() — fed to
	// the asserts.go helpers so they know which fields to check.
	Manifest manifest.VerbDef

	// SeedKind tells the driver which seed helper to call before
	// constructing the Client.
	SeedKind seedKind

	// SeedID is the claude_instance_id the seeded row uses. Happy
	// references this same id when calling the verb method.
	SeedID string

	// PaneText is the scripted CapturePane response for the read-pane
	// verb. Empty for other verbs.
	PaneText string

	// HappyCtx, when non-nil, is the context the driver passes to verbs
	// that take a context (pause, find-missing). Verbs that don't take
	// a context ignore this field.
	//
	// For pause specifically we use a short (2s) deadline so the
	// poll-loop cannot hang the suite — though for the happy path
	// (seedEnded), pause returns no-op success before the loop runs.
	HappyCtxDeadline time.Duration

	// Happy calls the verb method on c in its happy path. id is the
	// SeedID of the seeded row (empty for verbs that don't reference a
	// row). The returned result is fed to AssertResultMatchesManifest.
	Happy func(c *api.Client, id string, ctx context.Context) (any, error)

	// Error calls the verb method on c with deliberately bad input to
	// trigger one of the verb's declared ErrorNames. The returned err
	// is fed to AssertExpectedError.
	Error func(c *api.Client, ctx context.Context) error

	// LaunchStartedAt, when non-nil, returns the launch start the Happy
	// result shows for the seeded row id (nil when absent). The driver
	// asserts it equals smokeLaunchStartMillis, in UTC. Set by the specs
	// seeded with seedPendingLaunch.
	LaunchStartedAt func(result any, id string) *time.Time

	// PreTrust, when non-nil, returns the pre_trust the Happy result
	// reports. The driver plants a .claude.json lacking the folder-trust
	// entry in the per-subtest HOME before Happy and asserts "ok"
	// (SR-22.6). Set by the launch verbs, spawn and resume.
	PreTrust func(result any) string

	// TmuxSocket, when non-nil, returns the tmux_socket the Happy result
	// shows. The driver asserts it is apitest.TestSocket, the socket
	// seedPendingLaunch records (SR-3.3). Set by get only.
	TmuxSocket func(result any) string

	// KillSent, when non-nil, returns the kill_sent the Happy result shows.
	// The driver asserts it is true: the kill was sent to the session
	// seedLiveSession seeds (SR-20.3). Set by kill only.
	KillSent func(result any) bool
}

// seeders is the canonical registry: one entry per callable verb. The
// driver iterates manifest.CallableVerbs() and looks each verb's name
// up in this map; a missing entry fails the test with a clear message
// (see TestSmokeAllVerbs's startup check in smoke_test.go).
//
// The map is populated in init() so we can reference the verb's own
// VerbDef from manifest.Lookup — keeps the source of truth for the
// VerbDef pointer next to the verb name string.
var seeders = map[string]seederSpec{}

// bogusID is the claude_instance_id used in error-path Happy/Error closures
// — chosen to be very unlikely to collide with any seeded row.
const bogusID = "smoke-bogus-id-does-not-exist"

func init() {
	mustVerb := func(name string) manifest.VerbDef {
		vd, ok := manifest.Lookup(name)
		if !ok {
			panic("seeders init: manifest.Lookup(" + name + ") not found")
		}
		return vd
	}

	// ── spawn ─────────────────────────────────────────────────────────────
	seeders["spawn"] = seederSpec{
		Manifest: mustVerb("spawn"),
		SeedKind: seedNone, // spawn creates its own row
		SeedID:   "smoke-spawn-id",
		Happy: func(c *api.Client, id string, _ context.Context) (any, error) {
			return c.Spawn(api.SpawnParams{
				ClaudeInstanceID: id,
				CWD:              "/tmp",
				RelayMode:        "off",
			})
		},
		Error: func(c *api.Client, _ context.Context) error {
			// ErrCwdMissing — empty CWD violates the spawn precondition.
			_, err := c.Spawn(api.SpawnParams{})
			return err
		},
		PreTrust: func(result any) string {
			return result.(api.SpawnResult).PreTrust
		},
	}

	// ── status ────────────────────────────────────────────────────────────
	seeders["status"] = seederSpec{
		Manifest: mustVerb("status"),
		SeedKind: seedPendingLaunch,
		SeedID:   "smoke-status-id",
		Happy: func(c *api.Client, id string, _ context.Context) (any, error) {
			return c.Status(id)
		},
		Error: func(c *api.Client, _ context.Context) error {
			_, err := c.Status(bogusID)
			return err
		},
		LaunchStartedAt: func(result any, _ string) *time.Time {
			return result.(api.StatusResult).LaunchStartedAt
		},
	}

	// ── get ───────────────────────────────────────────────────────────────
	seeders["get"] = seederSpec{
		Manifest: mustVerb("get"),
		SeedKind: seedPendingLaunch,
		SeedID:   "smoke-get-id",
		Happy: func(c *api.Client, id string, _ context.Context) (any, error) {
			return c.Get(id)
		},
		Error: func(c *api.Client, _ context.Context) error {
			_, err := c.Get(bogusID)
			return err
		},
		LaunchStartedAt: func(result any, _ string) *time.Time {
			return result.(api.SpawnRow).LaunchStartedAt
		},
		TmuxSocket: func(result any) string {
			return result.(api.SpawnRow).TmuxSocket
		},
	}

	// ── send-keys ─────────────────────────────────────────────────────────
	seeders["send-keys"] = seederSpec{
		Manifest: mustVerb("send-keys"),
		SeedKind: seedWaiting, // interactive state required
		SeedID:   "smoke-send-keys-id",
		Happy: func(c *api.Client, id string, _ context.Context) (any, error) {
			return c.SendKeys(api.SendKeysParams{
				ClaudeInstanceID: id,
				Text:             "hello",
			})
		},
		Error: func(c *api.Client, _ context.Context) error {
			_, err := c.SendKeys(api.SendKeysParams{
				ClaudeInstanceID: bogusID,
				Text:             "hello",
			})
			return err
		},
	}

	// ── read-pane ─────────────────────────────────────────────────────────
	seeders["read-pane"] = seederSpec{
		Manifest: mustVerb("read-pane"),
		SeedKind: seedLive,
		SeedID:   "smoke-read-pane-id",
		// Non-empty pane text so the manifest's AllowEmpty pane field
		// still encodes consistently; the driver scripts this into the
		// recorder via WithPaneOutput before constructing the Client.
		PaneText: "smoke-pane-output",
		Happy: func(c *api.Client, id string, _ context.Context) (any, error) {
			return c.ReadPane(api.ReadPaneParams{
				ClaudeInstanceID: id,
				NLines:           5,
			})
		},
		Error: func(c *api.Client, _ context.Context) error {
			_, err := c.ReadPane(api.ReadPaneParams{
				ClaudeInstanceID: bogusID,
			})
			return err
		},
	}

	// ── kill ──────────────────────────────────────────────────────────────
	seeders["kill"] = seederSpec{
		Manifest: mustVerb("kill"),
		SeedKind: seedLiveSession,
		SeedID:   "smoke-kill-id",
		Happy: func(c *api.Client, id string, _ context.Context) (any, error) {
			return c.Kill(api.KillParams{ClaudeInstanceID: id})
		},
		Error: func(c *api.Client, _ context.Context) error {
			_, err := c.Kill(api.KillParams{ClaudeInstanceID: bogusID})
			return err
		},
		KillSent: func(result any) bool {
			return result.(api.KillResult).KillSent
		},
	}

	// ── decide ────────────────────────────────────────────────────────────
	seeders["decide"] = seederSpec{
		Manifest: mustVerb("decide"),
		SeedKind: seedCheckPermission,
		SeedID:   "smoke-decide-id",
		Happy: func(c *api.Client, id string, _ context.Context) (any, error) {
			// SeedCheckPermission seeds the open row using storefix.TestRequestTokenA.
			return c.Decide(api.DecideParams{
				ClaudeInstanceID: id,
				RequestToken:     storefix.TestRequestTokenA,
				Decision:         "allow",
			})
		},
		Error: func(c *api.Client, _ context.Context) error {
			// Invalid decision string — triggers ErrInvalidDecision
			// before the store is even hit. Robust against the seeded
			// row's state.
			_, err := c.Decide(api.DecideParams{
				ClaudeInstanceID: bogusID,
				Decision:         "maybe",
			})
			return err
		},
	}

	// ── get-permission ────────────────────────────────────────────────────
	seeders["get-permission"] = seederSpec{
		Manifest: mustVerb("get-permission"),
		// Reuse the check_permission fixture: it seeds an open row keyed
		// under storefix.TestRequestTokenA, which is exactly what the
		// happy-path GetPermission call resolves.
		SeedKind: seedCheckPermission,
		SeedID:   "smoke-get-permission-id",
		Happy: func(c *api.Client, _ string, _ context.Context) (any, error) {
			return c.GetPermission(api.GetPermissionParams{
				RequestToken: storefix.TestRequestTokenA,
			})
		},
		Error: func(c *api.Client, _ context.Context) error {
			// Token never written → ErrPermissionRequestNotFound.
			_, err := c.GetPermission(api.GetPermissionParams{
				RequestToken: "deadbeef-dead-4dea-adea-deadbeefdead",
			})
			return err
		},
	}

	// ── resume ────────────────────────────────────────────────────────────
	seeders["resume"] = seederSpec{
		Manifest: mustVerb("resume"),
		SeedKind: seedResumable,
		SeedID:   "smoke-resume-id",
		Happy: func(c *api.Client, id string, _ context.Context) (any, error) {
			return c.Resume(api.ResumeParams{ClaudeInstanceID: id})
		},
		Error: func(c *api.Client, _ context.Context) error {
			_, err := c.Resume(api.ResumeParams{ClaudeInstanceID: bogusID})
			return err
		},
		PreTrust: func(result any) string {
			return result.(api.ResumeResult).PreTrust
		},
	}

	// ── find-missing ──────────────────────────────────────────────────────
	seeders["find-missing"] = seederSpec{
		Manifest: mustVerb("find-missing"),
		SeedKind: seedNone, // no row needed; sweeps an empty live set
		SeedID:   "",
		Happy: func(c *api.Client, _ string, ctx context.Context) (any, error) {
			return c.FindMissing(ctx)
		},
		// find-missing has no verb-surface error to trigger: its only
		// declared error, ErrProbeUnsupported, stays listed by SR-1.7
		// but find-missing no longer returns it. Skipping the error
		// assertion is handled by the driver when Error is nil.
		Error: nil,
	}

	// ── expire ────────────────────────────────────────────────────────────
	seeders["expire"] = seederSpec{
		Manifest: mustVerb("expire"),
		SeedKind: seedExpired,
		SeedID:   "smoke-expire-id",
		Happy: func(c *api.Client, _ string, _ context.Context) (any, error) {
			// Override retention to zero so the back-dated row is
			// reaped regardless of config defaults.
			d := time.Duration(0)
			return c.Expire(&d)
		},
		// expire declares no ErrorNames in the manifest — per-row
		// failures surface in the result map, not as a verb error.
		Error: nil,
	}

	// ── delete ────────────────────────────────────────────────────────────
	seeders["delete"] = seederSpec{
		Manifest: mustVerb("delete"),
		SeedKind: seedLive,
		SeedID:   "smoke-delete-id",
		Happy: func(c *api.Client, id string, _ context.Context) (any, error) {
			return c.Delete([]string{id})
		},
		// delete declares no ErrorNames — missing ids are reported in
		// the per-row results map, not as a verb-level error.
		Error: nil,
	}

	// ── make-template ─────────────────────────────────────────────────────
	seeders["make-template"] = seederSpec{
		Manifest: mustVerb("make-template"),
		SeedKind: seedNone, // template is created on disk under HOME
		SeedID:   "",
		Happy: func(c *api.Client, _ string, _ context.Context) (any, error) {
			return c.MakeTemplate(api.MakeTemplateParams{
				Name: "smoke-template",
				CWD:  "/tmp",
			})
		},
		Error: func(c *api.Client, _ context.Context) error {
			// Unsafe name (path separator) — triggers ErrTemplateNameUnsafe.
			_, err := c.MakeTemplate(api.MakeTemplateParams{
				Name: "a/b",
			})
			return err
		},
	}

	// ── list ──────────────────────────────────────────────────────────────
	seeders["list"] = seederSpec{
		Manifest: mustVerb("list"),
		SeedKind: seedPendingLaunch,
		SeedID:   "smoke-list-id",
		Happy: func(c *api.Client, _ string, _ context.Context) (any, error) {
			return c.List(api.ListParams{})
		},
		Error: func(c *api.Client, _ context.Context) error {
			// Invalid label format triggers ErrListInvalidLabel before
			// the store is reached.
			_, err := c.List(api.ListParams{Labels: []string{"no-equals-sign"}})
			return err
		},
		LaunchStartedAt: func(result any, id string) *time.Time {
			for _, r := range result.(api.ListResult).Spawns {
				if r.ClaudeInstanceID == id {
					return r.LaunchStartedAt
				}
			}
			return nil
		},
	}

	// ── pause ─────────────────────────────────────────────────────────────
	//
	// Important: we seed the row in StateEnded so pause short-circuits to
	// no-op success (PauseResult is an empty struct — nothing to assert),
	// avoiding the 200ms*N polling loop entirely. The 2s context deadline
	// is a safety net; the happy path never enters the poll loop because
	// the state-switch returns before /exit is sent. The error path uses
	// a bogus id to trigger ErrSpawnNotFound, which also returns
	// immediately without polling.
	seeders["pause"] = seederSpec{
		Manifest:         mustVerb("pause"),
		SeedKind:         seedEnded,
		SeedID:           "smoke-pause-id",
		HappyCtxDeadline: 2 * time.Second,
		Happy: func(c *api.Client, id string, ctx context.Context) (any, error) {
			return c.Pause(ctx, api.PauseParams{ClaudeInstanceID: id})
		},
		Error: func(c *api.Client, ctx context.Context) error {
			_, err := c.Pause(ctx, api.PauseParams{ClaudeInstanceID: bogusID})
			return err
		},
	}

	// ── version ───────────────────────────────────────────────────────────
	seeders["version"] = seederSpec{
		Manifest: mustVerb("version"),
		SeedKind: seedNone,
		SeedID:   "",
		Happy: func(c *api.Client, _ string, _ context.Context) (any, error) {
			return c.Version()
		},
		// version declares no ErrorNames.
		Error: nil,
	}
}
