package storefix_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/writefailfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// proxyWrite is one of today's store writes a kind matches: how to seed its
// row, run it, and observe what it changes.
type proxyWrite struct {
	name        string
	kind        storefix.WriteFailureKind
	failOpen    bool // the store method swallows the failure; only the effect shows it
	ownKindOnly bool // run only with its own kind installed
	alsoBlocked []storefix.WriteFailureKind
	target      string                        // metadata key for store-wide authority, otherwise the instance id
	writeRaw    func(dbPath, id string) error // exact SQL operation where no public writer exists
	seed        func(t *testing.T, s *store.Store, dbPath, id string)
	write       func(s *store.Store, id string) error
	observe     func(t *testing.T, s *store.Store, dbPath, id string) any
}

// seedState seeds id in state through apitest.SeedSpawn.
func seedState(state, sessionID string) func(*testing.T, *store.Store, string, string) {
	return func(t *testing.T, _ *store.Store, dbPath, id string) {
		t.Helper()
		if _, err := apitest.SeedSpawn(dbPath, id, state, "", "", sessionID, false); err != nil {
			t.Fatalf("SeedSpawn(%q, %q): %v", id, state, err)
		}
	}
}

// seedLaunchPending seeds id as a fresh launch's pending row: a token and the
// test socket, with the six server and pane identity columns NULL.
func seedLaunchPending(t *testing.T, _ *store.Store, dbPath, id string) {
	t.Helper()
	launch := store.LaunchIdentity{Token: "0123456789abcdef", Socket: apitest.TestSocket}
	if _, err := apitest.SeedSpawn(dbPath, id, store.StatePending, "", "", "", false, apitest.WithLaunchIdentity(launch)); err != nil {
		t.Fatalf("SeedSpawn(%q, pending): %v", id, err)
	}
	c := observeColumns(t, nil, dbPath, id).(apitest.SpawnColumns)
	for name, v := range identityColumns(c) {
		if v != nil {
			t.Fatalf("seeded %q: %s = %v, want NULL", id, name, v)
		}
	}
}

// heldLaunchStartMs is the launch start seedHeldLaunch gives the row, which
// endHeldLaunch passes as the insert's (SR-5.3).
const heldLaunchStartMs int64 = 1700000000000

// seedHeldLaunch seeds id as a plain spawn's row after "duplicate session":
// pending at row_version 0 with a token, the test socket, no pane and launch
// start heldLaunchStartMs (SR-9.4).
func seedHeldLaunch(t *testing.T, _ *store.Store, dbPath, id string) {
	t.Helper()
	launch := store.LaunchIdentity{Token: "0123456789abcdef", Socket: apitest.TestSocket}
	if _, err := apitest.SeedSpawn(dbPath, id, store.StatePending, "", "", "", false,
		apitest.WithLaunchIdentity(launch), apitest.WithLaunchStartedAt(heldLaunchStartMs)); err != nil {
		t.Fatalf("SeedSpawn(%q, pending): %v", id, err)
	}
}

// endHeldLaunch runs the plain spawn's end write (store.EndHeldLaunch)
// against the seeded launch start; anything but CondApplied is an error.
func endHeldLaunch(s *store.Store, id string) error {
	res, err := s.EndHeldLaunch(id, heldLaunchStartMs, time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC))
	if err == nil && res != store.CondApplied {
		err = fmt.Errorf("EndHeldLaunch(%q) = %v, want CondApplied", id, res)
	}
	return err
}

// recordLaunchIdentity runs the identity write with the row's current version
// and token, as the launch that produced them would; anything but CondApplied
// is an error.
func recordLaunchIdentity(s *store.Store, id string) error {
	sp, err := s.GetSpawn(id)
	if err != nil {
		return err
	}
	res, err := s.RecordLaunchIdentity(id, sp.RowVersion, sp.Identity.Token, store.LaunchIdentity{
		ServerPID: 4101, ServerStart: 1700000000, ServerStarttime: "5101",
		PaneID: "%41", PanePID: 4102, PaneStarttime: "5102",
	})
	if err == nil && res != store.CondApplied {
		err = fmt.Errorf("RecordLaunchIdentity(%q) = %v, want CondApplied", id, res)
	}
	return err
}

