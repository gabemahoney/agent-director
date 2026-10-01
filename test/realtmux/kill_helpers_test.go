package realtmux_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// The shared real-tmux kill fixture (SRD SR-6.1, SR-20.4, SR-20.7): live rows
// in a temp HOME's store matching sessions made by the production create, the
// production pkg/api client through api.New with a configurable kill exit
// wait, SIGHUP stand-ins, and the observations after one kill call. Later
// verb Epics extend this fixture instead of writing their own.

// killFix is the lookup fixture plus a temp HOME holding a fresh store.
type killFix struct {
	*lookupFix
	DBPath  string
	StoreID string // this store's store_meta.store_id, the last label field
}

// killDefaultWait is the fixture's kill_exit_wait_ms: short, yet ample for a
// pane process to exit on SIGHUP under load.
const killDefaultWait = 3 * time.Second

// newKillFix makes a fresh private tmux world, isolates HOME and initialises
// the store there, reading its store id.
func newKillFix(t *testing.T) *killFix {
	t.Helper()
	f := newLookupFix(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	unsetEnv(t, "CLAUDE_CONFIG_DIR")
	db := filepath.Join(home, ".agent-director", "state.db")
	if _, err := apitest.InitStore(db); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	id, err := apitest.ReadStoreID(db)
	if err != nil {
		t.Fatalf("ReadStoreID: %v", err)
	}
	return &killFix{lookupFix: f, DBPath: db, StoreID: id}
}

// killRowSpec is a live row and its agent session on the fixture's socket;
// zero fields take defaults: state waiting and createSpec's defaults.
type killRowSpec struct {
	Name       string   // recorded name; one with $ or \ is labelled by id after the create
	Command    []string // the pane command (stubCommand exits on SIGHUP, hupSurvivor does not)
	State      string
	Token      string
	InstanceID string
	NoPane     bool   // a lost create reply: the server identity recorded, no pane
	PaneID     string // recorded in place of the reply's pane id (e.g. one the server never issued)
}

// killRow is a seeded live row: its agent (id, token, name, reply, identity),
// the world bound to its socket and the row as seeded.
type killRow struct {
	agent
	RT     *realTmux
	Before apitest.SpawnColumns
}

// liveRow creates the agent by the production create (labelled by id when the
// name needs it, as spawn does) and seeds its matching row; the pane's process
// is ended at cleanup even if it survived a pane kill.
func (f *killFix) liveRow(t *testing.T, spec killRowSpec) killRow {
	t.Helper()
	rt := f.realTmux
	a := f.agentOn(t, rt, createSpec{Name: spec.Name, Command: spec.Command, Token: spec.Token,
		InstanceID: spec.InstanceID, StoreID: f.StoreID})
	endAtCleanup(t, a.Reply.PanePID)
	if tmux.NeedsLabelByID(a.Name) {
		if err := f.Client.SetLabel(rt.Socket, a.Reply.SessionID, a.Reply.PaneID, a.Token, a.InstanceID, a.StoreID); err != nil {
			t.Fatalf("label session %s by id: %s", a.Reply.SessionID, describe(err))
		}
	}
	id := store.LaunchIdentity{Token: a.Token, Socket: a.Socket, ServerPID: a.Server.PID, ServerStart: a.Server.Start,
		ServerStarttime: a.Server.Starttime, PaneID: a.Reply.PaneID, PanePID: a.Reply.PanePID, PaneStarttime: a.PaneStart}
	if spec.PaneID != "" {
		id.PaneID = spec.PaneID
	}
	if spec.NoPane {
		id.PaneID, id.PanePID, id.PaneStarttime = "", 0, ""
	}
	return killRow{agent: a, RT: rt, Before: f.seedRow(t, a.InstanceID, a.Name, spec.State, id)}
}

// seedRow seeds a row named name in state ("" is waiting) recording id, with
// no agent process recorded, and returns it as stored.
func (f *killFix) seedRow(t testing.TB, instanceID, name, state string, id store.LaunchIdentity) apitest.SpawnColumns {
	t.Helper()
	if _, err := apitest.SeedSpawn(f.DBPath, instanceID, state, "", "", "", false,
		apitest.WithTmuxSessionName(name), apitest.WithLaunchIdentity(id)); err != nil {
		t.Fatalf("SeedSpawn %s: %v", instanceID, err)
	}
	return readRow(t, f.DBPath, instanceID)
}

// assertRowUnchanged checks every column of the row still holds its seeded
// value, naming only the columns that differ (no token is printed).
func (f *killFix) assertRowUnchanged(t testing.TB, instanceID string, before apitest.SpawnColumns) {
	t.Helper()
	after := readRow(t, f.DBPath, instanceID)
	b, a := reflect.ValueOf(before), reflect.ValueOf(after)
	var changed []string
	for i := 0; i < b.NumField(); i++ {
		if !reflect.DeepEqual(b.Field(i).Interface(), a.Field(i).Interface()) {
			changed = append(changed, b.Type().Field(i).Name)
		}
	}
	if len(changed) > 0 {
		t.Errorf("row %s changed in %v (state %v -> %v)", instanceID, changed, before.State, after.State)
	}
}

// killCall is one kill through the production client: result, error and how
// long the call took.
type killCall struct {
	Res  api.KillResult
	Err  error
	Took time.Duration
}

// open opens the production client on the fixture's store through api.New
// with a config file giving the client timeouts and kill_exit_wait_ms = wait
// (0: killDefaultWait), running tmuxCommand ("" is tmux on PATH).
func (f *killFix) open(t testing.TB, wait time.Duration, tmuxCommand string) *api.Client {
	t.Helper()
	if wait == 0 {
		wait = killDefaultWait
	}
	cfg := filepath.Join(t.TempDir(), "config.toml")
	apitest.WriteTmuxConfig(t, cfg,
		apitest.TmuxInt(config.TmuxQueryTimeoutMs, clientTimeouts.Query.Milliseconds()),
		apitest.TmuxInt(config.TmuxActionTimeoutMs, clientTimeouts.Action.Milliseconds()),
		apitest.TmuxInt(config.TmuxCreateTimeoutMs, clientTimeouts.Create.Milliseconds()),
		apitest.TmuxInt(config.TmuxKillExitWaitMs, wait.Milliseconds()))
	c, err := api.New(api.Options{StorePath: f.DBPath, ConfigPath: cfg, TmuxCommand: tmuxCommand})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	return c
}

// kill opens the production client (open) with kill_exit_wait_ms = wait
// (0: killDefaultWait) and kills instanceID.
func (f *killFix) kill(t testing.TB, instanceID string, wait time.Duration) killCall {
	t.Helper()
	c := f.open(t, wait, "")
	defer c.Close() //nolint:errcheck
	start := time.Now()
	res, err := c.Kill(api.KillParams{ClaudeInstanceID: instanceID})
	return killCall{Res: res, Err: err, Took: time.Since(start)}
}

// assertSuccess checks the kill succeeded with kill_sent equal to sent.
func (k killCall) assertSuccess(t testing.TB, sent bool) {
	t.Helper()
	if k.Err != nil {
		t.Fatalf("kill: %s; want success", describe(k.Err))
	}
	if k.Res.KillSent != sent {
		t.Errorf("kill_sent = %v, want %v", k.Res.KillSent, sent)
	}
}

// assertRefused checks the kill failed with exactly the catalogued error name
// (SR-1.5), a description matching c without any forbid value, and no kill_sent.
func (k killCall) assertRefused(t testing.TB, name string, c apitest.DescCase, forbid ...string) {
	t.Helper()
	if k.Err == nil {
		t.Fatalf("kill succeeded (kill_sent %v); want %s", k.Res.KillSent, name)
	}
	assertVerbError(t, "kill", k.Err, name, c, forbid...)
	if k.Res.KillSent {
		t.Errorf("a refused kill returned kill_sent true")
	}
}

// assertVerbError checks verb's error is exactly the catalogued error name
// (SR-1.5) with a description matching c without any forbid value.
func assertVerbError(t testing.TB, verb string, err error, name string, c apitest.DescCase, forbid ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s succeeded; want %s", verb, name)
	}
	var matched []string
	for _, e := range errnames.Catalog {
		if errors.Is(err, e.Err) {
			matched = append(matched, e.Name)
		}
	}
	if got, _ := errnames.Classify(err); len(matched) != 1 || matched[0] != name || got != name {
		t.Errorf("%s error %s matches catalogued %v, classified %s; want exactly %s", verb, describe(err), matched, got, name)
	}
	apitest.AssertDescription(t, err.Error(), c, forbid...)
}

