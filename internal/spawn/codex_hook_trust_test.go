package spawn

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/codexfix"
)

// Hook trust (SR-5.2, RN-5, RN-14): the key and hash of every handler AD
// registers equal what Codex itself lists, read from testdata (FORMAT.md,
// hook-trust-golden.json, never edited here). The shell read-back of the
// quoted paths and the deny command are in codex_shell_test.go.

const goldenPath = "testdata/hook-trust-golden.json"

// goldenNotComposed are the golden scenarios a Codex launch from AD cannot
// produce, with the reason; the hash function is still checked against each
// (TestCodexHashFunctionMatchesGolden). Every other scenario must be composed
// (TestCodexLaunchStateMatchesGolden).
var goldenNotComposed = map[string]string{
	"controls":                                "identity controls, not a layout",
	"plain-same-group-matcher-star":           "AD registers no matcher on the tool events (RN-15)",
	"plain-own-group-nolimit":                 "AD's help entry always passes a limit of 4000",
	"plain-same-group-nolimit":                "AD's help entry always passes a limit of 4000",
	"special-own-group-nolimit":               "AD's help entry always passes a limit of 4000",
	"special-same-group-nolimit":              "AD's help entry always passes a limit of 4000",
	"plain-own-group-help-first-limit4000":    "AD's own entry is group 0, the help entry after it",
	"plain-same-group-help-first-limit4000":   "AD's own entry is group 0, the help entry after it",
	"special-own-group-help-first-limit4000":  "AD's own entry is group 0, the help entry after it",
	"special-same-group-help-first-limit4000": "AD's own entry is group 0, the help entry after it",
}

// goldenLaunch composes the settings AD's launch carries for the scenario's
// fixed inputs: its path, help layout and relay window.
func goldenLaunch(t *testing.T, g codexfix.HookTrustGolden, sc codexfix.GoldenScenario) codexfix.Launch {
	t.Helper()
	args := CodexSettingsArgs(codexSettingsInput{
		Exe: g.BinaryPaths[sc.Path], HelpBin: g.InstalledPaths[sc.Path], Relay: sc.RelayWindow,
		InjectHelp: sc.Layout != "absent", CWD: "/work/proj",
	})
	return codexfix.Split(t, append([]string{"codex"}, args...))
}

// notCodexEvent is whether e names an event AD does not register on Codex
// (SubagentStart and SubagentStop): its golden entries are not composed.
func notCodexEvent(ev string) bool { return ev == "SubagentStart" || ev == "SubagentStop" }

// TestCodexLaunchStateMatchesGolden: for every golden scenario AD's launch can
// produce (both paths, help absent or in its own group, relay windows 86400,
// 3600 and 2147483), composing with those inputs gives each AD entry the
// golden key and hash in hooks.state, and the timeout and limit the entry
// carries are the golden ones passed. In the same-group layout AD does not
// compose, the help entry's key moves, so only its hash is compared.
func TestCodexLaunchStateMatchesGolden(t *testing.T) {
	g := codexfix.LoadHookTrustGolden(t, goldenPath)
	for _, name := range g.ScenarioNames() {
		sc := g.Scenarios[name]
		if reason, skip := goldenNotComposed[name]; skip {
			t.Logf("%s: not composed: %s", name, reason)
			continue
		}
		t.Run(name, func(t *testing.T) {
			l := goldenLaunch(t, g, sc)
			state, _ := l.State()
			for _, e := range sc.Entries {
				if notCodexEvent(e.Event) {
					continue
				}
				hashOnly := e.Kind == "help" && sc.Layout == "same-group"
				if !hashOnly {
					if got, ok := state[e.Key]; !ok || got != e.Hash {
						t.Errorf("%s %s: hooks.state[%s] = %q (present %v); want %q", e.Kind, e.Event, e.Key, got, ok, e.Hash)
					}
				}
				assertGoldenEntry(t, l, e, hashOnly, state)
			}
			if len(l.Hooks()) != len(sc.Entries)-2 {
				t.Errorf("launch carries %d handlers; want the scenario's %d without SubagentStart and SubagentStop", len(l.Hooks()), len(sc.Entries)-2)
			}
			if sc.Layout != "same-group" && len(state) != len(l.Hooks()) {
				t.Errorf("hooks.state has %d entries for %d handlers; want one each", len(state), len(l.Hooks()))
			}
		})
	}
}

