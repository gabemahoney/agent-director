package hook_test

// hook_gate_sessionstart_test.go — AC-HOOK-02 (SR-22.9): SessionStart moves a
// row only for its own agent. Driven through hook.Handle against a real store,
// with S1's payload fixtures and a fake parent process (hook_parent_fakes_test.go).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// ssgPane is a launch's recorded pane with pid pid and a recorded start time.
func ssgPane(token string, pid int) store.LaunchIdentity {
	return store.LaunchIdentity{Token: token, Socket: apitest.TestSocket, PaneID: "%" + strconv.Itoa(pid),
		PanePID: pid, PaneStarttime: apitest.LinuxProcStarttime}
}

var (
	ssgFreshPane = ssgPane("0123456789abcdef", 4101)
	ssgOldPane   = ssgPane("1111111111111111", 4102) // a resumed row's earlier life
	ssgNewPane   = ssgPane("2222222222222222", 4103) // a resumed row's new launch
	ssgNoPane    = store.LaunchIdentity{Token: "3333333333333333", Socket: apitest.TestSocket}
)

const (
	// ssgOutgoing is the session a live row records before its agent's SessionStart.
	ssgOutgoing = "sess-outgoing"
	// ssgKept is the session a resumed row keeps: session-start-resume.json's session_id.
	ssgKept = "4b0bf746-54c3-4934-95c4-43c3f0a1ff00"
)

// ssgPayload returns the SessionStart fixture for source with its transcript
// moved to a file on disk, plus the payload's session id and that path.
func ssgPayload(t *testing.T, source string) (payload, sessionID, path string) {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(readPayloadFixture(t, "session-start-"+source+".json"), &p); err != nil {
		t.Fatalf("fixture %s: %v", source, err)
	}
	sessionID, _ = p["session_id"].(string)
	path = writeTranscript(t, sessionID)
	p["transcript_path"] = path
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return string(raw), sessionID, path
}

// ssgHandle runs one Handle for id's row from parent p and returns its stdout.
func ssgHandle(t *testing.T, st hook.HookStore, id string, p hookParent, payload string, logger *log.Logger) string {
	t.Helper()
	return ssgHandleWith(t, st, hookConfig(envHook(id, ""), p), payload, logger)
}

// ssgHandleWith runs one Handle with hc and returns its stdout.
func ssgHandleWith(t *testing.T, st hook.HookStore, hc hook.HandleConfig, payload string, logger *log.Logger) string {
	t.Helper()
	var out bytes.Buffer
	if err := hook.Handle(context.Background(), strings.NewReader(payload), &out, st, hc, logger); err != nil {
		t.Fatalf("Handle: %v (want nil: the hook is fail-open)", err)
	}
	return out.String()
}

// ssgSeed opens a temp store and seeds id through apitest.SeedSpawn.
func ssgSeed(t *testing.T, id, state, sessionID string, opts ...apitest.SpawnOption) (*store.Store, string) {
	t.Helper()
	st, dbPath := storefix.OpenTempStore(t)
	if _, err := apitest.SeedSpawn(dbPath, id, state, "", "", sessionID, false, opts...); err != nil {
		t.Fatalf("SeedSpawn(%q, %s): %v", id, state, err)
	}
	return st, dbPath
}

// ssgResumed leaves id as resume's move and identity write do (Tasks mg, gp):
// pending, the kept session id, the earlier life's pane replaced by ssgNewPane.
func ssgResumed(t *testing.T, id string) (*store.Store, string) {
	t.Helper()
	st, dbPath := ssgSeed(t, id, store.StateEnded, ssgKept,
		apitest.WithJsonlPath("/x/"+ssgKept+".jsonl"), apitest.WithLaunchIdentity(ssgOldPane))
	res, moved, err := st.MoveToPending(id, mustGetSpawn(t, st, id).Snapshot, time.Now().UnixMilli(),
		ssgNewPane.Token, ssgNewPane.Socket, "")
	if err != nil || res != store.CondApplied {
		t.Fatalf("MoveToPending(%q) = %v, %v; want applied", id, res, err)
	}
	if res, err := st.RecordLaunchIdentity(id, moved, ssgNewPane.Token, ssgNewPane); err != nil || res != store.CondApplied {
		t.Fatalf("RecordLaunchIdentity(%q) = %v, %v; want applied", id, res, err)
	}
	return st, dbPath
}

