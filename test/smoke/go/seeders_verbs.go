package smoke_test

// Seeder entries for the verbs that need no tmux session: the launch,
// read, permission and housekeeping verbs (see seeders.go for the spec).

import (
	"context"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

func init() {
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
			// Override retention to zero so the ended row is selected
			// regardless of config defaults.
			d := time.Duration(0)
			return c.Expire(&d)
		},
		// expire declares no ErrorNames in the manifest — a row it does
		// not delete is kept and listed in kept_ids, not a verb error.
		Error: nil,
		Expired: func(result any) api.ExpireResult {
			return result.(api.ExpireResult)
		},
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