// assertGoldenEntry checks the handler of the launch that matches golden entry
// e: its command text, the timeout and limit it passes and (unless hashOnly)
// its position, and for a hashOnly entry that its hash is the golden one.
func assertGoldenEntry(t *testing.T, l codexfix.Launch, e codexfix.GoldenEntry, hashOnly bool, state map[string]string) {
	t.Helper()
	kind := codexfix.HookAD
	if e.Kind == "help" {
		kind = codexfix.HookHelp
	}
	var found []codexfix.Hook
	for _, h := range l.HooksFor(e.Event) {
		if h.Kind == kind {
			found = append(found, h)
		}
	}
	if len(found) != 1 {
		t.Errorf("%s %s: %d handlers in the launch; want 1", e.Kind, e.Event, len(found))
		return
	}
	h := found[0]
	if h.Command != e.Command {
		t.Errorf("%s %s: command %q; want %q", e.Kind, e.Event, h.Command, e.Command)
	}
	if !hashOnly && (h.Group != e.Group || h.Handler != e.Handler) {
		t.Errorf("%s %s: at group %d handler %d; want %d, %d", e.Kind, e.Event, h.Group, h.Handler, e.Group, e.Handler)
	}
	if h.HasTimeout != (e.TimeoutPassed != nil) || (e.TimeoutPassed != nil && h.Timeout != *e.TimeoutPassed) {
		t.Errorf("%s %s: timeout %d (given %v); want %v", e.Kind, e.Event, h.Timeout, h.HasTimeout, e.TimeoutPassed)
	}
	if h.HasLimit != (e.AdditionalContextLimitPassed != nil) || (e.AdditionalContextLimitPassed != nil && h.Limit != *e.AdditionalContextLimitPassed) {
		t.Errorf("%s %s: limit %d (given %v); want %v", e.Kind, e.Event, h.Limit, h.HasLimit, e.AdditionalContextLimitPassed)
	}
	if hashOnly {
		key := fmt.Sprintf("%s:%s:%d:%d", codexHookStateSource, codexTestLabels[h.Event], h.Group, h.Handler)
		if got := state[key]; got != e.Hash {
			t.Errorf("%s %s: hash %q at %s; want %q", e.Kind, e.Event, got, key, e.Hash)
		}
	}
}

// TestCodexHashFunctionMatchesGolden: the key and hash functions, given the
// inputs of every golden entry of every scenario (the controls' timeout
// defaults and clamps, matcher rules and limit rules, the layouts and paths
// included), return Codex's own key and hash. An entry that passes a limit of
// 0 is left out: AD's handler type reads 0 as none, which Codex hashes
// differently, and no AD entry passes one.
func TestCodexHashFunctionMatchesGolden(t *testing.T) {
	g := codexfix.LoadHookTrustGolden(t, goldenPath)
	for _, name := range g.ScenarioNames() {
		t.Run(name, func(t *testing.T) {
			for _, e := range g.Scenarios[name].Entries {
				if e.AdditionalContextLimitPassed != nil && *e.AdditionalContextLimitPassed == 0 {
					continue
				}
				if _, known := codexEventLabels[hookEventName(e.Event)]; !known {
					continue // Codex subagents are untracked
				}
				ev := hookEventName(e.Event)
				grp := codexGroup{}
				if e.Matcher != nil {
					grp = codexGroup{Matcher: *e.Matcher, HasMatcher: true}
				}
				h := codexHandler{Command: e.Command}
				if e.TimeoutPassed != nil {
					h.Timeout = *e.TimeoutPassed
				}
				if e.AdditionalContextLimitPassed != nil {
					h.ContextLimit = *e.AdditionalContextLimitPassed
				}
				id := string(codexIdentityJSON(ev, grp, h))
				if id != e.Identity {
					t.Errorf("%s %d:%d identity = %s; want %s", e.Event, e.Group, e.Handler, id, e.Identity)
				}
				if got := codexHandlerHash(ev, grp, h); got != e.Hash {
					t.Errorf("%s %d:%d hash = %s; want %s", e.Event, e.Group, e.Handler, got, e.Hash)
				}
				if got := codexEntryKey(ev, e.Group, e.Handler); got != e.Key {
					t.Errorf("%s %d:%d key = %s; want %s", e.Event, e.Group, e.Handler, got, e.Key)
				}
			}
		})
	}
}

