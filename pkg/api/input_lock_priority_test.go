//go:build linux || darwin

package api_test

// b.vxo: real proof-reader/input-writer exclusion, without timing races or
// sleeps. SQLite, flock, API guards and recorded transport remain real.
import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/inputdelivery"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"golang.org/x/sys/unix"
)

type heldInputProofStore struct {
	*apiProofPersistence
	lockPath string
	once     sync.Once
	entered  chan struct{}
	release  chan struct{}
}

func (s *heldInputProofStore) ReadInputAttempt(ctx context.Context, l inputdelivery.Launch, id string) (*inputdelivery.Attempt, error) {
	a, err := s.Persistence.ReadInputAttempt(ctx, l, id)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.lockPath, os.O_RDWR, 0600)
	if os.IsNotExist(err) {
		return a, nil
	} // optimistic read before admission
	if err != nil {
		return nil, err
	}
	defer f.Close()
	err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return a, nil
	}
	if !errors.Is(err, unix.EWOULDBLOCK) {
		return nil, err
	}
	s.once.Do(func() { close(s.entered); <-s.release })
	// The injected cancellation ends this pass after its real locked Load.
	return nil, context.Canceled
}

func inputLockFile(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func inputWaitPolicy(e *nativeSubmissionEnv) *inputdelivery.LockPolicy {
	return &inputdelivery.LockPolicy{GateTimeout: 10 * time.Millisecond, ProofTimeout: 20 * time.Millisecond, Interval: time.Millisecond,
		Now: e.clock.Now, Wait: func(ctx context.Context, d time.Duration) error { e.clock.Advance(d); return ctx.Err() }}
}

func TestVXOInputWriterWaitsForProofRead(t *testing.T) {
	t.Parallel()
	for _, verb := range []string{"send-keys", "pause"} {
		t.Run(verb, func(t *testing.T) {
			e, r, l := sqliteSubmission(t, sqliteCodexSpec(store.StateWaiting, false))
			seedSQLiteOwner(t, e, r, l, "original uncertain input")
			s := &heldInputProofStore{apiProofPersistence: &apiProofPersistence{Persistence: e.st, id: e.storeID},
				lockPath: l.EvidenceDir + ".lock", entered: make(chan struct{}), release: make(chan struct{})}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(s.release) }) }
			defer release()
			done := make(chan error, 1)
			go func() { _, err := api.ReconcileInputProof(context.Background(), s, r.Spawn); done <- err }()
			select {
			case <-s.entered:
			case err := <-done:
				t.Fatalf("read never reached locked Load: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("read admission barrier timed out")
			}
			readFinished := false
			defer func() {
				release()
				if !readFinished {
					<-done
				}
			}()
			policy := inputWaitPolicy(e)
			waits := 0
			policy.Wait = func(ctx context.Context, d time.Duration) error {
				waits++
				gate, err := os.OpenFile(l.EvidenceDir+".intent.lock", os.O_RDWR, 0600)
				if err != nil {
					return err
				}
				err = unix.Flock(int(gate.Fd()), unix.LOCK_SH|unix.LOCK_NB)
				_ = gate.Close()
				if !errors.Is(err, unix.EWOULDBLOCK) {
					return fmt.Errorf("writer waited without exclusive intent: %v", err)
				}
				release()
				err = <-done
				readFinished = true
				if !errors.Is(err, context.Canceled) {
					return fmt.Errorf("proof read did not close: %v", err)
				}
				e.clock.Advance(d)
				return ctx.Err()
			}
			e.env.Input.(*sqliteDeliveryBackend).locks = policy
			if verb == "pause" {
				e.rec.AfterCall(tmux.CallSendKey, func(c tmuxfix.SocketCall, err error) {
					if err == nil && c.Key == "Enter" {
						storefix.ApplyAgentHook(t, e.st, r.ID, "SessionEnd", r.Spawn.SessionID, storefix.HookAgent(store.AgentCodex))
						e.pc.Set(r.AgentPID, procfix.Gone())
					}
				})
			}
			e.rec.Reset()
			var err error
			if verb == "pause" {
				_, err = api.Pause(context.Background(), e.st, e.rec, api.PauseEnv{Input: e.env.Input, Relay: e.env.Relay}, 1, pauseParams(r))
			} else {
				_, err = e.send(api.SendKeysParams{InstanceID: r.ID, Text: "manual prefix", NoEnter: true})
			}
			if err != nil {
				t.Fatalf("%s failed behind a proof read: %v", verb, err)
			}
			if waits != 1 {
				t.Fatalf("wait callbacks=%d; want one causal read-drain wait", waits)
			}
			texts := e.rec.SocketCallsOf(tmux.CallSendText)
			want := "manual prefix"
			if verb == "pause" {
				want = "/exit"
			}
			if len(texts) != 1 || texts[0].Text != want {
				t.Fatalf("text writes=%+v; want exactly one %q", texts, want)
			}
			wantEnters := 0
			if verb == "pause" {
				wantEnters = 1
			}
			if got := len(ordinaryEnters(e.killEnv)); got != wantEnters {
				t.Fatalf("Enter calls=%d; want %d", got, wantEnters)
			}
		})
	}
}

