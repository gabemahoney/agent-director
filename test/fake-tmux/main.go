// Command fake-tmux is the test stand-in for tmux (SRD SR-20.3): the
// subprocess path that exercises the production tmux client end to end. CLI,
// MCP, envelope-diff and TypeScript smoke tests put it first on PATH (or pass
// it as the tmux command). Build it with `make fake-tmux`, or in a Go test
// with faketmuxfix.Binary, which builds it once per test binary.
//
// Every reply text it prints comes from the replay catalogue
// (internal/testsupport/tmuxfix); it has no reply wording of its own. The
// table schema, the control variable names and the injection helpers live
// in internal/testsupport/faketmuxfix, so tests never write a table file or
// spell a variable name.
//
// # Socket form (the Phase 1 call set, SR-2.1)
//
// argv: -u -S <socket> <command> [; <command> ...]. Commands split as in
// tmux's command parser: an argument ending in an unescaped ";" ends the
// command (a standalone ";" is the separator the client sends; any text
// before the ";" stays as the command's last argument), and an argument
// ending in `\;` is one argument with that backslash removed (so the text
// call's escaped final ";" arrives as ";"). An empty command is argv the fake
// does not understand. The commands run in order against the socket's
// table, and the first that fails ends the invocation with its exit status,
// as in tmux.
// Answers go to standard output, replies to standard error (Appendix E.10).
// The fake expands the -F formats the client sends (#{session_id},
// #{session_created}, #{pid}, #{start_time}, #{session_name}, #{@ad_owner},
// #{window_index}, #{pane_index}, #{pane_id}, #{pane_pid}, #{@ad_pane};
// "##" is "#").
//
//   - list-sessions -F <fmt>: one line per session, sorted by name. A
//     session's #{@ad_owner} is the server or global-window scope value when
//     one is set, else its own label, else the global value (as tmux
//     resolves a user option in a format).
//   - show-options -gqv|-sqv|-gwqv @ad_owner: the global, server or
//     global-window value with a newline, nothing when unset. With the
//     listing this is the lookup.
//   - list-panes -a -F <fmt>: every pane of every session. A pane's
//     #{@ad_pane} is its own value only (per-pane options; "" when unset,
//     as on a pane split from a labelled one); the fake models no window,
//     session, global or server @ad_pane, so a borrowed value is written
//     into the pane's table entry as the text tmux would list.
//   - kill-pane -t <%N>: removes the pane; the last pane removes its session.
//   - kill-session -t <$N>: removes the session.
//   - send-keys -t <%N> -l -- <text> and send-keys -t <%N> Enter: no effect
//     beyond the log.
//   - capture-pane -p [-e] -t <%N> -S -<n>: the pane's capture text, else
//     FAKE_TMUX_PANE_OUTPUT, else a fixed two-line stub.
//   - new-session -d -s <name> -c <cwd> [-e K=V ...] -P -F <fmt> -- <argv>:
//     adds a session named by the name's stored form (tmuxfix.StoredNames)
//     with one pane and new $N and %N, starting a server when the socket has
//     none, and prints the -P reply. A name already held gets the
//     catalogue's duplicate reply (exit 1) and creates nothing.
//   - set-option [-F] -t <target> @ad_owner <value>: sets the target
//     session's label; with -F the value is expanded for that session. The
//     target is a session id, or =<name>: matching a stored name exactly; a
//     colon-less =<name>, any other target or no target fails with the
//     catalogue's recorded "no such session" line (exit 1), so a chain that
//     is untargeted or colon-less is never silently accepted.
//   - set-option -p [-F] -t <target> @ad_pane <value>: sets the pane label,
//     a per-pane option, of the target pane only; with -F the value is
//     expanded for that pane (#{pane_id}). The target is a pane id, or
//     =<name>: as above, meaning that session's first pane (the fake's
//     active pane). An unknown pane id or any other target fails with the
//     catalogue's "no such pane" reply, except that an =<name> form that
//     matches nothing or no target fails with the "no such session" line,
//     as for @ad_owner. The create chain and the label by id
//     each end with this step, after @ad_owner's.
//
// A target that matches nothing fails with the catalogue's reply for it
// (reply/cant-find-pane, reply/cant-find-session, reply/no-such-session),
// exit 1. A socket with no table answers as a server with no sessions: the
// lookup and the pane listing are empty with exit 0.
//
// Argv the fake does not understand exits 2 with no output; a table it
// cannot read or write exits 3 with no output.
//
// # Tables
//
// One table per socket (faketmuxfix.Table: server pid and start time,
// scope values, sessions with id, creation time, stored name and label, and
// panes with window, index, id, pid and capture text), kept in the file
// "<socket>.fake-tmux.json" beside the socket path, or, when FAKE_TMUX_TABLES
// names a directory, in that directory (faketmuxfix.TablePath). Each
// invocation locks the table and replaces it atomically.
//
// # Injections
//
// A table's injections (faketmuxfix.Injection) replace or delay the answer
// to one call kind (tmux.Call: lookup, pane listing, pane kill, session
// kill, text send, Enter send, capture, session creation, label by id) on
// that socket, for every call or the next N: a catalogue entry's exact
// bytes and exit status (reply), a bare exit status (exit), a hang the
// client's timeout must end (hang; the fake exits 1 on its own after a
// bound), a normal answer and exit while a child holds the output pipes open
// (hold-pipes), and a create whose chained label step fails (chain-fails).
// With Effect set, the call's normal effect on the table is applied first
// (for example create and label, then hang).
//
// # Control variables
//
//   - FAKE_TMUX_LOG: a file every invocation (both forms; legacy: the three
//     logged subcommands) appends its argv to, one element per
//     line followed by a "---" line.
//   - FAKE_TMUX_PANE_OUTPUT: capture text (see capture-pane above).
//   - FAKE_TMUX_FAIL_NEWSESSION_NAME: a create (either form) whose -s name
//     equals it prints the catalogue's "duplicate session: <stored name>" on
//     standard error and exits 1, creating nothing.
//   - FAKE_TMUX_TABLES: the directory holding the tables (see Tables).
//
// # Legacy form
//
// argv without a leading -u is the name-based call set the name-based client
// methods still send: new-session, send-keys and kill-session log their argv
// and exit 0; has-session exits 1; new-session honours
// FAKE_TMUX_FAIL_NEWSESSION_NAME; anything else (a name-based capture-pane
// included: no client method sends one) exits 0 with no output or side
// effects. No table is read or written.
package main

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
)

