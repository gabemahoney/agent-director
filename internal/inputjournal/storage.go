package inputjournal

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"time"
)

// Evidence confirms only the named value at this revision. A zero At means
// unknown, including imported facts whose historical observation time is absent.
// Store intent is never evidence of provider receipt.
type Evidence struct {
	At       time.Time `json:"at,omitempty"`
	Source   string    `json:"source,omitempty"`
	Value    string    `json:"value,omitempty"`
	Revision int64     `json:"revision,omitempty"`
}

// Launch contains operational authority; EvidenceDir contains logs only.
// Blocker retains imported ownership that cannot safely be attributed. It is
// never interpreted as absent capability, nor cleared by acquiring a lock.
type Launch struct {
	Identity         Identity
	Capability       Capability
	EvidenceDir      string
	Revision         int64
	CurrentRequestID string
	Blocker          string
	Confirmation     Evidence
}

// ImportRecord is a validated snapshot held under the legacy launch lock.
// Evidence retains exact source bytes even when ambiguous ownership requires a
// blocker. Import never treats the snapshot as work to execute.
type ImportRecord struct {
	Launch       Launch
	Attempts     []*Attempt
	Source       string
	SourceSHA256 string
	Evidence     []byte
}

// Guard pins admission to the row observed by the caller. Permission epochs
// may change independently of launch identity and must not be adopted after a
// waited resource. ProofOnly can retain evidence after a turn state changes;
// it cannot authorize text, Enter or a new request.
type Guard struct {
	Identity        Identity
	PermissionEpoch int64
	RowVersion      int64
	SessionID       string
	State           string
	Manual          bool
	ProofOnly       bool
}

var (
	ErrInvalidState = errors.New("input delivery: invalid persisted state")
	ErrStale        = errors.New("input delivery: captured ownership changed")
	ErrUnconfirmed  = errors.New("input delivery: commit outcome unconfirmed")
)

// Persistence is the storage side of Backend/Session, implemented only by the
// store. Both store and inputdelivery must reuse this neutral contract. A
// successful write means confirmed commit. Neither an attempt nor a returned
// revision instructs a consumer to dispatch or retry anything.
type Persistence interface {
	ReadInputPreparation(context.Context, Identity) (Identity, error)
	CheckInputGuard(context.Context, *Launch, Guard) error
	ReadInputLaunch(context.Context, string) (*Launch, error)
	CreateInputLaunch(context.Context, Launch) error
	BindInputLaunch(context.Context, Launch, Guard) (*Launch, error)
	ReadInputAttempt(context.Context, Launch, string) (*Attempt, error)
	SaveInputAttempt(context.Context, Launch, Guard, *Attempt) (*Launch, *Attempt, error)
}

func ValidateLaunch(l Launch) error {
	i, m := l.Identity, l.Capability
	if !hexID.MatchString(i.StoreID) || !hexID.MatchString(i.Token) || l.Revision < 1 ||
		!filepath.IsAbs(l.EvidenceDir) || i.LifeNumber < 0 || l.Revision == math.MaxInt64 {
		return ErrInvalidState
	}
	if l.Blocker != "" {
		// An unresolved legacy owner deliberately has no invented instance/life.
		return nil
	}
	if i.InstanceID == "" || i.Socket == "" || !filepath.IsAbs(i.CWD) ||
		m.InstanceID != i.InstanceID || m.Token != i.Token || m.Socket != i.Socket ||
		m.CWD != i.CWD || m.Agent != i.Agent || m.Created.IsZero() ||
		(i.Agent != "claude" && i.Agent != "codex") ||
		(i.Agent == "claude" && m.Version != "2.1.280") ||
		(i.Agent == "codex" && m.Version != "0.159.2") {
		return ErrInvalidState
	}
	if (i.PaneID == "") != (i.PID == 0) || (i.PID == 0) != (i.Starttime == "") || i.PID < 0 {
		return ErrInvalidState
	}
	return validateEvidence(l.Confirmation, l.Revision, "capability", "store")
}

func validateEvidence(e Evidence, revision int64, value, source string) error {
	if e.At.IsZero() {
		if e.Source != "" || e.Value != "" || e.Revision != 0 {
			return ErrInvalidState
		}
		return nil
	}
	if e.Source != source || e.Value != value || e.Revision != revision {
		return ErrInvalidState
	}
	return nil
}

