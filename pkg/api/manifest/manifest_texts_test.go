package manifest_test

// manifest_texts_test.go pins what the manifest's agent-facing texts state,
// on the manifest source of truth (TestSurfaceJSONMirrorsManifest carries
// every check to surface.json), and the Go doc statements beside them.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// textCase is one manifest text, a verb's description or one param's or
// result field's (own: find-missing's description before the live-row
// pointer), and the case it must pass under the agent-text rules.
type textCase struct {
	verb, param, field string
	own                bool
	c                  apitest.DescCase
}

// tokens is a DescCase requiring each of phrases.
func tokens(name string, phrases ...string) apitest.DescCase {
	return apitest.DescCase{Name: name, Require: phrases}
}

// preTrustField is SR-22.6's pre_trust text, skippedWhy saying why pre-trust
// was off for the launch.
func preTrustField(skippedWhy string) apitest.DescCase {
	c := tokens("pre_trust values",
		"ok = the folder trust took effect (claude: the entry was written to .claude.json; codex: the launch carries the folder trust and the hook-trust bypass)",
		"skipped = pre-trust was off for this launch",
		skippedWhy, "nothing was attempted", "failed = pre-trust was attempted", "entry was not written", "launch still proceeds")
	c.Forbid = []string{"ok = the folder-trust entry was written"}
	return c
}

