package runner

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

type Row map[string]any
type Snapshot struct {
	Observed      time.Time        `json:"observed"`
	ReadCompleted time.Time        `json:"read_completed"`
	InstanceID    string           `json:"instance_id"`
	Tables        map[string][]Row `json:"tables"`
}

// These columns are deliberately named. Never add SELECT *, source environment,
// config, labels, or other instances' text to a live evidence export.
var queries = map[string]string{
	"spawns":    `SELECT instance_id,state,session_id,jsonl_path,pid,proc_starttime,launch_token,tmux_socket,launch_started_at,turn_starts FROM spawns WHERE instance_id=?`,
	"launches":  `SELECT launch_id,store_id,instance_id,launch_token,phase,provider,route_kind,route_id,connection_generation,wake_path,recorded_at_ms,ended_at_ms,end_cause FROM busy_send_launches WHERE instance_id=? ORDER BY recorded_at_ms`,
	"messages":  `SELECT message_id,instance_id,text,text_sha256,accepted_seq,accepted_at_ms,send_mode,place,handover_id,bound_launch_id,bound_session_id,result_class,result_reason FROM busy_send_messages WHERE instance_id=? ORDER BY accepted_seq`,
	"handovers": `SELECT handover_id,instance_id,launch_id,launch_token,session_id,boundary,forced,force_id,expected_turn_starts,intent_turn_starts,intent_running_turn,claimed_at_ms,closed_at_ms FROM busy_send_handovers WHERE instance_id=? ORDER BY handover_seq`,
	"prompts":   `SELECT handover_id,prompt_index,message_count,emission,issued_at_ms,transport,transport_at_ms,order_kind,order_at_ms,admission_kind,admitted_at_ms FROM busy_send_prompts WHERE handover_id IN (SELECT handover_id FROM busy_send_handovers WHERE instance_id=?) ORDER BY issued_at_ms,prompt_index`,
	"members":   `SELECT handover_id,member_index,message_id,prompt_index FROM busy_send_handover_members WHERE handover_id IN (SELECT handover_id FROM busy_send_handovers WHERE instance_id=?) ORDER BY handover_id,member_index`,
	"forces":    `SELECT force_id,instance_id,launch_id,handover_id,outcome,reason,started_at_ms,ended_at_ms FROM busy_send_forces WHERE instance_id=? ORDER BY started_at_ms`,
}

func OpenReadOnly(path string) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(1000)"}
	db, err := sql.Open("sqlite", u.String())
	if err == nil {
		db.SetMaxOpenConns(1)
	}
	return db, err
}

func Observe(ctx context.Context, path, id string) (Snapshot, error) {
	return observe(ctx, path, id, queries)
}

// activityQueries never read Message bodies or enumerate the growing backlog.
// The prompt row is only an existence witness, not a custody/evidence snapshot.
var activityQueries = map[string]string{
	"spawns":   queries["spawns"],
	"launches": queries["launches"],
	"prompts":  `SELECT 1 AS issued FROM busy_send_prompts WHERE handover_id IN (SELECT handover_id FROM busy_send_handovers WHERE instance_id=?) LIMIT 1`,
}

func observe(ctx context.Context, path, id string, selected map[string]string) (Snapshot, error) {
	s := Snapshot{Observed: time.Now().UTC(), InstanceID: id, Tables: map[string][]Row{}}
	if id == "" {
		return s, errors.New("observation requires an owned instance")
	}
	db, err := OpenReadOnly(path)
	if err != nil {
		return s, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return s, err
	}
	defer tx.Rollback()

	var schema int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema); err != nil {
		return s, err
	}
	for name, q := range selected {
		if schema > 0 && schema < 21 {
			if name != "spawns" {
				s.Tables[name] = []Row{}
				continue
			}
			q = `SELECT instance_id,state,session_id,jsonl_path,pid,proc_starttime,launch_token,tmux_socket,launch_started_at,0 AS turn_starts FROM spawns WHERE instance_id=?`
		}

		rows, err := tx.QueryContext(ctx, q, id)
		if err != nil {
			return s, fmt.Errorf("%s: %w", name, err)
		}
		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			return s, err
		}
		s.Tables[name] = []Row{}
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err = rows.Scan(ptrs...); err != nil {
				rows.Close()
				return s, err
			}
			row := Row{}
			for i, k := range cols {
				if b, ok := vals[i].([]byte); ok {
					row[k] = string(b)
				} else {
					row[k] = vals[i]
				}
			}
			s.Tables[name] = append(s.Tables[name], row)
		}
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			return s, err
		}
	}
	if err := tx.Commit(); err != nil {
		return s, err
	}
	// Observed precedes opening the DB. Only this successful read's upper
	// bound can prove that a retained Held state predates a terminal response.
	s.ReadCompleted = time.Now().UTC()
	return s, nil
}

