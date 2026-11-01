package api_test

// confirmations_test.go (b.x9o): the process_liveness value and the three
// main-field records the checked reads return. A read's own probe of the
// recorded agent process at its recorded start identity is its process
// evidence; a terminal move records the exit it proved in its own statement,
// and no sample taken before a concurrent terminal move survives it. An
// unsuccessful attempt describes only the value, interval and process record
// it examined.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/inputdelivery"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// confAt is t as a record time.
func confAt(t time.Time) string { return t.UTC().Format(displayTS) }

// conf renders a record as status|confirmed_at|source|checked_at|reason, "-"
// for null.
func conf(c api.Confirmation) string {
	s := func(p *string) string {
		if p == nil {
			return "-"
		}
		return *p
	}
	return strings.Join([]string{c.Status, s(c.ConfirmedAt), s(c.Source), s(c.CheckedAt), s(c.Reason)}, "|")
}

// confirmed and unknown are conf's form of those records.
func confirmed(at time.Time, src string) string {
	return "confirmed|" + confAt(at) + "|" + src + "|" + confAt(at) + "|-"
}

func unknownRecord(reason string) string { return "unknown|-|-|-|" + reason }

// mainRead is one verb's state, display, process_liveness and records for a row.
type mainRead struct {
	verb, state, display, process string
	records                       api.Confirmations
}

