package api_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/codexfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/contextfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// Compare independent copies of the same committed seed: reads write, and
// separately seeded rows would have different identities and timestamps.
func TestGetManyEqualsSequentialGet(t *testing.T) {
	t.Parallel()
	e := newPaneEnv(t, paneSpec{agent: store.AgentClaude})
	ids := []string{e.id}
	writer, err := apitest.ContextWriter(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{store.AgentClaude, store.AgentCodex, store.AgentCursor} {
		for _, state := range []string{store.StatePending, store.StateWaiting, store.StateWorking, store.StateAskUser, store.StateCheckPermission, store.StateEnded, store.StateMissing} {
			if agent == store.AgentCursor && state == store.StateAskUser {
				continue
			} // Cursor has no answerable ask_user state.
			id := agent + "-" + state
			_, err := apitest.SeedSpawn(e.dbPath, id, state, "/tmp", "off", "", false, apitest.WithAgent(agent), apitest.WithNoPane(), apitest.WithPID(writer.PanePID), apitest.WithProcStarttime(writer.PaneStarttime))
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
	}
	_, err = apitest.SeedSpawn(e.dbPath, "droid-pending", store.StatePending, "/tmp", "off", "", false, apitest.WithAgent(store.AgentDroid), apitest.WithNoPane(), apitest.WithPID(writer.PanePID), apitest.WithProcStarttime(writer.PaneStarttime))
	if err != nil {
		t.Fatal(err)
	}
	ids = append(ids, "droid-pending")
	sample := contextfix.Must(t, "claude-parent")
	known, _ := apitest.SeedContextSpawn(t, e.dbPath, "known-context", false, sample)
	ids = append(ids, known)
	path := codexfix.TempRolloutPath(t, codexfix.ErroredTurn.SessionID, true, codexfix.ErroredTurn.Ended())
	seedCodexTurn(t, e.s, e.dbPath, "turn-end", path, "claude-waiting", apitest.WithNoPane(), apitest.WithPID(writer.PanePID), apitest.WithProcStarttime(writer.PaneStarttime))
	ids = append(ids, "turn-end")
	// The known-gone pending gateway is a nondeferred terminal write.
	_, err = apitest.SeedSpawn(e.dbPath, "cursor-gone", store.StatePending, "/tmp", "off", "", false, apitest.WithAgent(store.AgentCursor), apitest.WithLaunchIdentity(store.LaunchIdentity{Token: "b1b1b1b1b1b1b1b1", PanePID: 987654, PaneStarttime: apitest.LinuxProcStarttime}))
	if err != nil {
		t.Fatal(err)
	}
	ids = append(ids, "cursor-gone")
	storefix.RegisterStorePath(t, e.s, e.dbPath)
	if _, err := apitest.SeedPermissionRequest(e.dbPath, "claude-check_permission", "Read", apitest.PermissionToken(storefix.TestRequestTokenA), apitest.PermissionTimes(e.now.Add(-48*time.Hour), time.Time{}, time.Time{}, time.Time{}, time.Time{}, "")); err != nil {
		t.Fatal(err)
	}

	ids = append(ids, "no-such-row")
	// Closing the sole open connection checkpoints WAL before the byte copy.
	if err := e.s.Close(); err != nil {
		t.Fatal(err)
	}
	seed, err := os.ReadFile(e.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// Every attempt on either side reads a fresh copy of the seed, so a
	// repeated attempt cannot count a write twice.
	type manySide struct {
		db       string
		client   *api.Client
		store    *store.Store
		recorder *tmuxfix.Recorder
	}
	newSide := func() manySide {
		db := filepath.Join(t.TempDir(), "state.db")
		if err := os.WriteFile(db, seed, 0600); err != nil {
			t.Fatal(err)
		}
		rec, pc := tmuxfix.NewRecorderForReadPane(t, db, e.id, "pane 1")
		pc.Set(writer.PanePID, procfix.Alive(writer.PaneStarttime))
		// Ordinary seed processes remain alive; only the pending gateway is gone.
		gone, err := openDB(t, db).GetSpawn("cursor-gone")
		if err != nil {
			t.Fatal(err)
		}
		// Give the gateway its own verdict only for this row; ordinary matrix
		// rows have no pane and the transcript writer uses the real test PID.
		pc.Set(gone.Identity.PanePID, procfix.Gone())
		cfg := filepath.Join(t.TempDir(), "config.toml")
		writeConfig(t, cfg, "[get_many]\nid_cap = 40\n")
		c, err := api.New(api.Options{StorePath: db, ConfigPath: cfg, TmuxClient: rec})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		api.SetClockForTest(c, func() time.Time { return e.now })
		api.SetProcCheckerForTest(c, pc)
		return manySide{db, c, openDB(t, db), rec}
	}
	before := map[string]store.Spawn{}
	base := newSide()
	for _, id := range ids[:len(ids)-1] {
		before[id], err = base.store.GetSpawn(id)
		if err != nil {
			t.Fatal(err)
		}
	}
	// uncommitted names the first write the comparisons below need that a
	// read pass did not commit: each is nonwaiting and best effort. Any such
	// write that does not commit (process evidence, display observation,
	// state transition) leaves its field's latest attempt with one of these
	// reasons; the fallback clock and the exactly-once counts are checked on
	// the stored rows.
	lostWrite := []string{store.ReasonBusy, store.ReasonDeadline, store.ReasonCommitUnconfirmed}
	uncommitted := func(s *store.Store, rows []api.SpawnRow) string {
		for _, row := range rows {
			for _, f := range []struct {
				name string
				c    api.Confirmation
			}{{"state", row.Confirmations.State}, {"process_liveness", row.Confirmations.ProcessLiveness}, {"displayed_state", row.Confirmations.DisplayedState}} {
				if f.c.Reason != nil && slices.Contains(lostWrite, *f.c.Reason) {
					return row.InstanceID + " " + f.name + " write (" + *f.c.Reason + ")"
				}
			}
		}
		for _, id := range []string{e.id, "turn-end", "cursor-gone"} {
			row, err := s.GetSpawn(id)
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case id == e.id && (row.ObservationVersion != before[id].ObservationVersion+1 || row.PaneSHA256 == ""):
				return "pane observation"
			case id == "turn-end" && (row.State != store.StateWaiting || row.RowVersion != before[id].RowVersion+1):
				return "turn-end transition"
			case id == "cursor-gone" && (row.State != store.StateMissing || row.RowVersion != before[id].RowVersion+1):
				return "cursor-gone transition"
			}
		}
		pr, err := s.PermissionRequestsForSpawn("claude-check_permission")
		if err != nil {
			t.Fatal(err)
		}
		if len(pr) != 1 || !pr[0].HookGoneAt.Equal(e.now) {
			return "fallback clock"
		}
		return ""
	}
	var windows map[string]manyReadWindow
	var want, got api.GetManyResult
	var batchWindow manyReadWindow
	passes := [2]func(c *api.Client) ([]api.SpawnRow, error){
		func(c *api.Client) ([]api.SpawnRow, error) {
			windows = map[string]manyReadWindow{}
			want = api.GetManyResult{Spawns: []api.SpawnRow{}, NotFound: []string{}}
			for _, id := range ids {
				from := time.Now()
				row, err := c.Get(id)
				windows[id] = manyReadWindow{from, time.Now()}
				if errors.Is(err, api.ErrSpawnNotFound) {
					want.NotFound = append(want.NotFound, id)
					continue
				}
				if err != nil {
					return nil, fmt.Errorf("Get(%s): %w", id, err)
				}
				want.Spawns = append(want.Spawns, row)
			}
			return want.Spawns, nil
		},
		func(c *api.Client) ([]api.SpawnRow, error) {
			from := time.Now()
			var err error
			got, err = c.GetMany(ids)
			batchWindow = manyReadWindow{from, time.Now()}
			return got.Spawns, err
		},
	}
	// b.vkr: each side's whole read pass repeats at the same injected clock
	// until every needed write commits. Its first attempt runs under a held
	// write lock, which skips them all, and its second skips only the pane
	// observation, so the bound is always exercised on both.
	var sides [2]manySide
	for i, pass := range passes {
		var missed string
		attempt := 0
		attempts, elapsed, committed := apitest.RepeatUntilCommitted(func() bool {
			attempt++
			side := newSide()
			release := func() {}
			switch attempt {
			case 1:
				release = apitest.HoldWriteLock(t, side.db, time.Minute)
			case 2:
				storefix.InjectWriteFailure(t, side.db, storefix.WriteFailDisplayObservation, e.id)
			}
			rows, err := pass(side.client)
			release()
			if errors.Is(err, api.ErrStoreBusy) {
				missed = err.Error()
				return false
			}
			if err != nil {
				t.Fatalf("side %d: %v", i, err)
			}
			missed = uncommitted(side.store, rows)
			if attempt <= 2 && missed == "" {
				t.Fatalf("side %d attempt %d committed despite its skipped write", i, attempt)
			}
			sides[i] = side
			return missed == ""
		})
		if !committed {
			t.Fatalf("side %d: %s never committed (%d attempts, %s)", i, missed, attempts, elapsed)
		}
	}
	for _, row := range want.Spawns {
		switch row.InstanceID {
		case "droid-pending":
			if row.IntegrationMode != "pane_only" {
				t.Fatalf("Droid seed is not pane-only: %+v", row)
			}
		case known:
			assertContext(t, row.Context, sample.Expected)
		case "claude-waiting":
			assertContext(t, row.Context, []byte(contextfix.UnknownJSON))
		}
	}
	if len(got.Spawns) != len(want.Spawns) || !reflect.DeepEqual(got.NotFound, want.NotFound) {
		t.Fatalf("found lengths %d/%d; not_found %v/%v", len(got.Spawns), len(want.Spawns), got.NotFound, want.NotFound)
	}
	for i, row := range got.Spawns {
		var fields []string
		if row.InstanceID == "turn-end" {
			fields = []string{"state_changed_at", "confirmations.state.confirmed_at", "confirmations.state.checked_at"}
		}
		normalized := maskManySQLFields(t, want.Spawns[i], row, fields, [2]manyReadWindow{windows[row.InstanceID], batchWindow})
		if normalized[0] != normalized[1] {
			t.Errorf("%s differs from sequential Get\ngot: %s\nwant: %s", row.InstanceID, jsonOf(t, row), jsonOf(t, want.Spawns[i]))
		}
	}
	for _, id := range ids[:len(ids)-1] {
		a, err := sides[0].store.GetSpawn(id)
		if err != nil {
			t.Fatal(err)
		}
		b, err := sides[1].store.GetSpawn(id)
		if err != nil {
			t.Fatal(err)
		}
		if id == "turn-end" || id == "cursor-gone" {
			bracket := [2]manyReadWindow{windows[id], batchWindow}
			var ae, be map[string]any
			if err := json.Unmarshal([]byte(a.StateEvidence), &ae); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(b.StateEvidence), &be); err != nil {
				t.Fatal(err)
			}
			masked := maskManySQLFields(t, ae, be, []string{"at", "checked"}, bracket)
			a.StateEvidence, b.StateEvidence = masked[0], masked[1]
			fields := []string{"StateChangedMillis"}
			if id == "cursor-gone" {
				fields = append(fields, "EndedAt", "EndedAtText", "LastSeenAt")
				for _, row := range []store.Spawn{a, b} {
					if row.EndedAt == nil || !row.EndedAt.Equal(row.LastSeenAt) || row.EndedAtText != row.EndedAt.UTC().Format("2006-01-02 15:04:05") {
						t.Fatalf("%s terminal timestamps disagree: %+v", id, row)
					}
				}
			}
			masked = maskManySQLFields(t, a, b, fields, bracket)
			if masked[0] != masked[1] {
				t.Errorf("%s stored rows differ after SQL-clock checks\nget: %s\nget-many: %s", id, masked[0], masked[1])
			}
		} else if !reflect.DeepEqual(a, b) {
			t.Errorf("%s stored rows differ\n%+v\n%+v", id, a, b)
		}
		pa, err := sides[0].store.PermissionRequestsForSpawn(id)
		if err != nil {
			t.Fatal(err)
		}
		pb, err := sides[1].store.PermissionRequestsForSpawn(id)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(pa, pb) {
			t.Errorf("%s stored requests differ: %+v / %+v", id, pa, pb)
		}
	}
	if !reflect.DeepEqual(sides[0].recorder.SocketCalls(), sides[1].recorder.SocketCalls()) || !reflect.DeepEqual(sides[0].recorder.Calls(), sides[1].recorder.Calls()) {
		t.Fatal("tmux calls differ")
	}
	if len(sides[0].recorder.SocketCalls()) == 0 {
		t.Fatal("fixture made no tmux observation")
	}
}

// The lead's revised equivalence method masks only SQL-clock values. Turn end
// stamps public state_changed_at and confirmations.state.{confirmed_at,checked_at}.
// Turn end and Cursor loss stamp stored StateChangedMillis and StateEvidence's
// at/checked (settle.go nowMsSQL and confirmations.go stateEvidenceSet).
// Cursor loss also stamps stored EndedAt (and its raw EndedAtText) and LastSeenAt
// with whole-second CURRENT_TIMESTAMP (find_missing_writes.go markMissingSetSQL).
// Every listed field must be present and non-null on BOTH sides and fall in
// that side's wall-clock bracket at the SQL clock's precision before replacement.
// All other bytes stay exact.
type manyReadWindow struct{ from, to time.Time }

func maskManySQLFields(t *testing.T, a, b any, fields []string, windows [2]manyReadWindow) [2]string {
	t.Helper()
	var out [2]string
	for side, value := range []any{a, b} {
		raw := jsonOf(t, value)
		if len(fields) == 0 {
			out[side] = raw
			continue
		}
		var doc map[string]any
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&doc); err != nil {
			t.Fatal(err)
		}
		for _, field := range fields {
			path := strings.Split(field, ".")
			obj := doc
			for _, key := range path[:len(path)-1] {
				nested, ok := obj[key].(map[string]any)
				if !ok {
					t.Fatalf("side %d: missing object in %s", side, field)
				}
				obj = nested
			}
			key := path[len(path)-1]
			v, ok := obj[key]
			if !ok || v == nil {
				t.Fatalf("side %d: %s absent/null", side, field)
			}
			wholeSeconds := field == "EndedAt" || field == "EndedAtText" || field == "LastSeenAt"
			var at time.Time
			switch x := v.(type) {
			case string:
				layout := time.RFC3339Nano
				if field == "EndedAtText" {
					layout = "2006-01-02 15:04:05"
				}
				var err error
				at, err = time.Parse(layout, x)
				if err != nil {
					t.Fatal(err)
				}
			case json.Number:
				ms, err := x.Int64()
				if err != nil {
					t.Fatal(err)
				}
				at = time.UnixMilli(ms)
			default:
				t.Fatalf("%s is not a timestamp: %T", field, v)
			}
			// SQLite rounds to milliseconds, so include the rounding boundary.
			lower, upper := windows[side].from.Truncate(time.Millisecond), windows[side].to.Add(time.Millisecond)
			if wholeSeconds {
				// CURRENT_TIMESTAMP truncates to seconds, unlike nowMsSQL.
				lower, upper = windows[side].from.Truncate(time.Second), windows[side].to.Truncate(time.Second)
				if at.Nanosecond() != 0 {
					t.Fatalf("side %d: %s=%s is not a whole-second SQL timestamp", side, field, at)
				}
			}
			if at.Before(lower) || at.After(upper) {
				t.Fatalf("side %d: %s=%s outside [%s,%s]", side, field, at, windows[side].from, windows[side].to)
			}
			obj[key] = "<SQL-clock>"
		}
		out[side] = jsonOf(t, doc)
	}
	return out
}
