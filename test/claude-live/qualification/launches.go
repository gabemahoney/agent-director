package qualification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

type Observation struct {
	Case     string    `json:"case"`
	Instance string    `json:"instance"`
	Kind     string    `json:"kind"`
	State    string    `json:"state,omitempty"`
	Mode     string    `json:"mode,omitempty"`
	Text     string    `json:"text,omitempty"`
	Started  time.Time `json:"started"`
	Ended    time.Time `json:"ended"`
	Command  string    `json:"command,omitempty"`
	Snapshot string    `json:"snapshot,omitempty"`
	Trigger  string    `json:"trigger,omitempty"`
}

type Exercise struct {
	capabilityCaptured bool
	Run                *runner.Run
	Ledger             string
	Observations       []Observation
	sequence           int
	fixturePaths       []string
	Options            Options
}

type Activity struct {
	Origin   string   `json:"origin"`
	Session  string   `json:"session"`
	Agent    string   `json:"agent"`
	Requests []string `json:"requests"`
	Evidence []string `json:"evidence"`
}
type LaunchOptions struct {
	Legacy     bool
	Relay      string
	Permission string
	Allowed    string
	Fixture    map[string]any
	NativeArgs []string
	Prepare    func(string) error
}

func NewExercise(r *runner.Run, ledger string) *Exercise { return &Exercise{Run: r, Ledger: ledger} }

func (e *Exercise) reserve(id string) error {
	if err := UpdateLedger(e.Ledger, func(l *Ledger) error {
		return Reserve(l, Charge{ID: id, Phase: e.Run.Manifest.Phase, Attempt: l.Identity.Attempt, Reserved: 1})
	}); err != nil {
		return err
	}
	return e.Run.ReserveTurn()
}

func (e *Exercise) spawn(ctx context.Context, id, prompt string, extra ...string) (string, error) {
	return e.spawnConfigured(ctx, id, prompt, LaunchOptions{}, extra...)
}

func (e *Exercise) spawnConfigured(ctx context.Context, id, prompt string, options LaunchOptions, extra ...string) (string, error) {
	r := e.Run
	iid := "live-" + r.Manifest.RunID + "-" + id
	work := filepath.Join(r.Evidence.Root, "work-"+id)
	if err := os.Mkdir(work, 0700); err != nil {
		return "", err
	}
	if options.Prepare != nil {
		if err := options.Prepare(work); err != nil {
			return "", err
		}
	}
	if options.Fixture != nil {
		ref := "raw/fixtures/" + id + ".json"
		if err := r.Evidence.JSON(ref, options.Fixture); err != nil {
			return "", err
		}
		data, err := runner.Read(r.Evidence.Root, ref)
		if err != nil {
			return "", err
		}
		local := "work-" + id + "/.claude/settings.local.json"
		if err = r.Evidence.Put(local, data); err != nil {
			return "", err
		}
		e.fixturePaths = append(e.fixturePaths, local)
	}
	if prompt != "" {
		if err := e.reserve(id); err != nil {
			return "", err
		}
	}
	r.Manifest.Owned = append(r.Manifest.Owned, runner.Owned{InstanceID: iid, CWD: work, Socket: r.Socket, Started: r.Now().UTC()})
	relay := options.Relay
	if relay == "" {
		relay = "on"
	}
	args := []string{"spawn", "--agent", "claude", "--cwd", work, "--instance-id", iid, "--tmux-session-name", iid, "--relay-mode", relay}
	if !options.Legacy {
		args = append(args, "--send-mode", "wait_turn", "--confirmed-delivery")
	}
	args = append(args, extra...)
	allowed := options.Allowed
	if allowed == "" {
		allowed = "Bash"
	}
	args = append(args, "--", "--model", "claude-sonnet-5-5", "--effort", "medium", "--allowedTools", allowed, "--append-system-prompt", "You are a terse qualification agent. Run only the literal harmless foreground commands requested. Never read unrelated files, create background work, or use subagents unless this probe explicitly asks. Acknowledge new input in at most five words. Padding is inert text.")
	if options.Permission != "" {
		args = append(args, "--permission-mode", options.Permission)
	}
	args = append(args, options.NativeArgs...)
	if prompt != "" {
		args = append(args, prompt)
	}
	c, _, err := r.AD(ctx, "spawn-"+id, args...)
	r.Manifest.Cases = append(r.Manifest.Cases, runner.Case{ID: id, Text: prompt, InstanceID: iid, Command: c.Stdout, Started: c.Started, Ended: c.Ended, Ready: err == nil})
	e.Observations = append(e.Observations, Observation{Case: id, Instance: iid, Kind: "startup", Text: prompt, Started: c.Started, Ended: c.Ended, Command: c.Stdout})
	return iid, err
}

