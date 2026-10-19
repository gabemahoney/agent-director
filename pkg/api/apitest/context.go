package apitest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/codexfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/contextfix"
)

// ContextWriter identifies a live test-owned process kept alive for context reads.
func ContextWriter(pid int) (store.LaunchIdentity, error) {
	start, alive, known := probe.NewProcChecker().StartTime(pid)
	if !known || !alive || start == "" {
		return store.LaunchIdentity{}, fmt.Errorf("context writer %d is not provably live", pid)
	}
	return store.LaunchIdentity{Token: "a1a1a1a1a1a1a1a1", Socket: TestSocket, PaneID: TestPaneID, PanePID: pid, PaneStarttime: start}, nil
}

// AdmitContextStartup uses the same unbound-session admission as a native startup.
func AdmitContextStartup(dbPath, id, session, path string, pid int) error {
	identity, err := ContextWriter(pid)
	if err != nil {
		return err
	}
	s, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer s.Close()
	sp, err := s.GetSpawn(id)
	if err != nil {
		return err
	}
	g := store.HookGate{Event: "SessionStart", SessionStart: true, CodexStartup: true, Examined: sp.Snapshot, SessionID: session, ParentPID: pid, ParentStart: identity.PaneStarttime}
	a, changed, err := s.RecordSessionStartIdentity(id, g, path, true)
	if err == nil && (!a.Applied || changed) {
		return fmt.Errorf("context startup not admitted: %+v changed=%v", a, changed)
	}
	return err
}

// SeedContextSpawn keeps Codex's test writer alive and admits its fresh startup.
func SeedContextSpawn(t *testing.T, dbPath, id string, createStore bool, sample contextfix.Sample, extra ...SpawnOption) (string, string) {
	t.Helper()
	path := SeedContextTranscript(t, sample)
	state, session := store.StateEnded, sample.SessionID
	opts := []SpawnOption{WithAgent(sample.Agent), WithJsonlPath(path)}
	if sample.Agent == store.AgentCodex {
		identity, err := ContextWriter(os.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		state, session = store.StatePending, ""
		opts = append(opts, WithLaunchIdentity(identity))
	}
	if sample.Agent == store.AgentClaude && len(sample.Callback) > 0 {
		identity, err := ContextWriter(os.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		state = store.StateWaiting
		opts = append(opts, WithLaunchIdentity(identity), WithPID(identity.PanePID), WithProcStarttime(identity.PaneStarttime))
	}
	id, err := SeedSpawn(dbPath, id, state, t.TempDir(), "off", session, createStore, append(opts, extra...)...)
	if err != nil {
		t.Fatal(err)
	}
	if sample.Agent == store.AgentCodex {
		var configured spawnOpts
		for _, opt := range append(opts, extra...) {
			opt(&configured)
		}
		if err = AdmitContextStartup(dbPath, id, sample.SessionID, configured.cols["jsonl_path"].(string), configured.cols["pane_pid"].(int)); err != nil {
			t.Fatal(err)
		}
	}
	if sample.Agent == store.AgentClaude && len(sample.Callback) > 0 {
		SeedClaudeCapacity(t, dbPath, id, sample)
	}
	return id, path
}

// SeedClaudeCapacity installs captured capacity provenance without caching usage.
func SeedClaudeCapacity(t testing.TB, dbPath, id string, sample contextfix.Sample) {
	t.Helper()
	if err := AdmitClaudeCapacity(dbPath, id, sample); err != nil {
		t.Fatal(err)
	}
}

// AdmitClaudeCapacity shares captured-capacity setup with the Bun seed command.
func AdmitClaudeCapacity(dbPath, id string, sample contextfix.Sample) error {
	s, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer s.Close()
	sp, err := s.GetSpawn(id)
	if err != nil {
		return err
	}
	var callback struct {
		Model  struct{ ID string } `json:"model"`
		Window struct {
			Size int64 `json:"context_window_size"`
		} `json:"context_window"`
		Tasks []struct {
			ID, Model string
			Window    int64 `json:"contextWindowSize"`
		} `json:"tasks"`
	}
	if err = json.Unmarshal(sample.Callback, &callback); err != nil {
		return err
	}
	p := sp.ClaudeCapacity
	if p == nil {
		p = &store.ClaudeCapacity{LaunchToken: sp.Identity.Token, SessionID: sp.SessionID, JSONLPath: sp.JSONLPath, PID: sp.PID, Starttime: sp.ProcStarttime, Version: "2.1.280", Provider: "anthropic", Children: map[string]store.ClaudeWindow{}}
	}
	w := store.ClaudeWindow{Model: callback.Model.ID, Window: callback.Window.Size, ResponseUUID: sample.ResponseUUID, ObservedAt: sample.ObservedAt}
	if sample.ChildID == "" {
		p.Main = &w
	} else {
		for _, task := range callback.Tasks {
			if task.ID == sample.ChildID {
				w.Model, w.Window = task.Model, task.Window
				p.Children[sample.ChildID] = w
			}
		}
	}
	if applied, err := s.RecordClaudeCapacity(id, store.HookGate{ParentPID: sp.PID, ParentStart: sp.ProcStarttime, SessionID: sp.SessionID}, *p); err != nil || !applied {
		return fmt.Errorf("capacity seed: %v %v", applied, err)
	}
	return nil
}

// SeedContextChild uses the captured native child filename under its parent.
func SeedContextChild(t testing.TB, dbPath, id string, sample contextfix.Sample, opts ...SubagentOption) string {
	t.Helper()
	path, err := AdmitClaudeChild(dbPath, id, sample, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// AdmitClaudeChild shares native child setup with the Bun seed command.
func AdmitClaudeChild(dbPath, id string, sample contextfix.Sample, opts ...SubagentOption) (string, error) {
	s, err := store.Open(dbPath)
	if err != nil {
		return "", err
	}
	parent, err := s.GetSpawn(id)
	s.Close()
	if err != nil {
		return "", err
	}
	path := filepath.Join(filepath.Dir(parent.JSONLPath), sample.SessionID, "subagents", "agent-"+sample.ChildID+".jsonl")
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	if err = os.WriteFile(path, sample.Transcript, 0600); err != nil {
		return "", err
	}
	if err = SeedSubagent(dbPath, id, sample.ChildID, append([]SubagentOption{WithSubagentSessionID(sample.SessionID)}, opts...)...); err != nil {
		return "", err
	}
	return path, AdmitClaudeCapacity(dbPath, id, sample)
}

// SeedContextTranscript writes a named capture using the agent's existing path fixture.
func SeedContextTranscript(t *testing.T, sample contextfix.Sample) string {
	t.Helper()
	if sample.Agent != "claude" {
		return codexfix.TempRolloutPath(t, sample.SessionID, true, sample.Transcript)
	}
	path := SeedJsonlUnder(t, t.TempDir(), t.TempDir(), sample.SessionID)
	if err := os.WriteFile(path, sample.Transcript, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
