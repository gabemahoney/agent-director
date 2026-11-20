package signals

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

type capture struct {
	m             runner.Manifest
	s             runner.Snapshot
	t             Transcript
	hooks         map[string][]runner.Row
	trail         []runner.Row
	root          string
	refs          []string
	transcriptErr error
	commands      map[string]runner.Command
	unavailable   []string
}

// Startup-only failed captures can lack files the old collector never reached.
// Keep that absence explicit and every unexecuted signal incomplete. Once a
// signal exercise was attempted, all required collected files remain mandatory.
func (c capture) beforeSignals() bool {
	if len(c.m.Problems) == 0 {
		return false
	}
	for _, x := range c.m.Cases {
		if x.ID != "startup" && x.Ready {
			return false
		}
	}
	return true
}

func (c *capture) collected(name string) ([]byte, error) {
	if _, ok := c.m.Artifacts[name]; !ok && c.beforeSignals() {
		if _, err := runner.Read(c.root, name); os.IsNotExist(err) {
			c.unavailable = append(c.unavailable, name)
			return nil, nil
		}
	}
	return c.indexed(name)
}

func (c *capture) bindTranscript() error {
	o := c.m.Owned[0]
	var cwd string
	for _, x := range c.m.Cases {
		if (x.ID != "startup" && x.ID != "startup-continuation") || x.InstanceID != o.InstanceID {
			continue
		}
		cmd, ok := c.commands[x.Command]
		if !ok || (cmd.Name != "spawn" && cmd.Name != "spawn-continuation") || cmd.ExitCode != 0 || cmd.Timeout || cmd.SpawnError != "" || !commandFlag(cmd.Argv, "--instance-id", o.InstanceID) {
			return errors.New("missing successful owned spawn for transcript binding")
		}
		for i := 0; i+1 < len(cmd.Argv); i++ {
			if cmd.Argv[i] == "--cwd" {
				cwd = cmd.Argv[i+1]
				break
			}
		}
	}
	if cwd == "" || (o.CWD != "" && o.CWD != cwd) {
		return errors.New("owned cwd differs from recorded spawn")
	}
	row := c.s.Tables["spawns"][0]
	if stored := runner.Text(row, "cwd"); stored != "" && stored != cwd {
		return errors.New("store cwd differs from recorded spawn")
	}
	path, err := runner.TranscriptPath(runner.TestHome, cwd, o.SessionID, runner.Text(row, "jsonl_path"))
	if err != nil {
		return err
	}
	if o.Transcript != "" && o.Transcript != path {
		return errors.New("owned transcript differs from store/resolved path")
	}
	o.CWD, o.Transcript = cwd, path
	c.m.Owned[0] = o
	return nil
}

func load(root string) (capture, error) { return loadOwner(root, 0) }

