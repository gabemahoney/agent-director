package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// world answers a rig's agent-director and tmux calls for one fake agent
// per spawn: the row, its labelled session and pane, and the process that
// goes away once its pane is killed.
type world struct {
	r          *rig
	socket     string
	state      string
	preTrust   string
	spawnCode  int
	endedAt    *time.Time
	listedFor  int // session-listing polls that still list the session
	spawns     int
	killed     bool
	sessionsOK int
}

// newWorld wires a world into r.
func newWorld(r *rig) *world {
	w := &world{r: r, socket: privateSocket(r.h.iso.TmuxTmpdir), state: stateWaiting, preTrust: "ok"}
	r.ex.reply = w.reply
	r.h.procs = fakeProcs(func(int) (string, bool, bool) { return "start-1", !w.killed, true })
	return w
}

func (w *world) id() string { return fmt.Sprintf("id-%d", w.spawns) }

func (w *world) reply(argv []string) (string, string, int) {
	joined := strings.Join(argv, " ")
	switch {
	case argv[0] == "agent-director" && argv[1] == "spawn":
		if w.spawnCode != 0 {
			return "", "spawn failed", w.spawnCode
		}
		w.spawns++
		return fmt.Sprintf(`{"claude_instance_id":%q,"pre_trust":%q}`, w.id(), w.preTrust), "", 0
	case argv[0] == "agent-director" && argv[1] == "get":
		row := map[string]any{"claude_instance_id": w.id(), "state": w.state, "tmux_socket": w.socket, "tmux_session_name": "ad-s", "ended_at": w.endedAt}
		b, _ := json.Marshal(row)
		return string(b), "", 0
	case strings.Contains(joined, "list-sessions -F #{session_id}\t#{@ad_owner}"):
		return "$9\tad1 other $9 id-other store-x\n$1\tad1 tok1 $1 " + w.id() + " store-abc123\n", "", 0
	case strings.Contains(joined, "list-panes"):
		return "%0\t111\tother %0\n%1\t4242\ttok1 %1\n", "", 0
	case strings.Contains(joined, "kill-pane"):
		w.killed = true
		return "", "", 0
	case strings.Contains(joined, "list-sessions -F #{session_id}"):
		if w.sessionsOK < w.listedFor {
			w.sessionsOK++
			return "$1\n", "", 0
		}
		return "", "no server running on " + w.socket, 1
	}
	return "", "", 0
}

func TestSpawnAgent(t *testing.T) {
	t.Run("labels, extra env and claude args", func(t *testing.T) {
		r := newRig(t, modeDry)
		newWorld(r)
		a, err := r.h.spawnAgent("rn6.idle", spawnSpec{CWD: "/w/0", ClaudeArgs: []string{"prompt", "--mcp-config", "/m.json"},
			ExtraEnv: map[string]string{sessionEndBudgetEnv: "3000"}})
		if err != nil || a.InstanceID != "id-1" || a.Socket != privateSocket(r.h.iso.TmuxTmpdir) {
			t.Fatalf("agent %+v, %v", a, err)
		}
		want := []string{"agent-director", "spawn", "--cwd", "/w/0", "--label", "mx_run=mx-test-run", "--label", "mx_case=rn6.idle",
			"--extra-env", sessionEndBudgetEnv + "=3000", "--", "prompt", "--mcp-config", "/m.json"}
		if !reflect.DeepEqual(r.ex.calls[0], want) {
			t.Errorf("spawn argv %q", r.ex.calls[0])
		}
		if r.h.iso.RecordedSocket != a.Socket || r.h.res.Isolation.RecordedSocket != a.Socket || r.ids.String() != "instance_id id-1\nsocket "+a.Socket+"\n" {
			t.Errorf("recorded socket %q, in results %q, identifiers %q", r.h.iso.RecordedSocket, r.h.res.Isolation.RecordedSocket, r.ids.String())
		}
	})
	t.Run("a socket outside the private TMUX_TMPDIR aborts", func(t *testing.T) {
		r := newRig(t, modeDry)
		w := newWorld(r)
		w.socket = "/tmp/tmux-1000/default"
		_, err := r.h.spawnAgent("rn6.idle", spawnSpec{CWD: "/w/0"})
		assertRefused(t, err, rulePrivateSocket)
	})
	t.Run("pre_trust not ok fails the sample", func(t *testing.T) {
		r := newRig(t, modeDry)
		w := newWorld(r)
		w.preTrust = "failed"
		a, err := r.h.spawnAgent("rn6.idle", spawnSpec{CWD: "/w/0"})
		var ref *refusal
		if err == nil || errors.As(err, &ref) || a.InstanceID != "id-1" {
			t.Fatalf("agent %+v, %v", a, err)
		}
	})
	t.Run("a forbidden extra variable is refused before spawning", func(t *testing.T) {
		r := newRig(t, modeDry)
		newWorld(r)
		if _, err := r.h.spawnAgent("rn6.idle", spawnSpec{CWD: "/w/0", ExtraEnv: map[string]string{"CLAUDE_CONFIG_DIR": "/c"}}); err == nil || len(r.ex.calls) != 0 {
			t.Fatalf("err %v, calls %q", err, r.ex.calls)
		}
	})
}

