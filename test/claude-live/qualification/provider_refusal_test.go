package qualification_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/test/claude-live/internal/qualificationfix"
	q "github.com/gabemahoney/agent-director/test/claude-live/qualification"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
	"github.com/gabemahoney/agent-director/test/claude-live/signals"
)

type refusalClip struct {
	Attempt string       `json:"attempt"`
	Owner   runner.Owned `json:"owner"`
	Active  runner.Case  `json:"active_case"`
	Waiting string       `json:"waiting_case"`
	Rows    []runner.Row `json:"rows"`
}

func recordedRefusal(t *testing.T, attempt, method string) refusalClip {
	t.Helper()
	b, err := os.ReadFile("testdata/recorded/a2-a3-provider-refusals.json")
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Clips []refusalClip `json:"clips"`
	}
	if err := json.Unmarshal(b, &data); err != nil {
		t.Fatal(err)
	}
	for _, clip := range data.Clips {
		if clip.Attempt == attempt && clip.Active.ID == "typed-long-tool-"+method+"-input" {
			return clip
		}
	}
	t.Fatal("recorded refusal absent")
	return refusalClip{}
}

func refusalJournal(t *testing.T, rows []runner.Row) []byte {
	t.Helper()
	var b bytes.Buffer
	for _, row := range rows {
		if err := json.NewEncoder(&b).Encode(row); err != nil {
			t.Fatal(err)
		}
	}
	return b.Bytes()
}

// A projected recorded transcript inside the existing immutable-evidence
// adapter. Process/snapshot/case text are labelled unit context, never intake.
func refusalFixture(t *testing.T, clips ...refusalClip) (*qualificationfix.Fixture, q.Capture) {
	t.Helper()
	f := qualificationfix.New(t, "typed-controls")
	// Retain the recorded instance IDs under their actual phase RunID. The
	// adapter contributes only synthetic candidate metadata, not old sessions.
	run := strings.TrimPrefix(clips[0].Owner.InstanceID, "live-")
	run = strings.TrimSuffix(strings.TrimSuffix(run, "-typed-ad-start"), "-typed-control-start")
	f.Manifest.RunID, f.Manifest.Owned, f.Manifest.Cases = run, nil, nil
	var installed runner.CandidateEvidence
	f.Decode("raw/candidate-installed.json", &installed)
	installed.RunID = run
	f.Put("raw/candidate-installed.json", installed)
	if err := os.RemoveAll(f.Path("raw/hooks")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.Path("raw/hooks"), 0700); err != nil {
		t.Fatal(err)
	}
	for ref := range f.Manifest.Artifacts {
		if strings.HasPrefix(ref, "raw/hooks/") {
			delete(f.Manifest.Artifacts, ref)
		}
	}
	inventory, err := runner.InspectRecordings(f.Root, run, f.Manifest.RecordingHook.Events)
	if err != nil {
		t.Fatal(err)
	}
	f.Put("raw/hook-inventory.json", inventory)
	var obs []q.Observation
	for _, clip := range clips {
		o := clip.Owner
		o.CWD, o.PID, o.ProcessStart = "/unit/refusal", 1, "unit-projection"
		f.Manifest.Owned = append(f.Manifest.Owned, o)
		if o.StoppedAt.After(f.Manifest.Finished) {
			f.Manifest.Finished = o.StoppedAt.Add(time.Second)
		}
		x := clip.Active
		x.Text, x.Ready = "unit active input (content deliberately not projected)", true
		f.Manifest.Cases = append(f.Manifest.Cases, x)
		prefix := "raw/" + o.InstanceID + "-" + o.LaunchToken
		f.PutBytes(prefix+"-transcript.jsonl", refusalJournal(t, clip.Rows))
		f.PutBytes(prefix+"-trail.jsonl", nil)
		f.Put("raw/final-"+o.InstanceID+"-"+o.LaunchToken+"-store.json", runner.Snapshot{InstanceID: o.InstanceID, Tables: map[string][]runner.Row{"spawns": {{"session_id": o.SessionID, "launch_token": o.LaunchToken}}}})
		kind := "send"
		if x.Mode == "typed" {
			kind = "typed-control"
		}
		obs = append(obs, q.Observation{Case: x.ID, Instance: o.InstanceID, Kind: kind, Started: x.Started, Ended: x.Ended}, q.Observation{Case: clip.Waiting, Instance: o.InstanceID, Kind: "wait", State: "between-tools", Started: x.Ended, Ended: o.StoppedAt})
	}
	f.Put("raw/qualification-observations.json", obs)
	f.Reindex()
	c, err := q.LoadCapture(f.Root)
	if err != nil {
		t.Fatalf("refusal projection must load before semantic judgment: %v", err)
	}
	return f, c
}

