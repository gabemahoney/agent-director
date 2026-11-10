// Package inputdelivery implements the version-pinned ordinary composer
// transport. Queue observations pace writes; only native transcripts prove a turn.
package inputdelivery

import (
	"context"
	"errors"
	"fmt"
	"github.com/gabemahoney/agent-director/internal/inputjournal"
	"os"
	"path/filepath"
	"time"
)

// ChunkBytes forwards inputjournal.ChunkBytes, the most bytes one write carries.
const ChunkBytes = inputjournal.ChunkBytes

// Session is held exclusively across automatic and manual AD writers. A crash
// releases the OS lock but leaves the last durable attempt for observation only.
type Session interface {
	Close() error
	Load() (*Attempt, error)
	LoadRequest(string) (*Attempt, error)
	Save(*Attempt) error
	Ready(Identity) (bool, error)
	Queue(Identity) (int, error)
	Watermark(Identity) (map[string]Watermark, error)
	Confirm(*Attempt) (*Proof, error)
}

type Backend interface {
	Open(Identity) (Session, error)
}
type Native struct {
	Store   Persistence
	Context context.Context
	Guard   Guard
}

type nativeSession struct {
	dir      string
	lock     *os.File
	manifest manifest
	store    Persistence
	ctx      context.Context
	base     context.Context
	cancel   context.CancelFunc
	guard    Guard
	launch   *Launch
	failed   error
	admitted bool
}

func launchDir(storeID, token string) (string, error) {
	if !inputjournal.ValidID(storeID) || !inputjournal.ValidID(token) {
		return "", fmt.Errorf("launch identity is unavailable")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "agent-director", "input", storeID, token), nil
}

// OpenExisting reads authority from SQLite only. Logs and legacy JSON cannot
// establish presence or absence. Every writer takes the original launch lock.
func (n Native) OpenExisting(id Identity) (Session, error) { return n.Open(id) }

func (n Native) Open(id Identity) (Session, error) {
	if n.Store == nil {
		return nil, fmt.Errorf("native input persistence is required")
	}
	ctx := n.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	acquireCtx, acquireCancel := context.WithTimeout(ctx, 12*time.Second)
	defer acquireCancel()
	l, err := n.Store.ReadInputLaunch(acquireCtx, id.Token)
	if err != nil {
		return nil, err
	}
	dir, err := launchDir(id.StoreID, id.Token)
	if err != nil {
		return nil, err
	}
	if l != nil {
		if l.Blocker != "" {
			return nil, ErrInvalidState
		}
		dir = l.EvidenceDir
	}
	if err = os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return nil, err
	}
	path := dir + ".lock"
	if info, e := os.Lstat(path); e == nil && !info.Mode().IsRegular() || e != nil && !os.IsNotExist(e) {
		return nil, ErrInvalidState
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = inputjournal.LockWriter(f); err != nil {
		f.Close()
		return nil, err
	}
	s := &nativeSession{dir: dir, lock: f, store: n.Store, base: ctx, ctx: acquireCtx, guard: n.Guard}
	s.guard.Identity = id
	// Read again after admission. A waited lock never grants an older snapshot.
	s.launch, err = n.Store.ReadInputLaunch(acquireCtx, id.Token)
	if err == nil && s.launch != nil {
		if s.launch.Blocker != "" || s.launch.EvidenceDir != dir {
			err = ErrInvalidState
		} else {
			s.manifest = s.launch.Capability
		}
	}
	if err == nil {
		err = s.BindIdentity(id, false)
	}
	if err != nil {
		s.Close()
		return nil, err
	}
	s.ctx = ctx
	return s, nil
}

func (s *nativeSession) Close() error {
	if s.cancel != nil {
		s.cancel()
	}
	if s.lock == nil {
		return nil
	}
	err := s.lock.Close()
	s.lock = nil
	if err != nil {
		s.failed = err
	}
	return err
}

// LimitTo shortens a phase to its original absolute deadline. Switching to a
// later phase restores only the enclosing caller context, never a retry budget.
func (s *nativeSession) LimitTo(end time.Time) {
	if s.cancel != nil {
		s.cancel()
	}
	s.ctx, s.cancel = context.WithDeadline(s.base, end)
}

// Admit freezes the API's final, validated preparation snapshot exactly once.
// It never reloads the launch/current request or clears a failed operation.
func (s *nativeSession) Admit(g Guard, target Identity) error {
	if s.admitted || s.failed != nil || s.guard.ProofOnly {
		return ErrStale
	}
	s.admitted = true
	before, next := s.guard.Identity, g.Identity
	before.PaneID, before.PID, before.Starttime = "", 0, ""
	next.PaneID, next.PID, next.Starttime = "", 0, ""
	if before != next {
		return ErrStale
	}
	g.Manual, g.ProofOnly = s.guard.Manual, s.guard.ProofOnly
	s.guard = g
	if err := s.BindIdentity(target, false); err != nil {
		return err
	}
	return s.Check()
}