func TestIdentifyFindsTheLabelledPane(t *testing.T) {
	r := newRig(t, modeDry)
	w := newWorld(r)
	w.spawns = 1
	a := agentRef{InstanceID: "id-1", Socket: w.socket}
	if err := r.h.identify("rn6.idle", &a); err != nil {
		t.Fatal(err)
	}
	if a.TmuxSessionID != "$1" || a.PaneID != "%1" || a.PID != 4242 || a.StartTime != "start-1" || a.StoreID != "store-abc123" {
		t.Errorf("agent %+v", a)
	}
	if !strings.Contains(r.ids.String(), "store_id store-abc123\n") {
		t.Errorf("identifiers %q", r.ids.String())
	}
}

func TestMeasureKill(t *testing.T) {
	r := newRig(t, modeDry)
	w := newWorld(r)
	polls := 0
	r.h.procs = fakeProcs(func(int) (string, bool, bool) {
		polls++
		return "start-1", polls <= 2 || !w.killed, true
	})
	a := agentRef{InstanceID: "id-1", Socket: w.socket, PaneID: "%1", PID: 4242, StartTime: "start-1"}
	var s sample
	measureKill(r.h, "rn6.idle", a, &s)
	if s.Outcome != outcomeCompleted || *s.Millis != 200 || s.Polls != 3 {
		t.Fatalf("sample %+v", s)
	}
	if r.ex.count("tmux", "-S", w.socket, "kill-pane", "-t", "%1") != 1 || !strings.Contains(r.log.String(), `"kind":"measured"`) {
		t.Errorf("calls %q, log %s", r.ex.calls, r.log)
	}
}

func TestMeasureEndedToGone(t *testing.T) {
	ended := clockStart.Add(-2 * time.Second)
	for _, tc := range []struct {
		name       string
		endedAt    *time.Time
		triggerErr error
		outcome    outcome
		millis     int64
		reason     string
	}{
		{"time from ended_at to the poll that saw it gone", &ended, nil, outcomeCompleted, 2200, ""},
		{"no ended_at", nil, nil, outcomeNoEndedAt, 0, "no ended_at (no SessionEnd applied); state ended; ignored hooks: no_exec_form"},
		{"a failed trigger with ended_at", &ended, errors.New("pause: exit 1"), outcomeCompleted, 2200, "trigger: pause: exit 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, modeDry)
			w := newWorld(r)
			w.spawns, w.state, w.endedAt, w.listedFor = 1, stateEnded, tc.endedAt, 2
			writeFile(t, filepath.Join(r.h.iso.Home, ".agent-director", "ad-trail.jsonl"),
				`{"event":"ad.hook.ignored","claude_instance_id":"id-1","reason":"no_exec_form"}`+"\n")
			s := sample{Case: "rn2.pause"}
			r.h.measureEndedToGone("rn2.pause", agentRef{InstanceID: "id-1", Socket: w.socket, TmuxSessionID: "$1"}, &s, tc.triggerErr)
			if s.Outcome != tc.outcome || !strings.Contains(s.Reason, tc.reason) || s.Polls != 3 {
				t.Fatalf("sample %+v", s)
			}
			if (s.Millis != nil) != (tc.millis != 0) || (s.Millis != nil && *s.Millis != tc.millis) {
				t.Errorf("millis %v, want %d", s.Millis, tc.millis)
			}
		})
	}
}

