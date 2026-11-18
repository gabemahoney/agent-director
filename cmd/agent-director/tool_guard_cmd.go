package main

import (
	"io"
	"os"
	"slices"
	"strings"

	"github.com/gabemahoney/agent-director/internal/clisetup"
	"github.com/gabemahoney/agent-director/internal/toolguard"
)

// toolGuardInvocation identifies the internal route without parsing or applying
// global flag values. Only the first-argument form is accepted. A global prefix
// (even one with invalid values) identifies a malformed Guard invocation, so it
// receives the Guard's fixed refusal before flag diagnostics or HOME mutation.
// Flag ownership comes from the shared registry, whose globals each take one
// value. An operand belonging to another verb or a global flag is not a verb.
func toolGuardInvocation(argv []string) (args []string, found bool) {
	if len(argv) > 0 && argv[0] == "tool-guard" {
		return argv[1:], true
	}
	names := clisetup.GlobalFlagNames()
	for i := 0; i < len(argv); {
		name, _, equals := strings.Cut(argv[i], "=")
		if !slices.Contains(names, name) {
			return nil, argv[i] == "tool-guard"
		}
		i++
		if !equals {
			i++ // one owned value, even if empty or invalid
		}
	}
	return nil, false
}

// toolGuardHandler is deliberately outside pkg/api and the lifecycle hook.
// No worker can write output; only this caller publishes the selected result.
func toolGuardHandler(args []string) int {
	result := toolguard.Run(args, os.Stdin)
	if result.Stderr != "" {
		if _, err := io.WriteString(os.Stderr, result.Stderr); err != nil {
			// Do not echo OS errors or try another surface on output failure.
			return 2
		}
	}
	return result.ExitCode
}
