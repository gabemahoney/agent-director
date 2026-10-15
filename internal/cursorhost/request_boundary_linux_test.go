//go:build linux

package cursorhost

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func waitRequestTerminal(t *testing.T, r *pendingAuthorization) {
	t.Helper()
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) {
		if r.terminal.Load() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("response did not retire")
}

func TestCursorAckCommittedBeforeFirstByte(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	for _, decision := range []string{"allow", "deny", "timeout"} {
		t.Run(decision, func(t *testing.T) {
			e := newRequestEnv(t, "on", "synthetic-session")
			wire := &observedWire{}
			c := e.controller(t, wire, io.Discard)
			r := e.admit(t, c, `"wire"`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "shell", "execute"))
			wire.first = func() error {
				// A separate connection cannot see an uncommitted acknowledgement.
				db, err := sql.Open("sqlite", e.path)
				if err != nil {
					return err
				}
				defer db.Close()
				var ack sql.NullInt64
				var verdict, proof sql.NullString
				if err = db.QueryRow(`SELECT delivered_at,decision,proven_gone_how FROM permission_requests WHERE request_token=?`, r.token).Scan(&ack, &verdict, &proof); err != nil {
					return err
				}
				want := decision
				if want == "timeout" {
					want = "deny"
				}
				if !ack.Valid || verdict.String != want || proof.Valid {
					return fmt.Errorf("first byte without committed own ack or with invented proof: %v %v %v", ack, verdict, proof)
				}
				return nil
			}
			if decision == "timeout" {
				r.timeout = time.Now().Add(-time.Millisecond)
			} else {
				e.decide(t, r.token, decision)
			}
			if !e.request(t, r.token).DeliveredAt.IsZero() {
				t.Fatal("decision became acknowledgement")
			}
			if err := c.tick(); err != nil {
				t.Fatal(err)
			}
			if err := completedWrite(t, c.protocol); err != nil {
				t.Fatalf("first-byte boundary: %v (%v)", err, wire.err)
			}
			want := "reject-once"
			if decision == "allow" {
				want = "allow-once"
			}
			if !bytes.Contains(wire.bytes(), []byte(`"optionId":"`+want+`"`)) {
				t.Fatalf("wrong response %s", wire.bytes())
			}
			pr := e.request(t, r.token)
			if pr.ProvenGone() || pr.Closed() {
				t.Fatal("ack claimed execution/closure")
			}
		})
	}
}

// blockProtocol holds the real serialized writer ahead of a request response.
func blockProtocol(t *testing.T, p *protocol) func() {
	t.Helper()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	if err := p.sendGuarded(map[string]any{"blocked": true}, time.Now().Add(time.Second), func() error { close(entered); <-release; return fmt.Errorf("synthetic queue barrier") }, func(error) { close(done) }); err != nil {
		t.Fatal(err)
	}
	<-entered
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }); <-done }
	t.Cleanup(unblock)
	return unblock
}