// manifestTextCases lists the texts and their cases: find-missing (SR-18.9,
// SR-18.7, SR-3.8, SR-11.7, SR-18.3, SR-18.11); kill (SR-18.9, SR-6.1,
// SR-1.7); spawn's launch rules (SR-18.1, SR-9.3, SR-9.4), ErrInternal
// triggers (AC-CAT-03), param rules (SR-9.2, SR-18.9, SR-18.10, SR-22.6);
// pre-trust (SR-22.6); launch_started_at (SR-22.2); list's liveness fields
// (SR-8.3); get's tmux socket (SR-3.3, SR-16.1); the pane verbs' tmux error
// classes (SR-18.1); missing is not proof of death (SR-18.2, AC-DOC-02: the
// full sentence, or the short form, decision-0930e); make-template naming
// spawn's per-call params by their manifest names, flag beside (b.c4u);
// spawn's permission arrays and agent_args texts read right on every surface,
// with no CLI-only spelling (b.ia3); and each integer count param stating that
// a negative value is refused (b.c4n).
func manifestTextCases() []textCase {
	cases := []textCase{
		{verb: "find-missing", own: true, c: apitest.DescFindMissingGrace()},
		{verb: "find-missing", own: true, c: apitest.DescFindMissingManifest()},
		{verb: "find-missing", field: "ids", c: apitest.DescFindMissingField(apitest.FindMissingIDs)},
		{verb: "find-missing", field: "unverified_ids", c: apitest.DescFindMissingField(apitest.FindMissingUnverifiedIDs)},
		{verb: "find-missing", own: true, c: tokens("Codex end report, dead process",
			"A dead agent process marks the row `missing` (a Codex row with an end report: `ended`, not in ids) whatever tmux shows, with no tmux call.")},
		{verb: "find-missing", field: "count", c: tokens("Codex row moved to ended not counted",
			"a Codex row it moved to `ended` is not counted")},
		{verb: "find-missing", field: "ids", c: tokens("Codex row moved to ended not listed",
			"so is a Codex row the sweep moved to `ended` (its agent process gone and an end report held)")},
		{verb: "pause", c: tokens("Codex end report ends the row",
			"Codex: once the agent is gone with an end report, ends the row (before the keys or in the wait).")},
		{verb: "kill", c: apitest.DescKillManifest()},
		{verb: "spawn", c: apitest.DescSpawnLaunchTimeoutRule()},
		{verb: "spawn", c: apitest.DescSpawnScanRefusal()},
		{verb: "spawn", c: apitest.DescSpawnHeldName()},
		{verb: "spawn", c: tokens("tmux error classes", "ErrTmuxSessionCreate (LAUNCH FAILURE)", "ErrTmuxNotAvailable (ENVIRONMENT)")},
		{verb: "spawn", c: tokens("pre-check ErrInternal", "ErrInternal", "collision pre-check", "read the store")},
		{verb: "spawn", c: tokens("reuse ErrInternal triggers", "ErrInternal, nothing created or changed",
			"a reuse's archive of the previous session", "or its change failed", "a busy store included")},
		{verb: "spawn", param: "tmux_session_name", c: apitest.DescSpawnSessionNameParam()},
		{verb: "spawn", param: "tmux_session_name", c: tokens("rejects dollar and backslash", `'$'`, `'\'`)},
		{verb: "spawn", param: "instance_id", c: tokens("rejects control characters", "control character", "ErrInvalidFlags")},
		{verb: "spawn", param: "instance_id", c: apitest.DescInstanceIDCollision(apitest.CollisionSite{})},
		{verb: "spawn", param: "no_pre_trust", c: tokens("recorded on the row until the next spawn or reuse", "every resume follows it")},
		{verb: "spawn", param: "no_pre_trust", c: tokens("both agents' pre-trust", "For a claude agent, pre-trust pre-writes",
			"For a codex agent, it puts the cwd's trust and the hook-trust bypass on the launch's command line and writes no file")},
		{verb: "spawn", param: "reuse_finished", c: apitest.DescReuseFinishedParam()},
		{verb: "resume", c: tokens("pre-trust", "best-effort pre-trust", "unless the row's latest spawn",
			"no_pre_trust", "pre-trust failure never fails the resume")},
		{verb: "spawn", field: "pre_trust", c: preTrustField("the caller passed no_pre_trust")},
		{verb: "resume", field: "pre_trust", c: preTrustField("the row's latest spawn turned it off with no_pre_trust")},
		{verb: "spawn", field: "pre_trust", c: tokens("codex failed", "for a claude agent, the .claude.json file is missing",
			"for a codex agent, extra_env sets CODEX_HOME to a path that is not absolute",
			"the launch carries neither the folder trust nor the hook-trust bypass", "(Claude Code's or Codex's)")},
		{verb: "resume", field: "pre_trust", c: tokens("codex failed", "for a claude agent, the .claude.json file is missing",
			"for a codex agent, the row's extra_env sets CODEX_HOME to a path that is not absolute",
			"the launch carries neither the folder trust nor the hook-trust bypass", "(Claude Code's or Codex's)")},
		{verb: "get", field: "jsonl_path", c: tokens("codex path never built",
			"for claude recomputes the current session's path from cwd + session_id", "A codex row uses only its recorded path",
			"resume never builds one for it")},
		{verb: "status", field: "launch_started_at", c: apitest.DescLaunchStartedAtField(false)},
		{verb: "get", field: "launch_started_at", c: apitest.DescLaunchStartedAtField(false)},
		{verb: "list", field: "spawns", c: apitest.DescLaunchStartedAtField(true)},
		{verb: "list", field: "spawns", c: tokens("liveness fields", "liveness_unverified_since", "liveness_note")},
		{verb: "get", field: "tmux_socket", c: apitest.DescTmuxSocketField()},
		{verb: "get-permission", c: apitest.DescCase{Name: "undecided versus open",
			Require: []string{"null while undecided", "a decided request can remain open until acknowledged"},
			MustNot: []string{"null while open"}}},
		{verb: "get-permission", field: "decision_reason", c: apitest.DescCase{Name: "pane verdict reasons",
			Require: []string{"for a request closed at the pane, allow or deny", "pane_outside", "tool_ran", "null while undecided and for allow rows decided by decide"},
			MustNot: []string{"null for open and allow rows"}}},
		{verb: "get", field: "unproven_requests", c: apitest.DescCase{Name: "finished-row projection is not exit proof",
			Require: []string{"in any state but ended and missing ([] on those)", "with [state] settle_ms set", "for up to settle_ms"},
			MustNot: []string{"the agent is gone"}}},
		{verb: "get", c: tokens("tmux socket", "tmux socket")},
		{verb: "find-missing", c: apitest.DescMissingNotProof()},
		{verb: "find-missing", field: "ids", c: apitest.DescMissingNotProof()},
		{verb: "make-template", param: "relay_mode", c: tokens("per-call spelling", "Per-call relay_mode (--relay-mode on the CLI) overrides.")},
		{verb: "make-template", param: "extra_env", c: tokens("per-call spelling",
			"Per-call extra_env (--extra-env on the CLI) merges by key; per-call wins on collision.")},
		{verb: "spawn", param: "agent", c: tokens("agent rules", "claude (Claude Code)", "codex (Codex)", "cursor (Cursor fresh Linux ACP)", "case-sensitive",
			"ErrAgentMissing", "ErrAgentInvalid", "ErrAgentArgsMismatch", "ErrPermissionsUnsupported")},
		{verb: "make-template", param: "agent", c: tokens("agent rules", "claude (Claude Code)", "codex (Codex)", "cursor (Cursor fresh Linux ACP)", "case-sensitive",
			"ErrTemplateMalformed", "ErrTemplateAgentMissing", "ErrPermissionsUnsupported")},
		{verb: "spawn", param: "extra_env", c: tokens("config-dir variable per agent", "CLAUDE_CONFIG_DIR (Claude Code) or CODEX_HOME (Codex)")},
		{verb: "make-template", param: "extra_env", c: tokens("config-dir variable per agent", "CLAUDE_CONFIG_DIR (Claude Code) or CODEX_HOME (Codex)")},
		{verb: "spawn", c: tokens("launch-timeout retry names the row's agent", "then retry an explicit id with the reuse opt-in and the row's agent, which get shows")},
		{verb: "spawn", c: tokens("held-name retry names the row's agent",
			"spawn the id again with reuse_finished (--reuse-finished on the CLI) and the row's agent")},
		{verb: "kill", c: tokens("live-row step 6 names the row's agent, which get shows",
			"otherwise spawn with reuse_finished (--reuse-finished on the CLI) and the row's agent, which get shows; callers whose ids agent-director mints spawn fresh.")},
		{verb: "make-template", param: "agent_args", c: apitest.DescCase{Name: "per-call spelling",
			Require: []string{"Per-call agent_args (on the CLI, spawn's arguments after --) REPLACES"}, Forbid: []string{"--agent-args", "--claude-args"}}},
		{verb: "spawn", param: "agent_args", c: apitest.DescCase{Name: "pass-through for both agents",
			Require: []string{"Pass-through argv to the agent program, one argument per array element", "Claude refuses --settings, --output-format and print (-p/--print, clusters included)", "Session selectors/owned values are refused except on a finished-agent switch", "Exact denied long tokens owned as values always refuse"},
			Forbid:  []string{"after the supervisor's own flags"}}},
		{verb: "spawn", param: "agent_args", c: tokens("codex refusals",
			"For a codex agent, refused (ErrSpawnDeniedFlag): a Codex subcommand in command position", "--remote, --remote-auth-token-env",
			"-c/--config keys at, above or under hooks, tui.disable_paste_burst or features.hooks", "and --disable hooks",
			"with disable_ask_user on, also --enable default_mode_request_user_input and the question-tool keys")},
		{verb: "spawn", param: "agent_args", c: apitest.DescCase{Name: "no second flag added, repeats not dropped",
			Require: []string{"agent-director adds no second --no-daemon, --dangerously-bypass-hook-trust (pre-trust ok) or --enable write_stdin_approval when the arguments already carry one before any --; Codex rejects a flag the arguments themselves repeat."},
			Forbid:  []string{"is dropped", "A repeated --no-daemon"}}},
		{verb: "get", field: "agent_args", c: apitest.DescCase{Name: "verbatim argv, no placement claim",
			Require: []string{"Verbatim argv passed through to the agent program."}, Forbid: []string{"--settings", "after the supervisor's own flags"}}},
	}
	for _, p := range []string{"allow", "deny", "ask"} {
		cases = append(cases, textCase{verb: "spawn", param: p, c: apitest.DescCase{Name: "array, repeatable flag on the CLI",
			Require: []string{"permissions." + p + " entries, one per array element (on the CLI, a repeatable flag)"}, Forbid: []string{"Repeated"}}})
	}
	for _, verb := range []string{"spawn", "resume"} {
		cases = append(cases, textCase{verb: verb, c: tokens("names pre_trust", "Returns the instance_id and pre_trust", "ok, skipped or failed")})
	}
	for _, s := range []struct{ verb, field string }{{"status", "state"}, {"get", "state"}, {"list", "spawns"}} {
		cases = append(cases, textCase{verb: s.verb, field: s.field, c: apitest.DescMissingNotProof()})
	}
	for _, verb := range []string{"kill", "resume", "pause", "expire"} {
		cases = append(cases, textCase{verb: verb, c: apitest.DescMissingNotProofShort()})
	}
	for _, s := range []struct{ verb, param string }{
		{"read-pane", "n_lines"}, {"list", "limit"}, {"trail-emit", "bytes_sent"}, {"trail-emit", "bytes_received"},
	} {
		cases = append(cases, textCase{verb: s.verb, param: s.param, c: tokens("negative refused", "A negative value is refused (ErrInvalidFlags).")})
	}
	for _, v := range []apitest.PaneVerb{apitest.PaneReadPane, apitest.PaneSendKeys, apitest.PanePause} {
		def, _ := manifest.Lookup(string(v)) // a missing verb fails at siteText
		cases = append(cases, textCase{verb: string(v), c: apitest.DescPaneManifest(v, def.ErrorNames)})
	}
	cases = append(cases, listSubagentsTextCases()...)
	return cases
}

