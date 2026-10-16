//go:build linux

package cursorhost

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
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

func TestCursorAcknowledgedAnswerSurvivesFiniteStoreContention(t *testing.T) {
	// Serial: b.jf1 Task3 .01 regression uses the verified HOME/PATH fixture.
	for _, tc := range []struct{ decision, loss string }{
		{"allow", "none"}, {"deny", "none"}, {"allow", "expiry"}, {"deny", "expiry"}, {"allow", "cancel"}, {"deny", "cancel"}, {"allow", "runtime death"}, {"deny", "runtime death"},
		{"allow", "launch"}, {"deny", "launch"}, {"allow", "session"}, {"deny", "session"}, {"allow", "connection"}, {"deny", "connection"},
	} {
		t.Run(tc.decision+"/"+tc.loss, func(t *testing.T) {
			decision := tc.decision
			e := newRequestEnv(t, "on", "synthetic-session")
			e.rep.cfg.Relay.TimeoutSeconds = 20
			wire := &observedWire{}
			c := e.controller(t, wire, io.Discard)
			first := e.admit(t, c, `"first"`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "first", "execute"))
			second := e.admit(t, c, `"second"`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "second", "execute"))
			holdFor := 80 * time.Millisecond
			deadlineEstablished := time.Now()
			if tc.loss == "expiry" {
				first.timeout = time.UnixMilli(time.Now().Add(200 * time.Millisecond).UnixMilli())
				first.settle = first.timeout
				e.exec(t, `UPDATE permission_requests SET settled_at=? WHERE request_token=?`, first.settle.UnixMilli(), first.token)
				holdFor = 400 * time.Millisecond
			}
			releaseWriter := blockProtocol(t, c.protocol)
			e.decide(t, first.token, decision)
			if err := c.tick(); err != nil {
				t.Fatal(err)
			}
			ackComplete := time.Now()
			if e.request(t, first.token).DeliveredAt.IsZero() {
				t.Fatal("first answer lacks durable acknowledgement")
			}

			// Observe the actual product guard, without replacing its authority lookup.
			next := <-c.protocol.outgoing
			originalGuard, originalDone := next.guard, next.done
			guardErrors := make(chan error, 2)
			entered, releaseStore := make(chan struct{}), make(chan struct{})
			type guardObservation struct {
				entry, exit, deadline time.Time
				err                   error
				holderReleased        bool
			}
			guardEntered := make(chan time.Time, 1)
			guardReturned := make(chan guardObservation, 1)
			var guardCalls atomic.Int32
			finished := make(chan error, 1)
			next.guard = func() error {
				firstCall := guardCalls.Add(1) == 1
				observation := guardObservation{entry: time.Now(), deadline: next.deadline}
				if firstCall {
					guardEntered <- observation.entry
				}
				err := originalGuard()
				if firstCall {
					observation.exit, observation.err = time.Now(), err
					select {
					case <-releaseStore:
						observation.holderReleased = true
					default:
					}
					guardReturned <- observation
				}
				select {
				case guardErrors <- err:
				default:
				}
				return err
			}
			next.done = func(err error) { originalDone(err); finished <- err }
			c.protocol.outgoing <- next

			var once sync.Once
			unhold := func() { once.Do(func() { close(releaseStore) }) }
			t.Cleanup(unhold)
			holderDone := make(chan error, 1)
			go func() {
				mutation := map[string]string{
					"launch":     `UPDATE spawns SET launch_token='2222222222222222'`,
					"session":    `UPDATE spawns SET session_id='replacement'`,
					"connection": `UPDATE cursor_runtimes SET bound_connection_id='replacement'`,
				}[tc.loss]
				if mutation != "" {
					// The independent SQLite writer commits replacement only after
					// the real answer guard has begun waiting on BEGIN IMMEDIATE.
					db, err := sql.Open("sqlite", e.path)
					if err != nil {
						holderDone <- err
						return
					}
					defer db.Close()
					conn, err := db.Conn(context.Background())
					if err != nil {
						holderDone <- err
						return
					}
					defer conn.Close()
					if _, err = conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
						holderDone <- err
						return
					}
					defer conn.ExecContext(context.Background(), "ROLLBACK")
					close(entered)
					<-releaseStore
					if _, err = conn.ExecContext(context.Background(), mutation); err == nil {
						_, err = conn.ExecContext(context.Background(), "COMMIT")
					}
					holderDone <- err
					return
				}
				holderDone <- e.s.CheckCursorAnswer(e.binding, first.token, true, first.timeout, func(row store.Spawn) error {
					if err := c.check(row); err != nil {
						return err
					}
					close(entered)
					<-releaseStore
					return nil
				})
			}()
			select {
			case <-entered:
			case err := <-holderDone:
				t.Fatalf("real store holder failed: %v", err)
			case <-time.After(time.Second):
				t.Fatal("store holder did not enter")
			}
			holderEntered := time.Now()
			remaining := time.Until(next.deadline)
			started := time.Now()
			timer := time.AfterFunc(holdFor, unhold)
			if tc.loss == "cancel" {
				cancelTimer := time.AfterFunc(40*time.Millisecond, c.cancel)
				defer cancelTimer.Stop()
			}
			var runtimeGone chan error
			if tc.loss == "runtime death" {
				runtimeGone = make(chan error, 1)
				go func() { time.Sleep(40 * time.Millisecond); runtimeGone <- e.process.terminate() }()
			}
			defer timer.Stop()
			releaseWriter()
			if tc.loss == "expiry" {
				var guardEntry time.Time
				select {
				case guardEntry = <-guardEntered:
				case <-time.After(transportWriteWait + time.Second):
					t.Fatalf("expiry guard did not enter: established=%s ack=%s holder=%s deadline=%s", deadlineEstablished, ackComplete, holderEntered, next.deadline)
				}
				var observation guardObservation
				select {
				case observation = <-guardReturned:
				case <-time.After(transportWriteWait + time.Second):
					t.Fatalf("expiry guard did not return: established=%s ack=%s holder=%s guardEntry=%s deadline=%s", deadlineEstablished, ackComplete, holderEntered, guardEntry, next.deadline)
				}
				t.Logf("expiry established=%s ack=%s holder=%s guardEntry=%s deadline=%s guardExit=%s holderReleased=%v guardErr=%v", deadlineEstablished, ackComplete, holderEntered, observation.entry, observation.deadline, observation.exit, observation.holderReleased, observation.err)
				if !observation.entry.Before(observation.deadline) {
					t.Errorf("fixture precondition failed: expiry guard entered after fixed deadline: entry=%s deadline=%s", observation.entry, observation.deadline)
				}
				if observation.holderReleased || observation.entry.Before(holderEntered) {
					t.Error("expiry holder did not remain held throughout the first guard")
				}
				if observation.exit.Before(observation.deadline) || !errors.Is(observation.err, store.ErrStoreBusy) || !errors.Is(observation.err, context.DeadlineExceeded) {
					t.Errorf("expiry guard did not exhaust its fixed deadline under contention: entry=%s deadline=%s exit=%s err=%v", observation.entry, observation.deadline, observation.exit, observation.err)
				}
				unhold()
			}
			var writeErr error
			select {
			case writeErr = <-finished:
			case <-time.After(transportWriteWait + time.Second):
				t.Fatal("answer did not finish within existing frame budget")
			}
			elapsed := time.Since(started)
			frameComplete := time.Now()
			if runtimeGone != nil {
				if err := <-runtimeGone; err != nil {
					t.Fatalf("runtime retirement: %v", err)
				}
			}
			if err := <-holderDone; err != nil && tc.loss != "expiry" {
				t.Fatalf("holder lost real authority: %v", err)
			}
			unhold()
			firstBytes := wire.bytes()
			var guardErr error
			select {
			case guardErr = <-guardErrors:
			default:
				t.Fatal("actual guard was not called")
			}
			pr := e.request(t, first.token)
			t.Logf("first guard=%v storeBusy=%v completion=%v elapsed=%s finiteHold=%s remainingFrame=%s remainingApproval=%s durableAck=%v firstBytes=%q", guardErr, errors.Is(guardErr, store.ErrStoreBusy), writeErr, elapsed, holdFor, remaining, time.Until(first.timeout), !pr.DeliveredAt.IsZero(), firstBytes)
			if tc.loss == "expiry" {
				t.Logf("expiry frameComplete=%s", frameComplete)
			}
			if pr.DeliveredAt.IsZero() || pr.ProvenGone() || pr.Closed() {
				t.Fatal("contention changed durable ack/proof")
			}
			if tc.loss != "none" {
				if tc.loss != "expiry" && elapsed < 40*time.Millisecond {
					t.Errorf("%s refused before the bounded acquisition wait: %s", tc.loss, elapsed)
				}
				if writeErr == nil {
					t.Errorf("%s lost authority but writer succeeded", tc.loss)
				}
				if tc.loss != "expiry" && errors.Is(guardErr, store.ErrStoreBusy) {
					t.Errorf("%s passed through fail-fast contention instead of rechecking authority", tc.loss)
				}
				if len(firstBytes) != 0 {
					t.Errorf("%s during store contention emitted bytes: %s", tc.loss, firstBytes)
				}
				if err := c.tick(); err != nil {
					t.Fatal(err)
				}
				if len(wire.bytes()) != 0 {
					t.Fatal("retired response replayed")
				}
				return
			}
			if writeErr != nil || bytes.Count(firstBytes, []byte(`"id":"first"`)) != 1 {
				t.Errorf("valid acknowledged %s answer lost under finite same-Store contention: guard=%v completion=%v bytes=%q", decision, guardErr, writeErr, firstBytes)
			}
			e.decide(t, second.token, decision)
			if err := c.tick(); err != nil {
				t.Fatal(err)
			}
			waitRequestTerminalOrSent := time.After(time.Second)
			for !second.sent.Load() && !second.terminal.Load() {
				select {
				case <-waitRequestTerminalOrSent:
					t.Fatal("independent second answer did not finish")
				default:
					time.Sleep(time.Millisecond)
				}
			}
			if bytes.Count(wire.bytes(), []byte(`"id":"second"`)) != 1 {
				t.Fatalf("independent later answer lost: %s", wire.bytes())
			}
			t.Logf("later independently acknowledged response=%s", wire.bytes())
		})
	}
}

