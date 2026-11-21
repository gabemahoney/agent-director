package api_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/resumedelegate"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// B21: "install a version that covers agent codex and retry resume".
func TestAdviceFollow_B21_ResumeCoveringVersionUnavailable(t *testing.T) {
	// Serial: effective HOME and trail observations.
	e := newDelegateEnv(t)
	p := api.ResumeParams{InstanceID: e.row.ID, ConfirmedDelivery: ptr(false)}
	check := e.untouched(t)
	_, err := e.c.Resume(p)
	adviceAssertAdvice(t, err, api.ErrSpawnNotResumable, "install a version that covers agent codex and retry resume")
	check()
	e.covering(t) // follow the advice in this scratch layout
	e.run(t, func(resumedelegate.Request) ([]byte, bool, error) { return delegateResult(e.row.ID), true, nil })
	check = e.untouched(t)
	res, err := e.c.Resume(p) // same request, including explicit false
	if err != nil || res.InstanceID != p.InstanceID || len(e.calls) != 1 {
		t.Fatalf("literal retry = %+v / %v, calls %d", res, err, len(e.calls))
	}
	check()
}

// B22: "check the agent with get before taking further action".
func TestAdviceFollow_B22_ResumeUnknownOutcomeGet(t *testing.T) {
	// Serial: effective HOME and trail observations; the runner models the recipient.
	e := newDelegateEnv(t)
	e.covering(t)
	var token string
	capabilities := storefix.BusySendWaitOnlyCapabilities()
	e.run(t, func(resumedelegate.Request) ([]byte, bool, error) {
		// Real authoring is covered by TestMixedVersionDelegatedCapabilities and TestBusySendStagedPublication.
		token = storefix.SeedBusySendResumeCandidate(t, e.dbPath, e.row.ID, e.socket, e.clock.Now().UnixMilli(), capabilities)
		return nil, true, errors.New("response lost after recipient move")
	})
	_, err := e.c.Resume(api.ResumeParams{InstanceID: e.row.ID})
	assertOneName(t, err, "ErrInternal")
	if err.Error() != "delegated resume outcome is unknown; a Launch may have been created; check the agent with get before taking further action" || api.ErrDetails(err) != nil {
		t.Fatalf("unknown advice: %v / %+v", err, api.ErrDetails(err))
	}
	row, err := e.c.Get(e.row.ID) // literally follow get; never repeat Resume
	if err != nil || row.State != store.StatePending {
		t.Fatalf("get after lost reply: %+v / %v", row, err)
	}
	if stored, err := e.st.GetSpawn(e.row.ID); err != nil || stored.Identity.Token != token {
		t.Fatalf("get's pending Launch is not the recipient's: %+v / %v", stored, err)
	}
	if len(e.calls) != 1 || len(e.rec.SocketCalls()) != 0 {
		t.Fatalf("automatic retry/input: calls %d; tmux %+v", len(e.calls), e.rec.SocketCalls())
	}
	// Validate the fixture only after the advice flow: the candidate must
	// publish normally and pass the production capabilities reader unchanged.
	assertBusyLaunchPhase(t, e.dbPath, e.row.ID, "resume", "candidate", store.BusySendCovered)
	if res, err := e.st.PublishBusySendLaunch(e.row.ID, 0, token); err != nil || res != store.CondApplied {
		t.Fatalf("seeded candidate publication: %v / %v", res, err)
	}
	assertBusyLaunchPhase(t, e.dbPath, e.row.ID, "resume", "current", store.BusySendCovered)
	observation, err := e.st.ReadBusySendObservation(e.row.ID, store.DefaultLockWait)
	if err != nil || observation == nil || !reflect.DeepEqual(observation.Capabilities, *capabilities) {
		t.Fatalf("seeded candidate capabilities: %+v / %v", observation, err)
	}
}
