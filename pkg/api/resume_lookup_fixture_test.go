package api_test

// resume_lookup_fixture_test.go extends the kill fixture (killEnv, killRow)
// for resume's pre-launch lookup (SR-8.2, SR-20.2, SR-20.6): resumable
// finished rows, the instant the starting-session rule reads, the name
// holders, the resume runners and the "wrote nothing" snapshot. It holds no
// tests. resumeEnv (resume_fixture_test.go) keeps the move, restore and
// pending tests.

import (
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// resumeRow is a resumable seeded row: the kill fixture's row, its cwd, its
// transcript and the config directory (CLAUDE_CONFIG_DIR) whose .claude.json
// pre-trust writes.
type resumeRow struct {
	killRow
	CWD, JSONLPath string
	Trust          trustConfig
}

// ruleInstant is when resume's starting-session rule reads the clock if
// resume is called next: one lookup (resumeLookupQ) after e.clock's now. It
// first advances e.clock, at most once, so that instant is a whole second,
// like a stored ended_at and a session's creation time.
func (e *killEnv) ruleInstant() time.Time {
	at := e.clock.Now().Add(resumeLookupQ)
	if frac := at.Sub(at.Truncate(time.Second)); frac > 0 {
		e.clock.Advance(time.Second - frac)
		at = at.Add(time.Second - frac)
	}
	return at
}

// createdBefore is the seed option giving a session the creation time d
// before ruleInstant (a negative d is in the future).
func (e *killEnv) createdBefore(d time.Duration) tmuxfix.RowSessionOption {
	return tmuxfix.WithRowSessionCreated(e.ruleInstant().Add(-d).Unix())
}

// resumableSpec is an ended row that ended age before ruleInstant, its
// agent in state a and no session seeded; opts go last.
func (e *killEnv) resumableSpec(age time.Duration, a agentState, opts ...apitest.SpawnOption) killRowSpec {
	return killRowSpec{State: store.StateEnded, Agent: a, NoSession: true,
		Opts: append([]apitest.SpawnOption{apitest.WithEndedAt(e.ruleInstant().Add(-age))}, opts...)}
}

// seedResumable seeds resumableSpec's row through seedResumableRow.
func (e *killEnv) seedResumable(t *testing.T, age time.Duration, a agentState, opts ...apitest.SpawnOption) resumeRow {
	t.Helper()
	return e.seedResumableRow(t, e.resumableSpec(age, a, opts...))
}

// seedResumableRow seeds spec's row made resumable: a session id and cwd (when
// spec gives none), its transcript under a config directory whose .claude.json
// lacks the cwd's entry, e.defaultSocket as its socket, and its recorded
// server running there with a bystander session (an empty listing names no
// server), so a normal resume's lookup reads Gone with no
// ad.provenance.disagree; spec.Opts still go last.
func (e *killEnv) seedResumableRow(t *testing.T, spec killRowSpec) resumeRow {
	t.Helper()
	if spec.SessionID == "" {
		spec.SessionID = "sess-" + uuid.NewString()[:8]
	}
	if spec.CWD == "" {
		spec.CWD = t.TempDir()
	}
	trust := seedTrustConfig(t, t.TempDir(), trustLacksEntry)
	jsonl := apitest.SeedJsonlUnder(t, trust.dir, spec.CWD, spec.SessionID)
	spec.Opts = append([]apitest.SpawnOption{apitest.WithTmuxSocket(e.defaultSocket), apitest.WithJsonlPath(jsonl),
		trust.env()}, spec.Opts...)
	r := resumeRow{killRow: e.seedRow(t, spec), CWD: spec.CWD, JSONLPath: jsonl, Trust: trust}
	e.ensureServer(&r.killRow)
	e.seedBystander(t, r.Socket)
	e.syncServers()
	return r
}

// holderKind is a session seeded on a row's socket by seedHolder: one
// holding the row's recorded name (in tmux's stored form) by its label's
// class, or one that holds nothing. The kinds from holderCurrent on serve
// resume's re-lookup after "duplicate session" (arrangeHeld,
// resume_held_fixture_test.go).
type holderKind int

const (
	holderOld                 holderKind = iota // r.old(): an earlier launch of the row
	holderForeign                               // another seeded row's current label
	holderOtherStore                            // another store's label for another id
	holderOtherStoreOwn                         // another store's label with the row's id and token
	holderNone                                  // no label
	holderMalformed                             // an @ad_owner value that does not parse
	holderOtherStoreElsewhere                   // another store's label with the row's id and token, under another name
	holderPrefixNeighbour                       // no label, under the row's name plus a suffix
	holderCurrent                               // r.current(): the row's own session for its examined token
	holderAmbiguous                             // two unlabelled sessions under the name: more than one entry matches
	holderConflicting                           // no label, with a malformed global scope value: conflicting labels
	holderVanished                              // nothing holds the name
)

// seedHolder seeds k's session on r's socket, with one new pane, and returns
// it as stored (the first of holderAmbiguous's two; the zero session for
// holderVanished); for holderForeign the label's instance id is the other
// row's.
func (e *killEnv) seedHolder(t *testing.T, r killRow, k holderKind) tmuxfix.SeedSession {
	t.Helper()
	placed := e.placeHolder(t, r, k, e.holderSessions(t, r, k))
	if len(placed) == 0 {
		return tmuxfix.SeedSession{}
	}
	return placed[0]
}

// holderSessions is k's sessions for r, not yet seeded (holderForeign seeds
// its other row now): under storedFormOf(r.Name) unless Elsewhere or
// Neighbour, each valid label's one pane carrying its token.
func (e *killEnv) holderSessions(t *testing.T, r killRow, k holderKind) []tmuxfix.SeedSession {
	t.Helper()
	s := tmuxfix.SeedSession{Name: storedFormOf(r.Name), LabelSet: true}
	switch k {
	case holderOld:
		s.Label = r.old()
	case holderForeign:
		s.Label = e.seedRow(t, killRowSpec{State: store.StateEnded, Agent: agentGone, NoSession: true}).current()
	case holderOtherStore:
		s.Label = tmuxfix.Valid(newToken(), "other-"+uuid.NewString()[:8], apitest.OtherStoreID(r.StoreID))
	case holderOtherStoreOwn:
		s.Label = r.otherStore(r.Token)
	case holderNone, holderAmbiguous, holderConflicting:
		s.LabelSet = false
	case holderOtherStoreElsewhere:
		s.Name, s.Label = "elsewhere-"+uuid.NewString()[:8], r.otherStore(r.Token)
	case holderPrefixNeighbour:
		s.Name, s.LabelSet = s.Name+"-neighbour", false
	case holderCurrent:
		s.Label = r.current()
	case holderVanished:
		return nil
	}
	if s.Label.Kind == tmux.LabelValid {
		s.Panes = []tmuxfix.SeedPane{{AdPane: s.Label.Token}}
	}
	if k == holderAmbiguous {
		return []tmuxfix.SeedSession{s, s}
	}
	return []tmuxfix.SeedSession{s}
}

// placeHolder seeds sessions (holderSessions' for k) on r's socket, its
// server bound when none is, sets holderConflicting's malformed global scope
// value, and returns the sessions as stored, in seeding order.
func (e *killEnv) placeHolder(t *testing.T, r killRow, k holderKind, sessions []tmuxfix.SeedSession) []tmuxfix.SeedSession {
	t.Helper()
	e.ensureServer(&r)
	var placed []tmuxfix.SeedSession
	for _, s := range sessions {
		before := map[string]bool{}
		for _, got := range e.rec.Sessions(r.Socket) {
			before[got.ID] = true
		}
		e.rec.SeedSessions(r.Socket, s)
		for _, got := range e.rec.Sessions(r.Socket) {
			if !before[got.ID] {
				placed = append(placed, got)
			}
		}
	}
	if len(placed) != len(sessions) {
		t.Fatalf("seeded %d of %d holder sessions on %s", len(placed), len(sessions), r.Socket)
	}
	if k == holderConflicting {
		e.rec.SetScope(r.Socket, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{})
	}
	return placed
}

// storedFormOf is the name tmux stores and lists for raw (the replay
// catalogue's StoredNames), raw itself when the catalogue has no entry.
func storedFormOf(raw string) string {
	for _, n := range tmuxfix.StoredNames() {
		if n.Raw == raw {
			return n.Stored
		}
	}
	return raw
}

// resume runs resumeWith with a hookedResumeStore over e.st.
func (e *killEnv) resume(id string) (api.ResumeResult, error) {
	return e.resumeWith(&hookedResumeStore{st: e.st}, id)
}

// resumeWith runs api.Resume (export_test.go) on id with s, e.rec, e.pc,
// config.Default() with e.cfg as its [tmux], e.storeID and e.clock.Now;
// nothing is logged.
func (e *killEnv) resumeWith(s api.ResumeStore, id string) (api.ResumeResult, error) {
	cfg := config.Default()
	cfg.Tmux = e.cfg
	return api.Resume(s, e.rec, e.pc, cfg, e.storeID, e.clock.Now, nil, api.ResumeParams{ClaudeInstanceID: id})
}

// resumeClient runs Client.Resume on id on a new e.client with settings
// (the bound and window as configured); logs is the Client's captured log.
func (e *killEnv) resumeClient(t *testing.T, id string, settings ...apitest.TmuxSetting) (res api.ResumeResult, logs string, err error) {
	t.Helper()
	c, buf := e.client(t, settings...)
	res, err = c.Resume(api.ResumeParams{ClaudeInstanceID: id})
	return res, buf.String(), err
}

// resumeDisagrees returns id's ad.provenance.disagree records written by resume.
func resumeDisagrees(t *testing.T, id string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range pendTrail(t, "ad.provenance.disagree", id) {
		if l["verb"] == "resume" {
			out = append(out, l)
		}
	}
	return out
}

// resumeSnapshot is what a refused resume of r must leave as it was: the
// row, its session history and permission requests, the trail length, the
// Recorder's call counts and every bound socket's sessions.
type resumeSnapshot struct {
	r                      resumeRow
	cols                   apitest.SpawnColumns
	history                []apitest.HistoryEntry
	perms                  []api.PermissionRow
	mark, calls, nameCalls int
	sessions               map[string][]tmuxfix.SeedSession
}

// snapshotResume takes r's resumeSnapshot; take it just before the resume.
func (e *killEnv) snapshotResume(t *testing.T, r resumeRow) resumeSnapshot {
	t.Helper()
	history, err := apitest.ReadSessionHistoryAllLives(e.dbPath, r.ID)
	if err != nil {
		t.Fatalf("ReadSessionHistoryAllLives(%s): %v", r.ID, err)
	}
	perms, err := e.st.PermissionRequestsForSpawn(r.ID)
	if err != nil {
		t.Fatalf("PermissionRequestsForSpawn(%s): %v", r.ID, err)
	}
	s := resumeSnapshot{r: r, cols: e.columns(t, r.ID), history: history, perms: perms, mark: trailMark(t),
		calls: len(e.rec.SocketCalls()), nameCalls: len(e.rec.Calls()), sessions: map[string][]tmuxfix.SeedSession{}}
	for _, srv := range e.rec.Servers() {
		s.sessions[srv.Socket] = e.rec.Sessions(srv.Socket)
	}
	s.sessions[r.Socket] = e.rec.Sessions(r.Socket)
	return s
}

// assertResumeWroteNothing fails unless, since before was taken, resume made
// at most one tmux call, a lookup, and changed nothing: the row, its history,
// permission requests and trust entry, every seeded session, and the trail
// apart from ad.provenance.disagree records.
func (e *killEnv) assertResumeWroteNothing(t *testing.T, before resumeSnapshot) {
	t.Helper()
	id := before.r.ID
	e.assertRowUnchanged(t, id, before.cols)
	if got, err := apitest.ReadSessionHistoryAllLives(e.dbPath, id); err != nil || !reflect.DeepEqual(got, before.history) {
		t.Errorf("session history of %s = %+v (%v); want unchanged %+v", id, got, err, before.history)
	}
	if got, err := e.st.PermissionRequestsForSpawn(id); err != nil || !reflect.DeepEqual(got, before.perms) {
		t.Errorf("permission requests of %s = %+v (%v); want unchanged %+v", id, got, err, before.perms)
	}
	before.r.Trust.check(t, before.r.CWD, false, "after the refused resume")
	for _, l := range readAPITrailLines(t)[before.mark:] {
		if l["event"] != "ad.provenance.disagree" {
			t.Errorf("trail record %v for %v; want none but ad.provenance.disagree", l["event"], l["claude_instance_id"])
		}
	}
	if calls := e.rec.SocketCalls()[before.calls:]; len(calls) > 1 || (len(calls) == 1 && calls[0].Call != tmux.CallLookup) {
		t.Errorf("tmux calls = %+v; want at most one lookup", calls)
	}
	if n := len(e.rec.Calls()) - before.nameCalls; n != 0 {
		t.Errorf("%d name-based tmux calls; want none", n)
	}
	for socket, want := range before.sessions {
		if got := e.rec.Sessions(socket); !reflect.DeepEqual(got, want) {
			t.Errorf("sessions on %s changed:\n got %+v\nwant %+v", socket, got, want)
		}
	}
}
