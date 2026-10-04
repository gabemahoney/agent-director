package tmux

import "strconv"

// This file holds the socket-taking methods of SR-2.1 other than the create
// (see create.go). Every target is a session id or pane id: never a name, an
// =name, a :0.0 suffix or a pattern. Each method returns only *CallError on
// failure. They are the call set of the shared lookup (lookup.go), the
// find-missing sweep (sweep.go) and the verbs: spawn, read-pane, send-keys,
// pause and kill call them.

// ownerOption is the session user option holding a session's label (SR-3.4).
const ownerOption = "@ad_owner"

// paneOption is the pane user option holding a created pane's label, "<launch
// token> <pane id>" (SR-2.1, SR-3.5; WD 2026-09-29c).
const paneOption = "@ad_pane"

// identityPrefix is the first field of the lookup's server identity line
// (LFR H5; b.47f). It is not a session id, and holds no '#' or '%', which
// display-message would expand.
const identityPrefix = "ad-server"

// The formats of the lookup's server identity read, the lookup and the pane
// listing (SR-2.1), tab separated. The listing formats each end with their
// label field, so a tab inside a label value cannot shift the fields before
// it.
const (
	identityFormat = identityPrefix + "\t#{pid}\t#{start_time}"
	lookupFormat   = "#{session_id}\t#{session_created}\t#{pid}\t#{start_time}\t#{session_name}\t#{@ad_owner}"
	panesFormat    = "#{session_id}\t#{window_index}\t#{pane_index}\t#{pane_id}\t#{pane_pid}\t#{@ad_pane}"
)

// Lookup makes the one-invocation lookup on socket (SR-2.1, SR-3.4): the
// server identity read (display-message -p of identityFormat), then the
// session listing with each session's label, then the global, server and
// global-window @ad_owner reads, as one server step. The identity read
// answers on a server with no session too (tmux's exit-empty off), so an
// empty listing still says which server answered (LFR H5; b.47f; verified on
// tmux 3.2a and 3.3a). Query timeout. Labels are read only here, and are
// returned classified, never raw. Output that does not parse by the rule of
// SR-3.4 (parseLookup) is FailUnrecognized.
func (c *Client) Lookup(socket string) (LookupAnswer, error) {
	out, err := c.runData(CallLookup, socket,
		[]string{"display-message", "-p", identityFormat},
		[]string{"list-sessions", "-F", lookupFormat},
		[]string{"show-options", "-gqv", ownerOption},
		[]string{"show-options", "-sqv", ownerOption},
		[]string{"show-options", "-gwqv", ownerOption})
	if err != nil {
		return LookupAnswer{}, err
	}
	ans, ok, firstLine := parseLookup(out)
	if !ok {
		return LookupAnswer{}, unparseable(CallLookup, out, firstLine)
	}
	return ans, nil
}

// ListPanes lists every pane of the server at socket, list-panes -a (SR-2.1,
// SR-3.7). Query timeout. A shared pane appears once per session showing its
// window. Each pane's #{@ad_pane} is returned as Pane.AdPane: the launch
// token when the value embeds the line's own pane id, else "" (the scope
// guard of SR-3.6), never raw. A line that does not parse is
// FailUnrecognized, its FirstLine the listing's first line cut before its
// pane label field.
func (c *Client) ListPanes(socket string) ([]Pane, error) {
	out, err := c.runData(CallListPanes, socket, []string{"list-panes", "-a", "-F", panesFormat})
	if err != nil {
		return nil, err
	}
	panes, ok, firstLine := parsePanes(out)
	if !ok {
		return nil, unparseable(CallListPanes, out, firstLine)
	}
	return panes, nil
}

// KillPane kills the pane paneID ("%N") on socket (SR-2.1, SR-6.1). Action
// timeout.
func (c *Client) KillPane(socket, paneID string) error {
	return c.runAction(CallKillPane, socket, []string{"kill-pane", "-t", paneID})
}

