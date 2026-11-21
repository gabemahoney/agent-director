package api_test

// resume_test.go covers resume's refusals before its lookup, its launch, a
// move not applied and the restore (SR-8.1, SR-8.3, SR-8.5, SR-14, SR-20.6).
// Transcripts, lookup verdicts and "duplicate session" have their own files.

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// TestResumeRefusedBeforeLookup (SR-8.1 steps 1 and 2, SR-3.2, SR-3.13,
// SR-22.6; AC-RES-19, Epic 19): step 1's guards, an unusable recorded name, a
// control-character id and an uncreatable socket directory refuse before the
// lookup, though a session holds the name and a caller id is set: no tmux call,
// nothing written, no vanished socket directory made again. Step 1 beats the
// name, which beats the id.
func TestResumeRefusedBeforeLookup(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID with t.Setenv; it checks every record written to the
	// shared trail since its mark.
	unusable := []apitest.SpawnOption{apitest.WithTmuxSessionName(preGqeDefaultName)}
	rewritten := func(name string) func(resumeRow, string) apitest.DescCase {
		return func(resumeRow, string) apitest.DescCase {
			return apitest.DescUnusableNameRewritten(name, apitest.RewrittenChars{Dot: true})
		}
	}
	type refusal struct {
		name   string
		state  string // "" ended; "unknown": no row
		id     string // "" a fresh id
		bare   bool   // no session id
		gone   bool   // its transcript removed
		opts   []apitest.SpawnOption
		socket string // "vanished": its recorded socket's per-user directory is gone; "parent": that directory's parent too
		want   string
		desc   func(r resumeRow, socket string) apitest.DescCase // nil: not checked
	}
	cases := []refusal{
		// No session id, no transcript and none ever written are advice_follow_resume_test.go's B4-B6.
		{name: "unknown id", state: "unknown", want: "ErrSpawnNotFound"},
		{name: "live row", state: store.StateWaiting, want: "ErrSpawnNotResumable"},
		{name: "pending row", state: store.StatePending, want: "ErrSpawnNotResumable"},
		{name: "unusable name, live row", state: store.StateWaiting, opts: unusable, want: "ErrSpawnNotResumable"},
		{name: "unusable name, no session id", bare: true, opts: unusable, want: "ErrNoSessionId"},
		{name: "unusable name, transcript missing", gone: true, opts: unusable, want: "ErrJsonlMissing"},
		{name: "unusable name, its socket's per-user directory vanished", socket: "vanished", opts: unusable,
			want: "ErrInternal", desc: rewritten(preGqeDefaultName)},
		{name: "catalogue name mix.$b", opts: []apitest.SpawnOption{apitest.WithTmuxSessionName("mix.$b")},
			want: "ErrInternal", desc: rewritten("mix.$b")},
		{name: "control character in the id", id: "legacy\n" + uuid.NewString()[:8],
			opts: []apitest.SpawnOption{apitest.WithTmuxSessionName("ctl-" + uuid.NewString()[:8])}, want: "ErrInternal",
			desc: func(r resumeRow, _ string) apitest.DescCase { return apitest.DescResumeInstanceIDControlChar(r.ID) }},
		// The recorded name holds the id, so is unusable too: the name's refusal wins.
		{name: "control character in the id and the recorded name", id: "legacy\x1b" + uuid.NewString()[:8], want: "ErrInternal",
			desc: func(r resumeRow, _ string) apitest.DescCase { return apitest.DescUnusableNameControlChar(r.Name) }},
		{name: "socket directory cannot be made", socket: "parent", want: "ErrTmuxNotAvailable",
			desc: func(_ resumeRow, socket string) apitest.DescCase {
				refuse := &tmux.SocketDirError{Socket: socket, Dir: filepath.Dir(socket), Reason: tmux.SocketDirNotCreatable}
				return apitest.DescSocketDir(socket, refuse.Dir, refuse.Error())
			}},
	}
	for _, f := range unusableNameFixtures() {
		cases = append(cases, refusal{name: f.label, opts: []apitest.SpawnOption{apitest.WithTmuxSessionName(f.raw)},
			want: "ErrInternal", desc: func(resumeRow, string) apitest.DescCase { return f.desc }})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			caller := e.seedRow(t, killRowSpec{NoSession: true})
			t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", caller.ID)
			if tc.state == "unknown" {
				id, mark := "unknown-"+uuid.NewString()[:8], trailMark(t)
				_, err := e.resume(id)
				assertOneName(t, err, tc.want)
				e.assertKillCalls(t)
				assertResumeEvents(t, mark, id)
				return
			}
			spec := e.resumableSpec(rlkSettled(e), agentGone, tc.opts...)
			spec.ID = tc.id
			if tc.state != "" {
				spec.State = tc.state
			}
			socket, gone := "", ""
			switch tc.socket {
			case "vanished":
				socket = vanishedUserSocket(t)
				gone = filepath.Dir(socket)
			case "parent":
				socket = filepath.Join(userSocketDir(filepath.Join(t.TempDir(), "gone")), "default")
				gone = filepath.Dir(filepath.Dir(socket))
			}
			if socket != "" {
				spec.Opts = append(spec.Opts, apitest.WithTmuxSocket(socket))
			}
			var r resumeRow
			if tc.bare {
				r = e.seedOnServer(t, spec, e.seedRow)
			} else {
				r = e.seedResumableRow(t, spec)
			}
			e.seedHolder(t, r.killRow, holderNone)
			if tc.gone {
				if err := os.Remove(r.JSONLPath); err != nil {
					t.Fatalf("remove transcript: %v", err)
				}
			}
			before := e.snapshotResume(t, r)

			_, err := e.resume(r.ID)

			assertOneName(t, err, tc.want)
			if tc.desc != nil && err != nil {
				apitest.AssertDescription(t, err.Error(), tc.desc(r, socket), r.ID, caller.ID, r.Token)
			}
			e.assertKillCalls(t)
			e.assertResumeWroteNothing(t, before)
			if _, err := os.Stat(gone); gone != "" && !errors.Is(err, os.ErrNotExist) {
				t.Errorf("stat %s: %v; want still missing", gone, err)
			}
		})
	}
}