func refusalRows(rows []runner.Result) []runner.Result {
	var found []runner.Result
	for _, r := range rows {
		if r.SignalID == "provider_refusal" || strings.HasPrefix(r.SignalID, "provider_refusal/") {
			found = append(found, r)
		}
	}
	return found
}

func assertRefusalRow(t *testing.T, rows []runner.Result, clip refusalClip) {
	t.Helper()
	if len(rows) != 1 {
		t.Fatalf("provider_refusal rows=%d, want one bound refusal for %s/%s", len(rows), clip.Owner.InstanceID, clip.Active.ID)
	}
	r := rows[0]
	b, err := json.Marshal(r.Observed)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "not_completed" {
		t.Fatalf("provider_refusal status=%s, want not_completed", r.Status)
	}
	for _, fact := range []string{clip.Owner.InstanceID, clip.Owner.SessionID, clip.Owner.LaunchToken, clip.Active.ID, clip.Waiting} {
		if !bytes.Contains(b, []byte(fact)) {
			t.Errorf("provider_refusal lost structured fact %q: %s", fact, b)
		}
	}
	ref := "raw/" + clip.Owner.InstanceID + "-" + clip.Owner.LaunchToken + "-transcript.jsonl"
	if !slices.Contains(r.Evidence, ref) {
		t.Errorf("provider_refusal lost raw transcript reference: %v", r.Evidence)
	}
	if !bytes.Contains(b, []byte("refusal")) || !bytes.Contains(b, []byte("2026-11-19T")) {
		t.Errorf("machine refusal/time lost: %s", b)
	}
	request, timestamp := false, false
	for _, row := range clip.Rows {
		if row["type"] == "api-request" {
			id, _ := row["id"].(string)
			request = request || (id != "" && bytes.Contains(b, []byte(id)))
		}
		message, _ := row["message"].(map[string]any)
		if message["stop_reason"] == "refusal" || row["subtype"] == "model_refusal_no_fallback" {
			clock, _ := row["timestamp"].(string)
			at, err := time.Parse(time.RFC3339Nano, clock)
			if err != nil {
				t.Fatal(err)
			}
			timestamp = timestamp || bytes.Contains(b, []byte(at.Format(time.RFC3339Nano)))
		}
	}
	if !request || !timestamp {
		t.Errorf("refusal lost its actual main-request binding or native timestamp: request=%t timestamp=%t observed=%s", request, timestamp, b)
	}
}