func TestCursorQueuedAnswersLoseAuthority(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	for _, decision := range []string{"allow", "deny"} {
		for _, loss := range []string{"deadline", "settlement retirement", "cancel", "runtime death", "connection", "session", "launch", "deleted ack", "deleted row", "closed"} {
			t.Run(decision+"/"+loss, func(t *testing.T) {
				e := newRequestEnv(t, "on", "synthetic-session")
				wire := &observedWire{}
				c := e.controller(t, wire, io.Discard)
				r := e.admit(t, c, `9`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "shell", "execute"))
				hold := e.admit(t, c, `10`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "independent", "execute"))

				if loss == "deadline" {
					r.timeout = time.UnixMilli(time.Now().Add(150 * time.Millisecond).UnixMilli())
					r.settle = r.timeout.Add(150 * time.Millisecond)
					e.exec(t, `UPDATE permission_requests SET settled_at=? WHERE request_token=?`, r.settle.UnixMilli(), r.token)
				}
				release := blockProtocol(t, c.protocol)
				e.decide(t, r.token, decision)
				if err := c.tick(); err != nil {
					t.Fatal(err)
				}
				if e.request(t, r.token).DeliveredAt.IsZero() {
					t.Fatal("queued response has no ack")
				}
				switch loss {
				case "deadline":
					until := r.settle
					if decision == "allow" {
						until = r.timeout
					}
					time.Sleep(time.Until(until) + 10*time.Millisecond)
				case "settlement retirement":
					r.timeout = time.Now().Add(-time.Millisecond)
					r.settle = time.Now().Add(-time.Millisecond)
					if err := c.tick(); err != nil {
						t.Fatal(err)
					}
				case "cancel":
					c.cancel()
				case "runtime death":
					if err := e.process.terminate(); err != nil {
						t.Fatal(err)
					}
				case "connection":
					e.exec(t, `UPDATE cursor_runtimes SET bound_connection_id='replacement'`)
				case "session":
					e.exec(t, `UPDATE spawns SET session_id='replacement'`)
				case "launch":
					e.exec(t, `UPDATE spawns SET launch_token='2222222222222222'`)
				case "deleted ack":
					e.exec(t, `UPDATE permission_requests SET delivered_at=NULL WHERE request_token=?`, r.token)
				case "deleted row":
					e.exec(t, `DELETE FROM permission_requests WHERE request_token=?`, r.token)
				case "closed":
					e.exec(t, `UPDATE permission_requests SET closed_at=CURRENT_TIMESTAMP WHERE request_token=?`, r.token)
				}
				release()
				waitRequestTerminal(t, r)
				if len(wire.bytes()) != 0 {
					t.Fatalf("retired %s emitted bytes: %s", decision, wire.bytes())
				}
				if got := e.request(t, hold.token); got.ProvenGone() || got.Closed() || got.Decision != "" {
					t.Fatal("independent hold lost")
				}
				c.choose(r.token, "allow-always")
				c.tick()
				if len(wire.bytes()) != 0 {
					t.Fatal("retired answer replayed as native")
				}
			})
		}
	}
}

func TestCursorMissingOrFailedAckEmitsNothing(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	for _, decision := range []string{"allow", "deny", "timeout"} {
		for _, failure := range []string{"failed mutation", "missing request", "stale connection"} {
			t.Run(decision+"/"+failure, func(t *testing.T) {
				e := newRequestEnv(t, "on", "synthetic-session")
				wire := &observedWire{}
				c := e.controller(t, wire, io.Discard)
				r := e.admit(t, c, `1`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "shell", "execute"))
				if decision == "timeout" {
					r.timeout = time.Now().Add(-time.Millisecond)
				} else {
					e.decide(t, r.token, decision)
				}
				switch failure {
				case "failed mutation":
					e.exec(t, `CREATE TRIGGER refuse_ack BEFORE UPDATE OF delivered_at ON permission_requests BEGIN SELECT RAISE(ABORT,'synthetic ack failure'); END`)
				case "missing request":
					e.exec(t, `DELETE FROM permission_requests WHERE request_token=?`, r.token)
				case "stale connection":
					e.exec(t, `UPDATE cursor_runtimes SET bound_connection_id='replacement'`)
				}
				c.tick()
				c.tick()
				if len(wire.bytes()) != 0 || r.queued {
					t.Fatal("ack failure emitted or queued a relay response")
				}
				if failure != "missing request" && !e.request(t, r.token).DeliveredAt.IsZero() {
					t.Fatal("failed ack committed")
				}
			})
		}
	}
}

func TestCursorAckDeadlineAfterLockAndEvidence(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	for _, delay := range []string{"write lock", "evidence"} {
		for _, deadline := range []string{"allow deadline", "confirm_by"} {
			t.Run(delay+"/"+deadline, func(t *testing.T) {
				e := newRequestEnv(t, "on", "synthetic-session")
				c := e.controller(t, &observedWire{}, io.Discard)
				r := e.admit(t, c, `1`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "shell", "execute"))
				e.decide(t, r.token, "allow")
				until, allowUntil := r.settle, r.timeout
				cutoff := time.Now().Add(60 * time.Millisecond)
				if deadline == "allow deadline" {
					allowUntil = cutoff
				} else {
					until = cutoff
					e.exec(t, `UPDATE permission_requests SET settled_at=? WHERE request_token=?`, cutoff.UnixMilli(), r.token)
					until = time.UnixMilli(cutoff.UnixMilli())
				}
				check := c.check
				if delay == "write lock" {
					defer apitest.HoldWriteLock(t, e.path, 120*time.Millisecond)()
				} else {
					check = func(row store.Spawn) error { time.Sleep(120 * time.Millisecond); return c.check(row) }
				}
				_, acked, err := e.s.AckCursorDecision(e.binding, r.token, false, until, allowUntil, time.Second, check)
				if acked || !e.request(t, r.token).DeliveredAt.IsZero() {
					t.Fatalf("late ack after %s/%s: %v %v", delay, deadline, acked, err)
				}
			})
		}
	}
}

