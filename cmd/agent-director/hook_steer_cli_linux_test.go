//go:build linux

package main_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite" // raw read of the prompts' transport and the Messages' Results

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The owned PostToolBatch steer through the built CLI, as a parent callback of
// the row's recorded pane process (this test process). Linux only: the hook
// reads its own identity from /proc, and the broken-pipe case shrinks its pipe
// with F_SETPIPE_SZ.

// TestHookCLISteerWritesOneEnvelope (SR-3.4, SR-3.2, SR-3.3a, SR-3.5; Subtask
// t3.xis.wm.hg.k1): with stdout a pipe the hook inherits blocking, as Claude
// Code gives it, a parent PostToolBatch of a working row exits 0 with nothing
// on stderr and exactly one envelope, then EOF, on stdout: hookSpecificOutput
// naming the event with the Steer Turn prompt of a confirmed and an
// unconfirmed Message as its additionalContext, and nothing else. The hook
// left that descriptor non-blocking, so its write ran under the route's write
// deadline. The tool-boundary steer is not race-exposed: the confirmed
// Message is Final handed_over_not_proven, the unconfirmed one
// confirmation_off, never wait_turn_raced. Nothing is typed.
func TestHookCLISteerWritesOneEnvelope(t *testing.T) {
	h := newGateHome(t)
	const id, session = "id-gate-steer", "8e2d4c6a-0b1f-4a3e-9c5d-7f6e5d4c3b2a"
	db := seedSteerRow(t, h, id, session)
	st := openDBForRead(t, h.home)
	steer, confirmed, unconfirmed := store.BusySendSteerTurn, true, false
	m1 := "one \"quoted\" \\ <b>&amp;</b>\tü 漢字 🙂\nline two"
	m2 := "two, unconfirmed"
	if _, err := st.AdmitBusySend(store.BusySendAdmissionRequest{InstanceID: id, Text: []byte(m1), SendMode: &steer, Confirmed: &confirmed,
		CallerUUID: "0f1e2d3c-4b5a-4968-8778-695a4b3c2d1e", DeliveryRequestID: "m1"}); err != nil {
		t.Fatalf("AdmitBusySend m1: %v", err)
	}
	if _, err := st.AdmitBusySend(store.BusySendAdmissionRequest{InstanceID: id, Text: []byte(m2), SendMode: &steer, Confirmed: &unconfirmed}); err != nil {
		t.Fatalf("AdmitBusySend m2: %v", err)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	fd := w.Fd() // Fd leaves the write end blocking; only the hook may change that.
	ctx, cancel := context.WithTimeout(context.Background(), gateHookDeadline)
	defer cancel()
	cmd := h.hookCmd(ctx, t, id, "", steerPayload(t, session))
	var stderr strings.Builder
	cmd.Stdout, cmd.Stderr = w, &stderr
	err = cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("hook still running after %s", gateHookDeadline)
	}
	flags, flagsErr := unix.FcntlInt(fd, unix.F_GETFL, 0)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, readErr := io.ReadAll(r)
	if err != nil || stderr.Len() != 0 || readErr != nil {
		t.Fatalf("hook = %v, stderr %q, stdout read %v; want exit 0 with nothing on stderr", err, stderr.String(), readErr)
	}

	var env struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.DisallowUnknownFields() // no decision, permission, stop control or other key
	want := "[agent-director: message 1 of 2]\n" + m1 + "\n\n[agent-director: message 2 of 2]\n" + m2
	if err := dec.Decode(&env); err != nil || string(out[dec.InputOffset():]) != "\n" ||
		env.HookSpecificOutput.HookEventName != "PostToolBatch" || env.HookSpecificOutput.AdditionalContext != want {
		t.Errorf("stdout = %q (%v); want one PostToolBatch envelope with additionalContext %q, a newline and EOF", out, err, want)
	}
	if flagsErr != nil || flags&unix.O_NONBLOCK == 0 {
		t.Errorf("the hook's stdout flags = %#x, %v; want O_NONBLOCK, so its write deadline applies", flags, flagsErr)
	}
	results := queryStrings(t, db, `SELECT result_class || '|' || result_final || '|' || COALESCE(result_reason, '')
	    FROM busy_send_messages WHERE instance_id = ? ORDER BY accepted_seq`, id)
	if wantResults := []string{"sent_unconfirmed|1|handed_over_not_proven", "sent_unconfirmed|1|confirmation_off"}; !slices.Equal(results, wantResults) {
		t.Errorf("Results = %q; want %q", results, wantResults)
	}
	assertNoTmuxCall(t, h.home)
}

