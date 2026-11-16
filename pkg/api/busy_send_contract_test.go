package api_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// busyAgents are the agent kinds busy send doesn't cover: every kind but
// Claude Code, which this release covers (SR-9.1).
var busyAgents = []string{"codex", "cursor", "droid", "opencode"}

// busyAgentArgs are the arguments a launch or template of agent needs to pass
// validation: a model for the pane-only agents, none otherwise.
func busyAgentArgs(agent string) []string {
	if agent == "droid" || agent == "opencode" {
		return paneOnlyArgs(agent, "model")
	}
	return nil
}

// busyClient is e.client with the experimental pane-only agents enabled, so
// every agent kind passes validation and reaches the busy-send decision.
func (e *killEnv) busyClient(t *testing.T) *api.Client {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[experimental]\ndroid = true\nopencode = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, _, err := e.clientFor(t, path)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// assertCantSend fails unless err is SR-9.2's refusal of options (as the
// call named them, in parameter order): ErrCantSend alone, with exactly
// SR-9.4's agent_not_covered err_details and their JSON.
func assertCantSend(t *testing.T, err error, options string) {
	t.Helper()
	assertOneName(t, err, "ErrCantSend")
	want := api.CantSendDetails{Class: "cant_send", Reason: "agent_not_covered", ResendSafe: true, RetryWith: "new_send"}
	if got := api.ErrDetails(err); got != any(want) {
		t.Errorf("err_details = %#v; want %#v", got, want)
	}
	if raw, _ := json.Marshal(api.ErrDetails(err)); string(raw) != `{"class":"cant_send","reason":"agent_not_covered","resend_safe":true,"retry_with":"new_send"}` {
		t.Errorf("err_details JSON = %s", raw)
	}
	if err != nil && !strings.Contains(err.Error(), "so "+options+" cannot apply") {
		t.Errorf("refusal %q does not name %s", err, options)
	}
}

// assertNotPausableUncovered fails unless err is pause's only_if_waiting
// refusal for an uncovered agent: ErrSpawnNotPausable, cause agent_not_covered.
func assertNotPausableUncovered(t *testing.T, err error) {
	t.Helper()
	assertOneName(t, err, "ErrSpawnNotPausable")
	if got, want := api.ErrDetails(err), (api.NotPausableDetails{Cause: "agent_not_covered"}); got != any(want) {
		t.Errorf("err_details = %#v; want %#v", got, want)
	}
}

// busyRefusal is a refused busy-send call's writesSnapshot of r, with r's
// busy-send core facts, the store's busy-send records and its row count.
type busyRefusal struct {
	writesSnapshot
	facts  apitest.BusySendFacts
	counts storefix.BusySendCounts
	rows   int
}

// snapshotBusy takes r's busyRefusal for a call named verb; take it just
// before the call.
func (e *killEnv) snapshotBusy(t *testing.T, verb string, r resumeRow) busyRefusal {
	t.Helper()
	rows, err := e.st.ListSpawns(store.ListFilters{})
	if err != nil {
		t.Fatal(err)
	}
	return busyRefusal{writesSnapshot: e.snapshotWrites(t, verb, r.ID, r.Trust, r.Socket),
		facts: busyFacts(t, e.dbPath, r.ID), counts: storefix.ReadBusySendCounts(t, e.dbPath), rows: len(rows)}
}

// assertBusyRefused is assertWroteNothing for a refused busy-send call, with
// no tmux call at all, no row added and the busy-send core and records as
// before: no Message, pair, force attempt or journal acceptance. called names
// the Client's one call record (ad.send_keys.called, outcome ErrCantSend), or
// "" when the entry writes none.
func (e *killEnv) assertBusyRefused(t *testing.T, before busyRefusal, called string) {
	t.Helper()
	e.assertBusyRefusedAs(t, before, called, "ErrCantSend")
}

// assertBusyRefusedAs is assertBusyRefused with the call record's outcome.
func (e *killEnv) assertBusyRefusedAs(t *testing.T, before busyRefusal, called, outcome string) {
	t.Helper()
	if called != "" {
		recs := readAPITrailLines(t)[before.mark:]
		if len(recs) != 1 || recs[0]["event"] != called || recs[0]["instance_id"] != before.id || recs[0]["outcome"] != outcome {
			t.Errorf("trail since the call = %v; want one %s for %s with outcome %s", recs, called, before.id, outcome)
		}
		before.mark = trailMark(t)
	}
	e.assertWroteNothing(t, before.writesSnapshot)
	if calls := e.rec.SocketCalls()[before.calls:]; len(calls) != 0 {
		t.Errorf("tmux calls %+v; want none", calls)
	}
	if got := busyFacts(t, e.dbPath, before.id); !reflect.DeepEqual(got, before.facts) {
		t.Errorf("busy-send core of %s = %+v; want unchanged %+v", before.id, got, before.facts)
	}
	if got := storefix.ReadBusySendCounts(t, e.dbPath); got != before.counts {
		t.Errorf("busy-send records = %+v; want unchanged %+v", got, before.counts)
	}
	if rows, err := e.st.ListSpawns(store.ListFilters{}); err != nil || len(rows) != before.rows {
		t.Errorf("%d rows (%v); want %d", len(rows), err, before.rows)
	}
}

// TestBusySendUncoveredRefusalMatrix (SR-9.2, SR-9.4): for each uncovered
// agent kind, each entry naming a busy-send option, false and wait_turn
// included, is refused with ErrCantSend agent_not_covered (pause
// only_if_waiting: ErrSpawnNotPausable, cause agent_not_covered) on a live and
// a finished row, through the Client and the exported entries, and stores,
// sends, types, launches and writes nothing; a refused template overwrite
// keeps the old bytes and a refused first save creates no directory.
func TestBusySendUncoveredRefusalMatrix(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its
	// mark, and sets HOME and OPENROUTER_API_KEY with t.Setenv.
	all := func() api.SendKeysParams {
		return api.SendKeysParams{Key: "Escape", SendMode: ptr("steer_turn"), ConfirmedDelivery: ptr(true),
			CallerUUID: ptr(busyCaller), Force: ptr(true)}
	}
	for _, agent := range busyAgents {
		t.Run(agent, func(t *testing.T) {
			if agent == "opencode" {
				// Its launch validation, which precedes the decision, needs the host key.
				t.Setenv("OPENROUTER_API_KEY", "synthetic-api-key")
			}
			e := newKillEnv(t)
			e.rowAgent = agent
			c := e.busyClient(t)
			live := resumeRow{killRow: e.seedRow(t, killRowSpec{})}
			done := e.seedResumable(t, time.Hour, agentGone)
			args := busyAgentArgs(agent)
			type busyCall struct {
				name, options, called string // options "": pause's refusal
				row                   resumeRow
				run                   func(t *testing.T) error
			}
			send := func(c *api.Client, p api.SendKeysParams) func(t *testing.T) error {
				return func(t *testing.T) error {
					var res api.SendKeysResult
					var err error
					if c != nil {
						res, err = c.SendKeys(p)
					} else {
						res, err = e.sendKeys(p)
					}
					if !reflect.DeepEqual(res, api.SendKeysResult{}) {
						t.Errorf("refused send-keys returned %+v; want no result", res)
					}
					return err
				}
			}
			resume := func(p api.ResumeParams) func(t *testing.T) error {
				p.InstanceID = done.ID
				return func(*testing.T) error { _, err := c.Resume(p); return err }
			}
			exported := all()
			exported.InstanceID = done.ID
			calls := []busyCall{
				{"send-keys send_mode", "send_mode", "ad.send_keys.called", live,
					send(c, api.SendKeysParams{InstanceID: live.ID, Text: "hi", SendMode: ptr("wait_turn")})},
				{"send-keys confirmed_delivery", "confirmed_delivery", "ad.send_keys.called", live,
					send(c, api.SendKeysParams{InstanceID: live.ID, Text: "hi", ConfirmedDelivery: ptr(false)})},
				{"send-keys caller_uuid", "caller_uuid", "ad.send_keys.called", live,
					send(c, api.SendKeysParams{InstanceID: live.ID, Text: "hi", CallerUUID: ptr(strings.ToUpper(busyCaller))})},
				{"send-keys force", "force", "ad.send_keys.called", live,
					send(c, api.SendKeysParams{InstanceID: live.ID, Text: "hi", Force: ptr(false)})},
				{"exported send-keys raw key on a finished row", "send_mode, confirmed_delivery, caller_uuid, force", "", done,
					send(nil, exported)},
				{"pause only_if_waiting", "", "", live, func(*testing.T) error {
					_, err := c.Pause(context.Background(), api.PauseParams{InstanceID: live.ID, OnlyIfWaiting: true})
					return err
				}},
				{"exported pause only_if_waiting on a finished row", "", "", done, func(*testing.T) error {
					_, err := e.pause(api.PauseParams{InstanceID: done.ID, OnlyIfWaiting: true})
					return err
				}},
				{"resume send_mode", "send_mode", "", done, resume(api.ResumeParams{SendMode: ptr("wait_turn")})},
				{"resume confirmed_delivery", "confirmed_delivery", "", done, resume(api.ResumeParams{ConfirmedDelivery: ptr(false)})},
				{"resume confirm_from_transcripts", "confirm_from_transcripts", "", done,
					resume(api.ResumeParams{ConfirmFromTranscripts: ptr(false)})},
				{"exported resume", "send_mode, confirmed_delivery, confirm_from_transcripts", "", done, func(*testing.T) error {
					cfg := config.Default()
					cfg.Tmux = e.cfg
					_, err := api.Resume(e.st, e.rec, e.pc, cfg, e.storeID, e.clock.Now, nil, api.ResumeParams{InstanceID: done.ID,
						SendMode: ptr("steer_turn"), ConfirmedDelivery: ptr(true), ConfirmFromTranscripts: ptr(true)})
					return err
				}},
				{"reuse", "confirmed_delivery, confirm_from_transcripts", "", done, func(*testing.T) error {
					_, err := c.Spawn(api.SpawnParams{Agent: agent, InstanceID: done.ID, ReuseFinished: true, CWD: done.CWD,
						AgentArgs: args, ConfirmedDelivery: ptr(false), ConfirmFromTranscripts: ptr(false)})
					return err
				}},
				{"fresh spawn", "send_mode", "", live, func(t *testing.T) error {
					_, err := c.Spawn(api.SpawnParams{Agent: agent, CWD: t.TempDir(), AgentArgs: args, SendMode: ptr("wait_turn")})
					return err
				}},
			}
			for _, bc := range calls {
				t.Run(bc.name, func(t *testing.T) {
					before := e.snapshotBusy(t, bc.name, bc.row)
					err := bc.run(t)
					if bc.options == "" {
						assertNotPausableUncovered(t, err)
					} else {
						assertCantSend(t, err, bc.options)
					}
					e.assertBusyRefused(t, before, bc.called)
				})
			}
			t.Run("make-template", func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				p := api.MakeTemplateParams{Name: "busy", Agent: agent, AgentArgs: args, ConfirmedDelivery: ptr(false)}
				_, err := c.MakeTemplate(p)
				assertCantSend(t, err, "confirmed_delivery")
				if _, err := os.Stat(config.TemplatesDir()); !os.IsNotExist(err) {
					t.Errorf("templates directory after a refused first save: %v; want none", err)
				}
				body := apitest.TemplateBody(apitest.TemplateFields{Agent: agent, AgentArgs: args})
				path, err := apitest.SeedTemplate(config.TemplatesDir(), p.Name, body)
				if err != nil {
					t.Fatal(err)
				}
				p.Overwrite, p.SendMode, p.ConfirmedDelivery, p.ConfirmFromTranscripts = true, ptr("wait_turn"), nil, ptr(false)
				_, err = c.MakeTemplate(p)
				assertCantSend(t, err, "send_mode, confirm_from_transcripts")
				if got, err := os.ReadFile(path); err != nil || string(got) != body {
					t.Errorf("template after a refused overwrite = %q, %v; want %q", got, err, body)
				}
			})
		})
	}
}

