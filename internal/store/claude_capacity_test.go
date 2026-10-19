package store

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func capacityRow(t *testing.T) (*Store, string, string, ClaudeCapacity) {
	t.Helper()
	s, path := openTempStore(t)
	const id = "claude-capacity"
	if err := insertAgentRow(s, Spawn{InstanceID: id, Agent: AgentClaude, CWD: t.TempDir(), RelayMode: "off"}); err != nil {
		t.Fatal(err)
	}
	if a, err := fireSessionStart(s, id, agentGate("SessionStart", "fresh-session"), "/tmp/fresh-session.jsonl", true); err != nil || !a.Applied {
		t.Fatalf("startup: %+v %v", a, err)
	}
	var p ClaudeCapacity
	if err := json.Unmarshal([]byte(capacityJSON(t, nil)), &p); err != nil {
		t.Fatal(err)
	}
	return s, path, id, p
}

func TestClaudeCapacityStoreCurrentLaunchAdmission(t *testing.T) {
	for _, name := range []string{"accepted", "stale snapshot", "foreign parent", "wrong parent start", "launch", "session", "path", "pid", "process start", "ended", "pending", "another agent"} {
		t.Run(name, func(t *testing.T) {
			s, _, id, p := capacityRow(t)
			g := agentGate("", p.SessionID)
			snapshot := "null"
			g.ClaudeCapacitySnapshot = &snapshot
			switch name {
			case "stale snapshot":
				snapshot = "{}"
			case "foreign parent":
				g.ParentPID++
			case "wrong parent start":
				g.ParentStart = "foreign"
			case "launch":
				p.LaunchToken = "b2b2b2b2b2b2b2b2"
			case "session":
				p.SessionID = "other"
			case "path":
				p.JSONLPath = "/tmp/other.jsonl"
			case "pid":
				p.PID++
			case "process start":
				p.Starttime = "foreign"
			case "ended":
				mustExec(t, s.db, `UPDATE spawns SET state='ended'`)
			case "pending":
				mustExec(t, s.db, `UPDATE spawns SET state='pending'`)
			case "another agent":
				mustExec(t, s.db, `UPDATE spawns SET agent='codex'`)
			}
			before := dbDump(t, s.Path())
			applied, err := s.RecordClaudeCapacity(id, g, p)
			if err != nil || applied != (name == "accepted") {
				t.Fatalf("admitted=%v err=%v", applied, err)
			}
			if name != "accepted" && !reflect.DeepEqual(before, dbDump(t, s.Path())) {
				t.Fatal("refused callback changed store")
			}
		})
	}
}

func TestClaudeCapacityPersistsAndInvalidatesAcrossLaunches(t *testing.T) {
	for _, name := range []string{"reopen", "session start", "resume", "reuse"} {
		t.Run(name, func(t *testing.T) {
			s, path, id, p := capacityRow(t)
			if ok, err := s.RecordClaudeCapacity(id, agentGate("", p.SessionID), p); err != nil || !ok {
				t.Fatalf("capacity: %v %v", ok, err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			sp, err := s.GetSpawn(id)
			if err != nil || !reflect.DeepEqual(sp.ClaudeCapacity, &p) || sp.ContextEvidence().CurrentClaudeCapacity() == nil {
				t.Fatalf("reopen lost capacity provenance: %+v %v", sp.ClaudeCapacity, err)
			}
			if name == "reopen" {
				return
			}
			if name == "session start" {
				if a, err := fireSessionStart(s, id, agentGate("SessionStart", p.SessionID), p.JSONLPath, true); err != nil || !a.Applied {
					t.Fatalf("session start: %+v %v", a, err)
				}
			} else {
				if err := agentHook(s, id, StateEnded, false, "SessionEnd"); err != nil {
					t.Fatal(err)
				}
				sp, err = s.GetSpawn(id)
				if err != nil {
					t.Fatal(err)
				}
				if name == "resume" {
					if r, _, err := s.MoveToPending(id, sp.Snapshot, time.Now().UnixMilli(), "b2b2b2b2b2b2b2b2", sp.Identity.Socket, "", LaunchOwner{}); err != nil || r != CondApplied {
						t.Fatalf("resume: %v %v", r, err)
					}
				} else {
					fresh := Spawn{Agent: AgentClaude, CWD: t.TempDir(), RelayMode: "off", StartedAt: time.Now(), LaunchStartedAtMillis: time.Now().UnixMilli(), Identity: LaunchIdentity{Token: "b2b2b2b2b2b2b2b2"}}
					if r, _, _, err := s.ResetForReuse(id, sp.Snapshot, sp.Agent, fresh); err != nil || r != CondApplied {
						t.Fatalf("reuse: %v %v", r, err)
					}
				}
			}
			sp, err = s.GetSpawn(id)
			if err != nil || sp.ClaudeCapacity != nil {
				t.Fatalf("new launch/session retained capacity: %+v %v", sp.ClaudeCapacity, err)
			}
			if name == "session start" {
				return
			}
			if ok, err := s.RecordClaudeCapacity(id, agentGate("", p.SessionID), p); err != nil || ok {
				t.Fatalf("old launch restored capacity: %v %v", ok, err)
			}
		})
	}
}