func Text(row Row, key string) string { s, _ := row[key].(string); return s }
func Number(row Row, key string) int64 {
	switch n := row[key].(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return 0
}

func (r *Run) Observe(ctx context.Context, id string) (Snapshot, error) {
	ctx, c := context.WithTimeout(ctx, 3*time.Second)
	defer c()
	return Observe(ctx, filepath.Join(r.Home, ".config/agent-director/state.db"), id)
}

func (r *Run) observeActivity(ctx context.Context, id string) (Snapshot, error) {
	ctx, c := context.WithTimeout(ctx, 3*time.Second)
	defer c()
	return observe(ctx, filepath.Join(r.Home, ".config/agent-director/state.db"), id, activityQueries)
}

var ErrTurnBudget = errors.New("observed turn budget exceeded")

// Bind is the exercise guard. Cleanup uses BindOwned so an exceeded budget
// stops exercise work without revoking authority to stop this owned Launch.
func (r *Run) Bind(s Snapshot) error {
	if err := r.BindOwned(s); err != nil {
		return err
	}
	return r.CheckTurnBudget()
}

func (r *Run) CheckTurnBudget() error {
	cause := "no case recorded"
	if len(r.Manifest.Cases) > 0 {
		cause = r.Manifest.Cases[len(r.Manifest.Cases)-1].ID
	}
	if r.Manifest.ObservedTurns > int64(r.Manifest.TurnBudget) {
		return fmt.Errorf("%w: phase cap exceeded; observed=%d reserved=%d cap=%d cause=%s", ErrTurnBudget, r.Manifest.ObservedTurns, r.Manifest.TurnsReserved, r.Manifest.TurnBudget, cause)
	}
	if r.StopOnUnreserved && r.Manifest.ObservedTurns > int64(r.Manifest.TurnsReserved) {
		return fmt.Errorf("%w: case reservation exceeded; observed=%d reserved=%d phase_cap=%d cause=%s", ErrTurnBudget, r.Manifest.ObservedTurns, r.Manifest.TurnsReserved, r.Manifest.TurnBudget, cause)
	}
	return nil
}

// BindOwned validates ownership before changing any recorded identity. A
// mismatched later Launch never inherits cleanup authority.
func (r *Run) BindOwned(s Snapshot) error {
	for i := range r.Manifest.Owned {
		o := r.Manifest.Owned[i]
		if o.InstanceID != s.InstanceID {
			continue
		}
		if o.Exited && o.LaunchToken != "" && len(s.Tables["spawns"]) == 1 && o.LaunchToken != Text(s.Tables["spawns"][0], "launch_token") {
			continue
		}
		if len(s.Tables["spawns"]) != 1 {
			return errors.New("owned spawn absent")
		}
		row := s.Tables["spawns"][0]
		if o.LaunchToken != "" && o.LaunchToken != Text(row, "launch_token") {
			return errors.New("owned Launch replaced")
		}
		o.LaunchToken, o.SessionID, o.Transcript = Text(row, "launch_token"), Text(row, "session_id"), Text(row, "jsonl_path")
		if socket := Text(row, "tmux_socket"); socket != "" && socket != o.Socket {
			return errors.New("spawn used another tmux socket")
		}
		o.PID, o.ProcessStart = int(Number(row, "pid")), Text(row, "proc_starttime")
		for _, l := range s.Tables["launches"] {
			if Text(l, "launch_token") == o.LaunchToken {
				o.LaunchID = Text(l, "launch_id")
			}
		}
		o.ObservedTurns = max(o.ObservedTurns, Number(row, "turn_starts"))
		r.Manifest.Owned[i] = o
		var total int64
		for _, owned := range r.Manifest.Owned {
			if owned.Agent == "" || owned.Agent == "claude" {
				total += owned.ObservedTurns
			}
		}
		r.Manifest.ObservedTurns = max(r.Manifest.ObservedTurns, total)
		return nil
	}
	return errors.New("unowned observation")
}

func (r *Run) Await(ctx context.Context, id string, limit time.Duration, predicate func(Snapshot) bool) (Snapshot, error) {
	return r.AwaitState(ctx, id, limit, false, predicate)
}

// AwaitState permits only explicitly expected Dialog exercises; ordinary
// callers retain Await's stop-on-Dialog behavior and its bounded observation loop.
func (r *Run) AwaitState(ctx context.Context, id string, limit time.Duration, dialogExpected bool, predicate func(Snapshot) bool) (Snapshot, error) {
	return r.AwaitStateChecked(ctx, id, limit, dialogExpected, nil, predicate)
}

// AwaitStateChecked checks provider progress only for explicit operational callers.
// Ordinary Await/cleanup/process-exit waits retain their unchecked contract.
func (r *Run) AwaitStateChecked(ctx context.Context, id string, limit time.Duration, dialogExpected bool, check func(context.Context, Snapshot) error, predicate func(Snapshot) bool) (Snapshot, error) {
	return r.awaitStateChecked(ctx, id, limit, dialogExpected, r.Observe, check, predicate)
}

// AwaitActivityChecked retains the ownership, budget, Dialog and provider guards
// without reading retained Messages. Its predicate sees only spawns, launches
// and at most one prompt-existence row. Collect full custody evidence separately.
// Future activity-only guards must reuse this wait and its ownership/deadline
// checks; see the shared live runner reuse rules in docs/architecture.md.
func (r *Run) AwaitActivityChecked(ctx context.Context, id string, limit time.Duration, dialogExpected bool, check func(context.Context, Snapshot) error, predicate func(Snapshot) bool) (Snapshot, error) {
	return r.awaitStateChecked(ctx, id, limit, dialogExpected, r.observeActivity, check, predicate)
}

func (r *Run) awaitStateChecked(ctx context.Context, id string, limit time.Duration, dialogExpected bool, observe func(context.Context, string) (Snapshot, error), check func(context.Context, Snapshot) error, predicate func(Snapshot) bool) (Snapshot, error) {
	ctx, c := context.WithTimeout(ctx, limit)
	defer c()
	for {
		s, err := observe(ctx, id)
		if err != nil {
			return s, err
		}
		if err = r.Bind(s); err != nil {
			return s, err
		}
		state := Text(s.Tables["spawns"][0], "state")
		if !dialogExpected && (state == "check_permission" || state == "ask_user") {
			return s, errors.New("unexpected dialog; intelligence required, no answer sent")
		}
		if check != nil {
			if err = check(ctx, s); err != nil {
				if !errors.Is(err, ErrTranscriptBindingPending) {
					return s, err
				}
				if err = r.Sleep(ctx, 200*time.Millisecond); err != nil {
					return s, err
				}
				continue
			}
		}
		if predicate(s) {
			return s, nil
		}
		if err = r.Sleep(ctx, 200*time.Millisecond); err != nil {
			return s, err
		}
	}
}

func (r *Run) Collect(ctx context.Context, id, label string) error {
	s, err := r.Observe(ctx, id)
	if err != nil {
		return err
	}
	if err = r.BindOwned(s); err != nil {
		return err
	}
	if err = r.Evidence.JSON("raw/"+label+"-store.json", s); err != nil {
		return err
	}
	_, _, paneErr := r.AD(ctx, label+"-pane", "read-pane", "--instance-id", id, "--n-lines", "80")
	return errors.Join(paneErr, r.CheckTurnBudget())
}

// ReadTranscript is a bounded, confined read of the already bound parent. It
// is also used for independent exercise progress, never product delivery proof.
func (r *Run) ReadTranscript(o Owned) ([]byte, error) {
	path, err := TranscriptPath(r.Home, o.CWD, o.SessionID, o.Transcript)
	if err != nil {
		return nil, err
	}
	o.Transcript = path
	if r.Manifest.RecordingHook.Installed {
		if err := checkTranscriptRecordings(r.Home, r.Evidence.Root, r.Manifest.RunID, o, r.Manifest.Owned...); err != nil {
			return nil, err
		}
	}
	base := filepath.Join(r.Home, ".claude", "projects")
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return nil, err
	}
	b, err := Read(base, rel)
	if err == nil {
		for i, current := range r.Manifest.Owned {
			if current.InstanceID == o.InstanceID && current.LaunchToken == o.LaunchToken && current.SessionID == o.SessionID {
				r.Manifest.Owned[i].Transcript = path
			}
		}
	}
	return b, err
}