// pendStartedAt is the started_at the launch tests seed, far from the clock's
// time, so a launch start taken from started_at would show.
var pendStartedAt = time.Date(2026, 9, 27, 8, 0, 0, 0, time.UTC)

// TestResumeLaunch (SR-8.1 step 3, SR-8.3, SR-3.3, SR-3.5, SR-3.6, SR-14;
// AC-RES-08, AC-LKP-05, AC-LKP-17): the lookup alone precedes the move (the
// clock's launch start, a new token, the launch socket, the caller's parent);
// one create there, labelled (by id once the chained label fails), runs
// `claude --resume <session> --settings <json>` and the row's args and env;
// the identity write at the move's version records the reply's server and
// pane (none after a lost reply; hooks before it ignored); one
// ad.resume.moved_to_pending and no WARN. The columns the move clears and
// keeps, and a lost identity write, are internal/store's.
func TestResumeLaunch(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID with t.Setenv.
	cases := []struct {
		name, state string
		parent      bool   // a caller with a parent; else a bare shell
		socket      string // "vanished": its recorded socket's per-user directory is gone; "none": no recorded socket
		setup       func(t *testing.T, e *resumeEnv, r resumableRow)
		labelByID   bool // the chained label fails and the session is labelled by id
		noIdentity  bool // the identity write records nothing
	}{
		{name: "ended row, caller with a parent", state: store.StateEnded, parent: true},
		{name: "missing row, bare shell", state: store.StateMissing},
		{name: "recorded socket's per-user directory vanished", state: store.StateEnded, socket: "vanished"},
		{name: "no recorded socket", state: store.StateMissing, socket: "none"},
		{name: "chained label fails, labelled by id", state: store.StateEnded, labelByID: true,
			setup: func(_ *testing.T, e *resumeEnv, _ resumableRow) {
				e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailLabel, Times: 1}, tmux.CallCreate)
			}},
		{name: "lost reply", state: store.StateEnded, noIdentity: true, setup: func(_ *testing.T, e *resumeEnv, _ resumableRow) {
			e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 0, Applied: true}, tmux.CallCreate)
		}},
		// SR-22.9 (decision A7): the moved row records no pane until the
		// identity write, so hooks before it are ignored and it applies.
		// These drive the store's gated write directly; the hook handler's
		// SessionStart first waits for the identity write, bounded by the
		// pending grace (SR-13.4).
		{name: "hooks before the identity write ignored", state: store.StateEnded,
			setup: func(t *testing.T, e *resumeEnv, r resumableRow) {
				e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) {
					for _, ev := range []string{"Stop", "SessionStart"} {
						got := apitest.ApplyAgentHook(t, e.dbPath, r.ID, ev, r.SessionID, apitest.HookTranscript(r.JSONLPath, true))
						if got.Applied || got.Reason != store.HookReasonNoPaneRecorded {
							t.Errorf("%s before the identity write = %+v; want not applied, %s", ev, got, store.HookReasonNoPaneRecorded)
						}
					}
				})
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newResumeEnv(t)
			opts, socket := []apitest.SpawnOption{apitest.WithStartedAt(pendStartedAt), apitest.WithTurnStarts(6)}, e.socket
			switch tc.socket {
			case "vanished":
				socket = vanishedUserSocket(t)
				opts = append(opts, apitest.WithTmuxSocket(socket))
			case "none":
				opts = append(opts, apitest.WithNoLaunchToken())
			}
			r := e.seedRow(t, resumableSpec{State: tc.state, Opts: opts})
			priorBusy := seedBusyCoveredConversation(t, e.dbPath, r.ID)
			parent := ""
			if tc.parent {
				parent = pendParent(t, e)
				t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", parent)
			}
			// The reply's server and pane run, so the identity write reads their start times.
			e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) {
				srv, _ := e.rec.Server(socket)
				e.pc.Set(srv.PID, procfix.Alive(apitest.LinuxProcStarttime))
				for _, s := range e.rec.Sessions(socket) {
					e.pc.Set(s.Panes[0].PID, procfix.Alive(apitest.DarwinProcStarttime))
				}
			})
			if tc.setup != nil {
				tc.setup(t, e, r)
			}
			wantStart, mark := e.moveStart().UnixMilli(), trailMark(t)
			var moved apitest.SpawnColumns
			var atMove []tmux.Call
			e.store.afterMove(func() {
				moved, atMove = e.columns(t, r.ID), pendCallKinds(e.rec)
				assertBusyProjection(t, e.dbPath, r.ID, priorBusy)
				assertBusyLaunchPhase(t, e.dbPath, r.ID, "resume", "candidate", store.BusySendCovered)
			})

			res, err := e.resume(r.ID)

			if err != nil || res.InstanceID != r.ID {
				t.Fatalf("Resume = %+v, %v; want %s launched", res, err, r.ID)
			}
			assertBusyLaunchPhase(t, e.dbPath, r.ID, "resume", "current", store.BusySendCovered)
			if facts := busyFacts(t, e.dbPath, r.ID); facts.Binding != priorBusy.Binding || facts.Coverage != store.BusySendCovered {
				t.Fatal("resume lost conversation or coverage")
			}
			if cols := e.columns(t, r.ID); cols.TurnStarts != int64(0) {
				t.Fatalf("resume counter=%v", cols.TurnStarts)
			}
			calls := []tmux.Call{tmux.CallLookup, tmux.CallCreate}
			if tc.labelByID {
				calls = append(calls, tmux.CallSetLabel)
			}
			if got := callKinds(e.rec); !reflect.DeepEqual(got, calls) || !slices.Equal(atMove, calls[:1]) {
				t.Errorf("tmux calls = %v, %v by the move; want %v, the lookup alone by the move", got, atMove, calls)
			}
			for _, c := range e.rec.SocketCalls() {
				if c.Socket != socket {
					t.Errorf("%v on %q; want every call on the launch socket %q", c.Call, c.Socket, socket)
				}
			}
			if calls := e.rec.Calls(); len(calls) != 0 {
				t.Errorf("name-based tmux calls = %v; want none", calls)
			}

			// The move's arguments (the columns it clears and keeps are internal/store's TestMoveToPendingApplied).
			create := e.rec.SocketCallsOf(tmux.CallCreate)[0]
			tok, _ := moved.LaunchToken.(string)
			bv, _ := r.Before.RowVersion.(int64)
			if moved.State != store.StatePending || moved.LaunchStartedAt != wantStart || wantStart == pendStartedAt.UnixMilli() ||
				!spawnTokenRE.MatchString(tok) || tok == r.Identity.Token || create.Token != tok || moved.TmuxSocket != socket ||
				moved.ParentID != pendRowNullOr(parent) || moved.RowVersion != bv+1 {
				t.Errorf("row at the move = %+v; want pending, the clock's start %d, a new token the create labels with (%q), %s, parent %#v, version %d",
					moved, wantStart, create.Token, socket, pendRowNullOr(parent), bv+1)
			}

			// The create.
			cmd := create.Command
			if create.Target != r.Name || create.Cwd != r.CWD || create.InstanceID != r.ID || create.StoreID != e.storeID ||
				len(cmd) != 7 || cmd[0] != "claude" || cmd[1] != "--resume" || cmd[2] != r.SessionID || cmd[3] != "--settings" ||
				cmd[5] != "--model" || cmd[6] != "opus" {
				t.Errorf("create = %+v; want name %s, cwd %s, id %s, this store's id, claude --resume %s --settings <json> --model opus",
					create, r.Name, r.CWD, r.ID, r.SessionID)
			}
			for k, v := range map[string]string{"AGENT_DIRECTOR_INSTANCE_ID": r.ID, "AGENT_DIRECTOR_LABEL_PROJECT": "resume-fixture"} {
				if create.Envs[k] != v {
					t.Errorf("create env %s = %q; want %q", k, create.Envs[k], v)
				}
			}

			// The labelled session and the identity write.
			s := rplSessionNamed(t, e.rec, socket, r.Name)
			if s.Label != tmuxfix.Valid(tok, r.ID, e.storeID) || s.Panes[0].AdPane != tok {
				t.Errorf("session label %+v, pane label %q; want ad1 %s <$N> %s <store id>, pane %s", s.Label, s.Panes[0].AdPane, tok, r.ID, tok)
			}
			if tc.labelByID {
				l := e.rec.SocketCallsOf(tmux.CallSetLabel)[0]
				if l.Target != s.ID || l.PaneID != s.Panes[0].ID || l.Token != tok || l.InstanceID != r.ID || l.StoreID != e.storeID {
					t.Errorf("label by id = %+v; want session %s pane %s with {%s %s <store id>}", l, s.ID, s.Panes[0].ID, tok, r.ID)
				}
			}
			after := e.columns(t, r.ID)
			if after.Agent != "claude" {
				t.Errorf("agent after the resume = %v; want the row's claude kept", after.Agent)
			}
			srv, _ := e.rec.Server(socket)
			identity := []any{int64(srv.PID), srv.Start, apitest.LinuxProcStarttime, s.Panes[0].ID, int64(s.Panes[0].PID), apitest.DarwinProcStarttime}
			if tc.noIdentity {
				identity = noIdentity
			} else if mv, _ := moved.RowVersion.(int64); after.RowVersion != mv+1 {
				t.Errorf("row_version after the identity write = %#v; want %d", after.RowVersion, mv+1)
			}
			if got := identityCols(after); !reflect.DeepEqual(got, identity) {
				t.Errorf("identity columns = %#v; want %#v", got, identity)
			}

			// The trail and the log.
			assertResumeEvents(t, mark, r.ID, "ad.resume.moved_to_pending")
			if l := pendTrail(t, "ad.resume.moved_to_pending", r.ID); len(l) == 1 {
				assertAPITrailStr(t, l[0], "prior_state", tc.state)
				assertAPITrailStr(t, l[0], "session_id", r.SessionID)
				assertAPITrailStr(t, l[0], "source", "ad_resume")
			}
			if strings.Contains(e.logs.String(), "WARN") {
				t.Errorf("client log = %q; want no WARN", e.logs.String())
			}
			if fi, err := os.Stat(filepath.Dir(socket)); tc.socket == "vanished" && (err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700) {
				t.Errorf("socket directory %s: %v; want a 0700 directory", filepath.Dir(socket), err)
			}
		})
	}
}

