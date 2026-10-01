package api_test

// resume_fixture_test.go is the shared resume test fixture (SR-20.3, SR-20.6):
// resumeEnv (a real store, a tmuxfix.Recorder on the test clock, a per-test
// TMUX_TMPDIR, a captured logger and a Client on the same store file),
// hookedResumeStore (api.ResumeStore over the real store with injected store
// errors and interleaving hooks) and the resumable-row factory. It holds no
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

// errInjectedStore is the store error hookedResumeStore returns for an
// injected move or restore failure unless the test supplies its own.
var errInjectedStore = errors.New("injected store failure: database is locked")

// hookedResumeStore implements api.ResumeStore (and forwards
// RecordLaunchIdentity) over a real *store.Store, delegating every call. A
// test can fail the move or the restore with a store error (failMove,
// failRestore; nothing is written), and run a function once after GetSpawn
// returns (afterGet) or after the move returns (afterMove). Each hook is
// cleared before it runs, so a nested resume through the same wrapper does not
// run it again. Safe for concurrent use.
type hookedResumeStore struct {
	st *store.Store

	mu         sync.Mutex
	moveErr    error
	restoreErr error
	afterGetFn func()
	afterMvFn  func()
}

// failMove makes every later MoveToPending return err (errInjectedStore when
// nil) without calling the store.
func (w *hookedResumeStore) failMove(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.moveErr = orInjected(err)
}

// failRestore makes every later RestoreAfterFailedResume return err
// (errInjectedStore when nil) without calling the store.
func (w *hookedResumeStore) failRestore(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.restoreErr = orInjected(err)
}

// afterGet runs fn once, when the next GetSpawn returns (after resume's
// examination, before its move): e.g. a second resume or a versioned write.
func (w *hookedResumeStore) afterGet(fn func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.afterGetFn = fn
}

// afterMove runs fn once, when the next MoveToPending returns (before the
// create): e.g. a second resume or a write that lands before the restore.
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

// take returns *fn and clears it, under the lock.
func (w *hookedResumeStore) take(fn *func()) func() {
	w.mu.Lock()
	defer w.mu.Unlock()
	f := *fn
	*fn = nil
	return f
}

// injected returns *err under the lock.
func (w *hookedResumeStore) injected(err *error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
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
func (w *hookedResumeStore) MoveToPending(instanceID string, examined api.RowSnapshot, launchStartedAtMillis int64, token, socket, parentID string) (api.CondResult, int64, error) {
	var (
		res     api.CondResult
		version int64
		err     = w.injected(&w.moveErr)
	)
	if err == nil {
		res, version, err = w.st.MoveToPending(instanceID, examined, launchStartedAtMillis, token, socket, parentID)
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

// resumeEnv is what a resume test drives: store is the wrapper resume gets
// (over st, a real store on dbPath), rec the Recorder on clock's virtual
// time, pc the start-time reader, logs the captured log, c a Client on the
// same store file and clock, storeID this store's id and socket the default
// socket under the per-test TMUX_TMPDIR, whose per-user directory exists.
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
	tmpdir  string
	socket  string
}

// resumeLookupQ is the virtual time the Recorder charges resume's one
// pre-launch lookup: Q, the default query timeout.
var resumeLookupQ = config.Tmux{}.EffectiveQueryTimeout()

// newResumeEnv builds a resumeEnv over a fresh store, with TMUX unset, no
// caller instance id and config.Default() as resume's configuration.
func newResumeEnv(t *testing.T) *resumeEnv {
	t.Helper()
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "")
	tmpdir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	t.Setenv("TMUX_TMPDIR", tmpdir)
	t.Setenv("TMUX", "")
	os.Unsetenv("TMUX") //nolint:errcheck
	if err := os.MkdirAll(userSocketDir(tmpdir), 0o700); err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}

	dir := t.TempDir()
	e := &resumeEnv{dbPath: filepath.Join(dir, "state.db"), logs: &bytes.Buffer{}, pc: procfix.New(),
		clock: tmuxfix.NewClock(time.Date(2026, 9, 30, 12, 0, 0, 456_000_000, time.UTC)),
		cfg:   config.Default(), tmpdir: tmpdir, socket: filepath.Join(userSocketDir(tmpdir), "default")}
	e.lg = log.New(e.logs, "", 0)
	e.rec = tmuxfix.NewRecorder().WithVirtualTime(e.clock, tmux.Timeouts{})

	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, nil, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
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

// resume runs resume on id through the export_test seam with e.store, e.rec,
// e.pc, e.cfg, e.storeID, e.clock and e.lg.
func (e *resumeEnv) resume(id string) (api.ResumeResult, error) {
	return api.Resume(e.store, e.rec, e.pc, e.cfg, e.storeID, e.clock.Now, e.lg,
		api.ResumeParams{ClaudeInstanceID: id})
}

// moveStart is the launch start a resume called now records: e.clock's now
// plus resumeLookupQ, the one lookup before its move.
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

// resumableRow is what seedRow seeded: the row's id, state, session id, cwd,
// transcript path, tmux name, launch identity and life, the earlier session of
// its history (whose transcript also exists) and its raw columns after seeding.
// Fields other than Before hold the defaults; Before also reflects spec.Opts.
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

// seedRow seeds, through apitest.SeedSpawn, a finished row with a value in
// every column the move clears or keeps (pid, proc start time, raw ended_at,
// both liveness columns, a full launch identity on e.socket, session id,
// transcript path, life 3, labels, args, env and one history entry of that
// life), and writes both transcripts with apitest.SeedJsonl.
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
