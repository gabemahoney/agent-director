package cursorhost

import (
	"bytes"
	"encoding/json"
	"errors"
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
	storeID                      string
	binding                      store.CursorRuntime
	wait                         requestWait
	proofWait                    requestWait
	lastProof                    []byte
	responseDeadline             time.Time
	responseDone                 bool
	session                      string
	generation                   uint64
	auth                         *authorization
	token                        string
	settle, timeout, nativeUntil time.Time
	relay                        bool
	queued                       bool
	sent                         atomic.Bool
	retired                      atomic.Bool
	terminal                     atomic.Bool
	// turnEnded: retired at its turn's completion (turnEnd).
	turnEnded       bool
	turnEndSequence uint64
	choice          *nativeChoice
	proof           *deferredProof
}

// A validated completion whose proof write met a competing writer. Its
// authority stays the binding that observed the completion, so it must be
// written before that binding is retired by rotation or runtime termination.
type deferredProof struct {
	binding store.CursorRuntime
	ran     bool
	since   time.Time
	event   store.CursorProof
	flush   uint64
}

// proofRetry paces a proof flush's attempts while another writer holds the lock.
const proofRetry = 10 * time.Millisecond

type requestController struct {
	clock         requestClock
	events        chan requestWake
	closed        chan struct{}
	closeOnce     sync.Once
	nextFlush     uint64
	prerequisites uint64
	rep           *reporter
	protocol      *protocol
	out           io.Writer
	requests      map[string]*pendingAuthorization
	seenIDs       map[string]bool
	idSessions    map[string]string
	seenTools     map[string]bool
	menuMu        sync.Mutex
	menu          map[rune]nativeChoice
	nextKey       int
	generation    atomic.Uint64
	// turnOpen reports whether a prompt turn is open and no rotation is under
	// way (the session loop's busy && !rotating). Read it only on the
	// controller goroutine; nil reads false.
	turnOpen func() bool
}

func newRequestController(rep *reporter, p *protocol, out io.Writer) *requestController {
	return &requestController{clock: realRequestClock{}, events: make(chan requestWake, 3*maxActiveRequests), closed: make(chan struct{}), rep: rep, protocol: p, out: out, requests: map[string]*pendingAuthorization{}, seenIDs: map[string]bool{}, idSessions: map[string]string{}, seenTools: map[string]bool{}, menu: map[rune]nativeChoice{}}
}
func (c *requestController) check(row store.Spawn) error {
	return c.checkFor(c.rep.binding)(row)
}

func (c *requestController) checkFor(binding store.CursorRuntime) store.CursorCheck {
	// A bounded store call can return while native work still owns its lease.
	// Capture this launch's binding before dispatch; later rotation must not
	// change the evidence examined by that operation.
	return func(row store.Spawn) error {
		if e := VerifyRuntime(binding, false); e != nil {
			return e
		}
		return VerifyPane(c.rep.store, row, c.rep.cfg)
	}
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
	c.idSessions[id] = session
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
	now := c.clock.Now()
	settle := time.UnixMilli(now.Add(time.Duration(c.rep.cfg.Relay.EffectiveTimeoutSeconds())*time.Second + requestReserve).UnixMilli())
	relay := row.RelayMode == "on"
	if !relay {
		settle = time.UnixMilli(now.Add(-time.Millisecond).UnixMilli())
	}
	nativeUntil := now.Add(30 * time.Minute)
	if relay {
		nativeUntil = settle.Add(30 * time.Minute)
	}
	r := &pendingAuthorization{storeID: c.rep.store.StoreID(), binding: c.rep.binding, session: session, generation: c.generation.Load(), auth: a, token: token, settle: settle, timeout: settle.Add(-requestReserve), nativeUntil: nativeUntil, relay: relay}
	e = c.rep.store.InsertCursorRequest(c.rep.binding, store.RelayRequest{RequestToken: token, ToolName: a.name, ToolInput: string(f.Params), ToolUseID: a.toolID, SettledAt: settle}, c.rep.cfg.Relay.PermissionRequestCap, now.Add(transportWriteWait), transportWriteWait, c.checkFor(c.rep.binding))
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
	c.scheduleRequest(r, c.clock.Now())
	return nil
}