// agentHook fires event at id's row as its own agent (storefix.FireHook from
// the row's pane process); a hook the gate does not apply is an error.
func agentHook(s *store.Store, id, event, sessionID string) error {
	pid, start, err := storefix.AgentHookParent(s, id)
	if err != nil {
		return err
	}
	applied, err := storefix.FireHook(s, id, event, sessionID, pid, start)
	if err == nil && !applied.Applied {
		err = fmt.Errorf("%s on %q not applied (reason %q)", event, id, applied.Reason)
	}
	return err
}

// observeColumns returns every stored column of the spawns row, raw.
func observeColumns(t *testing.T, _ *store.Store, dbPath, id string) any {
	t.Helper()
	c, err := apitest.ReadSpawnColumns(dbPath, id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns(%q): %v", id, err)
	}
	return c
}

// identityColumns names the six server and pane identity columns of c.
func identityColumns(c apitest.SpawnColumns) map[string]any {
	return map[string]any{
		"tmux_server_pid": c.TmuxServerPID, "tmux_server_started": c.TmuxServerStarted,
		"tmux_server_starttime": c.TmuxServerStarttime, "pane_id": c.PaneID,
		"pane_pid": c.PanePID, "pane_starttime": c.PaneStarttime,
	}
}

// seedCursorWorking seeds id as a working Cursor row with a launch and a
// gateway (its pane process) and no runtime registration.
func seedCursorWorking(t *testing.T, _ *store.Store, dbPath, id string) {
	t.Helper()
	launch := store.LaunchIdentity{Token: "0123456789abcdef", PanePID: apitest.TestPanePID, PaneStarttime: apitest.LinuxProcStarttime}
	if _, err := apitest.SeedSpawn(dbPath, id, store.StateWorking, "", "", "", false,
		apitest.WithAgent(store.AgentCursor), apitest.WithLaunchIdentity(launch)); err != nil {
		t.Fatalf("SeedSpawn(%q, cursor working): %v", id, err)
	}
}

// markCursorMissing runs the Cursor reconciliation's mark of a lost gateway
// with no runtime registration (1 s deadline); anything but CondApplied is an
// error.
func markCursorMissing(s *store.Store, id string) error {
	row, err := s.GetSpawn(id)
	if err != nil {
		return err
	}
	b := store.CursorRuntime{InstanceID: id, Token: row.Identity.Token, Life: row.LifeNumber, GatewayPID: row.Identity.PanePID, GatewayStart: row.Identity.PaneStarttime, SessionID: row.SessionID}
	_, res, err := s.MarkCursorMissingIfSameLaunch(row, b)
	if err == nil && res != store.CondApplied {
		err = fmt.Errorf("MarkCursorMissingIfSameLaunch(%q) = %v, want CondApplied", id, res)
	}
	return err
}

// observeSpawn returns the whole spawns row once the store's operation slot
// is free (GetSpawnWithin waits for it): a write its deadline cut short may
// still hold it briefly, and the next zero-wait write would be refused.
func observeSpawn(t *testing.T, s *store.Store, _, id string) any {
	t.Helper()
	sp, err := s.GetSpawnWithin(id, 10*time.Second)
	if err != nil {
		t.Fatalf("GetSpawnWithin(%q): %v", id, err)
	}
	return sp
}