// subagentRecordKeys are the keys of each element of list-subagents'
// subagents array, with the type and nullability its text states for each
// (SR-11.4): a "?" marks a key that is null when its value is unknown.
var subagentRecordKeys = []string{
	"subagent_id (string)", "instance_id (string)", "session_id (string?)", "subagent_type (string?)",
	"description (string?)", "model (string?)", "parent_type (string?)", "parent_id (string?)",
	"tool_use_id (string?)", "run_mode (string?)", "first_started_at (timestamp?)", "started_at (timestamp?)",
	"stopped_at (timestamp?)", "lost_at (timestamp?)", "state (string)", "close_reason (string?)",
}

// subagentsTimestampFormat is the sentence naming the format of every
// timestamp in the subagents text, which the text states once.
const subagentsTimestampFormat = "Each timestamp is RFC3339 UTC with millisecond precision (trailing zeros dropped, no fraction on a whole second)."

// listSubagentsTextCases are list-subagents' own texts: tracked's two values,
// and the subagents text's every key with its type and nullability, the value
// sets of state, close_reason, parent_type and run_mode with their meanings,
// the order, the empty array, the description never being prompt text, and the
// timestamp format (SR-11.1, SR-11.4, SR-11.5). All three texts are agent
// neutral: no agent name and no agent-specific hook field or event name.
func listSubagentsTextCases() []textCase {
	neutral := apitest.DescCase{Name: "agent neutral", MustNot: []string{
		"Claude", "Claude Code", "Codex", "SubagentStart", "SubagentStop", "agent_id", "agent_type", "transcript_path", "hook_event_name",
	}}
	cases := []textCase{
		{verb: "list-subagents", c: neutral},
		{verb: "list-subagents", param: "instance_id", c: neutral},
		{verb: "list-subagents", field: "tracked", c: neutral},
		{verb: "list-subagents", field: "subagents", c: neutral},
		{verb: "list-subagents", field: "tracked", c: tokens("tracked values",
			"false for a row launched before subagent tracking existed, which has no records",
			"false for a row whose launch registers no subagent events", "true for any other row")},
		{verb: "list-subagents", field: "subagents", c: tokens("every record key with type and nullability", subagentRecordKeys...)},
		{verb: "list-subagents", field: "subagents", c: apitest.DescCase{Name: "current-life scope and identity",
			Require: []string{"Every subagent record of the row's current life, across every launch of it", "unique among the records shown for its row"},
			MustNot: []string{"every launch and life", "unique within its row"}}},
		{verb: "list-subagents", field: "subagents", c: tokens("never null, even with no records",
			"Empty array when it has none (never null)", "a field with no known value null")},
		{verb: "list-subagents", field: "subagents", c: tokens("order",
			"Ordered by first_started_at ascending (records with a null first_started_at last), then subagent_id")},
		{verb: "list-subagents", field: "subagents", c: tokens("state values",
			"running (started, neither stopped nor judged lost)", "stopped (its agent reported it finished)",
			"lost (judged gone without a stop report)")},
		{verb: "list-subagents", field: "subagents", c: tokens("close_reason values",
			"null exactly while state is running", "stop_event (its agent reported it finished)",
			"absent_from_snapshot (a snapshot of the agent's running background tasks did not list it)",
			"agent_gone (its agent ended, went missing or began a new launch)")},
		{verb: "list-subagents", field: "subagents", c: tokens("parent_type values",
			"agent (the row's own agent, so parent_id is the row's instance_id)",
			"subagent (another subagent, so parent_id is that subagent's subagent_id)")},
		{verb: "list-subagents", field: "subagents", c: tokens("run_mode values", "background or foreground")},
		{verb: "list-subagents", field: "subagents", c: tokens("description is never prompt text", "never the prompt text")},
		{verb: "list-subagents", field: "subagents", c: tokens("timestamp format", subagentsTimestampFormat)},
	}
	return cases
}

