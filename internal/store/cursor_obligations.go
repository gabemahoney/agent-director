package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
)

// CursorOwner is captured before dispatch. Persisted evidence never grants a
// replacement helper authority to replay an input or permission response.
type CursorOwner struct {
	StoreID string
	Runtime CursorRuntime
}

// CursorRuntime deliberately has no public JSON surface. This private encoding
// retains its complete binding only inside the protected store.
type cursorRuntimeEvidence struct {
	InstanceID   string
	Token        string
	Life         int64
	GatewayPID   int
	GatewayStart string
	RuntimePID   int
	RuntimeStart string
	Connection   string
	InputPipe    string
	OutputPipe   string
	InputFD      int
	OutputFD     int
	SessionID    string
	Terminated   bool
}

func (o CursorOwner) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		StoreID string
		Runtime cursorRuntimeEvidence
	}{o.StoreID, cursorRuntimeEvidence(o.Runtime)})
}
func (o *CursorOwner) UnmarshalJSON(raw []byte) error {
	var v struct {
		StoreID string
		Runtime cursorRuntimeEvidence
	}
	if err := decodeCursorRecord(raw, &v); err != nil {
		return err
	}
	o.StoreID, o.Runtime = v.StoreID, CursorRuntime(v.Runtime)
	return nil
}
func (o CursorOwner) valid() bool {
	b := o.Runtime
	return validStoreID(o.StoreID) && b.InstanceID != "" && b.Token != "" && b.Life >= 0 && b.Connection != "" && b.GatewayPID > 0 && b.GatewayStart != "" && b.RuntimePID > 0 && b.RuntimeStart != "" && b.InputPipe != "" && b.OutputPipe != "" && b.InputFD >= 0 && b.OutputFD >= 0 && !b.Terminated
}

// CursorInput retains original admission/order/epoch and the eventual target
// separately: a held input has no admitted target until publication completes.
// Started and issued are intents, never receipt or execution evidence.
type CursorInput struct {
	Owner         CursorOwner
	Generation    uint64
	SubmissionID  string
	Order         uint64
	Epoch         uint64
	Text          string
	CapturedAt    time.Time
	LoadedPath    string
	State         string
	TargetSession string
	RequestID     string
	Deadline      time.Time
	Detail        string
}

const (
	CursorInputControl    = "control"
	CursorInputHeld       = "held"
	CursorInputAdmitted   = "admitted"
	CursorInputStarted    = "started"
	CursorInputIssued     = "issued"
	CursorInputCompleted  = "completed"
	CursorInputRefused    = "refused"
	CursorInputCancelled  = "cancelled"
	CursorInputUnresolved = "unresolved"
)

func (v CursorInput) validate() error {
	if !v.Owner.valid() || v.SubmissionID == "" || len(v.SubmissionID) > 512 || len(v.Text) > 1024*1024 || v.CapturedAt.IsZero() || len(v.Detail) > 4096 {
		return fmt.Errorf("store: invalid Cursor input evidence")
	}
	switch v.State {
	case CursorInputControl, CursorInputHeld:
		if v.TargetSession != "" || v.RequestID != "" || !v.Deadline.IsZero() {
			return fmt.Errorf("store: unadmitted Cursor input has a target")
		}
	case CursorInputRefused, CursorInputCancelled, CursorInputUnresolved:
		if (v.RequestID == "") != v.Deadline.IsZero() || v.RequestID != "" && v.TargetSession == "" {
			return fmt.Errorf("store: invalid final Cursor input identity")
		}
	case CursorInputAdmitted:
		if v.TargetSession == "" || v.RequestID != "" || !v.Deadline.IsZero() {
			return fmt.Errorf("store: Cursor input target missing")
		}
	case CursorInputStarted, CursorInputIssued, CursorInputCompleted:
		if v.TargetSession == "" || v.RequestID == "" || v.Deadline.IsZero() {
			return fmt.Errorf("store: Cursor input start identity missing")
		}
	default:
		return fmt.Errorf("store: invalid Cursor input disposition")
	}
	return nil
}