func TestProtocolGuardFailureOnlyInitialFailurePreservesLaterAnswer(t *testing.T) {
	// b.jf1 Task3 .01: a later answer distinguishes request refusal from failed transport.
	for _, option := range []string{"allow-once", "reject-once"} {
		for _, failAt := range []int32{1, 2} {
			t.Run(fmt.Sprintf("%s/guard%d", option, failAt), func(t *testing.T) {
				wire := &observedWire{}
				p := newProtocol(wire, bytes.NewReader(nil))
				defer p.close()
				entered, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				defer unblock()
				var calls atomic.Int32
				firstDone, laterDone := make(chan error, 1), make(chan error, 1)
				guard := func() error {
					if calls.Add(1) == failAt {
						close(entered)
						<-release
						return fmt.Errorf("independent authority refused")
					}
					return nil
				}
				if err := p.sendGuarded(map[string]string{"id": "first", "optionId": option}, time.Now().Add(time.Second), guard, func(err error) { firstDone <- err }); err != nil {
					t.Fatal(err)
				}
				select {
				case <-entered:
				case <-time.After(time.Second):
					t.Fatal("guard not reached")
				}
				if err := p.sendGuarded(map[string]string{"id": "later", "optionId": option}, time.Now().Add(time.Second), nil, func(err error) { laterDone <- err }); err != nil {
					t.Fatal(err)
				}
				unblock()
				select {
				case err := <-firstDone:
					if err == nil {
						t.Fatal("failed guard succeeded")
					}
				case <-time.After(time.Second):
					t.Fatal("first completion missing")
				}
				select {
				case err := <-laterDone:
					if (err == nil) != (failAt == 1) {
						t.Fatalf("later completion after guard%d: %v", failAt, err)
					}
				case <-time.After(time.Second):
					t.Fatal("later completion missing")
				}
				if bytes.Contains(wire.bytes(), []byte(`"first"`)) || bytes.Contains(wire.bytes(), []byte(`"later"`)) != (failAt == 1) {
					t.Fatalf("guard%d wire=%s", failAt, wire.bytes())
				}
			})
		}
	}
}