// readMain reads id through status, get and list over s with view (each a
// checked read when view has a process reader) and returns the three. An
// optional settle clock selects the Client's identical settled projections.
// Tests in this package must reuse this helper for status/get/list confirmation assertions.
func readMain(t *testing.T, s *store.Store, view api.RelayView, id string, settle ...store.SettleRead) []mainRead {
	t.Helper()
	status, get, listRows := api.Status, api.Get, api.List
	if len(settle) > 0 {
		status = func(s api.StatusStore, v api.RelayView, id string) (api.StatusResult, error) {
			return api.StatusSettledForTest(s, v, settle[0], id)
		}
		get = func(s api.GetStore, v api.RelayView, id string) (api.SpawnRow, error) {
			return api.GetSettledForTest(s, v, settle[0], id)
		}
		listRows = func(s api.ListStore, v api.RelayView, p api.ListParams) (api.ListResult, error) {
			return api.ListSettledForTest(s, v, settle[0], p)
		}
	}
	st, err := status(s, view, id)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	got, err := get(s, view, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	reads := []mainRead{{"status", st.State, st.DisplayedState, st.ProcessLiveness, st.Confirmations}, {"get", got.State, got.DisplayedState, got.ProcessLiveness, got.Confirmations}}
	list, err := listRows(s, view, api.ListParams{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, row := range list.Spawns {
		if row.InstanceID == id {
			reads = append(reads, mainRead{"list", row.State, row.DisplayedState, row.ProcessLiveness, row.Confirmations})
		}
	}
	if len(reads) != 3 {
		t.Fatalf("list returned no row %s", id)
	}
	return reads
}

// assertProcess fails unless every read reports state and process_liveness
// value with record want.
func assertProcess(t *testing.T, reads []mainRead, state, value, want string) {
	t.Helper()
	for _, r := range reads {
		if r.state != state || r.process != value || conf(r.records.ProcessLiveness) != want {
			t.Errorf("%s: state %s, process_liveness %s %s; want %s, %s %s", r.verb, r.state, r.process, conf(r.records.ProcessLiveness), state, value, want)
		}
	}
}

// storedProcess is id's process_evidence column as stored.
func storedProcess(t *testing.T, s *store.Store, id string) string {
	t.Helper()
	sp, err := s.GetSpawn(id)
	if err != nil {
		t.Fatal(err)
	}
	return sp.ProcessEvidence
}

// TestProcessLivenessOnCheckedReads: a checked read judges the recorded agent
// process at its recorded start identity and commits what it found, at the
// judgement's relay time, without touching the state record; a gone process
// moves the row with its exited record. A start identity that cannot be read,
// a pid-only identity and the pending-launch protections are unconfirmed or
// pending, never alive or exited, and a Cursor row's runtime is never probed.
func TestProcessLivenessOnCheckedReads(t *testing.T) {
	t.Parallel()
	unsupported := unknownRecord(store.ReasonUnsupported)
	for _, tc := range []struct {
		name   string
		spec   func(t *testing.T, e *killEnv) killRowSpec
		after  func(t *testing.T, e *killEnv, r killRow) // before the checked read
		state  string
		value  string
		record func(now time.Time) string
		stored bool // the process record is persisted
		// moved: the first read's mark finished the row, so only that read
		// carries its response-only attempt; later reads check nothing.
		moved bool
		// owned: the row holds an applicable state record (its own hook), so
		// "the probe never redates the state" is not vacuous.
		owned bool
	}{
		{name: "alive", spec: func(*testing.T, *killEnv) killRowSpec { return killRowSpec{State: store.StateWorking} },
			after: func(t *testing.T, e *killEnv, r killRow) {
				if got := storefix.ApplyAgentHook(t, e.st, r.ID, "PreToolUse", ""); !got.Applied {
					t.Fatalf("PreToolUse = %+v", got)
				}
			},
			state: store.StateWorking, value: store.ProcessAlive, record: func(now time.Time) string { return confirmed(now, "process") }, stored: true, owned: true},
		{name: "no recorded process", spec: func(*testing.T, *killEnv) killRowSpec {
			return killRowSpec{State: store.StateWorking, Agent: agentNotRecorded}
		},
			state: store.StateWorking, value: store.ProcessUnknown, record: func(now time.Time) string { return "unknown|-|-|" + confAt(now) + "|no_evidence" }},
		{name: "gone", spec: func(*testing.T, *killEnv) killRowSpec {
			return killRowSpec{State: store.StateWorking, Agent: agentGone}
		},
			state: store.StateMissing, value: store.ProcessExited, record: func(now time.Time) string { return confirmed(now, "process") }, stored: true},
		{name: "Codex gone with an end report", spec: func(*testing.T, *killEnv) killRowSpec { return codexSpec(store.StateWorking, agentGone, true) },
			state: store.StateEnded, value: store.ProcessExited, record: func(now time.Time) string { return confirmed(now, "process") }, stored: true},
		{name: "pid reused under another start", spec: func(*testing.T, *killEnv) killRowSpec { return killRowSpec{State: store.StateWaiting} },
			after: func(t *testing.T, e *killEnv, r killRow) { e.pc.Set(r.AgentPID, procfix.Alive("4242")) },
			state: store.StateMissing, value: store.ProcessExited, record: func(now time.Time) string { return confirmed(now, "process") }, stored: true},
		{name: "pid only, alive", spec: func(*testing.T, *killEnv) killRowSpec {
			return killRowSpec{State: store.StateWorking, Agent: agentNotRecorded, Opts: []apitest.SpawnOption{apitest.WithPID(9101)}}
		},
			after: func(t *testing.T, e *killEnv, r killRow) { e.pc.Set(9101, procfix.Alive("")) },
			state: store.StateWorking, value: store.ProcessUnknown, record: func(now time.Time) string { return "not_confirmed|-|-|" + confAt(now) + "|unreadable" }},
		{name: "pid only, gone", spec: func(*testing.T, *killEnv) killRowSpec {
			return killRowSpec{State: store.StateWorking, Agent: agentNotRecorded, Opts: []apitest.SpawnOption{apitest.WithPID(9102)}}
		},
			after: func(t *testing.T, e *killEnv, r killRow) { e.pc.Set(9102, procfix.Gone()) },
			state: store.StateMissing, value: store.ProcessUnknown, record: func(now time.Time) string { return "not_confirmed|-|-|" + confAt(now) + "|unreadable" },
			moved: true},
		{name: "pending grace", spec: func(t *testing.T, e *killEnv) killRowSpec { return e.pendingSpec(pendingOurs) },
			state: store.StatePending, value: store.ProcessUnknown, record: func(now time.Time) string { return "pending|-|-|" + confAt(now) + "|pending_grace" }},
		{name: "launch owner alive past the grace", spec: func(t *testing.T, e *killEnv) killRowSpec {
			spec := e.pendingSpec(pendingOurs, apitest.WithLaunchStartedAt(e.clock.Now().Add(-time.Hour).UnixMilli()))
			spec.Opts = append(spec.Opts, apitest.WithLaunchOwner(store.LaunchOwner{PID: 9103, Starttime: apitest.LinuxProcStarttime, PIDNamespace: selfPIDNamespace(t)}))
			return spec
		},
			after: func(t *testing.T, e *killEnv, r killRow) { e.pc.Set(9103, procfix.Alive(apitest.LinuxProcStarttime)) },
			state: store.StatePending, value: store.ProcessUnknown, record: func(now time.Time) string { return "pending|-|-|" + confAt(now) + "|operation_pending" }},
		{name: "Cursor, pane pid with no start", spec: func(*testing.T, *killEnv) killRowSpec {
			li := store.LaunchIdentity{Token: newToken(), Socket: apitest.TestSocket, PaneID: apitest.TestPaneID, PanePID: apitest.TestPanePID}
			return killRowSpec{State: store.StateWorking, Opts: []apitest.SpawnOption{apitest.WithAgent(store.AgentCursor),
				apitest.WithLaunchIdentity(li), apitest.WithNoPID()}}
		},
			state: store.StateWorking, value: store.ProcessUnknown, record: func(time.Time) string { return unsupported }},
		{name: "Cursor, pid and start recorded", spec: func(*testing.T, *killEnv) killRowSpec {
			return killRowSpec{State: store.StateWorking, Opts: []apitest.SpawnOption{apitest.WithAgent(store.AgentCursor)}}
		},
			state: store.StateWorking, value: store.ProcessUnknown, record: func(time.Time) string { return unsupported }},
		{name: "Cursor, a stored alive record", spec: func(*testing.T, *killEnv) killRowSpec {
			return killRowSpec{State: store.StateWorking, Opts: []apitest.SpawnOption{apitest.WithAgent(store.AgentCursor)}}
		},
			after: func(t *testing.T, e *killEnv, r killRow) {
				cursorReadSQL(t, e.dbPath, `UPDATE spawns SET process_evidence = json_object('v','alive','life',life_number,'token',launch_token,
				    'pid',pane_pid,'start',pane_starttime,'at',1,'src','process','checked',1,'reason',NULL),
                    display_evidence=json_object('v','none','life',life_number,'token',launch_token,
                    'at',1,'src','pane','checked',1,'reason',NULL) WHERE instance_id = ?`, r.ID)
			},
			state: store.StateWorking, value: store.ProcessUnknown, record: func(time.Time) string { return unsupported }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedRow(t, tc.spec(t, e))
			if tc.after != nil {
				tc.after(t, e, r)
			}
			before, err := api.Status(e.st, api.RelayView{}, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.owned && before.Confirmations.State.Status != store.ConfirmationConfirmed {
				t.Fatalf("fixture: state record %s; want an owned confirmation, or the redate check is vacuous", conf(before.Confirmations.State))
			}
			beforeProcess := storedProcess(t, e.st, r.ID)
			now := e.clock.Now()
			view := api.RelayView{Procs: e.pc, Now: func() time.Time { return now }}
			reads := readMain(t, e.st, view, r.ID)
			if tc.moved {
				assertProcess(t, reads[1:], tc.state, store.ProcessUnknown, unknownRecord(store.ReasonNoEvidence))
				reads = reads[:1]
			}
			assertProcess(t, reads, tc.state, tc.value, tc.record(now))
			if tc.state == before.State {
				// A live process never confirms or redates the state.
				for _, read := range reads {
					if conf(read.records.State) != conf(before.Confirmations.State) {
						t.Errorf("%s state record %s; want it as stored, %s", read.verb, conf(read.records.State), conf(before.Confirmations.State))
					}
				}
			}
			stored := storedProcess(t, e.st, r.ID)
			switch {
			case tc.stored && stored == "":
				t.Error("the probe's record was not persisted")
			case !tc.stored && stored != beforeProcess:
				t.Errorf("process_evidence %q -> %q; want nothing written", beforeProcess, stored)
			}
			// A later read with no check reports what was persisted.
			after := readMain(t, e.st, api.RelayView{}, r.ID)
			switch {
			case tc.stored:
				assertProcess(t, after, tc.state, tc.value, tc.record(now))
			case tc.record(now) == unsupported:
				assertProcess(t, after, tc.state, tc.value, unsupported)
				for _, read := range append(reads, after...) {
					if conf(read.records.DisplayedState) != unsupported {
						t.Errorf("Cursor display record %s; want unsupported despite stored evidence", conf(read.records.DisplayedState))
					}
				}
			default:
				assertProcess(t, after, tc.state, store.ProcessUnknown, unknownRecord(store.ReasonNoEvidence))
			}
		})
	}
}

// noted is a liveness note a checked read's alive probe clears.
var noted = []apitest.SpawnOption{apitest.WithLivenessUnverifiedSince("2026-01-01 00:00:00"), apitest.WithLivenessNote("probe_eacces")}

// TestProcessRecordKeepsTheLastValueAcrossAnUnreadableCheck: a later check
// that cannot read the start identity persists its time and reason over the
// older value and time. Success before and after that failure, including
// find-missing, keeps the first confirmation and advances only checked_at.
func TestProcessRecordKeepsTheLastValueAcrossAnUnreadableCheck(t *testing.T) {
	t.Parallel()
	for _, agent := range []string{store.AgentClaude, store.AgentCodex} {
		t.Run(agent, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			spec := killRowSpec{State: store.StateWorking}
			if agent == store.AgentCodex {
				spec = codexSpec(store.StateWorking, agentAlive, false)
			}
			r := e.seedRow(t, spec)
			first := e.clock.Now()
			for i, phase := range []string{"first", "same owner", "unreadable", "recovered", "find-missing"} {
				t.Run(phase, func(t *testing.T) {
					now := first.Add(time.Duration(i) * time.Minute)
					status, reason, why := store.ConfirmationConfirmed, "", "-"
					e.pc.Set(r.AgentPID, procfix.Alive(r.AgentStart))
					if phase == "unreadable" {
						e.pc.Set(r.AgentPID, procfix.Unreadable())
						status, reason, why = store.ConfirmationNotConfirmed, store.ReasonUnreadable, store.ReasonUnreadable
					}
					want := status + "|" + confAt(first) + "|process|" + confAt(now) + "|" + why
					if phase == "find-missing" {
						if _, err := api.FindMissing(context.Background(), e.st, e.rec, e.pc, time.Second, time.Minute, func() time.Time { return now }, nil); err != nil {
							t.Fatal(err)
						}
					} else {
						assertProcess(t, readMain(t, e.st, api.RelayView{Procs: e.pc, Now: func() time.Time { return now }}, r.ID), store.StateWorking, store.ProcessAlive, want)
					}
					sp, err := e.st.GetSpawn(r.ID)
					if err != nil {
						t.Fatal(err)
					}
					wantStored := store.Confirmation{Status: status, ConfirmedAtMillis: first.UnixMilli(), Source: store.EvidenceProcess, CheckedAtMillis: now.UnixMilli(), Reason: reason}
					if value, rec := sp.ProcessConfirmation(); value != store.ProcessAlive || rec != wantStored {
						t.Errorf("durable process record %s %+v; want alive %+v", value, rec, wantStored)
					}
					// Stored-only reads at a later clock neither probe nor redate either time.
					before := sp.ProcessEvidence
					assertProcess(t, readMain(t, e.st, api.RelayView{Now: func() time.Time { return now.Add(time.Hour) }}, r.ID), store.StateWorking, store.ProcessAlive, want)
					if after := storedProcess(t, e.st, r.ID); after != before {
						t.Errorf("stored-only read changed process evidence: %s -> %s", before, after)
					}
				})
			}
		})
	}
}

// b.x9o R5: an invalid present token lends neither its first time nor its
// source to a successful or failed real checked read (including find-missing).
func TestProcessRecordTokenOwnershipOnCheckedReads(t *testing.T) {
	t.Parallel()
	for _, agent := range []string{store.AgentClaude, store.AgentCodex} {
		for _, tc := range []struct {
			name             string
			token            any
			tokenless, valid bool
		}{
			{"malformed", "not-a-launch-token", true, false},
			{"empty", "", true, false},
			{"null", nil, true, true},
			{"matching", "0123456789abcdef", false, true},
			{"foreign", "fedcba9876543210", false, false},
		} {
			t.Run(agent+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				e := newKillEnv(t)
				spec := killRowSpec{State: store.StateWorking, Opts: []apitest.SpawnOption{apitest.WithAgent(agent), apitest.WithLifeNumber(0)}}
				r := e.seedRow(t, spec)
				var owner any = "0123456789abcdef"
				if tc.tokenless {
					owner = nil
				}
				cursorReadSQL(t, e.dbPath, `UPDATE spawns SET launch_token=? WHERE instance_id=?`, owner, r.ID)
				first := e.clock.Now()
				for _, phase := range []string{"success", "failure", "find-missing"} {
					t.Run(phase, func(t *testing.T) {
						cursorReadSQL(t, e.dbPath, `UPDATE spawns SET process_evidence=json_object('v','alive','life',0,'token',?,
                            'pid',?,'start',?,'at',?,'src','tmux','checked',?,'reason',NULL) WHERE instance_id=?`, tc.token, r.AgentPID, r.AgentStart, first.UnixMilli(), first.UnixMilli(), r.ID)
						before := storedProcess(t, e.st, r.ID)
						value, record := store.ProcessUnknown, unknownRecord(store.ReasonNoEvidence)
						if tc.valid {
							value, record = store.ProcessAlive, confirmed(first, store.EvidenceTmux)
						}
						assertProcess(t, readMain(t, e.st, api.RelayView{}, r.ID), store.StateWorking, value, record)
						if storedProcess(t, e.st, r.ID) != before {
							t.Error("stored-only reads changed the prior record")
						}
						now := first.Add(time.Minute)
						e.pc.Set(r.AgentPID, procfix.Alive(r.AgentStart))
						value = store.ProcessAlive
						want := store.Confirmation{Status: store.ConfirmationConfirmed, ConfirmedAtMillis: now.UnixMilli(), Source: store.EvidenceProcess, CheckedAtMillis: now.UnixMilli()}
						if phase == "failure" {
							e.pc.Set(r.AgentPID, procfix.Unreadable())
							value = store.ProcessUnknown
							want = store.Confirmation{Status: store.ConfirmationNotConfirmed, CheckedAtMillis: now.UnixMilli(), Reason: store.ReasonUnreadable}
						}
						if tc.valid {
							value, want.ConfirmedAtMillis, want.Source = store.ProcessAlive, first.UnixMilli(), store.EvidenceTmux
						}
						at, checked, source, why := "-", confAt(now), "-", "-"
						if want.ConfirmedAtMillis > 0 {
							at, source = confAt(time.UnixMilli(want.ConfirmedAtMillis)), want.Source
						}
						if want.Reason != "" {
							why = want.Reason
						}
						record = strings.Join([]string{want.Status, at, source, checked, why}, "|")
						if phase == "find-missing" {
							if _, err := api.FindMissing(context.Background(), e.st, e.rec, e.pc, time.Second, time.Minute, func() time.Time { return now }, nil); err != nil {
								t.Fatal(err)
							}
						} else {
							assertProcess(t, readMain(t, e.st, api.RelayView{Procs: e.pc, Now: func() time.Time { return now }}, r.ID), store.StateWorking, value, record)
						}
						sp, err := e.st.GetSpawn(r.ID)
						if err != nil {
							t.Fatal(err)
						}
						if v, got := sp.ProcessConfirmation(); v != value || got != want {
							t.Errorf("durable checked process record = %s %+v; want %s %+v", v, got, value, want)
						}
						assertProcess(t, readMain(t, e.st, api.RelayView{Now: func() time.Time { return now.Add(time.Hour) }}, r.ID), store.StateWorking, value, record)
						if storedProcess(t, e.st, r.ID) != sp.ProcessEvidence {
							t.Error("later stored-only reads changed process evidence")
						}
					})
				}
			})
		}
	}
}

// b.x9o repair 04: each public non-sampling projection keeps the lifecycle
// value, rejects malformed owners, and neither normalizes nor redates storage.
func TestStateRecordTokenOwnershipOnStoredReads(t *testing.T) {
	t.Parallel()
	for _, agent := range []string{store.AgentClaude, store.AgentCodex, store.AgentCursor} {
		for _, tc := range []struct {
			name             string
			token            any
			tokenless, valid bool
		}{
			{"malformed", "not-a-launch-token", true, false},
			{"empty", "", true, false},
			{"null", nil, true, true},
			{"matching", "0123456789abcdef", false, true},
			{"foreign", "fedcba9876543210", false, false},
		} {
			for _, settled := range []bool{false, true} {
				mode := "raw"
				if settled {
					mode = "settled"
				}
				t.Run(agent+"/"+tc.name+"/"+mode, func(t *testing.T) {
					t.Parallel()
					e := newKillEnv(t)
					r := e.seedRow(t, killRowSpec{State: store.StateWorking, Opts: []apitest.SpawnOption{apitest.WithAgent(agent), apitest.WithLifeNumber(0)}})
					var owner any = "0123456789abcdef"
					if tc.tokenless {
						owner = nil
					}
					first := e.clock.Now()
					cursorReadSQL(t, e.dbPath, `UPDATE spawns SET launch_token=?,state_evidence=json_object('v','working','life',0,'token',?,
                        'epoch',permission_epoch,'at',?,'src','agent_event','checked',?,'reason',NULL) WHERE instance_id=?`, owner, tc.token, first.UnixMilli(), first.Add(time.Second).UnixMilli(), r.ID)
					var settle []store.SettleRead
					if settled {
						cursorReadSQL(t, e.dbPath, `UPDATE spawns SET settled_state=state,settled_evidence=state_evidence,state='waiting',state_changed_ms=?,
                            state_evidence=json_object('v','waiting','life',0,'token',launch_token,'epoch',permission_epoch,'at',?,'src','store','checked',?) WHERE instance_id=?`, first.Add(2*time.Second).UnixMilli(), first.Add(2*time.Second).UnixMilli(), first.Add(2*time.Second).UnixMilli(), r.ID)
						settle = []store.SettleRead{{NowMs: first.Add(3 * time.Second).UnixMilli(), SettleMs: 5000}}
					}
					before, err := e.st.GetSpawn(r.ID)
					if err != nil {
						t.Fatal(err)
					}
					want := unknownRecord(store.ReasonNoEvidence)
					if tc.valid {
						want = "confirmed|" + confAt(first) + "|agent_event|" + confAt(first.Add(time.Second)) + "|-"
					}
					for _, read := range readMain(t, e.st, api.RelayView{}, r.ID, settle...) {
						if read.state != store.StateWorking || conf(read.records.State) != want {
							t.Errorf("%s stored state record = %s %s; want working %s", read.verb, read.state, conf(read.records.State), want)
						}
					}
					if after, err := e.st.GetSpawn(r.ID); err != nil || !reflect.DeepEqual(after, before) {
						t.Errorf("pure public state reads changed the durable row: %v", err)
					}
				})
			}
		}
	}
}

// markThenLockStore makes the shared checks' terminal moves through the
// nonwaiting writers, then runs after (a hook taking the write lock), so any
// further write of the same read is refused busy.
type markThenLockStore struct {
	*store.Store
	after func()
}

func (s *markThenLockStore) MarkMissingIfSameLifeFrom(id string, snap store.RowSnapshot, src string, exit store.ProcessExit) (string, store.CondResult, bool, error) {
	prior, res, recorded, err := s.Store.RecoveryWrites().MarkMissingIfSameLifeFrom(id, snap, src, exit)
	s.after()
	return prior, res, recorded, err
}

func (s *markThenLockStore) MarkEndedIfSameLifeFrom(id string, snap store.RowSnapshot, exit store.ProcessExit) (string, store.CondResult, bool, error) {
	prior, res, recorded, err := s.Store.MarkEndedIfSameLifeFrom(id, snap, exit)
	s.after()
	return prior, res, recorded, err
}

// legacyMarkStore offers only the legacy mark, which clears the process
// record; the read's follow-up then records the exit it judged.
type legacyMarkStore struct{ *store.Store }

// interleavedClearStore runs after once, right after the read's liveness-note
// clear commits and before the read refreshes its row.
type interleavedClearStore struct {
	*store.Store
	after func()
}

func (s *interleavedClearStore) ClearLivenessIfSameLife(id string, snap store.RowSnapshot) (store.CondResult, error) {
	res, err := s.Store.ClearLivenessIfSameLife(id, snap)
	if after := s.after; after != nil {
		s.after = nil
		after()
	}
	return res, err
}

// TestTerminalMovesNeverReportAlive (b.x9o B1, S5, R1): once a row is ended or
// missing, no read reports its last alive record. A move proven by the
// recorded process being gone commits its exited record with the move, so a
// write refused right after it changes nothing (no second commit). A move
// that proved no exit, or a legacy writer's, leaves unknown or the read's own
// follow-up. A sample taken before a concurrent terminal move is dropped as
// stale and the move's own record stands.
func TestTerminalMovesNeverReportAlive(t *testing.T) {
	t.Parallel()
	exited := func(at time.Time) string { return confirmed(at, "process") }
	probe := func(t *testing.T, e *killEnv, r killRow) time.Time {
		t.Helper()
		at := e.clock.Now()
		reads := readMain(t, e.st, api.RelayView{Procs: e.pc, Now: func() time.Time { return at }}, r.ID)
		if reads[0].process != store.ProcessAlive {
			t.Fatalf("setup read: process_liveness %s; want alive", reads[0].process)
		}
		return at
	}

	t.Run("pause's Codex end, real store", func(t *testing.T) {
		t.Parallel()
		e := newKillEnv(t)
		r := e.seedRow(t, codexSpec(store.StateWorking, agentAlive, true))
		alive := probe(t, e, r)
		e.pc.Set(r.AgentPID, procfix.Gone())
		e.clock.Advance(time.Minute)
		end := e.clock.Now()
		if _, err := api.Pause(context.Background(), e.st, e.rec, api.PauseEnv{
			Relay: api.RelayView{Procs: e.pc, Now: func() time.Time { return end }, Window: sendKeysWindow()}}, pauseTimeoutSeconds, pauseParams(r)); err != nil {
			t.Fatalf("Pause: %v", err)
		}
		if !end.After(alive) {
			t.Fatal("the end is not after the alive record; the check would be vacuous")
		}
		for _, view := range []api.RelayView{{}, {Procs: e.pc, Now: func() time.Time { return end.Add(time.Minute) }}} {
			assertProcess(t, readMain(t, e.st, view, r.ID), store.StateEnded, store.ProcessExited, exited(end))
		}
	})

	t.Run("pause's Codex end, legacy writer", func(t *testing.T) {
		t.Parallel()
		e := newKillEnv(t)
		r := e.seedRow(t, codexSpec(store.StateWorking, agentAlive, true))
		probe(t, e, r)
		e.pc.Set(r.AgentPID, procfix.Gone())
		if _, err := e.pause(pauseParams(r)); err != nil {
			t.Fatalf("Pause: %v", err)
		}
		assertProcess(t, readMain(t, e.st, api.RelayView{}, r.ID), store.StateEnded, store.ProcessUnknown, unknownRecord(store.ReasonNoEvidence))
	})

	for _, agent := range []string{store.AgentClaude, store.AgentCodex} {
		for _, verb := range []string{"status", "find-missing"} {
			t.Run("mark, then the write lock/"+agent+"/"+verb, func(t *testing.T) {
				t.Parallel()
				e := newKillEnv(t)
				spec, want := killRowSpec{State: store.StateWorking}, store.StateMissing
				if agent == store.AgentCodex {
					spec, want = codexSpec(store.StateWorking, agentAlive, true), store.StateEnded
				}
				r := e.seedRow(t, spec)
				probe(t, e, r)
				e.pc.Set(r.AgentPID, procfix.Gone())
				e.clock.Advance(time.Minute)
				gone := e.clock.Now()
				var release func()
				s := &markThenLockStore{Store: e.st, after: func() { release = apitest.HoldWriteLock(t, e.dbPath, time.Minute) }}
				view := api.RelayView{Procs: e.pc, Now: func() time.Time { return gone }}
				if verb == "status" {
					got, err := api.Status(s, view, r.ID)
					if err != nil || got.State != want || got.ProcessLiveness != store.ProcessExited || conf(got.Confirmations.ProcessLiveness) != exited(gone) {
						t.Errorf("Status = %s %s %s, %v; want %s exited %s with no attempt after the mark", got.State, got.ProcessLiveness,
							conf(got.Confirmations.ProcessLiveness), err, want, exited(gone))
					}
				} else if _, err := api.FindMissing(context.Background(), s, e.rec, e.pc, time.Second, time.Minute, func() time.Time { return gone }, nil); err != nil {
					t.Fatalf("FindMissing: %v", err)
				}
				if release == nil {
					t.Fatal("no mark ran; the check would be vacuous")
				}
				release()
				assertProcess(t, readMain(t, e.st, api.RelayView{}, r.ID), want, store.ProcessExited, exited(gone))
			})
		}
	}

	t.Run("legacy mark, then the read's own follow-up", func(t *testing.T) {
		t.Parallel()
		e := newKillEnv(t)
		r := e.seedRow(t, killRowSpec{State: store.StateWorking})
		probe(t, e, r)
		e.pc.Set(r.AgentPID, procfix.Gone())
		e.clock.Advance(time.Minute)
		gone := e.clock.Now()
		got, err := api.Status(legacyMarkStore{e.st}, api.RelayView{Procs: e.pc, Now: func() time.Time { return gone }}, r.ID)
		if err != nil || got.State != store.StateMissing || conf(got.Confirmations.ProcessLiveness) != exited(gone) {
			t.Errorf("Status = %s %s, %v; want missing, exited %s", got.State, conf(got.Confirmations.ProcessLiveness), err, exited(gone))
		}
		assertProcess(t, readMain(t, e.st, api.RelayView{}, r.ID), store.StateMissing, store.ProcessExited, exited(gone))
	})

	for _, tc := range []struct {
		name       string
		priorAlive bool // an alive record stored before the read
		interfere  func(t *testing.T, e *killEnv, r killRow, at time.Time)
		state      string
		value      string
		response   func(probe, at time.Time) string
		later      func(at time.Time) string
	}{
		{"concurrent proven mark", true, func(t *testing.T, e *killEnv, r killRow, at time.Time) {
			sp, err := e.st.GetSpawn(r.ID)
			if err != nil {
				t.Fatal(err)
			}
			exit := store.ProcessExit{PID: r.AgentPID, Start: r.AgentStart, AtMillis: at.UnixMilli()}
			if _, res, recorded, err := e.st.RecoveryWrites(sp).MarkMissingIfSameLifeFrom(r.ID, sp.Snapshot, store.EvidenceProcess, exit); err != nil || res != store.CondApplied || !recorded {
				t.Fatalf("interfering mark = %v, %v, %v", res, recorded, err)
			}
		}, store.StateMissing, store.ProcessExited,
			func(_, at time.Time) string { return exited(at) }, func(at time.Time) string { return exited(at) }},
		{"concurrent SessionEnd over an alive record", true, func(t *testing.T, e *killEnv, r killRow, _ time.Time) {
			if got := storefix.ApplyAgentHook(t, e.st, r.ID, "SessionEnd", ""); !got.Applied {
				t.Fatalf("SessionEnd = %+v", got)
			}
		}, store.StateEnded, store.ProcessUnknown,
			func(time.Time, time.Time) string { return unknownRecord(store.ReasonNoEvidence) }, func(time.Time) string { return unknownRecord(store.ReasonNoEvidence) }},
		{"concurrent SessionEnd, no record", false, func(t *testing.T, e *killEnv, r killRow, _ time.Time) {
			if got := storefix.ApplyAgentHook(t, e.st, r.ID, "SessionEnd", ""); !got.Applied {
				t.Fatalf("SessionEnd = %+v", got)
			}
		}, store.StateEnded, store.ProcessUnknown,
			func(probe, _ time.Time) string { return "not_confirmed|-|-|" + confAt(probe) + "|stale_observation" },
			func(time.Time) string { return unknownRecord(store.ReasonNoEvidence) }},
	} {
		t.Run("sample before a terminal move/"+tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{State: store.StateWorking, Opts: noted})
			if tc.priorAlive {
				probe(t, e, r)
				sp, err := e.st.GetSpawn(r.ID)
				if err != nil {
					t.Fatal(err)
				}
				// The probe cleared the note; put one back so the next read clears it again.
				if res, err := e.st.SetLivenessNoteIfSameLife(r.ID, sp.Snapshot, "probe_eacces"); err != nil || res != store.CondApplied {
					t.Fatalf("SetLivenessNoteIfSameLife = %v, %v", res, err)
				}
				e.clock.Advance(time.Minute)
			}
			at := e.clock.Now()
			moved := at.Add(time.Second)
			s := &interleavedClearStore{Store: e.st, after: func() { tc.interfere(t, e, r, moved) }}
			got, err := api.Status(s, api.RelayView{Procs: e.pc, Now: func() time.Time { return at }}, r.ID)
			if s.after != nil {
				t.Fatal("the read cleared no note; the interleaving never ran")
			}
			if err != nil || got.State != tc.state || got.ProcessLiveness != tc.value || conf(got.Confirmations.ProcessLiveness) != tc.response(at, moved) {
				t.Errorf("Status = %s %s %s, %v; want %s %s %s", got.State, got.ProcessLiveness, conf(got.Confirmations.ProcessLiveness), err,
					tc.state, tc.value, tc.response(at, moved))
			}
			assertProcess(t, readMain(t, e.st, api.RelayView{}, r.ID), tc.state, tc.value, tc.later(moved))
		})
	}
}

