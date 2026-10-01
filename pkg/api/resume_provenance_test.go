package api_test

// resume_provenance_test.go covers the ad.provenance.disagree records of
// resume's pre-launch lookup (SR-14, SR-3.3, SR-3.4, SR-3.16; AC-LKP-18,
// AC-LKP-19): one per reason per call, verb resume and source ad_resume,
// written before any ad.resume.* line and before the move; none in the
// normal case; never adopted; fail-open. It also covers the re-lookup's
// after "duplicate session" (SR-8.5): after the create, action the restore's
// row result, and never a reason the pre-launch lookup already wrote.

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/trail"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rpvSetup arranges the pre-launch lookup's answer for a resumable row.
type rpvSetup func(*testing.T, *killEnv, *resumeRow)

// rpvOwn seeds r's own session, created past the starting-session bound,
// under name ("" keeps the recorded name).
func rpvOwn(name string) rpvSetup {
	return func(t *testing.T, e *killEnv, r *resumeRow) {
		opts := []tmuxfix.RowSessionOption{e.createdBefore(rceSettled(e))}
		if name != "" {
			opts = append(opts, tmuxfix.WithRowSessionName(name))
		}
		e.seedSession(t, &r.killRow, opts...)
	}
}

// rpvRestart restarts r's server; the new one holds a bystander, so its
// listing carries the new server's identity.
func rpvRestart(t *testing.T, e *killEnv, r *resumeRow) {
	e.rec.RestartServer(r.Socket, tmuxfix.Server{})
	e.syncServers()
	e.seedBystander(t, r.Socket)
}

// rpvServer replaces r's server by how (an empty new one, or none) and then
// puts the recorded server process in the fake as p (nil: as the Recorder left it).
func rpvServer(how string, p *procfix.Process) rpvSetup {
	return func(_ *testing.T, e *killEnv, r *resumeRow) {
		switch how {
		case "restart":
			e.rec.RestartServer(r.Socket, tmuxfix.Server{})
		case "rebind":
			e.rec.RebindServer(r.Socket, tmuxfix.Server{})
		case "stop":
			e.rec.StopServer(r.Socket)
		}
		e.syncServers()
		if p != nil {
			e.pc.Set(r.Spawn.Identity.ServerPID, *p)
		}
	}
}

// rpvScope sets r's current label as the scope value at level, embedding r's session id.
func rpvScope(level tmuxfix.ScopeLevel) rpvSetup {
	return func(_ *testing.T, e *killEnv, r *resumeRow) {
		e.rec.SetScope(r.Socket, level, tmuxfix.ScopeValue{SessionID: r.Session.ID, Label: r.current()})
	}
}

// rpvCase is one pre-launch arrangement and the records one resume writes;
// moved says whether the call reaches the move ("" leaves it unchecked).
type rpvCase struct {
	name             string
	noServerIdentity bool
	opts             []apitest.SpawnOption
	setup            []rpvSetup
	want             []disagreeWant
	moved            string
}

