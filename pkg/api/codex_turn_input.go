package api

import (
	"fmt"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

// codexTurnErrorGuard covers ordinary submission only. Explicit manual input
// remains subject to the state, relay, ownership and optional pane-hash guards.
func codexTurnErrorGuard(row Spawn, ordinary bool) error {
	if !ordinary {
		return nil
	}
	if errored := codexLastTurnErrored(row); errored != nil && *errored {
		return fmt.Errorf("%w: %s: the latest observed Codex turn ended with an error; ordinary text submission was refused and nothing was sent; have a person or agent inspect read-pane; answer a menu with send-keys --key, or for intentional text input pass that read's pane_sha256 as --expect-pane-sha256; other input guards still apply", ErrCodexTurnErrored, row.InstanceID)
	}
	return nil
}

// guardCodexTurnBeforeWrite also covers compatibility rows with no input
// journal. Rollout appends do not advance the row's SQLite admission revision.
func (r *sendKeysRun) guardCodexTurnBeforeWrite(params SendKeysParams) {
	if !r.ordinary || r.row.Agent != store.AgentCodex {
		return
	}
	prior := r.beforeWrite
	r.beforeWrite = func() error {
		if prior != nil {
			if err := prior(); err != nil {
				return err
			}
		}
		row := r.row
		if r.input == nil {
			if r.env.Context != nil && r.env.Context.Err() != nil {
				return r.env.Context.Err()
			}
			var err error
			row, err = r.s.GetSpawnWithin(row.InstanceID, time.Second)
			if err != nil {
				return err
			}
			if !r.sameInputPreparation(row) {
				return deliveryNotStarted(r.row, "input preparation changed before dispatch")
			}
			if err := sendKeysStateGuard(row, params); err != nil {
				return err
			}
			guard, err := evaluateRelayGuard(r.s, r.j, r.env.IntentHold, row, params)
			if err != nil {
				return err
			}
			if guard.refusal != nil {
				return guard.refusal
			}
		}
		if err := codexTurnErrorGuard(row, true); err != nil {
			return err
		}
		if r.env.Context != nil && r.env.Context.Err() != nil {
			return r.env.Context.Err()
		}
		if r.input != nil {
			if err := checkInput(r.input); err != nil {
				return deliveryNotStarted(r.row, "input admission changed during rollout observation")
			}
		}
		return nil
	}
}
