package hook_test

// The safety of subagent capture through hook.Handle (b.xjn; SRD SR-5.2, SR-5.3,
// SR-15.1, SR-18, N3, N6): no content field of a payload reaches the store, the
// trail or the hook log; a store failure of the entry write never fails the
// hook; a payload past the 1 MiB limit writes nothing; and every string of a
// record is cut to 1 KiB the same way whichever hook writes it.

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
)

// TestSubagentCaptureStoresNoContent: every subagent event with the sentinel in
// each content field and each never-read field leaves the sentinel in none of
// the store file (write-ahead log included), the trail and the hook log, while
// the records it does capture are written.
func TestSubagentCaptureStoresNoContent(t *testing.T) {
	t.Run("scanners find the sentinel", func(t *testing.T) {
		db := filepath.Join(t.TempDir(), "x.db")
		if err := os.WriteFile(db+"-wal", []byte("aa"+subSentinel+"bb"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(db, []byte("none"), 0o600); err != nil {
			t.Fatal(err)
		}
		if !storeContains(t, db, subSentinel) {
			t.Error("storeContains missed the sentinel in the write-ahead log")
		}
		if !trailContains(t, []map[string]any{{"event": "x", "note": "a " + subSentinel}}, subSentinel) {
			t.Error("trailContains missed the sentinel")
		}
	})

	r := newSubRow(t, subRowID)
	fixtures := []string{fxRequest, fxStart, fxResult, fxStop, fxStartTeammate, fxStopInternal, fxResultNested,
		fxResultTeammate, fxRequestTeammate, fxCompleted, fxMainStop}
	var payloads [][]byte
	for _, fx := range fixtures {
		payloads = append(payloads, sentinelPayload(t, fx))
	}
	payloads = append(payloads, sentinelPayload(t, fxMainStop, emptyListing()),
		variant(t, fxFailureBase, sentinelEdits(), setKey("session_id", subSessionID), setKey("tool_name", "Agent"),
			setKey("tool_use_id", subToolUseID), setKey("tool_input", fixtureValue(t, fxRequest, "tool_input"))))
	var logs strings.Builder
	for i, p := range payloads {
		logs.WriteString(r.fire(p, at(int64(i)+1)).Log)
	}
	if len(r.rows().Entries) == 0 {
		t.Fatal("no entry was written; the scan proves nothing")
	}
	assertNoSentinel(t, r, logs.String())
}

// assertNoSentinel fails when the sentinel is in the row's store file (write-ahead
// log included), the trail or the hook log text logs.
func assertNoSentinel(t *testing.T, r *subRow, logs string) {
	t.Helper()
	if storeContains(t, r.dbPath, subSentinel) {
		t.Error("the store file holds the sentinel")
	}
	if trailContains(t, readTrailLines(t, trailFile()), subSentinel) {
		t.Error("the trail holds the sentinel")
	}
	if strings.Contains(logs, subSentinel) {
		t.Errorf("the hook log holds the sentinel: %q", logs)
	}
}

// TestSubagentListingStoresNoDescription: a background_tasks whose every
// description (a subagent's and a teammate's) is the sentinel is applied (the
// Stop's snapshot made its record, the SubagentStop's listing confirmed and
// filled its entry), yet the sentinel reaches no table, trail line or log line.
func TestSubagentListingStoresNoDescription(t *testing.T) {
	t.Run("a Stop's snapshot", func(t *testing.T) {
		r := newSubRow(t, subRowID)
		f := r.fire(sentinelPayload(t, fxMainStop), at(1))
		assertEntries(t, r, listedRecord(subAgentID, subType, at(1)))
		assertNoSentinel(t, r, f.Log)
	})

	t.Run("a SubagentStop's listing", func(t *testing.T) {
		r := newSubRow(t, subRowID)
		r.fire(variant(t, fxStart), at(1))
		f := r.fire(sentinelPayload(t, fxStop), at(2))
		want := listedRecord(subAgentID, subType, at(1))
		want.first, want.started = at(1), at(1)
		assertEntries(t, r, want.stoppedAt(at(2)))
		assertNoSentinel(t, r, f.Log)
	})
}

// failRow is a row with one claimable launch (B9) and one entry (B1's, claimed
// from a second launch) written, then every write of its subagent rows made
// to fail.
func failRow(t *testing.T) *subRow {
	t.Helper()
	r := newSubRow(t, subRowID)
	r.fire(secondRequest(t), at(1))
	r.fire(variant(t, fxStart), at(2))
	r.fire(variant(t, fxRequest), at(3))
	assertEntries(t, r, running(subAgentID, subType, true, at(2)))
	assertLaunches(t, r, wantLaunch{subToolUseID, subType, at(3)})
	storefix.InjectWriteFailure(t, r.dbPath, storefix.WriteFailSubagentRecord, r.id)
	return r
}

// failSnapshotRow is a row whose Stop snapshot (B8) made a background record,
// then every write of its subagent rows made to fail.
func failSnapshotRow(t *testing.T) *subRow {
	t.Helper()
	r := newSubRow(t, subRowID)
	r.fire(variant(t, fxMainStop), at(1))
	assertEntries(t, r, listedRecord(subAgentID, subType, at(1)))
	storefix.InjectWriteFailure(t, r.dbPath, storefix.WriteFailSubagentRecord, r.id)
	return r
}

// TestSubagentCaptureFailsOpen: when the entry write fails (a Stop's snapshot
// included, whether it would create or close a record) the hook exits 0
// with nothing on stdout, writes no entry or launch, and logs one line naming
// the instance and event without any payload string. A SubagentStart or
// SubagentStop, whose soft refresh shares the entry's transaction, leaves the
// row untouched and reports upsert_outcome error; the other hooks keep their
// row write's outcome.
func TestSubagentCaptureFailsOpen(t *testing.T) {
	for _, tc := range []struct {
		name, event string
		payload     func(*testing.T) []byte
		lifecycle   bool
		atMs        int64
		row         func(*testing.T) *subRow // failRow when nil
	}{
		{"launch request", "PreToolUse", func(t *testing.T) []byte {
			return sentinelPayload(t, fxRequest, setKey("tool_use_id", subSecondToolUseID))
		}, false, at(10), nil},
		{"launch result", "PostToolUse", func(t *testing.T) []byte { return sentinelPayload(t, fxResult) }, false, at(10), nil},
		{"nested launch result", "PostToolUse", func(t *testing.T) []byte { return sentinelPayload(t, fxResultNested) }, false, at(10), nil},
		{"completed result", "PostToolUse", func(t *testing.T) []byte { return sentinelPayload(t, fxCompleted) }, false, at(10), nil},
		{"launch failure", "PostToolUseFailure", func(t *testing.T) []byte { return agentFailure(t, subToolUseID, sentinelEdits()) }, false, at(10), nil},
		{"main-agent Stop past the grace", "Stop", func(t *testing.T) []byte { return sentinelPayload(t, fxMainStop, emptyListing()) }, false, at(10), nil},
		{"main-agent Stop whose snapshot would create", "Stop", func(t *testing.T) []byte {
			return sentinelPayload(t, fxMainStop, tasks(taskEntry(subAgentID, subType, map[string]any{"description": subSentinel}),
				taskEntry(subChildID, subType, map[string]any{"description": subSentinel})))
		}, false, at(10), failSnapshotRow},
		{"main-agent Stop whose snapshot would close", "Stop", func(t *testing.T) []byte { return sentinelPayload(t, fxMainStop, emptyListing()) },
			false, at(10), failSnapshotRow},
		{"SubagentStart", "SubagentStart", func(t *testing.T) []byte { return sentinelPayload(t, fxStart, setKey("agent_id", subChildID)) }, true, at(10), nil},
		{"SubagentStop", "SubagentStop", func(t *testing.T) []byte { return sentinelPayload(t, fxStop) }, true, at(10), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			build := tc.row
			if build == nil {
				build = failRow
			}
			r := build(t)
			rowsBefore, spawnBefore := r.rows(), r.spawn()
			f := r.fire(tc.payload(t), tc.atMs)
			if f.LogLines != 1 {
				t.Errorf("log = %q; want exactly one line", f.Log)
			}
			for _, leak := range []string{subSentinel, subLaunchDesc, subToolUseID, subAgentID, subSecondToolUseID} {
				if strings.Contains(f.Log, leak) {
					t.Errorf("log %q holds payload string %q", f.Log, leak)
				}
			}
			if !strings.Contains(f.Log, "instance="+subRowID) || !strings.Contains(f.Log, "event="+tc.event) {
				t.Errorf("log = %q; want the instance and event named", f.Log)
			}
			if after := r.rows(); !reflect.DeepEqual(after, rowsBefore) {
				t.Errorf("entries and launches changed:\n got %+v\nwant %+v", after, rowsBefore)
			}
			if tc.lifecycle {
				if f.upsert() != "error" {
					t.Errorf("upsert_outcome = %v; want error", f.upsert())
				}
				if after := r.spawn(); after != spawnBefore {
					t.Errorf("row changed: %+v; want %+v", after, spawnBefore)
				}
				if got := f.transitions(); len(got) != 0 {
					t.Errorf("state transitions = %v; want none", got)
				}
				return
			}
			if f.upsert() == "error" {
				t.Errorf("upsert_outcome = error; want the row write's own outcome")
			}
			if after := r.spawn(); after.RowVersion == spawnBefore.RowVersion {
				t.Errorf("row_version unchanged; want the row write applied")
			}
		})
	}
}

// fireUnread runs payload as the row's own agent and returns the hook log,
// asserting nil, empty stdout and that its one trail line is an ad.hook.fired
// that names no instance, event or outcome: the hook could not read it.
func fireUnread(t *testing.T, r *subRow, payload []byte, atMs int64) string {
	t.Helper()
	hc := hookConfig(envHook(r.id, ""), agentParent(t, r.st, r.id))
	hc.Start = time.UnixMilli(atMs)
	var stdout, logBuf bytes.Buffer
	before := len(readTrailLines(t, trailFile()))
	if err := hook.Handle(context.Background(), bytes.NewReader(payload), &stdout, r.st, hc, log.New(&logBuf, "", 0)); err != nil {
		t.Fatalf("Handle: %v; want nil", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q; want empty", stdout.String())
	}
	lines := readTrailLines(t, trailFile())[before:]
	if len(lines) != 1 || lines[0]["event"] != "ad.hook.fired" || lines[0]["event_name"] != nil ||
		lines[0]["instance_id"] != nil || lines[0]["upsert_outcome"] != nil {
		t.Errorf("trail lines = %v; want one bare ad.hook.fired", lines)
	}
	return logBuf.String()
}

// TestSubagentCaptureOverLimit: a payload over 1 MiB writes nothing, so a
// foreground subagent whose SubagentStop and completed result are both over the
// limit stays a running record.
func TestSubagentCaptureOverLimit(t *testing.T) {
	r := newSubRow(t, subRowID)
	r.fire(variant(t, fxRequest), at(1))
	r.fire(variant(t, fxStart), at(2))
	running1 := running(subAgentID, subType, true, at(2))
	assertEntries(t, r, running1)
	for _, tc := range []struct {
		name    string
		payload []byte
	}{
		{"SubagentStop", oversized(t, fxStop)},
		{"completed result", oversized(t, fxCompleted, setKey("tool_response.agentId", subAgentID))},
		{"launch request", oversized(t, fxRequest, setKey("tool_use_id", subSecondToolUseID))},
		{"SubagentStart", oversized(t, fxStart, setKey("agent_id", subChildID))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spawnBefore := r.spawn()
			if got := fireUnread(t, r, tc.payload, at(5)); !strings.Contains(got, "read payload") {
				t.Errorf("log = %q; want a read payload line", got)
			}
			assertEntries(t, r, running1)
			assertLaunches(t, r)
			if after := r.spawn(); after != spawnBefore {
				t.Errorf("row changed: %+v; want %+v", after, spawnBefore)
			}
		})
	}
}

// TestSubagentCaptureStringBound: every string of a record is cut to the longest
// whole-UTF-8 prefix within 1024 bytes (SR-5.3), the same for each hook that
// writes it, so a request, a start and a result of one launch still make one
// record; a value of exactly 1024 bytes is stored unchanged.
func TestSubagentCaptureStringBound(t *testing.T) {
	cut := func(s string) string { return wantCut(longOf(s)) }
	long := func(t *testing.T, fx string, edits ...payloadEdit) []byte {
		return longVariant(t, fx, longOf, edits...)
	}
	full := func(r *subRow, ms int64) wantEntry {
		e := filled(r, cut(subAgentID), cut(subType), cut(subLaunchDesc), cut(subModel), cut(subToolUseID), ms)
		e.session = cut(subSessionID)
		return e
	}

	t.Run("cut values", func(t *testing.T) {
		if got := cut(subAgentID); len(got) != 1023 || got == longOf(subAgentID) {
			t.Fatalf("oracle: cut length %d; want 1023 (the character across 1024 dropped whole)", len(got))
		}
	})

	t.Run("request, start and result make one record in either order", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			order []string
		}{
			{"start before result", []string{fxRequest, fxStart, fxResult}},
			{"result before start", []string{fxRequest, fxResult, fxStart}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				r := newSubRow(t, subRowID)
				for i, fx := range tc.order {
					r.fire(long(t, fx), at(int64(i)+1))
				}
				if got := len(r.rows().Entries); got != 1 {
					t.Fatalf("entries = %d; want 1", got)
				}
				want := full(r, at(2)) // the start's, or the result's: both at step 2
				assertEntries(t, r, want)
				assertLaunches(t, r)
			})
		}
	})

	t.Run("a nested result stores the long launcher id cut", func(t *testing.T) {
		r := newSubRow(t, subRowID)
		r.fire(long(t, fxResultNested), at(1))
		child := filled(r, cut(subChildID), cut(subType), cut(subNestedDesc), cut(subModel), cut(subNestedToolUseID), at(1))
		child.parent, child.parentID, child.session = "subagent", cut(subAgentID), cut(subSessionID)
		assertEntries(t, r, child)
	})

	t.Run("a stop with a long id stops the cut record", func(t *testing.T) {
		r := newSubRow(t, subRowID)
		r.fire(long(t, fxStart), at(1))
		r.fire(long(t, fxStop, emptyListing()), at(2))
		stopped := running(cut(subAgentID), cut(subType), false, at(1)).stoppedAt(at(2))
		stopped.session = cut(subSessionID)
		assertEntries(t, r, stopped)
	})

	t.Run("exactly 1024 bytes is stored unchanged", func(t *testing.T) {
		r := newSubRow(t, subRowID)
		exact := exactOf(subAgentID)
		r.fire(longVariant(t, fxStart, exactOf), at(1))
		e := running(exact, exactOf(subType), false, at(1))
		e.session = exactOf(subSessionID)
		assertEntries(t, r, e)
		if got := r.rows().Entries[0].SubagentID; got != exact || len(exact) != 1024 {
			t.Errorf("subagent_id = %v (len %d); want the 1024-byte id unchanged", got, len(exact))
		}
	})
}

