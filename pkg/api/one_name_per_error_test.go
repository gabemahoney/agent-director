package api_test

// one_name_per_error_test.go holds the SR-1.5 one-name-per-error check: the
// shared assertOneName helper, the catalogue-wide case, and one row per
// tmux-caused error (and reachable ErrInternal case) the verbs return today,
// driven through api.Client (or the exported api.Kill) with tmuxfix.Recorder
// failure kinds. The spawnEnv fixture is in spawn_test.go, resumeEnv in
// resume_fixture_test.go, killEnv in kill_fixture_test.go.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// assertOneName checks SR-1.5 on an error a verb returned: exactly one
// errnames.Catalog entry matches err under errors.Is, it is want, and
// errnames.Classify names it too. A want of "" or "ErrInternal" asserts that
// no entry matches and Classify gives ErrInternal. Failures list every
// matching entry. Task 2 of Epic 10 (the kill call-table and rewritten kill
// tests) and Epics 11, 13 and 16/17 must call it on every tmux-caused error
// they return.
func assertOneName(t testing.TB, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil; want %s", orInternal(want))
	}
	var matched []string
	for _, e := range errnames.Catalog {
		if errors.Is(err, e.Err) {
			matched = append(matched, e.Name)
		}
	}
	internal := want == "" || want == "ErrInternal"
	switch {
	case internal && len(matched) != 0:
		t.Errorf("err %q matches catalogued %q; want none (ErrInternal)", err, matched)
	case !internal && (len(matched) != 1 || matched[0] != want):
		t.Errorf("err %q matches catalogued %q; want exactly [%s]", err, matched, want)
	}
	if name, _ := errnames.Classify(err); name != orInternal(want) {
		t.Errorf("Classify(%q) = %s; want %s", err, name, orInternal(want))
	}
}

// orInternal is want, or ErrInternal when want is empty.
func orInternal(want string) string {
	if want == "" {
		return "ErrInternal"
	}
	return want
}

// TestOneNameCatalogue: each catalogued sentinel, wrapped, and each typed
// ErrTmuxNotAvailable carrier matches its own entry and no other.
func TestOneNameCatalogue(t *testing.T) {
	for _, e := range errnames.Catalog {
		t.Run(e.Name, func(t *testing.T) {
			assertOneName(t, fmt.Errorf("verb: %w", e.Err), e.Name)
		})
	}
	t.Run("SocketDeniedError", func(t *testing.T) {
		assertOneName(t, &spawn.SocketDeniedError{Socket: "/tmp/s", Consequence: "nothing was written"}, "ErrTmuxNotAvailable")
	})
	t.Run("SocketDirError", func(t *testing.T) {
		assertOneName(t, fmt.Errorf("verb: %w", &tmux.SocketDirError{Socket: "/tmp/d/s", Dir: "/tmp/d",
			Reason: tmux.SocketDirNotCreatable}), "ErrTmuxNotAvailable")
	})
}

// TestOneNameTmuxClasses: an ErrTmuxUnresponsive, ErrTmuxSessionConflict or
// ErrTmuxKillFailed error wraps none of the send, capture, create or unavailable sentinels.
func TestOneNameTmuxClasses(t *testing.T) {
	classes := map[string]error{"ErrTmuxUnresponsive": api.ErrTmuxUnresponsive,
		"ErrTmuxSessionConflict": api.ErrTmuxSessionConflict, "ErrTmuxKillFailed": api.ErrTmuxKillFailed}
	others := map[string]error{"ErrTmuxSendKeys": api.ErrTmuxSendKeys, "ErrTmuxCaptureFailed": api.ErrTmuxCaptureFailed,
		"ErrTmuxSessionCreate": api.ErrTmuxSessionCreate, "ErrTmuxNotAvailable": api.ErrTmuxNotAvailable}
	for name, class := range classes {
		for other, sentinel := range others {
			t.Run(name+"/"+other, func(t *testing.T) {
				if err := fmt.Errorf("verb: %w", class); errors.Is(err, sentinel) {
					t.Errorf("%s matches %s under errors.Is", name, other)
				}
			})
		}
	}
}

// oneNameRow is one returned error: run drives a verb through api.Client (or
// its export_test seam for store failures) and returns its error; want is the
// catalogued name, or "" for ErrInternal.
type oneNameRow struct {
	name string
	want string
	run  func(t *testing.T) error
}

// oneNameRows is every returned-error row.
func oneNameRows() []oneNameRow {
	return slices.Concat(oneNameSpawnRows(), oneNameResumeRows(), oneNameInternalRows(), oneNameKillRows())
}

// TestOneNameReturnedErrors: every tmux-caused error the verbs return matches
// exactly one catalogued sentinel, and every ErrInternal case none.
func TestOneNameReturnedErrors(t *testing.T) {
	for _, row := range oneNameRows() {
		t.Run(row.name, func(t *testing.T) { assertOneName(t, row.run(t), row.want) })
	}
}

