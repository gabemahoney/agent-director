package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// CursorRuntime is the narrow current-launch pipe owner, separate from the
// pane identity. No protocol payload establishes these kernel facts.
type CursorRuntime struct {
	InstanceID   string `json:"-"`
	Token        string `json:"-"`
	Life         int64  `json:"-"`
	GatewayPID   int    `json:"-"`
	GatewayStart string `json:"-"`
	RuntimePID   int    `json:"-"`
	RuntimeStart string `json:"-"`
	Connection   string `json:"-"`
	InputPipe    string `json:"-"`
	OutputPipe   string `json:"-"`
	InputFD      int    `json:"-"`
	OutputFD     int    `json:"-"`
	SessionID    string `json:"-"`
	Terminated   bool   `json:"-"`
}

const cursorRuntimeDDL = `CREATE TABLE IF NOT EXISTS cursor_runtimes (
 bound_instance_id TEXT PRIMARY KEY,
 bound_launch_token TEXT NOT NULL,
 bound_life_number INTEGER NOT NULL,
 bound_gateway_pid INTEGER NOT NULL,
 bound_gateway_start TEXT NOT NULL,
 bound_runtime_pid INTEGER NOT NULL,
 bound_runtime_start TEXT NOT NULL,
 bound_connection_id TEXT NOT NULL,
 bound_input_pipe TEXT NOT NULL,
 bound_output_pipe TEXT NOT NULL,
 bound_input_fd INTEGER NOT NULL,
 bound_output_fd INTEGER NOT NULL,
 bound_session_id TEXT NOT NULL DEFAULT '',
 bound_terminated INTEGER NOT NULL DEFAULT 0
);`

