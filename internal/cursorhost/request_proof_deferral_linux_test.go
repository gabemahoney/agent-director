//go:build linux

package cursorhost

import (
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// b.o89: a proof deferred by a competing writer is retried on the next tick
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
			if err := c.tick(); err != nil {
				t.Fatalf("tick under writer contention inside the busy timeout ended the controller: %v", err)
			}
			if got := e.request(t, r.token); got.ProvenGone() {
				t.Fatalf("proof stored while another writer held the lock: %+v", got)
			}
			release()
			if tc.mutation != "" {
				e.exec(t, tc.mutation)
			}

			err := c.tick()
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
		err = c.tick()
	}
	if elapsed := time.Since(start); !errors.Is(err, store.ErrStoreBusy) || elapsed < 50*time.Millisecond {
		t.Fatalf("tick = %v after %v; want ErrStoreBusy no sooner than the 50 ms busy timeout", err, elapsed)
	}
	if got := e.request(t, r.token); got.ProvenGone() {
		t.Fatalf("proof stored while another writer held the lock: %+v", got)
	}
}