// TestCodexHashWorkedExample: FORMAT.md section 1's example, an entry that
// passes a timeout of 5 on PermissionRequest, serialises as printed there and
// hashes to the printed value.
func TestCodexHashWorkedExample(t *testing.T) {
	const identity = `{"event_name":"permission_request","hooks":[{"async":false,"command":"exec '/x' hook --agent codex --timeout 5","timeout":5,"type":"command"}]}`
	const hash = "sha256:1049b8d0b6a31c6c2fe44795599a8730a632db112fcc9ed062f36586ba4de17d"
	h := codexHandler{Command: codexHookCommand("/x", hookPermissionRequest, 5), Timeout: 5}
	if got := string(codexIdentityJSON(hookPermissionRequest, codexGroup{}, h)); got != identity {
		t.Errorf("identity = %s; want %s", got, identity)
	}
	if got := codexHandlerHash(hookPermissionRequest, codexGroup{}, h); got != hash {
		t.Errorf("hash = %s; want %s", got, hash)
	}
}

// TestCodexDenyGroupTrust: with disable_ask_user on, the deny group is
// PreToolUse group 1, handler 0 (AD's own entry is group 0), and hooks.state
// carries its key with the hash FORMAT.md's rules give: the tool-event
// identity with the matcher, the default timeout of 600 and no limit. The
// golden data holds no deny entry; the expected text is written out here, with
// its command encoded by encoding/json rather than the code under test.
func TestCodexDenyGroupTrust(t *testing.T) {
	args := CodexSettingsArgs(codexSettingsInput{Exe: "/x/ad", Relay: 86400, QuestionOff: true, CWD: "/work/proj"})
	l := codexfix.Split(t, append([]string{"codex"}, args...))
	deny := l.HooksOfKind(codexfix.HookDeny)
	if len(deny) != 1 || deny[0].Event != "PreToolUse" || deny[0].Group != 1 || deny[0].Handler != 0 {
		t.Fatalf("deny groups = %+v; want one at PreToolUse group 1 handler 0", deny)
	}
	var cmd bytes.Buffer
	enc := json.NewEncoder(&cmd)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(deny[0].Command); err != nil {
		t.Fatal(err)
	}
	identity := `{"event_name":"pre_tool_use","hooks":[{"async":false,"command":` + strings.TrimSpace(cmd.String()) +
		`,"timeout":600,"type":"command"}],"matcher":"request_user_input_async"}`
	sum := sha256.Sum256([]byte(identity))
	wantHash := "sha256:" + hex.EncodeToString(sum[:])
	const wantKey = "/<session-flags>/config.toml:pre_tool_use:1:0"
	state, _ := l.State()
	if got, ok := state[wantKey]; !ok || got != wantHash {
		t.Errorf("hooks.state[%s] = %q (present %v); want %q", wantKey, got, ok, wantHash)
	}
	if deny[0].HasTimeout || deny[0].HasLimit {
		t.Errorf("deny entry passes timeout %v limit %v; want neither", deny[0].HasTimeout, deny[0].HasLimit)
	}
}