// F1: publication uncertainty must preserve the baseline lost-reply success,
// independently of covered authority; never restore or recreate the launch.
func TestResumeLostReplyPublicationPreservesSuccess(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		agent    string
		coverage store.BusySendCoverage
	}{
		{"claude", store.BusySendCovered},
		{"codex", store.BusySendNotCovered},
	} {
		for _, replaced := range []bool{false, true} {
			t.Run(tc.agent+"/"+map[bool]string{false: "publication error", true: "candidate replaced"}[replaced], func(t *testing.T) {
				e := newResumeEnv(t)
				opts := []apitest.SpawnOption{apitest.WithNoPreTrust()}
				if tc.agent == "codex" {
					opts = append(opts, codexRowOpts(seedCodexFolder(t, t.TempDir()).env())...)
				}
				r := e.seedResumable(t, store.StateEnded, opts...)
				prior := busyFacts(t, e.dbPath, r.ID)
				if tc.coverage == store.BusySendCovered {
					prior = seedBusyCoveredConversation(t, e.dbPath, r.ID)
				}
				e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 0, Applied: true}, tmux.CallCreate)
				var replacement apitest.BusySendFacts
				var replacementRow apitest.SpawnColumns
				if !replaced {
					e.store.failPublication(nil)
				}
				e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) {
					if !replaced {
						return
					}
					row, err := e.st.GetSpawn(r.ID)
					if err != nil {
						t.Fatal(err)
					}
					if _, res, err := e.st.MarkMissingIfSameLife(r.ID, row.Snapshot); err != nil || res != store.CondApplied {
						t.Fatalf("finish replaced launch=%v,%v", res, err)
					}
					row, err = e.st.GetSpawn(r.ID)
					if err != nil {
						t.Fatal(err)
					}
					if res, _, err := e.st.MoveToPendingWithExit(r.ID, row.Snapshot, e.clock.Now().UnixMilli(), "1122334455667788", e.socket, "", store.LaunchOwner{},
						store.BusySendDefaultsChoice{}, exitEvidenceFor(t, e.st, r.ID)); err != nil || res != store.CondApplied {
						t.Fatalf("stage replacement=%v,%v", res, err)
					}
					replacement, replacementRow = busyFacts(t, e.dbPath, r.ID), e.columns(t, r.ID)
				})

				result, err := e.resume(r.ID)
				if err != nil || result.InstanceID != r.ID || result.PreTrust != "skipped" {
					t.Fatalf("lost-reply resume=%+v,%v; want baseline instance_id/pre_trust success", result, err)
				}

				if replaced {
					if facts := busyFacts(t, e.dbPath, r.ID); !reflect.DeepEqual(facts, replacement) || !reflect.DeepEqual(e.columns(t, r.ID), replacementRow) {
						t.Fatal("obsolete creator changed replacement facts or row")
					}
				} else {
					if logs := e.logs.String(); !strings.Contains(logs, "WARN") || !strings.Contains(logs, r.ID) || !strings.Contains(logs, errInjectedStore.Error()) {
						t.Fatalf("publication store error was not warned: %q", logs)
					}
					assertBusyLaunchPhase(t, e.dbPath, r.ID, "resume", "uncertain", tc.coverage)
				}
				assertBusyProjection(t, e.dbPath, r.ID, prior)
				facts := busyFacts(t, e.dbPath, r.ID)
				wantLaunches := 2
				if replaced {
					wantLaunches++
				}
				if len(facts.Launches) != wantLaunches || len(facts.Conversations) != 1 {
					t.Fatalf("post-create core facts=%+v", facts)
				}
				for _, launch := range facts.Launches {
					want := prior.Launches[0]
					if launch.ID == want.ID && tc.coverage == store.BusySendCovered {
						// A covered resume's staging records the earlier Launch's
						// End with its exit evidence (SR-6.2); the creator adds nothing.
						if launch.EndCause.String != "process_exited" || !launch.EndedAt.Valid || launch.Revision != want.Revision+1 {
							t.Fatalf("staging left the earlier Launch's End unrecorded: %+v", launch)
						}
						want.EndCause, want.EndedAt, want.Revision = launch.EndCause, launch.EndedAt, launch.Revision
					}
					if launch.ID == want.ID && !reflect.DeepEqual(launch, want) {
						t.Fatalf("creator changed the earlier published Launch: %+v", launch)
					}
					if !replaced && launch.Kind == "resume" && (launch.Token.String != e.store.moveToken || launch.Conversation != prior.Binding || launch.EndCause.Valid || launch.EndedAt.Valid) {
						t.Fatalf("failed publication lost exact candidate facts: %+v", launch)
					}
				}
				cols := e.columns(t, r.ID)
				if cols.State != store.StatePending || cols.LaunchOwnerPID != nil || cols.LaunchOwnerStarttime != nil || cols.LaunchOwnerPIDNS != nil {
					t.Fatalf("post-create row restored or owner retained: %+v", cols)
				}
				if e.store.restores != 0 || e.store.publications != 1 {
					t.Fatalf("restores=%d, publications=%d; want 0,1", e.store.restores, e.store.publications)
				}
				if got := callKinds(e.rec); !reflect.DeepEqual(got, []tmux.Call{tmux.CallLookup, tmux.CallCreate}) {
					t.Fatalf("post-create calls=%v; want one lookup and one create", got)
				}
				created := rplSessionNamed(t, e.rec, e.socket, r.Name)
				create := rstOneCreate(t, e)
				if created.Label != tmuxfix.Valid(create.Token, r.ID, e.storeID) || created.Panes[0].AdPane != create.Token {
					t.Fatal("failed publication lost the possibly running session")
				}
				if !replaced {
					adviceAssertManifest(t, "resume", "", "Lost tmux reply: success; no restore/retry.")
					adviceAssertManifest(t, "resume", "instance_id", "Successful creation with a lost tmux create reply returns instance_id and pre_trust for every agent, with no restoration or retry.")
					srv, _ := e.rec.Server(e.socket)
					e.pc.Set(srv.PID, procfix.Alive(apitest.LinuxProcStarttime))
					e.pc.Set(created.Panes[0].PID, procfix.Alive(apitest.DarwinProcStarttime))
					e.rec.SetCapture(e.socket, created.Panes[0].ID, "launch awaiting report-in")
					if got, err := api.Get(e.st, api.RelayView{}, r.ID); err != nil || got.State != store.StatePending {
						t.Fatalf("inspection get=%+v,%v", got, err)
					}
					if got, err := e.c.ReadPane(api.ReadPaneParams{InstanceID: r.ID}); err != nil || got.Pane != "launch awaiting report-in" {
						t.Fatalf("inspection read-pane=%+v,%v", got, err)
					}
					assertBusyLaunchPhase(t, e.dbPath, r.ID, "resume", "uncertain", tc.coverage)
					row, err := e.st.GetSpawn(r.ID)
					if err != nil {
						t.Fatal(err)
					}
					identity := store.LaunchIdentity{Token: create.Token, Socket: e.socket,
						ServerPID: srv.PID, ServerStart: srv.Start, ServerStarttime: apitest.LinuxProcStarttime,
						PaneID: created.Panes[0].ID, PanePID: created.Panes[0].PID, PaneStarttime: apitest.DarwinProcStarttime}
					if res, err := e.st.AdoptIdentityIfUnchanged(r.ID, row.Snapshot, identity); err != nil || res != store.CondApplied {
						t.Fatalf("later guarded adoption=%v,%v", res, err)
					}
					assertBusyLaunchPhase(t, e.dbPath, r.ID, "resume", "current", tc.coverage)
					if _, err := e.resume(r.ID); !errors.Is(err, api.ErrSpawnNotResumable) {
						t.Fatalf("pending launch retry=%v; want refusal", err)
					}
					if len(e.rec.SocketCallsOf(tmux.CallCreate)) != 1 || e.store.restores != 0 {
						t.Fatal("inspection/retry restored or duplicated the running launch")
					}
				}
			})
		}
	}
}

