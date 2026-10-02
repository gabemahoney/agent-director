package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// smp builds a sample; ms < 0 leaves it without a time.
func smp(o outcome, ms int64) sample {
	s := sample{Case: "c", Outcome: o}
	if ms >= 0 {
		s.Millis = &ms
	}
	return s
}

// caseOf builds a finalized case with default budgets.
func caseOf(id string, fam family, samples ...sample) caseResult {
	c := caseResult{ID: id, Family: fam, Title: id + " title", Samples: samples,
		Budgets: budgetReport{SessionEndHooks: []hookBudget{{Layer: layerAgentDirector, Program: "agent-director", Timeout: "default"}},
			EnvSources: []envBudget{}, EnvEffective: "unset", ClaudeCode: "2.1.285"}}
	c.finalize()
	return c
}

// resultsOf builds results in mode m holding cases.
func resultsOf(m mode, cases ...caseResult) *results {
	r := newResults(isolation{RunID: "mx-run-1", Mode: m, AgentDirector: "1.2.3", ClaudeCode: "2.1.285", Parallelism: 1,
		TmuxTmpdir: "/tmp/mx-tmux-1", ExpectedSocket: "/tmp/mx-tmux-1/tmux-1000/default"}, clockStart)
	r.Cases = append(r.Cases, cases...)
	return r
}

// render returns the table and results.json of r.
func render(t *testing.T, r *results, scr scrubber) (table, js string) {
	t.Helper()
	var b bytes.Buffer
	if err := r.writeTable(&b, scr); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), resultsFile)
	if err := r.writeJSON(path, scr); err != nil {
		t.Fatal(err)
	}
	return b.String(), readFile(t, path)
}

func TestFinalize(t *testing.T) {
	for _, tc := range []struct {
		name                                 string
		samples                              []sample
		completed, didNotExit, noEnded, fail int
		largest                              int64 // -1: no data
	}{
		{"largest among completed", []sample{smp(outcomeCompleted, 100), smp(outcomeCompleted, 300), smp(outcomeCompleted, 200)}, 3, 0, 0, 0, 300},
		{"did not exit never the largest", []sample{smp(outcomeCompleted, 100), smp(outcomeDidNotExit, 9999)}, 1, 1, 0, 0, 100},
		{"no ended_at counted apart", []sample{smp(outcomeNoEndedAt, -1), smp(outcomeCompleted, 50)}, 1, 0, 1, 0, 50},
		{"other outcomes are failed", []sample{smp(outcomeFailed, -1), smp(outcomeNotReady, -1), smp(outcomeProbeError, -1)}, 0, 0, 0, 3, -1},
		{"no samples", nil, 0, 0, 0, 0, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := caseOf("rn6.idle", familyRN6, tc.samples...)
			if c.Attempted != len(tc.samples) || c.Completed != tc.completed || c.DidNotExit != tc.didNotExit || c.NoEndedAt != tc.noEnded || c.Failed != tc.fail {
				t.Fatalf("counts %+v", c)
			}
			if (tc.largest < 0) != (c.LargestMillis == nil) || (c.LargestMillis != nil && *c.LargestMillis != tc.largest) {
				t.Errorf("largest %v, want %d", c.LargestMillis, tc.largest)
			}
			table, _ := render(t, resultsOf(modeReal, c), scrubber{})
			if tc.largest < 0 && !strings.Contains(table, "no data") {
				t.Errorf("a case with no completed sample must read no data:\n%s", table)
			}
		})
	}
}

func TestTableRows(t *testing.T) {
	rn6 := caseOf("rn6.idle", familyRN6, smp(outcomeCompleted, 1500))
	notRun := caseResult{ID: "rn6.mcp", Family: familyRN6, NotRun: "no MCP configuration supplied"}
	table, _ := render(t, resultsOf(modeReal, rn6, notRun), scrubber{})
	for _, want := range []string{"rn6.idle", "1.5 s", "agent-director --settings: default; env: unset; default = Claude Code 2.1.285's",
		"rn6.mcp", "not run: no MCP configuration supplied", sessionEndBudgetEnv + ": unset"} {
		if !strings.Contains(table, want) {
			t.Errorf("table lacks %q:\n%s", want, table)
		}
	}
	if strings.Contains(table, rn2ResolutionNote) {
		t.Error("the RN-2 note printed with no RN-2 row")
	}
	table, _ = render(t, resultsOf(modeReal, caseOf("rn2.pause", familyRN2, smp(outcomeCompleted, 900))), scrubber{})
	if !strings.Contains(table, rn2ResolutionNote) {
		t.Errorf("an RN-2 row lacks the resolution note:\n%s", table)
	}
}