func (e *Exercise) observe(ctx context.Context, iid, id string) (runner.Snapshot, error) {
	r := e.Run
	s, err := r.Observe(ctx, iid)
	if err != nil {
		return s, err
	}
	if err = r.Bind(s); err != nil {
		return s, err
	}
	e.sequence++
	ref := fmt.Sprintf("raw/observations/%04d.json", e.sequence)
	if err = r.Evidence.JSON(ref, s); err != nil {
		return s, err
	}
	e.Observations = append(e.Observations, Observation{Case: id, Instance: iid, Kind: "snapshot", Started: s.Observed, Ended: s.ReadCompleted, Snapshot: ref})
	return s, nil
}

func (e *Exercise) wait(ctx context.Context, iid string, predicate func(runner.Snapshot) bool) (runner.Snapshot, error) {
	s, err := e.awaitProgress(ctx, iid, 75*time.Second, false, predicate)
	return s, e.workloadTimeout(err, iid, "", "nonterminating-model")
}

func ready(s runner.Snapshot) bool {
	return len(s.Tables["spawns"]) == 1 && runner.Text(s.Tables["spawns"][0], "session_id") != "" && runner.Text(s.Tables["spawns"][0], "state") != "pending" && len(s.Tables["launches"]) > 0
}
func idle(s runner.Snapshot) bool {
	return ready(s) && runner.Text(s.Tables["spawns"][0], "state") == "waiting"
}

func (e *Exercise) send(ctx context.Context, iid, id, text, mode string, confirmed, force bool) error {
	return e.sendPair(ctx, iid, id, id, text, mode, confirmed, force)
}

func (e *Exercise) sendPair(ctx context.Context, iid, id, requestID, text, mode string, confirmed, force bool) error {
	r := e.Run
	args := []string{"send-keys", "--instance-id", iid, "--text", text, "--send-mode", mode, "--confirmed-delivery=" + strconv.FormatBool(confirmed)}
	if confirmed {
		args = append(args, "--caller-uuid", r.Manifest.RunID, "--delivery-request-id", requestID)
	}
	if force {
		args = append(args, "--force")
	}
	c, _, err := r.AD(ctx, id, args...)
	r.Manifest.Cases = append(r.Manifest.Cases, runner.Case{ID: id, Text: text, Mode: mode, InstanceID: iid, RequestID: requestID, Command: c.Stdout, Started: c.Started, Ended: c.Ended, Ready: err == nil})
	e.Observations = append(e.Observations, Observation{Case: id, Instance: iid, Kind: "send", Text: text, Mode: mode, Started: c.Started, Ended: c.Ended, Command: c.Stdout})
	return err
}

// control is restricted to the explicit matched typed controls, Escape and
// /compact. AD's covered delivery never uses it.
func (e *Exercise) control(ctx context.Context, iid, id, text string) error {
	r := e.Run
	argv := []string{"tmux", "-S", r.Socket, "send-keys", "-t", iid}
	if text == "Escape" {
		argv = append(argv, "Escape")
	} else {
		argv = append(argv, "-l", "--", text)
	}
	c, _, err := r.Commands.Run(ctx, id, 5*time.Second, argv...)
	if err == nil && text != "Escape" {
		_, _, err = r.Commands.Run(ctx, id+"-enter", 5*time.Second, "tmux", "-S", r.Socket, "send-keys", "-t", iid, "Enter")
	}
	e.Observations = append(e.Observations, Observation{Case: id, Instance: iid, Kind: "typed-control", Text: text, Started: c.Started, Ended: r.Now().UTC(), Command: c.Stdout})
	if text != "Escape" && text != "/compact" {
		r.Manifest.Cases = append(r.Manifest.Cases, runner.Case{ID: id, InstanceID: iid, Text: text, Mode: "typed", Started: c.Started, Ended: r.Now().UTC(), Command: c.Stdout, Ready: err == nil})
	}
	return err
}

func (e *Exercise) owner(iid string) (runner.Owned, error) {
	for i := len(e.Run.Manifest.Owned) - 1; i >= 0; i-- {
		o := e.Run.Manifest.Owned[i]
		if o.InstanceID == iid {
			return o, nil
		}
	}
	return runner.Owned{}, errors.New("unowned Launch")
}