// baseIndexOneLostReply sets base-index and pane-base-index 1 on a new
// server, then makes a row in state with a live session but no recorded pane.
func (f *killFix) baseIndexOneLostReply(t *testing.T, state string) killRow {
	t.Helper()
	f.startSession(t, "") // starts the server the options are set on
	f.must(t, "set-option", "-g", "base-index", "1")
	f.must(t, "set-option", "-gw", "pane-base-index", "1")
	r := f.liveRow(t, killRowSpec{State: state, NoPane: true})
	if w, i := f.formatInt(t, r.Reply.PaneID, "#{window_index}"), f.formatInt(t, r.Reply.PaneID, "#{pane_index}"); w != 1 || i != 1 {
		t.Fatalf("agent pane %s is window %d pane %d, want 1.1", r.Reply.PaneID, w, i)
	}
	if r.Before.PaneID != nil {
		t.Fatalf("row records pane %v, want none (a lost reply)", r.Before.PaneID)
	}
	return r
}

// assertAdopted checks the row is in state and records the agent's pane
// (id, pid, start time): the lost reply's pane was adopted (SR-3.6).
func (f *killFix) assertAdopted(t testing.TB, r killRow, state string) {
	t.Helper()
	row := readRow(t, f.DBPath, r.InstanceID)
	got := fmt.Sprint(row.State, " ", row.PaneID, " ", row.PanePID, " ", row.PaneStarttime)
	if want := fmt.Sprint(state, " ", r.Reply.PaneID, " ", r.Reply.PanePID, " ", r.PaneStart); got != want {
		t.Errorf("row state and pane = %s, want %s (pane adopted, state kept)", got, want)
	}
}

