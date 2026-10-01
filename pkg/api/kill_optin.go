package api

import (
	"fmt"

	"github.com/gabemahoney/agent-director/internal/store"
)

// This file holds kill's operator-only finished-row opt-in (SR-6.5): the
// live-row refusal and the finished-row path. Nothing here is named in any
// text shown to agents (SR-6.8, SR-18.15): no description carries the
// opt-in's flag or a session-ending command (SR-1.4).

// withOptIn is the kill flow when the opt-in is set, right after the row
// read (SR-6.5, SR-6.1): a live row, pending included, gets liveRowRefusal
// before the unusable-name check, any lookup or any tmux call; a finished row
// (ended, missing) takes the finished-row path. SR-6.5's finished-row table
// and SR-6.7's reported-in rule are not built yet, so a finished row keeps the
// finished-row no-op: success, kill_sent false, no tmux call.
func (k *killRun) withOptIn() error {
	if k.row.State != store.StateEnded && k.row.State != store.StateMissing {
		return liveRowRefusal(k.id, k.row.State)
	}
	return nil
}

// liveRowRefusal is the opt-in's refusal of a live row (SR-6.5, SR-1.4; PRD
// OQ13): the instance id; "the row is live" and its state; that the
// finished-row option applies only to an ended or missing row; that no lookup
// was made and nothing was sent. It wraps only ErrSpawnNotResumable, which
// kill's manifest error list does not carry (SR-1.7).
func liveRowRefusal(instanceID, state string) error {
	return fmt.Errorf("%w: instance %s: the row is live (state %s); the finished-row option applies only to an ended or missing row; no lookup was made and nothing was sent",
		ErrSpawnNotResumable, instanceID, state)
}
