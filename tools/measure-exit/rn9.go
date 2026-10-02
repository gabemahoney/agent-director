package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// RN-9 (SRD Open Questions; WD 2026-09-29c, WD 2026-09-30b Q2/Q3), with
// RN-7's record inside it. Four scenarios, each one agent (two for
// /resume) spawned through agent-director on the private server, with the
// recorder (recorder.go) registered in the generated layer of its working
// directory:
//
//	rn9.drive            the event drive; RN-7's per-event record
//	rn9.resume           an in-session /resume of an earlier conversation
//	rn9.team-inprocess   an agent team, teammateMode in-process
//	rn9.team-splitpane   an agent team, teammateMode tmux (split panes)
//
// The harness's only actions are the spawns, send-keys to its own rows,
// decide (the permission answer) and pause, all logged as drive actions;
// there is no kill. Claude's own split panes are Claude's actions and are
// recorded as a note. One agent per scenario: the 20-sample floor does not
// apply.

// Scenario ids (stable: decide keys on them).
const (
	rn9DriveID         = "rn9.drive"
	rn9ResumeID        = "rn9.resume"
	rn9TeamInProcessID = "rn9.team-inprocess"
	rn9TeamSplitPaneID = "rn9.team-splitpane"
)

// rn9ScenarioIDs lists every RN-9 scenario decide requires.
var rn9ScenarioIDs = []string{rn9DriveID, rn9ResumeID, rn9TeamInProcessID, rn9TeamSplitPaneID}

// The drive's prompts. Each asks for a short answer: the drive needs the
// events, not the text.
const (
	// drivePermissionPrompt needs one Bash call, which the default
	// permission mode asks about, so a PermissionRequest occurs.
	drivePermissionPrompt = "Use the Bash tool to run exactly this command: date. Run nothing else. Then reply with the single word: done."
	driveShortPrompt      = "Reply with the single word: ok."
	// teamPrompt starts an agent team of two teammates that each reply
	// once.
	teamPrompt = "Create an agent team with two teammates. Give each teammate this task: send the lead the single word done, then stop. When both have replied, clean up the team and reply with the single word finished."
)

// agentTeamsEnv enables agent teams for the agents of the team scenarios
// (through the generated layer's env).
const agentTeamsEnv = "CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS"

// rn9Poll paces the drive's reads of the recorder file and get.
const rn9Poll = 500 * time.Millisecond

// rowIDWait bounds the wait for the row to record a SessionStart's id once
// the recorder has seen that SessionStart.
const rowIDWait = 15 * time.Second

func init() {
	for _, s := range []struct {
		id, title string
		run       func(h *harness, spec caseSpec) (rn9Scenario, error)
	}{
		{rn9DriveID, "RN-9 event drive (with RN-7's record)", (*harness).runDrive},
		{rn9ResumeID, "RN-9 in-session /resume", (*harness).runResume},
		{rn9TeamInProcessID, "RN-9 agent team, in-process", func(h *harness, spec caseSpec) (rn9Scenario, error) {
			return h.runTeam(spec, teamInProcess)
		}},
		{rn9TeamSplitPaneID, "RN-9 agent team, split panes", func(h *harness, spec caseSpec) (rn9Scenario, error) {
			return h.runTeam(spec, teamSplitPane)
		}},
	} {
		s := s
		spec := caseSpec{id: s.id, family: familyRN9, title: s.title, modes: []mode{modeReal, modeDry}, generated: true}
		spec.run = func(h *harness) (caseResult, error) {
			sc, err := s.run(h, spec)
			if sc.ID == "" {
				sc.ID = spec.id
			}
			if sc.Hooks == nil {
				sc.Hooks = []hookObservation{}
			}
			if h.res.RN9 == nil {
				h.res.RN9 = &rn9Section{Scenarios: []rn9Scenario{}}
			}
			h.res.RN9.Scenarios = append(h.res.RN9.Scenarios, sc)
			return newCaseResult(spec), err
		}
		registerCase(spec)
	}
}