func TestCursorPartialAndAckOnlyNeverReplay(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	for _, cut := range []string{"partial output", "after ack before byte"} {
		t.Run(cut, func(t *testing.T) {
			e := newRequestEnv(t, "on", "synthetic-session")
			wire := &observedWire{partial: cut == "partial output"}
			c := e.controller(t, wire, io.Discard)
			r := e.admit(t, c, `1`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "shell", "execute"))
			e.decide(t, r.token, "allow")
			if cut == "after ack before byte" {
				_, ok, err := e.s.AckCursorDecision(e.binding, r.token, false, r.settle, r.timeout, 0, c.check)
				if err != nil || !ok {
					t.Fatalf("ack %v %v", ok, err)
				}
				// Reconstruct controller state after an acknowledgement without transport receipt.
				if err = c.tick(); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := c.tick(); err != nil {
					t.Fatal(err)
				}
				if err := completedWrite(t, c.protocol); err == nil {
					t.Fatal("partial output reported success")
				}
			}
			before := len(wire.bytes())
			c.choose(r.token, "allow-once")
			c.tick()
			c.tick()
			pr := e.request(t, r.token)
			if len(wire.bytes()) != before || pr.DeliveredAt.IsZero() || pr.ProvenGone() || pr.Closed() {
				t.Fatalf("ack/partial replay or false proof: %+v", pr)
			}
		})
	}
}

func TestProtocolGuardDelayCannotWriteExpiredResponse(t *testing.T) {
	wrote := make(chan struct{}, 1)
	wire := &observedWire{first: func() error { wrote <- struct{}{}; return nil }}
	p := newProtocol(wire, bytes.NewReader(nil))
	defer p.close()
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	done := make(chan error, 1)
	guard := func() error {
		if calls.Add(1) == 2 {
			close(entered)
			<-release
		}
		return nil
	}
	if err := p.sendGuarded(map[string]bool{"allow": true}, time.Now().Add(40*time.Millisecond), guard, func(err error) { done <- err }); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := <-done; err == nil {
		t.Fatal("transport deadline did not retire slow final evidence check")
	}
	close(release)
	// Observe the writer after the final guard resumes, including a late write after timeout completion.
	select {
	case <-wrote:
		t.Fatal("final guard resumed into an expired pipe write")
	case <-time.After(80 * time.Millisecond):
	}
	if len(wire.bytes()) != 0 {
		t.Fatalf("expired response bytes: %s", wire.bytes())
	}
}

func TestCursorFailedAdmissionHasZeroRelayBytes(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	for _, fault := range []string{"insert failure", "ownership changed", "store closed"} {
		t.Run(fault, func(t *testing.T) {
			e := newRequestEnv(t, "on", "synthetic-session")
			wire := &observedWire{}
			c := e.controller(t, wire, io.Discard)
			switch fault {
			case "insert failure":
				e.exec(t, `CREATE TRIGGER refuse_request BEFORE INSERT ON permission_requests BEGIN SELECT RAISE(ABORT,'synthetic admission failure'); END`)
			case "ownership changed":
				e.exec(t, `UPDATE spawns SET pane_pid=pane_pid+1`)
			case "store closed":
				e.s.Close()
			}
			err := c.admit(frame{ID: []byte(`1`), Method: "session/request_permission", Params: cursorfix.Permission(e.binding.SessionID, "shell", "execute")}, e.binding.SessionID)
			if err == nil || len(c.requests) != 0 || len(wire.bytes()) != 0 {
				t.Fatalf("failed admission answered: %v %s", err, wire.bytes())
			}
		})
	}
}
