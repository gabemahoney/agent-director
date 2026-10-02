package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// panePID is the lead's pane process in these fixtures.
const panePID = 100

// ob builds a hook observation from the lead's process, applied, with
// overrides.
func ob(event string, edit ...func(o *hookObservation)) hookObservation {
	o := hookObservation{Event: event, ParentPID: panePID, PanePID: panePID, PIDMatch: true, TranscriptBase: "sess-lead", Outcome: hookApplied}
	if event == "SessionStart" {
		o.Source = "startup"
	}
	for _, e := range edit {
		e(&o)
	}
	return o
}

// seqd numbers observations 1..n.
func seqd(obs ...hookObservation) []hookObservation {
	for i := range obs {
		obs[i].Seq = i + 1
	}
	return obs
}

// withOutcome, source, session, agentID and fromPID are ob overrides.
func withOutcome(out, reason string) func(*hookObservation) {
	return func(o *hookObservation) { o.Outcome, o.Reason = out, reason }
}
func source(s string) func(*hookObservation) { return func(o *hookObservation) { o.Source = s } }
func session(s string) func(*hookObservation) {
	return func(o *hookObservation) { o.TranscriptBase = s }
}
func agentID(id string) func(*hookObservation) { return func(o *hookObservation) { o.AgentID = id } }
func fromPID(pid int) func(*hookObservation) {
	return func(o *hookObservation) { o.ParentPID, o.PIDMatch = pid, false }
}

func TestJoinOutcomes(t *testing.T) {
	line := func(event string, ppid int) recordLine {
		return recordLine{Event: event, ParentPID: ppid, InstanceID: "id-1", TranscriptBase: "sess-1"}
	}
	fired := func(event, upsert string) trailHook {
		return trailHook{instance: "id-1", event: event, sessionID: "sess-1", upsert: upsert}
	}
	ignored := func(event string, ppid int, reason string) trailHook {
		return trailHook{ignored: true, instance: "id-1", event: event, sessionID: "sess-1", parentPID: ppid, reason: reason}
	}
	lines := []recordLine{
		line("SessionStart", 200), // ignored with its own fired no_change
		line("SessionStart", 100), // applied
		line("Stop", 100),         // no_change
		line("Stop", 100),         // its one record is taken: no_record
		line("Notification", 300), // the ignored record names another pid: no_record
	}
	trail := []trailHook{
		fired("SessionStart", "no_change"), ignored("SessionStart", 200, "pid_mismatch"),
		fired("SessionStart", "updated"), fired("Stop", "no_change"), ignored("Notification", 301, "pid_mismatch"),
		{instance: "id-2", event: "Stop", sessionID: "sess-1", upsert: "updated"},
	}
	got := joinOutcomes(lines, map[string]int{"id-1": 100}, trail)
	want := []struct {
		out, reason string
		match       bool
	}{{hookIgnored, "pid_mismatch", false}, {hookApplied, "", true}, {hookNoChange, "", true}, {hookNoRecord, "", true}, {hookNoRecord, "", false}}
	for i, w := range want {
		if got[i].Seq != i+1 || got[i].Outcome != w.out || got[i].Reason != w.reason || got[i].PIDMatch != w.match || got[i].PanePID != 100 {
			t.Errorf("line %d: %+v, want %+v", i+1, got[i], w)
		}
	}
}