// rn9Agent is one driven agent and its recorder file.
type rn9Agent struct {
	h      *harness
	caseID string
	a      agentRef
	file   string
	// answered holds the permission request tokens already decided.
	answered map[string]bool
	notes    []string
}

// recorderFile is a scenario's raw per-hook file in the results
// directory (keys and ids only), kept with the results.
func (h *harness) recorderFile(caseID string) string {
	return filepath.Join(h.cfg.outDir, caseID+"-hooks.jsonl")
}

// startRN9Agent spawns one RN-9 agent in cwd with relay on (the harness
// answers permission requests with decide), waits for it to report in and
// identifies its pane process. claudeArgs go to claude after spawn's "--"
// (an agent team lead's --debug-file). A refusal is returned as is (it
// aborts the run); any other failure is a reason the scenario is
// inconclusive.
func (h *harness) startRN9Agent(caseID, cwd, file string, claudeArgs ...string) (*rn9Agent, error) {
	a, err := h.spawnAgent(caseID, spawnSpec{CWD: cwd, RelayMode: "on", ClaudeArgs: claudeArgs})
	if err != nil {
		return nil, err
	}
	if _, err := h.waitState(caseID, a.InstanceID, stateWaiting); err != nil {
		return nil, err
	}
	if err := h.identify(caseID, &a); err != nil {
		return nil, fmt.Errorf("identify: %w", err)
	}
	return &rn9Agent{h: h, caseID: caseID, a: a, file: file, answered: map[string]bool{}}, nil
}

// lines are the recorder's lines of this agent's row so far.
func (d *rn9Agent) lines() []recordLine {
	all, _ := readRecordLines(d.file)
	var out []recordLine
	for _, l := range all {
		if l.InstanceID == d.a.InstanceID {
			out = append(out, l)
		}
	}
	return out
}

// mark is the number of this agent's lines so far; waits look after it.
func (d *rn9Agent) mark() int { return len(d.lines()) }

// own reports whether a line is from the agent's own process and not a
// subagent's or teammate's.
func (d *rn9Agent) own(l recordLine) bool { return l.ParentPID == d.a.PID && l.AgentID == "" }

// send types text into the agent's prompt with agent-director send-keys.
func (d *rn9Agent) send(text string) error {
	_, err := d.h.inv.agentDirectorCall(d.caseID, actionDrive, "send-keys",
		"--claude-instance-id", d.a.InstanceID, "--text", text)
	return err
}

// rn9ExpectedTools are the tools whose permission requests each scenario's
// prompts call for; answerPermissions allows only these and denies every
// other request (a denial is safe: at worst the scenario is cut short and
// reads inconclusive). The drive asks for one Bash call (`date`); /resume
// asks for no tool; an agent team's lead starts its teammates with the
// Agent tool and coordinates through messages and the team's task list
// (Claude Code 2.1.178 and later; teammates' requests reach the lead).
var rn9ExpectedTools = map[string][]string{
	rn9DriveID:         {"Bash"},
	rn9ResumeID:        nil,
	rn9TeamInProcessID: rn9TeamTools,
	rn9TeamSplitPaneID: rn9TeamTools,
}

// rn9TeamTools are the agent-team coordination tools.
var rn9TeamTools = []string{"Agent", "SendMessage", "TaskCreate", "TaskUpdate", "TaskList", "TaskGet"}

// permissionDecision is the decision for a request of tool in caseID's
// scenario: allow for an expected tool, else deny.
func permissionDecision(caseID, tool string) string {
	for _, t := range rn9ExpectedTools[caseID] {
		if t == tool {
			return "allow"
		}
	}
	return "deny"
}