// repairBusyStore refuses the checked read's check_permission repair as busy,
// after running before (nil: nothing), and counts the repairs tried.
type repairBusyStore struct {
	*store.Store
	before func()
	calls  int
}

func (s *repairBusyStore) RepairCheckPermissionSample(store.Spawn, []store.PermissionRow) (string, store.CondResult, error) {
	s.calls++
	if s.before != nil {
		s.before()
	}
	return "", 0, fmt.Errorf("repair: %w", store.ErrStoreBusy)
}

// TestStateAttemptBindsToTheExaminedValue (b.x9o S1): a check_permission
// repair refused busy is the read's latest state attempt, not_confirmed with
// the request's older confirmation, but only while the row still holds the
// check_permission it examined and the attempt is no older than the record:
// a hook that commits working meanwhile shows its own record, never an
// overlay, and an attempt older than the stored confirmation leaves it as it
// is, so checked_at never precedes confirmed_at.
func TestStateAttemptBindsToTheExaminedValue(t *testing.T) {
	t.Parallel()
	for _, tc := range []string{"row unchanged", "a hook commits working first", "an attempt older than the record"} {
		t.Run(tc, func(t *testing.T) {
			t.Parallel()
			e := newRelayEnv(t, relayEnvHook)
			timeoutDeny(t, e.s, e.id, storefix.TestRequestTokenA)
			e.pc.Set(relayEnvHook.PID, procfix.Gone())
			stored, err := api.Status(e.s, api.RelayView{}, e.id)
			if err != nil || stored.Confirmations.State.ConfirmedAt == nil {
				t.Fatalf("setup: %+v %v; want a confirmed check_permission record", stored, err)
			}
			e.now = time.Now().UTC().Add(time.Second).Truncate(time.Millisecond)
			if tc == "an attempt older than the record" {
				// Same value, interval and owner, but the attempt predates the
				// record: checked_at may never precede confirmed_at.
				confirmedAt, err := time.Parse(displayTS, *stored.Confirmations.State.ConfirmedAt)
				if err != nil {
					t.Fatal(err)
				}
				e.now = confirmedAt.Add(-time.Minute)
			}
			s := &repairBusyStore{Store: e.s}
			if tc == "a hook commits working first" {
				s.before = func() {
					if err := storefix.WithSeedPane(e.dbPath, e.id, func(gate store.HookGate) error {
						_, _, err := e.s.ApplyHookTransitionResult(e.id, gate, store.StateWorking, false, "PreToolUse", "", false)
						return err
					}); err != nil {
						t.Fatalf("PreToolUse: %v", err)
					}
				}
			}
			got, err := api.Status(s, e.view(), e.id)
			if err != nil {
				t.Fatal(err)
			}
			if s.calls == 0 {
				t.Fatal("the repair never ran; the check would be vacuous")
			}
			record := got.Confirmations.State
			switch tc {
			case "a hook commits working first":
				if got.State != store.StateWorking || record.Status != store.ConfirmationConfirmed || record.Source == nil ||
					*record.Source != store.EvidenceAgentEvent || record.CheckedAt == nil || *record.CheckedAt < *record.ConfirmedAt {
					t.Errorf("Status = %s %s; want working with the hook's own record, no overlay", got.State, conf(record))
				}
			case "an attempt older than the record":
				if got.State != store.StateCheckPermission || conf(record) != conf(stored.Confirmations.State) {
					t.Errorf("Status = %s %s; want the stored record %s, no older overlay", got.State, conf(record), conf(stored.Confirmations.State))
				}
			default:
				want := "not_confirmed|" + *stored.Confirmations.State.ConfirmedAt + "|" + *stored.Confirmations.State.Source + "|" + confAt(e.now) + "|busy"
				if got.State != store.StateCheckPermission || conf(record) != want {
					t.Errorf("Status = %s %s; want check_permission %s", got.State, conf(record), want)
				}
			}
		})
	}
}