func TestEvalDrive(t *testing.T) {
	full := func() []hookObservation {
		return seqd(ob("SessionStart"), ob("UserPromptSubmit"), ob("PreToolUse"), ob("PermissionRequest"), ob("PostToolUse"),
			ob("Stop"), ob("Notification", withOutcome(hookNoChange, "")), ob("SessionStart", source("clear"), session("sess-2")),
			ob("SessionStart", source("compact"), session("sess-3")), ob("PreCompact", withOutcome(hookNoRecord, "")),
			ob("SessionStart", agentID("sub-1"), withOutcome(hookIgnored, "subagent_event")), ob("SessionEnd"))
	}
	readings := []sessionReading{{Seq: 1, Source: "startup", PayloadID: "sess-lead", RowID: "sess-lead"}}
	for _, tc := range []struct {
		name     string
		obs      []hookObservation
		readings []sessionReading
		verdict  string
		reason   string
		note     string
	}{
		{"every event applied or no_change", full(), readings, verdictPass, "", ""},
		{"an own hook ignored", func() []hookObservation {
			o := full()
			o[2].Outcome, o[2].Reason = hookIgnored, "pid_mismatch"
			return o
		}(), readings, verdictFail, "seq 3 PreToolUse from the agent's own process: ignored pid_mismatch", ""},
		{"an own hook with no record", func() []hookObservation {
			o := full()
			o[5].Outcome = hookNoRecord
			return o
		}(), readings, verdictFail, "seq 6 Stop", ""},
		{"the row records another session", full(), []sessionReading{{Seq: 8, Source: "clear", PayloadID: "sess-2", RowID: "sess-lead"}},
			verdictFail, `the row records "sess-lead", the payload's id is "sess-2"`, ""},
		{"no SessionEnd", full()[:11], readings, verdictInconclusive, "startup SessionStart and its SessionEnd", "not produced by the drive: SessionEnd"},
		{"no Notification is a note", append(full()[:6:6], full()[7:]...), readings, verdictPass, "", "not produced by the drive: Notification"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc := evalDrive(rn9DriveID, tc.obs, tc.readings)
			if sc.Verdict != tc.verdict || !strings.Contains(sc.Reason, tc.reason) || len(sc.Stop) != 0 {
				t.Fatalf("scenario %+v", sc)
			}
			if tc.note != "" && !strings.Contains(strings.Join(sc.Notes, "\n"), tc.note) {
				t.Errorf("notes %q lack %q", sc.Notes, tc.note)
			}
		})
	}
}

func TestEvalResume(t *testing.T) {
	resume := func(edit ...func(*hookObservation)) hookObservation {
		return ob("SessionStart", append([]func(*hookObservation){source("resume"), session("sess-old")}, edit...)...)
	}
	for _, tc := range []struct {
		name     string
		obs      []hookObservation
		rowAfter string
		verdict  string
		stop     bool
	}{
		{"applied and the row moved", seqd(ob("SessionStart"), resume()), "sess-old", verdictPass, false},
		{"ignored", seqd(ob("SessionStart"), resume(withOutcome(hookIgnored, "pid_mismatch"))), "sess-old", verdictFail, true},
		{"no record", seqd(ob("SessionStart"), resume(withOutcome(hookNoRecord, ""))), "sess-old", verdictFail, true},
		{"applied but the row did not move", seqd(ob("SessionStart"), resume()), "sess-lead", verdictFail, true},
		{"no resume SessionStart", seqd(ob("SessionStart"), ob("Stop")), "sess-lead", verdictInconclusive, false},
		{"resume only from another process", seqd(resume(fromPID(555))), "sess-old", verdictInconclusive, false},
		{"the resumed id is the startup id", seqd(resume(session("sess-lead"))), "sess-lead", verdictInconclusive, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc := evalResume(rn9ResumeID, tc.obs, "sess-lead", tc.rowAfter)
			if sc.Verdict != tc.verdict || reflect.DeepEqual(sc.Stop, []string{stopResumeNotApplied}) != tc.stop {
				t.Fatalf("scenario %+v", sc)
			}
		})
	}
}