// ssgMissingBeforeReport is a pending row with a pane that find-missing
// marked missing before its agent reported in.
func ssgMissingBeforeReport(t *testing.T, id string) (*store.Store, string) {
	t.Helper()
	st, dbPath := ssgSeed(t, id, store.StatePending, "", apitest.WithLaunchIdentity(ssgFreshPane))
	if prior, err := markMissingSameLife(st, id); err != nil || prior != store.StatePending {
		t.Fatalf("mark %q missing = %q, %v; want prior pending", id, prior, err)
	}
	return st, dbPath
}

// markMissingSameLife marks id missing the way find-missing does: the guarded
// mark on the row's current snapshot. It returns the prior state; a mark that
// does not apply is an error.
func markMissingSameLife(st *store.Store, id string) (string, error) {
	sp, err := st.GetSpawn(id)
	if err != nil {
		return "", err
	}
	prior, res, err := st.MarkMissingIfSameLife(id, sp.Snapshot)
	if err == nil && res != store.CondApplied {
		err = fmt.Errorf("MarkMissingIfSameLife(%q) = %v; want applied", id, res)
	}
	return prior, err
}

// bumpRowVersion advances id's row_version by one without moving its state:
// find-missing's guarded liveness clear on the row's current snapshot, which
// writes whether or not a note is set. A clear that does not apply is an error.
func bumpRowVersion(st *store.Store, id string) error {
	sp, err := st.GetSpawn(id)
	if err != nil {
		return err
	}
	res, err := st.ClearLivenessIfSameLife(id, sp.Snapshot)
	if err == nil && res != store.CondApplied {
		err = fmt.Errorf("ClearLivenessIfSameLife(%q) = %v; want applied", id, res)
	}
	return err
}

// ssgLive is a live row in state recording ssgOutgoing (life 2) and a pane.
func ssgLive(state string) func(*testing.T, string) (*store.Store, string) {
	return func(t *testing.T, id string) (*store.Store, string) {
		return ssgSeed(t, id, state, ssgOutgoing, apitest.WithJsonlPath("/x/"+ssgOutgoing+".jsonl"),
			apitest.WithLifeNumber(2), apitest.WithLaunchIdentity(ssgFreshPane))
	}
}

// ssgArchived is one expected session_history entry.
type ssgArchived struct {
	session, path string
	life          int64
}

// ssgHistory reads id's session_history from every life.
func ssgHistory(t *testing.T, dbPath, id string) []ssgArchived {
	t.Helper()
	entries, err := apitest.ReadSessionHistoryAllLives(dbPath, id)
	if err != nil {
		t.Fatalf("ReadSessionHistoryAllLives(%q): %v", id, err)
	}
	var out []ssgArchived
	for _, e := range entries {
		out = append(out, ssgArchived{e.ClaudeSessionID, e.JSONLPath.String, e.LifeNumber})
	}
	return out
}

