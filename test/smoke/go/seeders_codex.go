package smoke_test

// Seeder entries for the Codex rows of the launch verbs, spawn and resume
// (see seeders.go for the spec; run by TestSmokeCodexLaunch).

import (
	"context"

	"github.com/gabemahoney/agent-director/pkg/api"
)

func init() {
	// ── spawn as codex ────────────────────────────────────────────────────
	codexSeeders["spawn"] = seederSpec{
		Manifest: mustVerb("spawn"),
		SeedKind: seedNone, // spawn creates its own row
		SeedID:   "smoke-codex-spawn-id",
		Agent:    "codex",
		Happy: func(c *api.Client, id string, _ context.Context) (any, error) {
			return c.Spawn(api.SpawnParams{
				InstanceID: id,
				CWD:        "/tmp",
				Agent:      "codex",
				RelayMode:  "off",
			})
		},
		Error: func(c *api.Client, _ context.Context) error {
			// ErrSpawnDeniedFlag: --remote attaches a Codex agent to a
			// remote service, which a launch refuses.
			_, err := c.Spawn(api.SpawnParams{
				InstanceID: "smoke-codex-refused-id",
				CWD:        "/tmp",
				Agent:      "codex",
				AgentArgs:  []string{"--remote", "ws://example.invalid"},
			})
			return err
		},
		PreTrust: func(result any) string {
			return result.(api.SpawnResult).PreTrust
		},
	}

	// ── resume as codex ───────────────────────────────────────────────────
	codexSeeders["resume"] = seederSpec{
		Manifest: mustVerb("resume"),
		SeedKind: seedResumable,
		SeedID:   "smoke-codex-resume-id",
		Agent:    "codex",
		Happy: func(c *api.Client, id string, _ context.Context) (any, error) {
			return c.Resume(api.ResumeParams{InstanceID: id})
		},
		Error: func(c *api.Client, _ context.Context) error {
			_, err := c.Resume(api.ResumeParams{InstanceID: bogusID})
			return err
		},
		PreTrust: func(result any) string {
			return result.(api.ResumeResult).PreTrust
		},
	}
}