// The plan extension does not supply a session id. Retain its independently
// observed request/tool identity so delayed callbacks cannot acquire authority
// over a later conversation on this connection.
func (c *requestController) retiredAuthorization(f frame, current, retiring string) string {
	if len(f.ID) == 0 {
		return ""
	}
	id, e := requestID(f.ID)
	if e == nil {
		if sid := c.idSessions[id]; sid != "" && (sid != current || sid == retiring) {
			return sid
		}
	}
	a, e := parseAuthorization(f, current)
	if e != nil {
		return ""
	}
	for _, r := range c.requests {
		if r.auth.toolID == a.toolID && (r.session != current || r.session == retiring) {
			return r.session
		}
	}
	return ""
}
func (c *requestController) choose(token, option string) {
	r := c.requests[token]
	if r == nil || r.generation != c.generation.Load() || r.terminal.Load() || r.queued || r.sent.Load() {
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
	if r.choice != nil {
		c.scheduleRequest(r, c.clock.Now())
	}
}
func (c *requestController) cancel() {
	c.generation.Add(1)
	for _, r := range c.requests {
		r.wait.stop()
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
	deadline := c.clock.Now().Add(transportWriteWait)
	if until.Before(deadline) {
		deadline = until
	}
	r.responseDeadline = deadline
	binding := r.binding
	current := func() error {
		if r.generation != c.generation.Load() || r.terminal.Load() || (relay && r.retired.Load()) || !c.clock.Now().Before(deadline) {
			return fmt.Errorf("Cursor request response retired")
		}
		return nil
	}
	check := func(row store.Spawn) error {
		if e := VerifyRuntime(binding, false); e != nil {
			return e
		}
		if e := VerifyPane(c.rep.store, row, c.rep.cfg); e != nil {
			return e
		}
		return current()
	}
	guard := func() error {
		if e := current(); e != nil {
			return e
		}
		if e := c.rep.store.CheckCursorAnswer(binding, r.token, relay, deadline, check); e != nil {
			return e
		}
		return current()
	}
	issue := func() error {
		if e := current(); e != nil {
			return e
		}
		if e := c.rep.store.IssueCursorAnswer(binding, r.token, relay, deadline, check); e != nil {
			return e
		}
		return current()
	}
	return c.protocol.sendGuarded(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(r.auth.frame.ID), "result": result}, deadline, guard, func(e error) {
		if e == nil {
			r.sent.Store(true)
		} else {
			r.terminal.Store(true)
			fmt.Fprint(c.out, "Cursor response failed or retired; delivery is unconfirmed and will not be retried\r\n")
		}
		c.deliver(requestWake{storeID: r.storeID, token: r.token, binding: binding, generation: r.generation, kind: responseCompleted, deadline: deadline, err: e})
	}, issue)
}
func (c *requestController) advanceRequest(r *pendingAuthorization) error {
	if r.generation != c.generation.Load() {
		r.terminal.Store(true)
		r.retired.Store(true)
		c.removeKeys(r.token)
	}
	if r.terminal.Load() || r.sent.Load() {
		return nil
	}
	now := c.clock.Now()
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
			return nil
		}
		r.relay = false
		fmt.Fprintf(c.out, "Cursor relay settled for %s; connected native choices remain available\r\n", r.token)
	}
	if r.queued {
		return nil
	}
	if r.choice != nil {
		pr, e := c.rep.store.GetPermissionRequest(r.binding.InstanceID, r.token)
		if e != nil {
			return e
		}
		if pr.PaneAnswer == store.PaneAnswerIntent {
			return nil
		}
		if pr.PaneAnswer == store.PaneAnswerOutside {
			r.choice = nil
			fmt.Fprint(c.out, "Outside answer is a claim; no ACP response sent\r\n")
			return nil
		}
		// A deliberate choice can win while relay is active only before an API
		// decision. The transaction arbitrates those competing writers.
		nativeDeadline := r.nativeUntil
		if r.relay {
			nativeDeadline = r.timeout
		}
		chosen, e := c.rep.store.ClaimCursorPaneAnswer(r.binding, r.token, r.choice.decision, nativeDeadline, 0, c.checkFor(r.binding))
		if e != nil {
			return nil
		}
		if chosen {
			if e = c.enqueue(r, r.choice.decision, r.choice.option, false); e != nil {
				return e
			}
			return nil
		}
		r.choice = nil
	}
	if r.relay && !r.retired.Load() {
		pr, e := c.rep.store.GetPermissionRequest(r.binding.InstanceID, r.token)
		if e != nil {
			return e
		}
		if pr.Closed() || pr.ProvenGone() || pr.PaneAnswered() {
			r.terminal.Store(true)
			c.removeKeys(r.token)
			return nil
		}
		if pr.Decision == "" && now.Before(r.timeout) {
			return nil
		}
		d, ok, e := c.rep.store.AckCursorDecision(r.binding, r.token, !now.Before(r.timeout), r.settle, r.timeout, 0, c.checkFor(r.binding))
		if e != nil {
			return nil
		}
		if ok {
			if e = c.enqueue(r, d, "", true); e != nil {
				return e
			}
		}
	}
	return nil
}

