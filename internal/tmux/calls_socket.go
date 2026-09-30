package tmux

import (
	"strconv"
	"strings"
)

// This file holds the socket-taking methods of SR-2.1 other than the create
// (see create.go). Every target is a session id or pane id: never a name, an
// =name, a :0.0 suffix or a pattern. Each method returns only *CallError on
// failure. No verb calls them in this release step; they are the call set the
// shared lookup and the verbs move onto.

// ownerOption is the session user option holding a session's label (SR-3.4).
const ownerOption = "@ad_owner"

// The -F formats of the lookup and the pane listing (SR-2.1), tab separated.
const (
	lookupFormat = "#{session_id}\t#{session_created}\t#{pid}\t#{start_time}\t#{session_name}\t#{@ad_owner}"
	panesFormat  = "#{session_id}\t#{window_index}\t#{pane_index}\t#{pane_id}\t#{pane_pid}"
)

// cmdSeparator is tmux's command separator, passed as its own argv element.
const cmdSeparator = ";"

// Lookup makes the one-invocation lookup on socket (SR-2.1, SR-3.4): the
// session listing with each session's label, then the global, server and
// global-window @ad_owner reads, as one server step. Query timeout. Labels
// are read only here, and are returned classified, never raw. Output that
// does not parse by the rule of SR-3.4 is FailUnrecognized.
func (c *Client) Lookup(socket string) (LookupAnswer, error) {
	out, err := c.runData(CallLookup, socket,
		"list-sessions", "-F", lookupFormat,
		cmdSeparator, "show-options", "-gqv", ownerOption,
		cmdSeparator, "show-options", "-sqv", ownerOption,
		cmdSeparator, "show-options", "-gwqv", ownerOption)
	if err != nil {
		return LookupAnswer{}, err
	}
	ans, ok, firstLine := parseLookup(out)
	if !ok {
		return LookupAnswer{}, unparseable(CallLookup, firstLine)
	}
	return ans, nil
}

// ListPanes lists every pane of the server at socket, list-panes -a (SR-2.1,
// SR-3.7). Query timeout. A shared pane appears once per session showing its
// window. A line that does not parse is FailUnrecognized.
func (c *Client) ListPanes(socket string) ([]Pane, error) {
	out, err := c.runData(CallListPanes, socket, "list-panes", "-a", "-F", panesFormat)
	if err != nil {
		return nil, err
	}
	panes, ok, firstLine := parsePanes(out)
	if !ok {
		return nil, unparseable(CallListPanes, firstLine)
	}
	return panes, nil
}

// KillPane kills the pane paneID ("%N") on socket (SR-2.1, SR-6.1). Action
// timeout.
func (c *Client) KillPane(socket, paneID string) error {
	return c.runAction(CallKillPane, socket, "kill-pane", "-t", paneID)
}

// KillSessionID kills the session sessionID ("$N") on socket (SR-2.1,
// SR-3.5, SR-6.1). Action timeout.
func (c *Client) KillSessionID(socket, sessionID string) error {
	return c.runAction(CallKillSession, socket, "kill-session", "-t", sessionID)
}

// SendKeysPane types text into the pane paneID on socket with
// send-keys -t <pane id> -l -- <text>, then, when pressEnter is set and the
// text call succeeded, sends a real Enter key in a second call,
// send-keys -t <pane id> Enter (SR-2.1). The "--" ends tmux's options, so a
// text starting with "-" (such as "-x", "--" or "-l") is typed literally and
// never read as a flag. The text is passed through escapeFinalSemicolon, so a
// text ending in ";" is typed whole and never ends the command. Action
// timeout for each. The error's Call says which call failed (CallSendText or
// CallSendEnter).
func (c *Client) SendKeysPane(socket, paneID, text string, pressEnter bool) error {
	if err := c.runAction(CallSendText, socket, "send-keys", "-t", paneID, "-l", "--", escapeFinalSemicolon(text)); err != nil {
		return err
	}
	if !pressEnter {
		return nil
	}
	return c.runAction(CallSendEnter, socket, "send-keys", "-t", paneID, "Enter")
}

// escapeFinalSemicolon returns text with one backslash inserted before its
// final ";" when it ends in ";" (`;` becomes `\;`, `a;` becomes `a\;`, `a\;`
// becomes `a\\;`), and any other text unchanged. tmux's command parser reads
// every argv element that ends in ";" as a command separator, even after
// "--": the ";" is dropped and the element's text before it, if any, is the
// command's last argument. An element ending in `\;` is instead one argument
// with that backslash removed, so the escaped text reaches the pane exactly
// as written. A ";" anywhere else in the text is not special. Used only by
// the text call of SendKeysPane.
func escapeFinalSemicolon(text string) string {
	if !strings.HasSuffix(text, cmdSeparator) {
		return text
	}
	return text[:len(text)-len(cmdSeparator)] + `\` + cmdSeparator
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
	out, err := c.runData(CallCapture, socket, args...)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// SetLabel labels the session sessionID on socket by its id: set-option -t
// <$N> @ad_owner 'ad1 <token> <$N> <instance id> <store id>', the value one
// argv element, with no -F and no doubling, so a # in the instance id stays
// as written (SR-2.1, SR-3.5, Appendix F.1; WD 2026-09-29 STORE). storeID is
// the writing store's store_meta.store_id, written last; the client does not
// validate it, callers passing (*store.Store).StoreID(), which the store's
// open has already validated (SR-5.1). A failure's *CallError is built from
// the reply alone, never from the value (SR-15). Action timeout. Used for a
// name containing $ or \ and for the one relabel after a failed chained
// label.
func (c *Client) SetLabel(socket, sessionID, token, instanceID, storeID string) error {
	value := labelPrefix + token + " " + sessionID + " " + instanceID + " " + storeID
	return c.runAction(CallSetLabel, socket, "set-option", "-t", sessionID, ownerOption, value)
}