func loadOwner(root string, owner int) (capture, error) {
	c := capture{root: root, hooks: map[string][]runner.Row{}}
	manifest, err := runner.ReadManifest(root)
	c.m = manifest
	if err != nil {
		return c, err
	}
	var b []byte
	if c.m.Schema != runner.Schema || c.m.Phase != "probe" || c.m.RunID == "" || c.m.Candidate == "" || c.m.ProviderVersion == "" || (len(c.m.Owned) != 1 && len(c.m.Owned) != 2) || (c.m.TurnBudget != 5 && c.m.TurnBudget != 6 && c.m.TurnBudget != 7) || c.m.Artifacts == nil {
		return c, errors.New("missing or unsupported signal evidence manifest")
	}
	if owner < 0 || owner >= len(c.m.Owned) {
		return c, errors.New("missing selected owner")
	}
	c.m.Owned[0], c.m.Owned[owner] = c.m.Owned[owner], c.m.Owned[0]
	o := c.m.Owned[0]
	if len(c.m.Owned) == 2 && (c.m.Owned[0].InstanceID == c.m.Owned[1].InstanceID || c.m.Owned[0].SessionID == c.m.Owned[1].SessionID) {
		return c, errors.New("duplicate parent identity")
	}
	if o.InstanceID == "" || o.SessionID == "" || o.LaunchID == "" || o.LaunchToken == "" {
		return c, errors.New("missing parent Launch identity")
	}
	if err := c.provenance(); err != nil {
		return c, err
	}
	storeRef := "raw/final-" + o.InstanceID + "-store.json"
	b, err = c.indexed(storeRef)
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal(b, &c.s); err != nil {
		return c, err
	}
	if c.s.InstanceID != o.InstanceID || len(c.s.Tables["spawns"]) != 1 {
		return c, errors.New("store snapshot instance mismatch")
	}
	row := c.s.Tables["spawns"][0]
	if runner.Text(row, "session_id") != o.SessionID || runner.Text(row, "launch_token") != o.LaunchToken {
		return c, errors.New("store snapshot parent mismatch")
	}
	if err := c.bindTranscript(); err != nil {
		return c, err
	}
	o = c.m.Owned[0]
	inventory, err := runner.InspectRecordings(root, c.m.RunID, c.m.RecordingHook.Events)
	if err != nil {
		return c, err
	}
	b, err = c.indexed("raw/hook-inventory.json")
	if err != nil {
		return c, err
	}
	var recorded []runner.HookInvocation
	if json.Unmarshal(b, &recorded) != nil || !reflect.DeepEqual(recorded, inventory) {
		return c, errors.New("recorded hook inventory differs from invocation directories")
	}
	for _, invocation := range inventory {
		if !invocation.Complete {
			return c, fmt.Errorf("incomplete recorder invocation %s: %s", invocation.Directory, invocation.Reason)
		}
		name := invocation.Directory + "/receipt.json"
		b, err := c.indexed(name)
		if err != nil {
			return c, err
		}
		var receipt runner.Receipt
		if json.Unmarshal(b, &receipt) != nil || receipt.Schema != runner.Schema || receipt.RunID != c.m.RunID || !receipt.Complete || receipt.Input != "input.json" {
			return c, fmt.Errorf("incomplete hook receipt %s", name)
		}
		input := filepath.ToSlash(filepath.Join(filepath.Dir(name), receipt.Input))
		b, err = runner.Read(root, input)
		if err != nil {
			return c, err
		}
		if runner.Digest(b) != receipt.SHA256 || len(b) != receipt.Bytes {
			return c, errors.New("recorded hook input mismatch")
		}
		var payload runner.Row
		if err = json.Unmarshal(b, &payload); err != nil {
			return c, err
		}
		if _, ok := c.m.Artifacts[input]; !ok {
			return c, errors.New("unindexed hook input")
		}
		if runner.Text(payload, "session_id") != o.SessionID || runner.Text(payload, "transcript_path") != o.Transcript || runner.Text(payload, "cwd") != o.CWD {
			matched := false
			for _, other := range c.m.Owned[1:] {
				if other.InstanceID != "" && other.SessionID != "" && other.LaunchID != "" && other.LaunchToken != "" && other.CWD != "" && other.Transcript != "" && runner.Text(payload, "session_id") == other.SessionID && runner.Text(payload, "transcript_path") == other.Transcript && runner.Text(payload, "cwd") == other.CWD {
					matched = true
				}
			}
			if !matched {
				return c, errors.New("recorder input belongs to another parent")
			}
			continue
		}
		payload["_ref"], payload["_received"] = input, receipt.Received.Format(time.RFC3339Nano)
		c.hooks[receipt.Event] = append(c.hooks[receipt.Event], payload)
	}
	transcriptRef := "raw/" + o.InstanceID + "-transcript.jsonl"
	b, err = c.collected(transcriptRef)
	if err != nil {
		return c, err
	}
	if len(b) != 0 {
		c.t, c.transcriptErr = ParseTranscript(b, o.SessionID)
	}
	trailRef := "raw/" + o.InstanceID + "-trail.jsonl"
	b, err = c.collected(trailRef)
	if err != nil {
		return c, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			continue
		}
		var row runner.Row
		if json.Unmarshal([]byte(line), &row) != nil {
			return c, errors.New("invalid AD trail")
		}
		if runner.Text(row, "instance_id") != o.InstanceID {
			return c, errors.New("unrelated AD trail")
		}
		c.trail = append(c.trail, row)
	}
	c.refs = []string{"manifest.json", storeRef, transcriptRef, trailRef, "raw/candidate-installed.json", "raw/hook-inventory.json", "commands.jsonl"}
	return c, nil
}

