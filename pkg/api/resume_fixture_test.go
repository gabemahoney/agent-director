package api_test

// resume_fixture_test.go is the shared resume test fixture (SR-20.3, SR-20.6):
// resumeEnv (a real store, a tmuxfix.Recorder on the test clock, TestMain's
// TMUX_TMPDIR, a captured logger and a Client on the same store file),
// hookedResumeStore (api.ResumeStore over the real store with injected store
// errors and interleaving hooks), the resumable-row factory and the row, call
// and trail checks the resume tests share. It holds no
// tests. HOME is left as TestMain set it, so readAPITrailLines
// (find_missing_trail_test.go) reads the trail resume writes. The pre-launch
// lookup's arrangements (holders, the row's own session, process states) are
// on the kill fixture: resume_lookup_fixture_test.go.

import (
	"bytes"
	"errors"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// errInjectedStore is hookedResumeStore's default injected store error.
var errInjectedStore = errors.New("injected store failure: database is locked")

// hookedResumeStore implements api.ResumeStore (and RecordLaunchIdentity)
// over a real store: a test can fail the move or the restore (failMove,
// failRestore) and run a function once after GetSpawn (afterGet) or the move
// (afterMove), cleared before it runs, so a nested resume does not rerun it.
// moveToken is the last move's token. Safe for concurrent use.
type hookedResumeStore struct {
	st *store.Store

	hookLock
	moveErr    error
	restoreErr error
	afterGetFn func()
	afterMvFn  func()
	moveToken  string
}

// failMove makes every later MoveToPending return err (nil: errInjectedStore), writing nothing.
func (w *hookedResumeStore) failMove(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.moveErr = orInjected(err)
}

// failRestore makes every later RestoreAfterFailedResume return err (nil: errInjectedStore), writing nothing.
func (w *hookedResumeStore) failRestore(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.restoreErr = orInjected(err)
}

// afterGet runs fn once, when the next GetSpawn returns (after the examination, before the move).
func (w *hookedResumeStore) afterGet(fn func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.afterGetFn = fn
}

// afterMove runs fn once, when the next MoveToPending returns (before the create).
func (w *hookedResumeStore) afterMove(fn func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.afterMvFn = fn
}

// orInjected returns err, or errInjectedStore when err is nil.
func orInjected(err error) error {
	if err == nil {
		return errInjectedStore
	}
	return err
}

// hookLock guards a hooked store wrapper's injected errors and one-shot hooks
// (hookedResumeStore, hookedReuseStore in spawn_reuse_fixture_test.go).
type hookLock struct{ mu sync.Mutex }

// take returns *fn and clears it, under the lock.
func (h *hookLock) take(fn *func()) func() {
	h.mu.Lock()
	defer h.mu.Unlock()
	f := *fn
	*fn = nil
	return f
}

// injected returns *err under the lock.
func (h *hookLock) injected(err *error) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return *err
}

// GetSpawn delegates, then runs the afterGet hook.
func (w *hookedResumeStore) GetSpawn(instanceID string) (api.Spawn, error) {
	row, err := w.st.GetSpawn(instanceID)
	if fn := w.take(&w.afterGetFn); fn != nil {
		fn()
	}
	return row, err
}

// ListSessionHistory delegates.
func (w *hookedResumeStore) ListSessionHistory(instanceID string, life int64) ([]api.SessionHistoryEntry, error) {
	return w.st.ListSessionHistory(instanceID, life)
}

// MoveToPending delegates unless failMove is set, then runs the afterMove hook.
func (w *hookedResumeStore) MoveToPending(instanceID string, examined api.RowSnapshot, launchStartedAtMillis int64, token, socket, parentID string, owner api.LaunchOwner) (api.CondResult, int64, error) {
	var (
		res     api.CondResult
		version int64
		err     = w.injected(&w.moveErr)
	)
	w.mu.Lock()
	w.moveToken = token
	w.mu.Unlock()
	if err == nil {
		res, version, err = w.st.MoveToPending(instanceID, examined, launchStartedAtMillis, token, socket, parentID, owner)
	}
	if fn := w.take(&w.afterMvFn); fn != nil {
		fn()
	}
	return res, version, err
}

// RestoreAfterFailedResume delegates unless failRestore is set.
func (w *hookedResumeStore) RestoreAfterFailedResume(instanceID string, movedVersion int64, prior api.ResumePrior) (api.CondResult, error) {
	if err := w.injected(&w.restoreErr); err != nil {
		return 0, err
	}
	return w.st.RestoreAfterFailedResume(instanceID, movedVersion, prior)
}

// RecordLaunchIdentity delegates, so resume's identity write reaches the store.
func (w *hookedResumeStore) RecordLaunchIdentity(instanceID string, launchVersion int64, token string, id api.LaunchIdentity) (api.CondResult, error) {
	return w.st.RecordLaunchIdentity(instanceID, launchVersion, token, id)
}

