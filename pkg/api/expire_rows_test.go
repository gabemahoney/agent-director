package api_test

// expire_rows_test.go covers expire on odd rows (SR-5.5, SR-3.10, SR-2.2):
// hand-edited malformed rows judged like their well-formed twins, rows whose
// recorded name holds $ or \, and a non-ASCII name under LC_ALL=C or no
// locale. The real-tmux locale case is test/realtmux's.

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// exrAge is how long before the fixture clock's now every row here ended;
// exrWindow selects them.
const (
	exrAge    = time.Hour
	exrWindow = time.Minute
)

// exrWorld is what a finished row meets: its agent's state and, when session
// is set, a session holding its name labelled for it (its current label, or
// an old one for a row with no token).
type exrWorld struct {
	name    string
	agent   agentState
	session bool
}

// exrWorlds are the gone, process-alive and running-session worlds.
var exrWorlds = []exrWorld{{"gone", agentGone, false}, {"alive", agentAlive, false}, {"session", agentUnreadable, true}}

// want is w's expected outcome for a row ("" = deleted); a row with no
// token has no current label, so a session holding its name is a leftover.
func (w exrWorld) want(noToken bool) string {
	switch {
	case w.agent == agentAlive:
		return "process_alive"
	case !w.session:
		return ""
	case noToken:
		return "leftover_running"
	}
	return "ours"
}

// exrSeed seeds through apitest.SeedSpawn a row that ended exrAge ago with
// seedRow's launch identity (a new pane and pid) edited by ident, then opts,
// and its agent in state a. It reads nothing through GetSpawn, which refuses
// malformed labels, claude_args and extra_env.
func exrSeed(t *testing.T, e *killEnv, id string, a agentState, ident func(*store.LaunchIdentity), opts ...apitest.SpawnOption) killRow {
	t.Helper()
	pid := e.newPID()
	li := store.LaunchIdentity{Token: strings.ReplaceAll(uuid.NewString(), "-", "")[:16], Socket: apitest.TestSocket,
		ServerPID: killServerPID, ServerStart: killServerStart, ServerStarttime: apitest.LinuxProcStarttime,
		PaneID: fmt.Sprintf("%%%d", pid), PanePID: pid, PaneStarttime: apitest.LinuxProcStarttime}
	if ident != nil {
		ident(&li)
	}
	spec := e.finishedSpec(exrAge, a, append([]apitest.SpawnOption{apitest.WithLaunchIdentity(li),
		apitest.WithPID(pid), apitest.WithProcStarttime(apitest.LinuxProcStarttime)}, opts...)...)
	if _, err := apitest.SeedSpawn(e.dbPath, id, spec.State, "", "off", "", false, spec.Opts...); err != nil {
		t.Fatalf("SeedSpawn(%s): %v", id, err)
	}
	name, _ := e.columns(t, id).TmuxSessionName.(string)
	r := killRow{ID: id, Name: name, Socket: li.Socket, Token: li.Token, StoreID: e.storeID, Agent: a,
		Spawn: api.Spawn{ClaudeInstanceID: id, TmuxSessionName: name, Identity: li}}
	if r.Socket == "" {
		r.Socket = e.defaultSocket
	}
	e.setAgent(&r, pid, apitest.LinuxProcStarttime)
	return r
}

// exrHoldName seeds on r's socket a session holding r's name: r's own (its
// current label on its recorded pane) when own, else an earlier launch's.
func exrHoldName(t *testing.T, e *killEnv, r *killRow, own bool) {
	t.Helper()
	e.ensureServer(r)
	s := tmuxfix.SeedSession{Name: r.Name, Label: r.old(), Panes: []tmuxfix.SeedPane{{AdPane: tmuxfix.OtherToken}}}
	if own {
		s.Label = r.current()
		s.Panes = []tmuxfix.SeedPane{{ID: r.Spawn.Identity.PaneID, PID: r.Spawn.Identity.PanePID, AdPane: r.Token}}
	}
	r.Session = e.seedOther(t, r.Socket, s)
	e.syncServers()
}