// TestListSubagentsVerbDescription pins list-subagents' Description to one
// sentence of at most 67 bytes (its help entry's whole size, SR-11.1), naming
// both what it returns and the tracked flag.
func TestListSubagentsVerbDescription(t *testing.T) {
	desc := siteText(t, "list-subagents", "", "")
	if len(desc) > 67 {
		t.Errorf("list-subagents Description is %d bytes, want at most 67: %q", len(desc), desc)
	}
	if !strings.HasSuffix(desc, ".") || strings.Contains(strings.TrimSuffix(desc, "."), ".") {
		t.Errorf("list-subagents Description is not exactly one sentence: %q", desc)
	}
	apitest.AssertAgentTextCase(t, "list-subagents description", desc, tokens("what it returns", "subagent records", "tracked"))
}

// TestListSubagentsTimestampFormatOnce pins that the subagents text states the
// timestamp format once, for every timestamp field, not once per field.
func TestListSubagentsTimestampFormatOnce(t *testing.T) {
	text := siteText(t, "list-subagents", "", "subagents")
	if n := strings.Count(text, "RFC3339"); n != 1 {
		t.Errorf("subagents text names RFC3339 %d times, want once: %q", n, text)
	}
	if n := strings.Count(text, "millisecond precision"); n != 1 {
		t.Errorf("subagents text states millisecond precision %d times, want once: %q", n, text)
	}
}