// TestSessionStartGateApplied: the row's own agent's SessionStart applies for
// every source and row state, records the new session, archives the old pair
// and records the parent as pid/proc_starttime (SR-22.9, AC-HOOK-02).
func TestSessionStartGateApplied(t *testing.T) {
	outgoing := []ssgArchived{{ssgOutgoing, "/x/" + ssgOutgoing + ".jsonl", 2}}
	cases := []struct {
		name, source string
		seed         func(*testing.T, string) (*store.Store, string)
		archived     []ssgArchived
	}{
		{"fresh spawn's pending row", "startup", func(t *testing.T, id string) (*store.Store, string) {
			return ssgSeed(t, id, store.StatePending, "", apitest.WithLaunchIdentity(ssgFreshPane))
		}, nil},
		{"fresh spawn's pending row, pane start unread at the create", "startup", func(t *testing.T, id string) (*store.Store, string) {
			return ssgSeed(t, id, store.StatePending, "") // pane TestPanePID, pane_starttime NULL
		}, nil},
		{"resumed pending row, kept session id and new pane", "resume", ssgResumed, nil},
		{"row marked missing before its agent reported in", "startup", ssgMissingBeforeReport, nil},
		{"live row, /clear", "clear", ssgLive(store.StateWorking), outgoing},
		{"live row, in-session /resume", "resume", ssgLive(store.StateWaiting), outgoing},
		{"live row, compaction", "compact", ssgLive(store.StateAskUser), outgoing},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "ssg-applied-" + strconv.Itoa(i)
			st, dbPath := tc.seed(t, id)
			payload, sessionID, path := ssgPayload(t, tc.source)
			agent := agentParent(t, st, id)
			prior := mustGetSpawn(t, st, id)
			before := len(readTrailLines(t, trailFile()))

			if out := ssgHandle(t, st, id, agent, payload, nil); out != "" {
				t.Errorf("stdout = %q; want empty", out)
			}

			row := mustGetSpawn(t, st, id)
			if row.State != store.StateWaiting || row.EndedAt != nil {
				t.Errorf("state/ended_at = %q/%v; want waiting/NULL (from %q)", row.State, row.EndedAt, prior.State)
			}
			if row.ClaudeSessionID != sessionID || row.JSONLPath != path {
				t.Errorf("session/path = %q/%q; want the payload's %q/%q", row.ClaudeSessionID, row.JSONLPath, sessionID, path)
			}
			// SR-22.9: pid/proc_starttime = the hook's parent; a NULL pane start is recorded from it.
			if row.PID != agent.PID || row.ProcStarttime != agent.Start || row.Identity.PaneStarttime != agent.Start {
				t.Errorf("pid/proc/pane start = %d/%q/%q; want the parent %d/%q", row.PID, row.ProcStarttime,
					row.Identity.PaneStarttime, agent.PID, agent.Start)
			}
			if row.Identity.PanePID != prior.Identity.PanePID {
				t.Errorf("pane_pid = %d; want %d (unchanged)", row.Identity.PanePID, prior.Identity.PanePID)
			}
			if row.RowVersion != prior.RowVersion+1 {
				t.Errorf("row_version = %d; want %d (one write)", row.RowVersion, prior.RowVersion+1)
			}
			if got := ssgHistory(t, dbPath, id); !reflect.DeepEqual(got, tc.archived) {
				t.Errorf("session_history = %+v; want %+v", got, tc.archived)
			}
			if got := hookIgnoredAfter(t, before, id); len(got) != 0 {
				t.Errorf("ad.hook.ignored = %v; want none", got)
			}
		})
	}
}