func TestQualificationRecordedProviderRefusal(t *testing.T) {
	for _, kind := range []string{"a3-ad", "a3-native-thinking", "a2-ad", "a2-native", "code-only-parent", "api-error-only", "quoted-user", "wrong-session", "sidechain", "child-request", "stale-launch", "stale-case", "unbound-code"} {
		t.Run(kind, func(t *testing.T) {
			attempt, method := "a3", "ad"
			if strings.HasPrefix(kind, "a2") {
				attempt = "a2"
			}
			if strings.Contains(kind, "native") {
				method = "typed"
			}
			clip := recordedRefusal(t, attempt, method)
			want := strings.HasPrefix(kind, "a2") || strings.HasPrefix(kind, "a3") || kind == "code-only-parent"
			if kind == "code-only-parent" || kind == "unbound-code" {
				for _, row := range clip.Rows {
					if m, ok := row["message"].(map[string]any); ok {
						delete(m, "stop_reason")
					}
				}
			}
			if kind == "api-error-only" || kind == "quoted-user" {
				var kept []runner.Row
				substituted := false
				for _, row := range clip.Rows {
					if row["type"] == "api-request" {
						kept = append(kept, row)
						continue
					}
					message, _ := row["message"].(map[string]any)
					if substituted || row["type"] != "assistant" || message["stop_reason"] != "refusal" {
						continue
					}
					delete(message, "stop_reason")
					if kind == "api-error-only" {
						row["error"], row["isApiErrorMessage"] = "invalid_request", true
						message["content"] = []runner.Row{{"type": "text"}}
					} else {
						row["type"], message["role"] = "user", "user"
						message["content"] = "unit quoted tokens: stop_reason refusal model_refusal_no_fallback"
					}
					kept, substituted = append(kept, row), true
				}
				clip.Rows = kept
			}
			switch kind {
			case "wrong-session":
				clip.Owner.SessionID = "unit-other-session"
			case "sidechain":
				for _, row := range clip.Rows {
					row["isSidechain"] = true
				}
			case "child-request":
				for _, row := range clip.Rows {
					if row["type"] == "api-request" {
						row["querySource"] = "unit_child_thread"
					}
				}
			case "stale-launch":
				clip.Owner.Started = clip.Owner.StoppedAt.Add(-time.Millisecond)
			case "stale-case":
				clip.Active.Started, clip.Active.Ended = clip.Owner.StoppedAt.Add(-time.Second), clip.Owner.StoppedAt.Add(-time.Millisecond)
			case "unbound-code":
				for _, row := range clip.Rows {
					if row["type"] == "system" && row["subtype"] == "model_refusal_no_fallback" {
						row["parentUuid"] = "unit-missing-parent"
					}
				}
			}
			if kind == "wrong-session" {
				_, err := signals.ParseSessionTranscript(refusalJournal(t, clip.Rows), clip.Owner.SessionID)
				if err == nil {
					t.Fatal("wrong-session refusal parsed as owned")
				}
				return
			}
			if kind == "unbound-code" || kind == "api-error-only" || kind == "quoted-user" {
				parsed, err := signals.ParseSessionTranscript(refusalJournal(t, clip.Rows), clip.Owner.SessionID)
				if err != nil {
					t.Fatal(err)
				}
				eligible := false
				for _, request := range parsed.Requests {
					if request.ID == "49981935-80bb-4588-8cf3-a27c80f2cb06" && !request.Time.Before(clip.Active.Started) && request.Time.Before(clip.Owner.StoppedAt) {
						eligible = true
					}
				}
				if !eligible {
					t.Fatal("refusal negative lost its recorded eligible main request")
				}
			}
			f, _ := refusalFixture(t, clip)
			rows := refusalRows(q.AnalyzePhase(f.Root).Results)
			if want {
				assertRefusalRow(t, rows, clip)
			} else if len(rows) != 0 {
				t.Fatalf("non-current diagnostic became provider_refusal: %+v", rows)
			}
		})
	}
}

func TestQualificationProviderRefusalFinalScan(t *testing.T) {
	ad, native := recordedRefusal(t, "a3", "ad"), recordedRefusal(t, "a3", "typed")
	f, _ := refusalFixture(t, ad, native)
	for _, id := range []string{"typed-ad-start", "typed-control-start", "typed-long-tool-typed-input"} {
		f.Ledger.Charges = append(f.Ledger.Charges, q.Charge{ID: id, Phase: "typed-controls", Attempt: f.Ledger.Identity.Attempt, Reserved: 1, Actual: 1, Reconciled: true, Launch: "unit-count-only", Prompt: id + "@raw/unit-count-only.json", Evidence: []string{"raw/unit-count-only.json"}})
	}
	f.Put("raw/qualification-ledger-before.json", f.Ledger)
	f.Put("raw/qualification-ledger-after.json", f.Ledger)
	f.Reindex()
	before, _ := json.Marshal(f.Ledger.Charges)
	v := q.AnalyzePhase(f.Root)
	rows := refusalRows(v.Results)
	if len(rows) != 2 {
		t.Errorf("final scan preserved %d owner refusals, want AD and native", len(rows))
	}
	for _, clip := range []refusalClip{ad, native} {
		var own []runner.Result
		for _, row := range rows {
			b, _ := json.Marshal(row.Observed)
			if bytes.Contains(b, []byte(clip.Owner.InstanceID)) {
				own = append(own, row)
			}
		}
		assertRefusalRow(t, own, clip)
	}
	after, err := q.LoadCapture(f.Root)
	if err != nil {
		t.Fatal(err)
	}
	charged, _ := json.Marshal(after.After.Charges)
	if !bytes.Equal(before, charged) {
		t.Fatal("diagnosing refusal changed physical charges")
	}
	// The adapter deliberately lacks independent live typed proof; its existing
	// failure must survive detection rather than being waived by a refusal.
	if v.Status != "fail" {
		t.Fatalf("independent failures downgraded to %s", v.Status)
	}
}

