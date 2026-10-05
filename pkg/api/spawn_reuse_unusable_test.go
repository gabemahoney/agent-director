package api_test

// spawn_reuse_unusable_test.go: a reuse of a finished row whose recorded name
// is unusable (SR-3.2, SR-10.2) is that name's ErrInternal before the socket
// and the old-row lookup, whatever name is requested: no tmux call, no
// pre-trust, no reset, archive or event. A live row, or a finished row without
// the opt-in, still collides first.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rusRow is one unusable-name row a reuse meets: its fixture, and badSocket,
// a recorded socket whose directory cannot be created.
type rusRow struct {
	label     string
	f         unusableNameFixture
	badSocket bool
}

// rusRows are each fixture, then the pre-b.gqe default name with a socket
// directory that cannot be created.
func rusRows() []rusRow {
	var rows []rusRow
	var badSocket rusRow
	for _, f := range unusableNameFixtures() {
		rows = append(rows, rusRow{label: f.label, f: f})
		if f.raw == preGqeDefaultName {
			badSocket = rusRow{label: f.label + ", socket directory not creatable", f: f, badSocket: true}
		}
	}
	return append(rows, badSocket)
}

// rusName is a spawn option recording name as the row's tmux session name.
func rusName(name string) []apitest.SpawnOption {
	return []apitest.SpawnOption{apitest.WithTmuxSessionName(name)}
}

// TestSpawnReuseUnusableName: an ended or missing row with each unusable name,
// reused under the default or a new name, gets its ErrInternal with no tmux call and nothing written.
func TestSpawnReuseUnusableName(t *testing.T) {
	for _, state := range finishedStates {
		for _, row := range rusRows() {
			for _, named := range []bool{false, true} {
				request := "default name"
				if named {
					request = "new name"
				}
				t.Run(state+"/"+row.label+"/"+request, func(t *testing.T) {
					e := newKillEnv(t)
					spec := reuseRowSpec{State: state, Opts: rusName(row.f.raw)}
					if row.f.raw == preGqeDefaultName {
						spec.ID = "b.18k-fix" // the id whose default name it was
					}
					if row.badSocket {
						sock := filepath.Join(userSocketDir(filepath.Join(t.TempDir(), "gone")), "default")
						spec.Opts = append(spec.Opts, apitest.WithTmuxSocket(sock))
					}
					r := e.seedReusable(t, agentGone, spec)
					cwd := filepath.Join(t.TempDir(), "proj") // today's default name is proj-<id>
					if err := os.Mkdir(cwd, 0o700); err != nil {
						t.Fatalf("mkdir: %v", err)
					}
					p := reuseParams(t, r, reuseRequest{Name: "reuse-new-" + uuid.NewString()[:8], CWD: cwd})
					if !named {
						p.TmuxSessionName = ""
					}
					before := e.snapshotReuse(t, r)

					_, _, err := e.reuse(t, p)

					rtabAssertInternal(t, err, row.f.desc)
					e.rtabNoNewCalls(t, before)
					e.assertWroteNothing(t, before)
					if recs := before.since(t, "ad.provenance.disagree"); len(recs) != 0 {
						t.Errorf("ad.provenance.disagree records = %v; want none", recs)
					}
				})
			}
		}
	}
}

// TestSpawnReuseUnusableNameCollides: a finished row without the opt-in, or a
// live row with it, whose recorded name is unusable is ErrInstanceIdCollision
// at the pre-check with no tmux call and nothing written, the trust file
// included (b.hjs).
func TestSpawnReuseUnusableNameCollides(t *testing.T) {
	cases := []struct {
		state string
		reuse bool
	}{
		{store.StateEnded, false},
		{store.StateMissing, false},
		{store.StatePending, true},
		{store.StateWaiting, true},
	}
	for _, tc := range cases {
		for _, f := range unusableNameFixtures() {
			name := tc.state + ", without the opt-in/" + f.label
			if tc.reuse {
				name = tc.state + ", with the opt-in/" + f.label
			}
			t.Run(name, func(t *testing.T) {
				e := newKillEnv(t)
				r := e.seedRow(t, killRowSpec{State: tc.state, NoSession: true, Opts: rusName(f.raw)})
				trust := seedTrustConfig(t, t.TempDir(), trustLacksEntry)
				p := rtabParams(t, r.ID, "reuse-new-"+uuid.NewString()[:8], trust)
				p.ReuseFinished = tc.reuse
				before := e.snapshotWrites(t, "spawn", r.ID, trust, r.Socket)

				_, _, err := e.reuse(t, p)

				assertOneSentinel(t, err, spawn.ErrInstanceIdCollision)
				e.rtabNoNewCalls(t, before)
				e.assertWroteNothing(t, before)
			})
		}
	}
}
