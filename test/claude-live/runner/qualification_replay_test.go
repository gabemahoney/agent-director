package runner_test

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/test/claude-live/internal/qualificationfix"
	q "github.com/gabemahoney/agent-director/test/claude-live/qualification"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
	"github.com/gabemahoney/agent-director/test/claude-live/signals"
)

// t3.xis.w3.z9.9e: run the actual six producers through preparation and cleanup.
// Only external command effects and provider callbacks are scripted. A prefix
// assertion never supplies model intake, admission or a passing phase verdict.
func TestQualificationSetupReplay(t *testing.T) {
	kit := replayUnitKit(t) // Existing three-executable fixture, once per suite.
	for _, tc := range []replayCase{
		{name: "typed-a4-unobserved-sibling", phase: "typed-controls", schedule: "a4"},
		{name: "typed-a2-recorded-offsets", phase: "typed-controls", schedule: "a2"},
		{name: "typed-a3-recorded-offsets", phase: "typed-controls", schedule: "a3"},
		{name: "typed-reverse-hooks", phase: "typed-controls", schedule: "a4", reverseHooks: true},
		{name: "typed-sibling-binding-first", phase: "typed-controls", schedule: "a4", siblingFirst: true},
		{name: "typed-reverse-both-receipt-lag", phase: "typed-controls", schedule: "a4", siblingFirst: true, reverseHooks: true, receiptLag: true},
		{name: "typed-path-column-late", phase: "typed-controls", schedule: "a4", pathLate: true},
		{name: "typed-a4-UPS-during-cleanup", phase: "typed-controls", schedule: "a4", lateCleanup: true},
		{name: "typed-never-binding", phase: "typed-controls", schedule: "a4", fault: "never-binding"},
		{name: "typed-foreign-session", phase: "typed-controls", schedule: "a4", fault: "foreign-session"},
		{name: "typed-foreign-cwd", phase: "typed-controls", schedule: "a4", fault: "foreign-cwd"},
		{name: "typed-malformed-receipt", phase: "typed-controls", schedule: "a4", fault: "malformed-receipt"},
		{name: "typed-cancelled-binding", phase: "typed-controls", schedule: "a4", fault: "cancelled-binding"},
		{name: "typed-incomplete-receipt", phase: "typed-controls", schedule: "a4", fault: "incomplete-receipt"},
		{name: "typed-extra-start", phase: "typed-controls", schedule: "a4", fault: "extra-start"},
		{name: "force-F1", phase: "force-states"},
		{name: "dialogs-D1", phase: "dialogs", dialogs: []string{"D1", "D2"}},
		{name: "dialogs-D3-startup", phase: "dialogs", dialogs: []string{"D1", "D2", "D3"}},
		{name: "dialogs-D4", phase: "dialogs", dialogs: []string{"D4"}},
		{name: "dialogs-D5", phase: "dialogs", dialogs: []string{"D5"}},
		{name: "dialogs-D6", phase: "dialogs", dialogs: []string{"D6"}},
		{name: "groups-G0", phase: "groups"},
		{name: "groups-missing-prerequisite", phase: "groups", fault: "missing-prerequisite"},
		{name: "groups-incomplete-prerequisite", phase: "groups", fault: "incomplete-prerequisite"},
		{name: "text-native-N1", phase: "text-limits-backlog"},
		{name: "text-native-N1-own-receipt-late", phase: "text-limits-backlog", receipt: "late"},
		{name: "text-native-N7", phase: "text-limits-backlog", variant: "N7"},
		{name: "text-backlog-N8", phase: "text-limits-backlog", variant: "N8"},
		{name: "esc-E1", phase: "esc-gap"},
		{name: "esc-own-receipt-late", phase: "esc-gap", receipt: "late"},
		{name: "esc-partial-receipt-mid-write", phase: "esc-gap", receipt: "partial"},
		{name: "esc-real-clock", phase: "esc-gap", realClock: true},
	} {
		t.Run(tc.name, func(t *testing.T) { newSetupReplay(t, kit, tc).run() })
	}
}

type replayCase struct {
	name, phase, schedule, fault, variant, receipt              string
	dialogs                                                     []string
	reverseHooks, siblingFirst, receiptLag, pathLate, realClock bool
	lateCleanup                                                 bool
}

type replayRecordedEvent struct {
	Attempt    string     `json:"attempt"`
	Role       string     `json:"role"`
	Event      string     `json:"event"`
	Received   time.Time  `json:"received"`
	InputSHA   string     `json:"input_sha256"`
	ReceiptSHA string     `json:"receipt_sha256"`
	Source     string     `json:"source"`
	ToolFacts  runner.Row `json:"tool_facts,omitempty"`
}

type replayParent struct {
	id, role, session, cwd, transcript, token, prompt string
	child                                             *exec.Cmd
	start                                             string
	bound, exited                                     bool
}

type replayGateDriver struct {
	command *exec.Cmd
	start   string
	done    chan struct{}
}