func TestQualificationProviderProgressWait(t *testing.T) {
	for _, kind := range []string{"refusal-no-next-tool", "deadline-without-refusal", "process-wait-after-refusal", "receipt-later", "recorded-ready"} {
		t.Run(kind, func(t *testing.T) {
			clip := recordedRefusal(t, "a3", "ad")
			clip.Active.Text = "unit active input; recorded response content omitted"
			if kind == "deadline-without-refusal" || kind == "receipt-later" || kind == "recorded-ready" {
				clip.Rows = nil
			}
			home := t.TempDir()
			path := filepath.Join(home, ".config/agent-director/state.db")
			tr := apitest.SeedJsonlUnder(t, filepath.Join(home, ".claude"), home, clip.Owner.SessionID)
			if err := os.WriteFile(tr, refusalJournal(t, clip.Rows), 0600); err != nil {
				t.Fatal(err)
			}
			o := clip.Owner
			o.CWD, o.Transcript, o.Socket, o.Exited, o.StoppedAt = home, tr, "unit-socket", false, time.Time{}
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			_, err := apitest.SeedSpawn(path, o.InstanceID, "working", home, "off", o.SessionID, true, apitest.WithLaunchIdentity(store.LaunchIdentity{Token: o.LaunchToken, Socket: o.Socket}), apitest.WithLaunchStartedAt(o.Started.UnixMilli()), apitest.WithJsonlPath(tr), apitest.WithNoPID())
			if err != nil {
				t.Fatal(err)
			}
			evidence, err := runner.NewEvidence(filepath.Join(t.TempDir(), "evidence"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(evidence.Root, "raw/hooks"), 0700); err != nil {
				t.Fatal(err)
			}
			now := clip.Owner.StoppedAt.Add(-time.Millisecond)
			sleeps := 0
			r := &runner.Run{Home: home, Socket: o.Socket, Evidence: evidence, Now: func() time.Time { return now }, Manifest: runner.Manifest{RunID: "unit-refusal-wait", Phase: "typed-controls", Owned: []runner.Owned{o}, Cases: []runner.Case{clip.Active}, TurnBudget: 6}, Sleep: func(context.Context, time.Duration) error { sleeps++; return context.DeadlineExceeded }}
			e := q.NewExercise(r, "")
			e.Observations = []q.Observation{{Case: clip.Active.ID, Instance: o.InstanceID, Kind: "send", Started: clip.Active.Started, Ended: clip.Active.Ended}, {Case: clip.Waiting, Instance: o.InstanceID, Kind: "wait", Started: clip.Active.Ended}}
			if kind == "refusal-no-next-tool" {
				r.Manifest.RunID = strings.TrimSuffix(strings.TrimPrefix(o.InstanceID, "live-"), "-typed-ad-start")
				r.Manifest.RecordingHook = runner.RecordingHook{Installed: true, Directory: "raw/hooks", Events: []string{"SessionStart"}}
				if err := os.MkdirAll(filepath.Join(evidence.Root, "raw/hooks"), 0700); err != nil {
					t.Fatal(err)
				}
				input, err := json.Marshal(runner.Row{"hook_event_name": "SessionStart", "session_id": o.SessionID, "transcript_path": tr, "cwd": home})
				if err != nil {
					t.Fatal(err)
				}
				runner.RecordOwnedHook(evidence.Root, r.Manifest.RunID, "SessionStart", bytes.NewReader(input), o.InstanceID)
			}
			if kind == "receipt-later" || kind == "recorded-ready" {
				// R11-2: isolate one already-bound AD parent so a sibling/Home
				// mismatch cannot stand in for its missing passive receipt.
				r.Manifest.RunID = strings.TrimSuffix(strings.TrimPrefix(o.InstanceID, "live-"), "-typed-ad-start")
				r.Manifest.RecordingHook = runner.RecordingHook{Installed: true, Directory: "raw/hooks", Events: []string{"SessionStart", "PreToolUse"}}
				// a3 invocation-3371063491, input SHA256 8efda66118e8a26c7b533a62eaacc26a6b6e850a48922d730f07f8a1813a0766.
				// Retain its real ID/command. The receipt timing and temporary
				// paths here remain labelled unit provenance permutations.
				const command = "for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30 31 32 33 34 35 36 37 38 39 40 41 42 43 44 45 46 47 48 49 50 51 52 53 54 55 56 57 58 59 60; do sleep 1; done"
				r.Manifest.Cases[0].Text = "Unit frozen foreground request: `" + command + "`"
				if err := os.MkdirAll(filepath.Join(evidence.Root, "raw/hooks"), 0700); err != nil {
					t.Fatal(err)
				}
				publish := func() {
					for _, event := range []string{"SessionStart", "PreToolUse"} {
						payload := runner.Row{"hook_event_name": event, "session_id": o.SessionID, "transcript_path": tr, "cwd": home}
						if event == "PreToolUse" {
							payload["tool_name"], payload["tool_use_id"], payload["is_background"] = "Bash", "toolu_015vDkxYHpv3LHHq82LZc59e", false
							payload["tool_input"] = runner.Row{"command": command, "run_in_background": false}
						}
						input, err := json.Marshal(payload)
						if err != nil {
							t.Fatal(err)
						}
						runner.RecordOwnedHook(evidence.Root, r.Manifest.RunID, event, bytes.NewReader(input), o.InstanceID)
					}
				}
				r.Sleep = func(ctx context.Context, d time.Duration) error {
					sleeps++
					if sleeps != 1 {
						return context.DeadlineExceeded
					}
					now = now.Add(d)
					publish()
					return ctx.Err()
				}
				wantSleeps := 1
				if kind == "recorded-ready" {
					publish()
					wantSleeps = 0
				}
				trigger, err := q.ToolWindowForTest(context.Background(), e, o.InstanceID, 1, false)
				if err != nil || sleeps != wantSleeps || !strings.HasPrefix(trigger, "raw/hooks/invocation-") {
					t.Errorf("bound AD startup must wait for later passive receipt and reach the actual tool window: sleeps=%d trigger=%q error=%v", sleeps, trigger, err)
				}
				return
			}
			if kind == "process-wait-after-refusal" {
				_, err = r.Await(context.Background(), o.InstanceID, time.Second, func(runner.Snapshot) bool { return true })
				if err != nil || sleeps != 0 {
					t.Fatalf("file/process-only wait blocked by refusal: %v (sleeps %d)", err, sleeps)
				}
				return
			}
			_, err = q.ToolWindowForTest(context.Background(), e, o.InstanceID, 2, true)
			if kind == "deadline-without-refusal" {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("real deadline changed: %v", err)
				}
				return
			}
			typed := false
			for cause := err; cause != nil; cause = errors.Unwrap(cause) {
				if strings.Contains(strings.ToLower(reflect.TypeOf(cause).String()), "refusal") {
					typed = true
				}
			}
			if err == nil || errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "provider_refusal") || !typed {
				t.Fatalf("provider-progress wait must return typed provider_refusal before sleeping, got %T: %v (sleeps %d)", err, err, sleeps)
			}
			if sleeps != 0 {
				t.Errorf("known refusal consumed wait/deadline: sleeps=%d", sleeps)
			}
			if !strings.Contains(err.Error(), clip.Active.ID) || !strings.Contains(err.Error(), o.InstanceID) {
				t.Errorf("phase error lost refusal scope: %v", err)
			}
		})
	}
}