func (r *Run) CollectFiles(o Owned) error {
	if o.Agent != "" && o.Agent != "claude" {
		return r.collectTrail(o)
	}
	b, err := r.ReadTranscript(o)
	if r.LaunchArtifacts && os.IsNotExist(err) {
		// Absence is an observation, not proof of no input. Qualification's
		// loader must independently establish the narrow expected case.
		err = r.Evidence.JSON("raw/"+r.OwnedPrefix(o)+"-transcript-absence.json", map[string]any{"instance": o.InstanceID, "launch": o.LaunchToken, "session": o.SessionID, "observed": r.Now().UTC(), "reason": "not_exist"})
		if !o.Native {
			err = errors.Join(err, r.collectTrail(o))
		}
		return err
	}
	if err == nil {
		err = r.Evidence.Put("raw/"+r.OwnedPrefix(o)+"-transcript.jsonl", b)
	}
	// A missing transcript must not suppress the independent trail export.
	if o.Native {
		return err
	}
	return errors.Join(err, r.collectTrail(o))
}

func (r *Run) collectTrail(o Owned) error {
	f, err := os.Open(filepath.Join(r.Home, ".config/agent-director/ad-trail.jsonl"))
	if err != nil {
		return err
	}
	defer f.Close()
	var out []byte
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 4096), 1<<20)
	for scan.Scan() {
		var row Row
		if json.Unmarshal(scan.Bytes(), &row) != nil {
			return errors.New("malformed trail")
		}
		if Text(row, "instance_id") == o.InstanceID {
			out = append(out, append(append([]byte(nil), scan.Bytes()...), '\n')...)
			if len(out) > MaxArtifactBytes {
				return errors.New("trail size limit")
			}
		}
	}
	if err = scan.Err(); err != nil {
		return err
	}
	return r.Evidence.Put("raw/"+r.OwnedPrefix(o)+"-trail.jsonl", out)
}

