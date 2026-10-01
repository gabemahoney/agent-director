package api_test

// spawn_reuse_history_test.go: a reused id starts with no memory (SR-5.9,
// SR-8.7; AC-REUSE-20, 21, 22, 26), end to end through the real store on the
// reuse fixture (spawn_reuse_fixture_test.go). The new agent's report-in,
// rotation and end are its own pane's hooks (SR-22.9). The rlf helpers here
// are shared with spawn_reuse_lives_test.go and spawn_reuse_pretrust_test.go;
// the reuse itself is the fixture's (newReuseEnv, reuseLaunch).

import (
	"context"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rlfRow reads id's row through the store.
func rlfRow(t *testing.T, e *killEnv, id string) api.Spawn {
	t.Helper()
	row, err := e.st.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn(%s): %v", id, err)
	}
	return row
}

// rlfReportIn delivers id's SessionStart for sid from its agent, reporting the
// transcript under the row's config dir and cwd (written first when
// messaged); it fails unless the hook applies and returns that path.
func rlfReportIn(t *testing.T, e *killEnv, id, sid string, messaged bool) string {
	t.Helper()
	row := rlfRow(t, e, id)
	p, err := spawn.JsonlPathIn(row.ExtraEnv["CLAUDE_CONFIG_DIR"], row.CWD, sid)
	if err != nil {
		t.Fatalf("JsonlPathIn: %v", err)
	}
	if messaged {
		apitest.SeedJsonlUnder(t, row.ExtraEnv["CLAUDE_CONFIG_DIR"], row.CWD, sid)
	}
	if got := apitest.ApplyAgentHook(t, e.dbPath, id, "SessionStart", sid, apitest.HookTranscript(p, messaged)); !got.Applied {
		t.Fatalf("SessionStart %s from the agent = %+v; want applied", sid, got)
	}
	return p
}

// rlfEndLife ends id's life: its agent's SessionEnd applies, then its
// session goes and its agent process exits.
func rlfEndLife(t *testing.T, e *killEnv, id string) {
	t.Helper()
	if got := apitest.ApplyAgentHook(t, e.dbPath, id, "SessionEnd", ""); !got.Applied {
		t.Fatalf("SessionEnd from the agent = %+v; want applied", got)
	}
	rlfAgentGone(t, e, rlfRow(t, e, id))
}

// rlfNeverReported ends id's pending life with no report-in: its session
// goes, its agent exits and a find-missing past the grace period marks it missing.
func rlfNeverReported(t *testing.T, e *killEnv, id string) {
	t.Helper()
	rlfAgentGone(t, e, rlfRow(t, e, id))
	e.clock.Advance(time.Duration(config.DefaultPendingGraceSeconds+1) * time.Second)
	c, _ := e.client(t)
	if _, err := c.FindMissing(context.Background()); err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	if st := rlfRow(t, e, id).State; st != store.StateMissing {
		t.Fatalf("state after find-missing = %s; want missing", st)
	}
}

// rlfAgentGone removes row's own session and marks its pane process gone.
func rlfAgentGone(t *testing.T, e *killEnv, row api.Spawn) {
	t.Helper()
	killed := false
	for _, s := range e.rec.Sessions(row.Identity.Socket) {
		if s.Label.Token == row.Identity.Token {
			if err := e.rec.KillSessionID(row.Identity.Socket, s.ID); err != nil {
				t.Fatalf("KillSessionID: %v", err)
			}
			killed = true
		}
	}
	if !killed {
		t.Fatalf("no session labelled %s on %s", row.Identity.Token, row.Identity.Socket)
	}
	e.pc.Set(row.Identity.PanePID, procfix.Gone())
}

// rlfEarlierOnDisk writes every transcript r's earlier lives could name (its
// history's recorded and recomputed paths), so reading one would show.
func rlfEarlierOnDisk(t *testing.T, r reuseRow) {
	t.Helper()
	for _, h := range r.History {
		plantJsonl(t, h.JSONLPath)
		apitest.SeedJsonlUnder(t, r.Trust.dir, r.CWD, h.SessionID)
	}
}

// rlfGet is get of id.
func rlfGet(t *testing.T, e *killEnv, id string) api.SpawnRow {
	t.Helper()
	row, err := api.Get(e.st, id)
	if err != nil {
		t.Fatalf("Get(%s): %v", id, err)
	}
	return row
}

