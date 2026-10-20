package store_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// Retained exits are deliberately seeded after opening: malformed historical
// metadata must not participate in another row's otherwise valid transaction.
func seedUnrelatedStuckExit(t *testing.T, e *cursorTrailEnv, raw string) string {
	t.Helper()
	id, err := apitest.SeedSpawn(e.f.path, "unrelated-exit", store.StateEnded, "", "off", "", false)
	if err != nil {
		t.Fatal(err)
	}
	e.exec(t, `UPDATE spawns SET stuck_exit=? WHERE instance_id=?`, raw, id)
	return id
}

func readStuckExit(t *testing.T, e *cursorTrailEnv, id string) sql.NullString {
	t.Helper()
	db, err := sql.Open("sqlite", e.f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var raw sql.NullString
	if err := db.QueryRow(`SELECT stuck_exit FROM spawns WHERE instance_id=?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestCursorBoundedTransactionsIgnoreUnrelatedStuckExit(t *testing.T) {
	for _, operation := range []string{"prompt authority", "admission", "relay acknowledgement", "native claim", "relay answer authority", "native answer authority"} {
		for _, marker := range []struct{ name, raw string }{
			{"pending", `{"new_state":"ended","reason":"permission_unanswered","at_ms":1792411205000}`},
			{"malformed", `{"new_state":`},
		} {
			t.Run(operation+"/"+marker.name, func(t *testing.T) {
				e := newCursorTrailEnv(t)
				token := storefix.TestRequestTokenA
				if operation != "prompt authority" && operation != "admission" {
					e.insert(t, token)
				}
				if operation == "relay acknowledgement" || operation == "relay answer authority" {
					if ok, err := e.f.s.DecidePermissionRequest(e.b.InstanceID, token, "allow", "", store.WriterProcessDecide); err != nil || !ok {
						t.Fatalf("decision: %v %v", ok, err)
					}
				}
				if operation == "relay answer authority" {
					if _, ok, err := e.f.s.AckCursorDecision(e.b, token, false, e.until, e.until, store.DefaultLockWait, cursorTrailCheck); err != nil || !ok {
						t.Fatalf("ack setup: %v %v", ok, err)
					}
				}
				if operation == "native answer authority" {
					if ok, err := e.f.s.ClaimCursorPaneAnswer(e.b, token, "allow", e.until, store.DefaultLockWait, cursorTrailCheck); err != nil || !ok {
						t.Fatalf("claim setup: %v %v", ok, err)
					}
				}
				other := seedUnrelatedStuckExit(t, e, marker.raw)
				mark := store.TrailMark(t)
				var exitReads int
				stop := store.ObservePromptRowsForTest(t, e.f.s, e.f.path, func(_ context.Context, q string) error {
					if strings.HasPrefix(q, "SELECT") && strings.Contains(q, "stuck_exit") {
						exitReads++
					}
					return nil
				}, nil)
				defer stop()
				switch operation {
				case "prompt authority":
					if ok, err := e.f.s.CheckCursorPrompt(e.b, e.until, cursorTrailCheck); err != nil || !ok {
						t.Errorf("valid prompt: %v %v", ok, err)
					}
				case "admission":
					e.insert(t, token)
					if row := e.read(t, token); row.RequestToken != token || row.Decision != "" {
						t.Errorf("admitted request: %+v", row)
					}
				case "relay acknowledgement":
					decision, ok, err := e.f.s.AckCursorDecision(e.b, token, false, e.until, e.until, store.DefaultLockWait, cursorTrailCheck)
					if err != nil || !ok || decision != "allow" || e.read(t, token).DeliveredAt.IsZero() {
						t.Errorf("valid acknowledgement: %q %v %v", decision, ok, err)
					}
				case "native claim":
					ok, err := e.f.s.ClaimCursorPaneAnswer(e.b, token, "allow", e.until, store.DefaultLockWait, cursorTrailCheck)
					if err != nil || !ok || e.read(t, token).PaneAnswer != store.PaneAnswerSent {
						t.Errorf("valid native claim: %v %v", ok, err)
					}
				case "relay answer authority", "native answer authority":
					if err := e.f.s.CheckCursorAnswer(e.b, token, operation == "relay answer authority", e.until, cursorTrailCheck); err != nil {
						t.Errorf("valid answer authority: %v", err)
					}
				}
				if exitReads != 0 {
					t.Errorf("unrelated exit inspected %d times", exitReads)
				}
				if raw := readStuckExit(t, e, other); !raw.Valid || raw.String != marker.raw {
					t.Errorf("unrelated exit consumed: %+v", raw)
				}
				if events := store.TrailEventsSince(t, mark, "ad.spawn.stuck_transition", other); len(events) != 0 {
					t.Errorf("unrelated exit emitted: %v", events)
				}
			})
		}
	}
}

func TestCursorBoundedMissingStuckExitCommitIsolation(t *testing.T) {
	for _, outcome := range []string{"committed", "stale snapshot", "failed close", "failed commit"} {
		for _, marker := range []struct{ name, raw string }{
			{"pending", `{"new_state":"ended","reason":"permission_unanswered","at_ms":1792411205000}`},
			{"malformed", `{"new_state":`},
		} {
			t.Run(outcome+"/"+marker.name, func(t *testing.T) {
				e := newCursorTrailEnv(t)
				token := storefix.TestRequestTokenA
				e.insert(t, token)
				row, err := e.f.s.GetSpawnSettled(e.b.InstanceID, store.SettleRead{})
				if err != nil {
					t.Fatal(err)
				}
				pr := e.read(t, token)
				at := e.until.Add(time.Minute)
				if display, err := e.f.s.ObservePermissionStuck(row, []store.PermissionSample{{Request: pr, FallbackAt: e.until, Basis: store.FallbackDeadline}}, at, 30*time.Second, store.SettleRead{}); err != nil || display.State != store.StateStuck {
					t.Fatalf("committed stuck setup: %+v %v", display, err)
				}
				row, err = e.f.s.GetSpawn(e.b.InstanceID)
				if err != nil {
					t.Fatal(err)
				}
				before := e.read(t, token)
				other := seedUnrelatedStuckExit(t, e, marker.raw)
				examined := row
				switch outcome {
				case "stale snapshot":
					examined.Snapshot.RowVersion++
				case "failed close":
					storefix.InjectWriteFailure(t, e.f.path, storefix.WriteFailPermissionDecision, row.InstanceID)
				case "failed commit":
					e.rejectCommitOnUpdate(t)
				}
				mark := store.TrailMark(t)
				var commits int
				store.BeforeCursorTrailExecForTest(t, e.f.s, e.f.path, func(q string) {
					if q == "COMMIT" {
						commits++
						if commits == 1 {
							if events := store.TrailEventsSince(t, mark, "ad.spawn.stuck_transition", row.InstanceID); len(events) != 0 {
								t.Errorf("exit before commit: %v", events)
							}
						}
					}
				})
				prior, result, err := e.f.s.MarkCursorMissingIfSameLaunch(examined, e.b)
				after, readErr := e.f.s.GetSpawn(row.InstanceID)
				if readErr != nil {
					t.Fatal(readErr)
				}
				events := store.TrailEventsSince(t, mark, "ad.spawn.stuck_transition", row.InstanceID)
				if outcome == "committed" {
					if err != nil || result != store.CondApplied || prior != row.State || after.State != store.StateMissing || after.StuckObservation != "" || e.read(t, token).AwaitsAnswer() || commits != 1 {
						t.Fatalf("committed missing: %q %v %v row=%+v commits=%d", prior, result, err, after, commits)
					}
					_, stamp, err := apitest.ReadSettle(e.f.path, row.InstanceID)
					if err != nil {
						t.Fatal(err)
					}
					if len(events) != 1 || events[0]["prior_state"] != "stuck" || events[0]["new_state"] != store.StateMissing || events[0]["stuck_reason"] != store.StuckPermissionUnanswered || events[0]["ts"] != time.UnixMilli(stamp).UTC().Format("2006-01-02T15:04:05.000Z") {
						t.Fatalf("committed exit: %v stamp=%d", events, stamp)
					}
					if _, result, err := e.f.s.MarkCursorMissingIfSameLaunch(examined, e.b); err != nil || result != store.CondChanged {
						t.Errorf("repeat: %v %v", result, err)
					}
					if events := store.TrailEventsSince(t, mark, "ad.spawn.stuck_transition", row.InstanceID); len(events) != 1 {
						t.Errorf("duplicate exit: %v", events)
					}
				} else {
					if outcome == "stale snapshot" {
						if err != nil || result != store.CondChanged || prior != "" {
							t.Errorf("refused missing: %q %v %v", prior, result, err)
						}
					} else if err == nil || prior != "" || (outcome == "failed commit" && (commits != 1 || !strings.Contains(err.Error(), "commit"))) {
						t.Errorf("failure not observed: %q %v %v commits=%d", prior, result, err, commits)
					}
					if after.State != row.State || after.RowVersion != row.RowVersion || after.StuckObservation != row.StuckObservation || e.read(t, token) != before || len(events) != 0 {
						t.Errorf("uncommitted missing changed facts/emitted: row=%+v events=%v", after, events)
					}
				}
				if raw := readStuckExit(t, e, row.InstanceID); raw.Valid {
					t.Errorf("own exit retained after commit or rollback: %+v", raw)
				}
				if raw := readStuckExit(t, e, other); !raw.Valid || raw.String != marker.raw {
					t.Errorf("unrelated exit consumed: %+v", raw)
				}
				if events := store.TrailEventsSince(t, mark, "ad.spawn.stuck_transition", other); len(events) != 0 {
					t.Errorf("unrelated exit emitted: %v", events)
				}
			})
		}
	}
}
