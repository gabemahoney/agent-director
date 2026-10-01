package main

import (
	"fmt"
	"os"

	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
)

// legacyMain handles the name-based argv (no leading -u), as the fake always
// has; the duplicate-name reply's wording comes from the replay catalogue. A
// name-based capture-pane or send-keys is no longer answered or logged: no
// client method sends one.
func legacyMain() {
	if len(os.Args) < 2 {
		// `tmux` with no subcommand; nothing to record. Exit 0 so the
		// caller's `command -v tmux` probes still succeed.
		os.Exit(0)
	}
	sub := os.Args[1]
	if sub == "has-session" {
		// Production code calls HasSession as a precondition probe. The
		// real tmux exits non-zero for absent sessions; mirror that so a
		// "session already exists" branch isn't accidentally taken.
		os.Exit(1)
	}

	switch sub {
	case "new-session":
		logArgv()
		// Optional per-name failure injection: real tmux refuses to create
		// a session whose name matches a currently-live one.
		if name := sessionNameFromArgs(os.Args[2:]); failNewSession(name) {
			fmt.Fprint(os.Stderr, duplicateReply(name).Stderr)
			os.Exit(duplicateReply(name).Exit)
		}
	case "kill-session":
		logArgv()
	}
	os.Exit(0)
}

// failNewSession reports whether FAKE_TMUX_FAIL_NEWSESSION_NAME is set and
// equals name.
func failNewSession(name string) bool {
	fail := os.Getenv(faketmuxfix.EnvFailNewSessionName)
	return fail != "" && name == fail
}

// duplicateReply is the catalogue's "duplicate session" reply for a create
// with -s name: the stored form of the name, on standard error, exit 1.
func duplicateReply(name string) tmuxfix.Entry {
	return tmuxfix.Duplicate(faketmuxfix.StoredForm(name))
}

// sessionNameFromArgs returns the value passed to tmux's `-s` flag (the
// session name) in either split (`-s name`) or equals (`-s=name`) form.
// Returns "" if not found. Mirrors the production caller's argv shape;
// the legacy form does no real flag parsing.
func sessionNameFromArgs(args []string) string {
	for i, a := range args {
		switch {
		case a == "-s" && i+1 < len(args):
			return args[i+1]
		case len(a) > 3 && a[:3] == "-s=":
			return a[3:]
		}
	}
	return ""
}
