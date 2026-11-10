//go:build linux

package cursorhost

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// b.o89: a proof deferred by a competing writer is retried by its own named wake
// once the writer releases. The retry stores a failed MCP completion with its
// usual semantics, and returns a non-busy failure at once instead of deferring
// it again.
func TestCursorDeferredProofRetriesAfterWriterReleases(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	cases := []struct {
		name     string
		params   func(session string) json.RawMessage
		output   string
		mutation string
	}{
		{"failed MCP completion stored", func(s string) json.RawMessage { return mcpPermission(s, "tool") }, `{"error":"Tool execution error"}`, ""},
		{"authority change fatal", func(s string) json.RawMessage { return cursorfix.Permission(s, "tool", "execute") }, `{"exitCode":0,"stdout":"done","stderr":""}`, `UPDATE cursor_runtimes SET bound_terminated=1`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newRequestEnv(t, "off", "synthetic-session")
			c := e.controller(t, &observedWire{}, io.Discard)
			r := e.admit(t, c, `1`, "session/request_permission", tc.params(e.binding.SessionID))
			before := e.request(t, r.token)

			release := apitest.HoldWriteLock(t, e.path, 5*time.Second)
			if err := c.execution(executionUpdate(e.binding.SessionID, "tool", "completed", tc.output)); err != nil {
				t.Fatalf("writer contention inside the busy timeout ended the controller: %v", err)
			}
			if err := continueProof(c, r); err != nil {
				t.Fatalf("proof wake under writer contention inside the busy timeout ended the controller: %v", err)
			}
			if got := e.request(t, r.token); got.ProvenGone() {
				t.Fatalf("proof stored while another writer held the lock: %+v", got)
			}
			release()
			if tc.mutation != "" {
				e.exec(t, tc.mutation)
			}

			err := continueProof(c, r)
			got := e.request(t, r.token)
			if tc.mutation == "" {
				if err != nil {
					t.Fatalf("deferred proof failed after the writer released: %v", err)
				}
				assertFailedMCPProof(t, before, got)
				return
			}
			if err == nil || errors.Is(err, store.ErrStoreBusy) {
				t.Fatalf("deferred proof under changed authority = %v; want a non-busy error at once", err)
			}
			if got.ProvenGone() {
				t.Fatalf("stale deferred proof committed: %+v", got)
			}
		})
	}
}

func TestCursorProofWakeCannotBorrowTimeAfterOriginalDeadline(t *testing.T) {
	// Serial: a real captured proof and a separate SQLite writer.
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprint(late), func(t *testing.T) {
			e := newRequestEnv(t, "off", "synthetic-session")
			c := e.controller(t, &observedWire{}, io.Discard)
			r := e.admit(t, c, `1`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "shell", "execute"))
			independent := e.admit(t, c, `2`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "other", "execute"))
			before := e.request(t, independent.token)
			release := apitest.HoldWriteLock(t, e.path, time.Minute)
			if err := c.execution(executionUpdate(e.binding.SessionID, "shell", "completed", flushShellOutput)); err != nil {
				t.Fatal(err)
			}
			// Move only this captured phase boundary; no global clock/wake can discover jobs.
			r.proofWait.stop()
			r.proof.event.BusyUntil = time.Now().Add(time.Second)
			if late {
				r.proof.event.BusyUntil = time.Now().Add(-time.Nanosecond)
			}
			wake := proofWakeFor(r)
			id := r.proof.event.ID
			release()
			progressed, err := c.continueRequest(wake)
			if late {
				if !errors.Is(err, store.ErrStoreBusy) || progressed {
					t.Fatalf("expired ordinary retry: progressed=%v err=%v", progressed, err)
				}
				if _, err := e.s.ReadCursorProof(e.binding.Connection, id); !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("late wake persisted proof: %v", err)
				}
				if e.request(t, r.token).ProvenGone() {
					t.Fatal("late wake mutated permission")
				}
			} else if err != nil || !progressed || !e.request(t, r.token).ProvenGone() {
				t.Fatalf("live finite retry did not progress: %v %v", progressed, err)
			}
			if e.request(t, independent.token) != before {
				t.Fatal("wake discovered independent request")
			}
		})
	}
}

