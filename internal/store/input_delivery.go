package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"time"

	"github.com/gabemahoney/agent-director/internal/inputjournal"
)

var _ inputjournal.Persistence = (*Store)(nil)

// ReadInputPreparation derives life only from the exact pending launch token.
// Its later CreateInputLaunch rechecks that identity in the commit transaction.
func (s *Store) ReadInputPreparation(ctx context.Context, expected inputjournal.Identity) (inputjournal.Identity, error) {
	var id inputjournal.Identity
	if expected.StoreID != s.storeID {
		return id, inputjournal.ErrStale
	}
	err := s.WithContext(ctx).readWithin(DefaultLockWait, "store: input preparation", func(ctx context.Context, conn *sql.Conn) error {
		if err := inputAuthority(ctx, conn, s.storeID); err != nil {
			return err
		}
		id = expected
		var state, token, socket, agent, cwd, pane string
		err := conn.QueryRowContext(ctx, `SELECT life_number,state,COALESCE(launch_token,''),COALESCE(tmux_socket,''),agent,cwd,COALESCE(pane_id,'') FROM spawns WHERE instance_id=?`, expected.InstanceID).Scan(&id.LifeNumber, &state, &token, &socket, &agent, &cwd, &pane)
		if err != nil {
			return err
		}
		if state != StatePending || token != expected.Token || socket != expected.Socket || agent != expected.Agent || cwd != expected.CWD || pane != "" || id.LifeNumber < 0 {
			return inputjournal.ErrStale
		}
		return ctx.Err()
	})
	if err != nil {
		// An abandoned native read may still own its private result.
		return inputjournal.Identity{}, err
	}
	return id, nil
}

// inputSQL is satisfied by both the owned connection and the administrator's
// schema-step transaction. Import uses the latter and never commits independently.
type inputSQL interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func inputAuthority(ctx context.Context, q inputSQL, storeID string) error {
	var value string
	if err := q.QueryRowContext(ctx, `SELECT value FROM store_meta WHERE key=?`, inputAuthorityKey).Scan(&value); err != nil {
		return fmt.Errorf("%w: input authority marker unavailable", inputjournal.ErrInvalidState)
	}
	if value != inputAuthorityVersion+storeID {
		return fmt.Errorf("%w: input authority identity mismatch", inputjournal.ErrInvalidState)
	}
	return nil
}

// FULL is per connection. Never trust the opener or a different pool lease.
// Setting synchronous inside BEGIN is illegal, so establish it before BEGIN,
// then verify both settings on this lease before any intent can be admitted.
func inputDurability(ctx context.Context, conn *sql.Conn) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA synchronous = FULL"); err != nil {
		return err
	}
	var mode string
	var sync int
	if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return err
	}
	if err := conn.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&sync); err != nil {
		return err
	}
	if mode != "wal" || sync != 2 {
		return fmt.Errorf("%w: native intent requires WAL synchronous FULL", inputjournal.ErrInvalidState)
	}
	return ctx.Err()
}

// Input operations reuse the pool's bounded admission, native-operation slot
// and disposal ownership. No callback performs provider, tmux or filesystem IO.
// Results are published only after confirmed commit; callers never read a
// callback's result while an abandoned worker may still be changing it.
func (s *Store) inputWrite(ctx context.Context, fn func(context.Context, *sql.Conn) error) error {
	err := s.withConnContext(ctx, DefaultLockWait, func(ctx context.Context, conn *sql.Conn, busyMs int, cleanup *connectionCleanup) error {
		if err := inputDurability(ctx, conn); err != nil {
			return err
		}
		if err := beginWithin(ctx, conn, busyMs); err != nil {
			return err
		}
		cleanup.rollback = true
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := inputAuthority(ctx, conn, s.storeID); err != nil {
			return err
		}
		if err := fn(ctx, conn); err != nil {
			return err
		}
		// This is the same admission fence as inWriteTxContext. It precedes
		// COMMIT on this actual FULL connection, including native drivers that
		// cannot cancel a COMMIT after it has started.
		if op := cleanup.operation; op != nil {
			op.mu.Lock()
			defer op.mu.Unlock()
			if op.abandoned || ctx.Err() != nil {
				return operationBusy(context.Canceled)
			}
			op.commitStarted = true
			// Do not hold the mutex across the noncancelable statement: the
			// caller must be able to abandon its wait and report uncertainty.
			op.mu.Unlock()
			_, err := conn.ExecContext(ctx, "COMMIT")
			op.mu.Lock()
			if err != nil {
				return fmt.Errorf("%w: %w", errCommitUnconfirmed, err)
			}
		} else {
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
				return fmt.Errorf("%w: %w", errCommitUnconfirmed, err)
			}
		}
		cleanup.rollback = false
		return nil
	})
	if errors.Is(err, errCommitUnconfirmed) || err == nil && ctx.Err() != nil {
		return inputjournal.ErrUnconfirmed
	}
	return err
}

