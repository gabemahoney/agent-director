package api_test

// resume_pending_relife_test.go covers a second resume after a resumed agent's
// life (SR-8.4, SR-8.5; AC-RES-13): the resumed agent reports in with a new
// session id, its life ends, its session goes, and a second resume moves the
// row again, blocked by nothing the first resume left. On the shared fixture in
// resume_fixture_test.go against a real store; the hook helpers are in
// resume_pending_test.go.

import (
	"context"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// relifeSessionEnd delivers the SessionEnd a pause produces (reason
// prompt_input_exit) for id through the hook handler on the real store.
func relifeSessionEnd(t *testing.T, e *resumeEnv, id string) {
	t.Helper()
	payload := `{"hook_event_name":"SessionEnd","reason":"prompt_input_exit"}`
	env := func(k string) string {
		if k == "AGENT_DIRECTOR_INSTANCE_ID" {
			return id
		}
		return ""
	}
	if err := hook.Handle(context.Background(), strings.NewReader(payload), io.Discard, e.st,
		hook.HandleConfig{Env: env}, nil); err != nil {
		t.Fatalf("hook.Handle(SessionEnd): %v", err)
	}
}

// relifeResumeAs resumes id as a caller whose parent is parent.
func relifeResumeAs(t *testing.T, e *resumeEnv, id, parent string) {
	t.Helper()
	os.Setenv("AGENT_DIRECTOR_INSTANCE_ID", parent) //nolint:errcheck // newResumeEnv's t.Setenv restores it
	if _, err := e.resume(id); err != nil {
		t.Fatalf("Resume: %v", err)
	}
}

// TestResumeAgainAfterResumedAgentsLifeEnds: resume an ended row; its agent
// reports in with a new session id and later ends (SessionEnd, then its session
// goes); a second resume succeeds with one new create resuming the reported
// session, and its move applies again: pending, a new launch start, token and
// parent, ended_at cleared, row_version advanced and a second move event.
// Hook-driven: Task rc's hook gate (S17) converts the SessionStart and SessionEnd.
func TestResumeAgainAfterResumedAgentsLifeEnds(t *testing.T) {
	e := newResumeEnv(t)
	r := e.seedResumable(t, store.StateEnded)
	firstParent, secondParent := pendParent(t, e), pendParent(t, e)

	// (1) The first resume.
	firstStart := e.clock.Now().UnixMilli()
	relifeResumeAs(t, e, r.ID, firstParent)
	first := e.columns(t, r.ID)
	sess := e.rec.Sessions(e.socket)
	if first.State != store.StatePending || first.LaunchStartedAt != firstStart || len(sess) != 1 {
		t.Fatalf("after the first resume: {state %v, launch %#v}, sessions %+v; want pending at %d, one session",
			first.State, first.LaunchStartedAt, sess, firstStart)
	}

	// (2) Its agent reports in with a new session id whose transcript exists.
	newSess := "relife-" + uuid.NewString()
	newJSONL := apitest.SeedJsonl(t, r.CWD, newSess)
	pendSessionStart(t, e, r.ID, newJSONL)
	if c := e.columns(t, r.ID); c.State != store.StateWaiting || c.LaunchStartedAt != nil || c.ClaudeSessionID != newSess {
		t.Fatalf("after SessionStart: {state %v, launch %#v, session %v}; want waiting, NULL, %s",
			c.State, c.LaunchStartedAt, c.ClaudeSessionID, newSess)
	}

	// (3) Its life ends, and (4) its session goes.
	relifeSessionEnd(t, e, r.ID)
	if err := e.rec.KillSessionID(e.socket, sess[0].ID); err != nil {
		t.Fatalf("KillSessionID: %v", err)
	}
	before := e.columns(t, r.ID)
	if before.State != store.StateEnded || before.EndedAt == nil {
		t.Fatalf("after SessionEnd: {state %v, ended_at %#v}; want ended with ended_at set", before.State, before.EndedAt)
	}

	// (5) The second resume.
	createsBefore := len(e.rec.SocketCallsOf(tmux.CallCreate))
	secondStart := e.clock.Now().UnixMilli()
	var moved map[string]any
	e.store.afterMove(func() {
		c := e.columns(t, r.ID)
		moved = map[string]any{"state": c.State, "launch_started_at": c.LaunchStartedAt, "launch_token": c.LaunchToken,
			"ended_at": c.EndedAt, "parent_id": c.ParentID, "row_version": c.RowVersion}
	})
	relifeResumeAs(t, e, r.ID, secondParent)

	creates := e.rec.SocketCallsOf(tmux.CallCreate)[createsBefore:]
	if len(creates) != 1 {
		t.Fatalf("creates by the second resume = %d; want 1", len(creates))
	}
	if i := slices.Index(creates[0].Command, "--resume"); i < 0 || i+1 >= len(creates[0].Command) || creates[0].Command[i+1] != newSess {
		t.Errorf("argv = %q; want --resume %s (the session the resumed agent reported)", creates[0].Command, newSess)
	}
	if secondStart == firstStart {
		t.Fatalf("clock did not advance between the resumes (%d); the launch starts cannot be told apart", firstStart)
	}
	tok, _ := moved["launch_token"].(string)
	if !spawnTokenRE.MatchString(tok) || tok == first.LaunchToken || creates[0].Token != tok {
		t.Errorf("launch_token = %q (first resume's %#v, create's %q); want a new token the create labels with", tok, first.LaunchToken, creates[0].Token)
	}
	bv, _ := before.RowVersion.(int64)
	want := map[string]any{"state": store.StatePending, "launch_started_at": secondStart, "launch_token": tok,
		"ended_at": nil, "parent_id": secondParent, "row_version": bv + 1}
	for col, w := range want {
		if moved[col] != w {
			t.Errorf("%s after the second move = %#v; want %#v", col, moved[col], w)
		}
	}
	lines := pendTrail(t, "ad.resume.moved_to_pending", r.ID)
	if len(lines) != 2 {
		t.Fatalf("ad.resume.moved_to_pending lines = %d; want 2", len(lines))
	}
	assertAPITrailStr(t, lines[1], "prior_state", store.StateEnded)
	assertAPITrailStr(t, lines[1], "claude_session_id", newSess)
}