// ProcessGone checks the recorded Linux process start identity; a missing row
// or a reused numeric PID is never evidence that this process is still alive.
func ProcessGone(pid int, start string) (bool, error) {
	return processGone(pid, start, os.ReadFile)
}

func processGone(pid int, start string, readFile func(string) ([]byte, error)) (bool, error) {
	if pid <= 0 || start == "" {
		return false, errors.New("process identity unavailable")
	}
	b, err := readFile("/proc/" + strconv.Itoa(pid) + "/stat")
	// A task can exit after procfs opens stat but before the read completes;
	// Linux then reports ESRCH rather than ENOENT. Both prove this PID gone.
	if os.IsNotExist(err) || errors.Is(err, syscall.ESRCH) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 {
		return false, errors.New("malformed process stat")
	}
	fields := strings.Fields(string(b[i+1:]))
	if len(fields) < 20 {
		return false, errors.New("short process stat")
	}
	return fields[19] != start || fields[0] == "Z" || fields[0] == "X", nil
}

// OwnedPrefix separates resumed Launch artifacts without changing signal captures.
func (r *Run) OwnedPrefix(o Owned) string {
	if r.LaunchArtifacts && o.LaunchToken != "" {
		return o.InstanceID + "-" + o.LaunchToken
	}
	return o.InstanceID
}