// TestResumeMoveNotApplied (SR-8.3, SR-8.6, SR-5.8; AC-RES-09, AC-RES-11):
// when a competing resume before or after this call's lookup changed the row,
// or the row was removed, or the move fails in the store, resume returns its
// error after its one lookup: no create, no restore, no ad.resume.* line, the
// row as the race left it.
func TestResumeMoveNotApplied(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID with t.Setenv (os.Setenv for the competing resume).
	competing := func(t *testing.T, e *resumeEnv, id string) {
		loser := os.Getenv("AGENT_DIRECTOR_INSTANCE_ID")
		os.Setenv("AGENT_DIRECTOR_INSTANCE_ID", pendParent(t, e)) //nolint:errcheck // put back below; t.Setenv restores it at cleanup
		defer os.Setenv("AGENT_DIRECTOR_INSTANCE_ID", loser)      //nolint:errcheck
		if _, err := e.resume(id); err != nil {
			t.Errorf("competing resume: %v", err)
		}
	}
	lostRace := apitest.DescResumeLostRace()
	cases := []struct {
		name        string
		race        func(t *testing.T, e *resumeEnv, id string) // nil: none
		afterLookup bool                                        // the race runs as this call's lookup returns, else after its read
		want        string
		desc        apitest.DescCase
		noSession   bool // the row records a launch token but no session of that launch
	}{
		// Another write after the read loses the move the same way: advice_follow_resume_test.go's B3.
		// The winner's session is up at this call's lookup, a leftover, so its one re-read finds the row changed.
		{"a competing resume moved it before the lookup", competing, false, "ErrSpawnNotResumable", lostRace, false},
		// b.1n6: the winner's young session is then this id's own abandoned launch, refused through the same re-read.
		{"a competing resume moved it before the lookup, no session recorded", competing, false, "ErrSpawnNotResumable",
			lostRace, true},
		{"a competing resume moved it after the lookup", competing, true, "ErrSpawnNotResumable", lostRace, false},
		{"the row removed", func(t *testing.T, e *resumeEnv, id string) {
			if err := storefix.DeleteWithExit(t, e.st, id); err != nil {
				t.Errorf("DeleteSpawn: %v", err)
			}
		}, false, "ErrSpawnNotFound", apitest.DescCase{Name: "ErrSpawnNotFound, removed before the move"}, false},
		{"store error", nil, false, "ErrInternal", apitest.DescResumeMoveStoreError(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newResumeEnv(t)
			var opts []apitest.SpawnOption
			if tc.noSession {
				opts = append(opts, apitest.WithLaunchIdentity(store.LaunchIdentity{Token: newToken(), Socket: e.socket}))
			}
			r := e.seedResumable(t, store.StateEnded, opts...)
			t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", pendParent(t, e))
			raced, present, raceLines := r.Before, true, 0
			var during [2]int // the race's own tmux calls, as indexes into SocketCalls
			race := func() {
				during[0] = len(e.rec.SocketCalls())
				tc.race(t, e, r.ID)
				during[1] = len(e.rec.SocketCalls())
				raced, present = rstColumns(t, e, r.ID)
				raceLines = len(rstTrail(t, r.ID))
			}
			switch {
			case tc.race == nil:
				e.store.failMove(nil)
			case tc.afterLookup:
				ran := false // the competing resume's own lookup must not rerun it
				e.rec.AfterCall(tmux.CallLookup, func(tmuxfix.SocketCall, error) {
					if !ran {
						ran = true
						race()
					}
				})
			default:
				e.store.afterGet(race)
			}

			_, err := e.resume(r.ID)

			assertOneName(t, err, tc.want)
			var forbid []string
			for _, s := range []any{r.Before.LaunchToken, e.storeID, e.store.moveToken, raced.LaunchToken} {
				if s, _ := s.(string); s != "" {
					forbid = append(forbid, s)
				}
			}
			_, desc := errnames.Classify(err)
			apitest.AssertDescription(t, desc, tc.desc, forbid...)
			var own []tmux.Call
			for i, c := range e.rec.SocketCalls() {
				if i < during[0] || i >= during[1] {
					own = append(own, c.Call)
				}
			}
			if !slices.Equal(own, []tmux.Call{tmux.CallLookup}) || len(e.rec.Calls()) != 0 {
				t.Errorf("this call's tmux calls = %v (%d name-based); want its one lookup", own, len(e.rec.Calls()))
			}
			if got, still := rstColumns(t, e, r.ID); still != present || !reflect.DeepEqual(got, raced) {
				t.Errorf("row after resume (present %v) = %+v; want as the race left it (present %v) %+v", still, got, present, raced)
			}
			if n := len(rstTrail(t, r.ID)); n != raceLines {
				t.Errorf("ad.resume.* lines = %d; want the race's %d", n, raceLines)
			}
		})
	}
}