// answerPermissions decides every open permission request of the row
// (agent-director's own decide verb; relay mode on): allow when the
// scenario expects the requested tool, deny otherwise. Only the tool name
// is read, never the tool input.
func (d *rn9Agent) answerPermissions() error {
	r, err := d.h.getRow(d.caseID, d.a.InstanceID)
	if err != nil {
		return err
	}
	for _, p := range r.PermissionRequests {
		if p.RequestToken == "" || d.answered[p.RequestToken] {
			continue
		}
		d.answered[p.RequestToken] = true
		decision := permissionDecision(d.caseID, p.ToolName)
		if _, err := d.h.inv.agentDirectorCall(d.caseID, actionDrive, "decide", "--claude-instance-id", d.a.InstanceID,
			"--request-token", p.RequestToken, "--decision", decision); err != nil {
			return err
		}
		if decision == "allow" {
			d.notes = append(d.notes, "permission request answered: allow ("+p.ToolName+")")
		} else {
			d.notes = append(d.notes, fmt.Sprintf("permission request answered: deny (%q is not a tool this scenario expects)", p.ToolName))
		}
	}
	return nil
}

// waitFor waits up to bound for one of this agent's lines after mark to
// satisfy match, answering permission requests meanwhile. It returns that
// line.
func (d *rn9Agent) waitFor(mark int, bound time.Duration, what string, match func(recordLine) bool) (recordLine, error) {
	deadline := d.h.clock.Now().Add(bound)
	for {
		ls := d.lines()
		for i := mark; i < len(ls); i++ {
			if match(ls[i]) {
				return ls[i], nil
			}
		}
		if err := d.answerPermissions(); err != nil {
			return recordLine{}, err
		}
		if !d.h.clock.Now().Before(deadline) {
			return recordLine{}, fmt.Errorf("no %s within %s", what, bound)
		}
		d.h.clock.Sleep(rn9Poll)
	}
}

// turn sends text and waits for the agent's own Stop after it.
func (d *rn9Agent) turn(text string) error {
	m := d.mark()
	if err := d.send(text); err != nil {
		return err
	}
	_, err := d.waitFor(m, d.h.cfg.stepTimeout, "Stop after "+quoteShort(text), func(l recordLine) bool {
		return d.own(l) && l.Event == "Stop"
	})
	return err
}

// sessionStart sends a slash command and waits for the agent's own
// SessionStart with source, then for the row to record its session id. It
// returns the reading.
func (d *rn9Agent) sessionStart(command, source string) (sessionReading, error) {
	m := d.mark()
	if err := d.send(command); err != nil {
		return sessionReading{}, err
	}
	l, err := d.waitFor(m, d.h.cfg.stepTimeout, "SessionStart("+source+")", func(l recordLine) bool {
		return d.own(l) && l.Event == "SessionStart" && l.Source == source
	})
	if err != nil {
		return sessionReading{}, err
	}
	return d.reading(l)
}

// reading reads the row until it records the SessionStart line's session
// id (or rowIDWait passes) and returns what it records.
func (d *rn9Agent) reading(l recordLine) (sessionReading, error) {
	seq := 0
	for i, x := range d.lines() {
		if x.TimeNS == l.TimeNS && x.Event == l.Event && x.ParentPID == l.ParentPID {
			seq = i + 1
			break
		}
	}
	r := sessionReading{Seq: seq, Source: l.Source, PayloadID: l.TranscriptBase}
	deadline := d.h.clock.Now().Add(rowIDWait)
	for {
		row, err := d.h.getRow(d.caseID, d.a.InstanceID)
		if err != nil {
			return r, err
		}
		r.RowID = row.ClaudeSessionID
		if r.RowID == r.PayloadID || !d.h.clock.Now().Before(deadline) {
			return r, nil
		}
		d.h.clock.Sleep(rn9Poll)
	}
}

// startupReading is the reading after the agent's startup SessionStart.
func (d *rn9Agent) startupReading() (sessionReading, error) {
	l, err := d.waitFor(0, d.h.cfg.stepTimeout, "SessionStart(startup)", func(l recordLine) bool {
		return d.own(l) && l.Event == "SessionStart"
	})
	if err != nil {
		return sessionReading{}, err
	}
	return d.reading(l)
}

// end pauses the agent (a drive action) and gives its SessionEnd hooks
// time to be recorded.
func (d *rn9Agent) end() error {
	return d.h.pause(d.caseID, actionDrive, d.a)
}