func TestDryRunBanner(t *testing.T) {
	for _, m := range []mode{modeDry, modeReal, modeProbe} {
		table, js := render(t, resultsOf(m, caseOf("rn6.idle", familyRN6, smp(outcomeCompleted, 10))), scrubber{})
		var doc struct {
			DryRun bool   `json:"dry_run"`
			Banner string `json:"banner"`
		}
		if err := json.Unmarshal([]byte(js), &doc); err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(table), "\n")
		dry := m == modeDry
		if doc.DryRun != dry || (doc.Banner == dryRunBanner) != dry || strings.Contains(table, "DRY RUN") != dry {
			t.Errorf("%s: dry_run %t banner %q, table:\n%s", m, doc.DryRun, doc.Banner, table)
		}
		if dry && (!strings.Contains(lines[0], dryRunBanner) || !strings.Contains(lines[len(lines)-1], dryRunBanner)) {
			t.Errorf("the dry-run table must start and end with the banner:\n%s", table)
		}
	}
}

func TestResultsRoundTrip(t *testing.T) {
	r := resultsOf(modeReal, caseOf("rn2.natural", familyRN2, smp(outcomeCompleted, 700), smp(outcomeNoEndedAt, -1)))
	finished := clockStart.Add(90)
	r.FinishedAt = &finished
	r.Seed = &seedRecord{Path: "/home/t/.claude.json", Keys: []string{"theme"}, CredentialMode: credGateway}
	r.RN9 = &rn9Section{Scenarios: []rn9Scenario{{ID: rn9ResumeID, Verdict: verdictFail, Reason: "not applied", Stop: []string{stopResumeNotApplied},
		Hooks: []hookObservation{{Seq: 1, Event: "SessionStart", Source: "resume", ParentPID: 10, PanePID: 10, PIDMatch: true, Outcome: hookIgnored, Reason: "pid_mismatch"}}}}}
	r.RN7 = &rn7Record{Rows: []rn7Row{{Seq: 1, Event: "Notification", Keys: []string{"hook_event_name"}, Note: "records no session id"}}}
	r.Probe = &probeSection{Versions: []probeVersion{{Version: "2.1.285", Result: probeArgsReceived}}, Minimum: "2.1.285"}
	r.Notes = []string{"run note"}
	table, js := render(t, r, scrubber{})
	var back results
	if err := json.Unmarshal([]byte(js), &back); err != nil {
		t.Fatal(err)
	}
	again, _ := json.Marshal(&back)
	orig, _ := json.Marshal(r)
	if string(again) != string(orig) {
		t.Errorf("round trip lost data:\n got %s\nwant %s", again, orig)
	}
	if len(back.Cases[0].Samples) != 2 || back.Isolation.ExpectedSocket == "" || back.RN9.Scenarios[0].Hooks[0].Reason != "pid_mismatch" || back.Probe.Minimum != "2.1.285" {
		t.Errorf("round trip %+v", back)
	}
	for _, want := range []string{"rn9.resume", "resume-not-applied", "pid_mismatch", "RN-7 record (for the record; no verdict)",
		"absent", "records no session id", "oldest version that ran exec-form args: 2.1.285", "note: run note"} {
		if !strings.Contains(table, want) {
			t.Errorf("table lacks %q:\n%s", want, table)
		}
	}
}

func TestOutputsCarryNoCredential(t *testing.T) {
	vars := map[string]string{}
	for k, v := range credentialSentinels {
		vars[k] = v
	}
	scr := newScrubber(fakeEnv(vars, "/h"))
	mcp := filepath.Join(t.TempDir(), "mcp.json")
	writeFile(t, mcp, `{"mcpServers": {"tools": {"command": "/nonexistent/tools-server", "args": ["--key", "mcp-arg-secret"], "env": {"TOOLS_KEY": "mcp-env-secret"}}}}`)
	c := caseOf("rn6.mcp", familyRN6, sample{Case: "rn6.mcp", Outcome: outcomeFailed, Reason: "spawn: " + credentialSentinels["ANTHROPIC_AUTH_TOKEN"]})
	var err error
	if c.MCPServers, err = mcpServerNames(mcp); err != nil {
		t.Fatal(err)
	}
	notes, err := mcpMissingCommands(mcp, "/bin")
	if err != nil {
		t.Fatal(err)
	}
	c.Notes = append(notes, "base url "+credentialSentinels["ANTHROPIC_BASE_URL"])
	r := resultsOf(modeReal, c)
	r.Notes = sentinelValues()
	table, js := render(t, r, scr)
	for what, text := range map[string]string{"table": table, "results.json": js} {
		assertAbsent(t, what, text, append(sentinelValues(), "mcp-arg-secret", "mcp-env-secret")...)
		if !strings.Contains(text, "tools") || !strings.Contains(text, scrubbedValue) {
			t.Errorf("%s lacks the server name or the redaction:\n%s", what, text)
		}
	}
}