// BindIdentity may incorporate exactly this caller's successful adoption.
// Without a native launch, optional pane caching is independent of the stored
// row guard: the API separately verifies the discovered transport target.
func (s *nativeSession) BindIdentity(id Identity, adopted bool) error {
	if s.failed != nil {
		return s.failed
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	before := s.guard.Identity
	before.PaneID, before.PID, before.Starttime = "", 0, ""
	base := id
	base.PaneID, base.PID, base.Starttime = "", 0, ""
	if before != base {
		return ErrStale
	}
	if s.guard.Identity.PaneID != "" && s.guard.Identity != id {
		return ErrStale
	}
	if adopted {
		s.guard.RowVersion++
		s.guard.Identity = id
	}
	if s.launch != nil {
		if s.guard.Identity != id {
			return ErrStale
		}
		// Before targetPane there may be no recorded pane yet. Binding is deferred
		// until the caller has successfully recorded the exact found identity.
		if id.PaneID != "" {
			if s.guard.ProofOnly {
				// An empty launch has nothing to reconcile and must not acquire
				// a pane binding as a side effect of a read. Still validate the
				// captured row and launch, including the unbound base identity.
				if s.launch.Identity != id && (s.launch.CurrentRequestID != "" || s.launch.Identity != base) {
					return ErrStale
				}
				return s.Check()
			}
			if !s.admitted && s.launch.CurrentRequestID == "" {
				// An empty journal is still preparation. Defer its first binding
				// commit to Admit, retaining this exact native snapshot while the
				// API validates any same-launch report-in or liveness update.
				if s.launch.Identity != id && s.launch.Identity != base {
					return ErrStale
				}
				return nil
			}
			guard := s.guard
			guard.Manual = true // binding itself cannot authorize ordinary delivery
			l, err := s.store.BindInputLaunch(s.ctx, *s.launch, guard)
			if err != nil {
				s.failed = err
				return err
			}
			s.launch = l
		} else if s.launch.Identity != id {
			return ErrStale
		}
	}
	return nil
}

func (s *nativeSession) Manual() { s.guard.Manual = true }

func (s *nativeSession) LaunchEvidence() (int64, Evidence) {
	if s.launch == nil {
		return 0, Evidence{}
	}
	return s.launch.Revision, s.launch.Confirmation
}

func (s *nativeSession) Check() error {
	if s.failed != nil {
		return s.failed
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	guard := s.guard
	guard.Manual = true // read admission; ordinary issue also checks waiting state
	return s.store.CheckInputGuard(s.ctx, s.launch, guard)
}

// CheckIdentity rechecks only the captured launch record, launch and life,
// session, and process and pane identity, for an in-flight paced send whose
// admission (with its epoch, version and state guard) already ran before the
// first write. It sets ProofOnly on its own guard copy only because inputOwner
// returns after exactly those identity checks for that flag; it grants no
// proof-only save authority (it never reaches save), and s.guard is unchanged.
func (s *nativeSession) CheckIdentity() error {
	if s.failed != nil {
		return s.failed
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	guard := s.guard
	guard.Manual, guard.ProofOnly = true, true
	return s.store.CheckInputGuard(s.ctx, s.launch, guard)
}

func (s *nativeSession) Load() (*Attempt, error) {
	if s.failed != nil {
		return nil, s.failed
	}
	if s.launch == nil {
		if !s.admitted && !s.guard.ProofOnly {
			if err := s.ctx.Err(); err != nil {
				return nil, err
			}
			// Observe absence without authorizing the unadmitted row. Keep
			// persistence errors and reject new native authority; Admit still
			// checks this absence and the final row before any dispatch.
			current, err := s.store.ReadInputLaunch(s.ctx, s.guard.Identity.Token)
			if err != nil {
				return nil, err
			}
			if current != nil {
				return nil, ErrStale
			}
			return nil, nil
		}
		return nil, s.Check()
	}
	if s.launch.CurrentRequestID == "" {
		return nil, nil
	}
	return s.LoadRequest(s.launch.CurrentRequestID)
}
func (s *nativeSession) LoadRequest(id string) (*Attempt, error) {
	if s.failed != nil {
		return nil, s.failed
	}
	if s.launch == nil {
		return s.Load()
	}
	return s.store.ReadInputAttempt(s.ctx, *s.launch, id)
}

func (s *nativeSession) validateAttempt(a *Attempt) error { return inputjournal.ValidatePayload(a) }

func (s *nativeSession) Save(a *Attempt) error {
	if a == nil {
		return ErrInvalidState
	}
	return s.save(a, a.Phase == "submitted")
}

func (s *nativeSession) save(a *Attempt, proofOnly bool) error {
	s.admitted = true
	if s.failed != nil {
		return s.failed
	}
	if s.launch == nil {
		return ErrInvalidState
	}
	g := s.guard
	g.ProofOnly = g.ProofOnly || proofOnly
	g.Manual = !g.ProofOnly && a.ManualInterference
	l, stored, err := s.store.SaveInputAttempt(s.ctx, *s.launch, g, a)
	if err != nil {
		// An unknown or stale operation cannot regain write authority in this call.
		if errors.Is(err, ErrUnconfirmed) || errors.Is(err, ErrStale) || s.ctx.Err() != nil {
			s.failed = err
		}
		return err
	}
	s.launch = l
	*a = *stored
	return nil
}

// NewAttempt allocates a request using the canonical journal protocol.
func NewAttempt(id Identity, text string, watermark map[string]Watermark) (*Attempt, error) {
	return inputjournal.NewAttempt(id, text, watermark)
}

// Hash returns the SHA-256 of exact evidence bytes.
func Hash(data []byte) string { return inputjournal.Hash(data) }

// Chunks validates and splits ordinary input using the persisted proof contract.
func Chunks(text string) ([]string, error) { return inputjournal.Chunks(text) }

// Split forwards inputjournal.Split, the one splitter of ordinary text into writes.
func Split(text string) []string { return inputjournal.Split(text) }
