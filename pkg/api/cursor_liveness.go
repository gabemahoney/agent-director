package api

import (
	"database/sql"
	"errors"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// cursorGatewayProof samples only the already selected current Cursor row.
// Its guarded projection precedes persistence in activeReadStore.finish.
func cursorGatewayProof(source any, pc ProcChecker, row Spawn) (*store.CursorMissingProof, activeCheckOutcome) {
	s, ok := source.(interface {
		ReadCursorRuntimeWithin(string, time.Duration) (store.CursorRuntime, error)
	})
	if !ok || row.Identity.PanePID <= 0 || row.Identity.PaneStarttime == "" {
		return nil, activeCheckOutcome{Adapter: "cursor_gateway", Result: "unavailable"}
	}
	verdict := tmux.JudgeProcess(pc, tmux.ProcIdentity{PID: row.Identity.PanePID, Starttime: row.Identity.PaneStarttime})
	if verdict != tmux.ProcGone {
		outcome := activeCheckOutcome{Adapter: "cursor_gateway", Result: "unconfirmed", Evidence: "unknown"}
		if verdict == tmux.ProcAlive {
			outcome.Result = "unchanged"
			outcome.Evidence = "alive"
		}
		return nil, outcome
	}
	b, err := s.ReadCursorRuntimeWithin(row.InstanceID, 0)
	if errors.Is(err, sql.ErrNoRows) {
		b = store.CursorRuntime{InstanceID: row.InstanceID, Token: row.Identity.Token, Life: row.LifeNumber, GatewayPID: row.Identity.PanePID, GatewayStart: row.Identity.PaneStarttime, SessionID: row.SessionID}
	} else if err != nil {
		return nil, activeOutcome("cursor_gateway", false, err)
	}
	return &store.CursorMissingProof{Row: row, Binding: b}, activeCheckOutcome{Adapter: "cursor_gateway", Result: "observed", Evidence: "gone"}
}
