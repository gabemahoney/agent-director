package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Spawn labels every harness row carries, so its own rows are recognisable
// in the container store and trail.
const (
	labelRun  = "mx_run"
	labelCase = "mx_case"
)

// readyPollInterval paces the readiness poll (read-only get). It is not a
// measured poll, so it is coarser than pollInterval.
const readyPollInterval = 250 * time.Millisecond

// Natural exit: Claude Code exits on a second Ctrl-D at an empty prompt.
// agent-director's send-keys types text and cannot send control keys, so
// the harness sends them with its own tmux send-keys on the private socket,
// as one measured action of two keystrokes this far apart.
const (
	naturalExitKey = "C-d"
	naturalExitGap = 250 * time.Millisecond
)

// naturalExitTrigger describes the natural-exit action in the results.
var naturalExitTrigger = fmt.Sprintf("tmux send-keys %s twice, %s apart, at the idle prompt", naturalExitKey, naturalExitGap)

// Row states read from get (internal/store's state names).
const (
	stateWaiting = "waiting"
	stateWorking = "working"
	stateEnded   = "ended"
	stateMissing = "missing"
)

// errNotReady is a sample whose agent did not reach the wanted state in
// time ("did not reach its prompt"). It is a failed sample, never a time.
var errNotReady = errors.New("did not reach its prompt")

// spawnSpec is one agent's launch: its working directory (harness-owned),
// the claude arguments after spawn's "--" (an initial prompt, --mcp-config
// <path>), and extra environment for the agent (spawn --extra-env).
type spawnSpec struct {
	CWD        string
	ClaudeArgs []string
	ExtraEnv   map[string]string
	// RelayMode, when set, is spawn's --relay-mode. The RN-9 drive uses
	// "on" so a permission request is answered through agent-director's
	// own decide verb.
	RelayMode string
}

// agentRef is one spawned agent: its row, the private socket its row
// recorded, its tmux session and pane, and the agent process kill would wait
// on. Under the parent-process hook gate (SR-22.9) the SessionStart identity
// is the pane process, so the pane's pid and start time are that process.
type agentRef struct {
	InstanceID    string `json:"claude_instance_id"`
	CWD           string `json:"cwd"`
	Socket        string `json:"tmux_socket"`
	SessionName   string `json:"tmux_session_name"`
	TmuxSessionID string `json:"tmux_session_id"`
	PaneID        string `json:"pane_id"`
	PID           int    `json:"pid"`
	StartTime     string `json:"start_time"`
	StoreID       string `json:"store_id"`
	PreTrust      string `json:"pre_trust"`
}

// rowView is the part of get's output the harness reads.
type rowView struct {
	ClaudeInstanceID string     `json:"claude_instance_id"`
	State            string     `json:"state"`
	TmuxSessionName  string     `json:"tmux_session_name"`
	TmuxSocket       string     `json:"tmux_socket"`
	ClaudeSessionID  string     `json:"claude_session_id"`
	EndedAt          *time.Time `json:"ended_at"`
	// PermissionRequests are the row's open requests; only their tokens
	// and tool names are read (never the tool input get also returns).
	PermissionRequests []struct {
		RequestToken string `json:"request_token"`
		ToolName     string `json:"tool_name"`
	} `json:"permission_requests"`
}

