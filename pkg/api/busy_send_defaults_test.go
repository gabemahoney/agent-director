package api_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestBusySendResumeDefaults (SR-1.4, SR-6.3): a covered resume's Agent
// Default overrides become the conversation's only when its Launch is
// published, by its creator or by a later adoption, over the published
// values: an explicit false replaces true (confirmed_delivery,
// confirm_from_transcripts) and an omitted value is kept. Until then get and
// admission use the published values, and a failed creation never applies
// them. A Message admitted while the launch was being created keeps its
// Result, sequence and reservation whether the creation fails or publishes.
func TestBusySendResumeDefaults(t *testing.T) {
	t.Parallel()
	const published = `{"send_mode":"wait_turn","confirmed_delivery":true,"confirm_from_transcripts":true}`
	setup := func(t *testing.T) (*killEnv, *api.Client, resumeRow) {
		t.Helper()
		e := newKillEnv(t)
		c, _ := e.client(t)
		r := e.seedResumable(t, time.Hour, agentGone)
		seedBusyCovered(t, e, r.ID)
		if err := apitest.SeedBusySendDefaults(e.dbPath, r.ID, store.BusySendAgentDefaults{SendMode: store.BusySendWaitTurn,
			Confirmed: true, ConfirmFromTranscripts: true}); err != nil {
			t.Fatal(err)
		}
		return e, c, r
	}
	resume := func(e *killEnv, p api.ResumeParams) error {
		cfg := config.Default()
		cfg.Tmux = e.cfg
		_, err := api.Resume(e.st, e.rec, e.pc, cfg, e.storeID, e.clock.Now, nil, p)
		return err
	}
	defaults := func(t *testing.T, e *killEnv, id string) string {
		t.Helper()
		out, err := api.Get(e.st, api.RelayView{}, id)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := jsonField(t, out, "agent_defaults")
		return string(raw)
	}
	// admitWhileCreating admits, as the creation returns, a Message naming
	// only its pair, which the published default confirms.
	admitWhileCreating := func(t *testing.T, e *killEnv, c *api.Client, id string) *api.DeliveryResult {
		t.Helper()
		var pending api.DeliveryResult
		adviceOnceAfter(e.rec, tmux.CallCreate, func() {
			if got := defaults(t, e, id); got != published {
				t.Errorf("agent_defaults while the resume is pending = %s; want the published %s", got, published)
			}
			pending = busyAdmit(t, c, api.SendKeysParams{InstanceID: id, Text: "while starting", CallerUUID: ptr(busyCaller), DeliveryRequestID: "p-1"})
		})
		return &pending
	}
	// assertKept fails unless that Message is still Held, owned and reserved
	// in the published conversation, its Result as admitted but waiting wait:
	// agent_starting while a Launch is starting, launch_ended once a failed
	// creation's restoration finished the row again.
	assertKept := func(t *testing.T, e *killEnv, c *api.Client, id string, pending api.DeliveryResult, wait string) {
		t.Helper()
		if pending.RequestID != "p-1" || pending.Wait == nil || pending.Wait.Reason != "agent_starting" {
			t.Errorf("Result admitted while pending = %+v; want p-1 waiting agent_starting", pending)
		}
		got, err := c.GetDelivery(api.GetDeliveryParams{CallerUUID: busyCaller, DeliveryRequestID: "p-1"})
		want := pending
		if want.Wait != nil && want.Wait.Reason != wait && got.Result != nil && got.Result.Wait != nil {
			want.Reason, want.Wait = wait, &api.DeliveryWait{Reason: wait, Since: got.Result.Wait.Since}
		}
		if err != nil || got.Result == nil || !reflect.DeepEqual(*got.Result, want) {
			t.Errorf("GetDelivery(p-1) = %+v, %v; want %+v", got, err, want)
		}
		facts := busyFacts(t, e.dbPath, id)
		for _, conv := range facts.Conversations {
			if conv.ID == facts.Binding && (conv.AcceptedSeq != 1 || conv.ReservedCount != 1 || len(facts.Messages) != 1 ||
				facts.Messages[0].Place != "held" || !facts.Messages[0].Confirmed) {
				t.Errorf("published conversation %+v, Messages %+v; want the confirmed Message kept Held and reserved", conv, facts.Messages)
			}
		}
	}

	t.Run("failed creation", func(t *testing.T) {
		t.Parallel()
		e, c, r := setup(t)
		e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailUnavailable, ExitStatus: 1, Times: 1}, tmux.CallCreate)
		pending := admitWhileCreating(t, e, c, r.ID)
		err := resume(e, api.ResumeParams{InstanceID: r.ID, ConfirmedDelivery: ptr(false), ConfirmFromTranscripts: ptr(false)})
		assertOneSentinel(t, err, api.ErrTmuxNotAvailable)
		assertBusyLaunchPhase(t, e.dbPath, r.ID, "resume", "failed", store.BusySendCovered)
		if got := defaults(t, e, r.ID); got != published {
			t.Errorf("agent_defaults after a failed resume = %s; want the published %s", got, published)
		}
		assertKept(t, e, c, r.ID, *pending, "launch_ended")
	})
	t.Run("published by its creator", func(t *testing.T) {
		t.Parallel()
		e, c, r := setup(t)
		pending := admitWhileCreating(t, e, c, r.ID)
		if err := resume(e, api.ResumeParams{InstanceID: r.ID, ConfirmedDelivery: ptr(false)}); err != nil {
			t.Fatal(err)
		}
		if got, want := defaults(t, e, r.ID), `{"send_mode":"wait_turn","confirmed_delivery":false,"confirm_from_transcripts":true}`; got != want {
			t.Errorf("agent_defaults after publication = %s; want %s", got, want)
		}
		// Confirm From Transcripts on changes no capability (SR-4.5; E5 decision 4).
		if out, err := api.Get(e.st, api.RelayView{}, r.ID); err != nil {
			t.Fatal(err)
		} else if raw, _ := jsonField(t, out, "delivery_capabilities"); string(raw) != busyCapabilitiesJSON {
			t.Errorf("delivery_capabilities with confirm_from_transcripts on = %s; want %s", raw, busyCapabilitiesJSON)
		}
		assertBusyLaunchPhase(t, e.dbPath, r.ID, "resume", "current", store.BusySendCovered)
		assertKept(t, e, c, r.ID, *pending, "agent_starting")
		// Unconfirmed now by default: a Caller UUID alone is malformed.
		_, err := c.SendKeys(api.SendKeysParams{InstanceID: r.ID, Text: "after", CallerUUID: ptr(busyCaller)})
		assertOneName(t, err, "ErrInvalidFlags")
		if after := busyAdmit(t, c, api.SendKeysParams{InstanceID: r.ID, Text: "after"}); after.CallerUUID != "" || after.RequestID != "" {
			t.Errorf("send after publication = %+v; want an unconfirmed Result", after)
		}
	})
	t.Run("uncertain creation published by adoption", func(t *testing.T) {
		t.Parallel()
		e, _, r := setup(t)
		e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailTimeout, Applied: true, Times: 1}, tmux.CallCreate)
		err := resume(e, api.ResumeParams{InstanceID: r.ID, ConfirmFromTranscripts: ptr(false)})
		assertOneSentinel(t, err, api.ErrTmuxUnresponsive)
		assertBusyLaunchPhase(t, e.dbPath, r.ID, "resume", "uncertain", store.BusySendCovered)
		if got := defaults(t, e, r.ID); got != published {
			t.Errorf("agent_defaults after an uncertain resume = %s; want the published %s", got, published)
		}
		// A later guarded adoption of the created pane publishes the candidate.
		creates := e.rec.SocketCallsOf(tmux.CallCreate)
		created := rplSessionNamed(t, e.rec, r.Socket, storedFormOf(r.Name))
		srv, _ := e.rec.Server(r.Socket)
		row, err := e.st.GetSpawn(r.ID)
		if err != nil || len(creates) != 1 {
			t.Fatalf("row %v, creates %+v", err, creates)
		}
		identity := store.LaunchIdentity{Token: creates[0].Token, Socket: r.Socket, ServerPID: srv.PID, ServerStart: srv.Start,
			ServerStarttime: apitest.LinuxProcStarttime, PaneID: created.Panes[0].ID, PanePID: created.Panes[0].PID,
			PaneStarttime: apitest.DarwinProcStarttime}
		if res, err := e.st.AdoptIdentityIfUnchanged(r.ID, row.Snapshot, identity); err != nil || res != store.CondApplied {
			t.Fatalf("adoption = %v, %v", res, err)
		}
		assertBusyLaunchPhase(t, e.dbPath, r.ID, "resume", "current", store.BusySendCovered)
		if got, want := defaults(t, e, r.ID), `{"send_mode":"wait_turn","confirmed_delivery":true,"confirm_from_transcripts":false}`; got != want {
			t.Errorf("agent_defaults after adoption = %s; want %s", got, want)
		}
	})
}

