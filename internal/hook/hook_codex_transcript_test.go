package hook_test

// hook_codex_transcript_test.go — a Codex hook's session id and transcript,
// the hook heal and session rotation through hook.Handle (SR-6.3, SR-6.4, RN-6,
// RN-10, SR-6.6's session half), beside the SessionStart gate tests
// (hook_gate_sessionstart_test.go). The Codex session id is the payload's
// session_id, never read from the rollout's file name; the store's own heal
// guards are internal/store's.

import (
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/codexfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

const (
	// cxSession is the session id of the Codex fixtures user-prompt-submit, stop
	// and session-start-startup.
	cxSession = "019f0000-0000-7000-8000-000000000001"
	// cxReviewSession is the main session the /review thread's hooks carry.
	cxReviewSession = "019f0000-0000-7000-8000-000000000007"
)

// TestCodexSessionIdAndTranscript: on a row that records no session id, the
// first applied Codex hook records the payload's session_id (never the id in the
// rollout's file name, which differs) and records the transcript path only when
// the file exists; a hook that carries agent_id records neither.
func TestCodexSessionIdAndTranscript(t *testing.T) {
	cases := []struct {
		fixture string
		present bool
		session bool // the hook records the payload's session id
	}{
		{"session-start-startup.json", true, true},
		{"session-start-startup.json", false, true},
		{"user-prompt-submit.json", true, true},
		{"user-prompt-submit.json", false, true},
		{"pre-tool-use-subagent.json", true, false},
		{"post-tool-use-subagent.json", true, false},
		{"permission-request-subagent.json", true, false},
	}
	for _, tc := range cases {
		name := tc.fixture + "/file absent"
		if tc.present {
			name = tc.fixture + "/file present"
		}
		t.Run(name, func(t *testing.T) {
			id := "cx-session-" + strings.NewReplacer(".", "-", "/", "-", " ", "-").Replace(name)
			st, _ := cxSeed(t, id, store.StatePending)
			payload, sid, path := codexPayload(t, tc.fixture, tc.present, nil)

			cxFire(t, st, id, cxOwn(t, st, id), payload)

			row := mustGetSpawn(t, st, id)
			wantSession, wantPath := "", ""
			if tc.session {
				wantSession = sid
				if tc.present {
					wantPath = path
				}
			}
			if row.SessionID != wantSession || row.JSONLPath != wantPath {
				t.Errorf("session/path = %q/%q; want %q/%q", row.SessionID, row.JSONLPath, wantSession, wantPath)
			}
			if stem := strings.TrimSuffix(filepath.Base(path), ".jsonl"); row.SessionID == stem && row.SessionID != "" {
				t.Errorf("session id %q is the rollout's file stem; want the payload's session_id", row.SessionID)
			}
		})
	}
}

// TestCodexHookHeal: a Codex row with a session id and a NULL transcript path
// takes the path from its next applied hook that has no agent_id, the same
// session id and an existing file whose name carries the session id, with
// exactly one ad.session.jsonl_healed. Each no-heal condition below writes none
// and leaves the path NULL.
func TestCodexHookHeal(t *testing.T) {
	cases := []struct {
		name string
		// row is the row's session id; "" = cxSession.
		row     string
		fixture string
		extra   func(t *testing.T) map[string]any
		absent  bool // the transcript file is not on disk
		claude  bool // a Claude row fired by a Claude hook
		foreign bool // another process fires it
		path    string
		heal    bool
	}{
		{name: "heals", fixture: "user-prompt-submit.json", heal: true},
		{name: "heals from a Stop", fixture: "stop.json", heal: true},
		{name: "another session id", row: "019f0000-0000-7000-8000-000000000099", fixture: "user-prompt-submit.json"},
		{name: "file absent", fixture: "user-prompt-submit.json", absent: true},
		{name: "path already set", fixture: "user-prompt-submit.json", path: "/x/already-set.jsonl"},
		{name: "gate refusal", fixture: "user-prompt-submit.json", foreign: true},
		{name: "a Claude row", fixture: "user-prompt-submit.json", claude: true},
		// The file's name carries the session id; only the agent_id keeps it from healing.
		{name: "hook with agent_id", row: "019f0000-0000-7000-8000-000000000014", fixture: "pre-tool-use-subagent.json",
			extra: func(t *testing.T) map[string]any {
				return map[string]any{"transcript_path": codexfix.TempRolloutPath(t, "019f0000-0000-7000-8000-000000000014", true)}
			}},
		// The /review thread: the main session id, the Review thread's own rollout.
		{name: "review thread hook", row: cxReviewSession, fixture: "pre-tool-use-review-thread.json"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "cx-heal-" + strconv.Itoa(i)
			session := tc.row
			if session == "" {
				session = cxSession
			}
			var opts []apitest.SpawnOption
			if tc.path != "" {
				opts = append(opts, apitest.WithJsonlPath(tc.path))
			}
			var extra map[string]any
			if tc.extra != nil {
				extra = tc.extra(t)
			}
			payload, _, path := codexPayload(t, tc.fixture, !tc.absent, extra)
			var st *store.Store
			var p hookParent
			if tc.claude {
				// Claude's hooks never heal: its row names the session its transcript file names.
				path = writeTranscript(t, session)
				payload = payloadWith(t, "user-prompt-submit.json", map[string]any{"session_id": session, "transcript_path": path})
				st, _ = ssgSeed(t, id, store.StateWaiting, session, opts...)
				p = agentParent(t, st, id)
			} else {
				st, _ = cxSeedSession(t, id, store.StateWaiting, session, opts...)
				p = cxOwn(t, st, id)
				if tc.foreign {
					p = cxForeign(t, st, id)
				}
			}
			prior := mustGetSpawn(t, st, id)

			run := cxFire(t, st, id, p, payload)

			healed := run.lines("ad.session.jsonl_healed")
			row := mustGetSpawn(t, st, id)
			if !tc.heal {
				if len(healed) != 0 {
					t.Errorf("ad.session.jsonl_healed = %v; want none", healed)
				}
				if row.JSONLPath != tc.path {
					t.Errorf("jsonl_path = %q; want %q (unchanged)", row.JSONLPath, tc.path)
				}
				if tc.foreign {
					assertRowUnchanged(t, st, id, prior)
				}
				return
			}
			if row.JSONLPath != path || row.SessionID != session {
				t.Errorf("jsonl_path/session = %q/%q; want healed %q/%q", row.JSONLPath, row.SessionID, path, session)
			}
			if row.RowVersion != prior.RowVersion+2 {
				t.Errorf("RowVersion = %d; want %d (the hook's write and the heal)", row.RowVersion, prior.RowVersion+2)
			}
			want := map[string]any{"instance_id": id, "session_id": session, "jsonl_path": path, "source": "ad_spawn_store"}
			if len(healed) != 1 {
				t.Fatalf("ad.session.jsonl_healed lines = %d (%v); want exactly 1", len(healed), healed)
			}
			for k, v := range want {
				if healed[0][k] != v {
					t.Errorf("ad.session.jsonl_healed %s = %v; want %v", k, healed[0][k], v)
				}
			}
		})
	}
}