// TestManifestTextCases runs every manifestTextCases case.
func TestManifestTextCases(t *testing.T) {
	for _, tc := range manifestTextCases() {
		site := strings.TrimSpace(tc.verb + " " + tc.param + tc.field)
		t.Run(site+": "+tc.c.Name, func(t *testing.T) {
			text := siteText(t, tc.verb, tc.param, tc.field)
			if tc.own {
				text = apitest.FindMissingOwnText(text)
			}
			apitest.AssertAgentTextCase(t, site, text, tc.c)
		})
	}
}

// claudeOnlyPhrases is Appendix A's list of Claude-only wording, and the old
// Claude-named spellings, that no manifest text may carry (SR-4.4, SR-4.2):
// each verb, param and result field says "the agent", or names its agent where
// only one does something. The old names are refused, never described.
var claudeOnlyPhrases = []string{
	"Launch a tracked Claude Code agent",
	"(Claude Code's SessionStart)",
	"Claude Code tool that triggered the permission request",
	"Pass-through argv to `claude`",
	"via `claude --resume`",
	"Claude Code session UUID, extracted from SessionStart hook's transcript_path",
	"Bake default Claude argv",
	"Claude Code hook payload",
	"claude_instance_id. Required.",
	"claude_instance_id", "claude_args", "claude_session_id",
	"--claude-instance-id", "--claude-args",
}

// TestManifestTextsHaveNoClaudeOnlyPhrases runs claudeOnlyPhrases as MustNot
// over every verb, param and result-field text, the hook's and trail-emit's
// included (SR-4.4).
func TestManifestTextsHaveNoClaudeOnlyPhrases(t *testing.T) {
	c := apitest.DescCase{Name: "no Claude-only wording", MustNot: claudeOnlyPhrases}
	for _, name := range []string{"hook", "trail-emit"} {
		verbOf(t, name) // a missing verb would let the loop below pass vacuously for it
	}
	for _, v := range manifest.Verbs {
		apitest.AssertAgentTextCase(t, v.Name+" description", v.Description, c)
		for _, p := range v.Params {
			apitest.AssertAgentTextCase(t, v.Name+" param "+p.Name, p.Description, c)
		}
		for _, f := range v.ResultFields {
			apitest.AssertAgentTextCase(t, v.Name+" result field "+f.Name, f.Description, c)
		}
	}
}

// recordingTB records that a check failed, instead of failing the test.
type recordingTB struct {
	testing.TB
	failed bool
}

func (r *recordingTB) Helper()               {}
func (r *recordingTB) Errorf(string, ...any) { r.failed = true }

// TestClaudeOnlyPhrasesMatch keeps the check above from passing vacuously: the
// phrase list flags a text that carries one of its phrases.
func TestClaudeOnlyPhrasesMatch(t *testing.T) {
	probe := &recordingTB{}
	apitest.AssertAgentTextCase(probe, "probe", "Launch a tracked Claude Code agent in a new tmux session.",
		apitest.DescCase{Name: "probe", MustNot: claudeOnlyPhrases})
	if !probe.failed {
		t.Error("claudeOnlyPhrases did not flag \"Launch a tracked Claude Code agent\"; the MustNot check would pass vacuously")
	}
}

