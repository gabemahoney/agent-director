package tmux

import "strings"

// createReplyFormat is the create's -P -F reply format (SR-2.1).
const createReplyFormat = "#{session_id} #{pid} #{start_time} #{pane_id} #{pane_pid}"

// instanceIDEnv is the one AGENT_DIRECTOR_* variable the create sets in the
// new session, through its -e entry (SR-3.12).
const instanceIDEnv = "AGENT_DIRECTOR_INSTANCE_ID"

// NeedsLabelByID reports whether a session named name must be labelled by id
// rather than by the create's chained set-option: a name containing $ or \.
// tmux stores such a name escaped, and '=$7:' is read as session id $7, so a
// chain would label another session (SR-3.5, provenance-fresh.md F3).
func NeedsLabelByID(name string) bool { return strings.ContainsAny(name, `$\`) }

// NewSession runs the one create invocation of SR-2.1 and SR-3.5 on socket,
// with the create timeout:
//
//	new-session -d -s <name> -c <cwd, format-escaped> -e AGENT_DIRECTOR_INSTANCE_ID=<id>
//	  [-e KEY=VAL ...] -P -F '<reply format>' -- <command>
//	  ; set-option -F -t =<name>: @ad_owner 'ad1 <token> #{session_id} <id, format-escaped> <store id>'
//	  ; set-option -p -F -t =<name>: @ad_pane '<token> #{pane_id}'
//
// tmux expands formats in the -c value, where "#(...)" would run a shell
// command, so the cwd goes through escapeFormat and the agent starts in cwd
// exactly as given, at spawn and at resume alike (b.dsx); the cwd stored on
// the row and reported by agent-director is the unescaped one.
//
// The session label is five fields (WD 2026-09-29 STORE): the store id,
// storeID, is the writing store's store_meta.store_id and goes last,
// unchanged; only the instance id goes through escapeFormat, so that -F
// keeps it literal. The client does not validate storeID: callers pass
// (*store.Store).StoreID(), which the store's open has already validated
// (SR-5.1). The pane label @ad_pane, "<token> <pane id>", is a per-pane
// option set on the new session's one pane, so a pane split from it later
// does not carry it; it is read only through ListPanes (Pane.AdPane) to find
// a launch's pane after a lost reply (SR-3.5, SR-3.6; WD 2026-09-29c).
//
// Both chained targets are exactly =<name>: (never untargeted, never
// colon-less). For a name for which NeedsLabelByID holds there is no chain:
// NewSession makes that one invocation, never calls SetLabel, and returns the
// parsed reply with a nil error whatever the exit status; the caller then
// makes the one label-by-id call, which sets both labels (SR-3.5).
//
// envs entries other than AGENT_DIRECTOR_INSTANCE_ID follow in key order and
// are passed as given (other AGENT_DIRECTOR_* keys included); an
// AGENT_DIRECTOR_INSTANCE_ID key in envs is dropped, the -e entry from
// instanceID being the only one. The client's own environment has every
// AGENT_DIRECTOR_* variable removed, as for every call.
//
// name, cwd, every -e entry and every command element are passed to tmux as
// written, an element ending in ";" included: each argv element but the
// chain's ";" separators has its final ";" escaped (see createCommands), so
// none can end the new-session and start another tmux command (b.ukw).
//
// The reply is parsed from standard output whatever the exit status: a reply
// means the session was created. A reply with exit 0 is returned with a nil
// error; a reply with a non-zero exit and a chain is returned with FailLabel
// (a chained label step failed: the session label's or the pane label's; tmux
// stops the chain at the first failure). With no parseable reply, a non-zero exit
// is recognised from the first line of standard error (FailDuplicate,
// FailSocketDenied, FailNoServer, FailNoSocket, else FailUnrecognized), and
// an exit 0, or a reply cut short by the pipe-close wait, is
// FailUnrecognized. The *CallError's ExitStatus and HadStdout tell these
// apart for the caller.
func (c *Client) NewSession(socket, name, cwd string, envs map[string]string, command []string, token, instanceID, storeID string) (CreateReply, error) {
	chained := !NeedsLabelByID(name)
	res := c.invoke(CallCreate, socket, createCommands(name, cwd, envs, command, token, instanceID, storeID, chained)...)
	if res.Status == RunNotStarted || res.Status == RunTimedOut {
		return CreateReply{}, c.runFailure(CallCreate, res)
	}
	if reply, ok := parseCreateReply(res.Stdout); ok {
		if res.ExitStatus == 0 || !chained {
			return reply, nil
		}
		return reply, &CallError{Call: CallCreate, Failure: FailLabel,
			ExitStatus: res.ExitStatus, HadStdout: true}
	}
	if e := c.runFailure(CallCreate, res); e != nil {
		return CreateReply{}, e
	}
	firstLine := firstLineOf(res.Stdout)
	if res.Status == RunPipesCut {
		firstLine = firstLineOutputCut
	}
	return CreateReply{}, &CallError{Call: CallCreate, Failure: FailUnrecognized, FirstLine: firstLine,
		ExitStatus: res.ExitStatus, HadStdout: len(res.Stdout) > 0}
}

// createCommands composes the create's command list, which invoke turns into
// the argv after "-u -S <socket>" (commandArgv): the new-session, its -c
// value the cwd through escapeFormat (b.dsx), then, when chained, two
// set-options, each after a standalone ";" element: the session label
// "ad1 <token> #{session_id} <instance id through escapeFormat> <store id>"
// and the pane label "<token> #{pane_id}" with -p (SR-2.1, SR-3.5; WD
// 2026-09-29 STORE, WD 2026-09-29c). Every element other than those two
// separators then goes through escapeFinalSemicolon, so a name, cwd, -e entry
// or command element ending in ";" (a claude argument "x;", say) reaches tmux
// as written and never ends the new-session to start a command of the
// caller's choosing (b.ukw). The two escapes compose: escapeFormat neither
// adds nor removes a ";", and tmux's command parser drops the backslash
// escapeFinalSemicolon adds before new-session expands the format, so a cwd
// "/a#;" is sent as "/a##\;" and expands to "/a#;". The chained target
// =<name>: always ends in ":", so it is never escaped, and tmux matches it
// against the name it stores, which is the unescaped name.
func createCommands(name, cwd string, envs map[string]string, command []string, token, instanceID, storeID string, chained bool) [][]string {
	create := []string{"new-session", "-d", "-s", name, "-c", escapeFormat(cwd), "-e", instanceIDEnv + "=" + instanceID}
	rest := make(map[string]string, len(envs))
	for k, v := range envs {
		if k != instanceIDEnv {
			rest[k] = v
		}
	}
	for _, kv := range sortedEnvFlags(rest) {
		create = append(create, "-e", kv)
	}
	create = append(create, "-P", "-F", createReplyFormat, "--")
	create = append(create, command...)
	if !chained {
		return [][]string{create}
	}
	value := labelPrefix + token + " #{session_id} " + escapeFormat(instanceID) + " " + storeID
	target := "=" + name + ":"
	return [][]string{create,
		{"set-option", "-F", "-t", target, ownerOption, value},
		{"set-option", "-p", "-F", "-t", target, paneOption, token + " #{pane_id}"}}
}

// escapeFormat returns text escaped for tmux's format expansion, so that the
// expansion gives text back exactly: each run of "#" is doubled ("##"
// expands to "#"), except a run directly before "[", which is left as
// written. Once escaped, no "#(...)" in text runs a shell command and no
// "#{...}" or "#S"-style alias is replaced (b.dsx). A run of "#" before "["
// starts a style, which the expansion copies through unchanged whether
// doubled or not, so doubling it would add "#" to the result (tmux 3.2a to
// 3.5a, format_expand1). Used for the create's -c cwd and for the instance
// id in its -F label; the create's -s name is expanded too, but spawn never
// stores a name holding "#".
func escapeFormat(text string) string {
	if !strings.Contains(text, "#") {
		return text
	}
	var b strings.Builder
	b.Grow(len(text) + strings.Count(text, "#"))
	for i := 0; i < len(text); {
		j := i
		for j < len(text) && text[j] == '#' {
			j++
		}
		if j == i {
			b.WriteByte(text[i])
			i++
			continue
		}
		run := text[i:j]
		b.WriteString(run)
		if j == len(text) || text[j] != '[' {
			b.WriteString(run)
		}
		i = j
	}
	return b.String()
}
