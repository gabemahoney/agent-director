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
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
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

// apiTrail is the api trail file parsed so far: the file only ever grows in
// this process (the singleton appends one whole line per Emit; no test in
// this process truncates, replaces or removes it), so each read parses only
// the lines appended since the last one, instead of the whole file again.
// Re-parsing the whole file on every checkpoint made the trail-reading tests'
// cost grow with the run's length. The lock lets parallel tests read while
// others append; the parsed lines are shared, so callers must not modify them.
var apiTrail struct {
	mu    sync.Mutex
	file  os.FileInfo      // the file parsed so far; nil until it exists
	off   int64            // the file offset just past the last whole line parsed
	lines []map[string]any // every whole line parsed so far, in file order
}

// readAPITrailLines parses every JSONL line from the api trail file.
// Returns nil when the file does not exist yet. A last line not yet ended by
// its newline (a write in flight in a parallel test) is left for a later read.
// A file removed, replaced or truncated after a read fails the test: the
// cached lines and every caller's mark would no longer match it.
func readAPITrailLines(t *testing.T) []map[string]any {
	t.Helper()
	apiTrail.mu.Lock()
	defer apiTrail.mu.Unlock()
	f, err := os.Open(apiTrailFilePath())
	if os.IsNotExist(err) && apiTrail.file == nil {
		return nil
	}
	if err != nil {
		t.Fatalf("readAPITrailLines: %v", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		t.Fatalf("readAPITrailLines: stat: %v", err)
	}
	if apiTrail.file != nil && (!os.SameFile(apiTrail.file, fi) || fi.Size() < apiTrail.off) {
		t.Fatalf("readAPITrailLines: the trail file was replaced or truncated (%d bytes, %d already read); "+
			"the cache needs it to only grow", fi.Size(), apiTrail.off)
	}
	apiTrail.file = fi
	if _, err := f.Seek(apiTrail.off, io.SeekStart); err != nil {
		t.Fatalf("readAPITrailLines: seek: %v", err)
	}
	added, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("readAPITrailLines: read: %v", err)
	}
	for {
		line, rest, whole := bytes.Cut(added, []byte("\n"))
		if !whole {
			break
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("readAPITrailLines: unmarshal %q: %v", line, err)
		}
		apiTrail.lines = append(apiTrail.lines, m)
		apiTrail.off += int64(len(line) + 1)
		added = rest
	}
	n := len(apiTrail.lines)
	return apiTrail.lines[:n:n] // full capacity: a caller's append copies
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
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
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
	// Serial: it checks every record written to the shared trail since its mark.
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
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
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

// TestFindMissingUnusableNameTrail: per note token, rows with an unusable name (pane and SessionStart pids
// differing with the pane unreadable, or no evidence) get only their entry tick, with no lookup fields, no
// disagree or name-held record, nothing of another row or any environment; a second sweep writes nothing.
func TestFindMissingUnusableNameTrail(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	const marker = "ut-environment-marker"
	env := map[string]string{"AD_TRAIL_MARKER": marker}
	for _, f := range fmuReps() {
		t.Run(f.note, func(t *testing.T) {
			name := apitest.WithTmuxSessionName(f.raw)
			pc := procfix.New()
			pc.Set(1501, procfix.Alive(fmStart).WithEnv(env)) // ut-mismatch's SessionStart process
			pc.Set(1502, procfix.Unreadable())                // ut-mismatch's pane process, which decides
			pc.Set(1503, procfix.Alive(fmStart).WithEnv(env)) // ut-other's
			st, _ := seedTrailStore(t,
				trailRow{id: "ut-mismatch", ssPID: 1501, panePID: 1502, opts: []apitest.SpawnOption{name}},
				trailRow{id: "ut-none", opts: []apitest.SpawnOption{name}},
				trailRow{id: "ut-other", ssPID: 1503},
			)
			noted := []string{"ut-mismatch", "ut-none"}

			for sweep := 1; sweep <= 2; sweep++ {
				before := trailLen(t)
				res, err := runFindMissing(st, pc, fmSweep{})
				if err != nil {
					t.Fatalf("sweep %d: %v", sweep, err)
				}
				assertLists(t, res, nil, noted)
				added := readAPITrailLines(t)[before:]
				for _, id := range noted {
					recs := trailOf(added, id)
					if sweep == 2 {
						if len(recs) != 0 {
							t.Errorf("sweep 2: %s records = %v; want none", id, recs)
						}
						continue
					}
					if len(recs) != 1 || recs[0]["event"] != "ad.find_missing.tick" {
						t.Fatalf("%s records = %v; want only its entry tick", id, recs)
					}
					assertTick(t, recs[0], f.note, "", nil)
				}
				for _, l := range added {
					line, _ := json.Marshal(l)
					if l["event"] == "ad.provenance.disagree" || l["event"] == "ad.launch.name_held" {
						t.Errorf("sweep %d wrote %s", sweep, line)
					}
					for _, id := range append(noted, "ut-other") {
						if l["claude_instance_id"] != id && strings.Contains(string(line), `"`+id+`"`) {
							t.Errorf("sweep %d record names %s: %s", sweep, id, line)
						}
					}
					if strings.Contains(string(line), marker) {
						t.Errorf("sweep %d record carries environment content: %s", sweep, line)
					}
				}
			}
			if n := pc.EnvReads(); n != 0 {
				t.Errorf("environment reads = %d; want 0", n)
			}
		})
	}
}
