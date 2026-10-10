package smoke_test

// Seeder entries for the verbs that reach the row's tmux session (send-keys,
// read-pane, record-pane-answer, kill, pause); see seeders.go for the spec.

import (
	"context"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

func init() {
	// ── send-keys ─────────────────────────────────────────────────────────
	seeders["send-keys"] = seederSpec{
		Manifest: mustVerb("send-keys"),
		SeedKind: seedLiveSession, // the row's own session and pane (Ours)
		SeedID:   "smoke-send-keys-id",
		SentText: smokeSentText,
		Happy: func(c *api.Client, id string, _ context.Context) (any, error) {
			return c.SendKeys(api.SendKeysParams{
				ClaudeInstanceID: id,
				Text:             smokeSentText,
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
		SeedKind: seedReadPane,
		SeedID:   "smoke-read-pane-id",
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
		Pane: func(result any) string {
			return result.(api.ReadPaneResult).Pane
		},
	}

	// ── record-pane-answer ────────────────────────────────────────────────
	//
	// A fallen-back request whose hook was found gone a minute ago: the
	// happy path reads the row's own pane for its hash, then records the
	// request answered outside agent-director (b.146 rule 13). The error
	// path's unknown token is ErrPermissionRequestNotFound before any tmux call.
	seeders["record-pane-answer"] = seederSpec{
		Manifest: mustVerb("record-pane-answer"),
		SeedKind: seedFallenBack,
		SeedID:   "smoke-record-pane-answer-id",
		Happy: func(c *api.Client, id string, _ context.Context) (any, error) {
			read, err := c.ReadPane(api.ReadPaneParams{ClaudeInstanceID: id})
			if err != nil {
				return nil, err
			}
			return c.RecordPaneAnswer(api.RecordPaneAnswerParams{RequestToken: storefix.TestRequestTokenA, As: "unknown",
				ExpectPaneSHA256: read.PaneSHA256})
		},
		Error: func(c *api.Client, _ context.Context) error {
			_, err := c.RecordPaneAnswer(api.RecordPaneAnswerParams{RequestToken: "deadbeef-dead-4dea-adea-deadbeefdead",
				As: "unknown", ExpectPaneSHA256: strings.Repeat("0", 64)})
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

	// ── pause ─────────────────────────────────────────────────────────────
	//
	// The waiting row's own session and pane (Ours): pause sends /exit and
	// Enter by pane id, the Enter's after-call hook ends the row, and the
	// wait sees it ended on its first poll. The 2s deadline fails the
	// subtest, rather than hanging the suite, if the row never ends. The
	// error path's bogus id returns ErrSpawnNotFound before any tmux call.
	seeders["pause"] = seederSpec{
		Manifest:         mustVerb("pause"),
		SeedKind:         seedPause,
		SeedID:           "smoke-pause-id",
		SentText:         smokeExitText,
		HappyCtxDeadline: 2 * time.Second,
		Happy: func(c *api.Client, id string, ctx context.Context) (any, error) {
			return c.Pause(ctx, api.PauseParams{ClaudeInstanceID: id})
		},
		Error: func(c *api.Client, ctx context.Context) error {
			_, err := c.Pause(ctx, api.PauseParams{ClaudeInstanceID: bogusID})
			return err
		},
	}
}