// rpvCases is the reason table: every reason the pre-launch lookup meets, and
// the arrangements that write none.
func rpvCases() []rpvCase {
	alive := procfix.Alive(apitest.LinuxProcStarttime)
	unreadable := procfix.Unreadable()
	mismatch := []disagreeWant{{reason: "server_mismatch", server: "differs", verdict: "different_server", action: "refused"}}
	conflict := func(reason string) []disagreeWant {
		return []disagreeWant{{reason: reason, server: "match", verdict: "provenance_conflict", action: "refused"}}
	}
	renamed := func(server string) []disagreeWant {
		return []disagreeWant{{reason: "name_changed", server: server, verdict: "ours", action: "refused",
			current: "renamed-resume", ours: true}}
	}
	dup := func(_ *testing.T, e *killEnv, r *resumeRow) {
		e.rec.SeedSessions(r.Socket, tmuxfix.SeedSession{Name: "dup", Label: r.current()})
	}
	return []rpvCase{
		{name: "normal gone, name free, writes none", moved: "yes"},
		{name: "normal ours at the recorded name writes none", setup: []rpvSetup{rpvOwn("")}, moved: "no"},
		{name: "SessionStart pid differs from the pane pid writes none",
			opts: []apitest.SpawnOption{apitest.WithPID(apitest.TestPanePID + 5)}, moved: "yes"},
		{name: "ours with no server identity is not adopted", noServerIdentity: true,
			setup: []rpvSetup{rpvOwn("")}, moved: "no"},
		{name: "server_restarted, gone, launch proceeds", setup: []rpvSetup{rpvRestart}, moved: "yes",
			want: []disagreeWant{{reason: "server_restarted", server: "restarted", verdict: "gone", action: "proceeded"}}},
		{name: "server_restarted, ours on the new server", setup: []rpvSetup{rpvRestart, rpvOwn("")}, moved: "no",
			want: []disagreeWant{{reason: "server_restarted", server: "restarted", verdict: "ours", action: "refused", ours: true}}},
		{name: "server_mismatch, listing, recorded server runs",
			setup: []rpvSetup{rpvServer("rebind", nil), rpvBystander}, want: mismatch, moved: "no"},
		{name: "server_mismatch, listing, recorded server uncheckable",
			setup: []rpvSetup{rpvServer("rebind", &unreadable), rpvBystander}, want: mismatch, moved: "no"},
		{name: "no server reply, recorded server gone, writes none", setup: []rpvSetup{rpvServer("stop", nil)}},
		{name: "no server reply, recorded server runs", setup: []rpvSetup{rpvServer("stop", &alive)}, want: mismatch},
		{name: "no server reply, recorded server uncheckable", setup: []rpvSetup{rpvServer("stop", &unreadable)}, want: mismatch},
		{name: "empty listing, recorded server gone, writes none", setup: []rpvSetup{rpvServer("restart", nil)}},
		{name: "empty listing, recorded server runs", setup: []rpvSetup{rpvServer("rebind", nil)}, want: mismatch},
		{name: "empty listing, recorded server uncheckable", setup: []rpvSetup{rpvServer("rebind", &unreadable)}, want: mismatch},
		{name: "duplicate_label", setup: []rpvSetup{rpvOwn(""), dup}, want: conflict("duplicate_label"), moved: "no"},
		{name: "scope_value global", setup: []rpvSetup{rpvOwn(""), rpvScope(tmuxfix.ScopeGlobal)},
			want: conflict("scope_value"), moved: "no"},
		{name: "scope_value server", setup: []rpvSetup{rpvOwn(""), rpvScope(tmuxfix.ScopeServer)},
			want: conflict("scope_value"), moved: "no"},
		{name: "scope_value global-window", setup: []rpvSetup{rpvOwn(""), rpvScope(tmuxfix.ScopeGlobalWindow)},
			want: conflict("scope_value"), moved: "no"},
		{name: "name_changed", setup: []rpvSetup{rpvOwn("renamed-resume")}, want: renamed("match"), moved: "no"},
		{name: "name_changed with no server identity, not adopted", noServerIdentity: true,
			setup: []rpvSetup{rpvOwn("renamed-resume")}, want: renamed("unknown"), moved: "no"},
	}
}

// rpvBystander seeds a bystander on r's socket, so its server answers a listing.
func rpvBystander(t *testing.T, e *killEnv, r *resumeRow) { e.seedBystander(t, r.Socket) }

// seedRPVCase seeds tc's resumable row (id when given) with another row's
// label on its server, then runs tc's setups; it returns the row and that
// other row's id, which no record may hold.
func (e *killEnv) seedRPVCase(t *testing.T, tc rpvCase, id string) (resumeRow, string) {
	t.Helper()
	spec := e.resumableSpec(rceSettled(e), agentGone, tc.opts...)
	spec.ID, spec.NoServerIdentity = id, tc.noServerIdentity
	r := e.seedResumableRow(t, spec)
	other := "other-" + uuid.NewString()[:8]
	e.rec.SeedSessions(r.Socket, tmuxfix.SeedSession{Name: "foreign-" + uuid.NewString()[:8], Label: r.foreign(other)})
	for _, s := range tc.setup {
		s(t, e, &r)
	}
	return r, other
}

// rpvStore counts id's resume disagree records when the move to pending starts.
type rpvStore struct {
	*hookedResumeStore
	t      *testing.T
	atMove int // -1 until MoveToPending is called
}