// TestSessionStartGateIgnored: a SessionStart from any process but the row's
// recorded pane process changes nothing and writes one ad.hook.ignored at once,
// with no wait (SR-22.9: a pane-less pending row here has no launch start or
// one past the pending grace, so SessionStart has nothing to wait for).
func TestSessionStartGateIgnored(t *testing.T) {
	leftoverOfOldLife := func(*testing.T, *store.Store, string) hookParent {
		return hookParent{PID: ssgOldPane.PanePID, Start: ssgOldPane.PaneStarttime, Name: "claude"}
	}
	endedWithPane := func(t *testing.T, id string) (*store.Store, string) {
		return ssgSeed(t, id, store.StateEnded, "sess-ended", apitest.WithLaunchIdentity(ssgFreshPane))
	}
	pendingNoPaneNoLaunchStart := func(t *testing.T, id string) (*store.Store, string) {
		return ssgSeed(t, id, store.StatePending, "", apitest.WithLaunchIdentity(ssgNoPane),
			apitest.WithNoLaunchStartedAt())
	}
	pendingNoPanePastGrace := func(t *testing.T, id string) (*store.Store, string) {
		pastGrace := time.Now().Add(-config.Tmux{}.EffectivePendingGrace() - time.Second).UnixMilli()
		return ssgSeed(t, id, store.StatePending, "", apitest.WithLaunchIdentity(ssgNoPane),
			apitest.WithLaunchStartedAt(pastGrace))
	}
	cases := []struct {
		name       string
		seed       func(*testing.T, string) (*store.Store, string)
		parent     func(*testing.T, *store.Store, string) hookParent
		reason     string
		rowSession any // row_session_id; nil = null
		rowPanePID any // row_pane_pid; nil = null
	}{
		{"leftover on a fresh pending row", func(t *testing.T, id string) (*store.Store, string) {
			return ssgSeed(t, id, store.StatePending, "", apitest.WithLaunchIdentity(ssgFreshPane))
		}, foreignParent, store.HookReasonPIDMismatch, nil, float64(ssgFreshPane.PanePID)},
		{"earlier life's leftover on a resumed pending row", ssgResumed, leftoverOfOldLife,
			store.HookReasonPIDMismatch, ssgKept, float64(ssgNewPane.PanePID)},
		{"leftover on an ended row with a pane", endedWithPane, foreignParent,
			store.HookReasonPIDMismatch, "sess-ended", float64(ssgFreshPane.PanePID)},
		// SR-22.9/SR-14 over the PRD's wording (Task note WD 2026-09-30): a row with no pane is no_pane_recorded.
		{"leftover on an ended row with no pane", func(t *testing.T, id string) (*store.Store, string) {
			return ssgSeed(t, id, store.StateEnded, "sess-ended")
		}, foreignParent, store.HookReasonNoPaneRecorded, "sess-ended", nil},
		{"leftover on a pending row with no pane, launch start past the grace", pendingNoPanePastGrace,
			foreignParent, store.HookReasonNoPaneRecorded, nil, nil},
		{"the launch's own agent on a pane-less pending row, no launch start", pendingNoPaneNoLaunchStart,
			agentParent, store.HookReasonNoPaneRecorded, nil, nil},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "ssg-ignored-" + strconv.Itoa(i)
			st, dbPath := tc.seed(t, id)
			payload, sessionID, _ := ssgPayload(t, "startup")
			parent := tc.parent(t, st, id)
			prior, err := apitest.ReadSpawnColumns(dbPath, id)
			if err != nil {
				t.Fatalf("ReadSpawnColumns: %v", err)
			}
			priorHistory := ssgHistory(t, dbPath, id)
			before := len(readTrailLines(t, trailFile()))

			hc := hookConfig(envHook(id, ""), parent)
			if out := ssgHandleWith(t, st, hc, payload, nil); out != "" {
				t.Errorf("stdout = %q; want empty", out)
			}
			if sleeps := hookClock(t, hc).Sleeps(); len(sleeps) != 0 {
				t.Errorf("SessionStart slept %d times; want no wait", len(sleeps))
			}

			// SR-22.9: a hook not applied changes nothing (state, last_seen_at, identity, row_version).
			if after, err := apitest.ReadSpawnColumns(dbPath, id); err != nil || !reflect.DeepEqual(after, prior) {
				t.Errorf("row changed by an ignored SessionStart (err %v):\n got %+v\nwant %+v", err, after, prior)
			}
			if got := ssgHistory(t, dbPath, id); !reflect.DeepEqual(got, priorHistory) {
				t.Errorf("session_history = %+v; want %+v (unchanged)", got, priorHistory)
			}
			ignored := hookIgnoredAfter(t, before, id)
			if len(ignored) != 1 {
				t.Fatalf("ad.hook.ignored lines = %d; want 1", len(ignored))
			}
			want := map[string]any{
				"hook_event": "SessionStart", "reason": tc.reason, "parent_pid": float64(parent.PID),
				"parent_command": "claude", "hook_session_id": sessionID, "row_session_id": tc.rowSession,
				"row_pane_pid": tc.rowPanePID, "source": "ad_hook",
			}
			for k, v := range want {
				if got, ok := ignored[0][k]; !ok || got != v {
					t.Errorf("ad.hook.ignored[%q] = %v (present %t); want %v", k, got, ok, v)
				}
			}
		})
	}
}

// ssgBumpingStore is the real store with a writer that advances id's
// row_version right after each of Handle's first bumps reads of the row.
type ssgBumpingStore struct {
	*store.Store
	t      *testing.T
	bumps  int
	reads  int
	bumped int
}

func (s *ssgBumpingStore) GetSpawn(id string) (store.Spawn, error) {
	sp, err := s.Store.GetSpawn(id)
	s.reads++
	if s.reads <= s.bumps {
		if cerr := bumpRowVersion(s.Store, id); cerr != nil {
			s.t.Fatalf("bump %q: %v", id, cerr)
		}
		s.bumped++
	}
	return sp, err
}