// ReleaseLaunchOwner delegates, so resume's release of its hold reaches the store (b.kdf).
func (w *hookedResumeStore) ReleaseLaunchOwner(instanceID, token string) (api.CondResult, error) {
	return w.st.ReleaseLaunchOwner(instanceID, token)
}

// resumeEnv is what a resume test drives: store, the wrapper resume gets over
// st; rec, the Recorder on clock's virtual time; c, a Client on the same store
// and clock; socket, the default socket, whose per-user directory exists.
type resumeEnv struct {
	dbPath  string
	st      *store.Store
	store   *hookedResumeStore
	rec     *tmuxfix.Recorder
	clock   *tmuxfix.Clock
	pc      *procfix.Checker
	logs    *bytes.Buffer
	lg      *log.Logger
	cfg     config.Config
	c       *api.Client
	storeID string
	socket  string
}

// resumeLookupQ is the virtual time charged for resume's pre-launch lookup: the default Q.
var resumeLookupQ = config.Tmux{}.EffectiveQueryTimeout()

// newResumeEnv builds a resumeEnv over a fresh store, with TestMain's
// TMUX_TMPDIR, no caller instance id and config.Default(). It sets no
// environment its test has not already changed, so the test may run in parallel.
func newResumeEnv(t *testing.T) *resumeEnv {
	t.Helper()
	setenvIfChanged(t, "AGENT_DIRECTOR_INSTANCE_ID", "")
	tmpdir := useSharedTmuxTmpdir(t)

	dir := t.TempDir()
	e := &resumeEnv{dbPath: filepath.Join(dir, "state.db"), logs: &bytes.Buffer{}, pc: procfix.New(),
		clock: tmuxfix.NewClock(time.Date(2026, 9, 30, 12, 0, 0, 456_000_000, time.UTC)),
		cfg:   config.Default(), socket: filepath.Join(userSocketDir(tmpdir), "default")}
	e.lg = log.New(e.logs, "", 0)
	e.rec = tmuxfix.NewRecorder().WithVirtualTime(e.clock, tmux.Timeouts{})

	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, nil, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	var err error
	if e.c, err = api.New(api.Options{StorePath: e.dbPath, ConfigPath: cfgPath, CreateIfMissing: true,
		Logger: e.lg, TmuxClient: e.rec}); err != nil {
		t.Fatalf("api.New: %v", err)
	}
	t.Cleanup(func() { _ = e.c.Close() })
	api.SetClockForTest(e.c, e.clock.Now)
	api.SetProcCheckerForTest(e.c, e.pc)

	if e.st, err = store.Open(e.dbPath); err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = e.st.Close() })
	e.store = &hookedResumeStore{st: e.st}
	e.storeID = e.st.StoreID()
	return e
}

// resume runs resume on id through the export_test seam with e's parts.
func (e *resumeEnv) resume(id string) (api.ResumeResult, error) {
	return api.Resume(e.store, e.rec, e.pc, e.cfg, e.storeID, e.clock.Now, e.lg,
		api.ResumeParams{ClaudeInstanceID: id})
}

// moveStart is the launch start a resume called now records: now plus the lookup's Q.
func (e *resumeEnv) moveStart() time.Time { return e.clock.Now().Add(resumeLookupQ) }

// columns reads id's row raw through apitest.ReadSpawnColumns, failing the test on an error.
func (e *resumeEnv) columns(t *testing.T, id string) apitest.SpawnColumns {
	t.Helper()
	cols, err := apitest.ReadSpawnColumns(e.dbPath, id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns(%s): %v", id, err)
	}
	return cols
}

// resumableSpec overrides seedRow's defaults: a zero field takes its default
// and Opts are applied after the default options, so they win.
type resumableSpec struct {
	ID        string // default "resume-<8 hex>"
	State     string // default ended
	SessionID string // default "sess-<id>"
	Opts      []apitest.SpawnOption
}

// resumableRow is what seedRow seeded, as defaults; Before, the raw columns
// after seeding, also reflects spec.Opts.
type resumableRow struct {
	ID, State, SessionID, CWD, JSONLPath, Name string
	Identity                                   store.LaunchIdentity
	Life                                       int64
	HistorySessionID, HistoryJSONLPath         string
	Before                                     apitest.SpawnColumns
}

// Values seedRow stores in the columns resume's move clears or keeps.
const (
	resumableEndedAt   = "2026-09-28 10:11:12"
	resumableUnverSnc  = "2026-09-28 10:05:00"
	resumableNote      = "probe: EACCES reading the process start time"
	resumableLife      = int64(3)
	resumableServerPID = apitest.TestPanePID + 1
	resumableClaudePID = apitest.TestPanePID + 2
	resumableSrvStart  = int64(1_790_000_000)
)