// oneNameSpawn is a row that runs Client.Spawn with a caller-supplied id
// (so the label scan runs) after setup prepares e and p.
func oneNameSpawn(name, want string, setup func(t *testing.T, e spawnEnv, p *api.SpawnParams)) oneNameRow {
	return oneNameRow{name: "spawn/" + name, want: want, run: func(t *testing.T) error {
		e := newSpawnEnv(t)
		if err := os.WriteFile(filepath.Join(e.home, ".claude.json"), []byte("{}"), 0o600); err != nil {
			t.Fatalf("write .claude.json: %v", err)
		}
		p := api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: "one-" + uuid.NewString()[:8]}
		setup(t, e, &p)
		_, err := e.c.Spawn(p)
		return err
	}}
}

// scriptSpawn scripts s on every call of kind call on e's socket.
func scriptSpawn(call tmux.Call, s tmuxfix.Script) func(*testing.T, spawnEnv, *api.SpawnParams) {
	return func(_ *testing.T, e spawnEnv, _ *api.SpawnParams) { e.rec.Script(e.socket, s, call) }
}

// oneNameSpawnRows are plain spawn's tmux-caused errors: socket resolution,
// the label scan (SR-9.3) and the create-and-label (SR-9.4).
func oneNameSpawnRows() []oneNameRow {
	lookup, create := tmux.CallLookup, tmux.CallCreate
	return []oneNameRow{
		oneNameSpawn("TMUX_TMPDIR is a regular file", "ErrTmuxNotAvailable", func(t *testing.T, _ spawnEnv, _ *api.SpawnParams) {
			f := filepath.Join(t.TempDir(), "not-a-dir")
			if err := os.WriteFile(f, nil, 0o600); err != nil {
				t.Fatalf("write %s: %v", f, err)
			}
			t.Setenv("TMUX_TMPDIR", f)
		}),
		oneNameSpawn("scan: binary unavailable", "ErrTmuxNotAvailable", scriptSpawn(lookup, tmuxfix.Script{Failure: tmux.FailUnavailable})),
		oneNameSpawn("scan: socket permission", "ErrTmuxNotAvailable", scriptSpawn(lookup, tmuxfix.Script{Failure: tmux.FailSocketDenied})),
		oneNameSpawn("scan: timeout", "ErrTmuxUnresponsive", scriptSpawn(lookup, tmuxfix.Script{Failure: tmux.FailTimeout})),
		oneNameSpawn("scan: unrecognised reply", "ErrTmuxUnresponsive", scriptSpawn(lookup,
			tmuxfix.Script{Failure: tmux.FailUnrecognized, FirstLine: "scan: unexpected reply", ExitStatus: 1, HadStdout: true})),
		oneNameSpawn("scan: leftover", "ErrTmuxSessionConflict", func(t *testing.T, e spawnEnv, p *api.SpawnParams) {
			storeID, err := apitest.ReadStoreID(e.dbPath)
			if err != nil {
				t.Fatalf("ReadStoreID: %v", err)
			}
			e.rec.SeedSessions(e.socket, tmuxfix.SeedSession{Name: "old-life",
				Label: tmuxfix.Valid(tmuxfix.OtherToken, p.ClaudeInstanceID, storeID)})
		}),
		oneNameSpawn("scan: conflicting labels", "ErrTmuxSessionConflict", func(_ *testing.T, e spawnEnv, _ *api.SpawnParams) {
			e.rec.SeedSessions(e.socket, tmuxfix.SeedSession{Name: "bystander"})
			e.rec.SetScope(e.socket, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{})
		}),
		oneNameSpawn("create: binary unavailable", "ErrTmuxNotAvailable", scriptSpawn(create, tmuxfix.Script{Failure: tmux.FailUnavailable})),
		oneNameSpawn("create: socket permission", "ErrTmuxNotAvailable", scriptSpawn(create, tmuxfix.Script{Failure: tmux.FailSocketDenied})),
		oneNameSpawn("create: timeout", "ErrTmuxUnresponsive", scriptSpawn(create, tmuxfix.Script{Failure: tmux.FailTimeout})),
		oneNameSpawn("create: non-zero exit, unparseable reply", "ErrTmuxUnresponsive", scriptSpawn(create,
			tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1, HadStdout: true})),
		oneNameSpawn("create: no server", "ErrTmuxSessionCreate", scriptSpawn(create, tmuxfix.Script{Failure: tmux.FailNoServer})),
		oneNameSpawn("create: duplicate session", "ErrTmuxSessionCreate", func(_ *testing.T, e spawnEnv, p *api.SpawnParams) {
			e.rec.SeedSessions(e.socket, tmuxfix.SeedSession{Name: "one-held"})
			p.TmuxSessionName, p.TmuxSessionNameSupplied = "one-held", true
		}),
		oneNameSpawn("create: session not labelled", "ErrTmuxSessionCreate", func(_ *testing.T, e spawnEnv, _ *api.SpawnParams) {
			e.rec.Script(e.socket, tmuxfix.Script{Failure: tmux.FailLabel, Times: 1}, create).
				Script(e.socket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1}, tmux.CallSetLabel)
		}),
	}
}