// TestResumeRestore (SR-8.5, SR-5.8, SR-13.2 path (i), SR-14; AC-RES-06,
// AC-RES-07, AC-RES-12): an unresponsive create leaves the row pending; any
// other failure gets one restore, which applied puts the row back but for the
// move's parent, the earlier Launch's Turn Start counter included, which only
// resume's prior carries (b.7si SR-1.4; a second resume then launches), after
// another write or the row's removal writes nothing, and on a store error
// leaves it pending, counter 0, with one WARN. The error carries its sentence,
// the trail records it, and the call charges Q + C plus A per label or kill; a
// Codex row (SR-7.7) is charged the same.
func TestResumeRestore(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID with t.Setenv.
	create, label, kill := tmux.CallCreate, tmux.CallSetLabel, tmux.CallKillSession
	fails := tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1}
	once := func(s tmuxfix.Script) tmuxfix.Script { s.Times = 1; return s }
	unlabelled := map[tmux.Call]tmuxfix.Script{create: once(tmuxfix.Script{Failure: tmux.FailLabel}), label: fails}
	type descFn func(r resumableRow, s tmuxfix.SeedSession, rs apitest.ResumeRestore) apitest.DescCase
	createFailed := func(r resumableRow, _ tmuxfix.SeedSession, rs apitest.ResumeRestore) apitest.DescCase {
		return apitest.DescSessionCreateFailed(apitest.SessionCreateFailed{Name: r.Name}).AfterResumeRestore(rs)
	}
	notLabelled := func(ended bool) descFn {
		return func(r resumableRow, s tmuxfix.SeedSession, rs apitest.ResumeRestore) apitest.DescCase {
			return apitest.DescUnlabelledSession(apitest.UnlabelledSession{Name: r.Name, SessionID: s.ID, Ended: ended, Restore: rs})
		}
	}
	timedOut := func(unrecognised bool) descFn {
		return func(r resumableRow, _ tmuxfix.SeedSession, _ apitest.ResumeRestore) apitest.DescCase {
			return apitest.DescLaunchTimeout(apitest.LaunchTimeout{InstanceID: r.ID, Timeout: boundC, Unrecognised: unrecognised})
		}
	}
	_, a, _ := ceilDefaults()
	cases := []struct {
		name        string
		prior       string
		scripts     map[tmux.Call]tmuxfix.Script
		write       func(t *testing.T, e *resumeEnv, id string) // another write after the move (nil: none)
		onCreate    bool                                        // write runs as the create returns, else after the move
		failRestore bool
		noRestore   bool // the create is unresponsive: the row stays pending, no restore is attempted
		want        string
		calls       []tmux.Call // after the lookup and the create
		desc        descFn
		outcome     apitest.RestoreOutcome
		again       bool // a second resume launches
		codex       bool // the row is a Codex row, its CODEX_HOME a seeded folder under a test HOME
	}{
		{name: "create timed out", prior: store.StateEnded, scripts: map[tmux.Call]tmuxfix.Script{create: {Failure: tmux.FailTimeout}},
			noRestore: true, want: "ErrTmuxUnresponsive", desc: timedOut(false)},
		{name: "non-zero exit, unparseable reply", prior: store.StateMissing, noRestore: true, want: "ErrTmuxUnresponsive",
			scripts: map[tmux.Call]tmuxfix.Script{create: {Failure: tmux.FailUnrecognized, ExitStatus: 1, HadStdout: true}}, desc: timedOut(true)},
		{name: "create failed", prior: store.StateEnded, scripts: map[tmux.Call]tmuxfix.Script{create: once(fails)},
			want: "ErrTmuxSessionCreate", desc: createFailed, outcome: apitest.RestoreApplied, again: true},
		{name: "tmux not available", prior: store.StateMissing,
			scripts: map[tmux.Call]tmuxfix.Script{create: once(tmuxfix.Script{Failure: tmux.FailUnavailable})},
			want:    "ErrTmuxNotAvailable", outcome: apitest.RestoreApplied, again: true,
			desc: func(_ resumableRow, _ tmuxfix.SeedSession, rs apitest.ResumeRestore) apitest.DescCase {
				return apitest.DescTmuxNotRun().AfterResumeRestore(rs)
			}},
		{name: "session not labelled, killed by id", prior: store.StateEnded, scripts: unlabelled, calls: []tmux.Call{label, kill},
			want: "ErrTmuxSessionCreate", desc: notLabelled(true), outcome: apitest.RestoreApplied, again: true},
		{name: "session not labelled, its kill failed", prior: store.StateMissing,
			scripts: map[tmux.Call]tmuxfix.Script{create: unlabelled[create], label: fails, kill: fails}, calls: []tmux.Call{label, kill},
			want: "ErrTmuxSessionCreate", desc: notLabelled(false), outcome: apitest.RestoreApplied},
		{name: "row changed on the failing create", prior: store.StateEnded, scripts: map[tmux.Call]tmuxfix.Script{create: fails},
			write: func(t *testing.T, e *resumeEnv, id string) {
				if err := e.st.SetParentID(id, pendParent(t, e)); err != nil {
					t.Errorf("SetParentID: %v", err)
				}
			}, onCreate: true, want: "ErrTmuxSessionCreate", desc: createFailed, outcome: apitest.RestoreRowChanged},
		{name: "row removed after the move", prior: store.StateEnded, scripts: map[tmux.Call]tmuxfix.Script{create: fails},
			write: func(t *testing.T, e *resumeEnv, id string) {
				// Its conversation is covered: deleted as administrator
				// deletion does, with the test judging every owned process gone.
				row, err := e.st.GetSpawn(id)
				if err != nil {
					t.Errorf("GetSpawn: %v", err)
					return
				}
				scope, err := e.st.ReadBusySendExitScope(id)
				if err != nil {
					t.Errorf("ReadBusySendExitScope: %v", err)
					return
				}
				if res, err := e.st.DeleteBusySendCovered(id, row.Snapshot, store.BusySendExitEvidence{Scope: scope}); err != nil || res != store.CondApplied {
					t.Errorf("DeleteBusySendCovered = %v, %v; want applied", res, err)
				}
			}, want: "ErrTmuxSessionCreate", desc: createFailed, outcome: apitest.RestoreRowRemoved},
		{name: "restore store error", prior: store.StateEnded, scripts: map[tmux.Call]tmuxfix.Script{create: fails},
			failRestore: true, want: "ErrTmuxSessionCreate", desc: createFailed, outcome: apitest.RestoreStoreError},
		{name: "Codex row, create timed out", codex: true, prior: store.StateEnded,
			scripts:   map[tmux.Call]tmuxfix.Script{create: {Failure: tmux.FailTimeout}},
			noRestore: true, want: "ErrTmuxUnresponsive", desc: timedOut(false)},
		{name: "Codex row, create failed", codex: true, prior: store.StateEnded, scripts: map[tmux.Call]tmuxfix.Script{create: once(fails)},
			want: "ErrTmuxSessionCreate", desc: createFailed, outcome: apitest.RestoreApplied, again: true},
		{name: "Codex row, session not labelled, killed by id", codex: true, prior: store.StateEnded, scripts: unlabelled,
			calls: []tmux.Call{label, kill}, want: "ErrTmuxSessionCreate", desc: notLabelled(true), outcome: apitest.RestoreApplied, again: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newResumeEnv(t)
			parent := pendParent(t, e)
			t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", parent)
			opts := []apitest.SpawnOption{apitest.WithTurnStarts(4)} // the earlier Launch's counter, which the move resets
			if tc.codex {
				home := t.TempDir()
				t.Setenv("HOME", home)
				opts = append(opts, codexRowOpts(seedCodexFolder(t, filepath.Join(home, "codex")).env())...)
			}
			r := e.seedResumable(t, tc.prior, opts...)
			// These exercise local restore: Codex stays uncovered; a covered
			// Codex conversation would require a separate covering recipient.
			priorBusy := busyFacts(t, e.dbPath, r.ID)
			resumeCoverage := store.BusySendNotCovered
			if !tc.codex {
				priorBusy = seedBusyCoveredConversation(t, e.dbPath, r.ID)
				resumeCoverage = store.BusySendCovered
			}
			for call, s := range tc.scripts {
				e.rec.Script(tmuxfix.AnySocket, s, call)
			}
			var created tmuxfix.SeedSession
			e.rec.AfterCall(create, func(tmuxfix.SocketCall, error) {
				if s := e.rec.Sessions(e.socket); len(s) > 0 {
					created = s[0]
				}
			})
			var written apitest.SpawnColumns
			present := true
			if tc.write != nil {
				write := func() { tc.write(t, e, r.ID); written, present = rstColumns(t, e, r.ID) }
				if tc.onCreate {
					e.rec.AfterCall(create, func(tmuxfix.SocketCall, error) { write() })
				} else {
					e.store.afterMove(write)
				}
			}
			if tc.failRestore {
				e.store.failRestore(nil)
			}
			called, start, mark := e.clock.Now(), e.moveStart().UnixMilli(), trailMark(t)

			_, err := e.resume(r.ID)

			if tc.outcome != apitest.RestoreRowRemoved {
				assertBusyProjection(t, e.dbPath, r.ID, priorBusy)
				phase := "failed"
				if tc.noRestore || tc.outcome == apitest.RestoreRowChanged || tc.failRestore {
					phase = "uncertain"
				}
				assertBusyLaunchPhase(t, e.dbPath, r.ID, "resume", phase, resumeCoverage)
			}
			assertOneName(t, err, tc.want)
			if got, want := callKinds(e.rec), append([]tmux.Call{tmux.CallLookup, create}, tc.calls...); !reflect.DeepEqual(got, want) {
				t.Errorf("tmux calls = %v; want %v", got, want)
			}
			// SR-13.2 path (i): Q + C, and A per label or kill by id (Q + C + 2A at most).
			if got, want := e.clock.Now().Sub(called), resumeLookupQ+boundC+time.Duration(len(tc.calls))*a; got != want {
				t.Errorf("virtual time = %v; want %v", got, want)
			}
			for _, c := range e.rec.SocketCalls() {
				if c.Socket != e.socket || (c.Call == kill && c.Target != created.ID) {
					t.Errorf("%v on %q (target %q); want every call on %s, a kill by the session's id %s", c.Call, c.Socket, c.Target, e.socket, created.ID)
				}
			}
			c := rstOneCreate(t, e)
			restore := apitest.ResumeRestore{Outcome: tc.outcome}
			if tc.outcome == apitest.RestoreApplied {
				restore.PriorState = tc.prior
			}
			if err != nil {
				apitest.AssertDescription(t, err.Error(), tc.desc(r, created, restore), c.Token, e.storeID, r.Identity.Token,
					tmuxfix.LabelValue(c.Token, created.ID, r.ID, e.storeID))
			}
			moved := rstMoved(r, start, c.Token, e.socket)
			moved.ParentID = parent
			switch {
			case tc.noRestore || tc.outcome == apitest.RestoreStoreError:
				rstAssertRow(t, e, r.ID, moved)
			case tc.outcome == apitest.RestoreApplied:
				rstAssertRow(t, e, r.ID, rstRestored(r, parent))
			default: // the other write stands
				if got, still := rstColumns(t, e, r.ID); still != present || !reflect.DeepEqual(got, written) {
					t.Errorf("row after resume (present %v) = %+v; want as the write left it (present %v) %+v", still, got, present, written)
				}
			}
			if tc.calls != nil && (len(e.rec.Sessions(e.socket)) == 0) != tc.again {
				t.Errorf("sessions = %+v; want the unlabelled session present only when its kill failed", e.rec.Sessions(e.socket))
			}

			// The trail and the log.
			if m := pendTrail(t, "ad.resume.moved_to_pending", r.ID); len(m) != 1 || m[0]["prior_state"] != tc.prior ||
				m[0]["session_id"] != r.SessionID || m[0]["source"] != "ad_resume" {
				t.Errorf("ad.resume.moved_to_pending = %v; want prior_state %s, session_id %s, source ad_resume", m, tc.prior, r.SessionID)
			}
			var restoreErr any
			if tc.failRestore {
				restoreErr = errInjectedStore.Error()
			}
			if tc.noRestore {
				assertResumeEvents(t, mark, r.ID, "ad.resume.moved_to_pending")
			} else {
				assertResumeEvents(t, mark, r.ID, "ad.resume.moved_to_pending", "ad.resume.restored")
				rstAssertRestoredTrail(t, r.ID, tc.outcome == apitest.RestoreApplied, tc.want, restoreErr)
			}
			logs := strings.TrimSpace(e.logs.String())
			if l := strings.Split(logs, "\n"); tc.failRestore && (len(l) != 1 || !strings.Contains(l[0], "WARN") ||
				!strings.Contains(l[0], r.ID) || strings.Contains(l[0], c.Token)) {
				t.Errorf("client log = %q; want one WARN line naming %s and no token", logs, r.ID)
			} else if !tc.failRestore && logs != "" {
				t.Errorf("client log = %q; want nothing", logs)
			}

			// AC-RES-12: with the cause gone, an immediate second resume launches.
			if tc.again {
				if _, err := e.resume(r.ID); err != nil {
					t.Fatalf("second resume: %v", err)
				}
				if st, n := e.columns(t, r.ID).State, len(e.rec.SocketCallsOf(create)); st != store.StatePending || n != 2 {
					t.Errorf("after the second resume: state %v, creates %d; want pending, 2", st, n)
				}
				assertBusyLaunchPhase(t, e.dbPath, r.ID, "resume", "current", resumeCoverage)
				if facts := busyFacts(t, e.dbPath, r.ID); facts.Binding != priorBusy.Binding || facts.Coverage != resumeCoverage {
					t.Fatalf("retry lost retained conversation coverage: %+v", facts)
				}
			}
		})
	}
}