// TestManifestTextsNameNoSessionEndingCommand runs the helper's forbidden-only
// check over every verb, param and result-field description (SR-1.4, SR-6.8).
func TestManifestTextsNameNoSessionEndingCommand(t *testing.T) {
	for _, v := range manifest.Verbs {
		apitest.AssertAgentText(t, v.Name+" description", v.Description)
		for _, p := range v.Params {
			apitest.AssertAgentText(t, v.Name+" param "+p.Name, p.Description)
		}
		for _, f := range v.ResultFields {
			apitest.AssertAgentText(t, v.Name+" result field "+f.Name, f.Description)
		}
	}
}

// liveRowWants lists the verbs whose description states SR-18.6's live-row
// sequence or points to it (decision-0930b Q6), and the case it passes;
// every other verb carries neither.
var liveRowWants = map[string]struct {
	sequences, pointers int
	desc                func() apitest.DescCase
}{
	"kill":         {sequences: 1, desc: apitest.DescLiveRowSequence},
	"find-missing": {pointers: 1, desc: apitest.DescLiveRowPointer},
	"spawn":        {pointers: 1, desc: apitest.DescLiveRowPointer},
}

// TestLiveRowSequencePerVerb pins SR-18.6 (AC-DOC-05) on every verb: only kill
// states the short form, and find-missing and spawn only point to it.
func TestLiveRowSequencePerVerb(t *testing.T) {
	for verb := range liveRowWants {
		verbOf(t, verb)
	}
	for _, v := range manifest.Verbs {
		want := liveRowWants[v.Name]
		if n := apitest.LiveRowSequenceCount(v.Description); n != want.sequences {
			t.Errorf("%s description states the live-row sequence %d times; want %d", v.Name, n, want.sequences)
		}
		if n := apitest.LiveRowPointerCount(v.Description); n != want.pointers {
			t.Errorf("%s description carries the live-row pointer %d times; want %d", v.Name, n, want.pointers)
		}
		if want.desc != nil {
			apitest.AssertAgentTextCase(t, v.Name+" description", v.Description, want.desc())
		}
	}
}

// TestReuseReservedLists: make-template's Description and docs/settings.md's
// reserved section name the reuse opt-in by its manifest name (SR-18.16).
func TestReuseReservedLists(t *testing.T) {
	const name = "reuse_finished"
	if desc := siteText(t, "make-template", "", ""); !strings.Contains(desc, name) {
		t.Errorf("make-template description does not reserve %s: %q", name, desc)
	}
	_, thisFile, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "docs", "settings.md"))
	if err != nil {
		t.Fatalf("read docs/settings.md: %v", err)
	}
	_, rest, ok := strings.Cut(string(raw), "\n### Reserved per-invocation params\n")
	if !ok {
		t.Fatal("docs/settings.md has no \"Reserved per-invocation params\" section")
	}
	if section, _, _ := strings.Cut(rest, "\n#"); !strings.Contains(section, "`"+name+"`") {
		t.Errorf("docs/settings.md reserved per-invocation params do not list `%s`: %q", name, section)
	}
}

// TestGoDocStatements pins Client Go doc prose: Kill's unusable-name ErrInternal
// trigger (SR-1.7) and repeated-Kill limitation (decision-0930b Q5); missing is
// not proof of death on FindMissing, Expire, Kill and Resume (SR-18.2); and
// Spawn's ErrInstanceIdCollision bullet's full collision text (SR-18.9).
func TestGoDocStatements(t *testing.T) {
	apitest.AssertDescription(t, clientGoDocProse(t, "Kill"), apitest.DescKillInternalTrigger())
	apitest.AssertDescription(t, clientGoDocProse(t, "Kill"), apitest.DescKillRepeatedAfterLastSession("Kill"))
	for _, method := range []string{"FindMissing", "Expire", "Kill", "Resume"} {
		doc := strings.Join(strings.Fields(clientMethodDoc(t, method)), " ")
		apitest.AssertAgentTextCase(t, "Go doc of (*Client)."+method, doc, apitest.DescMissingNotProof())
	}
	apitest.AssertAgentTextCase(t, "(*Client).Spawn Errors: ErrInstanceIdCollision", goDocErrorBulletText(t, "Spawn", "ErrInstanceIdCollision"),
		apitest.DescInstanceIDCollision(apitest.CollisionSite{Full: true}))
}