// TestHookCLISteerBrokenPipe (SR-3.2, SR-2.5; Task t2.xis.wm.hg code review
// R1-1): a steer whose stdout reader closed its end before the write or
// during a write larger than the pipe holds exits 0 instead of dying by
// SIGPIPE, and its prompt's output is recorded as uncertain or partial. A
// later parent boundary writes nothing and issues no prompt: the ambiguity
// keeps its barrier and nothing is retried.
func TestHookCLISteerBrokenPipe(t *testing.T) {
	for _, tc := range []struct {
		name   string
		during bool
		want   string
	}{
		{name: "reader closed before the write", want: "uncertain"},
		{name: "reader closed during a larger write", during: true, want: "partial"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newGateHome(t)
			const id, session = "id-gate-steer", "5c4b3a29-1807-4f6e-9d5c-4b3a29180706"
			db := seedSteerRow(t, h, id, session)
			steer := store.BusySendSteerTurn
			text := bytes.Repeat([]byte("s"), 7_900)
			if _, err := openDBForRead(t, h.home).AdmitBusySend(store.BusySendAdmissionRequest{InstanceID: id, Text: text, SendMode: &steer}); err != nil {
				t.Fatalf("AdmitBusySend: %v", err)
			}
			payload := steerPayload(t, session)

			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			defer w.Close()
			if tc.during {
				// One page, so the envelope cannot fit before the reader closes.
				if size, err := unix.FcntlInt(w.Fd(), unix.F_SETPIPE_SZ, 4096); err != nil || size >= len(text) {
					t.Skipf("pipe size %d, %v: cannot hold the write below the envelope's size", size, err)
				}
			} else if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), gateHookDeadline)
			defer cancel()
			cmd := h.hookCmd(ctx, t, id, "", payload)
			var stderr strings.Builder
			cmd.Stdout, cmd.Stderr = w, &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			_ = w.Close()
			if tc.during {
				// The hook has written part of its envelope and waits for room.
				if _, err := io.ReadFull(r, make([]byte, 1024)); err != nil {
					t.Errorf("read the envelope's start: %v", err)
				}
				_ = r.Close()
			}
			err = cmd.Wait()
			if ctx.Err() != nil {
				t.Fatalf("hook still running after %s", gateHookDeadline)
			}
			if err != nil {
				t.Fatalf("hook = %v (%v); want exit 0 with its output recorded\nstderr=%s", err, cmd.ProcessState, stderr.String())
			}
			if got := steerTransports(t, db, id); !slices.Equal(got, []string{tc.want}) {
				t.Errorf("issued prompts' transport = %q; want [%s]", got, tc.want)
			}

			if out := h.hook(t, id, "", payload); out != "" {
				t.Errorf("a later parent boundary wrote %q; want nothing while the ambiguous prompt keeps its barrier", out)
			}
			if got := steerTransports(t, db, id); !slices.Equal(got, []string{tc.want}) {
				t.Errorf("after a later boundary, issued prompts' transport = %q; want [%s] and no retry", got, tc.want)
			}
			assertNoTmuxCall(t, h.home)
		})
	}
}

// seedSteerRow seeds id as a working Claude row of session whose pane process
// is this test process, covered on route A, and returns its store path.
func seedSteerRow(t *testing.T, h gateHome, id, session string) string {
	t.Helper()
	db := stateDB(h.home)
	if _, err := apitest.SeedSpawn(db, id, store.StateWorking, "", "off", session, true, withTestProcessPane(t)); err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	if err := apitest.SeedBusySendCoverage(db, id, store.BusySendCovered, store.BusySendCovered); err != nil {
		t.Fatal(err)
	}
	if err := apitest.SeedBusySendRoute(db, id, string(store.BusySendRouteClaudeHooks), filepath.Join(h.home, "busy-send-wake")); err != nil {
		t.Fatal(err)
	}
	return db
}

// steerPayload is a parent PostToolBatch of session (no agent_id).
func steerPayload(t *testing.T, session string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"hook_event_name": "PostToolBatch", "session_id": session,
		"transcript_path": "/x/" + session + ".jsonl", "cwd": "/x"})
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

// steerTransports reads the transport of every issued prompt of id's
// hand-overs ("" while none is recorded).
func steerTransports(t *testing.T, dbPath, id string) []string {
	t.Helper()
	return queryStrings(t, dbPath, `SELECT COALESCE(p.transport, '') FROM busy_send_prompts p
	    JOIN busy_send_handovers h ON h.handover_id = p.handover_id
	    WHERE h.instance_id = ? AND p.emission = 'issued' ORDER BY h.handover_seq, p.prompt_index`, id)
}

// queryStrings reads the one text column of every row query returns.
func queryStrings(t *testing.T, dbPath, query string, args ...any) []string {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
