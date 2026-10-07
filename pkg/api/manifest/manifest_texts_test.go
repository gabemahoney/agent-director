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
	return tokens("pre_trust values", "ok = the folder-trust entry was written", "skipped = pre-trust was off for this launch",
		skippedWhy, "nothing was attempted", "failed = pre-trust was attempted", "entry was not written", "launch still proceeds")
}

// manifestTextCases lists the texts and their cases: find-missing (SR-18.9,
// SR-18.7, SR-3.8, SR-11.7, SR-18.3, SR-18.11); kill (SR-18.9, SR-6.1,
// SR-1.7); spawn's launch rules (SR-18.1, SR-9.3, SR-9.4), ErrInternal
// triggers (AC-CAT-03), param rules (SR-9.2, SR-18.9, SR-18.10, SR-22.6);
// pre-trust (SR-22.6); launch_started_at (SR-22.2); list's liveness fields
// (SR-8.3); get's tmux socket (SR-3.3, SR-16.1); the pane verbs' tmux error
// classes (SR-18.1); missing is not proof of death (SR-18.2, AC-DOC-02: the
// full sentence, or the short form, decision-0930e); and make-template naming
// spawn's per-call params by their manifest names, flag beside (b.c4u).
func manifestTextCases() []textCase {
	cases := []textCase{
		{verb: "find-missing", own: true, c: apitest.DescFindMissingGrace()},
		{verb: "find-missing", own: true, c: apitest.DescFindMissingManifest()},
		{verb: "find-missing", field: "ids", c: apitest.DescFindMissingField(apitest.FindMissingIDs)},
		{verb: "find-missing", field: "unverified_ids", c: apitest.DescFindMissingField(apitest.FindMissingUnverifiedIDs)},
		{verb: "kill", c: apitest.DescKillManifest()},
		{verb: "spawn", c: apitest.DescSpawnLaunchTimeoutRule()},
		{verb: "spawn", c: apitest.DescSpawnScanRefusal()},
		{verb: "spawn", c: apitest.DescSpawnHeldName()},
		{verb: "spawn", c: tokens("pre-check ErrInternal", "ErrInternal", "collision pre-check", "read the store")},
		{verb: "spawn", c: tokens("reuse ErrInternal triggers", "ErrInternal, nothing created or changed",
			"a reuse's archive of the previous session", "or its change failed", "a busy store included")},
		{verb: "spawn", param: "tmux_session_name", c: apitest.DescSpawnSessionNameParam()},
		{verb: "spawn", param: "tmux_session_name", c: tokens("rejects dollar and backslash", `'$'`, `'\'`)},
		{verb: "spawn", param: "claude_instance_id", c: tokens("rejects control characters", "control character", "ErrInvalidFlags")},
		{verb: "spawn", param: "claude_instance_id", c: apitest.DescInstanceIDCollision(apitest.CollisionSite{})},
		{verb: "spawn", param: "no_pre_trust", c: tokens("recorded for the life", "recorded on the row for its life",
			"every resume of that life follows it")},
		{verb: "spawn", param: "reuse_finished", c: apitest.DescReuseFinishedParam()},
		{verb: "resume", c: tokens("pre-trust", "best-effort pre-trust", "unless the spawn that began the row's life",
			"no_pre_trust", "pre-trust failure never fails the resume")},
		{verb: "spawn", field: "pre_trust", c: preTrustField("the caller passed no_pre_trust")},
		{verb: "resume", field: "pre_trust", c: preTrustField("the spawn that began the row's life turned it off with no_pre_trust")},
		{verb: "status", field: "launch_started_at", c: apitest.DescLaunchStartedAtField(false)},
		{verb: "get", field: "launch_started_at", c: apitest.DescLaunchStartedAtField(false)},
		{verb: "list", field: "spawns", c: apitest.DescLaunchStartedAtField(true)},
		{verb: "list", field: "spawns", c: tokens("liveness fields", "liveness_unverified_since", "liveness_note")},
		{verb: "get", field: "tmux_socket", c: apitest.DescTmuxSocketField()},
		{verb: "get", c: tokens("tmux socket", "tmux socket")},
		{verb: "find-missing", c: apitest.DescMissingNotProof()},
		{verb: "find-missing", field: "ids", c: apitest.DescMissingNotProof()},
		{verb: "make-template", param: "relay_mode", c: tokens("per-call spelling", "Per-call relay_mode (--relay-mode on the CLI) overrides.")},
		{verb: "make-template", param: "extra_env", c: tokens("per-call spelling",
			"Per-call extra_env (--extra-env on the CLI) merges by key; per-call wins on collision.")},
	}
	for _, verb := range []string{"spawn", "resume"} {
		cases = append(cases, textCase{verb: verb, c: tokens("names pre_trust", "Returns the claude_instance_id and pre_trust", "ok, skipped or failed")})
	}
	for _, s := range []struct{ verb, field string }{{"status", "state"}, {"get", "state"}, {"list", "spawns"}} {
		cases = append(cases, textCase{verb: s.verb, field: s.field, c: apitest.DescMissingNotProof()})
	}
	for _, verb := range []string{"kill", "resume", "pause", "expire"} {
		cases = append(cases, textCase{verb: verb, c: apitest.DescMissingNotProofShort()})
	}
	for _, v := range []apitest.PaneVerb{apitest.PaneReadPane, apitest.PaneSendKeys, apitest.PanePause} {
		def, _ := manifest.Lookup(string(v)) // a missing verb fails at siteText
		cases = append(cases, textCase{verb: string(v), c: apitest.DescPaneManifest(v, def.ErrorNames)})
	}
	return cases
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
