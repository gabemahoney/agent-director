package api_test

// find_missing_trail_test.go: the shared trail reader (readAPITrailLines; TestMain, example_main_test.go, points the
// trail at apiTrailDir) and the find-missing trail helpers: seeded trail rows and the tick assertion.

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
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

// trailToken is the launch token every trail row carries; the process path never reads it.
const trailToken = "5eed0000000000c3"

// trailRow is one working row to seed: its SessionStart and pane pids (0 = none recorded), both with start
// time fmStart unless pidOnly.
type trailRow struct {
	id             string
	ssPID, panePID int
	pidOnly        bool
}

// pid is the row's agent process pid when only one is recorded or both agree (0 = none recorded).
func (r trailRow) pid() int { return max(r.ssPID, r.panePID) }

// seedTrailStore seeds r as a working row recording its identities in a fresh store and returns it open with its
// path.
func seedTrailStore(t *testing.T, r trailRow) (*store.Store, string) {
	t.Helper()
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
	return openSeeded(t, func(t *testing.T, p string, create bool, _ string) {
		if _, err := apitest.SeedSpawn(p, r.id, store.StateWorking, "/tmp", "off", "", create, opts...); err != nil {
			t.Fatalf("SeedSpawn %q: %v", r.id, err)
		}
	})
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