func TestEvalTeam(t *testing.T) {
	lead := map[string]bool{"sess-lead": true}
	for _, tc := range []struct {
		name    string
		mode    string
		obs     []hookObservation
		verdict string
		stop    []string
		note    string
	}{
		{"in-process teammates with agent_id ignored", teamInProcess, seqd(ob("SessionStart"),
			ob("SessionStart", agentID("tm-1"), session("sess-tm"), withOutcome(hookIgnored, "subagent_event")),
			ob("SessionEnd", agentID("tm-1"), session("sess-tm"), withOutcome(hookIgnored, "subagent_event"))),
			verdictPass, nil, ""},
		{"an in-process teammate without agent_id", teamInProcess, seqd(ob("SessionStart"),
			ob("SessionStart", session("sess-tm"), withOutcome(hookNoChange, ""))),
			verdictFail, []string{stopInProcessNoAgentID}, ""},
		{"a teammate with agent_id applied", teamInProcess, seqd(ob("SessionEnd", agentID("tm-1"), session("sess-tm"))),
			verdictFail, nil, ""},
		{"split-pane teammate ignored", teamSplitPane, seqd(ob("SessionStart"),
			ob("SessionStart", fromPID(555), session("sess-tm"), withOutcome(hookIgnored, "pid_mismatch")),
			ob("Stop", fromPID(555), session("sess-tm"), withOutcome(hookNoRecord, ""))),
			verdictPass, nil, ""},
		{"split-pane teammate applied", teamSplitPane, seqd(ob("SessionStart", fromPID(555), session("sess-tm"))),
			verdictFail, []string{stopSplitPaneApplied}, ""},
		{"split-pane teammate reached agent-director unrecorded", teamSplitPane,
			seqd(ob("Stop", fromPID(555), session("sess-tm"), withOutcome(hookNoChange, ""))), verdictFail, nil, ""},
		{"tmux mode but the teammates ran in-process", teamSplitPane,
			seqd(ob("SessionStart", agentID("tm-1"), session("sess-tm"), withOutcome(hookIgnored, "subagent_event"))),
			verdictInconclusive, nil, "teammateMode is tmux but the teammates ran in the lead's process"},
		{"no teammate hook", teamInProcess, seqd(ob("SessionStart"), ob("Stop")), verdictInconclusive, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc := evalTeam(rn9TeamInProcessID, tc.mode, tc.obs, lead)
			if sc.Verdict != tc.verdict || !reflect.DeepEqual(sc.Stop, tc.stop) {
				t.Fatalf("scenario %+v", sc)
			}
			if tc.note != "" && !strings.Contains(strings.Join(sc.Notes, "\n"), tc.note) {
				t.Errorf("notes %q", sc.Notes)
			}
		})
	}
}

func TestRN7RowsAreARecordOnly(t *testing.T) {
	lines := []recordLine{
		{Event: "SessionStart", Source: "startup", Keys: []string{"session_id", "transcript_path"}, TranscriptPresent: true, TranscriptBase: "s1", SessionID: "s1"},
		{Event: "Notification", Keys: []string{"hook_event_name", "message"}},
		{Event: "PreCompact", Keys: []string{"trigger"}, TranscriptPresent: true, TranscriptBase: "s1"},
	}
	rows := rn7Rows(lines, []sessionReading{{Seq: 1, PayloadID: "s1", RowID: "s1"}})
	wantNotes := []string{"get: row records this session", "records no session id", "not handled by agent-director"}
	for i, w := range wantNotes {
		if rows[i].Seq != i+1 || rows[i].Note != w {
			t.Errorf("row %d: %+v, want note %q", i+1, rows[i], w)
		}
	}
	d := decide([]decideInput{{Dir: "l2", Guard: "pass", Res: results{Schema: resultsSchema, RN7: &rn7Record{Rows: rows}}}}, curDefaults)
	if !strings.Contains(d.Record, "Records no session id: Notification (1).") {
		t.Errorf("decide's RN-7 summary:\n%s", d.Record)
	}
	for _, s := range append(d.Stops, d.Invalid...) {
		if strings.Contains(s, "RN-7") {
			t.Errorf("RN-7 raised %q", s)
		}
	}
}

