package qualification

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
	"github.com/gabemahoney/agent-director/test/claude-live/signals"
)

type OwnerEvidence struct {
	Owner           runner.Owned
	Snapshot        runner.Snapshot
	SnapshotRef     string
	Transcript      signals.Transcript
	TranscriptRef   string
	TranscriptError error
	Trail           []runner.Row
}
type Capture struct {
	Root         string
	Manifest     runner.Manifest
	Before       Ledger
	After        Ledger
	Product      ProductSource
	Commands     map[string]runner.Command
	Observations []Observation
	Hooks        []HookFact
	Owners       []OwnerEvidence
	Snapshots    map[string]runner.Snapshot
}

type evidenceError struct {
	status string
	err    error
}

func (e evidenceError) Error() string { return e.err.Error() }
func (e evidenceError) Unwrap() error { return e.err }
func absent(format string, args ...any) error {
	return evidenceError{"not_completed", fmt.Errorf(format, args...)}
}
func corrupt(format string, args ...any) error {
	return evidenceError{"fail", fmt.Errorf(format, args...)}
}
func evidenceStatus(err error) string {
	var e evidenceError
	if errors.As(err, &e) {
		return e.status
	}
	if os.IsNotExist(err) {
		return "not_completed"
	}
	return "fail"
}

func (c Capture) read(ref string) ([]byte, error) {
	if _, ok := c.Manifest.Artifacts[ref]; !ok {
		return nil, absent("required evidence is not indexed: %s", ref)
	}
	return runner.Read(c.Root, ref)
}
func (c Capture) json(ref string, out any) error {
	b, err := c.read(ref)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(b, out); err != nil {
		return corrupt("invalid %s: %v", ref, err)
	}
	return nil
}