// Proof requires the exact admitted tool's captured execution output. Shell
// exits (even nonzero) and successful MCP results establish execution. A captured
// failed MCP completion proves only that invocation's dialog gone, not that the
// server action ran. Status alone, child completion and accepted plans do not.
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
		if r.session != p.Session || r.binding != c.rep.binding || r.auth.plan || r.auth.toolID != p.Update.Tool {
			continue
		}
		ran := r.auth.executionOutput(p.Update.Status, p.Update.Output)
		if !ran && !r.auth.completedMCPError(p.Update.Status, p.Update.Output) {
			return nil
		}
		if r.proof != nil {
			if bytes.Equal(r.proof.event.Raw, params) {
				return nil
			}
			// A second, different completion cannot replace an unresolved
			// captured event or borrow its persistence attempts.
			return fmt.Errorf("Cursor conflicting completion while exact proof is unresolved")
		}
		if r.proof == nil {
			if bytes.Equal(r.lastProof, params) {
				return nil
			}
			id, e := randomID()
			if e != nil {
				return e
			}
			now := c.clock.Now()
			r.proof = &deferredProof{binding: r.binding, ran: ran, since: now, event: store.CursorProof{
				Owner: store.CursorOwner{StoreID: c.rep.store.StoreID(), Runtime: r.binding}, ID: id, Token: r.token, Tool: r.auth.toolID,
				Generation: c.generation.Load(), Kind: "tool_call_update", CapturedAt: now,
				BusyUntil: now.Add(time.Duration(c.rep.cfg.Store.EffectiveBusyTimeoutMs()) * time.Millisecond),
				Ran:       ran, TurnOpen: c.isTurnOpen(), Raw: append([]byte(nil), params...),
			}}
		}
		if e := c.prove(r); e != nil {
			return e
		}
		r.wait.stop()
		r.terminal.Store(true)
		r.retired.Store(true)
		c.removeKeys(r.token)
		c.prerequisites++
		return nil
	}
	return nil
}

