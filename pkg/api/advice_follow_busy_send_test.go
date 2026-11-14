package api_test

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// busyCantSendAdvice is ErrCantSend's agent_not_covered advice (SR-9.2).
const busyCantSendAdvice = "make the same call without them"

// TestAdviceFollow_E22_CantSendSameCallWithout: E22 "make the same call
// without them". A send-keys naming force for an uncovered agent is refused;
// the same send-keys without it sends its key.
func TestAdviceFollow_E22_CantSendSameCallWithout(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{})
	p := api.SendKeysParams{InstanceID: r.ID, Key: "Escape", Force: ptr(false)}
	_, err := e.sendKeys(p)
	adviceAssertAdvice(t, err, api.ErrCantSend, busyCantSendAdvice)
	p.Force = nil
	if _, err := e.sendKeys(p); err != nil {
		t.Fatalf("the same send-keys without force: %v", err)
	}
	var keys []string
	for _, c := range e.rec.SocketCalls() {
		if c.Call == tmux.CallSendKey {
			keys = append(keys, c.Key)
		}
	}
	if len(keys) != 1 || keys[0] != "Escape" {
		t.Errorf("keys sent = %q; want the one Escape", keys)
	}
}

// TestAdviceFollow_B17_CantSendSameResumeWithout: B17 "make the same call
// without them". A resume setting an Agent Default for an uncovered agent is
// refused; the same resume without it moves the row to pending.
func TestAdviceFollow_B17_CantSendSameResumeWithout(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	r := e.seedResumableRow(t, e.resumableSpec(rlkSettled(e), agentNotRecorded))
	cfg := config.Default()
	cfg.Tmux = e.cfg
	_, err := api.Resume(e.st, e.rec, e.pc, cfg, e.storeID, e.clock.Now, nil,
		api.ResumeParams{InstanceID: r.ID, ConfirmedDelivery: ptr(false)})
	adviceAssertAdvice(t, err, api.ErrCantSend, busyCantSendAdvice)
	if _, err := e.resumeWith(e.st, r.ID); err != nil {
		t.Fatalf("the same resume without confirmed_delivery: %v", err)
	}
	if row, err := e.st.GetSpawn(r.ID); err != nil || row.State != store.StatePending {
		t.Errorf("row after the resume = %q, %v; want pending", row.State, err)
	}
}

// TestAdviceFollow_A32_CantSendSameLaunchWithout: A32 "make the same call
// without them". A spawn, and a make-template naming its agent, that set an
// Agent Default for an uncovered agent are refused; the same calls without it
// launch the agent and save the template.
func TestAdviceFollow_A32_CantSendSameLaunchWithout(t *testing.T) {
	// Serial: newSpawnEnv sets HOME, TMUX and TMUX_TMPDIR with t.Setenv.
	e := newSpawnEnv(t)
	p := api.SpawnParams{Agent: "codex", CWD: t.TempDir(), NoPreTrust: true, SendMode: ptr("wait_turn")}
	_, err := e.c.Spawn(p)
	adviceAssertAdvice(t, err, api.ErrCantSend, busyCantSendAdvice)
	p.SendMode = nil
	res, err := e.c.Spawn(p)
	if err != nil {
		t.Fatalf("the same spawn without send_mode: %v", err)
	}
	if row, err := e.c.Get(res.InstanceID); err != nil || row.Agent != "codex" {
		t.Errorf("Get(%s) = %+v, %v; want the codex row", res.InstanceID, row, err)
	}
	m := api.MakeTemplateParams{Name: "follow", Agent: "codex", ConfirmFromTranscripts: ptr(false)}
	_, err = e.c.MakeTemplate(m)
	adviceAssertAdvice(t, err, api.ErrCantSend, busyCantSendAdvice)
	m.ConfirmFromTranscripts = nil
	if _, err := e.c.MakeTemplate(m); err != nil {
		t.Fatalf("the same make-template without confirm_from_transcripts: %v", err)
	}
	if tf, err := config.LoadTemplate("follow"); err != nil || tf.Agent != "codex" {
		t.Errorf("saved template = %+v, %v; want codex's", tf, err)
	}
}
