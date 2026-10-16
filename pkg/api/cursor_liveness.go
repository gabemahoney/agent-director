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
func (c *Client) reconcileCursorGateway(instanceID string) error {
	row, found, err := c.st.ReadLiveCursorSpawn(instanceID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	// Require the recorded start time: a pid alone cannot identify this launch.
	if row.Identity.PanePID <= 0 || row.Identity.PaneStarttime == "" {
		return nil
	}
	if tmux.JudgeProcess(c.procChecker, tmux.ProcIdentity{PID: row.Identity.PanePID, Starttime: row.Identity.PaneStarttime}) != tmux.ProcGone {
		return nil
	}
	b, err := c.st.ReadCursorRuntime(instanceID)
	if errors.Is(err, sql.ErrNoRows) {
		// Startup can fail before registration. Carry only the examined launch;
		// the terminal transaction must still prove that no binding exists.
		b = store.CursorRuntime{InstanceID: row.InstanceID, Token: row.Identity.Token, Life: row.LifeNumber, GatewayPID: row.Identity.PanePID, GatewayStart: row.Identity.PaneStarttime, SessionID: row.SessionID}
	} else if err != nil {
		return err
	}
	_, result, err := c.st.MarkCursorMissingIfSameLaunch(row, b)
	if err != nil {
		return err
	}
	if result == store.CondChanged {
		current, e := c.st.GetSpawn(instanceID)
		if e != nil {
			return e
		}
		if current.Agent == store.AgentCursor && current.State != store.StateEnded && current.State != store.StateMissing && current.Identity.Token == row.Identity.Token && current.LifeNumber == row.LifeNumber && current.Identity.PanePID == row.Identity.PanePID && current.Identity.PaneStarttime == row.Identity.PaneStarttime {
			return fmt.Errorf("Cursor lost gateway changed during reconciliation")
		}
	}
	return nil
}