// LoadCapture extends the shared manifest, signal provenance and transcript
// loaders. All reads stay below the explicitly supplied recording directory.
func LoadCapture(root string) (Capture, error) {
	c := Capture{Root: root, Snapshots: map[string]runner.Snapshot{}}
	m, err := runner.ReadManifest(root)
	c.Manifest = m
	if err != nil {
		return c, err
	}
	phase, err := FindPhase(m.Phase)
	if err != nil {
		return c, err
	}
	if m.RunID == "" || m.Started.IsZero() || m.Finished.Before(m.Started) || m.Candidate == "" || m.ProviderVersion == "" || m.TurnBudget > phase.Turns {
		return c, corrupt("invalid phase/run/time/budget identity")
	}
	if strings.ContainsAny(m.RunID, "/\\\r\n\t") {
		return c, corrupt("unsafe capture run identity")
	}
	if err = c.json("raw/qualification-ledger-before.json", &c.Before); err != nil {
		return c, err
	}
	if err = c.json("raw/qualification-ledger-after.json", &c.After); err != nil {
		return c, err
	}
	if _, err = AccountTurns(c.Before); err != nil {
		return c, err
	}
	// Overshoots remain indexed and visible, rather than being erased to make
	// this loader pass. The accounting judgment below reports their failure.
	if c.Before.Identity != c.After.Identity || !samePriorLedger(c.Before.Prior, c.After.Prior) {
		return c, corrupt("authority identity changed during phase")
	}
	i := c.Before.Identity
	if err = i.Validate(); err != nil {
		return c, err
	}
	if i.Candidate != m.Candidate || i.Provider != m.ProviderVersion || i.Kit != m.KitSHA256 {
		return c, corrupt("capture candidate/provider/kit differs from authority")
	}
	c.Commands, err = signals.CandidateCommands(root, m)
	if err != nil {
		return c, err
	}
	c.Product, err = LoadProductSource(root, "raw/qualification/source.json")
	if err != nil {
		return c, err
	}
	var proof runner.CandidateEvidence
	if err = c.json("raw/candidate-installed.json", &proof); err != nil {
		return c, err
	}
	if err = VerifyProductKit(c.Product, proof.Kit); err != nil {
		return c, err
	}
	productBytes, err := c.read("raw/qualification/source.json")
	if err != nil {
		return c, err
	}
	if want, ok := proof.Kit.Files["qualification/source.json"]; !ok || want.SHA256 != runner.Digest(productBytes) || want.Bytes != int64(len(productBytes)) {
		return c, corrupt("product source is not the verified kit's asset")
	}
	if c.Product.Product != i.Product {
		return c, corrupt("product source differs from qualification identity")
	}
	if err = c.json("raw/qualification-observations.json", &c.Observations); err != nil {
		return c, err
	}
	c.Hooks, err = HookFacts(root, m.RunID, m.RecordingHook.Events)
	if err != nil {
		return c, err
	}
	var inventory []runner.HookInvocation
	if err = c.json("raw/hook-inventory.json", &inventory); err != nil {
		return c, err
	}
	actual, err := runner.InspectRecordings(root, m.RunID, m.RecordingHook.Events)
	if err != nil {
		return c, err
	}
	a, _ := json.Marshal(actual)
	b, _ := json.Marshal(inventory)
	if !bytes.Equal(a, b) {
		return c, corrupt("hook inventory changed")
	}
	launches := map[string]bool{}
	for _, o := range m.Owned {
		key := o.InstanceID + "/" + o.LaunchToken
		if o.InstanceID == "" || !strings.HasPrefix(o.InstanceID, "live-"+m.RunID+"-") || launches[key] {
			return c, corrupt("foreign or duplicate owned Launch identity")
		}
		launches[key] = true
		if o.LaunchToken == "" || o.PID <= 0 || o.ProcessStart == "" {
			return c, absent("owned process/Launch identity not established: %s", o.InstanceID)
		}
		evidence := OwnerEvidence{Owner: o}
		prefix := o.InstanceID + "-" + o.LaunchToken
		if !o.Native {
			evidence.SnapshotRef = "raw/final-" + prefix + "-store.json"
			if err = c.json(evidence.SnapshotRef, &evidence.Snapshot); err != nil {
				return c, err
			}
			if err = validateSnapshot(evidence.Snapshot, o); err != nil {
				return c, err
			}
			c.Snapshots[evidence.SnapshotRef] = evidence.Snapshot
		}
		if o.Agent == "" || o.Agent == "claude" {
			if o.SessionID == "" {
				return c, absent("owned Claude session was not established")
			}
			evidence.TranscriptRef = "raw/" + prefix + "-transcript.jsonl"
			data, er := c.read(evidence.TranscriptRef)
			if er != nil {
				if _, indexed := m.Artifacts[evidence.TranscriptRef]; indexed {
					return c, er
				}
				if er = c.expectedNoTranscript(o); er != nil {
					return c, er
				}
				evidence.TranscriptRef = "raw/" + prefix + "-transcript-absence.json"
				data = nil
			}
			evidence.Transcript, evidence.TranscriptError = signals.ParseSessionTranscript(data, o.SessionID)
			if evidence.TranscriptError != nil {
				return c, corrupt("invalid owned transcript: %v", evidence.TranscriptError)
			}
		}
		if !o.Native {
			data, er := c.read("raw/" + prefix + "-trail.jsonl")
			if er != nil {
				return c, er
			}
			scan := bufio.NewScanner(bytes.NewReader(data))
			scan.Buffer(make([]byte, 4096), runner.MaxArtifactBytes)
			for scan.Scan() {
				var row runner.Row
				if er = json.Unmarshal(scan.Bytes(), &row); er != nil {
					return c, er
				}
				if runner.Text(row, "instance_id") != o.InstanceID {
					return c, corrupt("trail contains an unrelated instance")
				}
				evidence.Trail = append(evidence.Trail, row)
			}
			if er = scan.Err(); er != nil {
				return c, er
			}
		}
		c.Owners = append(c.Owners, evidence)
	}
	for _, h := range c.Hooks {
		if _, ok := m.Artifacts[h.Reference]; !ok {
			return c, corrupt("unindexed hook input")
		}
		matched := false
		for _, owner := range c.Owners {
			if runner.Text(h.Payload, "session_id") == owner.Owner.SessionID && runner.Text(h.Payload, "cwd") == owner.Owner.CWD {
				matched = true
				break
			}
		}
		if !matched {
			return c, corrupt("hook input belongs to an unrelated parent")
		}
	}
	for _, o := range c.Observations {
		if o.Snapshot == "" {
			continue
		}
		var snapshot runner.Snapshot
		if err = c.json(o.Snapshot, &snapshot); err != nil {
			return c, err
		}
		if snapshot.InstanceID != o.Instance {
			return c, corrupt("observation target differs from snapshot")
		}
		c.Snapshots[o.Snapshot] = snapshot
	}
	return c, nil
}