// TestBusySendCoveredLaunchDefaults (SR-1.4, SR-9.2; carried from Task
// t2.xis.oo.tc's product review): a covered (Claude) fresh spawn and reuse
// resolve each Agent Default from the call, then the template, then the
// out-of-the-box value, an explicit false kept over the template's true.
// send_mode steer_turn, which Claude's capability table does not offer, is
// ErrCantSend steer_unsupported on spawn, reuse and make-template, before
// anything is written or launched.
func TestBusySendCoveredLaunchDefaults(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	t.Setenv("HOME", t.TempDir())
	on, steer := true, "steer_turn"
	if _, err := apitest.SeedTemplate(config.TemplatesDir(), "covered", apitest.TemplateBody(apitest.TemplateFields{
		ConfirmedDelivery: &on, ConfirmFromTranscripts: &on})); err != nil {
		t.Fatal(err)
	}
	want := store.BusySendAgentDefaults{SendMode: store.BusySendWaitTurn, Confirmed: false, ConfirmFromTranscripts: true}
	published := func(t *testing.T, e *killEnv, id string) store.BusySendAgentDefaults {
		t.Helper()
		facts := busyFacts(t, e.dbPath, id)
		for _, c := range facts.Conversations {
			if c.ID == facts.Binding {
				return c.Defaults
			}
		}
		t.Fatalf("%s has no published conversation: %+v", id, facts)
		return store.BusySendAgentDefaults{}
	}
	t.Run("fresh spawn", func(t *testing.T) {
		e := newKillEnv(t)
		res, err := e.busyClient(t).Spawn(api.SpawnParams{Agent: "claude", Template: "covered", CWD: t.TempDir(), NoPreTrust: true,
			ConfirmedDelivery: ptr(false)})
		if err != nil {
			t.Fatal(err)
		}
		if got := published(t, e, res.InstanceID); got != want {
			t.Errorf("published Agent Defaults = %+v; want %+v", got, want)
		}
	})
	t.Run("reuse", func(t *testing.T) {
		e := newKillEnv(t)
		r := e.seedReusable(t, agentGone, reuseRowSpec{})
		p := reuseParams(t, r, reuseRequest{})
		p.Template, p.ConfirmedDelivery = "covered", ptr(false)
		if _, _, err := e.reuse(t, p); err != nil {
			t.Fatal(err)
		}
		if got := published(t, e, r.ID); got != want {
			t.Errorf("published Agent Defaults = %+v; want %+v", got, want)
		}
	})
	t.Run("steer_turn refused", func(t *testing.T) {
		e := newKillEnv(t)
		r := e.seedReusable(t, agentGone, reuseRowSpec{})
		reuse := reuseParams(t, r, reuseRequest{})
		reuse.SendMode = &steer
		c := e.busyClient(t)
		for verb, call := range map[string]func() error{
			"spawn": func() error {
				_, err := c.Spawn(api.SpawnParams{Agent: "claude", CWD: t.TempDir(), NoPreTrust: true, SendMode: &steer})
				return err
			},
			"reuse": func() error { _, err := c.Spawn(reuse); return err },
			"make-template": func() error {
				_, err := c.MakeTemplate(api.MakeTemplateParams{Name: "steer", Agent: "claude", SendMode: &steer})
				return err
			},
		} {
			before := e.snapshotBusy(t, verb, r.resumeRow)
			err := call()
			assertOneName(t, err, "ErrCantSend")
			if got, want := api.ErrDetails(err), (api.CantSendDetails{Class: "cant_send", Reason: "steer_unsupported", ResendSafe: true,
				RetryWith: "new_send"}); got != any(want) {
				t.Errorf("%s err_details = %#v; want %#v", verb, got, want)
			}
			e.assertBusyRefused(t, before, "")
		}
		if _, err := os.Stat(filepath.Join(config.TemplatesDir(), "steer.toml")); !os.IsNotExist(err) {
			t.Errorf("refused make-template left steer.toml (%v)", err)
		}
	})
}