// TestSubagentListingStringBound: a listed id and type are cut to the longest
// whole-UTF-8 prefix within 1024 bytes like every other string of a record
// (SR-5.3), so a long listed id makes the cut record, matches the cut id of a
// launch result or a start, and keeps that record from closing.
func TestSubagentListingStringBound(t *testing.T) {
	cut := func(s string) string { return wantCut(longOf(s)) }
	long := func(t *testing.T, fx string) []byte { return longVariant(t, fx, longOf) }

	t.Run("a Stop's snapshot creates the cut record, B5 fills it and a later snapshot keeps it", func(t *testing.T) {
		r := newSubRow(t, subRowID)
		created := listedRecord(cut(subAgentID), cut(subType), at(1))
		created.session = cut(subSessionID)
		full := filled(r, cut(subAgentID), cut(subType), cut(subLaunchDesc), cut(subModel), cut(subToolUseID), at(2))
		full.opened, full.session = at(1), cut(subSessionID)
		r.script(t, []step{
			{name: "B8 creates one record", payload: long(t, fxMainStop), at: at(1), entries: []wantEntry{created}},
			{name: "B5 fills that record", payload: long(t, fxResult), at: at(2), entries: []wantEntry{full}},
			{name: "a later snapshot past the grace lists the long id", payload: long(t, fxMainStop), at: at(3), entries: []wantEntry{full}},
		})
	})

	t.Run("a SubagentStop's listing confirms the cut entry", func(t *testing.T) {
		r := newSubRow(t, subRowID)
		started := running(cut(subAgentID), cut(subType), false, at(1))
		started.session = cut(subSessionID)
		confirmed := started
		confirmed.confirmed, confirmed.runMode = true, "background"
		r.script(t, []step{
			{name: "B1 with a long id", payload: long(t, fxStart), at: at(1), entries: []wantEntry{started}},
			{name: "B3 whose listing carries it", payload: long(t, fxStop), at: at(2), entries: []wantEntry{confirmed.stoppedAt(at(2))}},
		})
	})
}