// TestBusySendOptionOrder (SR-9.2): malformed parameters, today's and the new
// options' shapes alike, come before the coverage decision, and an unknown id
// stays ErrSpawnNotFound; a valid option is refused agent_not_covered before
// today's state, relay, permission, capability and lifecycle branches (on
// Codex rows). On a covered Claude Launch, pause only_if_waiting, like a
// resume through a store that cannot read coverage, is an unnamed error that
// never falls back to today's behaviour (a covered force is E5's; its calls'
// tests are Task t2.xis.p8.y2's; a covered resume's accepted defaults,
// steer_turn included, are TestBusySendResumeDefaults'). Every refusal
// writes, sends and stores nothing.
func TestBusySendOptionOrder(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	e := newKillEnv(t)
	c := e.busyClient(t)
	plain, _ := e.client(t)
	unknown := "no-such-" + uuid.NewString()[:8]
	// Today's order is an uncovered agent's: these rows are Codex's.
	e.rowAgent = "codex"
	waiting := resumeRow{killRow: e.seedRow(t, killRowSpec{})}
	pending := resumeRow{killRow: e.seedRow(t, killRowSpec{State: store.StatePending})}
	working := resumeRow{killRow: e.seedRow(t, killRowSpec{State: store.StateWorking})}
	asking := resumeRow{killRow: e.seedRow(t, killRowSpec{State: store.StateCheckPermission, RelayOn: true})}
	request := e.seedRequest(t, asking.ID)
	done := e.seedResumable(t, time.Hour, agentGone)
	e.rowAgent = "" // Claude's, covered as shipped
	coveredLive := resumeRow{killRow: e.seedRow(t, killRowSpec{})}
	e.rowAgent = "droid"
	paneOnly := resumeRow{killRow: e.seedRow(t, killRowSpec{})}
	send := func(p api.SendKeysParams) func() error {
		return func() error { _, err := e.sendKeys(p); return err }
	}
	pause := func(id string) func() error {
		return func() error { _, err := e.pause(api.PauseParams{InstanceID: id, OnlyIfWaiting: true}); return err }
	}
	resume := func(s api.ResumeStore, p api.ResumeParams) func() error {
		return func() error {
			cfg := config.Default()
			cfg.Tmux = e.cfg
			_, err := api.Resume(s, e.rec, e.pc, cfg, e.storeID, e.clock.Now, nil, p)
			return err
		}
	}
	spawn := func(c *api.Client, p api.SpawnParams) func() error {
		p.CWD = t.TempDir()
		return func() error { _, err := c.Spawn(p); return err }
	}
	makeTemplate := func(p api.MakeTemplateParams) func() error {
		return func() error { _, err := c.MakeTemplate(p); return err }
	}
	text := func(id string) api.SendKeysParams { return api.SendKeysParams{InstanceID: id, Text: "hi"} }
	with := func(p api.SendKeysParams, edit func(*api.SendKeysParams)) api.SendKeysParams { edit(&p); return p }
	for _, tc := range []struct {
		name    string
		row     resumeRow // whose writes are checked
		run     func() error
		want    string // the error's one name; "" is unnamed
		options string // ErrCantSend's named options
	}{
		{"today's malformed shape first", waiting, send(api.SendKeysParams{InstanceID: waiting.ID, NoEnter: true, Force: ptr(true)}), "ErrInvalidFlags", ""},
		{"empty send_mode before the lookup", waiting, send(with(text(unknown), func(p *api.SendKeysParams) { p.SendMode = ptr("") })), "ErrInvalidFlags", ""},
		{"other send_mode before the lookup", waiting, send(with(text(unknown), func(p *api.SendKeysParams) { p.SendMode = ptr("now") })), "ErrInvalidFlags", ""},
		{"braced caller_uuid before the lookup", waiting, send(with(text(unknown), func(p *api.SendKeysParams) { p.CallerUUID = ptr("{" + busyCaller + "}") })), "ErrInvalidFlags", ""},
		{"unhyphenated caller_uuid before the lookup", waiting, send(with(text(unknown), func(p *api.SendKeysParams) { p.CallerUUID = ptr(strings.ReplaceAll(busyCaller, "-", "")) })), "ErrInvalidFlags", ""},
		{"unknown target", waiting, send(with(text(unknown), func(p *api.SendKeysParams) { p.Force = ptr(true) })), "ErrSpawnNotFound", ""},
		{"pending row", pending, send(with(text(pending.ID), func(p *api.SendKeysParams) { p.SendMode = ptr("wait_turn") })), "ErrCantSend", "send_mode"},
		{"pane answer to an open relay request", asking, send(api.SendKeysParams{InstanceID: asking.ID, RequestToken: request, As: "allow", Key: "1",
			ExpectPaneSHA256: strings.Repeat("a", 64), Force: ptr(false)}), "ErrCantSend", "force"},
		{"pane-only delivery_request_id", paneOnly, send(with(text(paneOnly.ID), func(p *api.SendKeysParams) {
			p.DeliveryRequestID, p.ConfirmedDelivery = "m-1", ptr(true)
		})), "ErrCantSend", "confirmed_delivery"},
		{"pause of a working row", working, pause(working.ID), "ErrSpawnNotPausable", ""},
		{"resume of a live row", waiting, resume(e.st, api.ResumeParams{InstanceID: waiting.ID, ConfirmFromTranscripts: ptr(false)}), "ErrCantSend", "confirm_from_transcripts"},
		{"resume malformed send_mode before the lookup", waiting, resume(e.st, api.ResumeParams{InstanceID: unknown, SendMode: ptr("")}), "ErrInvalidFlags", ""},
		{"resume of an unknown id", waiting, resume(e.st, api.ResumeParams{InstanceID: unknown, ConfirmedDelivery: ptr(false)}), "ErrSpawnNotFound", ""},
		{"resume through a store that cannot read coverage", done, resume(&hookedResumeStore{st: e.st}, api.ResumeParams{InstanceID: done.ID, SendMode: ptr("wait_turn")}), "", ""},
		{"spawn id with a control character", waiting, spawn(c, api.SpawnParams{Agent: "claude", InstanceID: "bad\x01id", SendMode: ptr("wait_turn")}), "ErrInvalidFlags", ""},
		{"spawn malformed send_mode", waiting, spawn(c, api.SpawnParams{Agent: "claude", SendMode: ptr("")}), "ErrInvalidFlags", ""},
		{"spawn of a disabled experimental agent", waiting, spawn(plain, api.SpawnParams{Agent: "droid", AgentArgs: busyAgentArgs("droid"), ConfirmedDelivery: ptr(false)}), "ErrExperimentalAgentDisabled", ""},
		{"make-template malformed send_mode", waiting, makeTemplate(api.MakeTemplateParams{Name: "order", SendMode: ptr("now")}), "ErrTemplateMalformed", ""},
		{"make-template unknown agent", waiting, makeTemplate(api.MakeTemplateParams{Name: "order", Agent: "nope", ConfirmedDelivery: ptr(false)}), "ErrTemplateMalformed", ""},
		{"covered pause", coveredLive, pause(coveredLive.ID), "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := e.snapshotBusy(t, tc.name, tc.row)
			err := tc.run()
			switch {
			case tc.want == "ErrCantSend":
				assertCantSend(t, err, tc.options)
			case tc.want == "ErrSpawnNotPausable":
				assertNotPausableUncovered(t, err)
			default:
				assertOneName(t, err, tc.want)
				if d := api.ErrDetails(err); d != nil {
					t.Errorf("err_details = %#v; want none", d)
				}
			}
			e.assertBusyRefused(t, before, "")
		})
	}
}