// MoveToPending counts the records written so far, then delegates.
func (w *rpvStore) MoveToPending(id string, examined api.RowSnapshot, startedAt int64, token, socket, parent string) (api.CondResult, int64, error) {
	w.atMove = len(resumeDisagrees(w.t, id))
	return w.hookedResumeStore.MoveToPending(id, examined, startedAt, token, socket, parent)
}

// TestResumeProvenanceDisagree: each reason the pre-launch lookup meets is
// written once per call with every SR-14 field and no label content, before
// the move and any ad.resume.* line; the normal cases and adoption write none.
func TestResumeProvenanceDisagree(t *testing.T) {
	for _, tc := range rpvCases() {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r, other := e.seedRPVCase(t, tc, "")
			s := &rpvStore{hookedResumeStore: &hookedResumeStore{st: e.st}, t: t, atMove: -1}
			mark := trailMark(t)

			_, _ = e.resumeWith(s, r.ID)

			recs := resumeDisagrees(t, r.ID)
			if len(recs) != len(tc.want) {
				t.Fatalf("ad.provenance.disagree records = %d; want %d: %v", len(recs), len(tc.want), recs)
			}
			after, err := e.st.GetSpawn(r.ID)
			if err != nil {
				t.Fatalf("GetSpawn: %v", err)
			}
			for i, w := range tc.want {
				assertDisagreeRecord(t, recs[i], r.killRow, "resume", "ad_resume", w)
				ktrAssertNoForeignContent(t, recs[i], r.Token, after.Identity.Token, e.storeID, other)
			}
			if n := adoptedRecords(t, "resume", r.ID); n != 0 {
				t.Errorf("adopted records = %d; want none", n)
			}
			switch {
			case tc.moved == "yes" && s.atMove != len(tc.want):
				t.Errorf("records written before the move = %d; want all %d", s.atMove, len(tc.want))
			case tc.moved == "no" && s.atMove >= 0:
				t.Errorf("the refused resume reached the move")
			}
			rpvAssertDisagreeFirst(t, mark, r.ID)
		})
	}
}

// rpvAssertDisagreeFirst fails when an ad.resume.* line for id written since
// mark comes before one of its ad.provenance.disagree lines.
func rpvAssertDisagreeFirst(t *testing.T, mark int, id string) {
	t.Helper()
	resumeSeen := ""
	for _, l := range readAPITrailLines(t)[mark:] {
		if l["claude_instance_id"] != id {
			continue
		}
		ev, _ := l["event"].(string)
		switch {
		case strings.HasPrefix(ev, "ad.resume."):
			resumeSeen = cmp.Or(resumeSeen, ev)
		case ev == "ad.provenance.disagree" && resumeSeen != "":
			t.Errorf("ad.provenance.disagree written after %s", resumeSeen)
		}
	}
}

// rpvChildEnv gates TestResumeProvenanceFailOpenChild and carries the id prefix.
const rpvChildEnv = "AD_RESUME_PROVENANCE_FAIL_CHILD"

// rpvLinePrefix marks the child's result lines in its output.
const rpvLinePrefix = "RPV|"

// rpvFailOpenCases are the cases the fail-open run repeats: a proceeding and
// a refused call that each write a record, and one of each that write none.
var rpvFailOpenCases = []string{
	"normal gone, name free, writes none",
	"normal ours at the recorded name writes none",
	"server_restarted, gone, launch proceeds",
	"server_mismatch, listing, recorded server runs",
	"name_changed",
}

// rpvFailOpenRuns resumes one row per rpvFailOpenCases entry, id prefix-<i>,
// and returns one line per call: its result, error and the row's columns,
// with the per-test socket and cwd paths replaced.
func rpvFailOpenRuns(t *testing.T, prefix string) []string {
	t.Helper()
	var lines []string
	for _, tc := range rpvCases() {
		i := slices.Index(rpvFailOpenCases, tc.name)
		if i < 0 {
			continue
		}
		e := newKillEnv(t)
		r, _ := e.seedRPVCase(t, tc, prefix+"-"+strconv.Itoa(i))
		res, err := e.resume(r.ID)
		c := e.columns(t, r.ID)
		l := fmt.Sprintf("%d id=%s pre_trust=%s err=%v state=%v ended_at=%v row_version=%v launch_started_at=%v parent=%v socket=%v",
			i, res.ClaudeInstanceID, res.PreTrust, err, c.State, c.EndedAt, c.RowVersion, c.LaunchStartedAt, c.ParentID, c.TmuxSocket)
		lines = append(lines, strings.NewReplacer(r.Socket, "<socket>", r.CWD, "<cwd>").Replace(l))
	}
	return lines
}