func decodeInput(data []byte, value any) error {
	if len(data) == 0 || len(data) > 8<<20 {
		return inputjournal.ErrInvalidState
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return inputjournal.ErrInvalidState
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return inputjournal.ErrInvalidState
	}
	return nil
}

func readInputLaunch(ctx context.Context, q inputSQL, storeID, token string) (*inputjournal.Launch, error) {
	var data string
	var revision int64
	var current sql.NullString
	err := q.QueryRowContext(ctx, `SELECT revision,current_request_id,record FROM input_launches WHERE store_id=? AND launch_token=?`, storeID, token).Scan(&revision, &current, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var l inputjournal.Launch
	if err := decodeInput([]byte(data), &l); err != nil {
		return nil, err
	}
	if l.Identity.StoreID != storeID || l.Identity.Token != token || l.Revision != revision || l.CurrentRequestID != current.String || current.Valid && current.String == "" {
		return nil, inputjournal.ErrInvalidState
	}
	if err := inputjournal.ValidateLaunch(l); err != nil {
		return nil, err
	}
	return &l, nil
}

func readInputAttempt(ctx context.Context, q inputSQL, l inputjournal.Launch, requestID string) (*inputjournal.Attempt, error) {
	if requestID == "" {
		return nil, nil
	}
	var data string
	var raw []byte
	var revision int64
	err := q.QueryRowContext(ctx, `SELECT revision,record,proof_raw FROM input_attempts WHERE store_id=? AND launch_token=? AND request_id=?`, l.Identity.StoreID, l.Identity.Token, requestID).Scan(&revision, &data, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var a inputjournal.Attempt
	if err := decodeInput([]byte(data), &a); err != nil {
		return nil, err
	}
	if a.RequestID != requestID || a.Revision != revision || revision < 1 || a.Proof == nil && raw != nil {
		return nil, inputjournal.ErrInvalidState
	}
	if a.Proof != nil {
		// JSON stores metadata only: accepting a second copy would obscure
		// which representation the stored hash actually authenticates.
		if len(a.Proof.Raw) != 0 || len(raw) == 0 {
			return nil, inputjournal.ErrInvalidState
		}
		a.Proof.Raw = raw
	}
	if err := inputjournal.ValidateAttempt(l, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *Store) ReadInputLaunch(ctx context.Context, token string) (*inputjournal.Launch, error) {
	var result *inputjournal.Launch
	err := s.WithContext(ctx).readWithin(DefaultLockWait, "store: read input launch", func(ctx context.Context, conn *sql.Conn) error {
		if err := inputAuthority(ctx, conn, s.storeID); err != nil {
			return err
		}
		var err error
		result, err = readInputLaunch(ctx, conn, s.storeID, token)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// CheckInputGuard rechecks captured admission after a waited resource and
// confirmed intent. It authorizes no replay and makes no operational write.
func (s *Store) CheckInputGuard(ctx context.Context, expected *inputjournal.Launch, g inputjournal.Guard) error {
	return s.WithContext(ctx).readWithin(DefaultLockWait, "store: check input authority", func(ctx context.Context, conn *sql.Conn) error {
		if err := inputAuthority(ctx, conn, s.storeID); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
			return err
		}
		defer rollbackConn(ctx, conn)
		if expected != nil {
			if _, err := sameInputLaunch(ctx, conn, *expected); err != nil {
				return err
			}
		} else {
			l, err := readInputLaunch(ctx, conn, s.storeID, g.Identity.Token)
			if err != nil {
				return err
			}
			if l != nil {
				return inputjournal.ErrStale
			}
		}
		if err := inputOwner(ctx, conn, g, false); err != nil {
			return err
		}
		return ctx.Err()
	})
}

func (s *Store) ReadInputAttempt(ctx context.Context, expected inputjournal.Launch, requestID string) (*inputjournal.Attempt, error) {
	if expected.Identity.StoreID != s.storeID {
		return nil, inputjournal.ErrStale
	}
	var result *inputjournal.Attempt
	err := s.WithContext(ctx).readWithin(DefaultLockWait, "store: read input attempt", func(ctx context.Context, conn *sql.Conn) error {
		if err := inputAuthority(ctx, conn, s.storeID); err != nil {
			return err
		}
		// A read transaction pins launch and request together, including other
		// processes' writers. Separate autocommit SELECTs would tear them.
		if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
			return err
		}
		defer rollbackConn(ctx, conn)
		if _, err := sameInputLaunch(ctx, conn, expected); err != nil {
			return err
		}
		var err error
		result, err = readInputAttempt(ctx, conn, expected, requestID)
		if err == nil && result == nil && requestID == expected.CurrentRequestID && requestID != "" {
			return inputjournal.ErrInvalidState
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// importInputRecords is called only within the administrator schema step's
// transaction. Validate every record before writing any; schema/cutover stamping
// and commit belong to that caller. An existing source or launch is a conflict,
// never an instruction to overwrite newer runtime state.
func importInputRecords(ctx context.Context, tx *sql.Tx, storeID string, records []inputjournal.ImportRecord) error {
	seen := make(map[string]bool)
	for _, r := range records {
		l := r.Launch
		if l.Identity.StoreID != storeID || r.Source == "" || r.SourceSHA256 != inputjournal.Hash(r.Evidence) || seen[l.Identity.Token] || l.Revision != 1 || !l.Confirmation.At.IsZero() {
			return inputjournal.ErrInvalidState
		}
		seen[l.Identity.Token] = true
		if err := inputjournal.ValidateLaunch(l); err != nil {
			return err
		}
		requests := make(map[string]bool)
		for _, a := range r.Attempts {
			if a == nil || a.Revision != 1 || requests[a.RequestID] || !a.Confirmation.At.IsZero() {
				return inputjournal.ErrInvalidState
			}
			requests[a.RequestID] = true
			if err := inputjournal.ValidateAttempt(l, a); err != nil {
				return err
			}
		}
		if l.CurrentRequestID != "" && !requests[l.CurrentRequestID] {
			return inputjournal.ErrInvalidState
		}
	}
	for _, r := range records {
		if err := putInputLaunch(ctx, tx, r.Launch, true); err != nil {
			return err
		}
		for _, a := range r.Attempts {
			if err := putInputAttempt(ctx, tx, r.Launch, a); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO input_imports(source,store_id,source_sha256,evidence) VALUES(?,?,?,?)`, r.Source, storeID, r.SourceSHA256, r.Evidence); err != nil {
			return err
		}
	}
	return nil
}

func sameInputLaunch(ctx context.Context, q inputSQL, expected inputjournal.Launch) (*inputjournal.Launch, error) {
	l, err := readInputLaunch(ctx, q, expected.Identity.StoreID, expected.Identity.Token)
	if err != nil {
		return nil, err
	}
	if l == nil || !reflect.DeepEqual(*l, expected) || l.Blocker != "" {
		return nil, inputjournal.ErrStale
	}
	return l, nil
}

func inputOwner(ctx context.Context, q inputSQL, g inputjournal.Guard, unbound bool) error {
	var id inputjournal.Identity
	var epoch, version int64
	var session, state string
	id.StoreID = g.Identity.StoreID
	err := q.QueryRowContext(ctx, `SELECT instance_id, COALESCE(life_number,0), COALESCE(launch_token,''), COALESCE(tmux_socket,''), COALESCE(pane_id,''), COALESCE(pane_pid,0), COALESCE(pane_starttime,''), agent,cwd,permission_epoch,COALESCE(row_version,0),COALESCE(session_id,''),state FROM spawns WHERE instance_id=?`, g.Identity.InstanceID).Scan(&id.InstanceID, &id.LifeNumber, &id.Token, &id.Socket, &id.PaneID, &id.PID, &id.Starttime, &id.Agent, &id.CWD, &epoch, &version, &session, &state)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return inputjournal.ErrStale
		}
		return err
	}
	if unbound {
		id.PaneID, id.PID, id.Starttime = "", 0, ""
	}
	if id != g.Identity || g.SessionID != "" && session != g.SessionID {
		return inputjournal.ErrStale
	}
	if g.ProofOnly {
		return nil
	}
	if epoch != g.PermissionEpoch || version != g.RowVersion || g.State != "" && state != g.State || !g.Manual && state != StatePending && state != StateWaiting {
		return inputjournal.ErrStale
	}
	return ctx.Err()
}

func putInputLaunch(ctx context.Context, q inputSQL, l inputjournal.Launch, create bool) error {
	if err := inputjournal.ValidateLaunch(l); err != nil {
		return err
	}
	data, err := json.Marshal(l)
	if err != nil {
		return err
	}
	if create {
		_, err = q.ExecContext(ctx, `INSERT INTO input_launches(store_id,launch_token,revision,current_request_id,record) VALUES(?,?,?,?,?)`, l.Identity.StoreID, l.Identity.Token, l.Revision, nullableStringArg(l.CurrentRequestID), string(data))
	} else {
		_, err = q.ExecContext(ctx, `UPDATE input_launches SET revision=?,current_request_id=?,record=? WHERE store_id=? AND launch_token=?`, l.Revision, nullableStringArg(l.CurrentRequestID), string(data), l.Identity.StoreID, l.Identity.Token)
	}
	return err
}

func putInputAttempt(ctx context.Context, q inputSQL, l inputjournal.Launch, a *inputjournal.Attempt) error {
	if err := inputjournal.ValidateAttempt(l, a); err != nil {
		return err
	}
	copy := *a
	var raw []byte
	if a.Proof != nil {
		p := *a.Proof
		raw, p.Raw = p.Raw, nil
		copy.Proof = &p
	}
	data, err := json.Marshal(copy)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO input_attempts(store_id,launch_token,request_id,revision,record,proof_raw) VALUES(?,?,?,?,?,?) ON CONFLICT(store_id,launch_token,request_id) DO UPDATE SET revision=excluded.revision,record=excluded.record,proof_raw=excluded.proof_raw`, l.Identity.StoreID, l.Identity.Token, a.RequestID, a.Revision, string(data), raw)
	return err
}

func (s *Store) CreateInputLaunch(ctx context.Context, l inputjournal.Launch) error {
	if l.Identity.StoreID != s.storeID || l.Revision != 1 || l.CurrentRequestID != "" || l.Blocker != "" {
		return inputjournal.ErrInvalidState
	}
	return s.inputWrite(ctx, func(ctx context.Context, conn *sql.Conn) error {
		// Preparation precedes pane binding, but the owning pending token/life
		// already exists. No finished or replaced launch can be provisioned.
		var state string
		var life int64
		var token, socket, agent, cwd string
		if err := conn.QueryRowContext(ctx, `SELECT state,COALESCE(life_number,0),COALESCE(launch_token,''),COALESCE(tmux_socket,''),agent,cwd FROM spawns WHERE instance_id=?`, l.Identity.InstanceID).Scan(&state, &life, &token, &socket, &agent, &cwd); err != nil {
			return err
		}
		if state != StatePending || life != l.Identity.LifeNumber || token != l.Identity.Token || socket != l.Identity.Socket || agent != l.Identity.Agent || cwd != l.Identity.CWD || l.Identity.PaneID != "" || l.Identity.PID != 0 || l.Identity.Starttime != "" {
			return inputjournal.ErrStale
		}
		l.Confirmation = inputjournal.Evidence{At: time.Now().UTC(), Source: "store", Value: "capability", Revision: l.Revision}
		return putInputLaunch(ctx, conn, l, true)
	})
}

func (s *Store) BindInputLaunch(ctx context.Context, expected inputjournal.Launch, g inputjournal.Guard) (*inputjournal.Launch, error) {
	var result *inputjournal.Launch
	err := s.inputWrite(ctx, func(ctx context.Context, conn *sql.Conn) error {
		l, err := sameInputLaunch(ctx, conn, expected)
		if err != nil {
			return err
		}
		if err := inputOwner(ctx, conn, g, false); err != nil {
			return err
		}
		if l.Identity == g.Identity {
			result = l
			return nil
		}
		if g.ProofOnly {
			return inputjournal.ErrStale
		}
		base := g.Identity
		base.PaneID, base.PID, base.Starttime = "", 0, ""
		if l.Identity != base || l.CurrentRequestID != "" || g.Identity.PID <= 0 || g.Identity.Starttime == "" || g.Identity.PaneID == "" {
			return inputjournal.ErrStale
		}
		l.Identity, l.Revision = g.Identity, l.Revision+1
		// Binding proves the recorded association, not a new observation of
		// provider capability. Preserve its original confirmation time.
		if !l.Confirmation.At.IsZero() {
			l.Confirmation.Revision = l.Revision
		}
		if err := putInputLaunch(ctx, conn, *l, false); err != nil {
			return err
		}
		result = l
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) SaveInputAttempt(ctx context.Context, expected inputjournal.Launch, g inputjournal.Guard, next *inputjournal.Attempt) (*inputjournal.Launch, *inputjournal.Attempt, error) {
	// Detach all mutable maps/slices before handing work to a native driver.
	data, err := json.Marshal(next)
	if err != nil {
		return nil, nil, err
	}
	var a inputjournal.Attempt
	if err = decodeInput(data, &a); err != nil {
		return nil, nil, err
	}
	var result *inputjournal.Launch
	err = s.inputWrite(ctx, func(ctx context.Context, conn *sql.Conn) error {
		l, err := sameInputLaunch(ctx, conn, expected)
		if err != nil {
			return err
		}
		if g.Identity != l.Identity || g.Identity.StoreID != s.storeID {
			return inputjournal.ErrStale
		}
		if a.NativeSessionID != "" && !g.Manual {
			if g.SessionID != "" && g.SessionID != a.NativeSessionID {
				return inputjournal.ErrStale
			}
			g.SessionID = a.NativeSessionID
		}
		if err := inputOwner(ctx, conn, g, false); err != nil {
			return err
		}
		old, err := readInputAttempt(ctx, conn, *l, a.RequestID)
		if err != nil {
			return err
		}
		if err := inputjournal.ValidateTransition(old, &a); err != nil {
			return err
		}
		if g.ProofOnly && (old == nil || old.BytesAttempted != a.BytesAttempted || old.EnterAttempted != a.EnterAttempted || old.ManualInterference != a.ManualInterference || a.Phase != old.Phase && a.Phase != "submitted") {
			return inputjournal.ErrStale
		}
		if g.Manual && (old != nil && (old.BytesAttempted != a.BytesAttempted || old.EnterAttempted != a.EnterAttempted) || !a.ManualInterference) {
			return inputjournal.ErrStale
		}
		if old == nil && l.CurrentRequestID != "" {
			current, err := readInputAttempt(ctx, conn, *l, l.CurrentRequestID)
			if err != nil {
				return err
			}
			if current == nil || current.Phase != "submitted" && current.Phase != "not_started" || !g.Manual && current.ManualInterference {
				return inputjournal.ErrStale
			}
		}
		// History-only proof can settle an old request, never retarget current
		// ownership. A caller cannot resume dispatch from a historical record.
		if old != nil && a.RequestID != l.CurrentRequestID && !g.ProofOnly {
			return inputjournal.ErrStale
		}
		a.Confirmation = inputjournal.Evidence{}
		if err := inputjournal.ValidateAttempt(*l, &a); err != nil {
			return err
		}
		a.Revision++
		source := "store"
		if a.Phase == "submitted" {
			source = "transcript"
		}
		// An unchanged fact keeps its earlier basis. First exact proof records
		// the successful observation, never the time legacy data was imported.
		at := time.Now().UTC()
		if old != nil && old.Phase == a.Phase {
			at = old.Confirmation.At
		}
		if !at.IsZero() {
			a.Confirmation = inputjournal.Evidence{At: at, Source: source, Value: a.Phase, Revision: a.Revision}
		}
		if err := putInputAttempt(ctx, conn, *l, &a); err != nil {
			return err
		}
		l.Revision++
		if old == nil {
			l.CurrentRequestID = a.RequestID
		}
		if !l.Confirmation.At.IsZero() {
			l.Confirmation.Revision = l.Revision
		}
		if err := putInputLaunch(ctx, conn, *l, false); err != nil {
			return err
		}
		if err := inputOwner(ctx, conn, g, false); err != nil {
			return err
		}
		result = l
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return result, &a, nil
}
