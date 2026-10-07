package api_test

// resume_unusable_name_test.go covers resume's unusable recorded-name refusal
// (SR-8.1 step 2, SR-3.2, SR-1.4; Epic 19): ErrInternal with no tmux call and
// nothing written, after step 1's guards and without re-making a vanished
// socket directory. The control-character id that loses to it is
// TestResumeRefusesControlCharacterID's (resume_pending_launch_test.go).

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestResumeUnusableNameWritesNothing: each unusable name, and the catalogue's mix.$b with its stored
// form held, gets ErrInternal with no tmux call and nothing written, a caller instance id set.
func TestResumeUnusableNameWritesNothing(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID with t.Setenv; it checks every record written to the
	// shared trail since its mark.
	type unusableCase struct {
		fixture unusableNameFixture
		held    bool // a session holds the name's stored form
	}
	var cases []unusableCase
	for _, f := range unusableNameFixtures() {
		cases = append(cases, unusableCase{fixture: f})
	}
	mix := unusableNameFixture{label: "catalogue name mix.$b, its stored form held", raw: "mix.$b",
		desc: apitest.DescUnusableNameRewritten("mix.$b", apitest.RewrittenChars{Dot: true})}
	cases = append(cases, unusableCase{fixture: mix, held: true})
	for _, tc := range cases {
		t.Run(tc.fixture.label, func(t *testing.T) {
			e := newKillEnv(t)
			caller := e.seedRow(t, killRowSpec{NoSession: true})
			t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", caller.ID)
			r := e.seedResumable(t, time.Hour, agentGone, apitest.WithTmuxSessionName(tc.fixture.raw))
			if tc.held {
				e.seedHolder(t, r.killRow, holderOld)
			}
			before := e.snapshotResume(t, r)

			_, err := e.resume(r.ID)

			assertOneName(t, err, "ErrInternal")
			apitest.AssertDescription(t, err.Error(), tc.fixture.desc, r.ID, caller.ID, r.Token)
			e.assertKillCalls(t)
			e.assertResumeWroteNothing(t, before)
		})
	}
}

// TestResumeStepOneGuardsBeatUnusableName: with an unusable recorded name, a live row, a row with
// no session id and a missing transcript keep their own refusal, with no tmux call.
func TestResumeStepOneGuardsBeatUnusableName(t *testing.T) {
	t.Parallel()
	unusable := apitest.WithTmuxSessionName(preGqeDefaultName)
	cases := []struct {
		name string
		seed func(*testing.T, *killEnv) string
		want error
	}{
		{"live row", func(t *testing.T, e *killEnv) string {
			return e.seedRow(t, killRowSpec{State: store.StateWaiting, Agent: agentGone, NoSession: true,
				Opts: []apitest.SpawnOption{unusable}}).ID
		}, api.ErrSpawnNotResumable},
		{"no session id", func(t *testing.T, e *killEnv) string {
			return e.seedRow(t, killRowSpec{State: store.StateEnded, Agent: agentGone, NoSession: true,
				Opts: []apitest.SpawnOption{unusable}}).ID
		}, api.ErrNoSessionId},
		{"transcript missing", func(t *testing.T, e *killEnv) string {
			r := e.seedResumable(t, time.Hour, agentGone, unusable)
			if err := os.Remove(r.JSONLPath); err != nil {
				t.Fatalf("remove transcript: %v", err)
			}
			return r.ID
		}, api.ErrJsonlMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			id := tc.seed(t, e)

			_, err := e.resume(id)

			if !errors.Is(err, tc.want) {
				t.Fatalf("resume err = %v; want %v", err, tc.want)
			}
			assertNoTmuxCalls(t, e.rec)
		})
	}
}

// TestResumeUnusableNameLeavesVanishedSocketDir: a recorded socket whose per-user directory has
// gone, with an unusable name, gets ErrInternal and the directory is not made again.
func TestResumeUnusableNameLeavesVanishedSocketDir(t *testing.T) {
	t.Parallel()
	e := newResumeEnv(t)
	sock := vanishedUserSocket(t)
	r := e.seedResumable(t, "", apitest.WithTmuxSocket(sock), apitest.WithTmuxSessionName(preGqeDefaultName))

	_, err := e.resume(r.ID)

	assertOneName(t, err, "ErrInternal")
	apitest.AssertDescription(t, err.Error(),
		apitest.DescUnusableNameRewritten(preGqeDefaultName, apitest.RewrittenChars{Dot: true}), r.ID)
	assertNoTmuxCalls(t, e.rec)
	if _, err := os.Stat(filepath.Dir(sock)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat %s: %v; want still missing", filepath.Dir(sock), err)
	}
}