func validateSnapshot(s runner.Snapshot, o runner.Owned) error {
	if s.InstanceID != o.InstanceID || len(s.Tables["spawns"]) != 1 {
		return corrupt("snapshot instance mismatch")
	}
	spawn := s.Tables["spawns"][0]
	if runner.Text(spawn, "session_id") != o.SessionID || runner.Text(spawn, "launch_token") != o.LaunchToken {
		return corrupt("snapshot Launch/session mismatch")
	}
	handovers := map[string]bool{}
	for _, h := range s.Tables["handovers"] {
		if runner.Text(h, "instance_id") != o.InstanceID {
			return corrupt("foreign handover")
		}
		handovers[runner.Text(h, "handover_id")] = true
	}
	for _, m := range s.Tables["messages"] {
		if runner.Text(m, "instance_id") != o.InstanceID {
			return corrupt("foreign Message")
		}
	}
	for _, table := range []string{"prompts", "members"} {
		for _, row := range s.Tables[table] {
			if !handovers[runner.Text(row, "handover_id")] {
				return corrupt("foreign prompt/membership")
			}
		}
	}
	return nil
}

func (c Capture) owner(iid, token string) (OwnerEvidence, bool) {
	for i := len(c.Owners) - 1; i >= 0; i-- {
		o := c.Owners[i]
		if o.Owner.InstanceID == iid && (token == "" || o.Owner.LaunchToken == token) {
			return o, true
		}
	}
	return OwnerEvidence{}, false
}

// ownerDuring binds a physical send to one Launch, even if a later resume
// retains the same conversation and all of its earlier request history.
func (c Capture) ownerDuring(iid string, start, end time.Time) (OwnerEvidence, bool) {
	var found OwnerEvidence
	matches := 0
	if start.IsZero() || end.Before(start) {
		return found, false
	}
	for _, candidate := range c.Owners {
		o := candidate.Owner
		if o.InstanceID != iid || start.Before(o.Started) || (!o.StoppedAt.IsZero() && end.After(o.StoppedAt)) {
			continue
		}
		overlapsNext := false
		for _, next := range c.Owners {
			if next.Owner.InstanceID == iid && next.Owner.Started.After(o.Started) && !end.Before(next.Owner.Started) {
				overlapsNext = true
			}
		}
		if !overlapsNext {
			found = candidate
			matches++
		}
	}
	return found, matches == 1
}

func (c Capture) caseByID(id string) (runner.Case, bool) {
	for _, v := range c.Manifest.Cases {
		if v.ID == id && v.Text != "" {
			return v, true
		}
	}
	return runner.Case{}, false
}
func (c Capture) observation(id, kind string) (Observation, bool) {
	for _, v := range c.Observations {
		if v.Case == id && (kind == "" || v.Kind == kind) {
			return v, true
		}
	}
	return Observation{}, false
}
func (c Capture) snapshotCase(id string) (runner.Snapshot, string, bool) {
	o, ok := c.observation(id, "snapshot")
	if !ok {
		return runner.Snapshot{}, "", false
	}
	s, ok := c.Snapshots[o.Snapshot]
	return s, o.Snapshot, ok
}
func (c Capture) prompt(text string) (OwnerEvidence, runner.Row, runner.Row, error) {
	for _, o := range c.Owners {
		if o.Owner.Native {
			continue
		}
		p, ok := promptFor(o.Snapshot, text)
		if !ok {
			continue
		}
		for _, h := range o.Snapshot.Tables["handovers"] {
			if runner.Text(h, "handover_id") == runner.Text(p, "handover_id") && runner.Text(h, "launch_token") == o.Owner.LaunchToken {
				return o, p, h, nil
			}
		}
	}
	return OwnerEvidence{}, nil, nil, absent("no issued frozen prompt for known corpus")
}
func (c Capture) commandJSON(ref string, out any) error {
	if _, ok := c.Commands[ref]; !ok {
		return corrupt("missing command identity for %s", ref)
	}
	return c.json(ref, out)
}
func (c Capture) commandNamed(name string) (runner.Command, bool) {
	for _, cmd := range c.Commands {
		if cmd.Name == name {
			return cmd, true
		}
	}
	return runner.Command{}, false
}
func (c Capture) allCommands(prefix string) []runner.Command {
	var out []runner.Command
	for _, cmd := range c.Commands {
		if strings.HasPrefix(cmd.Name, prefix) {
			out = append(out, cmd)
		}
	}
	return out
}
func scopeRelative(root, name string) (string, error) {
	p, err := runner.Confined(root, name)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, p)
	return filepath.ToSlash(rel), err
}