// b.o89: contention that outlasts the configured store busy timeout ends the
// controller with ErrStoreBusy, no sooner than that timeout.
func TestCursorDeferredProofBusyBeyondStoreTimeoutIsFatal(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	e := newRequestEnv(t, "off", "synthetic-session")
	e.rep.cfg.Store.BusyTimeoutMs = 50
	c := e.controller(t, &observedWire{}, io.Discard)
	r := e.admit(t, c, `1`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "shell", "execute"))

	release := apitest.HoldWriteLock(t, e.path, 5*time.Second)
	defer release()
	start := time.Now()
	if err := c.execution(executionUpdate(e.binding.SessionID, "shell", "completed", `{"exitCode":0,"stdout":"done","stderr":""}`)); err != nil {
		t.Fatalf("contention within the busy timeout ended the controller: %v", err)
	}
	var err error
	for err == nil {
		if time.Since(start) > 3*time.Second {
			t.Fatal("contention beyond the store busy timeout never ended the controller")
		}
		err = continueProof(c, r)
	}
	if elapsed := time.Since(start); !errors.Is(err, store.ErrStoreBusy) || elapsed < 50*time.Millisecond {
		t.Fatalf("tick = %v after %v; want ErrStoreBusy no sooner than the 50 ms busy timeout", err, elapsed)
	}
	if got := e.request(t, r.token); got.ProvenGone() {
		t.Fatalf("proof stored while another writer held the lock: %+v", got)
	}
}

func TestCursorProofWakesRetainExactBytesAndIndependentIDs(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	e := newRequestEnv(t, "off", "synthetic-session")
	wire := &observedWire{}
	c := e.controller(t, wire, io.Discard)
	r := e.admit(t, c, `1`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "shell", "execute"))
	other := e.admit(t, c, `2`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "other", "execute"))
	before := e.request(t, other.token)
	raw := append([]byte(" \n"), executionUpdate(e.binding.SessionID, "shell", "completed", `{"exitCode":0, "stdout":"x\u0020", "stderr":"", "optional":null}`)...)
	release := apitest.HoldWriteLock(t, e.path, time.Minute)
	if err := c.execution(raw); err != nil {
		t.Fatal(err)
	}
	first := *r.proof
	wake := proofWakeFor(r)
	if err := c.execution(append([]byte(nil), raw...)); err != nil {
		t.Fatalf("exact pending duplicate: %v", err)
	}
	if err := c.execution(append([]byte("\t"), raw...)); err == nil {
		t.Fatal("conflicting unresolved event replaced proof")
	}
	if r.proof.event.ID != first.event.ID || !r.proof.event.BusyUntil.Equal(first.event.BusyUntil) || !bytes.Equal(r.proof.event.Raw, raw) {
		t.Fatal("repeat/conflict changed captured identity or budget")
	}
	for _, bad := range []string{"store", "token", "owner", "generation", "sequence", "deadline", "proof"} {
		w := wake
		switch bad {
		case "store":
			w.storeID += "x"
		case "token":
			w.token = other.token
		case "owner":
			w.binding.Connection += "x"
		case "generation":
			w.generation++
		case "sequence":
			w.sequence++
		case "deadline":
			w.deadline = w.deadline.Add(time.Second)
		case "proof":
			w.proofID += "x"
		}
		if progressed, err := c.continueRequest(w); err != nil || progressed {
			t.Fatalf("%s wake: %v %v", bad, progressed, err)
		}
	}
	release()
	if progressed, err := c.continueRequest(wake); err != nil || !progressed {
		t.Fatalf("own admitted proof did not complete: %v %v", progressed, err)
	}
	stored, err := e.s.ReadCursorProof(e.binding.Connection, first.event.ID)
	if err != nil || !bytes.Equal(stored.Raw, raw) || stored.Generation != first.event.Generation || stored.Owner != first.event.Owner || !stored.BusyUntil.Equal(first.event.BusyUntil) {
		t.Fatalf("stored snapshot: %+v %v", stored, err)
	}
	if progressed, err := c.continueRequest(wake); err != nil || progressed {
		t.Fatalf("duplicate completed wake: %v %v", progressed, err)
	}
	// A later distinct completion receives its own proof ID, even on the same request.
	release = apitest.HoldWriteLock(t, e.path, time.Minute)
	secondRaw := append([]byte("\t"), raw...)
	if err := c.execution(secondRaw); err != nil {
		t.Fatal(err)
	}
	second := *r.proof
	if second.event.ID == first.event.ID {
		t.Fatal("distinct completion reused proof identity")
	}
	if progressed, err := c.continueRequest(wake); err != nil || progressed || r.proof.event.ID != second.event.ID {
		t.Fatal("old wake disposed newer proof")
	}
	release()
	if err := continueProof(c, r); err != nil {
		t.Fatal(err)
	}
	if got, err := e.s.ReadCursorProof(e.binding.Connection, second.event.ID); err != nil || !bytes.Equal(got.Raw, secondRaw) {
		t.Fatalf("second exact source: %+v %v", got, err)
	}
	if e.request(t, other.token) != before || len(wire.bytes()) != 0 {
		t.Fatal("proof discovered another job or authorized response")
	}
}