// TestCheckedReadOutcomes (b.x9o, cw deferral S3): each checked write's
// outcome result and the SR-4.5 reason its field's attempt reports; a
// finished row records every adapter it admits none of, one by one; a
// canceled read records the rows it never checked as remaining.
func TestCheckedReadOutcomes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name           string
		err            error
		applied        bool
		result, reason string
	}{
		{"applied", nil, true, "committed", ""},
		{"no change", nil, false, "unchanged", ""},
		{"busy", fmt.Errorf("w: %w", store.ErrStoreBusy), false, "busy", store.ReasonBusy},
		{"canceled", context.Canceled, false, "cancelled", store.ReasonDeadline},
		{"deadline", context.DeadlineExceeded, false, "cancelled", store.ReasonDeadline},
		{"stale sample", inputdelivery.ErrStale, false, "stale", store.ReasonStaleObservation},
		{"commit unconfirmed", inputdelivery.ErrUnconfirmed, false, "unconfirmed", store.ReasonCommitUnconfirmed},
		{"row gone", store.ErrSpawnNotFound, false, "absent", store.ReasonNoEvidence},
		{"other failure", errors.New("disk"), false, "unreadable", store.ReasonUnreadable},
	} {
		if result, reason := api.CheckOutcomeForTest(tc.err, tc.applied); result != tc.result || reason != tc.reason {
			t.Errorf("%s: %s, %q; want %s, %q", tc.name, result, reason, tc.result, tc.reason)
		}
	}

	e := newKillEnv(t)
	ended, live := e.seedRow(t, killRowSpec{State: store.StateEnded}), e.seedRow(t, killRowSpec{State: store.StateWorking})
	var adapters []string
	for _, a := range []string{"liveness", "cursor_gateway", "codex_turn", "transcript", "permission_read", "permission_repair", "display", "permission_fallback", "native_proof"} {
		adapters = append(adapters, a+"=unattempted")
	}
	finished := append([]string{}, adapters...)
	for _, a := range []string{"liveness", "cursor_gateway", "codex_turn", "transcript", "permission_read", "permission_repair", "display", "permission_fallback"} {
		finished = append(finished, a+"=inapplicable")
	}
	got := api.ActiveCheckOutcomesForTest(context.Background(), e.st, api.RelayView{}, []api.Spawn{ended.Spawn})
	if strings.Join(got[ended.ID], " ") != strings.Join(finished, " ") {
		t.Errorf("finished row outcomes %v; want %v", got[ended.ID], finished)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got = api.ActiveCheckOutcomesForTest(ctx, e.st, api.RelayView{}, []api.Spawn{live.Spawn})
	if want := append(adapters, "remaining=cancelled"); strings.Join(got[live.ID], " ") != strings.Join(want, " ") {
		t.Errorf("canceled read's outcomes %v; want %v", got[live.ID], want)
	}
}