// oneNameResume is a row that runs Client.Resume on an ended row seeded with
// opts, after setup prepares e.
func oneNameResume(name, want string, setup func(t *testing.T, e *resumeEnv) []apitest.SpawnOption) oneNameRow {
	return oneNameRow{name: "resume/" + name, want: want, run: func(t *testing.T) error {
		e := newResumeEnv(t)
		r := e.seedResumable(t, store.StateEnded, setup(t, e)...)
		_, err := e.c.Resume(api.ResumeParams{ClaudeInstanceID: r.ID})
		return err
	}}
}

// scriptResume scripts s on every resume create.
func scriptResume(s tmuxfix.Script) func(*testing.T, *resumeEnv) []apitest.SpawnOption {
	return func(_ *testing.T, e *resumeEnv) []apitest.SpawnOption {
		e.rec.Script(tmuxfix.AnySocket, s, tmux.CallCreate)
		return nil
	}
}

// oneNameResumeRows are resume's tmux-caused errors (SR-8.1, SR-8.5).
func oneNameResumeRows() []oneNameRow {
	return []oneNameRow{
		oneNameResume("recorded socket's parent vanished", "ErrTmuxNotAvailable", func(t *testing.T, _ *resumeEnv) []apitest.SpawnOption {
			return []apitest.SpawnOption{apitest.WithTmuxSocket(filepath.Join(userSocketDir(filepath.Join(t.TempDir(), "gone")), "default"))}
		}),
		oneNameResume("recorded name already held", "ErrTmuxSessionCreate", func(_ *testing.T, e *resumeEnv) []apitest.SpawnOption {
			e.rec.WithHasSession(true)
			return nil
		}),
		oneNameResume("create: binary unavailable", "ErrTmuxNotAvailable", scriptResume(tmuxfix.Script{Failure: tmux.FailUnavailable})),
		oneNameResume("create: socket permission", "ErrTmuxNotAvailable", scriptResume(tmuxfix.Script{Failure: tmux.FailSocketDenied})),
		oneNameResume("create: timeout", "ErrTmuxUnresponsive", scriptResume(tmuxfix.Script{Failure: tmux.FailTimeout})),
		oneNameResume("create: non-zero exit, unparseable reply", "ErrTmuxUnresponsive",
			scriptResume(tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1, HadStdout: true})),
		oneNameResume("create: no server", "ErrTmuxSessionCreate", scriptResume(tmuxfix.Script{Failure: tmux.FailNoServer})),
		oneNameResume("create: session not labelled", "ErrTmuxSessionCreate", func(_ *testing.T, e *resumeEnv) []apitest.SpawnOption {
			e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailLabel, Times: 1}, tmux.CallCreate).
				Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1}, tmux.CallSetLabel)
			return nil
		}),
	}
}

// oneNameInternalRows are the ErrInternal cases reachable today: spawn's
// collision pre-check read failure, resume's control-character id and its
// failed move to pending.
func oneNameInternalRows() []oneNameRow {
	preCheck := func(name string, readErr error) oneNameRow {
		return oneNameRow{name: "spawn/pre-check read fails: " + name, run: func(t *testing.T) error {
			e := newSpawnEnv(t)
			_, err := api.SpawnWithCollisionReader(e.c, failingCollisionReader{err: readErr},
				api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: "one-" + uuid.NewString()[:8]})
			return err
		}}
	}
	return []oneNameRow{
		preCheck("plain store error", errors.New("store: live spawn lookup: disk I/O error")),
		preCheck("wraps ErrTmuxNotAvailable", fmt.Errorf("store: live spawn lookup: %w", tmux.ErrTmuxNotAvailable)),
		{name: "resume/control character in the id", run: func(t *testing.T) error {
			e := newResumeEnv(t)
			suffix := uuid.NewString()[:8]
			r := e.seedRow(t, resumableSpec{ID: "legacy\x1b" + suffix, SessionID: "sess-ctl-" + suffix})
			_, err := e.c.Resume(api.ResumeParams{ClaudeInstanceID: r.ID})
			return err
		}},
		{name: "resume/move to pending fails", run: func(t *testing.T) error {
			e := newResumeEnv(t)
			r := e.seedResumable(t, store.StateEnded)
			e.store.failMove(fmt.Errorf("store: move: %w", tmux.ErrTmuxSessionCreate))
			_, err := e.resume(r.ID)
			return err
		}},
	}
}