// Review R1: successful independent pane evidence arriving after the original
// proof deadline cannot release a ledger mutation. Expired flushes retain their
// single immediate attempt; an already-issued COMMIT is tested in store cases.
func TestCursorProofDeadlineFencesLateAuthority(t *testing.T) {
	// Serial: verified fake tmux delays one real authority query, with a live peer.
	for _, kind := range []string{"ordinary", "flush", "expired-flush", "live"} {
		t.Run(kind, func(t *testing.T) {
			e := newRequestEnv(t, "off", "synthetic-session")
			e.rep.cfg.Tmux.PipeCloseWaitMs = 2000
			c := e.controller(t, &observedWire{}, io.Discard)
			r := e.admit(t, c, `1`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "shell", "execute"))
			release := apitest.HoldWriteLock(t, e.path, time.Minute)
			if err := c.execution(executionUpdate(e.binding.SessionID, "shell", "completed", flushShellOutput)); err != nil {
				t.Fatal(err)
			}
			r.proofWait.stop()
			release()
			row, err := e.s.GetSpawn(e.binding.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			tables := faketmuxfix.Tables{}
			tables.Inject(t, row.Identity.Socket, faketmuxfix.HoldPipes(tmux.CallListPanes, 400*time.Millisecond).FirstN(1))
			until := time.Now().Add(200 * time.Millisecond)
			if kind == "live" || kind == "expired-flush" {
				until = time.Now().Add(3 * time.Second)
			}
			r.proof.event.BusyUntil = until
			id := r.proof.event.ID
			if kind == "ordinary" || kind == "live" {
				err = continueProof(c, r)
			} else {
				flush := c.flushProofs(proofFlushEvent{kind: proofExit, epoch: 1})
				eventUntil := until
				if kind == "expired-flush" {
					eventUntil = time.Now().Add(-time.Second)
				}
				err = flush.write(eventUntil)
				flush.settle()
			}
			// The fake consumes this exact real pane lookup, so a pre-entry expiry
			// cannot masquerade as the post-authority boundary under test.
			table := tables.Read(t, row.Identity.Socket)
			if len(table.Injections) != 0 {
				t.Fatalf("late-authority query was not reached: %+v", table.Injections)
			}
			got, readErr := e.s.ReadCursorProof(e.binding.Connection, id)
			if kind == "live" || kind == "expired-flush" {
				if err != nil || readErr != nil || got.ID != id || r.proof != nil {
					t.Fatalf("positive authority: %+v %v %v", got, err, readErr)
				}
			} else {
				if !errors.Is(err, store.ErrStoreBusy) || !errors.Is(readErr, sql.ErrNoRows) || r.proof == nil || e.request(t, r.token).ProvenGone() {
					t.Fatalf("late authority started proof SQL: %+v %v %v", got, err, readErr)
				}
			}
		})
	}
}