func TestRecorderLayerIsExecForm(t *testing.T) {
	doc := recorderLayer("/opt/mx/measure-exit", "/r/hooks.jsonl", map[string]any{"teammateMode": "tmux"})
	b, _ := json.Marshal(doc)
	var parsed struct {
		TeammateMode string `json:"teammateMode"`
		Hooks        map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string   `json:"command"`
				Args    []string `json:"args"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.TeammateMode != "tmux" || len(parsed.Hooks) != 11 {
		t.Fatalf("layer %s", b)
	}
	for ev, groups := range parsed.Hooks {
		h := groups[0].Hooks[0]
		if h.Command != "/opt/mx/measure-exit" || !reflect.DeepEqual(h.Args, []string{"record", "-out", "/r/hooks.jsonl"}) {
			t.Errorf("%s: %+v", ev, h)
		}
		if (groups[0].Matcher == "*") != (ev == "PreToolUse" || ev == "PostToolUse" || ev == "PermissionRequest") {
			t.Errorf("%s matcher %q", ev, groups[0].Matcher)
		}
	}
}

// runRecorder runs the record subcommand with payload on stdin.
func runRecorder(t *testing.T, payload string, args ...string) (code int, out string) {
	t.Helper()
	in := filepath.Join(t.TempDir(), "stdin")
	writeFile(t, in, payload)
	f, err := os.Open(in)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	orig := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = orig }()
	var stdout, stderr bytes.Buffer
	code = dispatch(append([]string{"record"}, args...), &stdout, &stderr)
	return code, stdout.String() + stderr.String()
}

func TestRecorderKeepsKeysAndIDsOnly(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-recorder-env-sentinel-0123456789")
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "id-row-1")
	payload := `{"hook_event_name":"SessionStart","source":"startup","session_id":"sess-1",
		"transcript_path":"/home/t/.claude/projects/p/sess-1.jsonl","agent_id":"has spaces PROMPT-SENTINEL",
		"prompt":"PROMPT-SENTINEL text","tool_input":{"command":"TOOL-INPUT-SENTINEL"},"message":"MESSAGE-SENTINEL"}`
	out := filepath.Join(t.TempDir(), "hooks.jsonl")
	code, printed := runRecorder(t, payload, "-out", out)
	if code != 0 || printed != "" {
		t.Fatalf("exit %d, printed %q", code, printed)
	}
	raw := readFile(t, out)
	assertAbsent(t, "recorder line", raw, "PROMPT-SENTINEL", "TOOL-INPUT-SENTINEL", "MESSAGE-SENTINEL", "sk-ant-recorder-env-sentinel", "/home/t/.claude")
	lines, err := readRecordLines(out)
	if err != nil || len(lines) != 1 {
		t.Fatalf("lines %v, %v", lines, err)
	}
	l := lines[0]
	wantKeys := []string{"agent_id", "hook_event_name", "message", "prompt", "session_id", "source", "tool_input", "transcript_path"}
	if l.Event != "SessionStart" || l.Source != "startup" || l.SessionID != "sess-1" || l.TranscriptBase != "sess-1" ||
		!l.TranscriptPresent || l.AgentID != unrecognizedValue || l.InstanceID != "id-row-1" || l.ParentPID != os.Getppid() ||
		!reflect.DeepEqual(l.Keys, wantKeys) || time.Since(time.Unix(0, l.TimeNS)) > time.Minute {
		t.Errorf("line %+v", l)
	}
	for _, args := range [][]string{{}, {"-out", "relative.jsonl"}, {"-bogus"}} {
		if code, printed := runRecorder(t, payload, args...); code != 0 || printed != "" {
			t.Errorf("%q: exit %d printed %q", args, code, printed)
		}
	}
	if code, _ := runRecorder(t, "not json", "-out", out); code != 0 {
		t.Error("an unparseable payload changed the exit status")
	}
	if lines, _ := readRecordLines(out); len(lines) != 2 || lines[1].Event != "(unparseable)" {
		t.Errorf("lines %+v", lines)
	}
}

func TestAnswerPermissions(t *testing.T) {
	for _, tc := range []struct {
		scenario, tool, decision, note string
	}{
		{rn9DriveID, "Bash", "allow", "permission request answered: allow (Bash)"},
		{rn9DriveID, "Write", "deny", `permission request answered: deny ("Write" is not a tool this scenario expects)`},
		{rn9ResumeID, "Bash", "deny", `deny ("Bash" is not a tool this scenario expects)`},
		{rn9TeamInProcessID, "Agent", "allow", "allow (Agent)"},
		{rn9TeamSplitPaneID, "SendMessage", "allow", "allow (SendMessage)"},
		{rn9TeamInProcessID, "Bash", "deny", `deny ("Bash" is not a tool this scenario expects)`},
	} {
		t.Run(tc.scenario+"/"+tc.tool, func(t *testing.T) {
			r := newRig(t, modeDry)
			r.ex.reply = func(argv []string) (string, string, int) {
				if argv[1] == "get" {
					return `{"claude_instance_id":"id-1","permission_requests":[{"request_token":"req-1","tool_name":"` + tc.tool + `"}]}`, "", 0
				}
				return "", "", 0
			}
			d := &rn9Agent{h: r.h, caseID: tc.scenario, a: agentRef{InstanceID: "id-1"}, answered: map[string]bool{}}
			for i := 0; i < 2; i++ {
				if err := d.answerPermissions(); err != nil {
					t.Fatal(err)
				}
			}
			if r.ex.count("decide") != 1 || r.ex.count("decide", "--request-token", "req-1", "--decision", tc.decision) != 1 {
				t.Errorf("decide calls %q, want one --decision %s", r.ex.calls, tc.decision)
			}
			if len(d.notes) != 1 || !strings.Contains(d.notes[0], tc.note) {
				t.Errorf("notes %q, want %q", d.notes, tc.note)
			}
		})
	}
}