// ValidateAttempt uses the same provider parser as live proof reconciliation.
// It never reparses and re-encodes Raw, which must survive storage byte for byte.
func ValidateAttempt(l Launch, a *Attempt) error {
	if a == nil || a.RequestID == "" || len(a.RequestID) > 4096 || a.Revision < 0 || a.Revision == math.MaxInt64 ||
		a.Identity != l.Identity || len(a.Payload) > 1<<20 || a.LastQueue != nil && *a.LastQueue < 0 {
		return ErrInvalidState
	}
	if err := ValidatePayload(a); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidState, err)
	}
	if a.Phase == "not_started" && (a.BytesAttempted != 0 || a.EnterAttempted) ||
		a.Phase == "attempt_started" && (a.BytesAttempted != 0 || a.EnterAttempted) ||
		a.Phase == "text_attempted" && (a.BytesAttempted == 0 || a.EnterAttempted) ||
		a.Phase == "enter_attempted" && !a.EnterAttempted ||
		a.Phase == "manual_input" && !a.ManualInterference ||
		a.Proof != nil && a.Phase != "submitted" {
		return ErrInvalidState
	}
	for p, m := range a.Watermark {
		if !filepath.IsAbs(p) || m.Size < 0 || !validHash(m.PrefixSHA256) ||
			(m.SessionID == "") != (m.MetadataSHA256 == "") || m.MetadataSHA256 != "" && !validHash(m.MetadataSHA256) {
			return ErrInvalidState
		}
	}
	if p := a.Proof; p != nil {
		m, known := a.Watermark[p.Path]
		if !known || !filepath.IsAbs(p.Path) || p.Offset < m.Size || p.SessionID == "" ||
			len(p.Raw) == 0 || a.NativeSessionID != "" && a.NativeSessionID != p.SessionID ||
			a.Identity.Agent == "claude" && (p.Path != l.Capability.Transcript || p.SessionID != l.Capability.SessionID) ||
			a.Identity.Agent == "codex" && (m.SessionID != p.SessionID || m.MetadataSHA256 == "") {
			return ErrInvalidState
		}
		recordID, _ := MatchRecord(p.Raw, a, p.SessionID)
		if p.RecordID != recordID {
			return ErrInvalidState
		}
	}
	source := "store"
	if a.Phase == "submitted" {
		source = "transcript"
	}
	return validateEvidence(a.Confirmation, a.Revision, a.Phase, source)
}

// ValidateTransition preserves immutable request ownership and monotonic
// irreversible intent. Only a confirmed refusal before all input can take a
// fresh snapshot; otherwise watermarks can only gain validated provenance.
func ValidateTransition(old, next *Attempt) error {
	if next == nil {
		return ErrInvalidState
	}
	if old == nil {
		if next.Revision != 0 || next.Phase != "attempt_started" && next.Phase != "manual_input" && next.Phase != "not_started" {
			return ErrStale
		}
		return nil
	}
	if old.Revision != next.Revision || old.RequestID != next.RequestID || old.Identity != next.Identity ||
		old.Payload != next.Payload || old.PayloadSHA256 != next.PayloadSHA256 ||
		next.BytesAttempted < old.BytesAttempted || old.EnterAttempted && !next.EnterAttempted ||
		old.ManualInterference && !next.ManualInterference ||
		old.NativeSessionID != "" && old.NativeSessionID != next.NativeSessionID ||
		old.Proof != nil && !reflect.DeepEqual(old.Proof, next.Proof) {
		return ErrStale
	}
	if old.Phase == "not_started" && next.Phase == "attempt_started" {
		// A new caller may retry this exact request only from confirmed zero
		// intent. Imported unknown facts and any manual interference remain
		// conservative refusals. The store also checks current ownership and
		// both revisions in the transaction that commits this restart.
		if old.Revision <= 0 || old.Confirmation.At.IsZero() ||
			validateEvidence(old.Confirmation, old.Revision, "not_started", "store") != nil ||
			old.BytesAttempted != 0 || old.EnterAttempted || old.ManualInterference || old.Proof != nil ||
			next.BytesAttempted != 0 || next.EnterAttempted || next.ManualInterference || next.Proof != nil || next.SnapshotAt.IsZero() {
			return ErrStale
		}
		return nil
	}
	if !old.SnapshotAt.Equal(next.SnapshotAt) {
		return ErrStale
	}
	for p, m := range old.Watermark {
		n, ok := next.Watermark[p]
		if !ok || n.Size != m.Size || n.PrefixSHA256 != m.PrefixSHA256 ||
			m.SessionID != "" && (m.SessionID != n.SessionID || m.MetadataSHA256 != n.MetadataSHA256) {
			return ErrStale
		}
	}
	if (old.Phase == "submitted" && next.Phase != "submitted") ||
		(old.EnterAttempted && next.Phase != "enter_attempted" && next.Phase != "submitted") ||
		(old.ManualInterference && (next.BytesAttempted != old.BytesAttempted || next.EnterAttempted != old.EnterAttempted)) ||
		(old.Phase == "not_started" && next.Phase != "not_started") ||
		(old.BytesAttempted > 0 && next.Phase != "text_attempted" && next.Phase != "enter_attempted" && next.Phase != "submitted") {
		return ErrStale
	}
	return nil
}

func validHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