// CaptureCursorInput records only an input actually observed by the current
// helper. It is not a queue reader. A duplicate cannot acquire new authority.
func (s *Store) CaptureCursorInput(ctx context.Context, v CursorInput, until time.Time, check CursorCheck) error {
	ctx, cancel := context.WithDeadline(ctx, until)
	defer cancel()
	if err := v.validate(); err != nil {
		return err
	}
	if v.State != CursorInputControl && v.State != CursorInputHeld && v.State != CursorInputAdmitted && v.State != CursorInputRefused && v.State != CursorInputCancelled && v.State != CursorInputUnresolved {
		return fmt.Errorf("store: Cursor input must be captured before start")
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.inWriteTxContext(ctx, max(time.Until(until), 0), func(ctx context.Context, conn *sql.Conn) error {
		if v.State == CursorInputAdmitted {
			if v.TargetSession != v.Owner.Runtime.SessionID {
				return fmt.Errorf("store: Cursor admission target changed")
			}
			if err := s.cursorInputAuthority(ctx, conn, v.Owner, v.TargetSession, v.LoadedPath, until, check); err != nil {
				return err
			}
		} else if err := s.cursorInputObservation(ctx, conn, v.Owner); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, `INSERT INTO cursor_inputs(store_id,connection_id,instance_id,submission_id,record) VALUES(?,?,?,?,?)`, v.Owner.StoreID, v.Owner.Runtime.Connection, v.Owner.Runtime.InstanceID, v.SubmissionID, string(raw))
		return err
	})
}

func (s *Store) ReadCursorInput(connection, submission string) (CursorInput, error) {
	var raw string
	err := s.db.QueryRow(`SELECT record FROM cursor_inputs WHERE store_id=? AND connection_id=? AND submission_id=?`, s.StoreID(), connection, submission).Scan(&raw)
	var v CursorInput
	if err != nil {
		return v, err
	}
	if err = decodeCursorRecord([]byte(raw), &v); err == nil {
		err = v.validate()
	}
	if err == nil && (v.Owner.StoreID != s.StoreID() || v.Owner.Runtime.Connection != connection || v.SubmissionID != submission) {
		err = fmt.Errorf("store: mismatched Cursor input evidence")
	}
	return v, err
}