func TestVXOIdleProofLeavesInputLocksAbsent(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"no launch", "empty launch", "no Enter", "submitted"} {
		t.Run(mode, func(t *testing.T) {
			var e *nativeSubmissionEnv
			var r killRow
			var l inputdelivery.Launch
			if mode == "no launch" {
				e = newNativeSubmissionEnv(t)
				r = e.seedRow(t, killRowSpec{State: store.StateWaiting})
				home, err := os.UserHomeDir()
				if err != nil {
					t.Fatal(err)
				}
				l.EvidenceDir = filepath.Join(home, ".config", "agent-director", "input", e.storeID, r.Token)
			} else {
				e, r, l = sqliteSubmission(t, killRowSpec{State: store.StatePending})
			}
			if mode == "no Enter" {
				g := sqliteGuard(r.Spawn, e.storeID)
				current, err := e.st.ReadInputLaunch(context.Background(), r.Token)
				if err != nil {
					t.Fatal(err)
				}
				bound, err := e.st.BindInputLaunch(context.Background(), *current, g)
				if err != nil {
					t.Fatal(err)
				}
				a, err := inputdelivery.NewAttempt(g.Identity, "not yet typed", nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err = e.st.SaveInputAttempt(context.Background(), *bound, g, a); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "submitted" {
				bound, a := seedSQLiteOwner(t, e, r, l, "already proved")
				raw := sqliteProofRecord(t, l, a.Payload)
				a.Phase = "submitted"
				a.Proof = &inputdelivery.Proof{Path: l.Capability.Transcript, SessionID: r.Spawn.SessionID, RecordID: "cde93519-6972-4b1b-a650-830a0042e876", Raw: raw, RawSHA256: inputdelivery.Hash(raw)}
				g := sqliteGuard(r.Spawn, e.storeID)
				g.ProofOnly = true
				if _, _, err := e.st.SaveInputAttempt(context.Background(), *bound, g, a); err != nil {
					t.Fatal(err)
				}
			}
			got, err := api.ReconcileInputProof(context.Background(), e.st, r.Spawn)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "submitted" && (got.Attempt == nil || got.Attempt.Phase != "submitted") {
				t.Fatalf("lost submitted proof: %+v", got)
			}
			for _, suffix := range []string{".lock", ".intent.lock"} {
				if _, err := os.Stat(l.EvidenceDir + suffix); !os.IsNotExist(err) {
					t.Errorf("idle %s touched %s: %v", mode, suffix, err)
				}
			}
		})
	}
}

func TestVXOProofDefersBeforeInputLock(t *testing.T) {
	t.Parallel()
	e, r, l := sqliteSubmission(t, sqliteCodexSpec(store.StateWaiting, false))
	seedSQLiteOwner(t, e, r, l, "unresolved")
	gate := inputLockFile(t, l.EvidenceDir+".intent.lock")
	if err := unix.Flock(int(gate.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	_, err := api.ReconcileInputProof(context.Background(), e.st, r.Spawn)
	if err == nil {
		t.Error("writer-held gate did not defer proof")
	}
	if result, reason := api.CheckOutcomeForTest(err, false); result != "busy" || reason != store.ReasonBusy {
		t.Errorf("deferred proof outcome=%s/%s; want busy", result, reason)
	}
	if _, err := os.Stat(l.EvidenceDir + ".lock"); !os.IsNotExist(err) {
		t.Errorf("deferred read touched input lock before checking intent: %v", err)
	}
}

func TestVXOInputWriterRefusalKeepsCause(t *testing.T) {
	t.Parallel()
	for _, verb := range []string{"send-keys", "pause"} {
		for _, holder := range []string{"writer", "proof reader"} {
			t.Run(verb+"/"+holder, func(t *testing.T) {
				e, r, _ := sqliteSubmission(t, sqliteCodexSpec(store.StateWaiting, false))
				guard := sqliteGuard(r.Spawn, e.storeID)
				guard.ProofOnly = holder == "proof reader"
				held, err := (inputdelivery.Native{Store: e.st, Guard: guard}).Open(sqliteInputIdentity(r.Spawn, e.storeID))
				if err != nil {
					t.Fatal(err)
				}
				defer held.Close()
				e.env.Input.(*sqliteDeliveryBackend).locks = inputWaitPolicy(e)
				e.rec.Reset()
				if verb == "pause" {
					_, err = api.Pause(context.Background(), e.st, e.rec, api.PauseEnv{Input: e.env.Input, Relay: e.env.Relay}, 0, pauseParams(r))
				} else {
					_, err = e.send(api.SendKeysParams{InstanceID: r.ID, Text: "nothing", NoEnter: true})
				}
				want := "ErrSubmissionUnconfirmed"
				if verb == "pause" {
					want = "ErrInternal"
				}
				assertOneName(t, err, want)
				cause := "another input writer is active or input admission stayed busy"
				if holder == "proof reader" {
					cause = "input lock stayed busy for 20ms"
				}
				if !strings.Contains(err.Error(), cause) {
					t.Errorf("lost %s cause: %v", holder, err)
				}
				d, ok := api.ErrDetails(err).(api.SubmissionUnconfirmedDetails)
				if !ok || d.Outcome != "not_started" || d.KeysSent || d.EnterMayHaveBeenSent || d.BytesAttempted != 0 || d.Phase != "pre_input" || !strings.Contains(d.Reason, cause) {
					t.Errorf("lost not-started cause facts: %+v", api.ErrDetails(err))
				}
				e.assertNoTmuxCall(t)
				_, a := sqliteAttempt(t, e, r)
				if !reflect.DeepEqual(a, (*inputdelivery.Attempt)(nil)) {
					t.Fatalf("refused call created attempt: %+v", a)
				}
			})
		}
	}
}

type inputOpenFailure struct{ err error }

func (b inputOpenFailure) Open(api.InputDeliveryIdentity) (api.InputDeliverySession, error) {
	return nil, b.err
}

func TestVXOInputOpenFailureKeepsOneNameAndCause(t *testing.T) {
	t.Parallel()
	const parentPath = "/private/vxo-input/home/.config/agent-director/input/store"
	const gatePath = parentPath + "/token.intent.lock"
	const lockPath = parentPath + "/token.lock"
	const cleanupPath = parentPath + "/other-token.intent.lock"
	cases := []struct {
		name  string
		cause error
		want  []string
	}{
		{"plain failure", errors.New("permission denied opening input journal"), []string{"permission denied opening input journal"}},
		{"store busy", store.ErrStoreBusy, []string{store.ErrStoreBusy.Error()}},
		{"parent directory", fmt.Errorf("prepare input parent directory: %w", &os.PathError{Op: "mkdir", Path: parentPath, Err: os.ErrPermission}), []string{"prepare input parent directory: mkdir: " + os.ErrPermission.Error()}},
		{"gate", fmt.Errorf("input admission: %w", fmt.Errorf("open input admission gate: %w", &os.PathError{Op: "open", Path: gatePath, Err: os.ErrPermission})), []string{"input admission: open input admission gate: open: " + os.ErrPermission.Error()}},
		{"input lock and cleanup", fmt.Errorf("input open failed: %w", errors.Join(
			fmt.Errorf("open input writer lock: %w", &os.PathError{Op: "open", Path: lockPath, Err: os.ErrPermission}),
			fmt.Errorf("cleanup: %w", &os.PathError{Op: "close", Path: cleanupPath, Err: os.ErrClosed}),
		)), []string{"input open failed: open input writer lock: open: " + os.ErrPermission.Error(), "cleanup: close: " + os.ErrClosed.Error()}},
	}
	for _, verb := range []string{"send-keys", "pause"} {
		for _, tc := range cases {
			t.Run(verb+"/"+tc.name, func(t *testing.T) {
				e := newNativeSubmissionEnv(t)
				r := e.seedRow(t, codexSpec(store.StateWaiting, agentAlive, false))
				e.env.Input = inputOpenFailure{err: tc.cause}
				var err error
				if verb == "pause" {
					_, err = api.Pause(context.Background(), e.store, e.rec, api.PauseEnv{Input: e.env.Input, Relay: e.env.Relay}, 0, pauseParams(r))
				} else {
					_, err = e.send(api.SendKeysParams{InstanceID: r.ID, Text: "nothing", NoEnter: true})
				}
				want := "ErrSubmissionUnconfirmed"
				if verb == "pause" {
					want = "ErrInternal"
				}
				assertOneName(t, err, want)
				d, ok := api.ErrDetails(err).(api.SubmissionUnconfirmedDetails)
				if !ok || d.Outcome != "not_started" || d.Phase != "pre_input" || d.RequestID != "" || d.BytesAttempted != 0 || d.KeysSent || d.EnterMayHaveBeenSent || d.SubmissionConfirmed || d.LastQueue != nil || d.RowState != store.StateWaiting {
					t.Errorf("open error lost its cause or facts: %v %+v", err, api.ErrDetails(err))
				}
				for _, text := range []string{err.Error(), d.Reason} {
					for _, want := range tc.want {
						if !strings.Contains(text, want) {
							t.Errorf("open error lost operation or cause %q: %q", want, text)
						}
					}
					for _, path := range []string{parentPath, gatePath, lockPath, cleanupPath} {
						if strings.Contains(text, path) {
							t.Errorf("open error exposes internal path %q: %q", path, text)
						}
					}
				}
				e.assertNoTmuxCall(t)
			})
		}
	}
}

// b.vxo: cancellation and an expired drain allowance release this caller's
// admission gate and must never dispatch input after the wait.
func TestVXOInputAdmissionEndsBeforeDispatch(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"cancel gate", "cancel proof", "proof expires as lock releases"} {
		t.Run(mode, func(t *testing.T) {
			e, r, _ := sqliteSubmission(t, sqliteCodexSpec(store.StateWaiting, false))
			g := sqliteGuard(r.Spawn, e.storeID)
			g.ProofOnly = mode != "cancel gate"
			held, err := (inputdelivery.Native{Store: e.st, Guard: g}).Open(sqliteInputIdentity(r.Spawn, e.storeID))
			if err != nil {
				t.Fatal(err)
			}
			defer held.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			policy := inputWaitPolicy(e)
			policy.ProofTimeout = time.Millisecond
			policy.Wait = func(_ context.Context, d time.Duration) error {
				if mode == "proof expires as lock releases" {
					e.clock.Advance(d)
					return held.Close()
				}
				cancel()
				return ctx.Err()
			}
			backend := e.env.Input.(*sqliteDeliveryBackend)
			backend.ctx = ctx
			backend.locks = policy
			_, err = e.send(api.SendKeysParams{InstanceID: r.ID, Text: "nothing", NoEnter: true})
			d := assertDeliveryError(t, err, "not_started", 0, false)
			cause := "context canceled"
			if mode == "proof expires as lock releases" {
				cause = "input lock stayed busy for 1ms"
			}
			if !strings.Contains(d.Reason, cause) {
				t.Errorf("wrong deadline/cancellation cause: %+v", d)
			}
			e.assertNoTmuxCall(t)
			if err := held.Close(); err != nil {
				t.Fatal(err)
			}
			next, err := (inputdelivery.Native{Store: e.st, Guard: sqliteGuard(r.Spawn, e.storeID)}).Open(sqliteInputIdentity(r.Spawn, e.storeID))
			if err != nil {
				t.Fatalf("cancelled/expired caller leaked input admission: %v", err)
			}
			if err := next.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
