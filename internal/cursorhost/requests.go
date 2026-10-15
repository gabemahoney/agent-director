package cursorhost

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

const maxActiveRequests = 32
const maxConnectionRequests = 4096
const requestReserve = 4 * time.Second

type policyRejection struct{ error }

type nativeChoice struct{ token, option, decision string }
type pendingAuthorization struct {
	auth                         *authorization
	token                        string
	settle, timeout, nativeUntil time.Time
	relay                        bool
	queued                       bool
	sent                         atomic.Bool
	retired                      atomic.Bool
	terminal                     atomic.Bool
	choice                       *nativeChoice
}
type requestController struct {
	rep       *reporter
	protocol  *protocol
	out       io.Writer
	requests  map[string]*pendingAuthorization
	seenIDs   map[string]bool
	seenTools map[string]bool
	menuMu    sync.Mutex
	menu      map[rune]nativeChoice
	nextKey   int
}

func newRequestController(rep *reporter, p *protocol, out io.Writer) *requestController {
	return &requestController{rep: rep, protocol: p, out: out, requests: map[string]*pendingAuthorization{}, seenIDs: map[string]bool{}, seenTools: map[string]bool{}, menu: map[rune]nativeChoice{}}
}
func (c *requestController) check(row store.Spawn) error {
	if e := VerifyRuntime(c.rep.binding, false); e != nil {
		return e
	}
	return VerifyPane(c.rep.store, row, c.rep.cfg)
}
func (c *requestController) key(r rune) (string, string, bool) {
	c.menuMu.Lock()
	defer c.menuMu.Unlock()
	v, ok := c.menu[r]
	return v.token, v.option, ok
}
func (c *requestController) removeKeys(token string) {
	c.menuMu.Lock()
	defer c.menuMu.Unlock()
	for k, v := range c.menu {
		if v.token == token {
			delete(c.menu, k)
		}
	}
}
func (c *requestController) addKey(r *pendingAuthorization, option, decision, name string) error {
	c.menuMu.Lock()
	defer c.menuMu.Unlock()
	// Keys are never reused on this connection: a delayed key cannot select a
	// different request. Beyond ASCII use Unicode letters accepted by send-keys.
	keys := []rune("123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")
	k := rune(0x4e00 + c.nextKey - len(keys))
	if c.nextKey < len(keys) {
		k = keys[c.nextKey]
	}
	c.nextKey++
	c.menu[k] = nativeChoice{r.token, option, decision}
	fmt.Fprintf(c.out, "  [%c] %s\r\n", k, safeText(name))
	return nil
}
func (c *requestController) admit(f frame, session string) error {
	// The lexical JSON id retains its JSON type (1 and "1" are independent).
	// Numeric spellings which denote the same value are normalized separately.
	id, e := requestID(f.ID)
	if e != nil {
		return e
	}
	if c.seenIDs[id] || len(c.seenIDs) >= maxConnectionRequests {
		return fmt.Errorf("duplicate or excessive Cursor request ids")
	}
	c.seenIDs[id] = true
	a, e := parseAuthorization(f, session)
	if e != nil {
		return policyRejection{e}
	}
	active := 0
	for _, r := range c.requests {
		if !r.sent.Load() && !r.terminal.Load() {
			active++
		}
	}
	if active >= maxActiveRequests || c.seenTools[a.toolID] {
		return fmt.Errorf("duplicate or excessive active Cursor tool ids")
	}
	token, e := randomID()
	if e != nil {
		return e
	}
	row, e := c.rep.store.GetSpawn(c.rep.binding.InstanceID)
	if e != nil {
		return e
	}
	now := time.Now()
	settle := time.UnixMilli(now.Add(time.Duration(c.rep.cfg.Relay.EffectiveTimeoutSeconds())*time.Second + requestReserve).UnixMilli())
	relay := row.RelayMode == "on"
	if !relay {
		settle = time.UnixMilli(now.Add(-time.Millisecond).UnixMilli())
	}
	nativeUntil := now.Add(30 * time.Minute)
	if relay {
		nativeUntil = settle.Add(30 * time.Minute)
	}
	r := &pendingAuthorization{auth: a, token: token, settle: settle, timeout: settle.Add(-requestReserve), nativeUntil: nativeUntil, relay: relay}
	e = c.rep.store.InsertCursorRequest(c.rep.binding, store.RelayRequest{RequestToken: token, ToolName: a.name, ToolInput: string(f.Params), ToolUseID: a.toolID, SettledAt: settle}, c.rep.cfg.Relay.PermissionRequestCap, now.Add(transportWriteWait), transportWriteWait, c.check)
	if e != nil {
		return e
	}
	c.requests[token] = r
	c.seenTools[a.toolID] = true
	fmt.Fprintf(c.out, "\r\nCursor request %s: %s\r\n", token, safeText(a.name))
	renderAuthorization(c.out, a)
	if relay {
		fmt.Fprint(c.out, "AD relay available; a deliberate pane choice competes with its decision\r\n")
	} else {
		fmt.Fprint(c.out, "Connected pane choices available now\r\n")
	}
	if a.plan {
		c.addKey(r, "accept", "allow", "Accept plan")
		c.addKey(r, "reject", "deny", "Reject plan")
	} else {
		for _, o := range a.options {
			d := "deny"
			if o.Kind == "allow_once" || o.Kind == "allow_always" {
				d = "allow"
			}
			c.addKey(r, o.ID, d, o.Name)
		}
	}
	return nil
}
func (c *requestController) choose(token, option string) {
	r := c.requests[token]
	if r == nil || r.terminal.Load() || r.queued || r.sent.Load() {
		fmt.Fprint(c.out, "Cursor choice expired; nothing sent\r\n")
		return
	}
	// An automated pane sender has already committed its intended request.
	// A mismatched key must never answer a different simultaneous request.
	rows, e := c.rep.store.PermissionRequestsForSpawn(c.rep.binding.InstanceID)
	if e != nil {
		fmt.Fprint(c.out, "Cursor choice evidence unavailable; nothing sent\r\n")
		return
	}
	for _, pr := range rows {
		other := c.requests[pr.RequestToken]
		if other != nil && pr.RequestToken != token && !other.queued && !other.sent.Load() &&
			(pr.PaneAnswer == store.PaneAnswerIntent || pr.PaneAnswer == store.PaneAnswerSent) {
			fmt.Fprint(c.out, "Cursor key does not match the pending pane request; nothing sent\r\n")
			return
		}
	}
	// Plan options carry distinct internal selectors; unlike permissions the
	// captured plan has no native option list.
	c.menuMu.Lock()
	for _, v := range c.menu {
		if v.token == token && v.option == option {
			v := v
			r.choice = &v
			break
		}
	}
	c.menuMu.Unlock()
}
func (c *requestController) cancel() {
	for _, r := range c.requests {
		r.terminal.Store(true)
		r.retired.Store(true)
		c.removeKeys(r.token)
	}
}
func (c *requestController) open() bool {
	for _, r := range c.requests {
		if !r.sent.Load() && !r.terminal.Load() {
			return true
		}
	}
	return false
}
func (c *requestController) enqueue(r *pendingAuthorization, decision, option string, relay bool) error {
	result, e := r.auth.result(decision, option)
	if e != nil {
		return e
	}
	r.queued = true
	c.removeKeys(r.token)
	until := r.nativeUntil
	if relay {
		until = r.settle
		if decision == "allow" {
			until = r.timeout
		}
	} else if r.relay {
		// A native choice made before relay settlement uses the same fixed
		// request decision deadline. A fresh deliberate fallback has its own
		// already-established native bound, never a replay of this frame.
		until = r.timeout
	}
	binding := c.rep.binding
	check := func(row store.Spawn) error {
		if e := VerifyRuntime(binding, false); e != nil {
			return e
		}
		return VerifyPane(c.rep.store, row, c.rep.cfg)
	}
	guard := func() error {
		if r.terminal.Load() || (relay && r.retired.Load()) || !time.Now().Before(until) {
			return fmt.Errorf("Cursor request response retired")
		}
		return c.rep.store.CheckCursorAnswer(binding, r.token, relay, until, check)
	}
	deadline := time.Now().Add(transportWriteWait)
	if until.Before(deadline) {
		deadline = until
	}
	return c.protocol.sendGuarded(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(r.auth.frame.ID), "result": result}, deadline, guard, func(e error) {
		if e == nil {
			r.sent.Store(true)
		} else {
			r.terminal.Store(true)
			fmt.Fprint(c.out, "Cursor response failed or retired; delivery is unconfirmed and will not be retried\r\n")
		}
	})
}
func (c *requestController) tick() error {
	for _, r := range c.requests {
		if r.terminal.Load() || r.sent.Load() {
			continue
		}
		now := time.Now()
		if !now.Before(r.nativeUntil) {
			r.terminal.Store(true)
			r.retired.Store(true)
			c.removeKeys(r.token)
			return fmt.Errorf("Cursor native request timed out")
		}
		// Drop a choice captured by the relay before switching to native
		// fallback. Only a new deliberate choice may use the native lifetime.
		if r.choice != nil && r.relay && !now.Before(r.timeout) {
			r.choice = nil
			fmt.Fprint(c.out, "Cursor relay approval deadline passed; stale pane choice refused\r\n")
		}
		if r.relay && !now.Before(r.settle) {
			r.retired.Store(true)
			if r.queued {
				r.terminal.Store(true)
				c.removeKeys(r.token)
				fmt.Fprint(c.out, "Cursor relay settled; delivery uncertain, answer will not be replayed\r\n")
				continue
			}
			r.relay = false
			fmt.Fprintf(c.out, "Cursor relay settled for %s; connected native choices remain available\r\n", r.token)
		}
		if r.queued {
			continue
		}
		if r.choice != nil {
			pr, e := c.rep.store.GetPermissionRequest(c.rep.binding.InstanceID, r.token)
			if e != nil {
				return e
			}
			if pr.PaneAnswer == store.PaneAnswerIntent {
				continue
			}
			if pr.PaneAnswer == store.PaneAnswerOutside {
				r.choice = nil
				fmt.Fprint(c.out, "Outside answer is a claim; no ACP response sent\r\n")
				continue
			}
			// A deliberate choice can win while relay is active only before an API
			// decision. The transaction arbitrates those competing writers.
			nativeDeadline := r.nativeUntil
			if r.relay {
				nativeDeadline = r.timeout
			}
			chosen, e := c.rep.store.ClaimCursorPaneAnswer(c.rep.binding, r.token, r.choice.decision, nativeDeadline, 0, c.check)
			if e != nil {
				continue
			}
			if chosen {
				if e = c.enqueue(r, r.choice.decision, r.choice.option, false); e != nil {
					return e
				}
				continue
			}
			r.choice = nil
		}
		if r.relay && !r.retired.Load() {
			pr, e := c.rep.store.GetPermissionRequest(c.rep.binding.InstanceID, r.token)
			if e != nil {
				return e
			}
			if pr.Closed() || pr.ProvenGone() || pr.PaneAnswered() {
				r.terminal.Store(true)
				c.removeKeys(r.token)
				continue
			}
			if pr.Decision == "" && now.Before(r.timeout) {
				continue
			}
			d, ok, e := c.rep.store.AckCursorDecision(c.rep.binding, r.token, !now.Before(r.timeout), r.settle, r.timeout, 0, c.check)
			if e != nil {
				continue
			}
			if ok {
				if e = c.enqueue(r, d, "", true); e != nil {
					return e
				}
			}
		}
	}
	return nil
}