// oneNameKill is a row that runs api.Kill on a row seeded with spec after
// setup (when set) prepares e and r.
func oneNameKill(name, want string, spec killRowSpec, setup func(t *testing.T, e *killEnv, r *killRow)) oneNameRow {
	return oneNameRow{name: "kill/" + name, want: want, run: func(t *testing.T) error {
		e := newKillEnv(t)
		r := e.seedRow(t, spec)
		if setup != nil {
			setup(t, e, &r)
		}
		_, err := e.kill(r.ID)
		return err
	}}
}

// scriptKill scripts ss in order on every call of kind call on r's socket.
func scriptKill(call tmux.Call, ss ...tmuxfix.Script) func(*testing.T, *killEnv, *killRow) {
	return func(_ *testing.T, e *killEnv, r *killRow) {
		for _, s := range ss {
			e.rec.Script(r.Socket, s, call)
		}
	}
}

// oneNameKillRows are kill's returned errors (SR-6.1): the three
// ErrTmuxKillFailed variants and a survivor alone, both Leftover and
// conflicting labels, each ErrTmuxNotAvailable and ErrTmuxUnresponsive
// cause, and ErrInternal for each kind of unusable recorded name.
func oneNameKillRows() []oneNameRow {
	lookup, unreadable := tmux.CallLookup, killRowSpec{Agent: agentUnreadable}
	noSession := func(name string) killRowSpec {
		return killRowSpec{NoSession: true, Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName(name)}}
	}
	timeout := tmuxfix.Script{Failure: tmux.FailTimeout}
	return []oneNameRow{
		oneNameKill("agent outlives the exit wait", "ErrTmuxKillFailed", killRowSpec{}, nil),
		oneNameKill("only a survivor outlives the exit wait", "ErrTmuxKillFailed", killRowSpec{Teammates: 1},
			func(_ *testing.T, e *killEnv, r *killRow) {
				e.setAfterCall(tmux.CallKillPane, procfix.Gone(), r.AgentPID)
			}),
		oneNameKill("process unreadable, labelled session still there", "ErrTmuxKillFailed", unreadable,
			func(t *testing.T, e *killEnv, r *killRow) {
				scriptKill(tmux.CallKillPane, timeout)(t, e, r)
				scriptKill(tmux.CallKillSession, timeout)(t, e, r)
			}),
		oneNameKill("gone, process running, no pane", "ErrTmuxKillFailed", killRowSpec{NoSession: true}, nil),
		oneNameKill("leftover", "ErrTmuxSessionConflict", killRowSpec{NoSession: true},
			func(t *testing.T, e *killEnv, r *killRow) {
				e.seedSession(t, r, tmuxfix.WithRowSessionLabel(r.old(), true))
			}),
		oneNameKill("conflicting labels: scope value", "ErrTmuxSessionConflict", killRowSpec{},
			func(_ *testing.T, e *killEnv, r *killRow) {
				e.rec.SetScope(r.Socket, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{})
			}),
		oneNameKill("conflicting labels: duplicate label", "ErrTmuxSessionConflict", killRowSpec{},
			func(t *testing.T, e *killEnv, r *killRow) {
				e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: "dup-" + r.ID, Label: r.current()})
			}),
		oneNameKill("different server", "ErrTmuxNotAvailable", killRowSpec{}, func(t *testing.T, e *killEnv, r *killRow) {
			e.rec.RebindServer(r.Socket, tmuxfix.Server{})
			e.syncServers()
			e.seedBystander(t, r.Socket)
		}),
		oneNameKill("binary unavailable", "ErrTmuxNotAvailable", killRowSpec{},
			scriptKill(lookup, tmuxfix.Script{Failure: tmux.FailUnavailable})),
		oneNameKill("socket permission", "ErrTmuxNotAvailable", killRowSpec{},
			scriptKill(lookup, tmuxfix.Script{Failure: tmux.FailSocketDenied})),
		oneNameKill("lookup timeout", "ErrTmuxUnresponsive", killRowSpec{}, scriptKill(lookup, timeout)),
		oneNameKill("lookup unrecognised reply", "ErrTmuxUnresponsive", killRowSpec{}, scriptKill(lookup,
			tmuxfix.Script{Failure: tmux.FailUnrecognized, FirstLine: callTableFirstLine(), ExitStatus: 1})),
		oneNameKill("follow-up unanswered after a kill", "ErrTmuxUnresponsive", unreadable,
			scriptKill(lookup, tmuxfix.Script{Times: 1}, timeout)),
		oneNameKill("empty recorded name", "", noSession(""), nil),
		oneNameKill("control character in the recorded name", "", noSession("ts-\x1b"), nil),
		oneNameKill("recorded name tmux rewrites", "", noSession("a.b"), nil),
	}
}