// seedResumable seeds a resumable row in state (ended or missing) with every
// default of seedRow; opts are applied last.
func (e *resumeEnv) seedResumable(t *testing.T, state string, opts ...apitest.SpawnOption) resumableRow {
	t.Helper()
	return e.seedRow(t, resumableSpec{State: state, Opts: opts})
}

// seedRow seeds a finished row with a value in every column the move clears
// or keeps (a full launch identity on e.socket, life 3, one history entry of
// that life) and writes both transcripts.
func (e *resumeEnv) seedRow(t *testing.T, spec resumableSpec) resumableRow {
	t.Helper()
	r := resumableRow{ID: spec.ID, State: spec.State, SessionID: spec.SessionID, CWD: t.TempDir(), Life: resumableLife}
	if r.ID == "" {
		r.ID = "resume-" + uuid.NewString()[:8]
	}
	if r.State == "" {
		r.State = store.StateEnded
	}
	if r.SessionID == "" {
		r.SessionID = "sess-" + r.ID
	}
	r.Name = "ts-" + r.ID
	r.HistorySessionID = "hist-" + r.ID
	r.JSONLPath = apitest.SeedJsonl(t, r.CWD, r.SessionID)
	r.HistoryJSONLPath = apitest.SeedJsonl(t, r.CWD, r.HistorySessionID)
	r.Identity = store.LaunchIdentity{
		Token:           strings.ReplaceAll(uuid.NewString(), "-", "")[:16],
		Socket:          e.socket,
		ServerPID:       resumableServerPID,
		ServerStart:     resumableSrvStart,
		ServerStarttime: apitest.LinuxProcStarttime,
		PaneID:          apitest.TestPaneID,
		PanePID:         apitest.TestPanePID,
		PaneStarttime:   apitest.LinuxProcStarttime,
	}
	opts := append([]apitest.SpawnOption{
		apitest.WithPID(resumableClaudePID),
		apitest.WithProcStarttime(apitest.LinuxProcStarttime),
		apitest.WithEndedAt(resumableEndedAt),
		apitest.WithLivenessUnverifiedSince(resumableUnverSnc),
		apitest.WithLivenessNote(resumableNote),
		apitest.WithLaunchIdentity(r.Identity),
		apitest.WithJsonlPath(r.JSONLPath),
		apitest.WithLifeNumber(r.Life),
		apitest.WithRawLabels(`{"project":"resume-fixture"}`),
		apitest.WithRawClaudeArgs(`["--model","opus"]`),
		apitest.WithExtraEnv(map[string]string{"RESUME_FIXTURE": "on"}),
		apitest.WithSessionHistory(apitest.SessionHistorySeed{
			SessionID: r.HistorySessionID, JSONLPath: r.HistoryJSONLPath, Life: r.Life}),
	}, spec.Opts...)
	if _, err := apitest.SeedSpawn(e.dbPath, r.ID, r.State, r.CWD, "off", r.SessionID, false, opts...); err != nil {
		t.Fatalf("SeedSpawn(%s): %v", r.ID, err)
	}
	r.Before = e.columns(t, r.ID)
	return r
}

// pendParent seeds a live row a caller can name as its parent and returns its id.
func pendParent(t *testing.T, e *resumeEnv) string {
	t.Helper()
	id, err := apitest.SeedSpawn(e.dbPath, "parent-"+uuid.NewString()[:8], store.StateWaiting, t.TempDir(), "off", "", false)
	if err != nil {
		t.Fatalf("SeedSpawn(parent): %v", err)
	}
	return id
}

// rstColumns reads id's row raw; present is false when the row is gone.
func rstColumns(t *testing.T, e *resumeEnv, id string) (cols apitest.SpawnColumns, present bool) {
	t.Helper()
	cols, err := apitest.ReadSpawnColumns(e.dbPath, id)
	if errors.Is(err, store.ErrSpawnNotFound) {
		return apitest.SpawnColumns{}, false
	}
	if err != nil {
		t.Fatalf("ReadSpawnColumns(%s): %v", id, err)
	}
	return cols, true
}

// rstRestored is r's row after a move and an applied restore with parent.
func rstRestored(r resumableRow, parent any) apitest.SpawnColumns {
	w := r.Before
	w.ParentID = parent
	w.RowVersion = r.Before.RowVersion.(int64) + 2
	return w
}

