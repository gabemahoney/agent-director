package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/gabemahoney/agent-director/internal/hook"
)

// RN-9's evaluation: pure functions over the recorder's lines, the
// container trail's hook records and the session ids read with get. The
// scenarios (rn9.go) gather those inputs; nothing here runs a process.

// Outcomes of a recorded hook in agent-director (hookObservation.Outcome).
const (
	hookApplied  = "applied"
	hookIgnored  = "ignored"
	hookNoChange = "no_change"
	hookNoRecord = "no_record"
)

// Verdicts of an RN-9 scenario (rn9Scenario.Verdict).
const (
	verdictPass         = "pass"
	verdictFail         = "fail"
	verdictInconclusive = "inconclusive"
	verdictNotRun       = "not_run"
)

// RN-9 STOP flags (lead decision 11): decide exits 3 on any.
const (
	stopResumeNotApplied   = "resume-not-applied"
	stopInProcessNoAgentID = "inprocess-teammate-lifecycle-without-agent-id"
	stopSplitPaneApplied   = "split-pane-teammate-hook-applied"
)

// Upsert outcomes of ad.hook.fired that mean the hook changed the row.
var appliedUpserts = map[string]bool{"updated": true, "inserted": true}

// trailHook is one agent-director hook record of the container trail:
// ad.hook.fired or ad.hook.ignored.
type trailHook struct {
	ignored   bool
	instance  string
	event     string
	sessionID string
	parentPID int
	reason    string
	upsert    string
	used      bool
}