// AdvanceCursorInput is an exact compare-and-swap, including the original
// payload, binding and deadlines. The supplied callback also fences the local
// operation epoch. Unknown results authorize neither dispatch nor another CAS.
func (s *Store) AdvanceCursorInput(ctx context.Context, old, next CursorInput, until time.Time, check CursorCheck) error {
	ctx, cancel := context.WithDeadline(ctx, until)
	defer cancel()
	if err := old.validate(); err != nil {
		return err
	}
	if err := next.validate(); err != nil {
		return err
	}
	a, b := old, next
	a.State, a.TargetSession, a.RequestID, a.Deadline, a.Detail = "", "", "", time.Time{}, ""
	b.State, b.TargetSession, b.RequestID, b.Deadline, b.Detail = "", "", "", time.Time{}, ""
	if a != b || !cursorInputTransition(old, next) {
		return fmt.Errorf("store: invalid Cursor input transition")
	}
	before, _ := json.Marshal(old)
	after, _ := json.Marshal(next)
	return s.inWriteTxContext(ctx, max(time.Until(until), 0), func(ctx context.Context, conn *sql.Conn) error {
		target := old.TargetSession
		if target == "" {
			target = old.Owner.Runtime.SessionID
		}
		if old.State == CursorInputHeld && next.State == CursorInputAdmitted {
			target = next.TargetSession
		}
		if cursorInputFinal(next.State) {
			if err := s.cursorInputObservation(ctx, conn, old.Owner); err != nil {
				return err
			}
		} else if err := s.cursorInputAuthority(ctx, conn, old.Owner, target, old.LoadedPath, until, check); err != nil {
			return err
		}
		if next.State == CursorInputIssued {
			var held int
			if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM permission_requests AS pr WHERE pr.instance_id=? AND `+cursorHoldsDialogSQL, old.Owner.Runtime.InstanceID, old.Owner.Runtime.Token, old.Owner.Runtime.Token).Scan(&held); err != nil {
				return err
			}
			if held != 0 || !time.Now().Before(next.Deadline) {
				return fmt.Errorf("store: Cursor input start retired or held")
			}
		}
		res, err := conn.ExecContext(ctx, `UPDATE cursor_inputs SET record=? WHERE store_id=? AND connection_id=? AND submission_id=? AND record=?`, string(after), old.Owner.StoreID, old.Owner.Runtime.Connection, old.SubmissionID, string(before))
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err == nil && n != 1 {
			err = fmt.Errorf("store: Cursor input evidence changed")
		}
		return err
	})
}

func cursorInputTransition(old, next CursorInput) bool {
	if cursorInputFinal(old.State) {
		return false
	}
	if next.State == CursorInputCancelled || next.State == CursorInputRefused || next.State == CursorInputUnresolved {
		return old.TargetSession == next.TargetSession && old.RequestID == next.RequestID && old.Deadline.Equal(next.Deadline)
	}
	switch old.State {
	case CursorInputHeld:
		return next.State == CursorInputAdmitted && next.TargetSession != "" && next.RequestID == "" && next.Deadline.IsZero()
	case CursorInputAdmitted:
		return next.State == CursorInputStarted && next.TargetSession == old.TargetSession && old.RequestID == "" && next.RequestID != "" && !next.Deadline.IsZero()
	case CursorInputStarted, CursorInputIssued:
		return (old.State == CursorInputStarted && next.State == CursorInputIssued || old.State == CursorInputIssued && next.State == CursorInputCompleted) && next.TargetSession == old.TargetSession && next.RequestID == old.RequestID && next.Deadline.Equal(old.Deadline)
	}
	return false
}

// Final dispositions carry no issue authority. Retain them even after session
// publication/runtime exit, only for the original physical connection and life.
// A replacement helper cannot adopt them and no read path resumes them.
func cursorInputFinal(state string) bool {
	return state == CursorInputControl || state == CursorInputCompleted || state == CursorInputRefused || state == CursorInputCancelled || state == CursorInputUnresolved
}
func (s *Store) cursorInputObservation(ctx context.Context, conn *sql.Conn, owner CursorOwner) error {
	if owner.StoreID != s.StoreID() {
		return fmt.Errorf("store: Cursor evidence store changed")
	}
	if err := permissionResponseDurability(ctx, conn); err != nil {
		return err
	}
	var n int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM cursor_runtimes WHERE `+cursorBindingMatchSQL+` AND EXISTS(SELECT 1 FROM spawns WHERE instance_id=bound_instance_id AND agent='cursor' AND launch_token=bound_launch_token AND life_number=bound_life_number AND pane_pid=bound_gateway_pid AND pane_starttime=bound_gateway_start)`, cursorBindingArgs(owner.Runtime)...).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("store: Cursor evidence connection changed")
	}
	return nil
}

func (s *Store) cursorInputAuthority(ctx context.Context, conn *sql.Conn, owner CursorOwner, target, loadedPath string, until time.Time, check CursorCheck) error {
	if owner.StoreID != s.StoreID() {
		return fmt.Errorf("store: Cursor input store changed")
	}
	if err := permissionResponseDurability(ctx, conn); err != nil {
		return err
	}
	b := owner.Runtime
	b.SessionID = target
	_, err := s.cursorAuthorityForPrompt(ctx, conn, b, until, check, loadedPath)
	return err
}

// CursorProof is an immutable, validated provider event. Raw is stored as a
// BLOB without parsing/re-encoding. Ledger insertion and existing request,
// state and family effects commit in the same proveCursorTool transaction.
type CursorProof struct {
	Owner      CursorOwner
	ID         string
	Token      string
	Tool       string
	Generation uint64
	Kind       string
	CapturedAt time.Time
	BusyUntil  time.Time
	Ran        bool
	TurnOpen   bool
	Raw        []byte `json:"-"`
}

func (p CursorProof) validate() error {
	if !p.Owner.valid() || p.Owner.Runtime.SessionID == "" || p.ID == "" || len(p.ID) > 128 || p.Token == "" || p.Tool == "" || p.Kind != "tool_call_update" || p.CapturedAt.IsZero() || p.BusyUntil.Before(p.CapturedAt) || len(p.Raw) == 0 || len(p.Raw) > 2*1024*1024 || !json.Valid(p.Raw) {
		return fmt.Errorf("store: invalid Cursor proof evidence")
	}
	// Provider envelopes may contain additional fields, but repeated fields
	// cannot supply unambiguous proof. Inspect without rewriting the raw bytes.
	if err := validateCursorJSONFields(p.Raw); err != nil {
		return err
	}
	var raw struct {
		Session string `json:"sessionId"`
		Update  struct {
			Kind   string          `json:"sessionUpdate"`
			Tool   string          `json:"toolCallId"`
			Status string          `json:"status"`
			Output json.RawMessage `json:"rawOutput"`
		} `json:"update"`
	}
	if json.Unmarshal(p.Raw, &raw) != nil || raw.Session != p.Owner.Runtime.SessionID || raw.Update.Kind != p.Kind || raw.Update.Tool != p.Tool {
		return fmt.Errorf("store: mismatched Cursor proof source")
	}
	// The gateway additionally matches the admitted authorization adapter.
	// Keep failed-invocation evidence incapable of claiming actual execution,
	// including when validating compatible migration reentry.
	var output map[string]json.RawMessage
	if json.Unmarshal(raw.Update.Output, &output) != nil {
		return fmt.Errorf("store: invalid Cursor proof output")
	}
	var exit int
	var stdout, stderr, message string
	var success bool
	shell := (raw.Update.Status == "completed" || raw.Update.Status == "failed") &&
		json.Unmarshal(output["exitCode"], &exit) == nil && !bytes.Equal(bytes.TrimSpace(output["exitCode"]), []byte("null")) &&
		json.Unmarshal(output["stdout"], &stdout) == nil && !bytes.Equal(bytes.TrimSpace(output["stdout"]), []byte("null")) &&
		json.Unmarshal(output["stderr"], &stderr) == nil && !bytes.Equal(bytes.TrimSpace(output["stderr"]), []byte("null"))
	mcp := raw.Update.Status == "completed" && len(output) == 1 && json.Unmarshal(output["success"], &success) == nil && success
	failed := raw.Update.Status == "completed" && len(output) == 1 && json.Unmarshal(output["error"], &message) == nil && message == "Tool execution error"
	if p.Ran && !shell && !mcp || !p.Ran && !failed {
		return fmt.Errorf("store: Cursor proof outcome contradicts captured output")
	}
	return nil
}

func (s *Store) ReadCursorProof(connection, id string) (CursorProof, error) {
	var raw string
	var evidence []byte
	err := s.db.QueryRow(`SELECT record,raw FROM cursor_proofs WHERE store_id=? AND connection_id=? AND proof_id=?`, s.StoreID(), connection, id).Scan(&raw, &evidence)
	var p CursorProof
	if err != nil {
		return p, err
	}
	if err = decodeCursorRecord([]byte(raw), &p); err != nil {
		return p, err
	}
	p.Raw = evidence
	if err = p.validate(); err == nil && (p.Owner.StoreID != s.StoreID() || p.Owner.Runtime.Connection != connection || p.ID != id) {
		err = fmt.Errorf("store: mismatched Cursor proof record")
	}
	return p, err
}

func (s *Store) recordCursorProof(ctx context.Context, conn *sql.Conn, p CursorProof) (bool, error) {
	if err := permissionResponseDurability(ctx, conn); err != nil {
		return false, err
	}
	if p.Owner.StoreID != s.StoreID() {
		return false, fmt.Errorf("store: Cursor proof store changed")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return false, err
	}
	var existing string
	var evidence []byte
	err = conn.QueryRowContext(ctx, `SELECT record,raw FROM cursor_proofs WHERE store_id=? AND connection_id=? AND proof_id=?`, p.Owner.StoreID, p.Owner.Runtime.Connection, p.ID).Scan(&existing, &evidence)
	if err == nil {
		if existing != string(raw) || !bytes.Equal(evidence, p.Raw) {
			return false, fmt.Errorf("store: conflicting Cursor proof identity")
		}
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	// Proof may outlive response authority, but never its admitted owner/tool.
	var matches int
	b := p.Owner.Runtime
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM permission_requests WHERE instance_id=? AND request_token=? AND tool_use_id=? AND owner_launch_token=? AND owner_life_number=? AND owner_session_id=? AND owner_cursor_connection=?`, b.InstanceID, p.Token, p.Tool, b.Token, b.Life, b.SessionID, b.Connection).Scan(&matches); err != nil {
		return false, err
	}
	if matches != 1 {
		return false, fmt.Errorf("store: Cursor proof request changed")
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO cursor_proofs(store_id,connection_id,instance_id,proof_id,record,raw) VALUES(?,?,?,?,?,?)`, p.Owner.StoreID, b.Connection, b.InstanceID, p.ID, string(raw), p.Raw)
	return err == nil, err
}

func decodeCursorRecord(raw []byte, v any) error {
	if err := validateCursorJSONFields(raw); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("store: trailing Cursor evidence")
	}
	return nil
}

