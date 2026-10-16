package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// CursorMissingProof carries the independently examined lost gateway and its
// binding. It is scoped to one public read, never durable permission evidence.
type CursorMissingProof struct {
	Row     Spawn
	Binding CursorRuntime
}

// CursorMissingReads projects missing only while the examined row snapshot and
// complete binding still match in the read statement. All other fields remain
// persisted facts: this view neither fabricates a terminal timestamp nor closes
// requests. The embedded store remains the owner of the shared connection.
type CursorMissingReads struct {
	*Store
	proofs []CursorMissingProof
}

func (s *Store) WithCursorMissingReads(proofs []CursorMissingProof) *CursorMissingReads {
	return &CursorMissingReads{Store: s, proofs: append([]CursorMissingProof(nil), proofs...)}
}

func (s *CursorMissingReads) GetSpawn(id string) (Spawn, error) {
	return s.Store.getSpawnSettled(id, SettleRead{}, s.proofs)
}

func (s *CursorMissingReads) GetSpawnSettled(id string, settle SettleRead) (Spawn, error) {
	return s.Store.getSpawnSettled(id, settle, s.proofs)
}

func (s *CursorMissingReads) SpawnStatus(id string) (string, int64, error) {
	_, raw, launch, err := s.SpawnStatusSettled(id, SettleRead{})
	return raw, launch, err
}

func (s *CursorMissingReads) SpawnStatusSettled(id string, settle SettleRead) (string, string, int64, error) {
	return s.Store.spawnStatusSettled(id, settle, s.proofs)
}

func (s *CursorMissingReads) ListSpawns(filters ListFilters) ([]Spawn, error) {
	return s.Store.listSpawns(filters, s.proofs)
}

func cursorMissingGuard(row Spawn, b CursorRuntime) (string, []any) {
	if row.Agent != AgentCursor || row.InstanceID != b.InstanceID || row.Identity.Token != b.Token || row.LifeNumber != b.Life || row.Identity.PanePID != b.GatewayPID || row.Identity.PaneStarttime != b.GatewayStart || row.SessionID != b.SessionID {
		return "", nil
	}
	guard := `instance_id=? AND ` + liveStateGuardSQL + ` AND ` + snapshotMatchSQL + ` AND agent='cursor' AND launch_token=? AND life_number=? AND pane_pid=? AND pane_starttime=?`
	args := append([]any{row.InstanceID}, liveStateGuardArgs()...)
	args = append(args, snapshotMatchArgs(row.Snapshot)...)
	args = append(args, b.Token, b.Life, b.GatewayPID, b.GatewayStart)
	if cursorRuntimeAbsent(b) {
		guard += ` AND NOT EXISTS(SELECT 1 FROM cursor_runtimes WHERE bound_instance_id=?)`
		args = append(args, row.InstanceID)
	} else {
		guard += ` AND EXISTS(SELECT 1 FROM cursor_runtimes WHERE ` + cursorBindingMatchSQL + ` AND bound_session_id=? AND bound_terminated=?)`
		args = append(args, cursorBindingArgs(b)...)
		args = append(args, b.SessionID, b.Terminated)
	}
	return guard, args
}

func cursorRuntimeAbsent(b CursorRuntime) bool {
	return b.RuntimePID == 0 && b.RuntimeStart == "" && b.Connection == "" && b.InputPipe == "" && b.OutputPipe == "" && b.InputFD == 0 && b.OutputFD == 0 && !b.Terminated
}

// cursorMissingState retains the normal expression unless this read found a
// lost gateway. The same guarded expression is used by List's
// SELECT and WHERE, before LIMIT, within one SQLite read snapshot.
func cursorMissingState(expr string, args []any, proofs []CursorMissingProof) (string, []any, error) {
	var guards []string
	var values [][]any
	seen := make(map[bool]bool)
	for _, p := range proofs {
		guard, a := cursorMissingGuard(p.Row, p.Binding)
		if guard != "" {
			absent := cursorRuntimeAbsent(p.Binding)
			values = append(values, append([]any{absent}, a...))
			if !seen[absent] {
				// Reuse the terminal write's exact predicate, substituting JSON
				// values for its parameters. There are only two predicate shapes:
				// an examined binding and examined absence, regardless of row count.
				guards = append(guards, cursorMissingJSONGuard(guard, absent))
				seen[absent] = true
			}
		}
	}
	if len(guards) == 0 {
		return expr, args, nil
	}
	data, err := json.Marshal(values)
	if err != nil {
		return "", nil, fmt.Errorf("store: encode Cursor missing proofs: %w", err)
	}
	// A single JSON1 input keeps bind count and expression depth independent
	// of the number of lost gateways, including settled and filtered lists.
	return `CASE WHEN EXISTS(SELECT 1 FROM json_each(?) AS cursor_missing_proof WHERE ` + strings.Join(guards, " OR ") + `) THEN 'missing' ELSE ` + expr + ` END`, append([]any{string(data)}, args...), nil
}

func cursorMissingJSONGuard(guard string, absent bool) string {
	var sql strings.Builder
	fmt.Fprintf(&sql, "(json_extract(cursor_missing_proof.value, '$[0]') = %t AND ", absent)
	for i, part := range strings.Split(guard, "?") {
		if i > 0 {
			fmt.Fprintf(&sql, "json_extract(cursor_missing_proof.value, '$[%d]')", i)
		}
		sql.WriteString(part)
	}
	sql.WriteString(")")
	return sql.String()
}

func cursorMissingColumns(proofs []CursorMissingProof) (string, []any, error) {
	expr, args, err := cursorMissingState("state", nil, proofs)
	return strings.Replace(spawnColumns, ", state,", ", "+expr+",", 1), args, err
}

func (s *Store) readContext() context.Context {
	if s.callContext != nil {
		return s.callContext
	}
	return context.Background()
}

// A changed snapshot or binding is not permission to report a proven-lost
// launch as live. A replacement launch, however, retains its own normal state.
func validateCursorMissingRead(row Spawn, proofs []CursorMissingProof) error {
	for _, p := range proofs {
		r := p.Row
		if row.Agent == AgentCursor && IsLiveState(row.State) && row.InstanceID == r.InstanceID && row.Identity.Token == r.Identity.Token && row.LifeNumber == r.LifeNumber && row.Identity.PanePID == r.Identity.PanePID && row.Identity.PaneStarttime == r.Identity.PaneStarttime {
			return fmt.Errorf("store: Cursor lost gateway changed during read")
		}
	}
	return nil
}

// CanDeferCursorMissingWrite recognizes unavailable best-effort persistence,
// never a failed underlying read or an exhausted/canceled caller budget.
// A timed-out COMMIT remains unconfirmed; this does not assert no writes.
func (s *Store) CanDeferCursorMissingWrite(err error) bool {
	if s.callContext != nil && s.callContext.Err() != nil {
		return false
	}
	return !errors.Is(err, context.Canceled) && (errors.Is(err, ErrStoreBusy) || errors.Is(err, context.DeadlineExceeded))
}

// CursorMissingReadContextErr checks the caller budget after best-effort
// persistence, including cleanup/audit after an already confirmed commit.
func (s *Store) CursorMissingReadContextErr() error {
	return s.readContext().Err()
}
