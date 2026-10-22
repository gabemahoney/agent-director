package api

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// Public reads reconcile only the requested Cursor launch. A lost gateway is
// missing even if its runtime remains alive; it is never an intentional end.
func (c *Client) reconcileCursorGateway(instanceID string) (*store.CursorMissingProof, error) {
	row, found, err := c.st.ReadLiveCursorSpawn(instanceID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	// Require the recorded start time: a pid alone cannot identify this launch.
	if row.Identity.PanePID <= 0 || row.Identity.PaneStarttime == "" {
		return nil, nil
	}
	if tmux.JudgeProcess(c.procChecker, tmux.ProcIdentity{PID: row.Identity.PanePID, Starttime: row.Identity.PaneStarttime}) != tmux.ProcGone {
		return nil, nil
	}
	b, err := c.st.ReadCursorRuntime(instanceID)
	if errors.Is(err, sql.ErrNoRows) {
		// Startup can fail before registration. Carry only the examined launch;
		// the terminal transaction must still prove that no binding exists.
		b = store.CursorRuntime{InstanceID: row.InstanceID, Token: row.Identity.Token, Life: row.LifeNumber, GatewayPID: row.Identity.PanePID, GatewayStart: row.Identity.PaneStarttime, SessionID: row.SessionID}
	} else if err != nil {
		return nil, err
	}
	return &store.CursorMissingProof{Row: row, Binding: b}, nil
}

// Assemble the guarded read before attempting persistence. No read can then
// queue behind a timed-out native COMMIT still holding the store's only lease.
func (c *Client) persistCursorMissing(proofs []store.CursorMissingProof) error {
	for _, proof := range proofs {
		row, b := proof.Row, proof.Binding
		_, result, err := c.st.MarkCursorMissingIfSameLaunch(row, b)
		if err != nil {
			if c.st.CanDeferCursorMissingWrite(err) {
				// Test seam (Client.cursorMissingDeferred; nil in production):
				// reports the deferral and changes nothing.
				if c.cursorMissingDeferred != nil {
					c.cursorMissingDeferred(row.InstanceID, err)
				}
				continue
			}
			return err
		}
		if result == store.CondChanged {
			current, err := c.st.GetSpawn(row.InstanceID)
			if err != nil {
				return err
			}
			if current.Agent == store.AgentCursor && current.State != store.StateEnded && current.State != store.StateMissing && current.Identity.Token == row.Identity.Token && current.LifeNumber == row.LifeNumber && current.Identity.PanePID == row.Identity.PanePID && current.Identity.PaneStarttime == row.Identity.PaneStarttime {
				return fmt.Errorf("Cursor lost gateway changed during reconciliation")
			}
		}
	}
	return c.st.CursorMissingReadContextErr()
}