func TestRaisedBudgetVariants(t *testing.T) {
	r := newRig(t, modeDry)
	if r.h.raisedLayer(budgetDefault) != nil || r.h.raisedEnv(budgetDefault) != nil || r.h.raisedEnv(budgetRaisedHook) != nil {
		t.Error("the default budget or the hook variant set something it must not")
	}
	if got := r.h.raisedEnv(budgetRaisedEnv); !reflect.DeepEqual(got, map[string]string{sessionEndBudgetEnv: "3000"}) {
		t.Errorf("env variant %v", got)
	}
	for v, wantTimeout := range map[budgetVariant]bool{budgetRaisedHook: true, budgetRaisedEnv: false} {
		b, _ := json.Marshal(r.h.raisedLayer(v))
		var doc struct {
			Hooks map[string][]struct {
				Hooks []map[string]any `json:"hooks"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatal(err)
		}
		hook := doc.Hooks["SessionEnd"][0].Hooks[0]
		_, hasTimeout := hook["timeout"]
		if filepath.Base(hook["command"].(string)) != "sleep" || !reflect.DeepEqual(hook["args"], []any{"2.000"}) || hasTimeout != wantTimeout {
			t.Errorf("variant %d hook %v", v, hook)
		}
		if wantTimeout && hook["timeout"] != float64(3) {
			t.Errorf("raised timeout %v", hook["timeout"])
		}
	}
}

// caseByID returns the registered case.
func caseByID(t *testing.T, id string) caseSpec {
	t.Helper()
	for _, c := range caseRegistry {
		if c.id == id {
			return c
		}
	}
	t.Fatalf("case %s is not registered", id)
	return caseSpec{}
}

func TestSampledCaseRunsEndToEnd(t *testing.T) {
	for _, tc := range []struct {
		id         string
		state      string
		wantArgs   []string
		generated  bool
		mcpServers []string
	}{
		{"rn6.mcp", stateWaiting, []string{"--", "--mcp-config"}, false, []string{"mx-dry-noop"}},
		{"rn6.midturn.raised-env", stateWorking, []string{"--extra-env", sessionEndBudgetEnv + "=3000", "--", defaultMidTurnPrompt}, true, nil},
		{"rn6.idle.raised-hook", stateWaiting, nil, true, nil},
	} {
		t.Run(tc.id, func(t *testing.T) {
			r := newRig(t, modeDry)
			r.h.cfg.samples = 1
			w := newWorld(r)
			w.state = tc.state
			res, err := caseByID(t, tc.id).run(r.h)
			if err != nil {
				t.Fatal(err)
			}
			res.finalize()
			if res.Completed != 1 || !reflect.DeepEqual(res.MCPServers, tc.mcpServers) {
				t.Fatalf("result %+v", res)
			}
			spawn := strings.Join(r.ex.calls[0], " ")
			for _, a := range tc.wantArgs {
				if !strings.Contains(spawn, a) {
					t.Errorf("spawn %q lacks %q", spawn, a)
				}
			}
			_, err = os.Stat(generatedLayerPath(filepath.Join(r.h.iso.WorkDir, tc.id, "0")))
			if (err == nil) != tc.generated {
				t.Errorf("generated layer present=%t, want %t", err == nil, tc.generated)
			}
		})
	}
}

func TestRunSampledAbortsAfterSetupFailures(t *testing.T) {
	r := newRig(t, modeDry)
	r.h.cfg.samples = 10
	w := newWorld(r)
	w.spawnCode = 1
	res, err := caseByID(t, "rn6.idle").run(r.h)
	if err == nil || !strings.Contains(err.Error(), "3 samples in a row") {
		t.Fatalf("err %v", err)
	}
	if len(res.Samples) != maxSetupFailures || r.ex.count("agent-director", "spawn") != maxSetupFailures {
		t.Errorf("samples %d, spawns %d", len(res.Samples), r.ex.count("agent-director", "spawn"))
	}
}

func TestMCPCaseNotRunInRealModeWithoutConfig(t *testing.T) {
	r := newRig(t, modeReal)
	newWorld(r)
	res, err := caseByID(t, "rn2.mcp").run(r.h)
	if err != nil || !strings.Contains(res.NotRun, "no MCP configuration") || len(r.ex.calls) != 0 {
		t.Fatalf("result %+v, err %v, calls %q", res, err, r.ex.calls)
	}
}

func TestCaseRegistryMatchesDecide(t *testing.T) {
	var sampled, rn9 []string
	for _, c := range caseRegistry {
		switch {
		case c.sampled:
			sampled = append(sampled, c.id)
			if !reflect.DeepEqual(c.modes, []mode{modeReal, modeDry}) || c.generated != strings.Contains(c.id, ".raised-") {
				t.Errorf("sampled case %s: modes %v generated %t", c.id, c.modes, c.generated)
			}
		case c.family == familyRN9 && c.id != probeCaseID:
			rn9 = append(rn9, c.id)
		}
	}
	want := append(append(append([]string{}, rn6DefaultCases...), rn6RaisedCases...), rn2Cases...)
	sort.Strings(sampled)
	sort.Strings(want)
	if !reflect.DeepEqual(sampled, want) {
		t.Errorf("sampled cases %v, decide reads %v", sampled, want)
	}
	if !reflect.DeepEqual(rn9, rn9ScenarioIDs) {
		t.Errorf("RN-9 scenarios %v, decide reads %v", rn9, rn9ScenarioIDs)
	}
	if p := caseByID(t, probeCaseID); !reflect.DeepEqual(p.modes, []mode{modeProbe, modeDry}) || p.sampled {
		t.Errorf("probe case %+v", p)
	}
}

func TestSelectCases(t *testing.T) {
	got, err := selectCases(caseRegistry, nil, modeProbe)
	if err != nil || len(got) != 1 || got[0].id != probeCaseID {
		t.Errorf("probe mode default selection %v, %v", got, err)
	}
	if got, err := selectCases(caseRegistry, nil, modeDry); err != nil || len(got) != len(caseRegistry) {
		t.Errorf("dry mode default selection: %d of %d cases, %v", len(got), len(caseRegistry), err)
	}
	for _, tc := range []struct {
		ids []string
		m   mode
	}{{[]string{"rn6.nope"}, modeDry}, {[]string{probeCaseID}, modeReal}, {[]string{"rn6.idle"}, modeProbe}, {nil, modeReal}} {
		if _, err := selectCases(caseRegistry, tc.ids, tc.m); !errors.Is(err, errUsage) {
			t.Errorf("%v in %s: %v", tc.ids, tc.m, err)
		}
	}
	// Real mode without -cases is a usage error before anything is written.
	out := filepath.Join(t.TempDir(), "out")
	var stdout, stderr bytes.Buffer
	if code := dispatch([]string{"run", "-mode", "real", "-out", out}, &stdout, &stderr); code != exitUsage || !strings.Contains(stderr.String(), "explicit -cases") {
		t.Errorf("run -mode real without -cases: exit %d:\n%s", code, stderr.String())
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("a refused real run created its results directory")
	}
}

func TestAgentDirsAreFreshAndHarnessOwned(t *testing.T) {
	r := newRig(t, modeDry)
	project := filepath.Join(t.TempDir(), "project.json")
	writeFile(t, project, `{"model":"x"}`)
	r.h.cfg.projectSettings = project
	dir, err := r.h.prepareAgentDir("rn6.idle", 0, map[string]any{"hooks": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if readFile(t, projectLayerPath(dir)) != `{"model":"x"}` || readFile(t, generatedLayerPath(dir)) == "" {
		t.Error("the agent dir lacks its project copy or generated layer")
	}
	if _, err := r.h.sampleWorkDir("rn6.idle", 0); err == nil {
		t.Error("an existing working directory was reused")
	}
	if err := writeGeneratedLayer(dir, map[string]any{}); err == nil {
		t.Error("a generated layer overwrote an existing local layer")
	}
	r.h.cfg.localSettings = project
	if _, err := r.h.prepareAgentDir("rn6.idle", 1, map[string]any{}); err == nil {
		t.Error("a generated layer was placed beside the deployment local layer")
	}
}

func TestIdentifiersFile(t *testing.T) {
	var b bytes.Buffer
	w := newIDWriter(&b)
	for _, v := range []string{"mx-run-0001", "mx-run-0001", "/tmp/mx-tmux-1"} {
		if err := w.add(idRun, v); err != nil {
			t.Fatal(err)
		}
	}
	if b.String() != "run_id mx-run-0001\nrun_id /tmp/mx-tmux-1\n" {
		t.Errorf("identifiers %q", b.String())
	}
	for _, bad := range []string{"", "has space", "two\nlines"} {
		if err := w.add(idInstance, bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}