// rstResumeAsync runs e.resume in its own goroutine, so a hook may end it with
// runtime.Goexit; returned is false when it was ended that way.
func rstResumeAsync(e *resumeEnv, id string) (err error, returned bool) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err = e.resume(id)
		returned = true
	}()
	<-done
	return err, returned
}

// TestResumeRestoreSkippedOnUnresponsiveCreate: a resume stopped after its
// move, before its create, leaves the row pending with its launch start and
// new token, and attempts no restore (an unresponsive create is
// TestResumeRestore's).
func TestResumeRestoreSkippedOnUnresponsiveCreate(t *testing.T) {
	t.Parallel()
	e := newResumeEnv(t)
	r := e.seedResumable(t, store.StateEnded)
	e.store.afterMove(func() { runtime.Goexit() })
	start, mark := e.moveStart(), trailMark(t)

	err, returned := rstResumeAsync(e, r.ID)

	if got := callKinds(e.rec); returned || !reflect.DeepEqual(got, []tmux.Call{tmux.CallLookup}) {
		t.Fatalf("resume returned %v (err %v) with calls %v; want it stopped after the lookup, before any other tmux call", returned, err, got)
	}
	token, _ := e.columns(t, r.ID).LaunchToken.(string)
	if !spawnTokenRE.MatchString(token) || token == r.Identity.Token {
		t.Errorf("launch_token = %q; want a new 16-hex token", token)
	}
	rstAssertRow(t, e, r.ID, rstMoved(r, start.UnixMilli(), token, e.socket))
	assertResumeEvents(t, mark, r.ID) // no ad.resume.restored: no restore attempted
	if e.logs.Len() != 0 {
		t.Errorf("client log = %q; want nothing", e.logs.String())
	}
}