func TestQualificationRefusalBlockedSuffix(t *testing.T) {
	for _, kind := range []string{"never-attempted", "attempted-suffix", "reserved-suffix", "conflicting-start", "extra-start", "product-failure", "cleanup-failure"} {
		t.Run(kind, func(t *testing.T) {
			ad, native := recordedRefusal(t, "a3", "ad"), recordedRefusal(t, "a3", "typed")
			_, c := refusalFixture(t, ad, native)
			c.Manifest.Owned = []runner.Owned{ad.Owner, native.Owner}
			c.Manifest.Started, c.Manifest.Finished = ad.Owner.Started, native.Owner.StoppedAt.Add(time.Second)
			c.Manifest.RecordingHook.Installed, c.Manifest.RecordingHook.Removed = true, true
			c.Manifest.Cases, c.Hooks, c.After.Charges = nil, nil, nil
			c.Manifest.ObservedTurns = 3
			for i, id := range []string{"typed-ad-start", "typed-control-start", "typed-long-tool-typed-input"} {
				clip := ad
				if i > 0 {
					clip = native
				}
				x := clip.Active
				x.ID, x.Text = id, "unit planned "+id
				if i < 2 {
					x.Mode = ""
					x.Started = clip.Owner.Started.Add(10 * time.Millisecond)
					x.Ended = x.Started.Add(time.Millisecond)
				}
				ref := "raw/unit-" + id + ".json"
				c.Manifest.Cases = append(c.Manifest.Cases, x)
				c.Hooks = append(c.Hooks, q.HookFact{Reference: ref, Receipt: runner.Receipt{Event: "UserPromptSubmit", Received: x.Ended}, Payload: runner.Row{"session_id": clip.Owner.SessionID, "prompt_id": id, "prompt": x.Text}})
				c.After.Charges = append(c.After.Charges, q.Charge{ID: id, Phase: "typed-controls", Attempt: c.After.Identity.Attempt, Launch: clip.Owner.LaunchToken, Prompt: id + "@" + ref, Reserved: 1, Actual: 1, Reconciled: true, Evidence: []string{ref}})
			}
			ad.Active.Text = "unit AD active steer"
			c.Manifest.Cases = append(c.Manifest.Cases, ad.Active)
			const blocked = "typed-between-tools-typed-input"
			switch kind {
			case "attempted-suffix":
				c.Manifest.Cases = append(c.Manifest.Cases, runner.Case{ID: blocked, InstanceID: native.Owner.InstanceID, Text: "unit attempted input", Mode: "typed", Started: native.Owner.StoppedAt.Add(-time.Second)})
			case "reserved-suffix":
				c.After.Charges = append(c.After.Charges, q.Charge{ID: blocked, Phase: "typed-controls", Attempt: c.After.Identity.Attempt, Reserved: 1})
			case "conflicting-start":
				c.After.Charges = append(c.After.Charges, c.After.Charges[0])
			case "extra-start":
				x := c.After.Charges[0]
				x.ID = "unexpected-unit-start"
				x.Prompt = "unit-unmatched@raw/extra.json"
				c.After.Charges = append(c.After.Charges, x)
				c.Manifest.ObservedTurns++
			case "product-failure", "cleanup-failure":
				c.Manifest.Problems = append(c.Manifest.Problems, runner.Result{SignalID: kind, Status: "fail", Reason: "unit independent failure"})
			}
			starts := q.ScheduledStartsForTest(c)
			v := q.PhaseResult(c.Manifest, []runner.Result{starts, q.AccountingForTest(c), q.CleanupForTest(c)})
			if kind != "never-attempted" {
				if (kind == "attempted-suffix" || kind == "reserved-suffix" || kind == "conflicting-start") && starts.Status != "fail" {
					t.Errorf("attempted or conflicting physical evidence was downgraded: %s", starts.Status)
				}
				if v.Status != "fail" {
					t.Fatalf("%s waived independent evidence: %s", kind, v.Status)
				}
				return
			}
			parts, ok := starts.Observed.([]runner.Result)
			if !ok {
				t.Fatalf("scheduled-starts components are %T", starts.Observed)
			}
			for _, id := range []string{"typed-ad-start", "typed-control-start", "typed-long-tool-typed-input"} {
				if r := namedResult(t, parts, "physical/"+id); r.Status != "pass" {
					t.Fatalf("issued prefix is not a valid refusal-abort control: %+v", r)
				}
			}
			if starts.Status != "not_completed" || v.Status != "not_completed" {
				t.Fatalf("bound refusal's untouched suffix: scheduled=%s phase=%s; want not_completed", starts.Status, v.Status)
			}
			for _, id := range []string{blocked, "typed-idle-ad-input", "typed-idle-typed-input"} {
				r := namedResult(t, parts, "physical/"+id)
				if r.Status != "not_completed" || !strings.Contains(r.Reason, "provider_refusal") || len(r.Evidence) == 0 {
					t.Errorf("blocked suffix lacks exact cause reference: %+v", r)
				}
			}
			summary := q.AggregatePhaseResults("shakedown", []string{"typed-controls", "text-limits-backlog"}, map[string][]runner.Result{"typed-controls": v.Results}, nil, nil)
			if summary.Status == "pass" || summary.SelectedStatus == "pass" {
				t.Fatal("continued partial recording qualified the selection")
			}
			if r := namedResult(t, summary.Results, "native-wake-ladder"); r.Status != "not_completed" {
				t.Errorf("skipped dependent text phase status=%s", r.Status)
			}
		})
	}
}