func (c capture) parent(p runner.Row, event string) bool {
	o := c.m.Owned[0]
	return runner.Text(p, "hook_event_name") == event && runner.Text(p, "session_id") == o.SessionID && runner.Text(p, "agent_id") == "" && runner.Text(p, "transcript_path") == o.Transcript
}

func (c capture) fired(event string) bool {
	for _, row := range c.trail {
		outcome := runner.Text(row, "upsert_outcome")
		if runner.Text(row, "event") == "ad.hook.fired" && runner.Text(row, "event_name") == event && runner.Text(row, "session_id") == c.m.Owned[0].SessionID && (outcome == "updated" || outcome == "no_change") {
			return true
		}
	}
	return false
}

func (c capture) caseDone(id string) bool {
	if id == "wake-steer-overlap" {
		return c.caseDone("overlap-wake") && c.caseDone("overlap-steer")
	}
	return c.caseProgress(id).TerminalLine != 0
}

func (c capture) during(p runner.Row, id string) bool {
	t, err := time.Parse(time.RFC3339Nano, runner.Text(p, "_received"))
	if err != nil {
		return false
	}
	for _, x := range c.m.Cases {
		if x.ID == id {
			return !t.Before(x.Started) && !t.After(x.ObservedUntil)
		}
	}
	return false
}

func (c capture) prompt(text string) (runner.Row, runner.Row, error) {
	m, err := c.message(text)
	if err != nil {
		return nil, nil, err
	}
	var prompts, handovers []runner.Row
	for _, member := range c.s.Tables["members"] {
		if runner.Text(member, "message_id") != runner.Text(m, "message_id") {
			continue
		}
		for _, h := range c.s.Tables["handovers"] {
			if runner.Text(h, "handover_id") != runner.Text(member, "handover_id") {
				continue
			}
			for _, p := range c.s.Tables["prompts"] {
				if runner.Text(p, "handover_id") == runner.Text(h, "handover_id") && runner.Number(p, "prompt_index") == runner.Number(member, "prompt_index") && runner.Text(p, "emission") == "issued" {
					prompts = append(prompts, p)
					handovers = append(handovers, h)
				}
			}
		}
	}
	if len(prompts) != 1 {
		return nil, nil, errors.New("Message has no unique issued prompt")
	}
	p, h := prompts[0], handovers[0]
	o := c.m.Owned[0]
	if runner.Text(h, "launch_id") != o.LaunchID || runner.Text(h, "launch_token") != o.LaunchToken || runner.Text(h, "session_id") != o.SessionID || runner.Text(h, "instance_id") != o.InstanceID {
		return p, h, errors.New("issued prompt belongs to another Launch")
	}
	// Transport completion and order are separate facts. A wake needs its
	// uniquely bound sighting as well; neither Stop nor a Final Result suffices.
	if runner.Number(p, "message_count") != 1 || runner.Text(p, "transport") != "written" || runner.Number(p, "issued_at_ms") <= 0 || runner.Number(p, "transport_at_ms") < runner.Number(p, "issued_at_ms") {
		return p, h, errors.New("no complete one-Message AD emission")
	}
	return p, h, nil
}