// observe joins this scenario's recorder lines (all rows given) with the
// container trail.
func (h *harness) observe(file string, agents ...*rn9Agent) ([]recordLine, []hookObservation, error) {
	all, err := readRecordLines(file)
	if err != nil {
		return nil, nil, err
	}
	panes, rows := map[string]int{}, map[string]bool{}
	for _, d := range agents {
		if d != nil {
			panes[d.a.InstanceID], rows[d.a.InstanceID] = d.a.PID, true
		}
	}
	var lines []recordLine
	for _, l := range all {
		if rows[l.InstanceID] {
			lines = append(lines, l)
		}
	}
	trail, err := readTrailHooks(h.trailPath(), rows)
	if err != nil {
		return nil, nil, err
	}
	return lines, joinOutcomes(lines, panes, trail), nil
}

// inconclusive is a scenario that could not gather its evidence; a
// refusal is passed on so it aborts the run.
func inconclusive(id string, err error) (rn9Scenario, error) {
	var r *refusal
	if errors.As(err, &r) {
		return rn9Scenario{ID: id, Verdict: verdictInconclusive, Reason: err.Error()}, err
	}
	return rn9Scenario{ID: id, Verdict: verdictInconclusive, Reason: err.Error()}, nil
}

// quoteShort shortens a prompt for an error message.
func quoteShort(s string) string {
	if len(s) > 24 {
		s = s[:24] + "…"
	}
	return fmt.Sprintf("%q", s)
}

// runDrive is the event drive: a prompt with one permission request
// answered, its Stop, a Notification, /clear and a prompt, /compact and a
// prompt, then pause. Every hook is joined with agent-director's outcome;
// RN-7's record is taken from the same lines. A step that times out ends
// the drive early (pause still runs) and the scenario is evaluated on what
// was recorded, inconclusive when the drive was cut short.
func (h *harness) runDrive(spec caseSpec) (rn9Scenario, error) {
	file := h.recorderFile(spec.id)
	dir, err := h.prepareAgentDir(spec.id, 0, recorderLayer(h.self, file, nil))
	if err != nil {
		return rn9Scenario{ID: spec.id}, err
	}
	d, err := h.startRN9Agent(spec.id, dir, file)
	if err != nil {
		return inconclusive(spec.id, err)
	}
	var readings []sessionReading
	stepErr := func() error {
		r, err := d.startupReading()
		if err != nil {
			return err
		}
		readings = append(readings, r)
		if err := d.turn(drivePermissionPrompt); err != nil {
			return err
		}
		if _, err := d.waitFor(0, h.cfg.notificationWait, "Notification", func(l recordLine) bool {
			return d.own(l) && l.Event == "Notification"
		}); err != nil {
			d.notes = append(d.notes, "notification: "+err.Error())
		}
		for _, step := range []struct{ command, source string }{{"/clear", "clear"}, {"/compact", "compact"}} {
			r, err := d.sessionStart(step.command, step.source)
			if err != nil {
				return err
			}
			readings = append(readings, r)
			if err := d.turn(driveShortPrompt); err != nil {
				return err
			}
		}
		return nil
	}()
	endErr := d.end()
	lines, obs, err := h.observe(file, d)
	if err != nil {
		return rn9Scenario{ID: spec.id}, err
	}
	sc := evalDrive(spec.id, obs, readings)
	sc.Notes = append(d.notes, sc.Notes...)
	h.res.RN7 = &rn7Record{Rows: rn7Rows(lines, readings)}
	if stepErr != nil || endErr != nil {
		sc.Notes = append(sc.Notes, "drive cut short: "+errText(stepErr, endErr))
		if sc.Verdict == verdictPass {
			sc.Verdict, sc.Reason = verdictInconclusive, "the drive was cut short: "+errText(stepErr, endErr)
		}
	}
	return sc, nil
}