// holdPipesArg is argv[1] of the child a hold-pipes injection starts: it
// sleeps for argv[2] milliseconds holding the inherited output pipes.
const holdPipesArg = "__fake-tmux-hold-pipes__"

// Exit statuses of the fake's own failures (no output on either).
const (
	exitBadArgv  = 2
	exitBadTable = 3
)

// stubPaneOutput is the capture text when neither the table nor
// FAKE_TMUX_PANE_OUTPUT gives one; read-pane CLI tests pin it.
const stubPaneOutput = "fake pane line one\nfake pane line two\n"

func main() {
	if len(os.Args) == 3 && os.Args[1] == holdPipesArg {
		ms, _ := strconv.Atoi(os.Args[2])
		time.Sleep(boundOf(ms, 0))
		os.Exit(0)
	}
	if len(os.Args) >= 2 && os.Args[1] == "-u" {
		os.Exit(socketMain(os.Args[2:]))
	}
	legacyMain()
}

// boundOf turns a millisecond count into a wait capped at MaxBound, with
// def for a count of zero or less.
func boundOf(ms int, def time.Duration) time.Duration {
	d := time.Duration(ms) * time.Millisecond
	if ms <= 0 {
		d = def
	}
	return min(d, faketmuxfix.MaxBound)
}

// capturePaneOutput is the capture text for a pane with none of its own:
// FAKE_TMUX_PANE_OUTPUT, else the stub.
func capturePaneOutput() string {
	if override := os.Getenv(faketmuxfix.EnvPaneOutput); override != "" {
		return override
	}
	return stubPaneOutput
}

// logArgv appends the current argv (one element per line, followed by a
// "---" separator) to $FAKE_TMUX_LOG. Silent no-op when the env var is
// unset so tests that don't care about argv don't blow up.
func logArgv() {
	logPath := os.Getenv(faketmuxfix.EnvLog)
	if logPath == "" {
		return
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		// Logging failure must not fail the call — production tmux would
		// have succeeded by this point.
		return
	}
	defer f.Close()
	// One write per record, so records of concurrent invocations do not
	// interleave.
	var rec strings.Builder
	for _, a := range os.Args {
		rec.WriteString(a + "\n")
	}
	rec.WriteString("---\n")
	_, _ = f.WriteString(rec.String())
}
