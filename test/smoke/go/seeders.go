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
//     row). This indirection keeps the seeder files free of internal/store
//     imports (only stdlib + pkg/api + manifest + internal/testsupport/*
//     are allowed per the import-graph guard).
//   - SeedID: the claude_instance_id the seeded row will carry. The Happy
//     closure references the same id when calling the verb method.
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
//   - Pane: for read-pane, reads the Happy result's pane; the driver checks
//     it is smokePaneText, the text the row's own pane captures.
//   - SentText: for send-keys and pause, the text Happy sends; the driver
//     checks the Recorder saw it sent to the seeded row's pane by its pane id,
//     then one Enter to that pane.
//   - Expired: for expire, returns the Happy result; the driver checks the
//     seeded row is the one deleted, none is kept and the row is gone.
//
// This file holds the spec type and the registry; the entries live in
// seeders_verbs.go (verbs that need no tmux) and seeders_tmux.go (verbs that
// reach the row's tmux session).
//
// Adding a new callable verb to the manifest requires adding a matching
// entry. The driver's startup check fails the build with a clear message
// naming any verb in manifest.CallableVerbs() that lacks an entry.
package smoke_test

import (
	"context"
	"time"

	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// seedKind enumerates the precondition shapes the driver knows how to set
// up. Each value maps to one storefix.Seed* or apitest.SeedSpawn call in
// the driver's setup switch.
type seedKind int

const (
	// seedNone means no DB row is seeded. Used for verbs whose happy path
	// does not require any pre-existing row (spawn, find-missing, version,
	// make-template).
	seedNone seedKind = iota

	// seedCheckPermission seeds a spawn in StateCheckPermission with
	// relay_mode=on and an open permission_requests row. Used by decide.
	seedCheckPermission

	// seedResumable seeds a spawn in StateEnded with claude_session_id
	// populated AND writes a JSONL placeholder on disk so the resume
	// verb's os.Stat pre-flight passes. HOME must be redirected first
	// (the smoke TestMain does this).
	seedResumable

	// seedExpired seeds an ended row through apitest.SeedSpawn (its SR-20.3
	// defaults: apitest.TestSocket, a launch token, no pane and no process
	// identity) with an old ended_at, and gives the Recorder a server on that
	// socket holding one unlabelled session that is not the row's (SR-20.3).
	// Used by expire, so its happy path looks the socket up, reads Gone and
	// deletes the row (SR-12.1).
	seedExpired

	// seedPendingLaunch seeds a pending row whose launch start is
	// smokeLaunchStartMillis, through apitest.SeedSpawn and
	// WithLaunchStartedAt. Used by status, get and list (SR-22.2).
	seedPendingLaunch

	// seedLiveSession seeds a working row through apitest.SeedSpawn (its
	// SR-20.3 defaults: apitest.TestSocket, a launch token and the pane
	// apitest.TestPaneID, whose pid reads gone) and the row's own labelled
	// session into the Recorder (SeedRowSession). Used by kill, so its happy
	// path finds the session, sends the kills and sees the agent gone, and by
	// send-keys, so its happy path takes the Ours path and sends into the
	// agent's pane by its pane id (SR-7.2).
	seedLiveSession

	// seedReadPane seeds a working row as seedLiveSession does and swaps the
	// driver's Recorder for tmuxfix.NewRecorderForReadPane's: the row's own
	// labelled session, whose pane captures smokePaneText by its pane id.
	// Used by read-pane, so its happy path takes the Ours path (SR-7.2).
	seedReadPane

	// seedPause seeds a waiting row through apitest.SeedSpawn (the same
	// SR-20.3 defaults) and swaps the driver's Recorder for
	// tmuxfix.NewRecorderForPause's, with an after-call hook on the Enter
	// that ends the row as its own agent (a SessionEnd through
	// apitest.ApplyAgentHook). Used by pause, so its happy path takes the
	// Ours path, sends /exit and Enter by pane id and its wait sees the row
	// ended (SR-7.2, SR-20.3).
	seedPause
)

// smokePaneText is the capture text seedReadPane scripts for the row's own
// pane; non-empty so the manifest's AllowEmpty pane field is exercised with
// content.
const smokePaneText = "smoke-pane-output"

// smokeSentText is the text the send-keys spec sends.
const smokeSentText = "hello"

// smokeExitText is the text pause sends to the agent's pane.
const smokeExitText = "/exit"

// smokeLaunchStartMillis is the launch start seedPendingLaunch records, in
// ms since the Unix epoch; its non-zero millisecond part (.123) checks that
// the verbs keep millisecond precision.
const smokeLaunchStartMillis int64 = 1790000000123

// smokeEndedAt is the ended_at seedExpired records: a fixed past time, so the
// row is old under any retention window.
var smokeEndedAt = time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC)

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

	// HappyCtxDeadline, when non-zero, bounds the context the driver passes
	// to verbs that take one (pause, find-missing); zero means 5s. Pause
	// uses a short one so a wait that never sees the row ended fails the
	// subtest instead of hanging the suite.
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

	// Pane, when non-nil, returns the pane the Happy result shows. The
	// driver asserts it is smokePaneText, the text seedReadPane scripts for
	// the row's own pane. Set by read-pane only.
	Pane func(result any) string

	// SentText, when non-empty, is the text Happy sends. The driver asserts
	// the Recorder saw one call carrying it, to apitest.TestPaneID (the pane
	// the row records) on apitest.TestSocket, with Enter, and one Enter to
	// that pane. Set by send-keys and pause.
	SentText string

	// Expired, when non-nil, returns the Happy result as an
	// api.ExpireResult. The driver asserts the seeded row is the one row
	// deleted, none is kept (kept 0, kept_ids []) and the row is gone from
	// the store (SR-12.1). Set by expire only.
	Expired func(result any) api.ExpireResult
}

// seeders is the canonical registry: one entry per callable verb. The
// driver iterates manifest.CallableVerbs() and looks each verb's name
// up in this map; a missing entry fails the test with a clear message
// (see TestSmokeAllVerbs's startup check in smoke_test.go).
//
// The map is populated by the init functions of seeders_verbs.go and
// seeders_tmux.go, so each entry can reference the verb's own VerbDef from
// manifest.Lookup (mustVerb) next to the verb name string.
var seeders = map[string]seederSpec{}

// bogusID is the claude_instance_id used in error-path Happy/Error closures
// — chosen to be very unlikely to collide with any seeded row.
const bogusID = "smoke-bogus-id-does-not-exist"

// mustVerb returns the manifest's VerbDef for name; it panics when the
// manifest has none, so a renamed verb fails the package at init.
func mustVerb(name string) manifest.VerbDef {
	vd, ok := manifest.Lookup(name)
	if !ok {
		panic("seeders init: manifest.Lookup(" + name + ") not found")
	}
	return vd
}