// errText joins the non-nil errors' texts.
func errText(errs ...error) string {
	var parts []string
	for _, e := range errs {
		if e != nil {
			parts = append(parts, e.Error())
		}
	}
	return strings.Join(parts, "; ")
}

// runResume makes an earlier conversation in a first agent (one prompt,
// then pause), then in a second agent in the same working directory sends
// `/resume <earlier session id>`; the second agent's SessionStart(resume)
// must be applied from its own process and its row must record the new id.
func (h *harness) runResume(spec caseSpec) (rn9Scenario, error) {
	file := h.recorderFile(spec.id)
	dir, err := h.prepareAgentDir(spec.id, 0, recorderLayer(h.self, file, nil))
	if err != nil {
		return rn9Scenario{ID: spec.id}, err
	}
	first, err := h.startRN9Agent(spec.id, dir, file)
	if err != nil {
		return inconclusive(spec.id, err)
	}
	earlier, err := func() (string, error) {
		if err := first.turn(driveShortPrompt); err != nil {
			return "", err
		}
		row, err := h.getRow(spec.id, first.a.InstanceID)
		return row.ClaudeSessionID, err
	}()
	if endErr := first.end(); err == nil && endErr != nil {
		err = endErr
	}
	if err != nil {
		return inconclusive(spec.id, fmt.Errorf("earlier conversation: %w", err))
	}
	if earlier == "" {
		return inconclusive(spec.id, errors.New("the earlier conversation's row records no session id"))
	}
	second, err := h.startRN9Agent(spec.id, dir, file)
	if err != nil {
		return inconclusive(spec.id, err)
	}
	startup, rowAfter := "", ""
	stepErr := func() error {
		r, err := second.startupReading()
		if err != nil {
			return err
		}
		startup = r.RowID
		r, err = second.sessionStart("/resume "+earlier, "resume")
		rowAfter = r.RowID
		return err
	}()
	endErr := second.end()
	_, obs, err := h.observe(file, second)
	if err != nil {
		return rn9Scenario{ID: spec.id}, err
	}
	sc := evalResume(spec.id, obs, startup, rowAfter)
	sc.Notes = append(append(first.notes, second.notes...), sc.Notes...)
	sc.Notes = append(sc.Notes, "earlier conversation "+earlier+" made by "+first.a.InstanceID)
	if stepErr != nil || endErr != nil {
		sc.Notes = append(sc.Notes, "drive cut short: "+errText(stepErr, endErr))
	}
	return sc, nil
}