// spawnAgent launches one agent with spawn on the private server, records
// its instance id in the identifiers file, reads its row once with get and
// checks the row's socket is under the private TMUX_TMPDIR (the first row's
// socket is recorded in the isolation summary). A pre_trust other than "ok"
// is an error after the row exists, so the caller can count the sample as
// failed. A socket outside the private directory is a *refusal: the run must
// abort.
func (h *harness) spawnAgent(caseID string, spec spawnSpec) (agentRef, error) {
	argv := []string{"spawn", "--cwd", spec.CWD,
		"--label", labelRun + "=" + h.iso.RunID, "--label", labelCase + "=" + caseID}
	if spec.RelayMode != "" {
		argv = append(argv, "--relay-mode", spec.RelayMode)
	}
	names := make([]string, 0, len(spec.ExtraEnv))
	for k := range spec.ExtraEnv {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if envForbidden(k) {
			return agentRef{}, fmt.Errorf("spawn --extra-env %s refused: a forbidden variable", k)
		}
		argv = append(argv, "--extra-env", k+"="+spec.ExtraEnv[k])
	}
	if len(spec.ClaudeArgs) > 0 {
		argv = append(append(argv, "--"), spec.ClaudeArgs...)
	}
	res, err := h.inv.agentDirectorCall(caseID, actionSpawn, argv...)
	if err != nil {
		return agentRef{}, err
	}
	var out struct {
		ClaudeInstanceID string `json:"claude_instance_id"`
		PreTrust         string `json:"pre_trust"`
	}
	if err := json.Unmarshal(res.stdout, &out); err != nil || out.ClaudeInstanceID == "" {
		return agentRef{}, fmt.Errorf("spawn: unexpected output %q", h.inv.excerpt(res.stdout))
	}
	a := agentRef{InstanceID: out.ClaudeInstanceID, CWD: spec.CWD, PreTrust: out.PreTrust}
	if err := h.ids.add(idInstance, a.InstanceID); err != nil {
		return a, err
	}
	r, err := h.getRow(caseID, a.InstanceID)
	if err != nil {
		return a, err
	}
	a.Socket, a.SessionName = r.TmuxSocket, r.TmuxSessionName
	if err := checkSocketPrivate(h.iso.TmuxTmpdir, a.Socket); err != nil {
		return a, err
	}
	if h.iso.RecordedSocket == "" {
		// h.res.Isolation is a copy taken in start, before any spawn; set
		// both so results.json records the socket too.
		h.iso.RecordedSocket = a.Socket
		if h.res != nil {
			h.res.Isolation.RecordedSocket = a.Socket
		}
		if err := h.ids.add(idSocket, a.Socket); err != nil {
			return a, err
		}
	}
	if a.PreTrust != "ok" {
		return a, fmt.Errorf("spawn: pre_trust is %q, want ok (the agent may stop at the folder-trust prompt)", a.PreTrust)
	}
	return a, nil
}

// getRow reads one row with the read-only get verb.
func (h *harness) getRow(caseID, instanceID string) (rowView, error) {
	res, err := h.inv.agentDirectorCall(caseID, actionRead, "get", "--claude-instance-id", instanceID)
	if err != nil {
		return rowView{}, err
	}
	var r rowView
	if err := json.Unmarshal(res.stdout, &r); err != nil {
		return rowView{}, fmt.Errorf("get: unexpected output %q", h.inv.excerpt(res.stdout))
	}
	return r, nil
}

// waitState polls the row with get until its state is one of want, and
// returns that reading. It gives up with errNotReady (wrapped, with the last
// state and any ad.hook.ignored reasons the container trail records for the
// row, such as no_exec_form) after the ready timeout, or at once when the
// row ends first.
func (h *harness) waitState(caseID, instanceID string, want ...string) (rowView, error) {
	deadline := h.clock.Now().Add(h.cfg.readyTimeout)
	var (
		last    rowView
		lastErr error
	)
	for {
		r, err := h.getRow(caseID, instanceID)
		lastErr = err
		if err == nil {
			last = r
			for _, s := range want {
				if r.State == s {
					return r, nil
				}
			}
			if r.State == stateEnded || r.State == stateMissing {
				return r, h.notReady(instanceID, "the row became "+r.State+" first")
			}
		}
		if !h.clock.Now().Before(deadline) {
			detail := fmt.Sprintf("state %q after %s", last.State, h.cfg.readyTimeout)
			if lastErr != nil {
				detail += "; last get failed: " + lastErr.Error()
			}
			return last, h.notReady(instanceID, detail)
		}
		h.clock.Sleep(readyPollInterval)
	}
}

// notReady builds the errNotReady failure, naming the trail's reasons.
func (h *harness) notReady(instanceID, detail string) error {
	reasons, err := ignoredHookReasons(h.trailPath(), instanceID)
	switch {
	case err != nil:
		detail += "; container trail unreadable: " + err.Error()
	case len(reasons) > 0:
		detail += "; ignored hooks: " + strings.Join(reasons, ", ")
	}
	return fmt.Errorf("%w: %s", errNotReady, detail)
}

// trailPath is the container's own trail (the run's HOME, never a host's).
func (h *harness) trailPath() string {
	return filepath.Join(h.iso.Home, ".agent-director", "ad-trail.jsonl")
}

