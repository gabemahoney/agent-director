package store_test

import (
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// unrelatedExits are the exits another row retains: a pending one and a
// malformed one.
var unrelatedExits = []struct{ name, raw string }{
	{"pending", `{"prior_state":"needs_intelligence","new_state":"ended","at_ms":1792411205000}`},
	{"malformed", `{"prior_state":`},
}

// Retained exits are deliberately seeded after opening: malformed historical
// metadata must not participate in another row's otherwise valid transaction.
func seedUnrelatedDisplayExit(t *testing.T, e *cursorTrailEnv, raw string) string {
	t.Helper()
	id, err := apitest.SeedSpawn(e.f.path, "unrelated-exit", store.StateEnded, "", "off", "", false, apitest.WithDisplayExit(raw))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestCursorBoundedTransactionsIgnoreUnrelatedDisplayExit(t *testing.T) {
	for _, operation := range []string{"prompt authority", "admission", "relay acknowledgement", "native claim", "relay answer authority", "native answer authority"} {
		for _, marker := range unrelatedExits {
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
				other := seedUnrelatedDisplayExit(t, e, marker.raw)
				mark := store.TrailMark(t)
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
				if raw := storedDisplayExit(t, e.f.path, other); !raw.Valid || raw.String != marker.raw {
					t.Errorf("unrelated exit consumed: %+v", raw)
				}
				assertDisplayRecords(t, mark, other)
			})
		}
	}
}

func TestCursorBoundedMissingDisplayExitCommitIsolation(t *testing.T) {
	for _, outcome := range []string{"committed", "stale snapshot", "failed close", "failed commit"} {
		for _, marker := range unrelatedExits {
			t.Run(outcome+"/"+marker.name, func(t *testing.T) {
				e := newCursorTrailEnv(t)
				token := storefix.TestRequestTokenA
				e.insert(t, token)
				e.showStoredDisplay(t, storedNeedsIntelligence)
				row, err := e.f.s.GetSpawn(e.b.InstanceID)
				if err != nil || row.CommittedDisplay().State != store.StateNeedsIntelligence {
					t.Fatalf("stored needs_intelligence setup: %+v %v", row, err)
				}
				before := e.read(t, token)
				other := seedUnrelatedDisplayExit(t, e, marker.raw)
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
							assertDisplayRecords(t, mark, row.InstanceID)
						}
					}
				})
				prior, result, err := e.f.s.MarkCursorMissingIfSameLaunch(examined, e.b)
				after, readErr := e.f.s.GetSpawn(row.InstanceID)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if outcome == "committed" {
					if err != nil || result != store.CondApplied || prior != row.State || after.State != store.StateMissing || after.DisplayObservation != "" || e.read(t, token).AwaitsAnswer() || commits != 1 {
						t.Fatalf("committed missing: %q %v %v row=%+v commits=%d", prior, result, err, after, commits)
					}
					_, stamp, err := apitest.ReadSettle(e.f.path, row.InstanceID)
					if err != nil {
						t.Fatal(err)
					}
					exit := displayRecord{store.StateNeedsIntelligence, store.StateMissing, time.UnixMilli(stamp)}
					assertDisplayRecords(t, mark, row.InstanceID, exit)
					if _, result, err := e.f.s.MarkCursorMissingIfSameLaunch(examined, e.b); err != nil || result != store.CondChanged {
						t.Errorf("repeat: %v %v", result, err)
					}
					assertDisplayRecords(t, mark, row.InstanceID, exit)
				} else {
					if outcome == "stale snapshot" {
						if err != nil || result != store.CondChanged || prior != "" {
							t.Errorf("refused missing: %q %v %v", prior, result, err)
						}
					} else if err == nil || prior != "" || (outcome == "failed commit" && (commits != 1 || !strings.Contains(err.Error(), "commit"))) {
						t.Errorf("failure not observed: %q %v %v commits=%d", prior, result, err, commits)
					}
					if after.State != row.State || after.RowVersion != row.RowVersion || after.DisplayObservation != row.DisplayObservation || e.read(t, token) != before {
						t.Errorf("uncommitted missing changed facts: row=%+v", after)
					}
					assertDisplayRecords(t, mark, row.InstanceID)
				}
				if raw := storedDisplayExit(t, e.f.path, row.InstanceID); raw.Valid {
					t.Errorf("own exit retained after commit or rollback: %+v", raw)
				}
				if raw := storedDisplayExit(t, e.f.path, other); !raw.Valid || raw.String != marker.raw {
					t.Errorf("unrelated exit consumed: %+v", raw)
				}
				assertDisplayRecords(t, mark, other)
			})
		}
	}
}
