package api_test

// resume_lookup_fixture_test.go extends the kill fixture (killEnv, killRow)
// for resume's pre-launch lookup (SR-8.2, SR-20.2, SR-20.6): resumable
// finished rows, the instant the starting-session rule reads, the name
// holders, the resume runners and the "wrote nothing" snapshot. It holds no
// tests. The "wrote nothing" check is verb-neutral (writesSnapshot,
// assertWroteNothing; reuse's in spawn_reuse_fixture_test.go). resumeEnv
// (resume_fixture_test.go) keeps the move, restore and pending tests.

import (
	"reflect"
	"slices"
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

// seedResumableRow seeds spec's row made resumable: a session id (when spec
// gives none), then seedOnServer through seedRow.
func (e *killEnv) seedResumableRow(t *testing.T, spec killRowSpec) resumeRow {
	t.Helper()
	if spec.SessionID == "" {
		spec.SessionID = "sess-" + uuid.NewString()[:8]
	}
	return e.seedOnServer(t, spec, e.seedRow)
}

// seedOnServer seeds spec's row through seed (seedRow, or seedRawRow for rows
// GetSpawn refuses) with a cwd (when spec gives none), a config directory
// whose .claude.json lacks the cwd's entry, its transcript there (when it has
// a session id), e.defaultSocket as its socket, and its recorded server
// running there with a bystander session, so a normal lookup reads Gone with
// no ad.provenance.disagree; spec.Opts
// still go last.
func (e *killEnv) seedOnServer(t *testing.T, spec killRowSpec, seed func(*testing.T, killRowSpec) killRow) resumeRow {
	t.Helper()
	if spec.CWD == "" {
		spec.CWD = t.TempDir()
	}
	trust := seedTrustConfig(t, t.TempDir(), trustLacksEntry)
	opts := []apitest.SpawnOption{apitest.WithTmuxSocket(e.defaultSocket), trust.env()}
	var jsonl string
	if spec.SessionID != "" {
		jsonl = apitest.SeedJsonlUnder(t, trust.dir, spec.CWD, spec.SessionID)
		opts = append(opts, apitest.WithJsonlPath(jsonl))
	}
	spec.Opts = append(opts, spec.Opts...)
	r := resumeRow{killRow: seed(t, spec), CWD: spec.CWD, JSONLPath: jsonl, Trust: trust}
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
	holderOtherStoreOldToken                    // another store's label with the row's id and an earlier token
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
	case holderOtherStoreOldToken:
		s.Label = r.otherStore(tmuxfix.OtherToken)
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
	return verbDisagrees(t, "resume", id)
}

// verbDisagrees returns id's ad.provenance.disagree records written by verb
// (reuse's are verb spawn).
func verbDisagrees(t *testing.T, verb, id string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range pendTrail(t, "ad.provenance.disagree", id) {
		if l["verb"] == verb {
			out = append(out, l)
		}
	}
	return out
}

// writesSnapshot is what a refused call of verb on row id must leave as it
// was: the row's raw columns, its history over every life, its permission
// requests, its children's ids (rows whose parent_id is id), the trust file
// (trust; none when its dir is ""), the trail length, the Recorder's call
// counts and every bound socket's sessions.
type writesSnapshot struct {
	verb, id               string
	trust                  trustConfig
	cols                   apitest.SpawnColumns
	history                []apitest.HistoryEntry
	perms                  []api.PermissionRow
	children               []string
	mark, calls, nameCalls int
	sessions               map[string][]tmuxfix.SeedSession
}

// resumeSnapshot is a resume's writesSnapshot with the resumed row.
type resumeSnapshot struct {
	writesSnapshot
	r resumeRow
}

// snapshotResume takes r's resumeSnapshot; take it just before the resume.
func (e *killEnv) snapshotResume(t *testing.T, r resumeRow) resumeSnapshot {
	t.Helper()
	return resumeSnapshot{writesSnapshot: e.snapshotWrites(t, "resume", r.ID, r.Trust, r.Socket), r: r}
}

// assertResumeWroteNothing is assertWroteNothing for a refused resume.
func (e *killEnv) assertResumeWroteNothing(t *testing.T, before resumeSnapshot) {
	t.Helper()
	e.assertWroteNothing(t, before.writesSnapshot)
}

// snapshotWrites takes id's writesSnapshot for a call of verb, with trust and
// the sessions of every bound socket and of sockets; take it just before the call.
// A later test proving a refusal of any verb wrote nothing uses it with
// assertWroteNothing, never its own copy.
func (e *killEnv) snapshotWrites(t *testing.T, verb, id string, trust trustConfig, sockets ...string) writesSnapshot {
	t.Helper()
	history, err := apitest.ReadSessionHistoryAllLives(e.dbPath, id)
	if err != nil {
		t.Fatalf("ReadSessionHistoryAllLives(%s): %v", id, err)
	}
	perms, err := e.st.PermissionRequestsForSpawn(id)
	if err != nil {
		t.Fatalf("PermissionRequestsForSpawn(%s): %v", id, err)
	}
	s := writesSnapshot{verb: verb, id: id, trust: trust, cols: e.columns(t, id), history: history, perms: perms,
		children: e.childIDs(t, id), mark: trailMark(t), calls: len(e.rec.SocketCalls()), nameCalls: len(e.rec.Calls()),
		sessions: map[string][]tmuxfix.SeedSession{}}
	for _, srv := range e.rec.Servers() {
		s.sessions[srv.Socket] = e.rec.Sessions(srv.Socket)
	}
	for _, socket := range sockets {
		s.sessions[socket] = e.rec.Sessions(socket)
	}
	return s
}

// childIDs returns the sorted ids of the rows whose parent_id is id.
func (e *killEnv) childIDs(t *testing.T, id string) []string {
	t.Helper()
	rows, err := e.st.ListSpawns(store.ListFilters{Parent: id})
	if err != nil {
		t.Fatalf("ListSpawns(parent %s): %v", id, err)
	}
	ids := []string{}
	for _, r := range rows {
		ids = append(ids, r.ClaudeInstanceID)
	}
	slices.Sort(ids)
	return ids
}

// since returns the snapshot row's event records written after it was taken.
func (s writesSnapshot) since(t *testing.T, event string) []map[string]any {
	t.Helper()
	return ptRecords(t, s.mark, event, s.id)
}

// wroteNothingExcept names an item assertWroteNothing leaves unchecked.
type wroteNothingExcept int

// exceptTrust skips the trust file: a reuse's reset that is not applied
// runs after its pre-trust.
const exceptTrust wroteNothingExcept = 1

// assertWroteNothing fails unless, since before was taken, the call made at
// most one tmux call, a lookup, and changed nothing: the row (every column,
// raw), its history, permission requests and children, the trust file
// (unless exceptTrust), every seeded session, and the trail apart from
// ad.provenance.disagree records.
func (e *killEnv) assertWroteNothing(t *testing.T, before writesSnapshot, except ...wroteNothingExcept) {
	t.Helper()
	id := before.id
	e.assertRowUnchanged(t, id, before.cols)
	if got, err := apitest.ReadSessionHistoryAllLives(e.dbPath, id); err != nil || !reflect.DeepEqual(got, before.history) {
		t.Errorf("session history of %s = %+v (%v); want unchanged %+v", id, got, err, before.history)
	}
	if got, err := e.st.PermissionRequestsForSpawn(id); err != nil || !reflect.DeepEqual(got, before.perms) {
		t.Errorf("permission requests of %s = %+v (%v); want unchanged %+v", id, got, err, before.perms)
	}
	if got := e.childIDs(t, id); !slices.Equal(got, before.children) {
		t.Errorf("children of %s = %q; want unchanged %q", id, got, before.children)
	}
	if before.trust.dir != "" && !slices.Contains(except, exceptTrust) {
		before.trust.check(t, "", false, "after the refused "+before.verb)
	}
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
