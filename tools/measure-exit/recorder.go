package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The RN-9 recorder (`measure-exit record -out FILE`) is a hook program the
// harness registers, in exec form, for every Claude Code event in the
// generated .claude/settings.local.json of an RN-9 agent's working
// directory, beside agent-director's own hooks (which stay in place). Per
// invocation it appends one line to FILE: the event, the payload's top-level
// key names, whether transcript_path is present and its basename without
// extension, the ids session_id, agent_id and agent_type, SessionStart's
// source, its own parent pid (in exec form, the Claude process that ran it)
// and the row id from AGENT_DIRECTOR_INSTANCE_ID. It never records a value
// of any other key: no prompt, tool input or output, message, file content,
// environment value or credential. It prints nothing and always exits 0, so
// it never changes the agent's behaviour.

// recorderEvents are the Claude Code events the recorder is registered
// for: agent-director's eight and the lifecycle events it does not handle
// (recorded as "not handled"). Events newer Claude Code versions may add
// (TeammateIdle, TaskCompleted) are left out, so an older version given
// this layer never meets an event name it does not know.
var recorderEvents = []string{
	"SessionStart", "SessionEnd", "UserPromptSubmit", "PreToolUse", "PostToolUse",
	"PermissionRequest", "Notification", "Stop", "SubagentStart", "SubagentStop", "PreCompact",
}

// recorderToolEvents carry a tool matcher; "*" matches every tool.
var recorderToolEvents = map[string]bool{"PreToolUse": true, "PostToolUse": true, "PermissionRequest": true}

// maxRecordedPayload bounds how much of a payload the recorder reads.
const maxRecordedPayload = 16 << 20

// recordedIDPattern is what an id-like field must look like to be kept;
// anything else is written as unrecognizedValue, so a field that ever held
// free text could not carry it into the record.
var recordedIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:@+-]{1,128}$`)

// unrecognizedValue replaces an id-like field that is not id-shaped.
const unrecognizedValue = "(unrecognized)"

// recordLine is one hook invocation as the recorder saw it.
type recordLine struct {
	// TimeNS is the wall clock at the invocation, in Unix nanoseconds.
	TimeNS            int64    `json:"t_ns"`
	Event             string   `json:"event"`
	Source            string   `json:"source,omitempty"`
	Keys              []string `json:"keys"`
	TranscriptPresent bool     `json:"transcript_path_present"`
	TranscriptBase    string   `json:"transcript_basename,omitempty"`
	SessionID         string   `json:"session_id,omitempty"`
	AgentID           string   `json:"agent_id,omitempty"`
	AgentType         string   `json:"agent_type,omitempty"`
	ParentPID         int      `json:"parent_pid"`
	InstanceID        string   `json:"instance_id,omitempty"`
}

// recorderLayer is a generated settings layer that registers the recorder
// (exe record -out out) for every recorderEvents event, in exec form,
// merged with extra top-level keys (env, teammateMode).
func recorderLayer(exe, out string, extra map[string]any) map[string]any {
	hooks := map[string]any{}
	for _, ev := range recorderEvents {
		entry := map[string]any{"hooks": []any{map[string]any{
			"type": "command", "command": exe, "args": []string{"record", "-out", out},
		}}}
		if recorderToolEvents[ev] {
			entry["matcher"] = "*"
		}
		hooks[ev] = []any{entry}
	}
	doc := map[string]any{"hooks": hooks}
	for k, v := range extra {
		doc[k] = v
	}
	return doc
}

// recordCommand is the record subcommand. Whatever happens it prints
// nothing and returns 0.
func recordCommand(args []string, _, _ io.Writer) int {
	fs := flag.NewFlagSet("record", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	out := fs.String("out", "", "file the line is appended to")
	if fs.Parse(args) != nil || *out == "" || !filepath.IsAbs(*out) {
		return 0
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, maxRecordedPayload))
	if err != nil {
		return 0
	}
	line := recordFromPayload(raw, time.Now(), os.Getppid(), os.Getenv("AGENT_DIRECTOR_INSTANCE_ID"))
	_ = appendRecord(*out, line)
	return 0
}

// recordFromPayload builds the line for one payload. It reads only the
// key names and the id-like fields listed on recordLine; an unparseable
// payload gives a line with event "(unparseable)".
func recordFromPayload(raw []byte, now time.Time, ppid int, instanceID string) recordLine {
	line := recordLine{TimeNS: now.UnixNano(), ParentPID: ppid, Keys: []string{}, InstanceID: idField(instanceID)}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		line.Event = "(unparseable)"
		return line
	}
	for k := range doc {
		line.Keys = append(line.Keys, idField(k))
	}
	sort.Strings(line.Keys)
	line.Event = idField(stringField(doc, "hook_event_name"))
	if line.Event == "" {
		line.Event = idField(stringField(doc, "event_name"))
	}
	if tp, ok := doc["transcript_path"]; ok {
		var s string
		if json.Unmarshal(tp, &s) == nil && s != "" {
			line.TranscriptPresent = true
			line.TranscriptBase = idField(transcriptBase(s))
		}
	}
	line.SessionID = idField(stringField(doc, "session_id"))
	line.AgentID = idField(stringField(doc, "agent_id"))
	line.AgentType = idField(stringField(doc, "agent_type"))
	if line.Event == "SessionStart" {
		line.Source = idField(stringField(doc, "source"))
	}
	return line
}

// stringField is doc[key] when it is a JSON string, else "".
func stringField(doc map[string]json.RawMessage, key string) string {
	var s string
	if raw, ok := doc[key]; ok && json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

// idField keeps an id-shaped value and replaces anything else.
func idField(v string) string {
	if v == "" || recordedIDPattern.MatchString(v) {
		return v
	}
	return unrecognizedValue
}

// transcriptBase is a transcript path's basename without its extension,
// computed as agent-director's hook classifier does (internal/hook
// extractSessionID), so the recorder's id and the trail's compare.
func transcriptBase(p string) string {
	base := filepath.Base(p)
	if base == "/" || base == "." || base == "" {
		return ""
	}
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	if base == "." || base == ".." {
		return ""
	}
	return base
}

// appendRecord appends one JSON line with a single write on an O_APPEND
// file, so concurrent hooks never interleave a line.
func appendRecord(path string, line recordLine) error {
	b, err := json.Marshal(line)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(b, '\n'))
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

// readRecordLines reads a recorder file. A missing file has no lines; a
// line that does not parse is skipped (a hook killed mid-write).
func readRecordLines(path string) ([]recordLine, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []recordLine
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var l recordLine
		if json.Unmarshal(sc.Bytes(), &l) == nil && l.Event != "" {
			out = append(out, l)
		}
	}
	return out, sc.Err()
}