// TestExpireMalformedRows (SR-5.5, AC-EXP-12): in every world a malformed row
// gets its well-formed twin's outcome, in one run that errs and logs nothing.
func TestExpireMalformedRows(t *testing.T) {
	kinds := []struct {
		name    string
		ident   func(*store.LaunchIdentity)
		opts    []apitest.SpawnOption
		noToken bool
	}{
		{name: "labels", opts: []apitest.SpawnOption{apitest.WithRawLabels(`{"team":`)}},
		{name: "claude_args", opts: []apitest.SpawnOption{apitest.WithRawClaudeArgs(`["--model",`)}},
		{name: "extra_env", opts: []apitest.SpawnOption{apitest.WithRawExtraEnv(`not an object`)}},
		{name: "launch start not an integer", opts: []apitest.SpawnOption{apitest.WithRawLaunchStartedAt("soon")}},
		{name: "launch token malformed", ident: func(li *store.LaunchIdentity) { li.Token = "NOT-A-HEX-TOKEN!" }, noToken: true},
		// The same columns as apitest.WithNoLaunchToken: no token, socket or identity.
		{name: "from before the release", ident: func(li *store.LaunchIdentity) { *li = store.LaunchIdentity{} }, noToken: true},
	}
	for _, k := range kinds {
		t.Run(k.name, func(t *testing.T) {
			e := newKillEnv(t)
			want, sockets := map[string]string{}, map[string]bool{}
			for _, w := range exrWorlds {
				// The malformed row sorts first: the run must go on past it.
				bad := exrSeed(t, e, "exr-a-malformed-"+w.name, w.agent, k.ident, k.opts...)
				twin := exrSeed(t, e, "exr-b-wellformed-"+w.name, w.agent, nil)
				for _, r := range []*killRow{&bad, &twin} {
					if w.session {
						exrHoldName(t, e, r, !k.noToken)
					}
					if w.agent != agentAlive {
						sockets[r.Socket] = true
					}
					want[r.ID] = w.want(k.noToken)
				}
			}
			mark := trailMark(t)
			res, lg, err := e.expire(olderThan(exrWindow))
			if err != nil {
				t.Fatalf("Expire: %v", err)
			}
			assertExpired(t, res, mark, want)
			var lookups []string
			for s := range sockets {
				lookups = append(lookups, s)
			}
			e.assertLookupsOn(t, lookups...)
			if len(lg.lines) != 0 {
				t.Errorf("log lines %q; want none", lg.lines)
			}
		})
	}
}

// exrAssertKeptOurs fails unless the run (since mark) kept r alone as ours
// after one lookup on its socket, its ad.expire.kept naming recorded byte for
// byte and no ad.provenance.disagree written.
func exrAssertKeptOurs(t *testing.T, e *killEnv, r killRow, mark int, res api.ExpireResult, err error, recorded string) {
	t.Helper()
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	assertExpired(t, res, mark, map[string]string{r.ID: "ours"})
	e.assertLookupsOn(t, r.Socket)
	for _, l := range expireKeptSince(t, mark) {
		if got, _ := l["tmux_session_name"].(string); got != recorded {
			t.Errorf("ad.expire.kept tmux_session_name = %q; want %q", got, recorded)
		}
	}
	if d := expireDisagreesSince(t, mark, r.ID); len(d) != 0 {
		t.Errorf("ad.provenance.disagree %v; want none", d)
	}
}

// TestExpireStoredNames (AC-LKP-09): a row whose recorded name holds $ or \,
// its own session running under tmux's stored form, is kept ours.
func TestExpireStoredNames(t *testing.T) {
	for _, n := range tmuxfix.StoredNames() {
		// A '.' or ':' name is one tmux rewrites: Epic 19's reason, not this.
		if !n.LabelByID || strings.ContainsAny(n.Raw, ".:") {
			continue
		}
		t.Run(n.Raw, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedFinished(t, exrAge, agentUnreadable, apitest.WithTmuxSessionName(n.Raw))
			e.seedSession(t, &r, tmuxfix.WithRowSessionName(n.Stored))
			mark := trailMark(t)
			res, _, err := e.expire(olderThan(exrWindow))
			exrAssertKeptOurs(t, e, r, mark, res, err, n.Raw)
		})
	}
}

// TestExpireLocaleName (SR-2.2, AC-LKP-08's unit half): a row whose name and
// id hold ü, its session running, is kept ours under LC_ALL=C and no locale.
func TestExpireLocaleName(t *testing.T) {
	forms := tmuxfix.LocaleForms()
	name, id := forms[0].Exact, forms[1].Exact
	for _, loc := range []struct{ name, lcAll string }{{"LC_ALL=C", "C"}, {"no locale variables", ""}} {
		t.Run(loc.name, func(t *testing.T) {
			for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
				t.Setenv(k, "")
				os.Unsetenv(k) //nolint:errcheck
			}
			if loc.lcAll != "" {
				t.Setenv("LC_ALL", loc.lcAll)
			}
			e := newKillEnv(t)
			spec := e.finishedSpec(exrAge, agentUnreadable, apitest.WithTmuxSessionName(name))
			spec.ID = id
			r := e.seedRow(t, spec)
			e.seedSession(t, &r, tmuxfix.WithRowSessionName(name))
			mark := trailMark(t)
			res, _, err := e.expireClient(t, olderThan(exrWindow))
			exrAssertKeptOurs(t, e, r, mark, res, err, name)
		})
	}
}