var proxyWrites = []proxyWrite{
	{
		name: "commit permission observation", kind: storefix.WriteFailDisplayObservation,
		// A retained candidate is what this read must clear; a row with no
		// observation and no eligible request reads without writing.
		seed: func(t *testing.T, _ *store.Store, dbPath, id string) {
			t.Helper()
			candidate := `{"state":"working","candidate":"needs_intelligence","candidate_since_ms":1,"observed_at_ms":1}`
			if _, err := apitest.SeedSpawn(dbPath, id, store.StateWaiting, "", "", "", false, apitest.WithDisplayObservation(candidate)); err != nil {
				t.Fatalf("SeedSpawn(%q): %v", id, err)
			}
		},
		write: func(s *store.Store, id string) error {
			row, err := s.GetSpawn(id)
			if err != nil {
				return err
			}
			_, _, err = s.ObserveNeedsIntelligence(row, nil, time.Now(), store.SettleRead{})
			return err
		},
		observe: func(t *testing.T, s *store.Store, _, id string) any {
			t.Helper()
			row, err := s.GetSpawn(id)
			if err != nil {
				t.Fatal(err)
			}
			return row.ObservationVersion
		},
	},

	{
		name:     "SessionStart rotation archive",
		kind:     storefix.WriteFailReuseArchive,
		failOpen: true,
		seed:     seedState(store.StateWorking, "sess-old"),
		write: func(s *store.Store, id string) error {
			sp, err := s.GetSpawn(id) // rotate to a new id every call: a blocked call still records its id
			if err != nil {
				return err
			}
			return agentHook(s, id, "SessionStart", sp.SessionID+"-next")
		},
		observe: func(t *testing.T, _ *store.Store, dbPath, id string) any {
			t.Helper()
			h, err := apitest.ReadSessionHistoryAllLives(dbPath, id)
			if err != nil {
				t.Fatalf("ReadSessionHistoryAllLives(%q): %v", id, err)
			}
			return h
		},
	},
	{
		// Resume's move to pending: a finished row takes no hook (it records
		// no pane, SR-22.9), so no hook write moves it to pending.
		name: "resume move to pending",
		kind: storefix.WriteFailReuseReset,
		seed: seedState(store.StateEnded, ""),
		write: func(s *store.Store, id string) error {
			sp, err := s.GetSpawn(id)
			if err != nil {
				return err
			}
			res, _, err := s.MoveToPending(id, sp.Snapshot, 1700000000000, "0123456789abcdef", apitest.TestSocket, "", store.LaunchOwner{})
			if err == nil && res != store.CondApplied {
				err = fmt.Errorf("MoveToPending(%q) = %v, want CondApplied", id, res)
			}
			return err
		},
		observe: observeSpawn,
	},
	{
		name: "spawn delete cascade",
		kind: storefix.WriteFailReusePermissionDelete,
		seed: func(t *testing.T, s *store.Store, _, id string) { storefix.SeedCheckPermission(t, s, id) },
		write: func(s *store.Store, id string) error {
			return s.DeleteSpawn(id) // cascades to the id's permission request
		},
		observe: observeOpenRequests,
	},
	{
		name: "SessionEnd ended transition",
		kind: storefix.WriteFailReuseRestore,
		seed: seedState(store.StatePending, ""),
		write: func(s *store.Store, id string) error {
			return agentHook(s, id, "SessionEnd", "")
		},
		observe: observeSpawn,
	},
	{
		// The raw row: a blocked end write leaves it pending at version 0 with
		// its launch start and no ended_at (SR-5.8).
		name:    "held launch end write",
		kind:    storefix.WriteFailReuseRestore,
		seed:    seedHeldLaunch,
		write:   endHeldLaunch,
		observe: observeColumns,
	},
	{
		// The raw row: a blocked write leaves the identity columns NULL and
		// row_version where it was.
		name:    "launch identity write",
		kind:    storefix.WriteFailLaunchIdentity,
		seed:    seedLaunchPending,
		write:   recordLaunchIdentity,
		observe: observeColumns,
	},
	{
		name: "decide an open permission request",
		kind: storefix.WriteFailPermissionDecision,
		seed: func(t *testing.T, s *store.Store, _, id string) { storefix.SeedCheckPermission(t, s, id) },
		write: func(s *store.Store, id string) error {
			ok, err := s.DecidePermissionRequest(id, storefix.TestRequestTokenA, "allow", "", store.WriterProcessDecide)
			if err == nil && !ok {
				err = fmt.Errorf("DecidePermissionRequest(%q) not updated", id)
			}
			return err
		},
		observe: observeOpenRequests,
	},
	{
		// The row holds only an entry: its delete cascades to the entry (no
		// permission request, so ReusePermissionDelete does not match).
		name: "spawn delete cascade to a subagent entry",
		kind: storefix.WriteFailSubagentRecord,
		seed: func(t *testing.T, _ *store.Store, dbPath, id string) {
			t.Helper()
			seedState(store.StateEnded, "")(t, nil, dbPath, id)
			if err := apitest.SeedSubagent(dbPath, id, "sa-wf"); err != nil {
				t.Fatalf("SeedSubagent(%q): %v", id, err)
			}
		},
		write:   func(s *store.Store, id string) error { return s.DeleteSpawn(id) },
		observe: observeSubagentRows,
	},
	{
		// The row holds only a claimable launch.
		name: "spawn delete cascade to a claimable launch",
		kind: storefix.WriteFailSubagentRecord,
		seed: func(t *testing.T, _ *store.Store, dbPath, id string) {
			t.Helper()
			seedState(store.StateEnded, "")(t, nil, dbPath, id)
			if err := apitest.SeedSubagentLaunch(dbPath, id, "toolu-wf"); err != nil {
				t.Fatalf("SeedSubagentLaunch(%q): %v", id, err)
			}
		},
		write:   func(s *store.Store, id string) error { return s.DeleteSpawn(id) },
		observe: observeSubagentRows,
	},
	{
		// The only proxy write that moves a row to missing, and it has a
		// deadline: a matched write with none would spin far longer. No other
		// kind can match this working-to-missing mark (the row has no request
		// or subagent record), so it runs only under its own: fewer marks that
		// must land within their 1 s, zero-wait budget.
		name:        "Cursor lost gateway mark",
		kind:        storefix.WriteFailMissingDeadlineOverrun,
		ownKindOnly: true,
		seed:        seedCursorWorking,
		write:       markCursorMissing,
		observe:     observeSpawn,
	},
}