// Proof is deliberately narrow: the captured shell rawOutput establishes an
// actual process exit, even nonzero. Status alone, MCP rejection, child task
// completion and accepted plans establish no tool execution.
func (c *requestController) execution(params json.RawMessage) error {
	var p struct {
		Session string `json:"sessionId"`
		Update  struct {
			Kind   string          `json:"sessionUpdate"`
			Tool   string          `json:"toolCallId"`
			Status string          `json:"status"`
			Output json.RawMessage `json:"rawOutput"`
		} `json:"update"`
	}
	if uniqueJSON(params) != nil || json.Unmarshal(params, &p) != nil || p.Session != c.rep.binding.SessionID || p.Update.Kind != "tool_call_update" || (p.Update.Status != "completed" && p.Update.Status != "failed") {
		return nil
	}
	for _, r := range c.requests {
		if r.auth.plan || r.auth.kind != "execute" || r.auth.toolID != p.Update.Tool {
			continue
		}
		var output map[string]json.RawMessage
		if json.Unmarshal(p.Update.Output, &output) != nil {
			return nil
		}
		var exit int
		var stdout, stderr string
		if json.Unmarshal(output["exitCode"], &exit) != nil || string(output["exitCode"]) == "null" || json.Unmarshal(output["stdout"], &stdout) != nil || bytes.Equal(bytes.TrimSpace(output["stdout"]), []byte("null")) || json.Unmarshal(output["stderr"], &stderr) != nil || bytes.Equal(bytes.TrimSpace(output["stderr"]), []byte("null")) {
			return nil
		}
		if e := c.rep.store.ProveCursorToolRan(c.rep.binding, r.token, r.auth.toolID, c.check); e != nil {
			return e
		}
		r.terminal.Store(true)
		r.retired.Store(true)
		c.removeKeys(r.token)
		return nil
	}
	return nil
}

// ACP ids are type-distinct. Numerically equal spellings cannot replace one
// another; outbound request IDs remain in protocol.pendingID independently.
func requestID(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "", fmt.Errorf("missing ACP request id")
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil || len(s) > 512 {
			return "", fmt.Errorf("invalid ACP id")
		}
		return "s:" + s, nil
	}
	if len(raw) > 64 {
		return "", fmt.Errorf("ACP numeric id exceeds bound")
	}
	n, e := strconv.ParseFloat(string(raw), 64)
	if e != nil || math.IsInf(n, 0) || math.IsNaN(n) || math.Abs(n) > 9007199254740991 || math.Trunc(n) != n {
		return "", fmt.Errorf("unsupported ACP numeric id")
	}
	if n == 0 {
		return "n:0", nil
	}
	return "n:" + strconv.FormatFloat(n, 'f', 0, 64), nil
}
