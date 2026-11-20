package qualification_test

import (
	"encoding/json"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/test/claude-live/internal/qualificationfix"
	q "github.com/gabemahoney/agent-director/test/claude-live/qualification"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

func TestQualificationUncoveredOperationContracts(t *testing.T) {
	for _, operation := range []string{"force", "conditional-pause", "native-ordinary", "native-id-repeat", "raw-key", "busy-choice-wait_turn"} {
		for _, variant := range []string{"documented", "wrong-contract", "failed-operation"} {
			t.Run(operation+"/"+variant, func(t *testing.T) {
				f := qualificationfix.New(t, "uncovered")
				c := f.Capture()
				agent := q.UncoveredAgent{Kind: "codex", Instance: "unit-codex"}
				c.Owners = []q.OwnerEvidence{{Owner: runner.Owned{Agent: "codex", InstanceID: agent.Instance, SessionID: "unit-session", LaunchID: "unit-launch"}, Snapshot: runner.Snapshot{Tables: map[string][]runner.Row{"forces": {{"force_id": "unit-force", "instance_id": agent.Instance, "launch_id": "unit-launch", "outcome": "refused", "reason": "agent_not_covered"}}}}}}
				args := []string{"ad", "send-keys", "--instance-id", agent.Instance}
				exit := 0
				proof := api.SendKeysResult{RequestID: "uncovered-codex-ordinary", Outcome: "submitted", SessionID: "unit-session", RecordID: "unit-record", RecordSHA256: runner.Digest([]byte("unit-native-proof"))}
				var value any = proof
				switch operation {
				case "force":
					args[1] = "force-delivery"
					value = runner.Row{"force_result": runner.Row{"force_id": "unit-force", "outcome": "refused", "reason": "agent_not_covered"}}
					if variant == "wrong-contract" {
						value = runner.Row{"force_result": runner.Row{"force_id": "unit-force", "outcome": "handed_over", "reason": "admitted"}}
					}
				case "conditional-pause":
					args[1] = "pause"
					args = append(args, "--only-if-waiting")
					exit = 1
					name := "ErrSpawnNotPausable"
					if variant == "wrong-contract" {
						name = "ErrCantSend"
					}
					value = runner.Row{"err_name": name, "err_details": runner.Row{"cause": "agent_not_covered", "signal_sent": false}}
				case "native-ordinary", "native-id-repeat":
					args = append(args, "--delivery-request-id", proof.RequestID)
					if variant == "wrong-contract" {
						bad := proof
						bad.RecordID = ""
						value = bad
					}
				case "raw-key":
					args = append(args, "--key", "Escape")
					value = runner.Row{}
					if variant == "wrong-contract" {
						value = runner.Row{"submitted": true}
					}
				case "busy-choice-wait_turn":
					args = append(args, "--send-mode", "wait_turn")
					exit = 1
					name := "ErrCantSend"
					if variant == "wrong-contract" {
						name = "ErrSpawnNotPausable"
					}
					value = runner.Row{"err_name": name, "err_details": runner.Row{"reason": "agent_not_covered"}}
				}
				if operation == "native-id-repeat" {
					ref := putCommand(f, &c, "unit-native-original", proof)
					first := c.Commands[ref]
					first.Argv = args
					c.Commands[ref] = first
					agent.Calls = []q.UncoveredCall{{Case: "native-ordinary", Command: first}}
				}
				if variant == "failed-operation" {
					if exit == 0 {
						exit = 1
					} else {
						exit = 0
					}
				}
				ref := putCommand(f, &c, "unit-operation", value)
				cmd := c.Commands[ref]
				cmd.Argv = args
				cmd.ExitCode = exit
				c.Commands[ref] = cmd
				got, handled := q.CheckUncoveredOperation(c, agent, q.UncoveredCall{Case: operation, Expected: "operation-specific public contract", Command: cmd})
				want := "fail"
				if variant == "documented" {
					want = "pass"
				}
				if !handled || got.Status != want {
					t.Fatalf("%+v handled=%t want=%s", got, handled, want)
				}
			})
		}
	}
}

func TestQualificationUnavailableKindFallback(t *testing.T) {
	t.Run("candidate-support-removed", func(t *testing.T) {
		constants := []byte("package store; const AgentClaude = \"claude\"; const AgentCodex = \"codex\"")
		for _, removed := range []bool{false, true} {
			body := "case store.AgentClaude:; case store.AgentCodex:"
			if removed {
				body = "case store.AgentClaude:"
			}
			launch := []byte("package api; func launchAgentError() { switch kind { " + body + " } }")
			kinds, err := q.CandidateKinds(launch, constants)
			want := 2
			if removed {
				want = 1
			}
			if err != nil || len(kinds) != want || kinds[0] != "claude" {
				t.Fatalf("removed=%t: kinds=%v err=%v", removed, kinds, err)
			}
		}
	})
	for _, kind := range []string{"exact-codex", "relabeled-droid", "generic-go-matrix", "wrong-case", "stale-candidate", "exact-request-token", "wrong-token-kind"} {
		t.Run(kind, func(t *testing.T) {
			i := q.Identity{Candidate: "unit-candidate", Product: "unit-product"}
			job := q.SchedulerJob{ID: "unit-job", Candidate: i.Candidate, Product: i.Product, Platform: "linux/amd64", Status: "succeeded", Environment: json.RawMessage(`{"dpkg_architecture":"amd64"}`)}
			log := []byte(`{"case":"t2.75s.bs.un","status":"pass"}` + "\n")
			test := q.SchedulerTest{Coverage: "uncovered/codex", Name: "busy-send-uncovered", Case: "t2.75s.bs.un", Format: "docker-case-json", Reason: "not usable on this host", Kind: "native_unavailable", Source: "tickets/testplans/b.75s/t1.75s.bs/t2.75s.bs.un/t2.75s.bs.un.md"}
			switch kind {
			case "relabeled-droid":
				test.Coverage = "uncovered/droid"
			case "generic-go-matrix":
				test.Name = "TestBusySendUncoveredRefusalMatrix"
				test.Format = "go-test-json"
				test.Case = ""
			case "wrong-case":
				test.Case = "other-case"
			case "stale-candidate":
				job.Candidate = "stale"
			case "exact-request-token", "wrong-token-kind":
				test.Coverage = "uncovered/codex/request-token"
				test.Name = "TestBusySendUncoveredRefusalMatrix"
				test.Case = "codex/send-keys_request_token"
				test.Format = "go-test-json"
				test.Package = "unit/api"
				test.Source = "unit/api_test.go"
				log = []byte(`{"Action":"pass","Package":"unit/api","Test":"TestBusySendUncoveredRefusalMatrix/codex/send-keys_request_token"}` + "\n")
				if kind == "wrong-token-kind" {
					test.Coverage = "uncovered/droid/request-token"
				}
			}
			test.SHA256 = runner.Digest(log)
			err := q.ValidateSchedulerTest(i, job, test, log)
			want := kind == "exact-codex" || kind == "exact-request-token"
			if (err == nil) != want {
				t.Fatalf("%s: %v", kind, err)
			}
		})
	}
}

func TestQualificationNativeRepeatJoin(t *testing.T) {
	for _, kind := range []string{"same-proof", "different-record", "different-digest", "changed-original-argv", "absent-original"} {
		t.Run(kind, func(t *testing.T) {
			f := qualificationfix.New(t, "uncovered")
			c := f.Capture()
			agent := q.UncoveredAgent{Kind: "codex", Instance: "unit-native"}
			c.Owners = []q.OwnerEvidence{{Owner: runner.Owned{InstanceID: agent.Instance, SessionID: "unit-session"}}}
			proof := api.SendKeysResult{RequestID: "uncovered-codex-ordinary", Outcome: "submitted", SessionID: "unit-session", RecordID: "unit-record", RecordSHA256: runner.Digest([]byte("unit-proof"))}
			originalRef := putCommand(f, &c, "native-original", proof)
			first := c.Commands[originalRef]
			first.Argv = []string{"ad", "send-keys", "--instance-id", agent.Instance, "--delivery-request-id", proof.RequestID, "--text", "unit input"}
			args := append([]string{}, first.Argv...)
			switch kind {
			case "different-record":
				proof.RecordID = "another-nonempty-record"
			case "different-digest":
				proof.RecordSHA256 = runner.Digest([]byte("another-valid-proof"))
			case "changed-original-argv":
				first.Argv = append([]string{}, first.Argv...)
				first.Argv[len(first.Argv)-1] = "another input"
			}
			c.Commands[originalRef] = first
			agent.Calls = []q.UncoveredCall{{Case: "native-ordinary", Command: first}}
			if kind == "absent-original" {
				agent.Calls = nil
			}
			ref := putCommand(f, &c, "native-repeat", proof)
			repeat := c.Commands[ref]
			repeat.Argv = args
			c.Commands[ref] = repeat
			// Both receipts/operations remain individually admissible. Only
			// their relationship can reject the altered repeat.
			for _, cmd := range []runner.Command{first, repeat} {
				got, ok := q.CheckUncoveredOperation(c, agent, q.UncoveredCall{Case: "native-ordinary", Command: cmd})
				if !ok || got.Status != "pass" {
					t.Fatalf("standalone proof invalid: %+v", got)
				}
			}
			got, ok := q.CheckUncoveredOperation(c, agent, q.UncoveredCall{Case: "native-id-repeat", Command: repeat})
			want, reason := "fail", "repeat changed original receipt/input instead of reading it"
			if kind == "same-proof" {
				want, reason = "pass", "actual public operation/proof agrees with its own contract"
			}
			if kind == "absent-original" {
				reason = "native repeat has no original owned operation"
			}
			if !ok || got.Status != want || got.Reason != reason {
				t.Fatalf("repeat join: %+v; want %s %q", got, want, reason)
			}
		})
	}
}