// readTrailHooks reads the run's own trail read-only and returns the hook
// records of the given rows, in trail order. A missing trail has none.
func readTrailHooks(path string, instances map[string]bool) ([]trailHook, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []trailHook
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var ev struct {
			Event         string  `json:"event"`
			ID            *string `json:"claude_instance_id"`
			EventName     *string `json:"event_name"`
			HookEvent     *string `json:"hook_event"`
			SessionID     *string `json:"session_id"`
			HookSessionID *string `json:"hook_session_id"`
			ParentPID     int     `json:"parent_pid"`
			Reason        *string `json:"reason"`
			Upsert        *string `json:"upsert_outcome"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.ID == nil || !instances[*ev.ID] {
			continue
		}
		switch ev.Event {
		case "ad.hook.fired":
			out = append(out, trailHook{instance: *ev.ID, event: deref(ev.EventName),
				sessionID: deref(ev.SessionID), upsert: deref(ev.Upsert)})
		case "ad.hook.ignored":
			out = append(out, trailHook{ignored: true, instance: *ev.ID, event: deref(ev.HookEvent),
				sessionID: deref(ev.HookSessionID), parentPID: ev.ParentPID, reason: deref(ev.Reason)})
		}
	}
	return out, sc.Err()
}

// deref is *s, or "" for nil.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// joinOutcomes pairs each recorder line with agent-director's outcome for
// the same hook. The two are separate hook processes of one event, so they
// pair on the row, the event and the transcript basename, and an ignored
// record also on the parent pid. Each trail record pairs with at most one
// line: an ad.hook.ignored with its parent pid first (its own
// ad.hook.fired, no_change, is consumed with it), then an ad.hook.fired
// that changed the row (applied), then one that did not (no_change); a line
// with none is no_record. panePIDs maps each row to its recorded pane
// process.
func joinOutcomes(lines []recordLine, panePIDs map[string]int, trail []trailHook) []hookObservation {
	tr := append([]trailHook(nil), trail...)
	take := func(match func(t trailHook) bool) *trailHook {
		for i := range tr {
			if !tr[i].used && match(tr[i]) {
				tr[i].used = true
				return &tr[i]
			}
		}
		return nil
	}
	obs := make([]hookObservation, 0, len(lines))
	for i, l := range lines {
		o := hookObservation{Seq: i + 1, Event: l.Event, Source: l.Source, ParentPID: l.ParentPID,
			PanePID: panePIDs[l.InstanceID], SessionID: l.SessionID, TranscriptBase: l.TranscriptBase,
			AgentID: l.AgentID, AgentType: l.AgentType, Outcome: hookNoRecord}
		o.PIDMatch = o.PanePID > 0 && o.ParentPID == o.PanePID
		same := func(t trailHook) bool {
			return t.instance == l.InstanceID && t.event == l.Event && t.sessionID == l.TranscriptBase
		}
		if t := take(func(t trailHook) bool { return t.ignored && same(t) && t.parentPID == l.ParentPID }); t != nil {
			o.Outcome, o.Reason = hookIgnored, t.reason
			take(func(t trailHook) bool { return !t.ignored && same(t) && t.upsert == "no_change" })
		} else if take(func(t trailHook) bool { return !t.ignored && same(t) && appliedUpserts[t.upsert] }) != nil {
			o.Outcome = hookApplied
		} else if take(func(t trailHook) bool { return !t.ignored && same(t) }) != nil {
			o.Outcome = hookNoChange
		}
		obs = append(obs, o)
	}
	return obs
}

// isHandledEvent reports whether agent-director's hook classifier knows
// event (internal/hook ClassifyEvent; the list is read from the code).
func isHandledEvent(event string) bool {
	raw, err := json.Marshal(map[string]string{"hook_event_name": event})
	if err != nil {
		return false
	}
	res, err := hook.ClassifyEvent(raw)
	return err == nil && !res.UnknownEvent && event != ""
}

// isLifecycle is SessionStart or SessionEnd.
func isLifecycle(event string) bool { return event == "SessionStart" || event == "SessionEnd" }

// sessionReading is the row's claude_session_id read with get after one of
// the agent's own SessionStarts.
type sessionReading struct {
	Seq       int    `json:"seq"`
	Source    string `json:"source"`
	PayloadID string `json:"payload_session_id"`
	RowID     string `json:"row_session_id"`
}

// driveExpected are the events the RN-9 drive tries to produce from the
// agent's own process; one not produced is listed, never a failure.
var driveExpected = []string{
	"SessionStart/startup", "UserPromptSubmit", "PreToolUse", "PermissionRequest", "PostToolUse",
	"Stop", "Notification", "SessionStart/clear", "SessionStart/compact", "SessionEnd",
}

// evalDrive is the event drive's verdict. Every handled event from the
// agent's own process (no agent_id) must reach agent-director and pass its
// gate (applied or no_change); one ignored or with no record fails the
// scenario, naming it. After each own SessionStart the row must record the
// payload's session id. The drive needs at least its startup SessionStart
// and its SessionEnd; events it did not produce are listed.
func evalDrive(id string, obs []hookObservation, readings []sessionReading) rn9Scenario {
	sc := rn9Scenario{ID: id, Hooks: obs}
	var fails []string
	seen := map[string]bool{}
	for _, o := range obs {
		if !o.PIDMatch || o.AgentID != "" {
			continue
		}
		seen[o.Event] = true
		if o.Event == "SessionStart" {
			seen["SessionStart/"+o.Source] = true
		}
		if !isHandledEvent(o.Event) {
			continue
		}
		if o.Outcome == hookIgnored || o.Outcome == hookNoRecord {
			fails = append(fails, fmt.Sprintf("seq %d %s from the agent's own process: %s %s", o.Seq, o.Event, o.Outcome, o.Reason))
		}
	}
	for _, r := range readings {
		sc.Notes = append(sc.Notes, fmt.Sprintf("after SessionStart(%s) seq %d: payload session %s, row records %s", r.Source, r.Seq, r.PayloadID, r.RowID))
		if r.PayloadID != "" && r.RowID != r.PayloadID {
			fails = append(fails, fmt.Sprintf("after SessionStart(%s) seq %d the row records %q, the payload's id is %q", r.Source, r.Seq, r.RowID, r.PayloadID))
		}
	}
	var missing []string
	for _, e := range driveExpected {
		if !seen[e] {
			missing = append(missing, e)
		}
	}
	if len(missing) > 0 {
		sc.Notes = append(sc.Notes, "not produced by the drive: "+strings.Join(missing, ", "))
	}
	switch {
	case len(fails) > 0:
		sc.Verdict, sc.Reason = verdictFail, strings.Join(fails, "; ")
	case !seen["SessionStart/startup"] || !seen["SessionEnd"]:
		sc.Verdict, sc.Reason = verdictInconclusive, "the drive did not record the agent's startup SessionStart and its SessionEnd"
	default:
		sc.Verdict = verdictPass
	}
	return sc
}

// evalResume is the in-session /resume verdict over the resuming agent's
// hooks: its SessionStart with source resume must come from its own
// process and be applied, and the row must then record that SessionStart's
// session id, which differs from the agent's startup id. Not applied (or
// the row not moved) is the STOP flag resume-not-applied; no such
// SessionStart at all is inconclusive.
func evalResume(id string, obs []hookObservation, startupID, rowAfter string) rn9Scenario {
	sc := rn9Scenario{ID: id, Hooks: obs}
	var resume *hookObservation
	for i := range obs {
		if obs[i].Event == "SessionStart" && obs[i].Source == "resume" && obs[i].AgentID == "" {
			if obs[i].PIDMatch {
				resume = &obs[i]
				break
			}
			sc.Notes = append(sc.Notes, fmt.Sprintf("seq %d: a SessionStart(resume) from another process (pid %d)", obs[i].Seq, obs[i].ParentPID))
		}
	}
	switch {
	case resume == nil:
		sc.Verdict, sc.Reason = verdictInconclusive, "no SessionStart with source resume came from the agent's own process"
	case resume.Outcome != hookApplied:
		sc.Verdict, sc.Stop = verdictFail, []string{stopResumeNotApplied}
		sc.Reason = fmt.Sprintf("seq %d SessionStart(resume) was %s %s", resume.Seq, resume.Outcome, resume.Reason)
	case rowAfter != resume.TranscriptBase:
		sc.Verdict, sc.Stop = verdictFail, []string{stopResumeNotApplied}
		sc.Reason = fmt.Sprintf("after the resume the row records %q, the resumed session is %q", rowAfter, resume.TranscriptBase)
	case resume.TranscriptBase == "" || resume.TranscriptBase == startupID:
		sc.Verdict = verdictInconclusive
		sc.Reason = fmt.Sprintf("the resumed session id %q is not a new id (startup id %q)", resume.TranscriptBase, startupID)
	default:
		sc.Verdict = verdictPass
		sc.Notes = append(sc.Notes, fmt.Sprintf("same process %d; the row's session moved from %s to %s", resume.ParentPID, startupID, rowAfter))
	}
	return sc
}

// Agent-team display modes (the generated layer's teammateMode).
const (
	teamInProcess = "in-process"
	teamSplitPane = "tmux"
)

// evalTeam is an agent team's verdict over the lead row's hooks. A
// teammate hook is one carrying agent_id, one from another process (a
// split-pane teammate), or a SessionStart/SessionEnd from the lead's
// process whose session is not one of the lead's own (leadIDs).
//   - In-process (lead's process): every teammate SessionStart/SessionEnd
//     must carry agent_id and be ignored as subagent_event; one without
//     agent_id is the STOP flag that reopens WD 2026-09-30b Q2.
//   - Another process: every such hook must be ignored (or never reach
//     agent-director); one applied is the STOP flag
//     split-pane-teammate-hook-applied.
//
// No teammate hook at all (in split-pane mode, none from another process)
// is inconclusive.
func evalTeam(id, teamMode string, obs []hookObservation, leadIDs map[string]bool) rn9Scenario {
	sc := rn9Scenario{ID: id, Hooks: obs}
	var fails []string
	stops := map[string]bool{}
	inProc, split := 0, 0
	for _, o := range obs {
		ownLifecycle := o.PIDMatch && o.AgentID == "" && isLifecycle(o.Event) && o.TranscriptBase != "" && !leadIDs[o.TranscriptBase]
		switch {
		case !o.PIDMatch:
			split++
			switch o.Outcome {
			case hookApplied:
				stops[stopSplitPaneApplied] = true
				fails = append(fails, fmt.Sprintf("seq %d %s from teammate process %d was applied", o.Seq, o.Event, o.ParentPID))
			case hookNoChange:
				fails = append(fails, fmt.Sprintf("seq %d %s from teammate process %d reached agent-director with no ignored record", o.Seq, o.Event, o.ParentPID))
			}
		case ownLifecycle:
			inProc++
			stops[stopInProcessNoAgentID] = true
			fails = append(fails, fmt.Sprintf("seq %d teammate %s (session %s) carries no agent_id: WD 2026-09-30b Q2 reopens", o.Seq, o.Event, o.TranscriptBase))
		case o.AgentID != "":
			inProc++
			if isLifecycle(o.Event) && !(o.Outcome == hookIgnored && o.Reason == "subagent_event") {
				fails = append(fails, fmt.Sprintf("seq %d teammate %s with agent_id was %s %s, want ignored subagent_event", o.Seq, o.Event, o.Outcome, o.Reason))
			}
		}
	}
	sc.Notes = append(sc.Notes, fmt.Sprintf("teammate hooks: %d from the lead's process, %d from other processes", inProc, split))
	if teamMode == teamSplitPane && split == 0 && inProc > 0 {
		sc.Notes = append(sc.Notes, "teammateMode is tmux but the teammates ran in the lead's process")
	}
	for s := range stops {
		sc.Stop = append(sc.Stop, s)
	}
	sort.Strings(sc.Stop)
	switch {
	case len(fails) > 0:
		sc.Verdict, sc.Reason = verdictFail, strings.Join(fails, "; ")
	case teamMode == teamSplitPane && split == 0:
		sc.Verdict, sc.Reason = verdictInconclusive, "no hook from a split-pane teammate's process was recorded"
	case inProc+split == 0:
		sc.Verdict, sc.Reason = verdictInconclusive, "no teammate hook was recorded (did the team start?)"
	default:
		sc.Verdict = verdictPass
	}
	return sc
}

// rn7Rows is RN-7's record, for the record only: one row per recorded
// hook with its key names and ids. A hook with no transcript_path records
// no session id, which is noted, never failed. After each own SessionStart
// the get reading is noted beside it.
func rn7Rows(lines []recordLine, readings []sessionReading) []rn7Row {
	bySeq := map[int]sessionReading{}
	for _, r := range readings {
		bySeq[r.Seq] = r
	}
	rows := make([]rn7Row, 0, len(lines))
	for i, l := range lines {
		row := rn7Row{Seq: i + 1, Event: l.Event, Source: l.Source, Keys: l.Keys,
			TranscriptPresent: l.TranscriptPresent, TranscriptBase: l.TranscriptBase, SessionID: l.SessionID}
		var notes []string
		if !l.TranscriptPresent {
			notes = append(notes, "records no session id")
		}
		if !isHandledEvent(l.Event) {
			notes = append(notes, "not handled by agent-director")
		}
		if r, ok := bySeq[row.Seq]; ok {
			if r.RowID == r.PayloadID {
				notes = append(notes, "get: row records this session")
			} else {
				notes = append(notes, "get: row records "+r.RowID)
			}
		}
		row.Note = strings.Join(notes, "; ")
		rows = append(rows, row)
	}
	return rows
}