// ignoredHookReasons reads the run's own trail read-only and returns the
// distinct ad.hook.ignored reasons recorded for instanceID, in first-seen
// order. A missing trail has none.
func ignoredHookReasons(path, instanceID string) ([]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var reasons []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var ev struct {
			Event  string `json:"event"`
			ID     string `json:"claude_instance_id"`
			Reason string `json:"reason"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Event != "ad.hook.ignored" || ev.ID != instanceID {
			continue
		}
		if !seen[ev.Reason] {
			seen[ev.Reason] = true
			reasons = append(reasons, ev.Reason)
		}
	}
	return reasons, sc.Err()
}

// identify fills the agent's tmux session, pane, pid and start time from
// read-only listings of the private server: the session whose @ad_owner
// label names the row, and that session's pane whose @ad_pane carries the
// label's launch token. The store id the label carries goes to the
// identifiers file.
func (h *harness) identify(caseID string, a *agentRef) error {
	res, err := h.inv.tmuxCall(caseID, actionRead, a.Socket, "list-sessions", "-F", "#{session_id}\t#{@ad_owner}")
	if err != nil {
		return err
	}
	token := ""
	for _, line := range strings.Split(string(res.stdout), "\n") {
		sid, owner, ok := strings.Cut(line, "\t")
		f := strings.Fields(owner)
		if !ok || len(f) < 4 || f[0] != "ad1" || f[3] != a.InstanceID || f[2] != sid {
			continue
		}
		a.TmuxSessionID, token = sid, f[1]
		if len(f) >= 5 {
			a.StoreID = f[4]
		}
	}
	if a.TmuxSessionID == "" {
		return fmt.Errorf("no session on %s is labelled for %s", a.Socket, a.InstanceID)
	}
	if a.StoreID != "" {
		if err := h.ids.add(idStore, a.StoreID); err != nil {
			return err
		}
	}
	res, err = h.inv.tmuxCall(caseID, actionRead, a.Socket, "list-panes", "-s", "-t", a.TmuxSessionID,
		"-F", "#{pane_id}\t#{pane_pid}\t#{@ad_pane}")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(res.stdout), "\n") {
		f := strings.SplitN(line, "\t", 3)
		if len(f) == 3 && f[2] == token+" "+f[0] {
			a.PaneID = f[0]
			a.PID, err = strconv.Atoi(f[1])
			if err != nil {
				return fmt.Errorf("pane %s: pid %q: %w", f[0], f[1], err)
			}
		}
	}
	if a.PaneID == "" {
		return fmt.Errorf("no pane of session %s carries the row's launch token", a.TmuxSessionID)
	}
	start, alive, known := h.procs.StartTime(a.PID)
	if !known || !alive {
		return fmt.Errorf("agent process %d: start time unreadable or gone before the action", a.PID)
	}
	a.StartTime = start
	return nil
}

// killPane is RN-6's measured action: the harness's own kill-pane of the
// agent's pane on the private socket (the SRD method; never agent-director's
// kill). It returns the instant taken immediately before the call, from
// which the exit time runs.
func (h *harness) killPane(caseID string, a agentRef) (time.Time, error) {
	t0 := h.clock.Now()
	_, err := h.inv.tmuxCall(caseID, actionMeasured, a.Socket, "kill-pane", "-t", a.PaneID)
	return t0, err
}

// pause runs agent-director pause on the row (pause's /exit). kind is
// actionMeasured for RN-2's pause case and actionDrive for an RN-9 drive.
func (h *harness) pause(caseID string, kind actionKind, a agentRef) error {
	_, err := h.inv.agentDirectorCall(caseID, kind, "pause", "--claude-instance-id", a.InstanceID)
	return err
}

// naturalExit sends the natural-exit keystrokes to the agent's pane on the
// private socket. Only the first keystroke's failure is an error: the agent
// may already be gone when the second is sent.
func (h *harness) naturalExit(caseID string, a agentRef) error {
	if _, err := h.inv.tmuxCall(caseID, actionMeasured, a.Socket, "send-keys", "-t", a.PaneID, naturalExitKey); err != nil {
		return err
	}
	h.clock.Sleep(naturalExitGap)
	_, _ = h.inv.tmuxCall(caseID, actionMeasured, a.Socket, "send-keys", "-t", a.PaneID, naturalExitKey)
	return nil
}

// noServerReply is how tmux answers a listing on a socket with no server
// (also when the socket file is gone). The listing poller reads it as every
// session gone; any other failure is a probe error.
const noServerReply = "no server running"

// sessionGone is RN-2's probe: the agent's tmux session no longer lists on
// the private server.
func (h *harness) sessionGone(caseID string, a agentRef) goneProbe {
	return func() (bool, error) {
		res, err := h.inv.tmuxCall(caseID, actionRead, a.Socket, "list-sessions", "-F", "#{session_id}")
		if err != nil {
			if strings.Contains(string(res.stderr), noServerReply) {
				return true, nil
			}
			return false, err
		}
		for _, line := range strings.Split(string(res.stdout), "\n") {
			if strings.TrimSpace(line) == a.TmuxSessionID {
				return false, nil
			}
		}
		return true, nil
	}
}