// prove attempts a deferred proof once, without blocking the controller. A
// busy store leaves it deferred, with only its own named wake until the busy
// timeout has passed since its first attempt. An expired wake makes no new
// attempt. Expiry and any other failure are returned. The controller writes
// tool proofs only through prove and proofFlush, never writeProof directly.
func (c *requestController) prove(r *pendingAuthorization) error {
	p := r.proof
	if p == nil {
		return nil
	}
	e := c.writeProof(r.token, r.auth.toolID, *p, c.isTurnOpen())
	if errors.Is(e, store.ErrStoreBusy) && c.clock.Now().Before(c.proofDeadline(*p)) {
		c.scheduleProof(r, p)
		return nil
	}
	if e != nil {
		return e
	}
	if r.proof == p {
		r.lastProof = append([]byte(nil), p.event.Raw...)
		r.proof = nil
		r.proofWait.stop()
	}
	return nil
}

// writeProof's turnOpen lets the proof move its row to working; a proof
// written while no turn is open moves nothing (proveCursorTool).
func (c *requestController) writeProof(token, tool string, p deferredProof, turnOpen bool) error {
	if err := p.checkSnapshot(token, tool); err != nil {
		return err
	}
	if !c.clock.Now().Before(p.event.BusyUntil) {
		return errProofDeadline
	}
	return c.rep.store.CommitCursorProof(p.event, p.event.TurnOpen && turnOpen, c.checkProof(p.binding, p.event.BusyUntil))
}

var errProofDeadline = fmt.Errorf("Cursor proof persistence deadline passed: %w", store.ErrStoreBusy)

// The deadline fences authority acquisition, including a late store lease or
// slow independent evidence. It does not reinterpret an already-issued COMMIT.
// Only an expired flush's single immediate attempt supplies a zero deadline.
func (c *requestController) checkProof(binding store.CursorRuntime, until time.Time) store.CursorCheck {
	check := c.checkFor(binding)
	return func(row store.Spawn) error {
		if !until.IsZero() && !c.clock.Now().Before(until) {
			return errProofDeadline
		}
		if err := check(row); err != nil {
			return err
		}
		if !until.IsZero() && !c.clock.Now().Before(until) {
			return errProofDeadline
		}
		return nil
	}
}

func (p deferredProof) checkSnapshot(token, tool string) error {
	if p.event.Token != token || p.event.Tool != tool || p.event.Owner.Runtime != p.binding || p.event.Ran != p.ran || !p.event.CapturedAt.Equal(p.since) {
		return fmt.Errorf("Cursor proof snapshot changed")
	}
	return nil
}

func (c *requestController) isTurnOpen() bool {
	return c.turnOpen != nil && c.turnOpen()
}

func (c *requestController) proofDeadline(p deferredProof) time.Time {
	return p.event.BusyUntil
}

func (a *authorization) executionOutput(status string, raw json.RawMessage) bool {
	var output map[string]json.RawMessage
	if json.Unmarshal(raw, &output) != nil {
		return false
	}
	switch a.kind {
	case "execute":
		var exit int
		var stdout, stderr string
		return json.Unmarshal(output["exitCode"], &exit) == nil && string(output["exitCode"]) != "null" && json.Unmarshal(output["stdout"], &stdout) == nil && !bytes.Equal(bytes.TrimSpace(output["stdout"]), []byte("null")) && json.Unmarshal(output["stderr"], &stderr) == nil && !bytes.Equal(bytes.TrimSpace(output["stderr"]), []byte("null"))
	case "other":
		// Admission already requires the captured MCP title and canonical
		// permission options. Only the captured success result establishes
		// execution; a completed error takes the separate proof-only path.
		var success bool
		return status == "completed" && len(output) == 1 && json.Unmarshal(output["success"], &success) == nil && success
	default:
		return false
	}
}

func (a *authorization) completedMCPError(status string, raw json.RawMessage) bool {
	if a.kind != "other" || status != "completed" {
		return false
	}
	output, err := object(raw)
	if err != nil || len(output) != 1 {
		return false
	}
	// This exact native completion was captured after MCP disconnection. It
	// ends the invocation without establishing server execution or an allow.
	var message string
	return json.Unmarshal(output["error"], &message) == nil && message == "Tool execution error"
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