// Missing transcript files are admissible only with a bounded explicit absence
// observation and a proven no-input launch, or the actual native H1 block.
func (c Capture) expectedNoTranscript(o runner.Owned) error {
	var absence struct {
		Instance, Launch, Session, Reason string
		Observed                          time.Time
	}
	ref := "raw/" + o.InstanceID + "-" + o.LaunchToken + "-transcript-absence.json"
	if err := c.json(ref, &absence); err != nil {
		return absent("missing transcript lacks its owned absence observation")
	}
	if absence.Instance != o.InstanceID || absence.Launch != o.LaunchToken || absence.Session != o.SessionID || absence.Reason != "not_exist" || absence.Observed.Before(o.Started) || absence.Observed.After(c.Manifest.Finished) || !o.Exited {
		return corrupt("invalid transcript absence scope/interval/exit")
	}
	starts := 0
	sessionStart := false
	for _, h := range c.Hooks {
		if runner.Text(h.Payload, "session_id") != o.SessionID || runner.Text(h.Payload, "agent_id") != "" || h.Receipt.Received.Before(o.Started) || (!o.StoppedAt.IsZero() && h.Receipt.Received.After(o.StoppedAt)) {
			continue
		}
		starts += boolInt(h.Receipt.Event == "UserPromptSubmit")
		sessionStart = sessionStart || h.Receipt.Event == "SessionStart"
	}
	if !sessionStart {
		return absent("no-input owner lacks its SessionStart")
	}
	if starts == 0 {
		for _, ob := range c.Observations {
			if ob.Instance != o.InstanceID || ob.Kind != "resume" {
				continue
			}
			command, ok := c.Commands[ob.Command]
			if ok && hasVerb(command.Argv, "spawn") && hasFlag(command.Argv, "--reuse-finished") && len(command.Argv) > 0 && command.ExitCode == 0 && !o.Started.After(command.Started) && command.Ended.After(o.Started) {
				return nil
			}
		}
		return absent("missing journal is not a proven fresh no-input reuse")
	}
	if c.Manifest.Phase != "user-hooks" || !strings.HasSuffix(o.InstanceID, "-H1") || starts != 1 {
		return absent("missing journal cannot prove requested model input")
	}
	for ref := range c.Manifest.Artifacts {
		if !strings.HasPrefix(ref, "raw/user-hook-fixtures/") || !strings.HasSuffix(ref, "/receipt.json") {
			continue
		}
		var rec UserHookReceipt
		if c.json(ref, &rec) != nil || rec.Instance != o.InstanceID || rec.Session != o.SessionID || rec.Action != "block" || rec.Written.Before(rec.Started) || rec.Written.After(o.StoppedAt) {
			continue
		}
		var input, output runner.Row
		if c.json(rec.Input, &input) != nil || c.json(rec.Output, &output) != nil {
			continue
		}
		if runner.Text(input, "session_id") == o.SessionID && runner.Text(input, "cwd") == o.CWD && runner.Text(input, "prompt_id") == rec.Prompt && output["decision"] == "block" {
			return nil
		}
	}
	return absent("H1 journal absence has no bound genuine block")
}
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