func validateCursorJSONFields(raw []byte) error {
	// Struct decoding accepts repeated keys (including case aliases). Evidence
	// must have one meaning before either migration or a live read accepts it.
	keys := json.NewDecoder(bytes.NewReader(raw))
	keys.UseNumber()
	var objects []map[string]bool
	for {
		token, err := keys.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		switch token {
		case json.Delim('{'):
			objects = append(objects, map[string]bool{})
		case json.Delim('['):
			objects = append(objects, nil)
		case json.Delim('}'), json.Delim(']'):
			objects = objects[:len(objects)-1]
		default:
			if len(objects) == 0 || objects[len(objects)-1] == nil {
				continue
			}
			// Consume the whole value separately so strings in values are not
			// mistaken for keys, while nested objects are checked recursively.
			key, ok := token.(string)
			if !ok {
				return fmt.Errorf("store: invalid Cursor evidence field")
			}
			seen := objects[len(objects)-1]
			// Match encoding/json's field-name folding, including Unicode
			// aliases such as long s, by choosing each fold set's smallest rune.
			key = strings.Map(func(r rune) rune {
				for {
					next := unicode.SimpleFold(r)
					if next <= r {
						return next
					}
					r = next
				}
			}, key)
			if seen[key] {
				return fmt.Errorf("store: duplicate Cursor evidence field")
			}
			seen[key] = true
			var value json.RawMessage
			if err := keys.Decode(&value); err != nil {
				return err
			}
			if err := validateCursorJSONFields(value); err != nil {
				return err
			}
		}
	}
	return nil
}