// hupSurvivor is a pane command that ignores SIGHUP and execs sleep, so the
// pane's pid is the surviving process itself.
func hupSurvivor() []string { return []string{"sh", "-c", "trap '' HUP; exec sleep 3600"} }

// endAtCleanup ends pid, if still the process running now, when the test
// ends: a SIGHUP survivor is in no pane after its pane's kill.
func endAtCleanup(t testing.TB, pid int) {
	t.Helper()
	was, ok := readStat(pid)
	if !ok {
		return
	}
	t.Cleanup(func() {
		if now, ok := readStat(pid); ok && now.start == was.start && !now.gone() {
			_ = syscallKill(pid)
		}
	})
}

// assertProcs checks each pid is gone (absent or a zombie) when gone is set,
// else still running.
func assertProcs(t testing.TB, gone bool, pids ...int) {
	t.Helper()
	for _, pid := range pids {
		if pidGone(pid) != gone {
			t.Errorf("process %s; want gone %v", procState(pid), gone)
		}
	}
}

// hasSession reports whether a session with this id runs on rt.
func (r *realTmux) hasSession(t testing.TB, sessionID string) bool {
	t.Helper()
	return r.run(t, "has-session", "-t", sessionID).Exit == 0
}

// assertSession checks the session id runs on rt (running set) or is gone.
func (r *realTmux) assertSession(t testing.TB, sessionID string, running bool) {
	t.Helper()
	if got := r.hasSession(t, sessionID); got != running {
		t.Errorf("session %s running %v, want %v", sessionID, got, running)
	}
}

// windowsOf lists a session's window ids (none when it is gone).
func (r *realTmux) windowsOf(t testing.TB, sessionID string) []string {
	t.Helper()
	res := r.run(t, "list-windows", "-t", sessionID, "-F", "#{window_id}")
	if res.Exit != 0 {
		return nil
	}
	return strings.Fields(res.Stdout)
}