// busyProcessRecordStore refuses the process record of one row as busy.
type busyProcessRecordStore struct {
	*store.Store
	busy string
}

func (s *busyProcessRecordStore) RecordProcessEvidence(row store.Spawn, record string) (store.CondResult, error) {
	if row.InstanceID == s.busy {
		return 0, fmt.Errorf("record: %w", store.ErrStoreBusy)
	}
	return s.Store.RecordProcessEvidence(row, record)
}

// TestOneRowsFailedProcessRecordSparesAnother (b.x9o, Epic AC3): in one
// list, a row whose process record is refused busy reports its attempt,
// response-only, while another selected row's record commits.
func TestOneRowsFailedProcessRecordSparesAnother(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	busy, fine := e.seedRow(t, killRowSpec{State: store.StateWorking}), e.seedRow(t, killRowSpec{State: store.StateWaiting})
	now := e.clock.Now()
	list, err := api.List(&busyProcessRecordStore{Store: e.st, busy: busy.ID}, api.RelayView{Procs: e.pc, Now: func() time.Time { return now }}, api.ListParams{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{busy.ID: "not_confirmed|-|-|" + confAt(now) + "|busy", fine.ID: confirmed(now, "process")}
	for _, row := range list.Spawns {
		if got := conf(row.Confirmations.ProcessLiveness); got != want[row.InstanceID] {
			t.Errorf("%s process record %s; want %s", row.InstanceID, got, want[row.InstanceID])
		}
	}
	if storedProcess(t, e.st, busy.ID) != "" || storedProcess(t, e.st, fine.ID) == "" {
		t.Error("want only the other row's record stored")
	}
}

// judgedClock is a relay clock that advances a second per reading and keeps
// the first reading taken once the agent process was judged.
type judgedClock struct {
	mu        sync.Mutex
	t, judged time.Time
	judging   bool
}

func (c *judgedClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Second)
	if c.judging && c.judged.IsZero() {
		c.judged = c.t
	}
	return c.t
}

// judgingChecker is a process fake that arms clock when pid is read.
type judgingChecker struct {
	*procfix.Checker
	pid   int
	clock *judgedClock
}

func (j judgingChecker) StartTime(pid int) (string, bool, bool) {
	if pid == j.pid {
		j.clock.mu.Lock()
		j.clock.judging = true
		j.clock.mu.Unlock()
	}
	return j.Checker.StartTime(pid)
}

// TestProcessRecordTakesTheJudgementTime (b.x9o S-a): with a relay clock that
// advances on every reading, a process record, alive or a gone process's
// exited record committed by its mark, is stamped with the instant the
// process was judged, never a later reading taken when it was written.
func TestProcessRecordTakesTheJudgementTime(t *testing.T) {
	t.Parallel()
	for _, agent := range []agentState{agentAlive, agentGone} {
		t.Run(map[agentState]string{agentAlive: "alive", agentGone: "gone"}[agent], func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{State: store.StateWorking, Agent: agent})
			clock := &judgedClock{t: e.clock.Now()}
			if _, err := api.Status(e.st, api.RelayView{Procs: judgingChecker{e.pc, r.AgentPID, clock}, Now: clock.now}, r.ID); err != nil {
				t.Fatal(err)
			}
			sp, err := e.st.GetSpawn(r.ID)
			if err != nil {
				t.Fatal(err)
			}
			_, rec := sp.ProcessConfirmation()
			if clock.judged.IsZero() || rec.ConfirmedAtMillis != clock.judged.UnixMilli() || rec.CheckedAtMillis != rec.ConfirmedAtMillis {
				t.Errorf("process record %+v; want it stamped at the judgement, %s (clock now %s)", rec, clock.judged, clock.t)
			}
		})
	}
}