// cxLive is a live Codex row recording session old and its path (life 2) and a pane.
func cxLive(t *testing.T, id, state string) (*store.Store, string) {
	t.Helper()
	return cxSeedSession(t, id, state, ssgOutgoing, apitest.WithJsonlPath("/x/"+ssgOutgoing+".jsonl"),
		apitest.WithLifeNumber(2), apitest.WithLaunchIdentity(ssgFreshPane))
}

// TestCodexSessionStartRotation: a Codex SessionStart with a new session id
// (startup after /new, clear, fork, in-session resume) archives the old id and
// path into the session history and records the new id and its rollout; one with
// the row's own id archives nothing; the SessionStart of a resumed row brings
// back the resumed session's own id, so nothing is archived either.
func TestCodexSessionStartRotation(t *testing.T) {
	oldPair := []ssgArchived{{ssgOutgoing, "/x/" + ssgOutgoing + ".jsonl", 2}}
	for _, source := range []string{"startup", "clear", "fork", "resume"} {
		t.Run("new session, "+source, func(t *testing.T) {
			id := "cx-rotate-" + source
			st, dbPath := cxLive(t, id, store.StateWorking)
			payload, sid, path := codexPayload(t, "session-start-"+source+".json", true, nil)

			cxFire(t, st, id, cxOwn(t, st, id), payload)

			row := mustGetSpawn(t, st, id)
			if row.State != store.StateWaiting || row.SessionID != sid || row.JSONLPath != path {
				t.Errorf("state/session/path = %q/%q/%q; want waiting and the payload's %q/%q", row.State, row.SessionID, row.JSONLPath, sid, path)
			}
			if got := ssgHistory(t, dbPath, id); !reflect.DeepEqual(got, oldPair) {
				t.Errorf("session_history = %+v; want %+v", got, oldPair)
			}
		})
	}
	t.Run("same session id", func(t *testing.T) {
		const id = "cx-rotate-same"
		payload, sid, path := codexPayload(t, "session-start-startup.json", true, nil)
		st, dbPath := cxSeedSession(t, id, store.StateWorking, sid, apitest.WithJsonlPath(path),
			apitest.WithLifeNumber(2), apitest.WithLaunchIdentity(ssgFreshPane))

		cxFire(t, st, id, cxOwn(t, st, id), payload)

		row := mustGetSpawn(t, st, id)
		if row.State != store.StateWaiting || row.SessionID != sid || row.JSONLPath != path {
			t.Errorf("state/session/path = %q/%q/%q; want waiting and kept %q/%q", row.State, row.SessionID, row.JSONLPath, sid, path)
		}
		if got := ssgHistory(t, dbPath, id); len(got) != 0 {
			t.Errorf("session_history = %+v; want none (the same id archives nothing)", got)
		}
	})
	t.Run("resumed row brings back its own id", func(t *testing.T) {
		const id = "cx-rotate-resumed"
		payload, sid, path := codexPayload(t, "session-start-resume.json", true, nil)
		st, dbPath := cxResumed(t, id, sid)

		cxFire(t, st, id, cxOwn(t, st, id), payload)

		row := mustGetSpawn(t, st, id)
		if row.State != store.StateWaiting || row.SessionID != sid {
			t.Errorf("state/session = %q/%q; want waiting and the resumed session %q", row.State, row.SessionID, sid)
		}
		if row.JSONLPath != path {
			t.Errorf("jsonl_path = %q; want the payload's %q", row.JSONLPath, path)
		}
		if got := ssgHistory(t, dbPath, id); len(got) != 0 {
			t.Errorf("session_history = %+v; want none (the resumed id is the row's own)", got)
		}
	})
}

// cxResumed is ssgResumed for a Codex row whose kept session is sid: ended, then
// moved to pending by resume with a new pane recorded.
func cxResumed(t *testing.T, id, sid string) (*store.Store, string) {
	t.Helper()
	st, dbPath := cxSeedSession(t, id, store.StateEnded, sid,
		apitest.WithJsonlPath("/x/"+sid+".jsonl"), apitest.WithLaunchIdentity(ssgOldPane))
	res, moved, err := st.MoveToPending(id, mustGetSpawn(t, st, id).Snapshot, time.Now().UnixMilli(),
		ssgNewPane.Token, ssgNewPane.Socket, "", store.LaunchOwner{})
	if err != nil || res != store.CondApplied {
		t.Fatalf("MoveToPending(%q) = %v, %v; want applied", id, res, err)
	}
	if res, err := st.RecordLaunchIdentity(id, moved, ssgNewPane.Token, ssgNewPane); err != nil || res != store.CondApplied {
		t.Fatalf("RecordLaunchIdentity(%q) = %v, %v; want applied", id, res, err)
	}
	return st, dbPath
}