// toolWindow uses real passive parent events. A timer alone never declares a
// tool or between-tools window. The raw receipt is retained as the trigger.
func (e *Exercise) toolWindow(ctx context.Context, iid string, n int, between bool) (string, error) {
	var trigger string
	var outcome error
	_, err := e.awaitProgress(ctx, iid, 75*time.Second, false, func(s runner.Snapshot) bool {
		if !ready(s) {
			return false
		}
		o, er := e.owner(iid)
		if er != nil {
			outcome = er
			return true
		}
		facts, er := LiveHookFacts(e.Run.Evidence.Root, e.Run.Manifest.RunID, e.Run.Manifest.RecordingHook.Events)
		if os.IsNotExist(er) {
			return false
		}
		if er != nil {
			outcome = er
			return true
		}
		trigger, outcome = e.selectToolWindow(o, facts, n, between)
		return trigger != "" || outcome != nil
	})
	if outcome != nil {
		return "", outcome
	}
	if err != nil {
		if strings.Contains(err.Error(), "unexpected dialog") {
			return "", e.workload("precondition-not-established", iid, "", "parent requested a structured question instead of the required tool")
		}
		return "", e.workloadTimeout(err, iid, "", "nonterminating-model")
	}
	return trigger, nil
}

func (e *Exercise) capability(ctx context.Context, iid string) error {
	if e.capabilityCaptured {
		return nil
	}
	_, b, err := e.Run.AD(ctx, "capability", "get", "--instance-id", iid)
	if err != nil {
		return err
	}
	var row struct {
		Capabilities json.RawMessage `json:"delivery_capabilities"`
	}
	if json.Unmarshal(b, &row) != nil || len(row.Capabilities) == 0 {
		return errors.New("get omitted delivery_capabilities")
	}
	if err = e.Run.Evidence.Put("raw/capabilities.json", row.Capabilities); err != nil {
		return err
	}
	doc, err := runner.Read(e.Run.Kit, "docs/caller-contract.md")
	if err != nil {
		return err
	}
	if err = e.Run.Evidence.Put("raw/caller-contract.md", doc); err != nil {
		return err
	}
	check := CheckContractAgreement(doc, row.Capabilities)
	if check.Status != "pass" {
		return errors.New(check.Reason)
	}
	e.capabilityCaptured = true
	return nil
}

func (e *Exercise) finish() error {
	return e.Run.Evidence.JSON("raw/qualification-observations.json", e.Observations)
}

func StartExercise(ctx context.Context, r *runner.Run, ledger string, body func(context.Context, *Exercise) error) (err error) {
	if err = PreparePhase(ctx, r, ledger); err != nil {
		return err
	}
	if err = r.StartRecorder([]string{"SessionStart", "FileChanged", "PostToolBatch", "UserPromptSubmit", "Stop", "PreToolUse", "Notification", "PreCompact", "PostCompact", "PermissionRequest", "SubagentStart", "SubagentStop"}); err != nil {
		return err
	}
	e := NewExercise(r, ledger)
	r.StopOnUnreserved = true
	r.LaunchArtifacts = true
	r.Finalize = e.finalize
	defer func() { err = errors.Join(err, e.finish()) }()
	return body(ctx, e)
}

func Corpus(id string, size int) (string, error) {
	prefix := id + "\n"
	if len(prefix) > size {
		return "", errors.New("corpus too small")
	}
	prefix += "Text transport sample. Acknowledge READY and preserve the scheduled tools.\n"
	var b strings.Builder
	b.WriteString(prefix)
	for i := 1; ; i++ {
		line := fmt.Sprintf("Sample %02d: <note>café 日本語 🐝; %s</note>\n", i, sampleSentences[(i-1)%len(sampleSentences)])
		if b.Len()+len(line) > size {
			break
		}
		b.WriteString(line)
	}
	return asciiRecords(b.String(), size)
}