// KillSessionID kills the session sessionID ("$N") on socket (SR-2.1,
// SR-3.5, SR-6.1). Action timeout.
func (c *Client) KillSessionID(socket, sessionID string) error {
	return c.runAction(CallKillSession, socket, []string{"kill-session", "-t", sessionID})
}

// SendKeysPane types text into the pane paneID on socket with
// send-keys -t <pane id> -l -- <text>, then, when pressEnter is set and the
// text call succeeded, sends a real Enter key in a second call,
// send-keys -t <pane id> Enter (SR-2.1). The "--" ends tmux's options, so a
// text starting with "-" (such as "-x", "--" or "-l") is typed literally and
// never read as a flag. Like every argv element, the text is passed through
// escapeFinalSemicolon (commandArgv), so a text ending in ";" is typed whole
// and never ends the command. Action timeout for each. The error's Call says
// which call failed (CallSendText or CallSendEnter).
func (c *Client) SendKeysPane(socket, paneID, text string, pressEnter bool) error {
	if err := c.runAction(CallSendText, socket, []string{"send-keys", "-t", paneID, "-l", "--", text}); err != nil {
		return err
	}
	if !pressEnter {
		return nil
	}
	return c.runAction(CallSendEnter, socket, []string{"send-keys", "-t", paneID, "Enter"})
}

// SendKeyPane sends one key to the pane paneID on socket, by its tmux key
// name and never typed literally: send-keys -t <pane id> <key> (b.9o4). pause
// sends C-u, which deletes the agent's input from the cursor back to the
// start of its line, before typing /exit. key is a fixed key name the caller
// chooses, never caller text. Action timeout. The error's Call is
// CallSendKey.
func (c *Client) SendKeyPane(socket, paneID, key string) error {
	return c.runAction(CallSendKey, socket, []string{"send-keys", "-t", paneID, key})
}

// CapturePaneID returns the last nLines lines of the pane paneID on socket:
// capture-pane -p [-e] -t <pane id> -S -<n>, -e when ansi is set (SR-2.1).
// Action timeout. The text is the standard output of an exit-0 call.
func (c *Client) CapturePaneID(socket, paneID string, nLines int, ansi bool) (string, error) {
	args := []string{"capture-pane", "-p"}
	if ansi {
		args = append(args, "-e")
	}
	args = append(args, "-t", paneID, "-S", "-"+strconv.Itoa(nLines))
	out, err := c.runData(CallCapture, socket, args)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// SetLabel labels the session sessionID and its pane paneID on socket by
// their ids, in one invocation (SR-2.1, SR-3.5, Appendix F.1; WD 2026-09-29
// STORE, WD 2026-09-29c):
//
//	set-option -t <$N> @ad_owner 'ad1 <token> <$N> <instance id> <store id>'
//	  ; set-option -p -t <%N> @ad_pane '<token> <%N>'
//
// Each value is one argv element, with no -F and no doubling, so a # in the
// instance id stays as written; the ";" is its own element, and every other
// element goes through escapeFinalSemicolon (commandArgv), so neither value
// can end a step. storeID is the writing store's store_meta.store_id, written
// last; the client does not validate it, callers passing
// (*store.Store).StoreID(), which the store's open has already validated
// (SR-5.1). sessionID and paneID are the create reply's. tmux stops at the
// first failing step, so a failure may leave the session labelled and its
// pane not. A failure's *CallError is built from the reply alone, never from
// a value (SR-15). Action timeout. Used for a name containing $ or \ and for
// the one relabel after a failed chained label.
func (c *Client) SetLabel(socket, sessionID, paneID, token, instanceID, storeID string) error {
	value := labelPrefix + token + " " + sessionID + " " + instanceID + " " + storeID
	return c.runAction(CallSetLabel, socket,
		[]string{"set-option", "-t", sessionID, ownerOption, value},
		[]string{"set-option", "-p", "-t", paneID, paneOption, token + " " + paneID})
}
