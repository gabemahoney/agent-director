package store

import (
	"reflect"
	"testing"
	"time"
)

func startupRow(t *testing.T) (*Store, string, string) {
	t.Helper()
	s, path := openTempStore(t)
	if err := insertAgentRow(s, Spawn{InstanceID: "fresh-codex", Agent: AgentCodex, CWD: t.TempDir(), RelayMode: "off"}); err != nil {
		t.Fatal(err)
	}
	return s, path, "fresh-codex"
}

func admitStartup(t *testing.T, s *Store, id string) Spawn {
	t.Helper()
	g := agentGate("SessionStart", "fresh-session")
	g.CodexStartup = true
	a, err := fireSessionStart(s, id, g, "/tmp/fresh-session.jsonl", true)
	if err != nil || !a.Applied {
		t.Fatalf("startup admission: %+v %v", a, err)
	}
	sp, err := s.GetSpawn(id)
	if err != nil || sp.CodexStartup == nil || !sp.ContextEvidence().FreshCodexWriter() {
		t.Fatalf("fresh writer: %+v %v", sp.CodexStartup, err)
	}
	return sp
}

func TestCodexStartupAdmission(t *testing.T) {
	for _, name := range []string{"fresh", "already bound", "unqualified source", "another event", "another agent", "foreign parent", "wrong parent start", "stale snapshot"} {
		t.Run(name, func(t *testing.T) {
			s, _, id := startupRow(t)
			g := agentGate("SessionStart", "fresh-session")
			g.CodexStartup, g.SessionStart = true, true
			switch name {
			case "already bound":
				mustExec(t, s.db, `UPDATE spawns SET session_id='existing-session'`)
			case "unqualified source":
				g.CodexStartup = false
			case "another event":
				g.Event = "Stop"
			case "another agent":
				mustExec(t, s.db, `UPDATE spawns SET agent='claude'`)
			case "foreign parent":
				g.ParentPID++
			case "wrong parent start":
				g.ParentStart = "another-start"
			}
			sp, err := s.GetSpawn(id)
			if err != nil {
				t.Fatal(err)
			}
			g.Examined = sp.Snapshot
			if name == "stale snapshot" {
				mustExec(t, s.db, `UPDATE spawns SET row_version=row_version+1`)
			}
			a, changed, err := s.RecordSessionStartIdentity(id, g, "/tmp/fresh-session.jsonl", true)
			if err != nil {
				t.Fatal(err)
			}
			sp, err = s.GetSpawn(id)
			if err != nil || (sp.CodexStartup != nil) != (name == "fresh") {
				t.Fatalf("startup=%+v %v (applied=%+v changed=%v)", sp.CodexStartup, err, a, changed)
			}
		})
	}
}

func TestCodexStartupCurrentWriterAndProjection(t *testing.T) {
	s, path, id := startupRow(t)
	sp := admitStartup(t, s, id)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reopened, err := s.GetSpawn(id)
	if err != nil || !reflect.DeepEqual(reopened.CodexStartup, sp.CodexStartup) {
		t.Fatalf("restart changed startup: %+v %v", reopened.CodexStartup, err)
	}
	_, _, _, projected, err := s.SpawnStatusContext(id, SettleRead{})
	if err != nil || !reflect.DeepEqual(projected, reopened.ContextEvidence()) {
		t.Fatalf("status projection=%+v %v; want %+v", projected, err, reopened.ContextEvidence())
	}
	for _, name := range []string{"launch", "session", "path", "pid", "process start", "pane pid", "pane start", "pending", "ended", "missing", "end report", "agent", "no startup"} {
		t.Run(name, func(t *testing.T) {
			e := projected
			switch name {
			case "launch":
				e.LaunchToken = "b2b2b2b2b2b2b2b2"
			case "session":
				e.SessionID = "other"
			case "path":
				e.JSONLPath = "/tmp/other.jsonl"
			case "pid":
				e.PID++
			case "process start":
				e.ProcStarttime = "other"
			case "pane pid":
				e.PanePID++
			case "pane start":
				e.PaneStarttime = "other"
			case "pending":
				e.State = StatePending
			case "ended":
				e.State = StateEnded
			case "missing":
				e.State = StateMissing
			case "end report":
				e.EndReportedAtMillis = 1
			case "agent":
				e.Agent = AgentClaude
			case "no startup":
				e.CodexStartup = nil
			}
			if e.FreshCodexWriter() {
				t.Fatal("changed authority still qualifies writer")
			}
		})
	}
}

func TestCodexStartupInvalidation(t *testing.T) {
	for _, name := range []string{"move to pending", "failed resume", "reuse", "failed reuse"} {
		t.Run(name, func(t *testing.T) {
			s, _, id := startupRow(t)
			sp := admitStartup(t, s, id)
			if err := agentHook(s, id, StateEnded, false, "SessionEnd"); err != nil {
				t.Fatal(err)
			}
			sp, _ = s.GetSpawn(id)
			if name == "reuse" || name == "failed reuse" {
				prior, found, err := s.ReadForReuse(id)
				if err != nil || !found {
					t.Fatalf("reuse read: %v %v", found, err)
				}
				fresh := Spawn{Agent: AgentCodex, CWD: t.TempDir(), RelayMode: "off", StartedAt: time.Now(), LaunchStartedAtMillis: time.Now().UnixMilli(), Identity: LaunchIdentity{Token: "b2b2b2b2b2b2b2b2"}}
				r, _, v, err := s.ResetForReuse(id, sp.Snapshot, sp.Agent, fresh)
				if err != nil || r != CondApplied {
					t.Fatalf("reuse: %v %v", r, err)
				}
				cleared, _ := s.GetSpawn(id)
				if cleared.CodexStartup != nil {
					t.Fatal("reuse retained startup")
				}
				if name == "failed reuse" {
					r, err = s.RestoreAfterFailedReuse(id, v, prior.Life, time.Now())
					if err != nil || r != CondApplied {
						t.Fatalf("restore reuse: %v %v", r, err)
					}
					got, _ := s.GetSpawn(id)
					if !reflect.DeepEqual(got.CodexStartup, sp.CodexStartup) {
						t.Fatal("failed reuse lost prior startup")
					}
					return
				}
			} else {
				r, v, err := s.MoveToPending(id, sp.Snapshot, time.Now().UnixMilli(), "b2b2b2b2b2b2b2b2", sp.Identity.Socket, "", LaunchOwner{})
				if err != nil || r != CondApplied {
					t.Fatalf("resume: %v %v", r, err)
				}
				if name == "failed resume" {
					r, err = s.RestoreAfterFailedResume(id, v, ResumePrior{State: sp.State, PID: sp.PID, ProcStarttime: sp.ProcStarttime, Identity: sp.Identity})
					if err != nil || r != CondApplied {
						t.Fatalf("restore resume: %v %v", r, err)
					}
				}
			}
			got, err := s.GetSpawn(id)
			if err != nil || got.CodexStartup != nil {
				t.Fatalf("invalidation: %+v %v", got.CodexStartup, err)
			}
			if name == "reuse" || name == "move to pending" {
				return
			}
			g := agentGate("SessionStart", sp.SessionID)
			g.CodexStartup = true
			if a, err := fireSessionStart(s, id, g, sp.JSONLPath, true); err != nil || !a.Applied {
				t.Fatalf("delayed startup: %+v %v", a, err)
			}
			got, _ = s.GetSpawn(id)
			if got.CodexStartup != nil {
				t.Fatal("delayed startup restored invalidated provenance")
			}
		})
	}
}