func (d replayGateDriver) wait(ctx context.Context) error {
	select {
	case <-d.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type setupReplay struct {
	t                 *testing.T
	r                 *runner.Run
	tc                replayCase
	kit               runner.Kit
	product           q.ProductSource
	ledger            string
	options           q.Options
	clock             time.Time
	base              time.Time
	parents           []*replayParent
	events            []replayRecordedEvent
	emitted           map[int]bool
	polls             int
	stop              error
	stopped, cleaning bool
	inputs            [][]string
	capabilities      json.RawMessage
	settings          []byte
	cancel            context.CancelFunc
	receiptPending    bool
	receiptCompleted  bool
	cleanupStarted    time.Time
	cleanupUPS        bool
	workload          *workloadReplay
	drivers           []replayGateDriver
	openRaw           func(string) (*sql.DB, error)
}

func replayRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func replayWrite(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func replayJSON(t *testing.T, path string, value any) {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	replayWrite(t, path, append(b, '\n'))
}

func replayDecode(t *testing.T, path string, value any) {
	t.Helper()
	if err := json.Unmarshal(replayRead(t, path), value); err != nil {
		t.Fatal(err)
	}
}

func replayUnitKit(t *testing.T) string {
	t.Helper()
	root := runner.LifecycleKitForSignalsTest(t)
	var k runner.Kit
	replayDecode(t, filepath.Join(root, "kit.json"), &k)
	for target, source := range map[string]string{
		"docs/caller-contract.md": "../../../docs/caller-contract.md",
		"qualification/launch.go": "../../../internal/spawn/launch.go",
		"qualification/spawns.go": "../../../internal/store/spawns.go",
	} {
		b := replayRead(t, source)
		replayWrite(t, filepath.Join(root, target), b)
		k.Files[target] = runner.Artifact{SHA256: runner.Digest(b), Bytes: int64(len(b))}
	}
	// Explicit unit subset, never the final candidate's product identity. The
	// real product digest and kit-source joins remain mandatory in PreparePhase.
	p := q.ProductSource{Schema: q.Schema, Algorithm: q.ProductAlgorithm, Toolchain: "synthetic-unit-subset/" + runtime.Version(), Platform: k.Platform,
		Snapshot: k.SnapshotTree, SourceArchive: k.SourceSHA256, InventorySHA256: runner.Digest([]byte("unit replay inventory")), Files: map[string]string{}}
	for _, name := range []string{"go.mod", "go.sum"} {
		p.Files[name] = runner.Digest(replayRead(t, "../../../"+name))
	}
	var err error
	p.Product, err = q.ProductDigest(p)
	if err != nil {
		t.Fatal(err)
	}
	replayJSON(t, filepath.Join(root, "qualification/source.json"), p)
	b := replayRead(t, filepath.Join(root, "qualification/source.json"))
	k.Files["qualification/source.json"] = runner.Artifact{SHA256: runner.Digest(b), Bytes: int64(len(b))}
	replayJSON(t, filepath.Join(root, "kit.json"), k)
	// VerifyCandidate hashes this envelope; rearchive after extending the kit.
	cmd := exec.Command("tar", "-C", root, "-czf", filepath.Join(filepath.Dir(root), "claude-live-kit.tar.gz"), ".")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("unit kit archive: %v %s", err, b)
	}
	if _, err := runner.VerifyKit(root); err != nil {
		t.Fatal(err)
	}
	return root
}

func newSetupReplay(t *testing.T, kit string, tc replayCase) *setupReplay {
	t.Helper()
	scope := t.TempDir()
	home := filepath.Join(scope, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	phase, err := q.FindPhase(tc.phase)
	if err != nil {
		t.Fatal(err)
	}
	r, err := runner.NewRunAtForTest(scope, home, kit, filepath.Join(scope, "evidence"), tc.phase, phase.Turns)
	if err != nil {
		t.Fatal(err)
	}
	s := &setupReplay{t: t, r: r, tc: tc, clock: r.Manifest.Started, base: r.Manifest.Started.Add(20 * time.Millisecond),
		stop: errors.New("replay planned first-input boundary"), emitted: map[int]bool{}, ledger: filepath.Join(scope, "ledger.json")}
	replayDecode(t, filepath.Join(kit, "kit.json"), &s.kit)
	replayDecode(t, filepath.Join(kit, "qualification/source.json"), &s.product)
	fixture := qualificationfix.New(t, tc.phase)
	var capturedGet struct {
		Capabilities json.RawMessage `json:"delivery_capabilities"`
	}
	fixture.Decode("raw/commands/0024.stdout", &capturedGet)
	s.capabilities = capturedGet.Capabilities
	r.Manifest.RunID = fixture.Manifest.RunID
	r.Now = func() time.Time {
		if tc.realClock {
			return time.Now().UTC()
		}
		return s.clock
	}
	r.Commands.Now, r.Commands.Execute = r.Now, s.execute
	r.Sleep = s.sleep
	s.settings = []byte("{\"unit_preserve\":true,\"hooks\":{}}\n")
	replayWrite(t, filepath.Join(home, ".claude/settings.json"), s.settings)
	for _, component := range []string{"agent-director", "agent-director-admin"} {
		dir := "bin"
		if component == "agent-director-admin" {
			dir = "admin"
		}
		replayWrite(t, filepath.Join(home, ".config/agent-director", dir, component), replayRead(t, filepath.Join(kit, "payload", component+"-linux-"+runtime.GOARCH)))
	}
	replayWrite(t, filepath.Join(home, ".config/agent-director/ad-trail.jsonl"), nil)
	if _, err := apitest.InitStore(s.db()); err != nil {
		t.Fatal(err)
	}
	tools := t.TempDir()
	// LookPath is real. Even a mistaken fallback can only execute this refusal,
	// never a provider. No Execute case below delegates to runner.Exec.
	for _, tool := range []string{"claude", "tmux", "sqlite3", "bash", "cp", "jq"} {
		p := filepath.Join(tools, tool)
		replayWrite(t, p, []byte("#!/bin/sh\nexit 99\n"))
		if err := os.Chmod(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	id := q.Identity{Run: r.Manifest.RunID, Attempt: "synthetic-replay", Candidate: s.kit.BaseCommit + "/" + s.kit.SnapshotTree,
		Product: s.product.Product, Provider: fixture.Manifest.ProviderVersion, Kit: runner.Digest(replayRead(t, filepath.Join(filepath.Dir(kit), "claude-live-kit.tar.gz")))}
	l, err := q.NewLedger(id, "full", q.SignalCharge{})
	if err != nil {
		t.Fatal(err)
	}
	l.Selected = []string{tc.phase}
	if len(tc.dialogs) > 0 {
		l.DialogCases = tc.dialogs
	}
	replayJSON(t, s.ledger, l)
	s.options.Ledger = s.ledger
	if tc.fault != "missing-prerequisite" {
		var phases []string
		if tc.phase == "groups" {
			phases = []string{"force-states"}
		}
		if tc.phase == "text-limits-backlog" {
			phases = []string{"force-states", "typed-controls", "groups"}
		}
		for _, phase := range phases {
			s.options.Imports = append(s.options.Imports, s.prerequisite(phase, l))
		}
	}
	if tc.schedule != "" {
		var recorded struct {
			Events []replayRecordedEvent `json:"events"`
		}
		replayDecode(t, "testdata/qualification-replay-schedules.json", &recorded)
		for _, e := range recorded.Events {
			if e.Attempt == tc.schedule {
				s.events = append(s.events, e)
			}
		}
		if len(s.events) == 0 {
			t.Fatal("missing recorded schedule")
		}
		if tc.reverseHooks {
			// Reverse parent roles, preserving the measured offsets. The reverse
			// order is synthetic; original fields stay in the audit unchanged.
			for i := range s.events {
				if s.events[i].Role == "ad" {
					s.events[i].Role = "control"
				} else {
					s.events[i].Role = "ad"
				}
			}
		}
	}
	t.Cleanup(func() {
		for _, p := range s.parents {
			s.reap(p)
		}
	})
	return s
}

// Adapt and seal a wholly passing synthetic producer. Fresh analysis is a
// prerequisite of the replay itself, never a claim of live qualification.
func (s *setupReplay) prerequisite(phase string, ledger q.Ledger) string {
	s.t.Helper()
	f := qualificationfix.New(s.t, phase)
	f.Manifest.Candidate, f.Manifest.KitSHA256, f.Manifest.ProviderVersion = ledger.Identity.Candidate, ledger.Identity.Kit, ledger.Identity.Provider
	f.Manifest.RecordingHook.SHA256 = s.kit.Files["bin/claude-live"].SHA256
	ledger.Selected = []string{phase}
	ledger.Charges = nil
	f.Put("raw/qualification/source.json", s.product)
	var proof runner.CandidateEvidence
	f.Decode("raw/candidate-installed.json", &proof)
	proof.Kit, proof.KitSHA256, proof.ProviderVersion = s.kit, ledger.Identity.Kit, ledger.Identity.Provider
	f.Put("raw/candidate-installed.json", proof)
	for _, ref := range []string{proof.RuntimeCommand, proof.AdminCommand} {
		f.Put(ref, map[string]string{"version": s.kit.Version, "commit": s.kit.CommitStamp})
	}
	s.completePrerequisite(f, ledger)
	f.Reindex()
	if _, err := q.LoadCapture(f.Root); err != nil {
		s.t.Fatalf("sealed synthetic prerequisite: %v", err)
	}
	if v := q.AnalyzePhase(f.Root); v.Status != "pass" {
		var logProblems func([]runner.Result)
		logProblems = func(rows []runner.Result) {
			for _, row := range rows {
				if row.Status != "pass" {
					s.t.Logf("prerequisite %s %s: %s", row.SignalID, row.Status, row.Reason)
				}
				if nested, ok := row.Observed.([]runner.Result); ok {
					logProblems(nested)
				}
			}
		}
		logProblems(v.Results)
		s.t.Fatalf("synthetic %s prerequisite must wholly pass fresh analysis: %s", phase, v.Status)
	}
	if s.tc.fault == "incomplete-prerequisite" {
		f.Manifest.Problems = []runner.Result{{SignalID: "workload/missing-next-tool", Status: "not_completed", Reason: "synthetic prerequisite ended before its next tool", Evidence: []string{"manifest.json"}}}
		f.Reindex()
		if v := q.AnalyzePhase(f.Root); v.Status != "not_completed" {
			s.t.Fatalf("negative prerequisite must be sealed and incomplete: %+v", v)
		}
	}
	return f.Root
}

func (s *setupReplay) db() string { return filepath.Join(s.r.Home, ".config/agent-director/state.db") }

func (s *setupReplay) rawDB() (*sql.DB, error) {
	dsn := fmt.Sprintf("%s?_pragma=busy_timeout(%d)", s.db(), store.DefaultBusyTimeoutMs)
	if s.openRaw != nil {
		return s.openRaw(dsn)
	}
	return sql.Open("sqlite", dsn)
}

func (s *setupReplay) sql(query string, args ...any) {
	s.t.Helper()
	db, err := s.rawDB()
	if err != nil {
		s.t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(query, args...); err != nil {
		s.t.Fatal(err)
	}
}

func replayArg(argv []string, key string) string {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == key {
			return argv[i+1]
		}
	}
	return ""
}

func (s *setupReplay) parent(id string) *replayParent {
	for _, p := range s.parents {
		if p.id == id || p.role == id {
			return p
		}
	}
	s.t.Fatalf("unowned intercepted target %q", id)
	return nil
}

func (s *setupReplay) spawn(argv []string, native bool) *replayParent {
	s.t.Helper()
	id, cwd, prompt := replayArg(argv, "--instance-id"), replayArg(argv, "--cwd"), ""
	if native {
		id, cwd = replayArg(argv, "-s"), replayArg(argv, "-c")
	}
	sep := slices.Index(argv, "--")
	if sep < 0 || !strings.Contains(strings.Join(argv[sep+1:], " "), "claude-sonnet-5-5") {
		s.t.Fatal("invalid production startup argv")
	}
	if last := argv[len(argv)-1]; last != "medium" && !strings.HasPrefix(last, "--") {
		prompt = last
	}
	if s.tc.phase == "force-states" {
		prompt = ""
	}
	role := "ad"
	if strings.HasSuffix(id, "typed-control-start") {
		role = "control"
	}
	p := &replayParent{id: id, role: role, cwd: cwd, session: "unit-" + role + "-session", token: fmt.Sprintf("%016x", len(s.parents)+1), prompt: prompt}
	if native {
		p.session = replayArg(argv, "--session-id")
	}
	// A real, harmless owned process makes cleanup's PID/start checks meaningful.
	// Outlive the phase plus cleanup; the replay explicitly reaps this process.
	p.child = exec.Command("/bin/sleep", fmt.Sprint(int((runner.PhaseLimit+runner.CleanupReserve)/time.Second)))
	if err := p.child.Start(); err != nil {
		s.t.Fatal(err)
	}
	start, alive, known := probe.NewProcChecker().StartTime(p.child.Process.Pid)
	if !known || !alive {
		s.t.Fatal("unit child's start identity unavailable")
	}
	p.start = start
	s.parents = append(s.parents, p)
	p.transcript = apitest.SeedJsonlUnder(s.t, filepath.Join(s.r.Home, ".claude"), cwd, p.session)
	// No model requests or provider acceptance are fabricated.
	replayWrite(s.t, p.transcript, nil)
	if !native {
		_, err := apitest.SeedSpawn(s.db(), id, "pending", cwd, replayArg(argv, "--relay-mode"), "", false,
			apitest.WithPID(p.child.Process.Pid), apitest.WithProcStarttime(p.start), apitest.WithLaunchStartedAt(s.r.Now().UnixMilli()),
			apitest.WithLaunchIdentity(store.LaunchIdentity{Token: p.token, Socket: s.r.Socket}))
		if err != nil {
			s.t.Fatal(err)
		}
	}
	if s.workload != nil {
		s.workload.spawn(p, native)
		return p
	}
	if s.tc.phase == "typed-controls" {
		if len(s.parents) == 2 {
			s.clock = s.base.Add(s.events[1].Received.Sub(s.events[0].Received))
			first := s.parent("ad")
			if s.tc.siblingFirst {
				first = s.parent("control")
			}
			s.bind(first, false)
			if s.tc.lateCleanup {
				// This cleanup variant changes only database binding cadence;
				// the primary a4 case keeps its unobserved sibling regression.
				s.bind(s.parent("control"), false)
			}
			s.publish()
		}
	} else if s.tc.phase != "force-states" {
		s.bind(p, native)
		if s.tc.receipt == "late" {
			// R11-2 integration: product/native identity exists, but the
			// independent passive recorder has not published any own receipt.
			s.receiptPending = true
		} else {
			s.synthetic(p, "SessionStart", nil)
		}
		if !native && s.tc.receipt == "partial" {
			// R11-3 integration: a valid SS already binds this parent while
			// its first tool's recorder invocation is visibly mid-write.
			name := "invocation-synthetic-ad-PreToolUse"
			s.hook(p, "PreToolUse", s.r.Now(), name, s.firstTool(p), false)
			replayWrite(s.t, filepath.Join(s.r.Evidence.Root, "raw/hooks", name, "receipt.json"), []byte(`{"schema":1,"run_id":`))
			s.receiptPending = true
		} else if !native && s.tc.receipt != "late" && s.tc.phase != "dialogs" {
			s.synthetic(p, "PreToolUse", s.firstTool(p))
		}
	}
	if s.tc.phase == "dialogs" {
		if replayArg(argv, "--permission-mode") != "default" {
			s.t.Fatal("missing Dialog permission mode")
		}
		if _, err := os.Stat(filepath.Join(cwd, ".claude/settings.local.json")); err != nil {
			s.t.Fatal(err)
		}
		origin := "main"
		if strings.HasSuffix(id, "D5") {
			origin = "subagent"
		}
		if strings.HasSuffix(id, "D6") {
			origin = "background"
		}
		target := filepath.Join(cwd, "qd-"+origin+".txt")
		gate := runner.Row{"session_id": p.session, "tool_use_id": "synthetic-dialog-" + origin, "tool_name": "Bash", "tool_input": runner.Row{"command": "printf qualification > " + target}}
		if origin != "main" {
			gate["agent_id"] = "synthetic-dialog-child"
			var transcript bytes.Buffer
			for _, row := range replayDialogChild(p, origin, target, s.r.Now()) {
				if err := json.NewEncoder(&transcript).Encode(row); err != nil {
					s.t.Fatal(err)
				}
			}
			replayWrite(s.t, p.transcript, transcript.Bytes())
		}
		replayJSON(s.t, filepath.Join(s.r.Evidence.Root, "raw/gates/invocation-unit/input.json"), gate)
	}
	return p
}

// Labelled synthetic native child launch facts, shared by both Dialog replays.
func replayDialogChild(p *replayParent, origin, target string, at time.Time) []runner.Row {
	request := "synthetic-dialog-parent-request-" + origin
	return []runner.Row{
		{"type": "api-request", "sessionId": p.session, "id": request, "querySource": "repl_main_thread", "timestamp": at, "keep": 0, "tail": []string{}},
		{"type": "assistant", "sessionId": p.session, "requestRef": request, "apiBlockIndex": 0, "timestamp": at, "message": runner.Row{"id": "synthetic-dialog-parent-response-" + origin, "role": "assistant", "stop_reason": "tool_use", "content": []runner.Row{{"type": "tool_use", "id": "synthetic-dialog-agent-" + origin, "name": "Agent", "input": runner.Row{"prompt": "Run Bash exactly: printf qualification > " + target, "run_in_background": origin == "background"}}}}},
	}
}

func (s *setupReplay) bind(p *replayParent, native bool) {
	p.bound = true
	if native {
		return
	}
	state := "working"
	if s.tc.phase == "force-states" {
		state = "waiting"
	}
	s.sql("UPDATE spawns SET session_id=?,state=? WHERE instance_id=?", p.session, state, p.id)
	if !s.tc.pathLate {
		s.sql("UPDATE spawns SET jsonl_path=? WHERE instance_id=?", p.transcript, p.id)
	}
	// SeedSpawn created the actual current-schema Launch; only publish its
	// scripted readiness route, leaving all identity joins in production reads.
	s.sql("UPDATE busy_send_launches SET wake_path=? WHERE instance_id=?", filepath.Join(filepath.Dir(s.r.Socket), "unit-wake"), p.id)
}

func (s *setupReplay) hook(p *replayParent, event string, at time.Time, name string, extra runner.Row, complete bool) {
	input := runner.Row{"hook_event_name": event, "session_id": p.session, "cwd": p.cwd, "transcript_path": p.transcript}
	for k, v := range extra {
		input[k] = v
	}
	if event == "UserPromptSubmit" {
		input["prompt_id"] = name
		input["prompt"] = p.prompt
	}
	if s.tc.fault == "foreign-session" && event == "SessionStart" && p.role == "control" {
		input["session_id"] = "foreign-session"
	}
	if s.tc.fault == "foreign-cwd" && event == "SessionStart" && p.role == "control" {
		input["cwd"] = filepath.Join(p.cwd, "foreign")
	}
	b, err := json.Marshal(input)
	if err != nil {
		s.t.Fatal(err)
	}
	dir := filepath.Join(s.r.Evidence.Root, "raw/hooks", name)
	replayWrite(s.t, filepath.Join(dir, "input.json"), b)
	if complete {
		digest := runner.Digest(b)
		if s.tc.fault == "malformed-receipt" && p.role == "control" {
			digest = runner.Digest(nil)
		}
		replayJSON(s.t, filepath.Join(dir, "receipt.json"), runner.Receipt{Schema: runner.Schema, RunID: s.r.Manifest.RunID, Event: event, Received: at, Finished: at.Add(time.Microsecond), Input: "input.json", SHA256: digest, Bytes: len(b), Complete: true})
	}
}

func (s *setupReplay) synthetic(p *replayParent, event string, extra runner.Row) {
	name := fmt.Sprintf("invocation-synthetic-%s-%s", p.role, event)
	s.hook(p, event, s.r.Now(), name, extra, true)
	if event == "PreToolUse" && (s.tc.variant == "N8" || s.tc.phase == "groups") {
		// The separate blocking gate receives this same parent invocation.
		dir := filepath.Join(s.r.Evidence.Root, "raw/gates/invocation-unit")
		replayWrite(s.t, filepath.Join(dir, "input.json"), replayRead(s.t, filepath.Join(s.r.Evidence.Root, "raw/hooks", name, "input.json")))
		s.gateDriver(p, dir)
	}
}

func (s *setupReplay) gateDriver(p *replayParent, dir string) {
	command := exec.Command("/bin/sleep", "600")
	if err := command.Start(); err != nil {
		s.t.Fatal(err)
	}
	start, alive, known := probe.NewProcChecker().StartTime(command.Process.Pid)
	if !alive || !known {
		s.t.Fatal("gate driver start identity unavailable")
	}
	driver := replayGateDriver{command: command, start: start, done: make(chan struct{})}
	go func() { _ = command.Wait(); close(driver.done) }()
	s.drivers = append(s.drivers, driver)
	s.t.Cleanup(func() {
		_ = command.Process.Kill()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := driver.wait(ctx); err != nil {
			s.t.Errorf("gate driver cleanup: %v", err)
		}
	})
	replayJSON(s.t, filepath.Join(dir, "driver.json"), runner.Row{"pid": command.Process.Pid, "process_start": start, "run": s.r.Manifest.RunID, "instance": p.id, "session": p.session, "cwd": p.cwd, "input_sha256": runner.Digest(replayRead(s.t, filepath.Join(dir, "input.json")))})
}

func (s *setupReplay) assertDriversStopped() {
	s.t.Helper()
	for _, driver := range s.drivers {
		gone, err := runner.ProcessGone(driver.command.Process.Pid, driver.start)
		if err != nil || !gone {
			s.t.Errorf("canonical cleanup retained exact gate driver: %v", err)
		}
	}
}

func (s *setupReplay) firstTool(p *replayParent) runner.Row {
	input := runner.Row{"command": workloadShell(p.prompt, 0), "run_in_background": false}
	if s.tc.phase == "groups" {
		input["timeout"] = 600000
	}
	return runner.Row{"tool_name": "Bash", "tool_use_id": "synthetic-" + p.role + "-tool-1", "is_background": false, "tool_input": input}
}

func (s *setupReplay) publish() {
	if len(s.parents) < 2 {
		return
	}
	for i, e := range s.events {
		at := s.base.Add(e.Received.Sub(s.events[0].Received))
		if s.clock.Before(at) || s.emitted[i] {
			continue
		}
		// Lexical enumeration is deliberately independent of received order.
		name := fmt.Sprintf("invocation-%04d", len(s.events)-i)
		complete := !(s.tc.receiptLag && e.Event == "SessionStart" && e.Role == "control" && s.polls < 2) && !(s.tc.fault == "incomplete-receipt" && e.Role == "control")
		if e.Event == "PreToolUse" && len(e.ToolFacts) == 0 {
			s.t.Fatal("recorded PreToolUse lacks source tool facts")
		}
		s.hook(s.parent(e.Role), e.Event, at, name, e.ToolFacts, complete)
		s.emitted[i] = complete || s.tc.fault == "incomplete-receipt"
	}
	if s.polls >= 2 && s.tc.schedule == "a4" && s.tc.fault != "never-binding" {
		// a4 ended before any tool. These two explicit synthetic tool callbacks
		// extend only its recorded startup schedule to the planned input cutoff.
		for _, p := range s.parents {
			if !s.emitted[100+len(p.role)] {
				s.synthetic(p, "PreToolUse", s.firstTool(p))
				s.emitted[100+len(p.role)] = true
			}
		}
	}
}

func (s *setupReplay) reap(p *replayParent) {
	if p.exited {
		return
	}
	_ = p.child.Process.Kill()
	_ = p.child.Wait()
	p.exited = true
}

func (s *setupReplay) sleep(ctx context.Context, d time.Duration) error {
	if s.workload != nil {
		return s.workload.sleep(ctx, d)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.clock = s.clock.Add(d)
	if s.cleaning {
		return nil
	}
	s.polls++
	if s.stopped {
		return s.stop
	}
	if s.tc.fault == "cancelled-binding" {
		s.cancel()
		return context.Canceled
	}
	if s.polls > 65 {
		return errors.New("replay logical binding timeout (poll limit)")
	}
	if s.receiptPending {
		p := s.parents[0]
		if s.tc.receipt == "late" {
			s.synthetic(p, "SessionStart", nil)
		}
		if s.tc.phase != "text-limits-backlog" {
			s.synthetic(p, "PreToolUse", s.firstTool(p))
		}
		s.receiptPending, s.receiptCompleted = false, true
	}
	if s.tc.phase == "typed-controls" {
		for _, p := range s.parents {
			if !p.bound && !(s.tc.fault == "never-binding" && p.role == "control") {
				s.bind(p, false)
			}
		}
		s.publish()
		if s.tc.pathLate && s.polls >= 3 {
			for _, p := range s.parents {
				s.sql("UPDATE spawns SET jsonl_path=? WHERE instance_id=?", p.transcript, p.id)
			}
		}
		if s.tc.fault == "extra-start" && s.polls == 2 {
			s.sql("UPDATE spawns SET turn_starts=99 WHERE instance_id=?", s.parents[0].id)
		}
	} else if s.tc.phase == "force-states" && len(s.parents) > 0 && !s.parents[0].bound {
		s.bind(s.parents[0], false)
		s.synthetic(s.parents[0], "SessionStart", nil)
	}
	if s.tc.phase == "text-limits-backlog" && s.tc.variant != "N8" {
		var plan q.NativePlan
		replayDecode(s.t, filepath.Join(s.r.Evidence.Root, "raw/native-wake-plan.json"), &plan)
		pointer, watch := string(replayRead(s.t, plan.Pointer)), string(replayRead(s.t, plan.Watch))
		if pointer == "" && watch == "" && s.tc.receipt == "late" {
			// The first wait belongs to startNative's real SessionStart gate.
			// Only its later N1 measurement wait is the planned input cutoff.
			return nil
		}
		if pointer != "N1" || watch != "N1\n" {
			s.t.Fatal("native input intent was not published")
		}
		s.stopped = true
		return s.stop
	}
	return nil
}

func (s *setupReplay) execute(ctx context.Context, argv, _ []string, out, _ io.Writer) (int, error) {
	write := func(v any) (int, error) { return 0, json.NewEncoder(out).Encode(v) }
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	if s.workload != nil {
		if handled, code, err := s.workload.command(ctx, argv, out); handled {
			return code, err
		}
	}
	if reflect.DeepEqual(argv, []string{"claude", "auth", "status", "--json"}) {
		return write(runner.Row{"loggedIn": true, "subscriptionType": "synthetic-command-seam"})
	}
	if reflect.DeepEqual(argv, []string{"claude", "--version"}) {
		var l q.Ledger
		replayDecode(s.t, s.ledger, &l)
		_, err := io.WriteString(out, l.Identity.Provider+"\n")
		return 0, err
	}
	if len(argv) == 2 && argv[1] == "version" && (argv[0] == filepath.Join(s.r.Home, ".config/agent-director/bin/agent-director") || argv[0] == filepath.Join(s.r.Home, ".config/agent-director/admin/agent-director-admin")) {
		return write(runner.Row{"version": s.kit.Version, "commit": s.kit.CommitStamp})
	}
	if len(argv) > 3 && argv[0] == "tmux" {
		if argv[1] != "-S" || argv[2] != s.r.Socket {
			return -1, errors.New("foreign tmux socket")
		}
		switch argv[3] {
		case "new-session":
			s.inputs = append(s.inputs, append([]string(nil), argv...))
			p := s.spawn(argv, true)
			_, err := fmt.Fprintf(out, "$unit%d %d\n", len(s.parents), p.child.Process.Pid)
			return 0, err
		case "set-option":
			return 0, nil
		case "show-option":
			s.cleaning = true
			_, err := io.WriteString(out, s.r.Manifest.RunID+"\n")
			return 0, err
		case "kill-session":
			s.reap(s.parents[0])
			return 0, nil
		case "send-keys":
			if s.tc.phase != "typed-controls" || s.stopped {
				return -1, errors.New("unexpected typed input")
			}
			s.inputs = append(s.inputs, append([]string(nil), argv...))
			if argv[len(argv)-1] == "Enter" {
				s.stopped = true
			}
			return 0, nil
		}
	}
	if len(argv) < 2 || argv[0] != filepath.Join(s.r.Home, ".config/agent-director/bin/agent-director") {
		return -1, fmt.Errorf("unhandled external command %q", argv)
	}
	switch argv[1] {
	case "spawn":
		s.inputs = append(s.inputs, append([]string(nil), argv...))
		p := s.spawn(argv, false)
		return write(runner.Row{"instance_id": p.id})
	case "get":
		if s.tc.lateCleanup && !s.cleaning && !s.stopped {
			// a4's first cleanup command was 18.903877ms after AD SS.
			// Stop after readiness, before an exercise input or capability result.
			s.clock = s.base.Add(18903877 * time.Nanosecond)
			s.cleanupStarted = s.clock
			s.stopped = true
			return -1, s.stop
		}
		return write(runner.Row{"delivery_capabilities": s.capabilities})
	case "read-pane":
		s.cleaning = s.stopped || !strings.Contains(strings.Join(argv, " "), "--n-lines 100")
		if s.tc.lateCleanup && replayArg(argv, "--instance-id") == s.parent("control").id {
			// Publish the recorded control UPS only after AD was really
			// stopped/reaped, while the separately owned control is still live.
			if !s.parent("ad").exited || s.parent("control").exited {
				return -1, errors.New("late UPS did not land between the two owned cleanup stops")
			}
			for _, event := range s.events {
				if event.Event == "UserPromptSubmit" && event.Role == "control" {
					s.clock = s.base.Add(event.Received.Sub(s.events[0].Received))
				}
			}
			s.publish()
			s.sql("UPDATE spawns SET turn_starts=1 WHERE instance_id=?", s.parent("control").id)
			s.cleanupUPS = true
		}
		return 0, nil
	case "kill":
		s.cleaning = true
		p := s.parent(replayArg(argv, "--instance-id"))
		s.reap(p)
		s.sql("UPDATE spawns SET state='ended' WHERE instance_id=?", p.id)
		return 0, nil
	case "force-delivery":
		if s.tc.phase != "force-states" || len(s.inputs) != 2 {
			return -1, errors.New("unexpected force")
		}
		return write(runner.Row{"force_result": runner.Row{"force_id": "unit-not-started", "outcome": "refused", "reason": "agent_starting"}})
	case "send-keys":
		if s.stopped {
			return -1, errors.New("unexpected input after planned cutoff")
		}
		if s.receiptPending {
			return -1, errors.New("exercise input crossed the unpublished recorder receipt boundary")
		}
		if s.tc.phase == "groups" {
			if len(s.drivers) != 1 {
				s.t.Fatal("groups-G0 first input preceded its independent gate driver")
			}
			driver := s.drivers[0]
			gone, er := runner.ProcessGone(driver.command.Process.Pid, driver.start)
			if er != nil || gone {
				s.t.Fatalf("groups-G0 first input lacked a live gate: gone=%t err=%v", gone, er)
			}
			var held runner.Row
			replayDecode(s.t, filepath.Join(s.r.Evidence.Root, "raw/gates/invocation-unit/input.json"), &held)
			input, _ := held["tool_input"].(map[string]any)
			if runner.Number(input, "timeout") != 600000 {
				s.t.Fatal("groups-G0 gate did not retain requested timeout")
			}
		}
		s.inputs = append(s.inputs, append([]string(nil), argv...))
		if s.tc.phase == "force-states" && !slices.Contains(argv, "--force") {
			p := s.parent(replayArg(argv, "--instance-id"))
			mid := storefix.SeedBusySendResult(s.t, s.db(), storefix.BusySendResultSeed{InstanceID: p.id, Pair: store.BusySendPair{CallerUUID: s.r.Manifest.RunID, DeliveryRequestID: "F1"}, Class: store.BusySendResultClass("held")})
			text := replayArg(argv, "--text")
			s.sql("UPDATE busy_send_messages SET text=?,text_bytes=?,text_sha256=? WHERE message_id=?", []byte(text), len(text), runner.Digest([]byte(text)), mid)
			return 0, nil
		}
		if s.tc.phase == "typed-controls" {
			return 0, nil
		}
		s.stopped = true
		if s.tc.variant == "N8" {
			// Keep the deliberate command refusal distinct from a JSON decoder
			// error, without inventing a Held/admission result for the member.
			if _, err := io.WriteString(out, "{}\n"); err != nil {
				return -1, err
			}
		}
		return -1, s.stop
	}
	return -1, fmt.Errorf("unhandled product command %q", argv)
}

func (s *setupReplay) run() {
	started := time.Now()
	if err := s.r.Evidence.JSON("raw/replay-projection.json", runner.Row{"basis": s.tc.schedule, "recorded_events": s.events, "rebased_start": s.base, "reverse_order_is_synthetic": s.tc.reverseHooks, "late_cleanup": s.tc.lateCleanup, "receipt_schedule": s.tc.receipt, "scope": "synthetic callback boundary; no provider or product writer"}); err != nil {
		s.t.Fatal(err)
	}
	var phaseErr error
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.cancel = cancel
	v, err := q.Execute(parent, s.r, func(ctx context.Context, r *runner.Run) error {
		if s.tc.variant == "N7" {
			// Fresh production N7 startup, without fabricating earlier native
			// wake measurements to reach it. Its planned input is the spawn argv.
			phaseErr = q.StartExercise(ctx, r, s.ledger, func(ctx context.Context, e *q.Exercise) error {
				if _, err := q.StartNativeSteer(ctx, e); err != nil {
					return err
				}
				s.stopped = true
				return s.stop
			})
		} else if s.tc.variant == "N8" {
			// A fresh bounded variant of the public production backlog helper.
			// The primary N1 case separately requires Dispatch/import retention.
			phaseErr = q.StartExercise(ctx, r, s.ledger, q.LargeBacklog)
		} else {
			phaseErr = q.Dispatch(ctx, r, s.options)
		}
		var rows []runner.Snapshot
		for _, p := range s.parents {
			if s.tc.phase == "text-limits-backlog" && s.tc.variant != "N8" {
				continue
			}
			snap, er := r.Observe(context.Background(), p.id)
			if er != nil {
				s.t.Error(er)
			} else {
				rows = append(rows, snap)
			}
		}
		if er := r.Evidence.JSON("raw/replay-boundary.json", runner.Row{"manifest": r.Manifest, "snapshots": rows, "stopped": s.stopped}); er != nil {
			s.t.Error(er)
		}
		s.cleaning = true
		return phaseErr
	}, nil)
	s.t.Logf("replay %s execution=%s polls=%d phase=%v problems=%+v", s.tc.name, time.Since(started), s.polls, phaseErr, s.r.Manifest.Problems)
	if err != nil {
		s.t.Fatal(err)
	}
	if s.tc.fault == "" && !errors.Is(phaseErr, s.stop) {
		s.t.Errorf("producer stopped before the planned first-input boundary: %v", phaseErr)
	}
	if s.tc.fault != "" && s.tc.fault != "incomplete-receipt" {
		if phaseErr == nil || s.stopped {
			s.t.Errorf("negative did not stop before input: %v", phaseErr)
		}
		if (s.tc.fault == "missing-prerequisite" || s.tc.fault == "incomplete-prerequisite") && (len(s.parents) != 0 || s.r.Manifest.TurnsReserved != 0 || len(s.inputs) != 0) {
			s.t.Error("rejected prerequisite spent a start/reservation/input")
		}
		if s.tc.fault == "incomplete-prerequisite" && (phaseErr == nil || phaseErr.Error() != "phase import prerequisite not_completed: force-states") {
			s.t.Errorf("wrong incomplete prerequisite refusal: %v", phaseErr)
		}
		if s.tc.fault == "extra-start" && !errors.Is(phaseErr, runner.ErrTurnBudget) {
			s.t.Errorf("wrong extra-start refusal: %v", phaseErr)
		}
		if s.tc.fault == "cancelled-binding" && !errors.Is(phaseErr, context.Canceled) {
			s.t.Errorf("wrong cancellation: %v", phaseErr)
		}
		if s.tc.fault == "never-binding" && (phaseErr == nil || !strings.Contains(phaseErr.Error(), "replay logical binding timeout")) {
			s.t.Errorf("wrong bounded binding timeout: %v", phaseErr)
		}
	}
	if (s.tc.fault == "incomplete-receipt" || s.tc.fault == "malformed-receipt") && !slices.ContainsFunc(s.r.Manifest.Problems, func(p runner.Result) bool { return p.SignalID == "recording-hook-incomplete" && p.Status == "fail" }) {
		s.t.Error("malformed/incomplete recorder input escaped final inventory")
	}
	if v.Status != "not_completed" && s.tc.fault == "" {
		s.t.Errorf("planned prefix verdict=%s; want not_completed", v.Status)
	}
	for _, p := range s.parents {
		if !p.exited {
			s.t.Errorf("canonical cleanup did not stop/reap owned child %s", p.role)
		}
	}
	for _, o := range s.r.Manifest.Owned {
		if !o.Exited {
			s.t.Errorf("runner did not verify exit for %s", o.InstanceID)
		}
	}
	s.assertDriversStopped()
	if _, err := os.Stat(filepath.Dir(s.r.Socket)); !os.IsNotExist(err) {
		s.t.Errorf("private runtime retained after cleanup: %v", err)
	}
	if !s.r.Manifest.RecordingHook.Removed {
		s.t.Error("recorder registration not removed")
	}
	var settings runner.Row
	replayDecode(s.t, filepath.Join(s.r.Home, ".claude/settings.json"), &settings)
	if settings["unit_preserve"] != true {
		s.t.Error("existing settings lost")
	}
	if s.r.Settings != nil {
		defer s.r.Settings.Close()
	}
	l, err := q.LoadLedger(s.ledger)
	if err != nil {
		s.t.Fatal(err)
	}
	for _, c := range l.Charges {
		if !c.Reconciled && s.tc.fault != "incomplete-receipt" && s.tc.fault != "malformed-receipt" {
			s.t.Errorf("unreconciled reservation %+v", c)
		}
	}
	if s.tc.fault == "" {
		for _, problem := range s.r.Manifest.Problems {
			if problem.SignalID != "phase" {
				s.t.Errorf("unexpected setup/cleanup problem: %+v", problem)
			}
		}
		s.assertInputs()
	}
	if s.tc.receipt != "" && (!s.receiptCompleted || s.receiptPending || s.polls == 0) {
		s.t.Error("integrated producer did not wait for the delayed/partial own receipt to complete")
	}
	if s.tc.lateCleanup {
		s.assertCleanupUPS(l)
	}
	// Verify the actual retained command and observation records are readable.
	f, err := os.Open(filepath.Join(s.r.Evidence.Root, "commands.jsonl"))
	if err != nil {
		s.t.Fatal(err)
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 4096), runner.MaxArtifactBytes)
	for scan.Scan() {
		var c runner.Command
		if err := json.Unmarshal(scan.Bytes(), &c); err != nil {
			s.t.Fatal(err)
		}
	}
	if err := scan.Err(); err != nil {
		s.t.Fatal(err)
	}
}

func (s *setupReplay) assertInputs() {
	s.t.Helper()
	var observations []q.Observation
	replayDecode(s.t, filepath.Join(s.r.Evidence.Root, "raw/qualification-observations.json"), &observations)
	switch s.tc.phase {
	case "typed-controls":
		if s.tc.lateCleanup {
			if len(s.inputs) != 2 || len(observations) != 2 {
				s.t.Error("late-UPS cleanup replay issued an extra exercise input")
			}
			break
		}
		var ad, control string
		for _, ob := range observations {
			if ob.Case == "typed-long-tool-ad-input" {
				ad = ob.Text
			}
			if ob.Case == "typed-long-tool-typed-input" {
				control = ob.Text
			}
		}
		if len(ad) != 8000 || ad != control || !strings.Contains(ad, "community garden") {
			s.t.Error("actual matched long-tool inputs absent/different")
		}
		if len(s.parents) != 2 || len(s.inputs) != 5 {
			s.t.Errorf("typed command prefix changed: parents=%d inputs=%d", len(s.parents), len(s.inputs))
		}
	case "force-states":
		if len(s.inputs) != 3 || replayArg(s.inputs[1], "--delivery-request-id") != "F1" || !slices.Contains(s.inputs[2], "--force") || replayArg(s.inputs[2], "--delivery-request-id") != "F1" || replayArg(s.inputs[1], "--text") != replayArg(s.inputs[2], "--text") {
			s.t.Error("F1 was not repeated once under the same forced confirmed pair")
		}
	case "groups":
		var plans []q.GroupPlan
		replayDecode(s.t, filepath.Join(s.r.Evidence.Root, "raw/group-plans.json"), &plans)
		if len(plans) != 2 || len(s.inputs) != 2 || !strings.Contains(strings.Join(s.inputs[0], " "), "release-first") || !strings.Contains(strings.Join(s.inputs[0], " "), "release-second") || !strings.Contains(strings.Join(s.inputs[0], " "), "release-ordinary") || len(s.drivers) != 1 {
			s.t.Error("three-call group plans/live-gate runtime prefix missing")
		}
	case "esc-gap":
		if len(s.inputs) != 2 || replayArg(s.inputs[1], "--delivery-request-id") != "Esc-steer-1" || !strings.Contains(strings.Join(s.inputs[0], " "), "release-E1-second-never") {
			s.t.Error("Esc runtime/first steer prefix missing")
		}
	case "dialogs":
		if len(s.inputs) != 2 || !strings.HasPrefix(replayArg(s.inputs[1], "--text"), "Qualification Dialog probe.") {
			s.t.Error("first about-to-open Dialog probe missing")
		}
		for _, p := range s.parents {
			if _, err := os.Stat(filepath.Join(p.cwd, ".claude/settings.local.json")); !os.IsNotExist(err) {
				s.t.Errorf("owned local fixture retained: %v", err)
			}
		}
	case "text-limits-backlog":
		if s.tc.variant == "N7" {
			var plan q.NativePlan
			replayDecode(s.t, filepath.Join(s.r.Evidence.Root, "raw/native-post_tool_batch-plan.json"), &plan)
			if plan.Route != "post_tool_batch" || len(plan.Rungs) != 5 || plan.Rungs[0].ID != "N7.1" || plan.Rungs[4].ID != "N7.5" {
				s.t.Error("N7 did not retain the actual five-rung native steer plan")
			}
			if len(s.parents) != 1 || len(s.r.Manifest.Owned) != 1 || !s.r.Manifest.Owned[0].Native || s.r.Manifest.TurnsReserved != 1 {
				s.t.Error("fresh N7 did not reserve exactly one owned native startup")
			}
			if len(s.inputs) != 1 || replayArg(s.inputs[0], "--session-id") != plan.Session || replayArg(s.inputs[0], "--setting-sources") != "" || filepath.Dir(replayArg(s.inputs[0], "--settings")) != filepath.Dir(s.r.Socket) {
				s.t.Fatal("N7 did not use its own native session and private runtime settings")
			}
			if len(s.r.Manifest.Cases) != 1 || s.r.Manifest.Cases[0].ID != "N7" || s.r.Manifest.Cases[0].Mode != "native" || s.r.Manifest.Cases[0].Text != s.inputs[0][len(s.inputs[0])-1] || !strings.Contains(s.r.Manifest.Cases[0].Text, "EXACTLY five separate foreground Bash calls") {
				s.t.Error("actual N7 startup workload was not captured once")
			}
			facts, err := q.HookFacts(s.r.Evidence.Root, s.r.Manifest.RunID, s.r.Manifest.RecordingHook.Events)
			if err != nil || len(facts) != 1 || facts[0].Receipt.Event != "SessionStart" || runner.Text(facts[0].Payload, "session_id") != plan.Session {
				s.t.Errorf("N7 prefix did not reach native SessionStart readiness: %v", err)
			}
			break
		}
		if s.tc.variant == "N8" {
			if len(s.inputs) != 2 || replayArg(s.inputs[1], "--delivery-request-id") != "backlog-0001" || len(replayArg(s.inputs[1], "--text")) != 1900 || !strings.Contains(strings.Join(s.inputs[0], " "), "release-never") {
				s.t.Error("bounded fresh N8 startup/first backlog member absent")
			}
			break
		}
		if len(s.parents) != 1 || !s.r.Manifest.Owned[0].Native {
			s.t.Error("native route incorrectly used an AD ready row")
		}
		if len(s.r.Manifest.Cases) != 1 || s.r.Manifest.Cases[0].ID != "N1" {
			s.t.Error("native N1 input intent absent")
		}
	}
}

func (s *setupReplay) assertCleanupUPS(ledger q.Ledger) {
	s.t.Helper()
	if !s.cleanupUPS || s.cleanupStarted.IsZero() {
		s.t.Fatal("recorded late UPS was not published during canonical cleanup")
	}
	facts, err := q.HookFacts(s.r.Evidence.Root, s.r.Manifest.RunID, s.r.Manifest.RecordingHook.Events)
	if err != nil {
		s.t.Fatal(err)
	}
	if len(facts) != 3 {
		s.t.Errorf("late cleanup retained %d hooks; want exactly a4's two SS and one UPS", len(facts))
	}
	var ups time.Time
	for _, fact := range facts {
		if fact.Receipt.Event == "UserPromptSubmit" {
			if runner.Text(fact.Payload, "session_id") != s.parent("control").session {
				s.t.Error("late UPS was assigned to the wrong parent")
			}
			ups = fact.Receipt.Received
		}
	}
	if ups.IsZero() || !ups.After(s.cleanupStarted) {
		s.t.Fatal("UPS receipt did not follow the cleanup boundary")
	}
	for _, owner := range s.r.Manifest.Owned {
		if owner.InstanceID == s.parent("ad").id && !owner.StoppedAt.Before(ups) {
			s.t.Error("AD cleanup stop did not precede control's late UPS")
		}
		if owner.InstanceID == s.parent("control").id && owner.StoppedAt.Before(ups) {
			s.t.Error("control's late UPS was published after its verified stop")
		}
	}
	accounting, err := q.AccountTurns(ledger)
	if err != nil || accounting.Actual != 1 {
		s.t.Errorf("late cleanup UPS was not reconciled exactly once: actual=%d err=%v", accounting.Actual, err)
	}
}

// R13-1..4 / a5: these are producer replays, not additional live evidence.
// The only recorded workload is a5. All other provider/product callbacks below
// are explicitly synthetic schedules; none supplies a qualification verdict.
func TestQualificationWorkloadReplay(t *testing.T) {
	kit := replayUnitKit(t)
	cases := []workloadCase{{name: "a5-recorded-Stop-fallback", phase: "typed-controls", kind: "a5", role: "ad", want: "early-Stop-fallback"}}
	for _, role := range []string{"ad", "native"} {
		for _, kind := range []string{"short-prose", "absent-prose", "excess-prose", "no-next-tool", "plain-question", "structured-question", "nontermination"} {
			want := "missed-prose-window"
			if kind == "no-next-tool" {
				want = "missing-next-tool|early-Stop-fallback|precondition-not-established"
			}
			if strings.Contains(kind, "question") {
				want = "precondition-not-established"
			}
			if kind == "nontermination" || kind == "excess-prose" {
				want = "nonterminating-model"
			}
			cases = append(cases, workloadCase{name: role + "-" + kind, phase: "typed-controls", kind: kind, role: role, want: want})
		}
		for _, steer := range []string{"long", "between"} {
			for _, side := range []string{"before", "after"} {
				cases = append(cases, workloadCase{name: role + "-Stop-" + side + "-" + steer + "-steer", phase: "typed-controls", kind: "early-Stop", role: role, at: side + "-" + steer, want: "early-Stop-fallback|precondition-not-established|missing-next-tool|missed-prose-window"})
			}
		}
	}
	cases = append(cases,
		workloadCase{name: "force-tool-finishes-before-return", phase: "force-states", kind: "force-ends", want: "precondition-not-established"},
		workloadCase{name: "dialog-origin-missing", phase: "dialogs", kind: "missing-origin", want: "different-origin"},
		workloadCase{name: "dialog-plan-missing", phase: "dialogs", kind: "missing-plan", want: "plan-not-present"},
		workloadCase{name: "G0-second-tool-missing", phase: "groups", kind: "missing-second", want: "missing-next-tool"},
		workloadCase{name: "N7-stops-after-rung-1", phase: "text-limits-backlog", kind: "native-rung-1", want: "insufficient-batches"},
		workloadCase{name: "E3-recovery-tools-missing", phase: "esc-gap", kind: "E3-missing", want: "missing-next-tool"},
		workloadCase{name: "F2-Stop-with-F3-Held", phase: "force-states", kind: "tail-early", want: "ordinary-early-F3"},
		workloadCase{name: "F3-pending-tail-reclaim", phase: "force-states", kind: "tail-control", control: true},
		workloadCase{name: "F3-ordinary-during-F2-sighting", phase: "force-states", kind: "tail-late-sighted", want: "ordinary-early-F3"},
		workloadCase{name: "F3-ordinary-with-unsighted-F2", phase: "force-states", kind: "tail-late-unsighted", want: "ordinary-early-F3"},
		workloadCase{name: "G0-Stop-after-S2-first-ordinary", phase: "groups", kind: "group-Stop-after-S2"},
		workloadCase{name: "G0-Stop-during-steer-admission", phase: "groups", kind: "group-Stop-during-steer"},
		workloadCase{name: "checked-refresh-Launch-replaced", phase: "typed-controls", kind: "launch-replaced", want: "owned-Launch-replaced"})
	for _, kind := range []string{"batch-two", "reverse-completion", "background", "combined", "extra", "serial"} {
		cases = append(cases, workloadCase{name: "tool-window-" + kind, phase: "groups", kind: "window-" + kind, want: "precondition-not-established", control: kind == "serial"})
	}
	for _, kind := range []string{"missing", "wrong", "background", "held-through-admission", "Stop-after-1", "Stop-after-24", "driver-failure", "cancel", "drain", "prompt-during-hold", "driver-exit"} {
		want := "backlog-not-exercised"
		if slices.Contains([]string{"held-through-admission", "driver-failure", "cancel"}, kind) {
			want = ""
		}
		cases = append(cases, workloadCase{name: "N8-" + kind, phase: "text-limits-backlog", kind: "backlog-" + kind, want: want, control: kind == "drain"})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := replayCase{name: tc.name, phase: tc.phase}
			if tc.phase == "dialogs" {
				base.dialogs = []string{"D1", "D2", "D3"}
				if tc.kind == "missing-origin" {
					base.dialogs = []string{"D4"}
				}
			}
			s := newSetupReplay(t, kit, base)
			w := &workloadReplay{s: s, tc: tc, clock: tmuxfix.NewClock(s.r.Manifest.Started), starts: map[string]int64{}, journal: map[string][]runner.Row{}, blobs: map[string]bool{}, messages: map[string]workloadMessage{}, calls: map[string][]string{}, typed: map[string]string{}}
			s.workload = w
			s.r.Now, s.r.Commands.Now = w.clock.Now, w.clock.Now
			w.run()
		})
	}
}

type workloadCase struct {
	name, phase, kind, role, at, want string
	control                           bool
}
type workloadMessage struct {
	parent              *replayParent
	id, text, mode, mid string
}
type workloadReplay struct {
	s                                                                                *setupReplay
	tc                                                                               workloadCase
	clock                                                                            *tmuxfix.Clock
	mu                                                                               sync.Mutex
	starts                                                                           map[string]int64
	journal                                                                          map[string][]runner.Row
	blobs                                                                            map[string]bool
	messages                                                                         map[string]workloadMessage
	calls                                                                            map[string][]string
	typed                                                                            map[string]string
	seq, polls, longInputs, betweenInputs, accepts, dialogOrigin, refusals           int
	ready, exercised, queued, tailReserved, tailReclaimed, stoppedEarly, holdMissing bool
	groupSecond                                                                      bool
	groupGates                                                                       map[string]string
	forceCalls                                                                       int
	canary                                                                           *exec.Cmd
	canaryStart                                                                      string
	a5                                                                               *workloadA5
	a5Base                                                                           time.Time
	// Repair 11 causal checkpoints; failures must not pass before these fire.
	ordinaryAccepts, inputsAtEvent                     int
	eventSeen, tailLate, groupDrained, refreshRejected bool
	injectedFault                                      error
	replacementBefore, replacementAfter                runner.Owned
}

type workloadA5 struct {
	Injection       time.Time    `json:"injection"`
	Frozen          string       `json:"frozen_input"`
	Journal         []runner.Row `json:"journal"`
	OriginalSession string       `json:"original_session"`
	Hooks           []struct {
		Event    string     `json:"event"`
		Received time.Time  `json:"received"`
		Payload  runner.Row `json:"payload"`
	} `json:"hooks"`
}

func (w *workloadReplay) a5Time(at time.Time) time.Time {
	return w.a5Base.Add(at.Sub(w.a5.Hooks[0].Received))
}
func (w *workloadReplay) a5Hook(p *replayParent, n int) {
	h := w.a5.Hooks[n]
	fields := runner.Row{}
	for k, v := range h.Payload {
		fields[k] = v
	}
	fields["session_id"], fields["cwd"], fields["transcript_path"] = p.session, p.cwd, p.transcript
	delete(fields, "scratchpad_dir")
	at := w.a5Time(h.Received)
	w.clock.Advance(max(time.Duration(0), at.Sub(w.clock.Now())))
	prior := p.prompt
	if h.Event == "UserPromptSubmit" {
		p.prompt = runner.Text(fields, "prompt")
	}
	w.s.hook(p, h.Event, at, fmt.Sprintf("invocation-a5-projected-%d", n), fields, true)
	p.prompt = prior
	if h.Event == "UserPromptSubmit" {
		w.starts[p.id]++
		w.s.sql("UPDATE spawns SET turn_starts=?,state='working' WHERE instance_id=?", w.starts[p.id], p.id)
	}
}
func (w *workloadReplay) a5Rows(p *replayParent, first, last int) {
	for _, original := range w.a5.Journal[first:last] {
		row := runner.Row{}
		for k, v := range original {
			row[k] = v
		}
		row["sessionId"] = p.session
		if _, ok := row["session_id"]; ok {
			row["session_id"] = p.session
		}
		if _, ok := row["cwd"]; ok {
			row["cwd"] = p.cwd
		}
		if stamp := runner.Text(row, "timestamp"); stamp != "" {
			at, err := time.Parse(time.RFC3339Nano, stamp)
			if err != nil {
				w.s.t.Fatal(err)
			}
			row["timestamp"] = w.a5Time(at)
		}
		w.journal[p.id] = append(w.journal[p.id], row)
	}
	w.flush(p)
}
func (w *workloadReplay) a5Fallback(p *replayParent, m workloadMessage) {
	if m.text != w.a5.Frozen {
		w.s.t.Fatal("a5 measured input changed; the recorded blob cannot prove the new input")
	}
	w.a5Rows(p, 1, 2)
	w.a5Hook(p, 2)
	w.s.sql("UPDATE spawns SET state='waiting' WHERE instance_id=?", p.id)
	w.deliver(m, "turn_end", "", true)
	w.a5Hook(p, 3)
	w.a5Rows(p, 2, 4)
	w.stoppedEarly = true
	w.exercised = true
}

func (w *workloadReplay) tick() time.Time { w.clock.Advance(time.Millisecond); return w.clock.Now() }
func (w *workloadReplay) hook(p *replayParent, event string, fields runner.Row) {
	w.seq++
	w.s.hook(p, event, w.tick(), fmt.Sprintf("invocation-workload-%05d", w.seq), fields, true)
}
func (w *workloadReplay) ups(p *replayParent, text string) {
	prior := p.prompt
	p.prompt = text
	w.hook(p, "UserPromptSubmit", nil)
	p.prompt = prior
	w.starts[p.id]++
	if !strings.Contains(p.id, "-native-") {
		w.s.sql("UPDATE spawns SET turn_starts=?,state='working' WHERE instance_id=?", w.starts[p.id], p.id)
	}
}
func (w *workloadReplay) pre(p *replayParent, id, command string, background bool) {
	w.calls[p.id] = append(w.calls[p.id], id)
	input := runner.Row{"command": command, "run_in_background": background}
	if w.tc.phase == "groups" {
		input["timeout"] = 600000
	}
	w.hook(p, "PreToolUse", runner.Row{"tool_name": "Bash", "tool_use_id": id, "tool_input": input})
	if w.tc.phase == "groups" {
		if _, er := os.Stat(filepath.Join(p.cwd, ".claude/settings.local.json")); er != nil {
			w.s.t.Fatal("G0 PreToolUse lacks registered gate fixture", er)
		}
		if w.groupGates == nil {
			w.groupGates = map[string]string{}
		}
		dir := filepath.Join(w.s.r.Evidence.Root, "raw/gates/group-"+id)
		w.groupGates[id] = dir
		replayWrite(w.s.t, filepath.Join(dir, "input.json"), replayRead(w.s.t, filepath.Join(w.s.r.Evidence.Root, "raw/hooks", fmt.Sprintf("invocation-workload-%05d", w.seq), "input.json")))
		w.s.gateDriver(p, dir)
	}
}
func (w *workloadReplay) post(p *replayParent, ids ...string) {
	var calls []runner.Row
	for _, id := range ids {
		calls = append(calls, runner.Row{"tool_use_id": id, "tool_name": "Bash"})
	}
	w.hook(p, "PostToolBatch", runner.Row{"tool_calls": calls})
}
func workloadShell(prompt string, n int) string {
	parts := strings.Split(prompt, "`")
	if 2*n+1 < len(parts) {
		return parts[2*n+1]
	}
	return "printf synthetic-foreground"
}

func (w *workloadReplay) spawn(p *replayParent, native bool) {
	w.s.cleaning = false
	if native && p.prompt == replayArg(w.s.inputs[len(w.s.inputs)-1], "--settings") {
		p.prompt = ""
	}
	w.s.bind(p, native)
	if w.tc.phase == "force-states" {
		w.s.sql("UPDATE spawns SET state='pending',session_id=NULL,jsonl_path=NULL WHERE instance_id=?", p.id)
		p.bound = false
		return
	}
	w.hook(p, "SessionStart", nil)
	if p.prompt != "" {
		w.ups(p, p.prompt)
	}
	if native {
		return
	}
	if w.tc.phase == "dialogs" {
		if w.tc.kind != "missing-origin" {
			w.gate(p)
		}
		return
	}
	if strings.HasPrefix(w.tc.kind, "backlog-") {
		switch w.tc.kind {
		case "backlog-missing":
			w.stop(p, "no driver invoked")
		case "backlog-wrong":
			w.pre(p, "N8-wrong", "printf wrong-driver", false)
		case "backlog-background":
			w.pre(p, "N8-background", workloadShell(p.prompt, 0), true)
		default:
			w.pre(p, "N8-hold", workloadShell(p.prompt, 0), false)
		}
		// A gate is provider-observed only when the real producer registered it.
		if _, err := os.Stat(filepath.Join(p.cwd, ".claude/settings.local.json")); err == nil {
			w.gate(p)
		}
		return
	}
	w.pre(p, "tool-1", workloadShell(p.prompt, 0), false)
	if w.tc.phase == "typed-controls" && w.target(p) && w.tc.at == "before-long" {
		w.post(p, "tool-1")
		w.stop(p, "early Stop before first queued steer")
		w.exercised = true
	}
}
func (w *workloadReplay) target(p *replayParent) bool {
	return (w.tc.role == "native" && p.role == "control") || (w.tc.role != "native" && p.role == "ad")
}

// Test-side request journal with explicit, independently hashed message blobs.
// Keys here are fixed ASCII; SetEscapeHTML(false) matches the existing journal
// fixture's JSON spelling. This is synthetic callback input, never model proof.
func (w *workloadReplay) request(p *replayParent, text, prose, stop string) {
	w.requestRole(p, text, prose, stop, "user")
}
func (w *workloadReplay) requestRole(p *replayParent, text, prose, stop, role string) {
	w.seq++
	id := fmt.Sprintf("synthetic-request-%05d", w.seq)
	tail := []string{}
	if text != "" {
		message := runner.Row{"role": role, "content": []runner.Row{{"type": "text", "text": text}}}
		var raw bytes.Buffer
		enc := json.NewEncoder(&raw)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(message); err != nil {
			w.s.t.Fatal(err)
		}
		hash := runner.Digest(bytes.TrimSuffix(raw.Bytes(), []byte("\n")))
		if !w.blobs[p.id+hash] {
			w.journal[p.id] = append(w.journal[p.id], runner.Row{"type": "api-request-blob", "sessionId": p.session, "hash": hash, "message": message})
			w.blobs[p.id+hash] = true
		}
		tail = append(tail, hash)
	}
	w.journal[p.id] = append(w.journal[p.id], runner.Row{"type": "api-request", "sessionId": p.session, "id": id, "timestamp": w.tick(), "querySource": "repl_main_thread", "keep": 0, "tail": tail})
	if prose != "" || stop != "" {
		w.journal[p.id] = append(w.journal[p.id], runner.Row{"type": "assistant", "sessionId": p.session, "uuid": id + "-row", "requestRef": id, "apiBlockIndex": 0, "timestamp": w.tick(), "message": runner.Row{"id": id + "-message", "role": "assistant", "content": []runner.Row{{"type": "text", "text": prose}}, "stop_reason": stop}})
	}
	w.flush(p)
}
func (w *workloadReplay) flush(p *replayParent) {
	var b bytes.Buffer
	for _, row := range w.journal[p.id] {
		if err := json.NewEncoder(&b).Encode(row); err != nil {
			w.s.t.Fatal(err)
		}
	}
	replayWrite(w.s.t, p.transcript+".next", b.Bytes())
	if err := os.Rename(p.transcript+".next", p.transcript); err != nil {
		w.s.t.Fatal(err)
	}
}
func (w *workloadReplay) stop(p *replayParent, prose string) {
	w.request(p, "", prose, "end_turn")
	w.hook(p, "Stop", runner.Row{"stop_hook_active": false})
	if !strings.Contains(p.id, "-native-") {
		w.s.sql("UPDATE spawns SET state='waiting' WHERE instance_id=?", p.id)
	}
}

func (w *workloadReplay) query(query string, args ...any) string {
	db, err := w.s.rawDB()
	if err != nil {
		w.s.t.Fatal(err)
	}
	defer db.Close()
	var text string
	if err := db.QueryRow(query, args...).Scan(&text); err != nil {
		w.s.t.Fatal(err)
	}
	return text
}
func (w *workloadReplay) enqueue(p *replayParent, id, text, mode string) workloadMessage {
	if m, ok := w.messages[id]; ok {
		return m
	}
	mid := storefix.SeedBusySendResult(w.s.t, w.s.db(), storefix.BusySendResultSeed{InstanceID: p.id, Pair: store.BusySendPair{CallerUUID: w.s.r.Manifest.RunID, DeliveryRequestID: id}, SendMode: store.BusySendMode(mode), Class: store.BusySendResultClass("held")})
	w.s.sql("UPDATE busy_send_messages SET text=?,text_bytes=?,text_sha256=? WHERE message_id=?", []byte(text), len(text), runner.Digest([]byte(text)), mid)
	m := workloadMessage{p, id, text, mode, mid}
	w.messages[id] = m
	return m
}
func (w *workloadReplay) deliver(m workloadMessage, boundary, force string, sighted bool, followers ...workloadMessage) {
	w.seq++
	// This callback completes the previous physical emission before issuing
	// the next one. Keep its evidence, but release its exclusive open slot.
	w.s.sql("UPDATE busy_send_handovers SET state='closed',closed_at_ms=? WHERE instance_id=? AND state='open'", w.clock.Now().UnixMilli(), m.parent.id)
	// Reuse storefix's complete current-schema handover builder. Rebind its
	// fixture member to the original Held Message; preserve its pair/identity.
	tmp := storefix.SeedBusySendResult(w.s.t, w.s.db(), storefix.BusySendResultSeed{InstanceID: m.parent.id, Pair: store.BusySendPair{CallerUUID: w.s.r.Manifest.RunID, DeliveryRequestID: fmt.Sprintf("synthetic-driver-%d", w.seq)}, SendMode: store.BusySendMode(m.mode), Class: store.BusySendResultClass("sent_unconfirmed"), LaunchToken: m.parent.token, SessionID: m.parent.session, Transport: store.BusySendTransport("written")})
	h := w.query("SELECT handover_id FROM busy_send_handover_members WHERE message_id=?", tmp)
	w.s.sql("UPDATE busy_send_handover_members SET message_id=? WHERE message_id=?", m.mid, tmp)
	w.s.sql("DELETE FROM busy_send_results WHERE delivery_request_id LIKE 'synthetic-driver-%'")
	w.s.sql("DELETE FROM busy_send_messages WHERE message_id=?", tmp)
	w.s.sql("UPDATE busy_send_messages SET place='issued',handover_id=?,bound_launch_token=?,bound_session_id=? WHERE message_id=?", h, m.parent.token, m.parent.session, m.mid)
	texts := [][]byte{[]byte(m.text)}
	for i, follower := range followers {
		w.s.sql("INSERT INTO busy_send_handover_members (handover_id,member_index,message_id,prompt_index,store_id) SELECT handover_id,?,?,0,store_id FROM busy_send_handovers WHERE handover_id=?", i+1, follower.mid, h)
		w.s.sql("UPDATE busy_send_messages SET place='issued',handover_id=?,bound_launch_token=?,bound_session_id=? WHERE message_id=?", h, m.parent.token, m.parent.session, follower.mid)
		texts = append(texts, []byte(follower.text))
	}
	w.s.sql("UPDATE busy_send_prompts SET message_count=?,rendered_bytes=? WHERE handover_id=?", len(texts), len(store.RenderBusySendPromptBody(texts)), h)
	w.s.sql("UPDATE busy_send_handovers SET cutoff_seq=(SELECT MAX(accepted_seq) FROM busy_send_messages WHERE handover_id=?) WHERE handover_id=?", h, h)
	var forceValue any
	if force != "" {
		forceValue = force
	}
	w.s.sql("UPDATE busy_send_handovers SET boundary=?,session_id=?,force_id=?,forced=? WHERE handover_id=?", boundary, m.parent.session, forceValue, force != "", h)
	at := w.tick().UnixMilli()
	w.s.sql("UPDATE busy_send_prompts SET issued_at_ms=?,transport_at_ms=? WHERE handover_id=?", at, at, h)
	if sighted {
		w.s.sql("UPDATE busy_send_prompts SET admission_kind='prompt_sighting',admitted_at_ms=?,order_kind='emission_complete',order_at_ms=? WHERE handover_id=?", at, at, h)
	}
}

func (w *workloadReplay) command(ctx context.Context, argv []string, out io.Writer) (bool, int, error) {
	if len(argv) < 2 {
		return false, 0, nil
	}
	op := argv[1]
	if argv[0] == "tmux" {
		if len(argv) < 4 {
			return false, 0, nil
		}
		op = argv[3]
	}
	if op == "spawn" && strings.HasPrefix(w.tc.kind, "backlog-") && len(w.s.parents) > 0 {
		return true, -1, w.s.stop
	}
	if !slices.Contains([]string{"send-keys", "force-delivery", "pause", "get", "get-delivery", "decide", "kill-session", "kill"}, op) {
		return false, 0, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	write := func(value any) (bool, int, error) { return true, 0, json.NewEncoder(out).Encode(value) }
	if op == "kill" {
		p := w.s.parent(replayArg(argv, "--instance-id"))
		if w.tc.kind == "backlog-held-through-admission" && !p.exited {
			w.checkHold(p)
		}
		w.s.reap(p)
		w.s.sql("UPDATE spawns SET state='ended' WHERE instance_id=?", p.id)
		return true, 0, nil
	}
	if op == "kill-session" {
		for _, owner := range w.s.r.Manifest.Owned {
			if owner.TmuxID == replayArg(argv, "-t") {
				w.s.reap(w.s.parent(owner.InstanceID))
				return true, 0, nil
			}
		}
		return true, -1, errors.New("unowned native cleanup target")
	}
	if op == "pause" {
		return true, -1, errors.New("synthetic conditional pause refused while working")
	}
	if op == "get" {
		if w.tc.kind == "launch-replaced" && !w.eventSeen {
			p := w.s.parents[0]
			w.eventSeen, w.exercised = true, true
			w.inputsAtEvent = len(w.s.inputs)
			w.replacementBefore = w.s.r.Manifest.Owned[0]
			_, er := w.s.r.AwaitStateChecked(ctx, p.id, time.Second, false,
				func(ctx context.Context, _ runner.Snapshot) error {
					w.s.sql("UPDATE spawns SET launch_token='synthetic-replacement' WHERE instance_id=?", p.id)
					err := w.s.r.RefreshTranscriptBindings(ctx)
					w.refreshRejected = err != nil && strings.Contains(err.Error(), "owned Launch replaced")
					w.replacementAfter = w.s.r.Manifest.Owned[0]
					// Restore only this test's row for canonical cleanup of the
					// old registered process; never adopt the replacement.
					w.s.sql("UPDATE spawns SET launch_token=? WHERE instance_id=?", p.token, p.id)
					if err == nil {
						return errors.New("replacement refresh unexpectedly succeeded")
					}
					return err
				}, func(runner.Snapshot) bool { return false })
			return true, -1, er
		}
		row := runner.Row{"delivery_capabilities": w.s.capabilities}
		if w.tc.phase == "dialogs" && w.dialogOrigin < 3 {
			p := w.s.parents[0]
			origin := []string{"main", "subagent", "background"}[w.dialogOrigin]
			row["permission_requests"] = []runner.Row{{"request_token": "unit-permission-" + origin, "tool_name": "Bash", "tool_input": "printf qualification > " + filepath.Join(p.cwd, "qd-"+origin+".txt"), "decision": nil}}
		}
		if w.tc.phase == "esc-gap" {
			row["delivery"] = runner.Row{"awaiting_turn_start": 1}
		}
		return write(row)
	}
	if op == "get-delivery" {
		id := replayArg(argv, "--delivery-request-id")
		if id == "backlog-over-count" {
			return true, -1, w.s.stop
		}
		if strings.HasPrefix(id, "backlog-over-") {
			return write(runner.Row{"lookup": "unknown"})
		}
		return write(runner.Row{"lookup": "found", "result": runner.Row{"caller_uuid": w.s.r.Manifest.RunID, "request_id": id, "class": "held", "final": false, "wait": runner.Row{"reason": "readiness_unknown"}}})
	}
	if op == "decide" {
		w.dialogOrigin++
		if w.dialogOrigin < 3 {
			w.gate(w.s.parents[0])
		} else {
			w.exercised = true
		}
		return write(runner.Row{"decision": "allow", "delivery": "delivered"})
	}
	if op == "force-delivery" {
		return w.force(out)
	}
	if argv[0] == "tmux" {
		text := argv[len(argv)-1]
		p := w.s.parent(replayArg(argv, "-t"))
		w.s.inputs = append(w.s.inputs, append([]string(nil), argv...))
		if text == "Escape" {
			return true, 0, nil
		}
		if text != "Enter" {
			w.typed[p.id] = text
			return true, 0, nil
		}
		text = w.typed[p.id]
		if w.tc.phase == "esc-gap" {
			w.ups(p, text)
			w.stop(p, "typed recovery completed")
			m := w.messages["E3"]
			w.deliver(m, "turn_end", "", true)
			w.ups(p, q.Transform(string(store.RenderBusySendPromptBody([][]byte{[]byte(m.text)})), "wait_turn"))
			w.post(p, "tool-2")
			w.stop(p, "E3 skipped its recovery tools")
			w.exercised = true
			return true, 0, nil
		}
		w.longInputs++
		if strings.HasPrefix(text, "TYPED-between-tools") {
			w.betweenInputs++
			if w.tc.kind == "early-Stop" && w.tc.at == "after-between" && w.target(p) {
				w.stop(p, "native ended before queued typed input")
				w.stoppedEarly = true
			}
			w.ups(p, text)
			if w.target(p) && slices.Contains([]string{"excess-prose", "nontermination"}, w.tc.kind) {
				w.request(p, text, "The native response remains active.", "")
			} else {
				if !w.target(p) {
					w.pre(p, "tool-3", "for i in 1 2 3; do sleep 1; done", false)
					w.post(p, "tool-3")
				}
				w.request(p, text, "native reply", "end_turn")
				w.stop(p, "native queued input completed")
			}
		} else {
			w.ups(p, text)
			if w.tc.kind == "early-Stop" && w.tc.at == "after-long" && w.target(p) {
				w.stop(p, "native ended after first queued steer")
				w.stoppedEarly = true
				w.exercised = true
			}
		}
		w.typedAdvance()
		return true, 0, nil
	}
	p := w.s.parent(replayArg(argv, "--instance-id"))
	text := replayArg(argv, "--text")
	id := replayArg(argv, "--delivery-request-id")
	if id == "" {
		id = fmt.Sprintf("unconfirmed-%d", len(w.s.inputs))
	}
	w.s.inputs = append(w.s.inputs, append([]string(nil), argv...))
	if strings.HasPrefix(text, "TYPED-") {
		m := w.enqueue(p, id, text, replayArg(argv, "--send-mode"))
		fallback := (strings.HasPrefix(text, "TYPED-between-tools") && (w.tc.kind == "a5" || ((w.tc.at == "after-between" || w.tc.kind == "no-next-tool") && w.target(p)))) || (strings.HasPrefix(text, "TYPED-long-tool") && w.tc.at == "after-long" && w.target(p))
		if !fallback {
			w.deliver(m, "tool_boundary", "", false)
		}
		if strings.HasPrefix(text, "TYPED-long-tool") {
			w.longInputs++
			if w.tc.kind == "early-Stop" && w.tc.at == "after-long" && w.target(p) {
				w.fallback(p, m)
			}
			w.typedAdvance()
		}
		if strings.HasPrefix(text, "TYPED-between-tools") {
			w.betweenInputs++
			w.queued = true
			if w.tc.kind == "a5" {
				w.a5Fallback(p, m)
			} else if fallback || (w.target(p) && w.tc.kind == "no-next-tool") {
				w.fallback(p, m)
			} else if w.target(p) && slices.Contains([]string{"excess-prose", "nontermination"}, w.tc.kind) {
				w.requestRole(p, signals.NativeContextBody(string(store.RenderBusySendPromptBody([][]byte{[]byte(text)}))), "The response remains active.", "", "system")
			} else {
				w.pre(p, "tool-3", workloadShell(w.messages["typed-long-tool-ad-input"].text, 1), false)
				w.post(p, "tool-3")
				w.requestRole(p, signals.NativeContextBody(string(store.RenderBusySendPromptBody([][]byte{[]byte(text)}))), "AD input completed.", "end_turn", "system")
				w.hook(p, "Stop", runner.Row{"stop_hook_active": false})
				w.s.sql("UPDATE spawns SET state='waiting' WHERE instance_id=?", p.id)
			}
		}
		return write(runner.Row{"class": "held", "final": false})
	}
	if w.tc.phase == "force-states" {
		request := replayArg(argv, "--delivery-request-id")
		m := w.enqueue(p, request, text, "wait_turn")
		if request == "F1" && slices.Contains(argv, "--force") {
			w.deliver(m, "force", "unit-F1", true)
			w.ups(p, q.Transform(string(store.RenderBusySendPromptBody([][]byte{[]byte(text)})), "wait_turn"))
			w.pre(p, "tool-1", workloadShell(text, 0), false)
			return write(runner.Row{"force_result": runner.Row{"force_id": "unit-F1", "outcome": "handed_over", "reason": "admitted"}})
		}
		if request == "F3" {
			ledger, err := q.LoadLedger(w.s.ledger)
			if err != nil {
				return true, -1, err
			}
			w.tailReserved = slices.ContainsFunc(ledger.Charges, func(c q.Charge) bool { return c.ID == "F3" && c.Reserved == 1 })
		}
		return write(runner.Row{"class": "held", "final": false})
	}
	if strings.HasPrefix(id, "backlog-") {
		return w.backlog(ctx, p, id, text, out)
	}
	if w.tc.phase == "groups" {
		mode := replayArg(argv, "--send-mode")
		m := w.enqueue(p, id, text, mode)
		if mode == "wait_turn" {
			w.ordinaryAccepts++
			if !w.groupSecond {
				w.s.t.Error("ordinary group sent before both valid steer boundaries")
			}
			if len(w.calls[p.id]) != 3 || w.groupGates["tool-3"] == "" {
				w.s.t.Error("ordinary group admission preceded its third foreground gate")
			}
			if w.tc.kind == "group-Stop-after-S2" && w.ordinaryAccepts == 1 {
				// With no hold, a fast ordinary wake takes only this prefix.
				w.deliver(m, "turn_end", "", true)
				w.ups(p, q.Transform(string(store.RenderBusySendPromptBody([][]byte{[]byte(text)})), "wait_turn"))
				w.stop(p, "first ordinary prefix completed")
			}
			return write(runner.Row{"class": "held", "final": false})
		}
		w.accepts++
		if w.tc.kind == "group-Stop-during-steer" && w.accepts == 1 {
			// Inject loss of the live hold without completing its unreleased
			// call or inventing a differently partitioned ordinary start.
			w.stop(p, "parent stopped during first steer admission")
			w.eventSeen, w.exercised, w.stoppedEarly = true, true, true
			w.inputsAtEvent = len(w.s.inputs)
		}
		steer, _ := q.PlanGroup("steer_turn")
		if w.accepts == len(steer.Texts) {
			w.queued = true
		}
		return write(runner.Row{"class": "held", "final": false})
	}
	if w.tc.phase == "dialogs" {
		w.accepts++
		if id == "D2" {
			m := w.enqueue(p, id, text, "wait_turn")
			w.deliver(m, "force", "unit-plan-force", true)
			w.ups(p, q.Transform(string(store.RenderBusySendPromptBody([][]byte{[]byte(text)})), "wait_turn"))
			w.stop(p, "No plan was presented.")
			w.exercised = true
		}
		if w.accepts%4 == 0 && w.dialogOrigin < 3 {
			origin := []string{"main", "subagent", "background"}[w.dialogOrigin]
			w.hook(p, "PermissionRequest", runner.Row{"tool_name": "Bash", "tool_input": runner.Row{"command": "printf qualification > " + filepath.Join(p.cwd, "qd-"+origin+".txt")}})
		}
		return write(runner.Row{"class": "held", "final": false})
	}
	if w.tc.phase == "esc-gap" {
		m := w.enqueue(p, id, text, replayArg(argv, "--send-mode"))
		if id == "Esc-steer-1" {
			w.deliver(m, "tool_boundary", "", false)
			w.requestRole(p, signals.NativeContextBody(string(store.RenderBusySendPromptBody([][]byte{[]byte(text)}))), "steer seen", "", "system")
			w.post(p, "tool-1")
			w.pre(p, "tool-2", workloadShell(p.prompt, 1), false)
		}
		return write(runner.Row{"class": "held", "final": false})
	}
	return true, -1, fmt.Errorf("unexpected workload input %s", id)
}

func (w *workloadReplay) gate(p *replayParent) {
	origin := []string{"main", "subagent", "background"}[min(w.dialogOrigin, 2)]
	path := filepath.Join(w.s.r.Evidence.Root, "raw/gates", "synthetic-"+origin, "input.json")
	if strings.HasPrefix(w.tc.kind, "backlog-") {
		if w.tc.kind == "backlog-missing" {
			return
		}
		// Both passive recording and the blocking gate see the same invocation.
		replayWrite(w.s.t, path, replayRead(w.s.t, filepath.Join(w.s.r.Evidence.Root, "raw/hooks", fmt.Sprintf("invocation-workload-%05d", w.seq), "input.json")))
		w.s.gateDriver(p, filepath.Dir(path))
		return
	}
	target := filepath.Join(p.cwd, "qd-"+origin+".txt")
	gate := runner.Row{"session_id": p.session, "tool_use_id": "gate-" + origin, "tool_name": "Bash", "tool_input": runner.Row{"command": "printf qualification > " + target}}
	if origin != "main" {
		gate["agent_id"] = "synthetic-dialog-child-" + origin
		w.journal[p.id] = append(w.journal[p.id], replayDialogChild(p, origin, target, w.tick())...)
		w.flush(p)
	}
	replayJSON(w.s.t, path, gate)
}
func (w *workloadReplay) force(out io.Writer) (bool, int, error) {
	write := func(id, outcome, reason string) (bool, int, error) {
		return true, 0, json.NewEncoder(out).Encode(runner.Row{"force_result": runner.Row{"force_id": id, "outcome": outcome, "reason": reason}})
	}
	if w.tc.phase == "esc-gap" {
		w.hook(w.s.parents[0], "Notification", runner.Row{"notification_type": "idle_prompt"})
		return write("unit-gap", "refused", "input_unresolved")
	}
	w.forceCalls++
	if w.forceCalls == 1 {
		return write("unit-starting", "refused", "agent_starting")
	}
	if w.forceCalls == 2 {
		p := w.s.parents[0]
		w.post(p, "tool-1")
		w.pre(p, "tool-2", "for i in 1 2 3; do sleep 1; done", false)
		w.post(p, "tool-2")
		w.stop(p, "F1 completed")
		m := w.messages["F2"]
		w.deliver(m, "force", "unit-F2", !strings.HasPrefix(w.tc.kind, "tail-late-"))
		if !strings.HasPrefix(w.tc.kind, "tail-late-") {
			w.ups(p, q.Transform(string(store.RenderBusySendPromptBody([][]byte{[]byte(m.text)})), "wait_turn"))
		}
		w.pre(p, "tool-3", workloadShell(m.text, 0), false)
		w.post(p, "tool-3")
		w.request(p, "", "Synthetic between-tools prose.", "")
		if w.tc.kind == "tail-early" {
			w.stop(p, "F2 ended with F3 Held")
			tail := w.messages["F3"]
			w.deliver(tail, "turn_end", "", true)
			w.ups(p, q.Transform(string(store.RenderBusySendPromptBody([][]byte{[]byte(tail.text)})), "wait_turn"))
			w.exercised = true
		}
		if w.tc.kind == "force-ends" {
			w.stop(p, "tool completed before force returned")
			w.exercised = true
			return write("unit-F2", "handed_over", "admitted")
		}
		return write("unit-F2", "pending", "awaiting_admission")
	}
	if w.forceCalls == 3 {
		return write("unit-already-forcing", "refused", "force_running")
	}
	// The normal control reaches the same F3 identity's reclaim command.
	w.tailReclaimed = true
	w.exercised = true
	return true, -1, w.s.stop
}

func (w *workloadReplay) typedAdvance() {
	if w.longInputs != 2 || w.ready {
		return
	}
	w.ready = true
	parents := append([]*replayParent(nil), w.s.parents...)
	if w.tc.kind == "a5" {
		slices.Reverse(parents)
	}
	for _, p := range parents {
		if w.stoppedEarly && w.tc.at == "after-long" && w.target(p) {
			continue
		}
		if w.tc.kind == "a5" && w.target(p) {
			w.post(p, "tool-1")
			w.a5Base = w.tick()
			w.a5Hook(p, 0)
			w.a5Hook(p, 1)
			w.a5Rows(p, 0, 1)
			w.clock.Advance(w.a5Time(w.a5.Injection).Sub(w.clock.Now()))
			continue
		}
		w.post(p, "tool-1")
		w.pre(p, "tool-2", "for i in 1 2 3; do sleep 1; done", false)
		w.post(p, "tool-2")
		prose := "A practical community garden balances shared beds and accessible paths."
		terminal := ""
		if w.target(p) {
			switch w.tc.kind {
			case "short-prose":
				prose = "Short."
				terminal = "end_turn"
			case "absent-prose":
				prose = ""
				terminal = "end_turn"
			case "excess-prose":
				prose = strings.Repeat("garden planning ", 2000)
			case "plain-question":
				prose = "Should I run the next command?"
				terminal = "end_turn"
			case "structured-question":
				w.s.sql("UPDATE spawns SET state='ask_user' WHERE instance_id=?", p.id)
			}
			if w.tc.at == "before-between" {
				terminal = "end_turn"
			}
			w.exercised = true
		}
		w.request(p, "", prose, terminal)
		if terminal != "" {
			w.hook(p, "Stop", runner.Row{"stop_hook_active": false})
			w.s.sql("UPDATE spawns SET state='waiting' WHERE instance_id=?", p.id)
		}
	}
}
func (w *workloadReplay) fallback(p *replayParent, m workloadMessage) {
	w.stop(p, "Model ended without its requested next tool.")
	w.deliver(m, "turn_end", "", true)
	body := string(store.RenderBusySendPromptBody([][]byte{[]byte(m.text)}))
	w.ups(p, q.Transform(body, "wait_turn"))
	w.request(p, q.Transform(body, "wait_turn"), "", "")
	w.stoppedEarly = true
	w.exercised = true
}

func (w *workloadReplay) groupReleased(tool, release string) bool {
	gate := w.groupGates[tool]
	if gate == "" {
		return false
	}
	_, shellErr := os.Stat(filepath.Join(w.s.r.Evidence.Root, "work-G0", release))
	_, gateErr := os.Stat(filepath.Join(gate, "release"))
	return shellErr == nil && gateErr == nil
}

func (w *workloadReplay) groupWindow(p *replayParent) {
	w.exercised = true
	kind := strings.TrimPrefix(w.tc.kind, "window-")
	if w.tc.kind == "group-Stop-after-S2" {
		kind = w.tc.kind
	}
	switch kind {
	case "batch-two":
		w.pre(p, "tool-2", workloadShell(p.prompt, 1), false)
		w.post(p, "tool-1", "tool-2")
	case "reverse-completion":
		w.pre(p, "tool-2", workloadShell(p.prompt, 1), false)
		w.post(p, "tool-2")
		w.post(p, "tool-1")
	case "background":
		w.post(p, "tool-1")
		w.pre(p, "tool-2", workloadShell(p.prompt, 1), true)
	case "combined":
		w.post(p, "tool-1")
		w.pre(p, "tool-2", workloadShell(p.prompt, 0)+"; "+workloadShell(p.prompt, 1), false)
	case "extra":
		w.post(p, "tool-1")
		w.pre(p, "unrelated-extra", "printf unrelated-extra", false)
	case "serial", "group-Stop-after-S2":
		w.post(p, "tool-1")
		w.groupPart(p, 0)
		w.pre(p, "tool-2", workloadShell(p.prompt, 1), false)
	default:
		w.post(p, "tool-1")
		w.stop(p, "G0 omitted its second tool")
	}
}

func (w *workloadReplay) groupPart(p *replayParent, n int) {
	plan, err := q.PlanGroup("steer_turn")
	if err != nil {
		w.s.t.Fatal(err)
	}
	part := plan.Parts[n]
	var members []workloadMessage
	for _, text := range plan.Texts[part.First : part.First+part.Count] {
		found := false
		for _, m := range w.messages {
			if m.parent == p && m.text == text && m.mode == "steer_turn" {
				members = append(members, m)
				found = true
				break
			}
		}
		if !found {
			w.s.t.Fatal("controlled boundary preceded a steer admission")
		}
	}
	w.deliver(members[0], "tool_boundary", "", false, members[1:]...)
}

func (w *workloadReplay) groupOrdinary(p *replayParent, n int) {
	plan, err := q.PlanGroup("wait_turn")
	if err != nil {
		w.s.t.Fatal(err)
	}
	part := plan.Parts[n]
	var members []workloadMessage
	for _, text := range plan.Texts[part.First : part.First+part.Count] {
		for _, m := range w.messages {
			if m.parent == p && m.text == text && m.mode == "wait_turn" {
				members = append(members, m)
				break
			}
		}
	}
	if len(members) != part.Count {
		w.s.t.Fatal("ordinary boundary preceded its complete partition")
	}
	w.deliver(members[0], "turn_end", "", true, members[1:]...)
	var texts [][]byte
	for _, m := range members {
		texts = append(texts, []byte(m.text))
	}
	body := q.Transform(string(store.RenderBusySendPromptBody(texts)), "wait_turn")
	w.ups(p, body)
	w.request(p, body, "ordinary partition completed", "end_turn")
	w.hook(p, "Stop", runner.Row{"stop_hook_active": false})
	w.s.sql("UPDATE spawns SET state='waiting' WHERE instance_id=?", p.id)
}

func (w *workloadReplay) backlog(ctx context.Context, p *replayParent, id, text string, out io.Writer) (bool, int, error) {
	write := func() (bool, int, error) {
		return true, 0, json.NewEncoder(out).Encode(runner.Row{"caller_uuid": w.s.r.Manifest.RunID, "request_id": id, "class": "held", "final": false})
	}
	w.checkHold(p)
	if strings.HasPrefix(id, "backlog-over-") {
		w.refusals++
		caps := store.BusySendCapabilitiesFor("claude")
		limit, _ := caps.Mode(store.BusySendWaitTurn)
		reason, actual, allowed := "text_bytes", int64(len(text)), limit.MessageBytes
		if id == "backlog-over-count" {
			reason, actual, allowed = "held_count", 1001, 1000
		}
		if err := json.NewEncoder(out).Encode(runner.Row{"err_name": "CantSend", "err_details": runner.Row{"class": "cant_send", "reason": reason, "resend_safe": true, "limit": runner.Row{"actual": actual, "allowed": allowed}}}); err != nil {
			return true, -1, err
		}
		return true, 1, errors.New("synthetic machine-readable limit refusal")
	}
	w.accepts++
	w.exercised = true
	m := w.enqueue(p, id, text, "wait_turn")
	stopAt := 0
	if w.tc.kind == "backlog-Stop-after-1" || w.tc.kind == "backlog-drain" {
		stopAt = 1
	}
	if w.tc.kind == "backlog-Stop-after-24" {
		stopAt = 24
	}
	if w.accepts == stopAt {
		w.stop(p, "N8 tool ended during admission")
		w.deliver(m, "turn_end", "", true)
		w.ups(p, q.Transform(string(store.RenderBusySendPromptBody([][]byte{[]byte(text)})), "wait_turn"))
		w.stoppedEarly = true
	}
	if w.tc.kind == "backlog-cancel" && w.accepts == 1 {
		w.eventSeen = true
		w.injectedFault = context.Canceled
		w.s.cancel()
		return true, -1, w.injectedFault
	}
	if w.tc.kind == "backlog-driver-failure" && w.accepts == 1 {
		w.eventSeen = true
		w.injectedFault = errors.New("synthetic owned driver failed")
		return true, -1, w.injectedFault
	}
	if w.accepts == 1 && w.tc.kind == "backlog-prompt-during-hold" {
		// Isolate the prompt-existence guard: no Stop, batch, UPS, or extra turn.
		w.deliver(m, "tool_boundary", "", false)
		w.eventSeen = true
	}
	if w.accepts == 1 && w.tc.kind == "backlog-driver-exit" {
		driver := w.s.drivers[0]
		if err := driver.command.Process.Kill(); err != nil {
			w.s.t.Fatal(err)
		}
		if err := driver.wait(ctx); err != nil {
			return true, -1, err
		}
		gone, er := runner.ProcessGone(driver.command.Process.Pid, driver.start)
		if er != nil || !gone {
			w.s.t.Fatalf("injected driver exit not witnessed: gone=%t err=%v", gone, er)
		}
		w.eventSeen = true
	}
	if slices.Contains([]string{"backlog-missing", "backlog-wrong", "backlog-background"}, w.tc.kind) {
		return true, -1, w.s.stop
	}
	return write()
}

func (w *workloadReplay) checkHold(p *replayParent) {
	if _, err := os.Stat(filepath.Join(p.cwd, ".claude/settings.local.json")); err != nil {
		w.holdMissing = true
	}
	gate := filepath.Join(w.s.r.Evidence.Root, "raw/gates/synthetic-main")
	if _, err := os.Stat(filepath.Join(gate, "input.json")); err != nil {
		w.holdMissing = true
	}
	if _, err := os.Stat(filepath.Join(gate, "release")); err == nil && !p.exited {
		w.holdMissing = true
	}
}

func (w *workloadReplay) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.clock.Advance(d)
	if w.s.cleaning {
		return nil
	}
	w.polls++
	if strings.HasPrefix(w.tc.kind, "tail-late-") && w.forceCalls >= 3 && !w.tailLate {
		files, err := os.ReadDir(filepath.Join(w.s.r.Evidence.Root, "raw/observations"))
		if err != nil || len(files) < 5 {
			w.s.t.Fatalf("late F3 preceded F3-schedule: files=%d err=%v", len(files), err)
		}
		var before runner.Snapshot
		replayDecode(w.s.t, filepath.Join(w.s.r.Evidence.Root, "raw/observations", files[len(files)-1].Name()), &before)
		for _, row := range before.Tables["messages"] {
			if runner.Text(row, "message_id") == w.messages["F3"].mid && runner.Text(row, "place") != "held" {
				w.s.t.Fatal("F3-schedule was not Held before late publication")
			}
		}
		p := w.s.parents[0]
		if w.tc.kind == "tail-late-sighted" {
			f2 := w.messages["F2"]
			w.ups(p, q.Transform(string(store.RenderBusySendPromptBody([][]byte{[]byte(f2.text)})), "wait_turn"))
			h := w.query("SELECT handover_id FROM busy_send_handover_members WHERE message_id=?", w.messages["F2"].mid)
			at := w.tick().UnixMilli()
			w.s.sql("UPDATE busy_send_prompts SET admission_kind='prompt_sighting',admitted_at_ms=?,order_kind='emission_complete',order_at_ms=? WHERE handover_id=?", at, at, h)
		}
		w.stop(p, "F2 ended after F3-schedule")
		tail := w.messages["F3"]
		w.deliver(tail, "turn_end", "", true)
		w.ups(p, q.Transform(string(store.RenderBusySendPromptBody([][]byte{[]byte(tail.text)})), "wait_turn"))
		w.tailLate, w.exercised = true, true
		w.inputsAtEvent = len(w.s.inputs)
		return nil
	}
	if w.tc.phase == "force-states" && !w.s.parents[0].bound {
		w.s.bind(w.s.parents[0], false)
		w.hook(w.s.parents[0], "SessionStart", nil)
		return nil
	}
	if w.tc.phase == "text-limits-backlog" && !strings.HasPrefix(w.tc.kind, "backlog-") {
		w.native()
	}
	if w.tc.phase == "groups" && w.queued && !w.exercised {
		if w.groupReleased("tool-1", "release-first") {
			w.groupWindow(w.s.parents[0])
			return nil
		}
	}
	if (w.tc.kind == "window-serial" || w.tc.kind == "group-Stop-after-S2") && w.exercised {
		if !w.groupSecond && w.groupReleased("tool-2", "release-second") {
			w.post(w.s.parents[0], "tool-2")
			w.groupPart(w.s.parents[0], 1)
			w.groupSecond = true
			if w.tc.kind == "group-Stop-after-S2" {
				w.stop(w.s.parents[0], "G0 stopped immediately after S2")
				w.eventSeen, w.stoppedEarly = true, true
				w.inputsAtEvent = len(w.s.inputs)
			} else {
				p := w.s.parents[0]
				w.pre(p, "tool-3", workloadShell(p.prompt, 2), false)
			}
			return nil
		}
		if w.tc.kind == "window-serial" && !w.groupDrained && w.groupReleased("tool-3", "release-ordinary") {
			p := w.s.parents[0]
			w.post(p, "tool-3")
			w.stop(p, "G0 third foreground call completed")
			ordinary, _ := q.PlanGroup("wait_turn")
			if w.ordinaryAccepts != len(ordinary.Texts) {
				w.s.t.Fatal("third gate released before all ordinary admissions")
			}
			for n := range ordinary.Parts {
				w.groupOrdinary(p, n)
			}
			w.groupDrained = true
			return nil
		}
		if w.groupSecond && w.tc.kind == "group-Stop-after-S2" {
			return w.s.stop
		}
	}
	if w.polls > 12 {
		return context.DeadlineExceeded
	}
	return nil
}

func (w *workloadReplay) native() {
	for _, p := range w.s.parents {
		if p.exited || !strings.Contains(p.id, "-native-") {
			continue
		}
		route := "wake"
		if strings.HasSuffix(p.id, "post_tool_batch") {
			route = "post_tool_batch"
		}
		var plan q.NativePlan
		replayDecode(w.s.t, filepath.Join(w.s.r.Evidence.Root, "raw/native-"+route+"-plan.json"), &plan)
		id := string(replayRead(w.s.t, plan.Pointer))
		if route == "post_tool_batch" {
			id = "N7.1"
			if w.exercised {
				continue
			}
		}
		if id == "" {
			continue
		}
		if _, ok := w.typed[p.id+id]; ok {
			continue
		}
		w.typed[p.id+id] = "emitted"
		var rung q.Rung
		for _, candidate := range plan.Rungs {
			if candidate.ID == id {
				rung = candidate
			}
		}
		if rung.ID == "" {
			w.s.t.Fatalf("native scripted rung %s not in real plan", id)
		}
		event := "FileChanged"
		mode := "wait_turn"
		if route == "post_tool_batch" {
			event = "PostToolBatch"
			mode = "steer_turn"
			w.pre(p, "native-tool-1", "for i in 1 2; do sleep 1; done", false)
			w.post(p, "native-tool-1")
		}
		started := w.tick()
		stdout, stderr, code, err := q.NativeOutput(event, plan.Watch, rung.Body)
		if err != nil {
			w.s.t.Fatal(err)
		}
		output := stdout
		if len(stderr) > 0 {
			output = stderr
		}
		ref := "raw/native-emissions/synthetic-" + id
		input := runner.Row{"session_id": p.session, "cwd": p.cwd, "hook_event_name": event, "transcript_path": p.transcript, "file_path": plan.Watch}
		if err := w.s.r.Evidence.JSON(ref+"/input.json", input); err != nil {
			w.s.t.Fatal(err)
		}
		if err := w.s.r.Evidence.Put(ref+"/output.bin", output); err != nil {
			w.s.t.Fatal(err)
		}
		emission := q.NativeEmission{Case: id, Route: route, Session: p.session, Event: event, Started: started, Written: w.tick(), BodySHA: runner.Digest([]byte(rung.Body)), Input: ref + "/input.json", Output: ref + "/output.bin", Exit: code}
		if err := w.s.r.Evidence.JSON(ref+"/receipt.json", emission); err != nil {
			w.s.t.Fatal(err)
		}
		if route == "wake" {
			w.ups(p, q.Transform(rung.Body, mode))
		}
		inputText, role := signals.NativeWake(q.Transform(rung.Body, mode)), "user"
		if mode == "steer_turn" {
			inputText, role = signals.NativeContextBody(rung.Body), "system"
		}
		w.requestRole(p, inputText, "Synthetic measured-prefix response.", "end_turn", role)
		w.hook(p, "Stop", runner.Row{"stop_hook_active": false})
		if route == "post_tool_batch" {
			w.exercised = true
		}
	}
}

func workloadReason(v runner.Verdict, err error) string {
	var b strings.Builder
	if err != nil {
		b.WriteString(err.Error())
	}
	for _, r := range v.Results {
		if !strings.HasPrefix(r.SignalID, "independent-product/") {
			data, _ := json.Marshal(r)
			b.Write(data)
		}
	}
	return strings.ToLower(strings.NewReplacer("_", "-", " ", "-").Replace(b.String()))
}

func (w *workloadReplay) run() {
	s := w.s
	t := s.t
	if w.tc.kind == "a5" {
		var data struct {
			A5 workloadA5 `json:"a5_workload"`
		}
		replayDecode(t, "testdata/qualification-replay-schedules.json", &data)
		w.a5 = &data.A5
	}
	if strings.HasPrefix(w.tc.kind, "backlog-") {
		w.canary = exec.Command("/bin/sleep", fmt.Sprint(int((runner.PhaseLimit+runner.CleanupReserve)/time.Second)))
		if err := w.canary.Start(); err != nil {
			t.Fatal(err)
		}
		start, alive, known := probe.NewProcChecker().StartTime(w.canary.Process.Pid)
		if !alive || !known {
			t.Fatal("unrelated canary unavailable")
		}
		w.canaryStart = start
		t.Cleanup(func() { _ = w.canary.Process.Kill(); _ = w.canary.Wait() })
	}
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.cancel = cancel
	contract, err := q.ParseContract(replayRead(t, filepath.Join(s.r.Kit, "docs/caller-contract.md")))
	if err != nil {
		t.Fatal(err)
	}
	product := q.CheckForceState(contract, q.ForceObservation{State: "idle", Outcome: "handed_over", Reason: "admitted", Exercised: true})
	if product.Status != "fail" {
		t.Fatal("eligible-force sighting negative is not a failing product judgment")
	}
	product.SignalID = "independent-product/" + product.SignalID
	var phaseErr error
	v, err := q.Execute(parent, s.r, func(ctx context.Context, r *runner.Run) error {
		if strings.HasPrefix(w.tc.kind, "backlog-") {
			phaseErr = q.StartExercise(ctx, r, s.ledger, q.LargeBacklog)
		} else {
			phaseErr = q.Dispatch(ctx, r, s.options)
		}
		// An independent diagnosis reaches finalization alongside the workload
		// result; it must survive incomplete handling and owned cleanup.
		r.Manifest.Problems = append(r.Manifest.Problems, product)
		s.cleaning = true
		return phaseErr
	}, func(string) runner.Verdict { return q.PhaseResult(s.r.Manifest, nil) })
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(v.Results, func(r runner.Result) bool {
		return r.SignalID == product.SignalID && r.Status == "fail" && r.Reason == product.Reason
	}) {
		t.Error("workload outcome erased independently established eligible-force product failure")
	}
	t.Logf("workload %s basis=%s exercised=%t polls=%d sends=%d accepts=%d phase=%v", w.tc.name, map[bool]string{true: "recorded a5 chronology", false: "synthetic"}[w.tc.kind == "a5"], w.exercised, w.polls, len(s.inputs), w.accepts, phaseErr)
	if !w.tc.control && w.tc.want != "" && !slices.ContainsFunc(strings.Split(w.tc.want, "|"), func(name string) bool { return strings.Contains(workloadReason(v, phaseErr), strings.ToLower(name)) }) {
		t.Errorf("missing named %s outcome; actual producer error: %v", w.tc.want, phaseErr)
	}
	if !w.exercised && !slices.Contains([]string{"missing-origin", "backlog-missing", "backlog-wrong", "backlog-background"}, w.tc.kind) {
		t.Error("replay stopped before its assigned workload deviation")
	}
	for _, p := range s.parents {
		if !p.exited {
			t.Errorf("canonical cleanup did not reap owned process %s", p.id)
		}
	}
	for _, o := range s.r.Manifest.Owned {
		if !o.Exited {
			t.Errorf("runner did not verify owned exit %s", o.InstanceID)
		}
	}
	s.assertDriversStopped()
	if _, er := os.Stat(filepath.Dir(s.r.Socket)); !os.IsNotExist(er) {
		t.Errorf("owned runtime retained: %v", er)
	}
	if !s.r.Manifest.RecordingHook.Removed {
		t.Error("recorder registration retained")
	}
	if s.r.Settings != nil {
		defer s.r.Settings.Close()
	}
	if w.canary != nil {
		gone, er := runner.ProcessGone(w.canary.Process.Pid, w.canaryStart)
		if er != nil || gone {
			t.Errorf("driver cleanup disturbed unrelated canary: pid=%d gone=%t err=%v", w.canary.Process.Pid, gone, er)
		}
	}
	w.assertUnused()
	ledger, er := q.LoadLedger(s.ledger)
	if er != nil {
		t.Fatal(er)
	}
	if w.tc.kind == "tail-early" || strings.HasPrefix(w.tc.kind, "tail-late-") {
		if !w.tailReserved {
			t.Error("F3's original reservation did not predate first enqueue/UPS")
		}
		count, actual := 0, 0
		for _, c := range ledger.Charges {
			if c.ID == "F3" {
				count++
				actual += c.Actual
			}
			if strings.HasPrefix(c.ID, "unexpected-") {
				t.Errorf("planned F3 was charged unexpectedly: %s", c.ID)
			}
		}
		if count != 1 || actual != 1 {
			t.Errorf("F3 must be charged once under its original identity: occurrences=%d actual=%d", count, actual)
		}
	}
	if w.tc.kind == "tail-control" && (!w.tailReclaimed || w.messages["F3"].mid == "") {
		t.Error("normal pending tail never reached reclaim with the original F3 Message")
	}
	if w.tc.kind == "a5" {
		w.assertA5(ledger, v)
	}
	if strings.HasPrefix(w.tc.kind, "tail-late-") {
		if !w.tailLate || len(s.inputs) != w.inputsAtEvent {
			t.Error("late F3 boundary not reached, or later input sent")
		}
		found := false
		for _, row := range s.r.Manifest.Problems {
			if row.SignalID == "force-sighting" && row.Status == "fail" {
				found = true
			}
		}
		if found != (w.tc.kind == "tail-late-unsighted") {
			t.Errorf("force-sighting failure=%t for %s", found, w.tc.kind)
		}
		analysis := q.AnalyzePhase(s.r.Evidence.Root)
		if !slices.ContainsFunc(analysis.Results, func(r runner.Result) bool { return r.SignalID == "force-group" && r.Status == "not_completed" }) {
			t.Error("F3/F4 measurement gap not retained")
		}
		if w.tc.kind == "tail-late-unsighted" {
			more, er := q.CheckContinuation(s.r.Evidence.Root, ledger)
			if er == nil || more.ContinueIndependent {
				t.Error("unsighted F2 permitted continuation")
			}
		}
	}
	if w.tc.kind == "launch-replaced" {
		old, after := w.replacementBefore, w.replacementAfter
		if !w.eventSeen || !w.refreshRejected || len(s.inputs) != w.inputsAtEvent ||
			old.LaunchToken == "" || old.LaunchToken != after.LaunchToken || old.LaunchID != after.LaunchID ||
			old.PID != after.PID || old.ProcessStart != after.ProcessStart || old.SessionID != after.SessionID || old.Socket != after.Socket {
			t.Errorf("replacement refresh guard/old ownership failed: rejected=%t before=%+v after=%+v", w.refreshRejected, old, after)
		}
	}
	if w.tc.kind == "window-serial" || strings.HasPrefix(w.tc.kind, "group-Stop-") {
		w.assertGroup(ledger, phaseErr)
	}
	if w.tc.kind == "native-rung-1" && w.exercised {
		var samples []q.NativeSample
		replayDecode(t, filepath.Join(s.r.Evidence.Root, "raw/native-steer-samples.json"), &samples)
		if !slices.ContainsFunc(samples, func(sample q.NativeSample) bool {
			return sample.Rung.ID == "N7.1" && sample.Whole && sample.Receipt != ""
		}) {
			t.Error("N7 lost its measured first-rung prefix")
		}
		for _, sample := range samples {
			if sample.Rung.ID != "N7.1" && sample.Whole {
				t.Errorf("unmeasured N7 rung claimed whole: %s", sample.Rung.ID)
			}
		}
	}
	if strings.HasPrefix(w.tc.kind, "backlog-") {
		if slices.Contains([]string{"backlog-missing", "backlog-wrong", "backlog-background"}, w.tc.kind) && w.accepts != 0 {
			t.Errorf("invalid N8 invocation admitted %d Messages; want zero", w.accepts)
		}
		if !w.tc.control && w.holdMissing {
			t.Error("N8 had no harness-owned foreground hold during admission")
		}
		if w.tc.kind == "backlog-held-through-admission" && (w.accepts != 1000 || w.refusals != 2) {
			t.Errorf("N8 hold did not cover 1000 admissions and both refusals: accepts=%d refusals=%d", w.accepts, w.refusals)
		}
		if w.tc.kind == "backlog-Stop-after-1" && (w.accepts != 1 || !w.stoppedEarly) {
			t.Errorf("N8 sent %d Messages after Stop at accept1", w.accepts)
		}
		if w.tc.kind == "backlog-Stop-after-24" && (w.accepts != 24 || !w.stoppedEarly) {
			t.Errorf("N8 sent %d Messages after Stop at accept24", w.accepts)
		}
		if w.tc.kind == "backlog-drain" && (w.accepts != 1 || !w.stoppedEarly) {
			t.Errorf("drain never reached its actual drain: accepts=%d stopped=%t", w.accepts, w.stoppedEarly)
		}
		if slices.Contains([]string{"backlog-driver-failure", "backlog-cancel"}, w.tc.kind) {
			if w.accepts != 1 || !w.eventSeen || w.injectedFault == nil || !errors.Is(phaseErr, w.injectedFault) {
				t.Errorf("N8 injected fault not witnessed: accepts=%d seen=%t wanted=%v got=%v", w.accepts, w.eventSeen, w.injectedFault, phaseErr)
			}
		}
		if slices.Contains([]string{"backlog-prompt-during-hold", "backlog-driver-exit"}, w.tc.kind) {
			if w.accepts != 1 || !w.eventSeen || w.stoppedEarly || w.starts[s.parents[0].id] != 1 {
				t.Errorf("isolated hold fault was not reached: accepts=%d seen=%t stopped=%t starts=%d", w.accepts, w.eventSeen, w.stoppedEarly, w.starts[s.parents[0].id])
			}
			reason := "backlog unexpectedly issued during harness hold; zero further traffic"
			if w.tc.kind == "backlog-driver-exit" {
				reason = "harness hold driver exited; zero further traffic"
			}
			if phaseErr == nil || !strings.Contains(phaseErr.Error(), reason) {
				t.Errorf("missing prompt/exit-specific outcome %q: %v", reason, phaseErr)
			}
		}
		if w.stoppedEarly {
			actual := 0
			for _, c := range ledger.Charges {
				if c.Launch == s.parents[0].token {
					actual += c.Actual
				}
			}
			if actual != 2 {
				t.Errorf("N8 startup plus detected drain must both be charged: actual=%d", actual)
			}
			var proof q.BacklogProof
			replayDecode(t, filepath.Join(s.r.Evidence.Root, "raw/backlog-proof.json"), &proof)
			if q.CheckBacklog(proof).Status == "pass" {
				t.Error("actual backlog checker passed the drained producer proof")
			}
			if w.tc.control && w.accepts > 25 {
				t.Errorf("producer did not stop at its own first drain observation: accepts=%d", w.accepts)
			}
		}
		for _, r := range v.Results {
			if r.SignalID == "large-backlog" && r.Status == "pass" && w.stoppedEarly {
				t.Error("drained N8 was claimed as a 1000-Held pass")
			}
		}
	}
}

func (w *workloadReplay) assertGroup(ledger q.Ledger, phaseErr error) {
	t := w.s.t
	t.Helper()
	if len(w.s.parents) == 0 {
		t.Errorf("group setup produced no parent: %v", phaseErr)
		return
	}
	if strings.HasPrefix(w.tc.kind, "group-Stop-") {
		named := slices.ContainsFunc(w.s.r.Manifest.Problems, func(r runner.Result) bool {
			return strings.HasPrefix(r.SignalID, "workload/") && r.Status == "not_completed"
		})
		if !w.eventSeen || !named || len(w.s.inputs) != w.inputsAtEvent {
			t.Errorf("group lost-window branch: seen=%t named=%t sends_after=%d phase=%v", w.eventSeen, named, len(w.s.inputs)-w.inputsAtEvent, phaseErr)
		}
		if w.tc.kind == "group-Stop-after-S2" && (w.ordinaryAccepts != 0 || len(w.calls[w.s.parents[0].id]) != 2) {
			t.Errorf("Stop after S2 crossed into ordinary admission: ordinary=%d calls=%d", w.ordinaryAccepts, len(w.calls[w.s.parents[0].id]))
		}
		if w.tc.kind == "group-Stop-during-steer" && (w.accepts != 1 || w.ordinaryAccepts != 0 || w.starts[w.s.parents[0].id] != 1) {
			t.Errorf("Stop during steer admitted later traffic: steer=%d ordinary=%d", w.accepts, w.ordinaryAccepts)
		}
	} else {
		ordinary, _ := q.PlanGroup("wait_turn")
		if phaseErr != nil || !w.groupDrained || w.ordinaryAccepts != len(ordinary.Texts) {
			t.Errorf("normal group did not drain its full ordinary corpus: accepts=%d drained=%t err=%v", w.ordinaryAccepts, w.groupDrained, phaseErr)
		}
		p := w.s.parents[0]
		if len(w.calls[p.id]) != 3 || len(w.groupGates) != 3 || len(w.s.drivers) != 3 || !w.groupReleased("tool-3", "release-ordinary") {
			t.Error("normal group did not exercise and release all three independent foreground gates")
		}
		var observations []q.Observation
		replayDecode(t, filepath.Join(w.s.r.Evidence.Root, "raw/qualification-observations.json"), &observations)
		for _, mode := range []string{"steer_turn", "wait_turn"} {
			complete := 0
			heldBefore := false
			label := "group-all-accepted-before-boundary"
			if mode == "wait_turn" {
				label = "group-ordinary-all-accepted-before-boundary"
			}
			plan, _ := q.PlanGroup(mode)
			for _, ob := range observations {
				if ob.Kind == "group-admission" && ob.Mode == mode && ob.State == "complete" && ob.Trigger != "" {
					complete++
				}
				if ob.Case == label && ob.Snapshot != "" {
					var before runner.Snapshot
					replayDecode(t, filepath.Join(w.s.r.Evidence.Root, ob.Snapshot), &before)
					n := 0
					for _, text := range plan.Texts {
						for _, m := range before.Tables["messages"] {
							if runner.Text(m, "text") == text && runner.Text(m, "place") == "held" {
								n++
							}
						}
					}
					heldBefore = n == len(plan.Texts)
				}
			}
			if complete != 1 || !heldBefore {
				t.Errorf("%s group lacks complete held admission evidence before release: complete=%d held=%t", mode, complete, heldBefore)
			}
		}
		for _, id := range []string{"G0", "O1", "O2"} {
			n := 0
			for _, ch := range ledger.Charges {
				if ch.ID == id {
					n += ch.Actual
				}
			}
			if n != 1 {
				t.Errorf("normal group %s starts=%d; want one", id, n)
			}
		}
		snap, err := runner.Observe(context.Background(), w.s.db(), w.s.parents[0].id)
		if err != nil {
			t.Fatal(err)
		}
		for _, mode := range []string{"steer_turn", "wait_turn"} {
			plan, _ := q.PlanGroup(mode)
			for _, part := range plan.Parts {
				var want [][]byte
				for _, text := range plan.Texts[part.First : part.First+part.Count] {
					want = append(want, []byte(text))
				}
				var handover string
				texts := map[string]string{}
				for _, row := range snap.Tables["messages"] {
					mid, text := runner.Text(row, "message_id"), runner.Text(row, "text")
					texts[mid] = text
					if text == plan.Texts[part.First] {
						handover = runner.Text(row, "handover_id")
					}
				}
				var got [][]byte
				for _, row := range snap.Tables["members"] {
					if runner.Text(row, "handover_id") == handover {
						got = append(got, []byte(texts[runner.Text(row, "message_id")]))
					}
				}
				if handover == "" || !bytes.Equal(store.RenderBusySendPromptBody(got), store.RenderBusySendPromptBody(want)) {
					t.Errorf("%s partition at %d was split or incomplete: members=%d want=%d", mode, part.First, len(got), part.Count)
				}
			}
		}
	}
	for _, ch := range ledger.Charges {
		if strings.HasPrefix(ch.ID, "unexpected-") && ch.Actual > 0 {
			t.Errorf("group introduced unreserved start: %+v", ch)
		}
	}
}

func (w *workloadReplay) assertUnused() {
	w.s.t.Helper()
	for _, argv := range w.s.inputs {
		id, text := replayArg(argv, "--delivery-request-id"), replayArg(argv, "--text")
		if strings.HasPrefix(text, "TYPED-idle") {
			w.s.t.Error("unused idle input was sent after a workload deviation")
		}
		if w.tc.phase == "typed-controls" && w.tc.kind == "early-Stop" {
			role := w.tc.role
			if role == "native" {
				role = "control"
			}
			targetID := replayArg(argv, "--instance-id")
			if argv[0] == "tmux" {
				targetID = replayArg(argv, "-t")
				text = argv[len(argv)-1]
			}
			target := targetID == w.s.parent(role).id
			if target && w.tc.at == "before-long" && strings.HasPrefix(text, "TYPED-") {
				w.s.t.Error("input sent after the target ended before its long-tool window")
			}
			if target && (w.tc.at == "before-between" || w.tc.at == "after-long") && strings.HasPrefix(text, "TYPED-between-tools") {
				w.s.t.Error("between-tools input sent after target already ended")
			}
		}
		if w.tc.phase == "force-states" && w.tc.kind != "tail-control" && id == "F4" {
			w.s.t.Error("unused F4 was sent after force workload ended")
		}
		if w.tc.kind == "missing-plan" && id == "D3" {
			w.s.t.Error("D3 sent without an actual plan")
		}
		if w.tc.kind == "missing-origin" && strings.HasPrefix(text, "Qualification Dialog probe") {
			w.s.t.Error("Dialog probe sent without its requested origin")
		}
		if w.tc.kind == "E3-missing" && (id == "Esc-steer-2" || id == "E4") {
			w.s.t.Error("Esc continued after missing E3 recovery tools")
		}
	}
	if strings.HasPrefix(w.tc.kind, "window-") || w.tc.kind == "missing-second" {
		_, err := os.Stat(filepath.Join(w.s.r.Evidence.Root, "work-G0/release-second"))
		if w.tc.kind == "window-serial" {
			if err != nil {
				w.s.t.Error("serial foreground control did not reach the true second window")
			}
		} else if err == nil {
			w.s.t.Error("false tool window released the second fixture / advanced the producer")
		}
	}
}

func (w *workloadReplay) assertA5(ledger q.Ledger, v runner.Verdict) {
	var data struct {
		A5 struct {
			Injection       time.Time    `json:"injection"`
			Frozen          string       `json:"frozen_input"`
			Journal         []runner.Row `json:"journal"`
			OriginalSession string       `json:"original_session"`
		} `json:"a5_workload"`
	}
	replayDecode(w.s.t, "testdata/qualification-replay-schedules.json", &data)
	var raw bytes.Buffer
	for _, row := range data.A5.Journal {
		if err := json.NewEncoder(&raw).Encode(row); err != nil {
			w.s.t.Fatal(err)
		}
	}
	transcript, err := signals.ParseTranscript(raw.Bytes(), data.A5.OriginalSession)
	if err != nil {
		w.s.t.Fatal(err)
	}
	body := string(store.RenderBusySendPromptBody([][]byte{[]byte(data.A5.Frozen)}))
	whole := q.CheckWholeInput(q.WholeInput{Body: body, Mode: "wait_turn", After: data.A5.Injection.UnixMilli(), Transcript: transcript, Evidence: []string{"testdata/qualification-replay-schedules.json"}})
	if whole.Status != "pass" {
		w.s.t.Errorf("a5's recorded complete wait-turn intake was lost: %+v", whole)
	}
	p := w.s.parent("ad")
	projected, err := signals.ParseTranscript(replayRead(w.s.t, p.transcript), p.session)
	if err != nil {
		w.s.t.Fatal(err)
	}
	intake := q.CheckWholeInput(q.WholeInput{Body: body, Mode: "wait_turn", After: w.a5Time(w.a5.Injection).UnixMilli(), Transcript: projected, Evidence: []string{p.transcript}})
	if intake.Status != "pass" {
		w.s.t.Errorf("producer projection did not retain a5's whole wait-turn intake: %+v", intake)
	}
	for _, c := range ledger.Charges {
		if strings.HasPrefix(c.ID, "unexpected-") {
			w.s.t.Errorf("a5 Stop fallback still creates unexpected charge %s", c.ID)
		}
	}
	reserved := 0
	for _, c := range ledger.Charges {
		if c.Phase == "typed-controls" && c.Reserved == 1 && c.Actual == 1 && c.Launch == w.s.parent("ad").token {
			reserved++
		}
	}
	if reserved != 2 {
		w.s.t.Errorf("a5 AD startup plus exactly one reserved fallback occurrence expected; got %d", reserved)
	}
	for _, id := range []string{"typed-between-tools", "typed-idle"} {
		if !slices.ContainsFunc(v.Results, func(r runner.Result) bool { return r.SignalID == id && r.Status == "not_completed" }) {
			w.s.t.Errorf("a5's unmeasured %s gap was not retained", id)
		}
	}
}
