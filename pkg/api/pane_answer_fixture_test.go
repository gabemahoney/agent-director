package api_test

// pane_answer_fixture_test.go is the b.146 step 2b fixture (paEnv): a killEnv
// row with the relay on whose agent pane shows a known text, request A
// recorded by a relay hook (relayEnvHook) that has exited, judged in relayNS
// on a settable clock, and a pane answer's sender (paSender) alive in the
// process fake; with the err_details and tmux-call assertions the send-keys
// and record-pane-answer tests share. It holds no tests.

import (
	"encoding/json"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// paSender is the process a paEnv's pane answer records as its sender.
var paSender = api.ProcessIdentity{PID: 64001, Starttime: "4242", PIDNamespace: relayNS}

// paToolUseID is request A's tool_use_id.
const paToolUseID = "toolu_01PANE"

// paEnv is a pane-answer test's world: e's relay-on row r, its agent pane
// showing text, request A fallen back (its relay hook gone), the relay judged
// in relayNS at now, a pane answer recording self as its sender and counting an
// intent whose sender cannot be checked as in progress for hold, through sks
// (e.store unless replaced).
type paEnv struct {
	*killEnv
	r    killRow
	text string
	now  time.Time
	self api.ProcessIdentity
	hold time.Duration
	sks  api.SendKeysStore
}

// newPAEnv seeds a paEnv: request A recorded by relayEnvHook, which is gone.
func newPAEnv(t *testing.T) *paEnv {
	t.Helper()
	e := newKillEnv(t)
	p := &paEnv{killEnv: e, r: seedRelayRow(t, e), now: e.clock.Now(), self: paSender, sks: e.store}
	e.pc.Set(paSender.PID, procfix.Alive(paSender.Starttime))
	p.seedRequest(t, storefix.TestRequestTokenA, paToolUseID, relayEnvHook)
	p.setPane("pane of request A\n")
	return p
}

// seedRequest records token on p's row as a relay hook with identity hook
// does, with toolUseID and a settle instant an hour after p.now.
func (p *paEnv) seedRequest(t *testing.T, token, toolUseID string, hook api.ProcessIdentity) {
	t.Helper()
	storefix.SeedRelayRequest(t, p.st, p.r.ID, store.RelayRequest{RequestToken: token, ToolName: "Bash",
		ToolInput: `{"command":"ls"}`, ToolUseID: toolUseID, Hook: hook, SettledAt: p.now.Add(time.Hour)})
}

// setPane makes the agent's pane show text (no escapes: read-pane gives it as is).
func (p *paEnv) setPane(text string) {
	p.text = text
	p.rec.SetCapture(p.r.Socket, p.r.Spawn.Identity.PaneID, text)
}

// hash is the pane_sha256 of the pane p shows now.
func (p *paEnv) hash() string { return api.PaneSHA256(p.text) }

// view is p's relay view: e's process fake, relayNS, p.now and a one-hour window.
func (p *paEnv) view() api.RelayView {
	return api.RelayView{Procs: p.pc, PIDNamespace: func() (string, bool) { return relayNS, true },
		Now: func() time.Time { return p.now }, Window: time.Hour}
}

// sendKeys runs the exported api.SendKeys with p.sks and p's environment.
func (p *paEnv) sendKeys(params api.SendKeysParams) error {
	_, err := api.SendKeys(p.sks, p.rec, api.SendKeysEnv{Relay: p.view(),
		Self: func() api.ProcessIdentity { return p.self }, IntentHold: p.hold}, params)
	return err
}

// sendKeysRun is sendKeys with the tmux calls it made.
func (p *paEnv) sendKeysRun(params api.SendKeysParams) verbRun[struct{}] {
	return runVerb(p.killEnv, func() (struct{}, error) { return struct{}{}, p.sendKeys(params) })
}

// plain is a plain send-keys of text on p's row.
func (p *paEnv) plain(text string) api.SendKeysParams {
	return api.SendKeysParams{ClaudeInstanceID: p.r.ID, Text: text}
}

// answer is a pane answer to request token claiming as with key, over the
// pane p shows now.
func (p *paEnv) answer(token, as, key string) api.SendKeysParams {
	return api.SendKeysParams{ClaudeInstanceID: p.r.ID, RequestToken: token, As: as, Key: key, ExpectPaneSHA256: p.hash()}
}

// recordPaneAnswer runs the exported api.RecordPaneAnswer on p's store with
// p's relay view and hold.
func (p *paEnv) recordPaneAnswer(params api.RecordPaneAnswerParams) (api.RecordPaneAnswerResult, error) {
	return api.RecordPaneAnswer(p.st, p.rec, api.RecordPaneAnswerEnv{Relay: p.view(), IntentHold: p.hold}, params)
}

// outside is record-pane-answer of request token claiming as, over the pane p shows now.
func (p *paEnv) outside(token, as string) api.RecordPaneAnswerParams {
	return api.RecordPaneAnswerParams{RequestToken: token, As: as, ExpectPaneSHA256: p.hash()}
}

// request reads p's request token.
func (p *paEnv) request(t *testing.T, token string) store.PermissionRow {
	t.Helper()
	pr, err := p.st.GetPermissionRequest(p.r.ID, token)
	if err != nil {
		t.Fatalf("GetPermissionRequest(%s): %v", token, err)
	}
	return pr
}

// paDyingStore is a SendKeysStore whose pane answer's sender dies once its
// intent is committed (die: no sent write and no release ever come), or whose
// sent write fails with sentErr, writing nothing.
type paDyingStore struct {
	*killStore
	die     bool
	sentErr error
}

// RecordPaneSent writes nothing when the sender died or the write fails, else delegates.
func (d *paDyingStore) RecordPaneSent(id, token string, intent api.PaneIntent, maxWait time.Duration) (bool, error) {
	switch {
	case d.die:
		return false, nil
	case d.sentErr != nil:
		return false, d.sentErr
	}
	return d.killStore.RecordPaneSent(id, token, intent, maxWait)
}

// ReleasePaneIntent writes nothing when the sender died, else delegates.
func (d *paDyingStore) ReleasePaneIntent(id, token string, intent api.PaneIntent, maxWait time.Duration) (bool, error) {
	if d.die {
		return false, nil
	}
	return d.killStore.ReleasePaneIntent(id, token, intent, maxWait)
}

// paKeyCall is the one call a key sends: a named key by name (CallSendKey),
// a character typed literally with no Enter (CallSendText).
func paKeyCall(key string) tmux.Call {
	if len([]rune(key)) == 1 {
		return tmux.CallSendText
	}
	return tmux.CallSendKey
}

// assertOneKey fails unless calls are exactly before, then one send of key to
// paneID (assertKeyOnly).
func assertOneKey(t *testing.T, calls []tmuxfix.SocketCall, paneID, key string, before ...tmux.Call) {
	t.Helper()
	var kinds []tmux.Call
	for _, c := range calls {
		kinds = append(kinds, c.Call)
	}
	if want := append(slices.Clone(before), paKeyCall(key)); !slices.Equal(kinds, want) {
		t.Fatalf("tmux calls = %v; want %v", kinds, want)
	}
	c := calls[len(calls)-1]
	sent := c.Key
	if c.Call == tmux.CallSendText {
		sent = c.Text
	}
	if sent != key || c.Target != paneID || c.PressEnter {
		t.Errorf("key call = %+v; want %q alone to %s, no Enter", c, key, paneID)
	}
}

// The keys of ErrRelayFallenBack's err_details (b.146 rule 15, the ticket's
// rule 6): the request's facts as get-permission gives them, the Spawn's
// state and open_requests; and of each open_requests entry.
var (
	paFallenBackKeys = []string{"request_id", "request_token", "tool_name", "tool_input", "requested_at", "decision",
		"decision_reason", "delivery", "confirm_by", "hook_alive", "hook_gone_at", "attempted_decision", "attempted_at",
		"tool_use_id", "pane_answer", "pane_as", "state", "open_requests"}
	paOpenRequestKeys = []string{"delivery", "hook_alive", "pane_answer", "request_token", "requested_at", "tool_name"}
)

// assertFallenBackDetails fails unless err's err_details are
// ErrRelayFallenBack's, naming token, fallen back, on a Spawn in state, with
// exactly paFallenBackKeys (open_requests a list, each entry exactly
// paOpenRequestKeys); it returns them as JSON values.
func assertFallenBackDetails(t *testing.T, err error, token, state string) map[string]any {
	t.Helper()
	d, ok := api.ErrDetails(err).(api.RelayFallenBackDetails)
	if !ok {
		t.Fatalf("err_details of %v = %#v; want RelayFallenBackDetails", err, api.ErrDetails(err))
	}
	m := paJSON(t, d)
	if got := sortedKeys(m); !slices.Equal(got, sortedCopy(paFallenBackKeys)) {
		t.Errorf("err_details keys = %v; want %v", got, sortedCopy(paFallenBackKeys))
	}
	if m["request_token"] != token || m["delivery"] != api.DeliveryFallenBack || m["state"] != state {
		t.Errorf("err_details = %v; want request %s, fallen_back, state %s", m, token, state)
	}
	reqs, ok := m["open_requests"].([]any)
	if !ok {
		t.Fatalf("err_details open_requests = %#v; want a list", m["open_requests"])
	}
	for _, o := range reqs {
		if got := sortedKeys(o.(map[string]any)); !slices.Equal(got, paOpenRequestKeys) {
			t.Errorf("open_requests entry keys = %v; want %v", got, paOpenRequestKeys)
		}
	}
	return m
}

// paJSON is v marshalled and read back as a JSON object.
func paJSON(t *testing.T, v any) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(jsonOf(t, v)), &m); err != nil {
		t.Fatalf("unmarshal %T: %v", v, err)
	}
	return m
}

// sortedKeys is m's keys, sorted.
func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortedCopy is a sorted copy of s.
func sortedCopy(s []string) []string {
	out := slices.Clone(s)
	sort.Strings(out)
	return out
}