// TestBusySendTemplateDeferredDecision (SR-1.4, SR-9.2): an agent-less
// template stores the Agent Defaults and confirm_from_transcripts, false and
// wait_turn included, exactly as apitest.TemplateBody encodes them, and loads
// them back present. The spawn that uses it is decided for its resolved agent,
// as is a template naming an uncovered agent: refused before anything is
// written. A present empty send_mode fails the template's load first.
func TestBusySendTemplateDeferredDecision(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	t.Setenv("HOME", t.TempDir())
	e := newKillEnv(t)
	c := e.busyClient(t)
	mode, off, empty := "wait_turn", false, ""
	fields := apitest.TemplateFields{SendMode: &mode, ConfirmedDelivery: &off, ConfirmFromTranscripts: &off}
	if _, err := c.MakeTemplate(api.MakeTemplateParams{Name: "agentless", SendMode: &mode, ConfirmedDelivery: &off,
		ConfirmFromTranscripts: &off}); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(config.TemplatesDir(), "agentless.toml")); err != nil || string(raw) != apitest.TemplateBody(fields) {
		t.Errorf("saved template = %q, %v; want %q", raw, err, apitest.TemplateBody(fields))
	}
	loaded, err := config.LoadTemplate("agentless")
	if err != nil || !reflect.DeepEqual(loaded, config.TemplateFile{SendMode: &mode, ConfirmedDelivery: &off, ConfirmFromTranscripts: &off}) {
		t.Errorf("LoadTemplate = %+v, %v; want the three settings present", loaded, err)
	}
	for name, f := range map[string]apitest.TemplateFields{
		"named":      {Agent: "cursor", ConfirmedDelivery: ptr(true)},
		"empty-mode": {SendMode: &empty},
	} {
		if _, err := apitest.SeedTemplate(config.TemplatesDir(), name, apitest.TemplateBody(f)); err != nil {
			t.Fatal(err)
		}
	}
	live := resumeRow{killRow: e.seedRow(t, killRowSpec{})}
	for _, tc := range []struct {
		name, want, options string
		p                   api.SpawnParams
	}{
		{"agent-less template with codex", "ErrCantSend", "send_mode, confirmed_delivery, confirm_from_transcripts",
			api.SpawnParams{Template: "agentless", Agent: "codex"}},
		{"agent-less template with droid", "ErrCantSend", "send_mode, confirmed_delivery, confirm_from_transcripts",
			api.SpawnParams{Template: "agentless", Agent: "droid", AgentArgs: busyAgentArgs("droid")}},
		{"template naming cursor", "ErrCantSend", "confirmed_delivery", api.SpawnParams{Template: "named"}},
		{"empty send_mode in the template", "ErrTemplateMalformed", "", api.SpawnParams{Template: "empty-mode", Agent: "claude"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.p.CWD = t.TempDir()
			before := e.snapshotBusy(t, tc.name, live)
			_, err := c.Spawn(tc.p)
			if tc.options != "" {
				assertCantSend(t, err, tc.options)
			} else {
				assertOneName(t, err, tc.want)
			}
			e.assertBusyRefused(t, before, "")
		})
	}
}
