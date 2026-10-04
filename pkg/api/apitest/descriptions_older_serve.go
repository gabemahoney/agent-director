package apitest

import "fmt"

// descriptions_older_serve.go holds the shared description helper's case for
// the older-serve sentence that ends each spawn and list parameter text
// b.c4u renamed or b.7or made MCP decode (DescOlderServeParam). Parameter
// texts reach MCP tools/list, surface.json and the generated references,
// never help.

// olderServeReleases names the releases whose MCP serve process predates
// b.c4u and b.7or; 0.11.0-rc.1 reports 0.11.0, so the version check cannot
// tell it from the fixed release.
const olderServeReleases = "(0.10.x and earlier, or 0.11.0-rc.1, which counts as 0.11.0"

// The two older-serve sentence forms: a renamed param, which an older serve
// process knew only by its dashed name, and a param it did not decode under
// either spelling. Each form must not carry the other's claim.
const (
	olderServeRenamed   = "Over MCP, a serve process from before the rename " + olderServeReleases
	olderServeUndecoded = "Over MCP, a serve process from before the fix " + olderServeReleases + ")"
	knownOnlyAs         = "knows this parameter only as "
	eitherSpelling      = "silently ignores this parameter under either spelling"
)

// DescOlderServeParam is the older-serve sentence of verb's param text (b.c4u,
// b.7or): the releases, then for a renamed param the dashed name it was known
// by and that the new name is silently ignored, with its consequence; for a
// param older serve processes never decoded, that it is ignored under either
// spelling, with list's consequence. Check it with AssertAgentTextCase.
func DescOlderServeParam(verb, param string) DescCase {
	renamed := func(tail string) DescCase {
		return DescCase{Require: []string{olderServeRenamed + tail}, MustNot: []string{eitherSpelling}}
	}
	undecoded := func(tail string) DescCase {
		return DescCase{Require: []string{olderServeUndecoded + " " + eitherSpelling + tail}, MustNot: []string{knownOnlyAs}}
	}
	cases := map[string]DescCase{
		"spawn relay_mode": renamed(") " + knownOnlyAs + "relay-mode and silently ignores relay_mode."),
		"spawn extra_env": renamed(") " + knownOnlyAs + "extra-env and silently ignores extra_env, " +
			"so its variables (CLAUDE_CONFIG_DIR included) are not set."),
		"spawn reuse_finished": renamed(" and " + knownOnlyAs + "reuse-finished) silently ignores reuse_finished, " +
			"so a finished row gives ErrInstanceIdCollision."),
		"spawn no_pre_trust":      undecoded("."),
		"spawn tmux_session_name": undecoded("."),
		"list tmux_session_name":  undecoded(", so list is not filtered by session name."),
	}
	c, ok := cases[verb+" "+param]
	if !ok {
		panic(fmt.Sprintf("apitest: no older-serve sentence for %s %s", verb, param))
	}
	c.Name = verb + " manifest, " + param + " parameter, older serve processes"
	return c
}