// rlfAssertListed fails unless get shows status and exactly want as
// prior_sessions (ids and paths, in any order).
func rlfAssertListed(t *testing.T, got api.SpawnRow, status string, want ...getWantPrior) {
	t.Helper()
	if got.TranscriptStatus != status {
		t.Errorf("transcript_status = %q; want %q", got.TranscriptStatus, status)
	}
	var have, need []string
	for _, p := range got.PriorSessions {
		have = append(have, p.ClaudeSessionID+" "+p.JSONLPath)
	}
	for _, w := range want {
		need = append(need, w.id+" "+w.path)
	}
	sort.Strings(have)
	sort.Strings(need)
	if !slices.Equal(have, need) {
		t.Errorf("prior_sessions = %q; want %q", have, need)
	}
}

// rlfResumeRefused resumes id and fails unless it returns want with no tmux
// call and nothing written; it returns the error text.
func rlfResumeRefused(t *testing.T, e *killEnv, id string, want error) string {
	t.Helper()
	before := e.snapshotWrites(t, "resume", id, trustConfig{})
	_, err := e.resume(id)
	assertLaunchSentinel(t, err, want)
	if calls := e.rec.SocketCalls()[before.calls:]; len(calls) != 0 {
		t.Errorf("tmux calls by the refused resume = %+v; want none", calls)
	}
	e.assertWroteNothing(t, before)
	return err.Error()
}

// rlfResumed resumes id and returns the session id its one create resumes.
func rlfResumed(t *testing.T, e *killEnv, id string) string {
	t.Helper()
	n := len(e.rec.SocketCallsOf(tmux.CallCreate))
	if _, err := e.resume(id); err != nil {
		t.Fatalf("Resume(%s): %v", id, err)
	}
	cs := e.rec.SocketCallsOf(tmux.CallCreate)[n:]
	if len(cs) != 1 {
		t.Fatalf("creates by the resume = %d; want 1", len(cs))
	}
	i := slices.Index(cs[0].Command, "--resume")
	if i < 0 || i+1 >= len(cs[0].Command) {
		t.Fatalf("argv = %q; want --resume <session>", cs[0].Command)
	}
	return cs[0].Command[i+1]
}

// rlfAssertEarlierUnused fails if text names, or any create resumes, a
// session or transcript of r0, the row as seeded before its first reuse.
func rlfAssertEarlierUnused(t *testing.T, e *killEnv, r0 reuseRow, text string) {
	t.Helper()
	earlier := []string{r0.Spawn.ClaudeSessionID, r0.JSONLPath}
	for _, h := range r0.History {
		earlier = append(earlier, h.SessionID, h.JSONLPath)
	}
	for _, s := range earlier {
		if s != "" && strings.Contains(text, s) {
			t.Errorf("%q names the earlier life's %q", text, s)
		}
	}
	for _, c := range e.rec.SocketCallsOf(tmux.CallCreate) {
		if i := slices.Index(c.Command, "--resume"); i >= 0 && i+1 < len(c.Command) && slices.Contains(earlier, c.Command[i+1]) {
			t.Errorf("create %q resumes an earlier life's session", c.Command)
		}
	}
}

// rlfAssertStored fails unless id's history holds exactly one entry for sid, in life.
func rlfAssertStored(t *testing.T, e *killEnv, id, sid string, life int64) {
	t.Helper()
	all, err := apitest.ReadSessionHistoryAllLives(e.dbPath, id)
	if err != nil {
		t.Fatalf("ReadSessionHistoryAllLives: %v", err)
	}
	var lives []int64
	for _, h := range all {
		if h.ClaudeSessionID == sid {
			lives = append(lives, h.LifeNumber)
		}
	}
	if !slices.Equal(lives, []int64{life}) {
		t.Errorf("history entries for %s in lives %v; want one in life %d (all %+v)", sid, lives, life, all)
	}
}

// rlfNewSession is a new Claude session id.
func rlfNewSession() string { return "sess-" + uuid.NewString()[:8] }