// TestResumeProvenanceFailOpen: with the trail unwritable, resume's results,
// errors and rows equal those of a run with a working trail.
func TestResumeProvenanceFailOpen(t *testing.T) {
	prefix := "resume-failopen-" + uuid.NewString()[:8]
	want := rpvFailOpenRuns(t, prefix)
	if n := len(resumeDisagrees(t, prefix+"-2")); n != 1 {
		t.Fatalf("working trail: server_restarted records = %d; want 1", n)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestResumeProvenanceFailOpenChild$", "-test.count=1", "-test.v") //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), rpvChildEnv+"="+prefix)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: TestResumeProvenanceFailOpenChild") {
		t.Fatalf("child: %v\n%s", err, out)
	}
	var got []string
	for _, l := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(l, rpvLinePrefix); ok {
			got = append(got, rest)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("unwritable trail gave\n%s\nwant (working trail)\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestResumeProvenanceFailOpenChild is TestResumeProvenanceFailOpen's child:
// it runs the resumes with an unwritable trail and prints their lines.
func TestResumeProvenanceFailOpenChild(t *testing.T) {
	prefix := os.Getenv(rpvChildEnv)
	if prefix == "" {
		t.Skip("run only as TestResumeProvenanceFailOpen's child")
	}
	adDir := filepath.Join(apiTrailDir, ".agent-director")
	if err := os.MkdirAll(adDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(adDir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(adDir, 0o700) })
	if err := trail.Emit(context.Background(), "ad.test.resume_probe", map[string]any{}); err == nil {
		t.Fatal("trail write succeeded; want it to fail")
	}

	for _, l := range rpvFailOpenRuns(t, prefix) {
		fmt.Println(rpvLinePrefix + l)
	}

	if _, err := os.Stat(apiTrailFilePath()); !os.IsNotExist(err) {
		t.Errorf("trail file stat err = %v; want it never created", err)
	}
}

// rpvHeldCase is one "duplicate session" arrangement and its disagree
// records: pre from the pre-launch lookup, then post from the re-lookup,
// whose tmux_session_id is session's (nil: null).
type rpvHeldCase struct {
	name      string
	spec      heldSpec
	restore   func(t *testing.T, e *killEnv, r resumeRow, w *hookedResumeStore) // the restore's result; nil: applied
	pre, post []disagreeWant
	session   func(sc *heldScene) string
}

// rpvHeldCases is the re-lookup's reason table: each reason it can meet, a
// reason both lookups meet, the restore results as action, and none.
func rpvHeldCases() []rpvHeldCase {
	holder := func(sc *heldScene) string { return sc.Holder().ID }
	conflict := func(reason, server string) []disagreeWant {
		return []disagreeWant{{reason: reason, server: server, verdict: "provenance_conflict", action: "restored"}}
	}
	mismatch := func(action string) []disagreeWant {
		return []disagreeWant{{reason: "server_mismatch", server: "differs", verdict: "different_server", action: action}}
	}
	restarted := []disagreeWant{{reason: "server_restarted", server: "restarted", verdict: "gone", action: "proceeded"}}
	rebound := heldSpec{Holder: holderNone, Server: heldServerRebound}
	scope := func(level tmuxfix.ScopeLevel) heldSpec { return heldSpec{Holder: holderCurrent, Scope: level} }
	return []rpvHeldCase{
		{name: "no reason at the re-lookup writes none", spec: heldSpec{Holder: holderOld}},
		{name: "server_restarted at both lookups is written once", spec: heldSpec{Holder: holderOld, Server: heldServerRestarted},
			pre: restarted},
		{name: "server_mismatch new at the re-lookup", spec: rebound, post: mismatch("restored"), session: holder},
		{name: "server_mismatch with no holder", spec: heldSpec{Holder: holderVanished, Server: heldServerRebound},
			post: mismatch("restored")},
		{name: "server_mismatch, row changed before the restore", spec: rebound, post: mismatch("left_changed"), session: holder,
			restore: func(t *testing.T, e *killEnv, r resumeRow, w *hookedResumeStore) {
				parent := e.seedRow(t, killRowSpec{State: store.StateEnded, Agent: agentGone, NoSession: true}).ID
				w.afterMove(func() {
					if err := e.st.SetParentID(r.ID, parent); err != nil {
						t.Errorf("SetParentID: %v", err)
					}
				})
			}},
		{name: "server_mismatch, restore store error", spec: rebound, post: mismatch("still_pending"), session: holder,
			restore: func(_ *testing.T, _ *killEnv, _ resumeRow, w *hookedResumeStore) { w.failRestore(nil) }},
		{name: "duplicate_label", spec: heldSpec{Holder: holderCurrent, OursRenamed: "dup-resume"},
			post: conflict("duplicate_label", "match"), session: holder},
		{name: "scope_value global", spec: scope(tmuxfix.ScopeGlobal), post: conflict("scope_value", "match"), session: holder},
		{name: "scope_value server", spec: scope(tmuxfix.ScopeServer), post: conflict("scope_value", "match"), session: holder},
		{name: "scope_value global-window", spec: scope(tmuxfix.ScopeGlobalWindow), post: conflict("scope_value", "match"),
			session: holder},
		{name: "name_changed", spec: heldSpec{Holder: holderForeign, OursRenamed: "renamed-resume"},
			post: []disagreeWant{{reason: "name_changed", server: "match", verdict: "ours", action: "restored",
				current: "renamed-resume"}},
			session: func(sc *heldScene) string { return sc.Ours.ID }},
		{name: "server_restarted at both lookups, scope_value new at the re-lookup",
			spec: heldSpec{Holder: holderCurrent, Scope: tmuxfix.ScopeGlobal, Server: heldServerRestarted},
			pre:  restarted, post: conflict("scope_value", "restarted"), session: holder},
	}
}

// TestResumeProvenanceAfterDuplicateSession: the re-lookup after "duplicate
// session" writes each reason the pre-launch lookup did not, once, after the
// create, with every SR-14 field and no label content; never adopted.
func TestResumeProvenanceAfterDuplicateSession(t *testing.T) {
	for _, tc := range rpvHeldCases() {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedHeldResumable(t, rceSettled(e), agentGone)
			s := &hookedResumeStore{st: e.st}
			if tc.restore != nil {
				tc.restore(t, e, r, s)
			}
			atCreate := -1

			run := e.rhtResume(t, r, tc.spec, s, func() { atCreate = len(resumeDisagrees(t, r.ID)) })

			if !run.sc.Placed {
				t.Fatalf("the create never answered %q", "duplicate session")
			}
			if atCreate != len(tc.pre) {
				t.Errorf("records written by the create = %d; want the pre-launch lookup's %d", atCreate, len(tc.pre))
			}
			recs := resumeDisagrees(t, r.ID)
			if len(recs) != len(tc.pre)+len(tc.post) {
				t.Fatalf("ad.provenance.disagree records = %d; want %d: %v", len(recs), len(tc.pre)+len(tc.post), recs)
			}
			post := r.killRow
			post.Session = tmuxfix.SeedSession{}
			if tc.session != nil {
				post.Session.ID = tc.session(run.sc)
			}
			for i, want := range append(slices.Clone(tc.pre), tc.post...) {
				row := r.killRow
				if i >= len(tc.pre) {
					row, want.ours = post, post.Session.ID != ""
				}
				assertDisagreeRecord(t, recs[i], row, "resume", "ad_resume", want)
				ktrAssertNoForeignContent(t, recs[i], rhtForbid(e, run.sc)...)
			}
			if n := adoptedRecords(t, "resume", r.ID); n != 0 {
				t.Errorf("adopted records = %d; want none", n)
			}
		})
	}
}