// runTeam starts an agent team from one lead (agent teams enabled and
// teammateMode set through the generated layer; Claude's debug log on),
// waits for the lead's prompt box, sends the team prompt, waits for the
// lead's UserPromptSubmit and then until the team settles (the lead's Stop,
// then settleQuiet with no recorded hook, bounded by stepTimeout), records
// the panes Claude itself split, pauses the lead and evaluates every
// teammate hook. When the run is cut short, the panes' text is captured
// before pause (and again after a failed pause), and an inconclusive
// verdict's reason leads with the cause. Claude's debug logs are kept
// either way (capture.go).
func (h *harness) runTeam(spec caseSpec, teamMode string) (rn9Scenario, error) {
	file := h.recorderFile(spec.id)
	layer := recorderLayer(h.self, file, map[string]any{
		"env":          map[string]any{agentTeamsEnv: "1"},
		"teammateMode": teamMode,
	})
	dir, err := h.prepareAgentDir(spec.id, 0, layer)
	if err != nil {
		return rn9Scenario{ID: spec.id}, err
	}
	debugFile, err := h.leadDebugFile(spec.id)
	if err != nil {
		return rn9Scenario{ID: spec.id}, err
	}
	debugBefore := h.debugLogStamps()
	lead, err := h.startRN9Agent(spec.id, dir, file, "--debug-file", debugFile)
	if err != nil {
		sc, rerr := inconclusive(spec.id, err)
		sc.Artifacts, sc.Notes = h.keepDebugLogs(spec.id, debugFile, debugBefore)
		return sc, rerr
	}
	leadIDs := map[string]bool{}
	stepErr := func() error {
		r, err := lead.startupReading()
		if err != nil {
			return err
		}
		leadIDs[r.PayloadID], leadIDs[r.RowID] = true, true
		if err := lead.waitInputReady(); err != nil {
			return err
		}
		m := lead.mark()
		if err := lead.send(teamPrompt); err != nil {
			return err
		}
		if _, err := lead.waitFor(m, h.cfg.promptAcceptWait, "UserPromptSubmit", func(l recordLine) bool {
			return lead.own(l) && l.Event == "UserPromptSubmit"
		}); err != nil {
			return fmt.Errorf("the team prompt was not accepted: %w", err)
		}
		return lead.settle(m)
	}()
	panes := h.paneCount(spec.id, lead.a)
	var captured []capturedPane
	if stepErr != nil {
		c, notes := h.capturePanes(spec.id, lead.a, "")
		captured = append(captured, c...)
		lead.notes = append(lead.notes, notes...)
	}
	endErr := lead.end()
	if endErr != nil {
		c, notes := h.capturePanes(spec.id, lead.a, "-after-pause")
		captured = append(captured, c...)
		lead.notes = append(lead.notes, notes...)
	}
	debugFiles, debugNotes := h.keepDebugLogs(spec.id, debugFile, debugBefore)
	_, obs, err := h.observe(file, lead)
	if err != nil {
		return rn9Scenario{ID: spec.id}, err
	}
	sc := evalTeam(spec.id, teamMode, obs, leadIDs)
	sc.Notes = append(lead.notes, sc.Notes...)
	sc.Notes = append(sc.Notes, fmt.Sprintf("panes in the lead's tmux session before pause: %s (split panes are Claude's own)", panes))
	sc.Notes = append(sc.Notes, debugNotes...)
	for _, c := range captured {
		sc.Artifacts = append(sc.Artifacts, c.File)
	}
	sc.Artifacts = append(sc.Artifacts, debugFiles...)
	if stepErr != nil || endErr != nil {
		sc.Notes = append(sc.Notes, "team run cut short: "+errText(stepErr, endErr))
	}
	if stepErr != nil && sc.Verdict == verdictInconclusive {
		sc.Reason = "team run cut short: " + stepErr.Error() + "; " + sc.Reason
		if len(captured) > 0 {
			sc.Reason += "; pane text in " + captured[0].File
		}
	}
	return sc, nil
}

// settle waits for the lead's own Stop after mark and then for settleQuiet
// with no new line from the row, answering permissions meanwhile; the whole
// wait is bounded by stepTimeout.
func (d *rn9Agent) settle(mark int) error {
	deadline := d.h.clock.Now().Add(d.h.cfg.stepTimeout)
	stopped := false
	count, quietSince := -1, d.h.clock.Now()
	for {
		ls := d.lines()
		for i := mark; i < len(ls); i++ {
			if d.own(ls[i]) && ls[i].Event == "Stop" {
				stopped = true
			}
		}
		if len(ls) != count {
			count, quietSince = len(ls), d.h.clock.Now()
		}
		if stopped && d.h.clock.Now().Sub(quietSince) >= d.h.cfg.settleQuiet {
			return nil
		}
		if err := d.answerPermissions(); err != nil {
			return err
		}
		if !d.h.clock.Now().Before(deadline) {
			return fmt.Errorf("the team did not settle within %s (lead stopped: %t)", d.h.cfg.stepTimeout, stopped)
		}
		d.h.clock.Sleep(rn9Poll)
	}
}

// paneCount lists the lead's tmux session's panes (read-only) and returns
// their number as text, or why it could not.
func (h *harness) paneCount(caseID string, a agentRef) string {
	res, err := h.inv.tmuxCall(caseID, actionRead, a.Socket, "list-panes", "-s", "-t", a.TmuxSessionID, "-F", "#{pane_id}")
	if err != nil {
		return "unknown (" + err.Error() + ")"
	}
	return fmt.Sprint(len(strings.Fields(string(res.stdout))))
}