// TestSpawnReuseHistoryNeverMessaged: a reused id whose new life is never
// messaged resumes to ErrJsonlNeverWritten (ErrNoSessionId if it never
// reported in) and get lists nothing, though the earlier life's transcripts are on disk.
func TestSpawnReuseHistoryNeverMessaged(t *testing.T) {
	cases := []struct {
		name       string
		request    func(t *testing.T) reuseRequest
		noReport   bool
		wantErr    error
		wantStatus string
	}{
		{"AC-REUSE-20/26 same cwd, config dir and name", nil, false, api.ErrJsonlNeverWritten, "never_written"},
		{"AC-REUSE-20/26 never reported in", nil, true, api.ErrNoSessionId, "no_session"},
		{"AC-REUSE-22 moved cwd", func(t *testing.T) reuseRequest { return reuseRequest{CWD: t.TempDir()} },
			false, api.ErrJsonlNeverWritten, "never_written"},
		{"AC-REUSE-22 moved CLAUDE_CONFIG_DIR", func(t *testing.T) reuseRequest {
			return reuseRequest{Env: seedTrustConfig(t, t.TempDir(), trustLacksEntry).extraEnv()}
		}, false, api.ErrJsonlNeverWritten, "never_written"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newReuseEnv(t)
			r0 := e.seedReusable(t, agentGone, reuseRowSpec{})
			rlfEarlierOnDisk(t, r0)
			var q reuseRequest
			if tc.request != nil {
				q = tc.request(t)
			}
			r, _ := e.reuseLaunch(t, r0, agentAlive, q)
			if q.Name == "" && r.Name != r0.Name {
				t.Fatalf("reused under %q; want the recorded name %q", r.Name, r0.Name)
			}
			if tc.noReport {
				rlfNeverReported(t, e, r.ID)
			} else {
				rlfReportIn(t, e, r.ID, rlfNewSession(), false)
				rlfAssertListed(t, rlfGet(t, e, r.ID), "never_written") // live, before any message
				rlfEndLife(t, e, r.ID)
			}

			rlfAssertListed(t, rlfGet(t, e, r.ID), tc.wantStatus)
			msg := rlfResumeRefused(t, e, r.ID, tc.wantErr)
			rlfAssertEarlierUnused(t, e, r0, msg)
			rlfAssertStored(t, e, r.ID, r0.Spawn.ClaudeSessionID, reuseLife)
			for _, h := range r0.History {
				rlfAssertStored(t, e, r.ID, h.SessionID, h.Life)
			}
		})
	}
}

// rlfMessagedLife reuses a seeded row and has its new agent report in with
// a messaged session; it returns the env, the row before and after the reuse,
// the session and its transcript path.
func rlfMessagedLife(t *testing.T) (e *killEnv, r0, r reuseRow, sid, path string) {
	t.Helper()
	e = newReuseEnv(t)
	r0 = e.seedReusable(t, agentGone, reuseRowSpec{})
	rlfEarlierOnDisk(t, r0)
	r, _ = e.reuseLaunch(t, r0, agentAlive, reuseRequest{})
	sid = rlfNewSession()
	return e, r0, r, sid, rlfReportIn(t, e, r.ID, sid, true)
}

// TestSpawnReuseHistoryMessagedNewLife (AC-REUSE-21): a messaged new life is
// what resume reattaches and get lists; the earlier life's never is.
func TestSpawnReuseHistoryMessagedNewLife(t *testing.T) {
	t.Run("resume reattaches the new life's session", func(t *testing.T) {
		e, r0, r, sid, _ := rlfMessagedLife(t)
		rlfEndLife(t, e, r.ID)
		if got := rlfResumed(t, e, r.ID); got != sid {
			t.Errorf("--resume %s; want the new life's %s", got, sid)
		}
		rlfAssertEarlierUnused(t, e, r0, "")
	})
	t.Run("its transcript removed: ErrJsonlMissing names only the new life's candidates", func(t *testing.T) {
		e, r0, r, _, path := rlfMessagedLife(t)
		rlfEndLife(t, e, r.ID)
		if err := os.Remove(path); err != nil {
			t.Fatalf("remove %s: %v", path, err)
		}
		if _, err := os.Stat(r0.JSONLPath); err != nil {
			t.Fatalf("the earlier life's transcript: %v; want it on disk", err)
		}
		msg := rlfResumeRefused(t, e, r.ID, api.ErrJsonlMissing)
		for _, want := range []string{"persisted " + path, "fallback " + path} {
			if !strings.Contains(msg, want) {
				t.Errorf("%q does not name %q", msg, want)
			}
		}
		if strings.Contains(msg, "history ") {
			t.Errorf("%q names a history candidate; the new life has none", msg)
		}
		rlfAssertEarlierUnused(t, e, r0, msg)
	})
	t.Run("after one rotation get lists only that rotation's entry", func(t *testing.T) {
		e, _, r, sid, path := rlfMessagedLife(t)
		rlfReportIn(t, e, r.ID, rlfNewSession(), false)
		rlfAssertListed(t, rlfGet(t, e, r.ID), "rotated", getWantPrior{id: sid, path: path})
	})
}
