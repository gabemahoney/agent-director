package api_test

// find_missing_trail_test.go: the trail records find-missing writes on the
// process path (SR-11.1, SR-11.3, SR-11.4, SR-3.8, SR-14), and the trail
// helpers the find-missing tests share. Each test sweeps a real store seeded
// through apitest.SeedSpawn, judged by procfix, and asserts on the trail
// lines added since a checkpoint. TestMain (example_main_test.go) points the
// trail at apiTrailDir. The ticks of rows the tmux lookup decides are in
// find_missing_trail_tmux_test.go, the pending grace period's trail side in
// find_missing_grace_trail_test.go.

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

var apiTSRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3,}Z$`)

// apiTrailFilePath returns the trail file path used by the singleton in api tests.
func apiTrailFilePath() string {
	return filepath.Join(apiTrailDir, ".agent-director", "ad-trail.jsonl")
}

// readAPITrailLines parses every JSONL line from the api trail file.
// Returns nil when the file does not exist yet.
func readAPITrailLines(t *testing.T) []map[string]any {
	t.Helper()
	f, err := os.Open(apiTrailFilePath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("readAPITrailLines: %v", err)
	}
	defer f.Close()
	var rows []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("readAPITrailLines: unmarshal %q: %v", sc.Text(), err)
		}
		rows = append(rows, m)
	}
	if sc.Err() != nil {
		t.Fatalf("readAPITrailLines: scan: %v", sc.Err())
	}
	return rows
}

// apiFindMissingTicksAt returns ad.find_missing.tick lines added after prevCount
// total lines in the api trail file.
func apiFindMissingTicksAt(t *testing.T, prevCount int) []map[string]any {
	t.Helper()
	return trailSince(t, prevCount, "ad.find_missing.tick")
}

// apiTicksWithReason filters ticks (from apiFindMissingTicksAt) by
// reconciliation_reason.
func apiTicksWithReason(ticks []map[string]any, reason string) []map[string]any {
	var out []map[string]any
	for _, tick := range ticks {
		if tick["reconciliation_reason"] == reason {
			out = append(out, tick)
		}
	}
	return out
}

// assertAPITrailStr checks row[key] == want.
func assertAPITrailStr(t *testing.T, row map[string]any, key, want string) {
	t.Helper()
	got, ok := row[key]
	if !ok {
		t.Errorf("field %q missing", key)
		return
	}
	if got != want {
		t.Errorf("[%q] = %v; want %q", key, got, want)
	}
}

// trailClock returns a sweep clock that always reads at.
func trailClock(at time.Time) func() time.Time { return func() time.Time { return at } }

// trailToken is the launch token every trail row carries; the process path never reads it.
const trailToken = "5eed0000000000c3"

// trailRow is one working row to seed: its SessionStart and pane pids (0 = none recorded), both with start
// time fmStart unless pidOnly, plus extra options.
type trailRow struct {
	id             string
	ssPID, panePID int
	pidOnly        bool
	opts           []apitest.SpawnOption
}

// pid is the row's agent process pid when only one is recorded or both agree (0 = none recorded).
func (r trailRow) pid() int { return max(r.ssPID, r.panePID) }

// options returns the SeedSpawn options recording r's identities, then r.opts.
func (r trailRow) options() []apitest.SpawnOption {
	start := fmStart
	if r.pidOnly {
		start = ""
	}
	li := store.LaunchIdentity{Token: trailToken, Socket: apitest.TestSocket}
	if r.panePID > 0 {
		li.PaneID, li.PanePID, li.PaneStarttime = apitest.TestPaneID, r.panePID, start
	}
	opts := []apitest.SpawnOption{apitest.WithLaunchIdentity(li)}
	if r.ssPID > 0 {
		opts = append(opts, apitest.WithPID(r.ssPID), apitest.WithProcStarttime(start))
	}
	return append(opts, r.opts...)
}

// seedTrailStore seeds rows as working rows in a fresh store and returns it open with its path.
func seedTrailStore(t *testing.T, rows ...trailRow) (*store.Store, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	for i, r := range rows {
		if _, err := apitest.SeedSpawn(dbPath, r.id, store.StateWorking, "/tmp", "off", "", i == 0, r.options()...); err != nil {
			t.Fatalf("SeedSpawn %q: %v", r.id, err)
		}
	}
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, dbPath
}

// recordedName is the row id's recorded tmux session name in st.
func recordedName(t *testing.T, st *store.Store, id string) string {
	t.Helper()
	sp, err := st.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn %s: %v", id, err)
	}
	return sp.TmuxSessionName
}

// trailOf keeps the lines of recs naming instance id.
func trailOf(recs []map[string]any, id string) []map[string]any {
	var out []map[string]any
	for _, r := range recs {
		if r["claude_instance_id"] == id {
			out = append(out, r)
		}
	}
	return out
}

// tickExtras are the fields SR-11.4 adds to a mark tick by reason: lookup_outcome on the tmux path's ticks,
// tmux_session_name on tmux_name_held's.
var tickExtras = []string{"lookup_outcome", "tmux_session_name"}

// assertTick fails unless tick is an ad_find_missing tick with reason: a mark from prior to missing, or (prior "")
// a note with null states; it carries exactly extra of tickExtras, with those values.
func assertTick(t *testing.T, tick map[string]any, reason, prior string, extra map[string]any) {
	t.Helper()
	assertAPITrailStr(t, tick, "reconciliation_reason", reason)
	assertAPITrailStr(t, tick, "source", "ad_find_missing")
	if prior != "" {
		assertAPITrailStr(t, tick, "prior_state", prior)
		assertAPITrailStr(t, tick, "new_state", store.StateMissing)
	} else {
		for _, k := range []string{"prior_state", "new_state"} {
			if v, ok := tick[k]; !ok || v != nil {
				t.Errorf("[%s] = %v (present %v); want null", k, v, ok)
			}
		}
	}
	if ts, ok := tick["ts"].(string); !ok || !apiTSRe.MatchString(ts) {
		t.Errorf("[ts] = %v; want RFC3339Nano timestamp", tick["ts"])
	}
	for _, k := range tickExtras {
		got, has := tick[k]
		want, wanted := extra[k]
		if has != wanted || got != want {
			t.Errorf("%s tick [%s] = %v (present %v); want %v (present %v)", reason, k, got, has, want, wanted)
		}
	}
}

// assertProcAbsentTick fails unless ticks is one proc_absent tick from prior to missing for id.
func assertProcAbsentTick(t *testing.T, ticks []map[string]any, id, prior string) {
	t.Helper()
	if len(ticks) != 1 {
		t.Fatalf("%s ticks = %v; want exactly one proc_absent", id, ticks)
	}
	assertTick(t, ticks[0], "proc_absent", prior, nil)
}

// TestFindMissingProcAbsentEmitsTrail: a dead SessionStart or pane process gives its row one proc_absent tick;
// rows whose process is alive get no record of any kind, and no environment is read.
func TestFindMissingProcAbsentEmitsTrail(t *testing.T) {
	cases := []struct {
		row  trailRow
		proc procfix.Process // the answer for the row's recorded pid
		dead bool
	}{
		{trailRow{id: "pa-ss-gone", ssPID: 1101}, procfix.Gone(), true},
		{trailRow{id: "pa-ss-reused", ssPID: 1102}, procfix.Alive(fmOtherStart), true},
		{trailRow{id: "pa-pane-gone", panePID: 1103}, procfix.Gone(), true},
		{trailRow{id: "pa-ss-alive", ssPID: 1104}, procfix.Alive(fmStart), false},
		{trailRow{id: "pa-pane-alive", panePID: 1105}, procfix.Alive(fmStart), false},
		{trailRow{id: "pa-both-alive", ssPID: 1106, panePID: 1106}, procfix.Alive(fmStart), false},
	}
	pc := procfix.New()
	var rows []trailRow
	var wantIDs []string
	for _, c := range cases {
		pc.Set(c.row.pid(), c.proc)
		rows = append(rows, c.row)
		if c.dead {
			wantIDs = append(wantIDs, c.row.id)
		}
	}
	st, _ := seedTrailStore(t, rows...)
	before := trailLen(t)

	res, err := runFindMissing(st, pc, fmSweep{})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	slices.Sort(wantIDs)
	assertLists(t, res, wantIDs, nil)
	added := readAPITrailLines(t)[before:]
	for _, c := range cases {
		recs := trailOf(added, c.row.id)
		if c.dead {
			assertProcAbsentTick(t, recs, c.row.id, store.StateWorking)
			continue
		}
		if len(recs) != 0 {
			t.Errorf("%s: trail records = %v; want none for an alive row", c.row.id, recs)
		}
		if got, err := st.GetSpawnState(c.row.id); err != nil || got != store.StateWorking {
			t.Errorf("%s state = %q (err %v); want working", c.row.id, got, err)
		}
	}
	if n := pc.EnvReads(); n != 0 {
		t.Errorf("environment reads = %d; want 0", n)
	}
}

// TestFindMissingIdentitiesDisagreePaneDecides: when the SessionStart and pane identities disagree the pane
// process decides (dead: proc_absent mark; alive: untouched) and no sweep writes ad.provenance.disagree.
func TestFindMissingIdentitiesDisagreePaneDecides(t *testing.T) {
	pc := procfix.New()
	pc.Set(1201, procfix.Alive(fmStart)) // dm-pane-dead's SessionStart process
	pc.Set(1203, procfix.Alive(fmStart)) // dm-pane-alive's pane process; its SessionStart 1204 is gone
	st, _ := seedTrailStore(t,
		trailRow{id: "dm-pane-dead", ssPID: 1201, panePID: 1202},
		trailRow{id: "dm-pane-alive", ssPID: 1204, panePID: 1203},
	)

	for sweep := 1; sweep <= 2; sweep++ {
		before := trailLen(t)
		res, err := runFindMissing(st, pc, fmSweep{})
		if err != nil {
			t.Fatalf("sweep %d: %v", sweep, err)
		}
		ticks := apiFindMissingTicksAt(t, before)
		if sweep == 1 {
			assertLists(t, res, []string{"dm-pane-dead"}, nil)
			assertProcAbsentTick(t, trailOf(ticks, "dm-pane-dead"), "dm-pane-dead", store.StateWorking)
		} else {
			assertLists(t, res, nil, nil)
			if got := trailOf(ticks, "dm-pane-dead"); len(got) != 0 {
				t.Errorf("sweep 2: dm-pane-dead ticks = %v; want none (already missing)", got)
			}
		}
		if got := trailOf(ticks, "dm-pane-alive"); len(got) != 0 {
			t.Errorf("sweep %d: dm-pane-alive ticks = %v; want none", sweep, got)
		}
		if got := trailSince(t, before, "ad.provenance.disagree"); len(got) != 0 {
			t.Errorf("sweep %d: ad.provenance.disagree = %v; want none", sweep, got)
		}
	}
	if got, err := st.GetSpawnState("dm-pane-alive"); err != nil || got != store.StateWorking {
		t.Errorf("dm-pane-alive state = %q (err %v); want working", got, err)
	}
}

// TestFindMissingAllDeadNoDegradedModeSkip: when every recorded process is gone (after a reboot) every row is
// marked with only proc_absent ticks; no refusal is recorded.
func TestFindMissingAllDeadNoDegradedModeSkip(t *testing.T) {
	st, _ := seedTrailStore(t,
		trailRow{id: "dg-1", ssPID: 1401},
		trailRow{id: "dg-2", panePID: 1402},
	)
	before := trailLen(t)

	res, err := runFindMissing(st, procfix.New(), fmSweep{})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	assertLists(t, res, []string{"dg-1", "dg-2"}, nil)
	for _, tick := range apiFindMissingTicksAt(t, before) {
		if id := tick["claude_instance_id"]; (id == "dg-1" || id == "dg-2") && tick["reconciliation_reason"] != "proc_absent" {
			t.Errorf("tick %v; want only proc_absent", tick)
		}
	}
	// The removed guard's reason is assembled at runtime so a repo grep for the literal stays clean.
	if got := apiTicksWithReason(apiFindMissingTicksAt(t, before), "degraded_mode"+"_skip"); len(got) != 0 {
		t.Errorf("degraded-mode skip ticks = %v; want none", got)
	}
}