func migrateV9toV10(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Reentry may find the complete table. Probe every required column before
	// stamping; an incomplete/case-mismatched hand-added table is refused.
	var exists int
	var tableName string
	if err = tx.QueryRow(`SELECT COUNT(*), COALESCE(MAX(name),'') FROM sqlite_master WHERE type='table' AND lower(name)='cursor_runtimes'`).Scan(&exists, &tableName); err != nil {
		return err
	}
	if exists != 0 {
		if tableName != "cursor_runtimes" {
			return fmt.Errorf("store: v9→v10 conflicting Cursor binding table")
		}
		rows, e := tx.Query(`PRAGMA table_info(cursor_runtimes)`)
		if e != nil {
			return e
		}
		names := map[string]bool{}
		expected := map[string]string{"bound_instance_id": "TEXT", "bound_launch_token": "TEXT", "bound_life_number": "INTEGER", "bound_gateway_pid": "INTEGER", "bound_gateway_start": "TEXT", "bound_runtime_pid": "INTEGER", "bound_runtime_start": "TEXT", "bound_connection_id": "TEXT", "bound_input_pipe": "TEXT", "bound_output_pipe": "TEXT", "bound_input_fd": "INTEGER", "bound_output_fd": "INTEGER", "bound_session_id": "TEXT", "bound_terminated": "INTEGER"}
		for rows.Next() {
			var cid, notnull, pk int
			var name, kind string
			var def any
			if e = rows.Scan(&cid, &name, &kind, &notnull, &def, &pk); e != nil {
				rows.Close()
				return e
			}
			want, ok := expected[name]
			wantNull, wantPK := 1, 0
			if name == "bound_instance_id" {
				wantNull, wantPK = 0, 1
			}
			wantDefault := any(nil)
			if name == "bound_session_id" {
				wantDefault = "''"
			}
			if name == "bound_terminated" {
				wantDefault = "0"
			}
			if !ok || kind != want || notnull != wantNull || pk != wantPK || def != wantDefault {
				rows.Close()
				return fmt.Errorf("store: v9→v10 incompatible Cursor binding table")
			}
			names[name] = true
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, name := range []string{"bound_instance_id", "bound_launch_token", "bound_life_number", "bound_gateway_pid", "bound_gateway_start", "bound_runtime_pid", "bound_runtime_start", "bound_connection_id", "bound_input_pipe", "bound_output_pipe", "bound_input_fd", "bound_output_fd", "bound_session_id", "bound_terminated"} {
			if !names[name] {
				return fmt.Errorf("store: v9→v10 incomplete cursor_runtimes table")
			}
		}
	}
	if _, err = tx.Exec(cursorRuntimeDDL); err != nil {
		return err
	}
	if _, err = tx.Exec(`PRAGMA user_version = 10`); err != nil {
		return err
	}
	return tx.Commit()
}

// RegisterCursorRuntime is conditional on the independently examined launch.
// A conflicting same-launch registration is refused rather than replaced.
func (s *Store) RegisterCursorRuntime(row Spawn, b CursorRuntime) (bool, error) {
	if b.InstanceID != row.InstanceID || b.Token == "" || b.Token != row.Identity.Token || b.GatewayPID != row.Identity.PanePID || b.GatewayStart == "" || b.RuntimePID <= 0 || b.RuntimeStart == "" || b.Connection == "" || b.InputPipe == "" || b.OutputPipe == "" {
		return false, nil
	}
	q := `INSERT INTO cursor_runtimes (bound_instance_id,bound_launch_token,bound_life_number,bound_gateway_pid,bound_gateway_start,bound_runtime_pid,bound_runtime_start,bound_connection_id,bound_input_pipe,bound_output_pipe,bound_input_fd,bound_output_fd)
 SELECT instance_id, launch_token,life_number,?,?,?,?,?,?,?,?,? FROM spawns
 WHERE instance_id=? AND agent='cursor' AND state='pending' AND launch_token=? AND life_number=? AND pane_pid=? AND pane_starttime=? AND ` + snapshotMatchSQL + `
 ON CONFLICT(bound_instance_id) DO UPDATE SET bound_launch_token=excluded.bound_launch_token,bound_life_number=excluded.bound_life_number,bound_gateway_pid=excluded.bound_gateway_pid,bound_gateway_start=excluded.bound_gateway_start,bound_runtime_pid=excluded.bound_runtime_pid,bound_runtime_start=excluded.bound_runtime_start,bound_connection_id=excluded.bound_connection_id,bound_input_pipe=excluded.bound_input_pipe,bound_output_pipe=excluded.bound_output_pipe,bound_input_fd=excluded.bound_input_fd,bound_output_fd=excluded.bound_output_fd,bound_session_id='',bound_terminated=0
 WHERE cursor_runtimes.bound_launch_token<>excluded.bound_launch_token OR cursor_runtimes.bound_life_number<>excluded.bound_life_number`
	args := []any{b.GatewayPID, b.GatewayStart, b.RuntimePID, b.RuntimeStart, b.Connection, b.InputPipe, b.OutputPipe, b.InputFD, b.OutputFD, b.InstanceID, b.Token, b.Life, b.GatewayPID, b.GatewayStart}
	args = append(args, snapshotMatchArgs(row.Snapshot)...)
	return s.execGuarded(q, args, "store: register Cursor runtime")
}

func (s *Store) ReadCursorRuntime(id string) (b CursorRuntime, err error) {
	b.InstanceID = id
	err = s.db.QueryRow(`SELECT bound_launch_token,bound_life_number,bound_gateway_pid,bound_gateway_start,bound_runtime_pid,bound_runtime_start,bound_connection_id,bound_input_pipe,bound_output_pipe,bound_input_fd,bound_output_fd,bound_session_id,bound_terminated FROM cursor_runtimes WHERE bound_instance_id=?`, id).Scan(&b.Token, &b.Life, &b.GatewayPID, &b.GatewayStart, &b.RuntimePID, &b.RuntimeStart, &b.Connection, &b.InputPipe, &b.OutputPipe, &b.InputFD, &b.OutputFD, &b.SessionID, &b.Terminated)
	return
}

// SetCursorRuntimeSession and MarkCursorRuntimeGone preserve the complete
// connection identity. Their caller first checks the independent kernel facts.
func (s *Store) SetCursorRuntimeSession(b CursorRuntime, sid string) (bool, error) {
	return s.execGuarded(`UPDATE cursor_runtimes SET bound_session_id=? WHERE bound_session_id='' AND bound_terminated=0 AND `+cursorBindingMatchSQL+` AND EXISTS(SELECT 1 FROM spawns WHERE spawns.instance_id=cursor_runtimes.bound_instance_id AND agent='cursor' AND launch_token=cursor_runtimes.bound_launch_token AND life_number=cursor_runtimes.bound_life_number AND pane_pid=cursor_runtimes.bound_gateway_pid AND pane_starttime=cursor_runtimes.bound_gateway_start AND state='pending')`, append([]any{sid}, cursorBindingArgs(b)...), "store: Cursor session")
}

// BindLoadedCursorSession publishes only a confirmed load of the examined
// latest durable identity into this launch's previously unbound connection.
// Lock acquisition, SQL, COMMIT and cleanup retain the original absolute
// load bound. current must recheck captured input authority and runtime facts;
// a raced or unconfirmed COMMIT never grants prompt admission.
func (s *Store) BindLoadedCursorSession(ctx context.Context, row Spawn, b CursorRuntime, path string, until time.Time, current func() bool) (bool, error) {
	if b.Terminated || b.InstanceID != row.InstanceID || b.GatewayPID != row.Identity.PanePID || b.GatewayStart != row.Identity.PaneStarttime || b.SessionID == "" || row.SessionID != b.SessionID || row.JSONLPath != path || path == "" || row.Identity.Token != b.Token || row.LifeNumber != b.Life {
		return false, nil
	}
	args := append([]any{b.SessionID}, cursorBindingArgs(b)...)
	args = append(args, b.SessionID, path)
	args = append(args, snapshotMatchArgs(row.Snapshot)...)
	args = append(args, row.Identity.Socket, row.Identity.ServerPID, row.Identity.ServerStart, row.Identity.ServerStarttime, row.Identity.PaneID)
	ctx, cancel := context.WithDeadline(ctx, until)
	defer cancel()
	applied := false
	err := s.inWriteTxContext(ctx, time.Until(until), func(ctx context.Context, conn *sql.Conn) error {
		if current == nil || !current() || ctx.Err() != nil || !time.Now().Before(until) {
			return fmt.Errorf("store: loaded Cursor publication retired")
		}
		result, err := conn.ExecContext(ctx, `UPDATE cursor_runtimes SET bound_session_id=? WHERE bound_session_id='' AND bound_terminated=0 AND `+cursorBindingMatchSQL+` AND EXISTS(SELECT 1 FROM spawns WHERE spawns.instance_id=cursor_runtimes.bound_instance_id AND agent='cursor' AND launch_token=cursor_runtimes.bound_launch_token AND life_number=cursor_runtimes.bound_life_number AND pane_pid=cursor_runtimes.bound_gateway_pid AND pane_starttime=cursor_runtimes.bound_gateway_start AND state='pending' AND session_id=? AND jsonl_path=? AND `+snapshotMatchSQL+` AND COALESCE(tmux_socket,'')=? AND COALESCE(tmux_server_pid,0)=? AND COALESCE(tmux_server_started,0)=? AND COALESCE(tmux_server_starttime,'')=? AND COALESCE(pane_id,'')=?)`, args...)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		applied = n == 1
		if err == nil && !current() {
			return fmt.Errorf("store: loaded Cursor publication retired before commit")
		}
		return err
	})
	// The callback's UPDATE is provisional. Only confirmed COMMIT is success;
	// the shared transaction helper preserves unconfirmed-commit classification.
	return err == nil && applied, err
}
func (s *Store) MarkCursorRuntimeGone(b CursorRuntime) (bool, error) {
	return s.execGuarded(`UPDATE cursor_runtimes SET bound_terminated=1 WHERE `+cursorBindingMatchSQL+` AND EXISTS(SELECT 1 FROM spawns WHERE spawns.instance_id=cursor_runtimes.bound_instance_id AND agent='cursor' AND launch_token=cursor_runtimes.bound_launch_token AND life_number=cursor_runtimes.bound_life_number AND pane_pid=cursor_runtimes.bound_gateway_pid AND pane_starttime=cursor_runtimes.bound_gateway_start)`, cursorBindingArgs(b), "store: retire Cursor runtime")
}

const cursorBindingMatchSQL = `bound_instance_id=? AND bound_launch_token=? AND bound_life_number=? AND bound_gateway_pid=? AND bound_gateway_start=? AND bound_runtime_pid=? AND bound_runtime_start=? AND bound_connection_id=? AND bound_input_pipe=? AND bound_output_pipe=? AND bound_input_fd=? AND bound_output_fd=?`

func cursorBindingArgs(b CursorRuntime) []any {
	return []any{b.InstanceID, b.Token, b.Life, b.GatewayPID, b.GatewayStart, b.RuntimePID, b.RuntimeStart, b.Connection, b.InputPipe, b.OutputPipe, b.InputFD, b.OutputFD}
}

// MarkCursorMissingIfSameLaunch closes a lost gateway or a failed Run using
// the common terminal transaction, including request and subagent closure.
// The complete examined row launch and descriptor must still match. A retained
// obsolete descriptor is guarded as present, without granting launch authority.
// A descriptor
// with every runtime field empty represents examined absence of registration;
// that branch requires no binding for this instance at the terminal write.
// It never waits for the write lock: contention returns ErrStoreBusy with no
// writes. SQL, cleanup and fail-open post-commit audit share one bounded context.
func (s *Store) MarkCursorMissingIfSameLaunch(row Spawn, b CursorRuntime) (string, CondResult, error) {
	guard, guardArgs := cursorMissingGuard(row, b)
	if guard == "" {
		return "", CondChanged, nil
	}
	args := append(immediateStateArgs(StateMissing), guardArgs...)
	stmt := markMissingSetSQL + ` WHERE ` + guard

	return s.terminalMarkWithoutWait("store: close lost Cursor launch", stmt, args, row.InstanceID, DecisionReasonFindMissing)
}

// ReadLiveCursorSpawn confines liveness reconciliation to one live Cursor row.
// Other agents and terminal rows retain their normal public read paths.
func (s *Store) ReadLiveCursorSpawn(id string) (Spawn, bool, error) {
	row, err := scanSpawn(s.db.QueryRow(getSpawnSQL+` AND agent='cursor' AND `+liveStateGuardSQL, append([]any{id}, liveStateGuardArgs()...)...), getSpawnErrs)
	if errors.Is(err, sql.ErrNoRows) {
		return Spawn{}, false, nil
	}
	return row, err == nil, err
}