// TestSessionStartGateSnapshotRace: a row changed between SessionStart's read
// and its write is re-read and written once more; changed twice, nothing is
// written and no ad.hook.ignored (SR-5.3, decision A3).
func TestSessionStartGateSnapshotRace(t *testing.T) {
	cases := []struct {
		name    string
		bumps   int
		applied bool
	}{
		{"changed once: retried and applied", 1, true},
		{"changed twice: nothing written", 2, false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "ssg-race-" + strconv.Itoa(i)
			st, _ := ssgSeed(t, id, store.StatePending, "", apitest.WithLaunchIdentity(ssgFreshPane))
			payload, sessionID, _ := ssgPayload(t, "startup")
			agent := agentParent(t, st, id)
			prior := mustGetSpawn(t, st, id)
			before := len(readTrailLines(t, trailFile()))
			var logBuf bytes.Buffer
			bs := &ssgBumpingStore{Store: st, t: t, bumps: tc.bumps}

			ssgHandle(t, bs, id, agent, payload, log.New(&logBuf, "", 0))

			if bs.reads != 2 {
				t.Errorf("row reads = %d; want 2 (one retry, no more)", bs.reads)
			}
			row := mustGetSpawn(t, st, id)
			wantVersion := prior.RowVersion + int64(bs.bumped)
			if tc.applied {
				wantVersion++
				if row.State != store.StateWaiting || row.ClaudeSessionID != sessionID || row.PID != agent.PID {
					t.Errorf("state/session/pid = %q/%q/%d; want waiting/%q/%d", row.State, row.ClaudeSessionID, row.PID, sessionID, agent.PID)
				}
			} else {
				if row.State != store.StatePending || row.ClaudeSessionID != "" || row.PID != 0 || row.JSONLPath != "" {
					t.Errorf("state/session/pid/path = %q/%q/%d/%q; want pending, nothing recorded",
						row.State, row.ClaudeSessionID, row.PID, row.JSONLPath)
				}
				if !strings.Contains(logBuf.String(), id) {
					t.Errorf("log = %q; want one line naming %q", logBuf.String(), id)
				}
			}
			if row.RowVersion != wantVersion {
				t.Errorf("row_version = %d; want %d", row.RowVersion, wantVersion)
			}
			if got := hookIgnoredAfter(t, before, id); len(got) != 0 {
				t.Errorf("ad.hook.ignored = %v; want none", got)
			}
		})
	}
}

// TestSessionStartGateNoProbeOrTmuxDependency: the hook path walks no process
// ancestry and makes no tmux call — internal/hook's non-test files, followed
// through module-internal imports (every build tag), reach neither
// internal/probe nor internal/tmux (SR-22.9).
func TestSessionStartGateNoProbeOrTmuxDependency(t *testing.T) {
	const module = "github.com/gabemahoney/agent-director"
	root := filepath.Join("..", "..")
	if mod, err := os.ReadFile(filepath.Join(root, "go.mod")); err != nil || !strings.HasPrefix(string(mod), "module "+module+"\n") {
		t.Fatalf("go.mod at %s does not declare %s (err %v)", root, module, err)
	}
	forbidden := map[string]bool{module + "/internal/probe": true, module + "/internal/tmux": true}
	start := module + "/internal/hook"
	from := map[string]string{start: ""} // package -> the package that imports it
	for queue := []string{start}; len(queue) > 0; queue = queue[1:] {
		for _, imp := range ssgImports(t, filepath.Join(root, strings.TrimPrefix(queue[0], module+"/"))) {
			if _, seen := from[imp]; seen || !strings.HasPrefix(imp, module+"/") {
				continue
			}
			from[imp] = queue[0]
			if forbidden[imp] {
				chain := imp
				for p := from[imp]; p != ""; p = from[p] {
					chain = p + " -> " + chain
				}
				t.Errorf("internal/hook depends on %s: %s", imp, chain)
			}
			queue = append(queue, imp)
		}
	}
	if _, ok := from[module+"/internal/store"]; !ok {
		t.Fatalf("walk reached %v; want it to follow internal/hook's import of internal/store", from)
	}
}

// ssgImports returns the import paths of every non-test .go file in dir.
func ssgImports(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []string
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, spec := range f.Imports {
			p, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("%s: import %s: %v", name, spec.Path.Value, err)
			}
			out = append(out, p)
		}
	}
	return out
}