// rstMoved is r's row after the move alone, with no parent.
func rstMoved(r resumableRow, launchStart int64, token, socket string) apitest.SpawnColumns {
	w := r.Before
	w.State, w.LaunchStartedAt, w.LaunchToken, w.TmuxSocket, w.ParentID = store.StatePending, launchStart, token, socket, nil
	w.PID, w.ProcStarttime, w.EndedAt, w.LivenessUnverifiedSince, w.LivenessNote = nil, nil, nil, nil, nil
	w.TmuxServerPID, w.TmuxServerStarted, w.TmuxServerStarttime, w.PaneID, w.PanePID, w.PaneStarttime = nil, nil, nil, nil, nil, nil
	w.RowVersion = r.Before.RowVersion.(int64) + 1
	return w
}

// rstAssertRow fails unless id's row equals want column for column.
func rstAssertRow(t *testing.T, e *resumeEnv, id string, want apitest.SpawnColumns) {
	t.Helper()
	if got := e.columns(t, id); !reflect.DeepEqual(got, want) {
		t.Errorf("row %s =\n  %+v\nwant\n  %+v", id, got, want)
	}
}

// rstTrail returns id's ad.resume.* trail lines, in the order they were written.
func rstTrail(t *testing.T, id string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range readAPITrailLines(t) {
		if ev, _ := l["event"].(string); strings.HasPrefix(ev, "ad.resume.") && l["claude_instance_id"] == id {
			out = append(out, l)
		}
	}
	return out
}

// rstAssertRestoredTrail checks id has exactly one ad.resume.restored with
// applied, launch_error and restore_error (nil: none).
func rstAssertRestoredTrail(t *testing.T, id string, applied bool, launchErr string, restoreErr any) {
	t.Helper()
	lines := pendTrail(t, "ad.resume.restored", id)
	if len(lines) != 1 {
		t.Fatalf("ad.resume.restored lines = %v; want exactly one", lines)
	}
	l := lines[0]
	if l["applied"] != applied || l["launch_error"] != launchErr || l["restore_error"] != restoreErr || l["source"] != "ad_resume" {
		t.Errorf("ad.resume.restored = %v; want applied %v, launch_error %s, restore_error %v, source ad_resume",
			l, applied, launchErr, restoreErr)
	}
}

// assertResumeEvents fails unless id's ad.resume.* and ad.launch.name_held
// lines written since mark are exactly want, in order.
func assertResumeEvents(t *testing.T, mark int, id string, want ...string) {
	t.Helper()
	got := []string{}
	for _, l := range readAPITrailLines(t)[mark:] {
		ev, _ := l["event"].(string)
		if l["claude_instance_id"] == id && (strings.HasPrefix(ev, "ad.resume.") || ev == "ad.launch.name_held") {
			got = append(got, ev)
		}
	}
	if !slices.Equal(got, append([]string{}, want...)) {
		t.Errorf("trail events of %s = %q; want %q", id, got, want)
	}
}

// rstOneCreate returns the one recorded create, failing unless it is on e's socket.
func rstOneCreate(t *testing.T, e *resumeEnv) tmuxfix.SocketCall {
	t.Helper()
	creates := e.rec.SocketCallsOf(tmux.CallCreate)
	if len(creates) != 1 || creates[0].Socket != e.socket {
		t.Fatalf("create calls = %+v; want exactly one on %s", creates, e.socket)
	}
	return creates[0]
}

// rplSessionNamed returns the session on socket whose stored name is name.
func rplSessionNamed(t *testing.T, rec *tmuxfix.Recorder, socket, name string) tmuxfix.SeedSession {
	t.Helper()
	for _, s := range rec.Sessions(socket) {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no session named %q on %s: %+v", name, socket, rec.Sessions(socket))
	return tmuxfix.SeedSession{}
}

// pendTrail returns the trail lines of event for instance id.
func pendTrail(t *testing.T, event, id string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range readAPITrailLines(t) {
		if l["event"] == event && l["claude_instance_id"] == id {
			out = append(out, l)
		}
	}
	return out
}

// pendMoved counts id's ad.resume.moved_to_pending lines.
func pendMoved(t *testing.T, id string) int {
	t.Helper()
	return len(pendTrail(t, "ad.resume.moved_to_pending", id))
}

// pendCalls is every tmux call rec recorded, name-based and socket-taking.
func pendCalls(rec *tmuxfix.Recorder) int { return len(rec.Calls()) + len(rec.SocketCalls()) }

// pendCallKinds is the kind of each socket-taking call rec recorded, in order.
func pendCallKinds(rec *tmuxfix.Recorder) []tmux.Call {
	var out []tmux.Call
	for _, c := range rec.SocketCalls() {
		out = append(out, c.Call)
	}
	return out
}

// pendRowNullOr returns nil for "" and s otherwise (a column's raw NULL or text).
func pendRowNullOr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// vanishedUserSocket is a socket under a missing per-user directory whose parent exists.
func vanishedUserSocket(t *testing.T) string {
	return filepath.Join(userSocketDir(t.TempDir()), "default")
}