// inputProxyWrites exercises the trigger's exact SQL boundary against the real
// schema. The records carry only the identity/phase fields the triggers inspect;
// the API/store/migration consumers cover complete validated records and rollback.
var inputProxyWrites = []struct {
	name, sql string
	kind      storefix.WriteFailureKind
	also      []storefix.WriteFailureKind
}{
	{"input initial attempt insert", `INSERT INTO input_attempts(store_id,launch_token,request_id,revision,record)
 SELECT store_id,launch_token,'new',1,json_set(record,'$.request_id','new') FROM input_attempts WHERE launch_token=? AND request_id='seed'`, storefix.WriteFailInputAttemptInsert, nil},
	{"input positive-byte upsert", `INSERT INTO input_attempts(store_id,launch_token,request_id,revision,record)
 SELECT store_id,launch_token,request_id,revision+1,json_set(record,'$.phase','text_attempted','$.bytes_attempted',1) FROM input_attempts WHERE launch_token=? AND request_id='seed'
 ON CONFLICT(store_id,launch_token,request_id) DO UPDATE SET revision=excluded.revision,record=excluded.record`, storefix.WriteFailInputTextIntent, []storefix.WriteFailureKind{storefix.WriteFailInputAttemptInsert}},
	{"input zero-byte Enter upsert", `INSERT INTO input_attempts(store_id,launch_token,request_id,revision,record)
 SELECT store_id,launch_token,request_id,revision+1,json_set(record,'$.phase','enter_attempted','$.enter_attempted',1) FROM input_attempts WHERE launch_token=? AND request_id='seed'
 ON CONFLICT(store_id,launch_token,request_id) DO UPDATE SET revision=excluded.revision,record=excluded.record`, storefix.WriteFailInputAttemptInsert, nil},
	{"input direct attempt update", `UPDATE input_attempts SET revision=revision+1,record=json_set(record,'$.bytes_attempted',1) WHERE launch_token=?`, 0, nil},
	{"input current update", `UPDATE input_launches SET revision=revision+1 WHERE launch_token=?`, storefix.WriteFailInputCurrent, nil},
	{"input launch insert", `INSERT INTO input_launches(store_id,launch_token,revision,record)
 SELECT store_id,launch_token||'-new',1,record FROM input_launches WHERE launch_token=?`, 0, nil},
}

func rawFailureDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func inputFailureProxies() []proxyWrite {
	var out []proxyWrite
	for _, test := range inputProxyWrites {
		test := test
		out = append(out, proxyWrite{
			name: test.name, kind: test.kind, alsoBlocked: test.also,
			seed: func(t *testing.T, s *store.Store, path, id string) {
				db := rawFailureDB(t, path)
				if _, err := db.Exec(`INSERT INTO input_launches(store_id,launch_token,revision,record)
 VALUES(?,?,1,json_object('Identity',json_object('InstanceID',?)))`, s.StoreID(), id, id); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO input_attempts(store_id,launch_token,request_id,revision,record)
 VALUES(?,?,'seed',1,json_object('identity',json_object('InstanceID',?),'phase','attempt_started','bytes_attempted',0))`, s.StoreID(), id, id); err != nil {
					t.Fatal(err)
				}
			},
			writeRaw: func(path, id string) error {
				db, err := sql.Open("sqlite", path)
				if err != nil {
					return err
				}
				defer db.Close()
				_, err = db.Exec(test.sql, id)
				return err
			},
			observe: func(t *testing.T, _ *store.Store, path, id string) any {
				db := rawFailureDB(t, path)
				var launches, attempts string
				if err := db.QueryRow(`SELECT json_group_array(json_array(launch_token,revision,record)) FROM input_launches WHERE json_extract(record,'$.Identity.InstanceID')=?`, id).Scan(&launches); err != nil {
					t.Fatal(err)
				}
				if err := db.QueryRow(`SELECT json_group_array(json_array(request_id,revision,record)) FROM input_attempts WHERE launch_token=?`, id).Scan(&attempts); err != nil {
					t.Fatal(err)
				}
				return []string{launches, attempts}
			},
		})
	}
	for _, update := range []bool{false, true} {
		kind := storefix.WriteFailInputAuthority
		query := `INSERT INTO store_meta(key,value) VALUES(?,'fixture') ON CONFLICT(key) DO UPDATE SET value=value||'-next'`
		name := "input authority insert"
		if update {
			kind, name = 0, "input authority direct update"
			query = `UPDATE store_meta SET value=value||'-next' WHERE key=?`
		}
		out = append(out, proxyWrite{
			name: name, kind: kind, target: "native_input_authority",
			seed: func(t *testing.T, _ *store.Store, path, id string) {
				if _, err := rawFailureDB(t, path).Exec(`INSERT OR IGNORE INTO store_meta(key,value) VALUES(?,'fixture')`, id); err != nil {
					t.Fatal(err)
				}
			},
			writeRaw: func(path, id string) error {
				db, err := sql.Open("sqlite", path)
				if err != nil {
					return err
				}
				defer db.Close()
				_, err = db.Exec(query, id)
				return err
			},
			observe: func(t *testing.T, _ *store.Store, path, id string) any {
				var value string
				if err := rawFailureDB(t, path).QueryRow(`SELECT value FROM store_meta WHERE key=?`, id).Scan(&value); err != nil {
					t.Fatal(err)
				}
				return value
			},
		})
	}
	return out
}

// observeSubagentRows returns the id's subagent entries and claimable launches, raw.
func observeSubagentRows(t *testing.T, _ *store.Store, dbPath, id string) any {
	t.Helper()
	rows, err := apitest.ReadSubagentRows(dbPath, id)
	if err != nil {
		t.Fatalf("ReadSubagentRows(%q): %v", id, err)
	}
	return rows
}

// observeOpenRequests returns the id's open permission requests.
func observeOpenRequests(t *testing.T, s *store.Store, _, id string) any {
	t.Helper()
	rows, err := s.OpenPermissionRequestsForSpawn(id)
	if err != nil {
		t.Fatalf("OpenPermissionRequestsForSpawn(%q): %v", id, err)
	}
	return rows
}

// runWrite runs w on id and asserts it was blocked by the injected blockedBy
// failure (for the overrun kind, by the write's own deadline), or took effect
// when blockedBy is 0 (not a kind).
func runWrite(t *testing.T, s *store.Store, dbPath string, w proxyWrite, id string, blockedBy storefix.WriteFailureKind) {
	t.Helper()
	before := w.observe(t, s, dbPath, id)
	var err error
	if w.writeRaw != nil {
		err = w.writeRaw(dbPath, id)
	} else {
		err = w.write(s, id)
	}
	changed := !reflect.DeepEqual(before, w.observe(t, s, dbPath, id))
	if blockedBy == 0 {
		if err != nil || !changed {
			t.Errorf("%s on %q: err=%v changed=%v, want success", w.name, id, err, changed)
		}
		return
	}
	if changed {
		t.Errorf("%s on %q changed its row despite the injected failure", w.name, id)
	}
	if w.failOpen {
		if err != nil {
			t.Errorf("%s on %q: err=%v, want nil (fail-open)", w.name, id, err)
		}
		return
	}
	if blockedBy == storefix.WriteFailMissingDeadlineOverrun {
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("%s on %q: err=%v, want its deadline's error (context.DeadlineExceeded)", w.name, id, err)
		}
		return
	}
	if want := "injected write failure: " + blockedBy.String(); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("%s on %q: err=%v, want it to contain %q", w.name, id, err, want)
	}
}

// TestInjectWriteFailure installs each kind on one id and runs every proxy
// write (an ownKindOnly one under its kind alone): only the kind's own writes
// on that id fail, and cleanup lifts them.
func TestInjectWriteFailure(t *testing.T) {
	const target, other = "wf-target", "wf-other"
	writes := append(slices.Clone(proxyWrites), inputFailureProxies()...)
	covered := map[storefix.WriteFailureKind]bool{}
	for _, w := range writes {
		covered[w.kind] = true
	}
	for _, k := range writefailfix.Kinds() {
		if !covered[k] {
			t.Fatalf("no proxy write for kind %v", k)
		}
	}
	for _, installed := range writefailfix.Kinds() {
		for _, w := range writes {
			if w.ownKindOnly && installed != w.kind {
				continue
			}
			t.Run(fmt.Sprintf("%v/%s", installed, w.name), func(t *testing.T) {
				targetID := target
				if w.target != "" {
					targetID = w.target
				}
				s, dbPath := storefix.OpenTempStore(t)
				w.seed(t, s, dbPath, targetID)
				w.seed(t, s, dbPath, other)
				matching := installed == w.kind || slices.Contains(w.alsoBlocked, installed)

				t.Run("installed", func(t *testing.T) {
					installTarget := targetID
					if installed == storefix.WriteFailInputAuthority {
						installTarget = "native_input_authority"
					}
					remove := storefix.InjectWriteFailure(t, dbPath, installed, installTarget)
					var blockedBy storefix.WriteFailureKind
					if matching {
						blockedBy = installed
					}
					runWrite(t, s, dbPath, w, targetID, blockedBy)
					runWrite(t, s, dbPath, w, other, 0)
					// One overlapping handle must not release the first. Explicit
					// removal is also safe when its registered cleanup runs later.
					if matching && installed >= storefix.WriteFailInputAttemptInsert {
						removeSecond := storefix.InjectWriteFailure(t, dbPath, installed, installTarget)
						removeSecond()
						removeSecond()
						runWrite(t, s, dbPath, w, targetID, blockedBy)
						remove()
					}
				})

				if matching {
					runWrite(t, s, dbPath, w, targetID, 0) // cleanup or explicit removal lifted the failure
				}
			})
		}
	}
}