// TestResumeRestoreReuseAfterFailedResume (AC-RES-12's reuse half; SR-8.5,
// SR-10.2, SR-10.4): after a restored resume failure, with the cause removed,
// a reuse launches (Gone, the retained life); while a holder holds the name it is
// refused and writes nothing.
func TestResumeRestoreReuseAfterFailedResume(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	cases := []struct {
		name, prior string
		want        error
		// arrange sets up the resume's failure; remove (nil: a spent script) takes its cause away.
		arrange func(*testing.T, *killEnv, reuseRow) (remove func())
	}{
		{"launch failure", store.StateEnded, api.ErrTmuxSessionCreate, func(_ *testing.T, e *killEnv, r reuseRow) func() {
			e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1, Times: 1}, tmux.CallCreate)
			return nil
		}},
		{"duplicate session with a holder", store.StateMissing, api.ErrTmuxSessionConflict, func(t *testing.T, e *killEnv, r reuseRow) func() {
			sc := e.arrangeHeld(t, r.resumeRow, heldSpec{Holder: holderNone})
			return func() { e.removeHolders(t, sc) }
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/"+tc.prior, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedReusable(t, agentGone, reuseRowSpec{State: tc.prior, Held: true, Age: rlkSettled(e), Bare: true})
			remove := tc.arrange(t, e, r)

			_, err := e.resume(r.ID)

			assertOneSentinel(t, err, tc.want)
			if cols := e.columns(t, r.ID); cols.State != tc.prior || cols.LifeNumber != reuseLife {
				t.Fatalf("after the failed resume: {state %v, life %#v}; want restored to %s, life %d", cols.State, cols.LifeNumber, tc.prior, reuseLife)
			}
			if rs := pendTrail(t, "ad.resume.restored", r.ID); len(rs) != 1 || rs[0]["applied"] != true {
				t.Fatalf("ad.resume.restored = %v; want one applied", rs)
			}
			p := reuseParams(t, r, reuseRequest{})
			if remove != nil {
				held := e.snapshotReuse(t, r)
				_, logs, err := e.reuse(t, p)
				assertOneSentinel(t, err, api.ErrTmuxSessionConflict)
				if err == nil {
					t.Fatalf("reuse while the holder holds %q succeeded (log %q)", r.Name, logs)
				}
				// The failed resume already pre-trusted this cwd, so the trust file is no longer as seeded.
				e.assertWroteNothing(t, held, exceptTrust)
				remove()
			}
			before := e.snapshotReuse(t, r)

			if _, logs, err := e.reuse(t, p); err != nil {
				t.Fatalf("reuse with the cause removed: %v (log %q)", err, logs)
			}

			if cols := e.columns(t, r.ID); cols.State != store.StatePending || cols.LifeNumber != reuseLife {
				t.Errorf("after the reuse: {state %v, life %#v}; want pending, life %d", cols.State, cols.LifeNumber, reuseLife)
			}
			if got := callKinds(e.rec)[before.calls:]; !slices.Equal(got, []tmux.Call{tmux.CallLookup, tmux.CallCreate}) {
				t.Errorf("reuse tmux calls = %v; want its lookup, then the create", got)
			}
			gone := (tmux.Result{Verdict: tmux.Gone}).Token()
			if rs := before.since(t, "ad.spawn.reused"); len(rs) != 1 || rs[0]["lookup_outcome"] != gone || rs[0]["prior_state"] != tc.prior {
				t.Errorf("ad.spawn.reused = %v; want one with lookup_outcome %s, prior_state %s", rs, gone, tc.prior)
			}
		})
	}
}