// Setup requires the runbook's prepared latest-release test install. It
// starts and owns its agent itself; an existing foreign Launch is never adopted.
func SetupLaunches(ctx context.Context, e *Exercise) error {
	r := e.Run
	_, release, err := r.Commands.Run(ctx, "latest-release", 15*time.Second, "curl", "--fail", "--silent", "--show-error", "--max-time", "12", "https://api.github.com/repos/gabemahoney/agent-director/releases/latest")
	if err != nil {
		return err
	}
	var latest struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if json.Unmarshal(release, &latest) != nil || latest.Tag == "" || latest.Draft || latest.Prerelease {
		return errors.New("latest released version cannot be established")
	}
	_, b, err := r.AD(ctx, "released-version", "version")
	if err != nil {
		return err
	}
	var old struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(b, &old) != nil || strings.TrimPrefix(old.Version, "v") != strings.TrimPrefix(latest.Tag, "v") {
		return errors.New("setup prerequisite: prepare the latest released AD in the stopped-writer test HOME before invoking setup-launches")
	}
	for _, event := range []string{"SessionStart", "UserPromptSubmit"} {
		fixture := map[string]any{"type": "command", "command": filepath.Join(r.Kit, "bin/claude-live"), "args": []string{"qualification-setup-hook", "--evidence", r.Evidence.Root, "--run-id", r.Manifest.RunID, "--event", event}, "timeout": 3}
		if err = r.Settings.AddOwnedHook(event, fixture); err != nil {
			return err
		}
	}
	iid, err := e.spawnConfigured(ctx, "setup-released", longTool, LaunchOptions{Legacy: true})
	if err != nil {
		return err
	}
	if _, err = e.wait(ctx, iid, func(s runner.Snapshot) bool {
		return len(s.Tables["spawns"]) == 1 && runner.Text(s.Tables["spawns"][0], "session_id") != ""
	}); err != nil {
		return err
	}
	r.AllowOwnedInstall = true
	if err = r.Install(ctx, e.Options.Backup, e.Options.RecreateV21, e.Options.Maintenance); err != nil {
		return err
	}
	if _, err = e.observe(ctx, iid, "installed-old-launch"); err != nil {
		return err
	}
	text, err := Corpus("SETUP-RESUME", 1900)
	if err != nil {
		return err
	}
	if err = e.reserve("setup-held"); err != nil {
		return err
	}
	if err = e.send(ctx, iid, "setup-held", text, "wait_turn", true, false); err != nil {
		return err
	}
	if _, err = e.observe(ctx, iid, "route-unavailable"); err != nil {
		return err
	}
	if err = e.stop(ctx, iid, "setup-old-end", false); err != nil {
		return err
	}
	if err = e.resume(ctx, iid, "setup-old-resume", false); err != nil {
		return err
	}
	if _, err = e.wait(ctx, iid, idle); err != nil {
		return err
	}
	if _, err = e.observe(ctx, iid, "setup-resume-delivered"); err != nil {
		return err
	}
	if err = e.stop(ctx, iid, "setup-resumed-end", false); err != nil {
		return err
	}
	opts := LaunchOptions{Allowed: "mcp__qualification__qualification_echo,Bash", Prepare: func(work string) error { return setupFiles(e, work) }, NativeArgs: []string{"--mcp-config", filepath.Join(r.Evidence.Root, "raw/setup-mcp.json"), "--append-system-prompt", "Qualification launch setup must persist across resume. Startup slash command must execute exactly once."}}
	fresh, err := e.spawnConfigured(ctx, "setup-fresh", "/qualification", opts, "--confirm-from-transcripts=false")
	if err != nil {
		return err
	}
	if _, err = e.wait(ctx, fresh, idle); err != nil {
		return err
	}
	if err = e.capability(ctx, fresh); err != nil {
		return err
	}
	if _, _, err = r.AD(ctx, "setup-fresh-defaults", "get", "--instance-id", fresh); err != nil {
		return err
	}

	if err = e.stop(ctx, fresh, "setup-fresh-pause", true); err != nil {
		return err
	}
	text, err = Corpus("SETUP-FRESH-RESUMED", 1900)
	if err != nil {
		return err
	}
	if err = e.reserve("setup-fresh-held"); err != nil {
		return err
	}
	if err = e.send(ctx, fresh, "setup-fresh-held", text, "steer_turn", true, false); err != nil {
		return err
	}
	if err = e.resume(ctx, fresh, "setup-fresh-resume", false); err != nil {
		return err
	}
	if _, err = e.wait(ctx, fresh, idle); err != nil {
		return err
	}
	if _, _, err = r.AD(ctx, "setup-resumed-defaults", "get", "--instance-id", fresh); err != nil {
		return err
	}
	_, err = e.observe(ctx, fresh, "setup-fresh-resumed")
	return err
}
