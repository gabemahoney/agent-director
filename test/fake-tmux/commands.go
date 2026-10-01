package main

import (
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
)

// The two options the fake stores: the session label (SR-3.4) and the pane
// label (WD 2026-09-29c).
const (
	ownerOption = "@ad_owner"
	paneOption  = "@ad_pane"
)

// createReplyDefault is tmux's -P format when no -F is given.
const createReplyDefault = "#{session_name}:"

// server runs one invocation's commands against a socket's table.
type server struct {
	tb      *faketmuxfix.Table
	socket  string
	out     output
	changed bool
	// failChain makes a set-option after a new-session in the same
	// invocation fail (ActChainFails).
	failChain bool
	created   bool
}

// run runs cmds in order; the first failing command ends the invocation
// with its status. It returns errBadArgv for argv outside the call set.
func (s *server) run(cmds [][]string) (output, error) {
	for _, c := range cmds {
		code, err := s.exec(c[0], c[1:])
		if err != nil {
			return output{}, err
		}
		if code != 0 {
			s.out.exit = code
			break
		}
	}
	return s.out, nil
}

// exec runs one command and returns its exit status.
func (s *server) exec(name string, args []string) (int, error) {
	switch name {
	case "list-sessions":
		return s.listSessions(args)
	case "show-options":
		return s.showOptions(args)
	case "list-panes":
		return s.listPanes(args)
	case "kill-pane":
		return s.killPane(args)
	case "kill-session":
		return s.killSession(args)
	case "send-keys":
		return s.sendKeys(args)
	case "capture-pane":
		return s.capturePane(args)
	case "new-session":
		return s.newSession(args)
	case "set-option":
		return s.setOption(args)
	}
	return 0, errBadArgv
}

// reply writes a catalogue entry's bytes and returns its exit status.
func (s *server) reply(e tmuxfix.Entry) int {
	s.out.stdout += e.Stdout
	s.out.stderr += e.Stderr
	return e.Exit
}

// catalogued returns the socket's catalogue reply named name.
func (s *server) catalogued(name string) tmuxfix.Entry {
	return tmuxfix.Find(tmuxfix.Replies(s.socket), name)
}

// chainFailure is the recorded failure of a chained set-option whose target
// names no session (rn4-tmux-check F1, G7): its standard error line only,
// the create's own reply having been printed already.
func chainFailure() tmuxfix.Entry {
	e := tmuxfix.Find(tmuxfix.CreateReplies(), "create/reply-then-label-failed")
	return tmuxfix.Entry{Name: e.Name, Stderr: e.Stderr, Exit: e.Exit}
}

// sorted returns the sessions sorted by name, as tmux lists them.
func (s *server) sorted() []*faketmuxfix.Session {
	out := make([]*faketmuxfix.Session, len(s.tb.Sessions))
	for i := range s.tb.Sessions {
		out[i] = &s.tb.Sessions[i]
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *server) listSessions(args []string) (int, error) {
	o, rest, ok := getopt(args, "F", "")
	if !ok || len(rest) != 0 || len(o['F']) != 1 {
		return 0, errBadArgv
	}
	for _, sess := range s.sorted() {
		s.out.stdout += expand(o['F'][0], sessionVars(s.tb, sess)) + "\n"
	}
	return 0, nil
}

func (s *server) showOptions(args []string) (int, error) {
	o, rest, ok := getopt(args, "", "gqsvw")
	if !ok || len(rest) != 1 || rest[0] != ownerOption || !o.has('v') {
		return 0, errBadArgv
	}
	var value string
	switch {
	case o.has('s') && !o.has('g') && !o.has('w'):
		value = s.tb.Scope.Server
	case o.has('g') && o.has('w') && !o.has('s'):
		value = s.tb.Scope.GlobalWindow
	case o.has('g') && !o.has('w') && !o.has('s'):
		value = s.tb.Scope.Global
	default:
		return 0, errBadArgv
	}
	if value != "" {
		s.out.stdout += value + "\n"
	}
	return 0, nil
}

func (s *server) listPanes(args []string) (int, error) {
	o, rest, ok := getopt(args, "F", "a")
	if !ok || len(rest) != 0 || !o.has('a') || len(o['F']) != 1 {
		return 0, errBadArgv
	}
	for _, sess := range s.sorted() {
		panes := append([]faketmuxfix.Pane(nil), sess.Panes...)
		sort.SliceStable(panes, func(i, j int) bool {
			if panes[i].Window != panes[j].Window {
				return panes[i].Window < panes[j].Window
			}
			return panes[i].Index < panes[j].Index
		})
		for _, p := range panes {
			v := sessionVars(s.tb, sess)
			for k, val := range paneVars(p) {
				v[k] = val
			}
			s.out.stdout += expand(o['F'][0], v) + "\n"
		}
	}
	return 0, nil
}

// target returns a command's single -t value.
func target(args []string, withValue, boolean string) (opts, []string, string, error) {
	o, rest, ok := getopt(args, withValue+"t", boolean)
	if !ok || len(o['t']) != 1 {
		return nil, nil, "", errBadArgv
	}
	return o, rest, o['t'][0], nil
}

// findPane returns the session index and pane index of pane id.
func (s *server) findPane(id string) (int, int, bool) {
	for i, sess := range s.tb.Sessions {
		for j, p := range sess.Panes {
			if p.ID == id {
				return i, j, true
			}
		}
	}
	return 0, 0, false
}

// findSession returns the index of the session with id.
func (s *server) findSession(id string) (int, bool) {
	for i, sess := range s.tb.Sessions {
		if sess.ID == id {
			return i, true
		}
	}
	return 0, false
}

func (s *server) killPane(args []string) (int, error) {
	_, rest, t, err := target(args, "", "")
	if err != nil || len(rest) != 0 {
		return 0, errBadArgv
	}
	i, j, ok := s.findPane(t)
	if !ok {
		return s.reply(s.catalogued("reply/cant-find-pane")), nil
	}
	sess := &s.tb.Sessions[i]
	sess.Panes = append(sess.Panes[:j], sess.Panes[j+1:]...)
	if len(sess.Panes) == 0 {
		s.tb.Sessions = append(s.tb.Sessions[:i], s.tb.Sessions[i+1:]...)
	}
	s.changed = true
	return 0, nil
}

func (s *server) killSession(args []string) (int, error) {
	_, rest, t, err := target(args, "", "")
	if err != nil || len(rest) != 0 {
		return 0, errBadArgv
	}
	i, ok := s.findSession(t)
	if !ok {
		return s.reply(s.catalogued("reply/cant-find-session")), nil
	}
	s.tb.Sessions = append(s.tb.Sessions[:i], s.tb.Sessions[i+1:]...)
	s.changed = true
	return 0, nil
}

func (s *server) sendKeys(args []string) (int, error) {
	o, rest, t, err := target(args, "", "l")
	if err != nil || len(rest) == 0 || (o.has('l') && len(rest) != 1) {
		return 0, errBadArgv
	}
	if _, _, ok := s.findPane(t); !ok {
		return s.reply(s.catalogued("reply/cant-find-pane")), nil
	}
	return 0, nil
}

func (s *server) capturePane(args []string) (int, error) {
	o, rest, t, err := target(args, "SE", "peJ")
	if err != nil || len(rest) != 0 || !o.has('p') {
		return 0, errBadArgv
	}
	i, j, ok := s.findPane(t)
	if !ok {
		return s.reply(s.catalogued("reply/cant-find-pane")), nil
	}
	if text := s.tb.Sessions[i].Panes[j].Capture; text != "" {
		s.out.stdout += text
	} else {
		s.out.stdout += capturePaneOutput()
	}
	return 0, nil
}

func (s *server) newSession(args []string) (int, error) {
	o, _, ok := getopt(args, "sceF", "dP")
	if !ok || len(o['s']) > 1 || len(o['c']) > 1 || len(o['F']) > 1 {
		return 0, errBadArgv
	}
	sessNum, paneNum := s.nextIDs()
	name := strconv.Itoa(sessNum)
	if len(o['s']) == 1 {
		name = o['s'][0]
	}
	if failNewSession(name) {
		return s.reply(duplicateReply(name)), nil
	}
	stored := faketmuxfix.StoredForm(name)
	for _, sess := range s.tb.Sessions {
		if sess.Name == stored {
			return s.reply(tmuxfix.Duplicate(stored)), nil
		}
	}
	now := time.Now().Unix()
	if s.tb.Server == nil {
		s.tb.Server = &faketmuxfix.Server{PID: os.Getppid(), Start: now}
	}
	panePID := s.tb.NewPanePID
	if panePID == 0 {
		panePID = os.Getppid()
	}
	sess := faketmuxfix.Session{
		ID: "$" + strconv.Itoa(sessNum), Created: now, Name: stored,
		Panes: []faketmuxfix.Pane{{ID: "%" + strconv.Itoa(paneNum), PID: panePID}},
	}
	s.tb.Sessions = append(s.tb.Sessions, sess)
	s.tb.NextSession, s.tb.NextPane = sessNum+1, paneNum+1
	s.changed, s.created = true, true
	if o.has('P') {
		format := createReplyDefault
		if len(o['F']) == 1 {
			format = o['F'][0]
		}
		s.out.stdout += expand(format, sessionVars(s.tb, &s.tb.Sessions[len(s.tb.Sessions)-1])) + "\n"
	}
	return 0, nil
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

// nextIDs returns the numbers of the next $N and %N: the table's counters,
// raised past every id already in the table.
func (s *server) nextIDs() (int, int) {
	sessNum, paneNum := s.tb.NextSession, s.tb.NextPane
	for _, sess := range s.tb.Sessions {
		if n, err := strconv.Atoi(strings.TrimPrefix(sess.ID, "$")); err == nil && n >= sessNum {
			sessNum = n + 1
		}
		for _, p := range sess.Panes {
			if n, err := strconv.Atoi(strings.TrimPrefix(p.ID, "%")); err == nil && n >= paneNum {
				paneNum = n + 1
			}
		}
	}
	return sessNum, paneNum
}

// setOption sets the session label (set-option [-F] -t <target> @ad_owner
// <value>) or, with -p, the pane label (set-option -p [-F] -t <target>
// @ad_pane <value>) of the target's pane; -F expands the value for the
// target session and pane. A chain-form target (=<name>: or none) that
// matches nothing fails with the recorded chain failure; any other target
// with the catalogue's no-such-session or no-such-pane reply.
func (s *server) setOption(args []string) (int, error) {
	o, rest, ok := getopt(args, "t", "Fp")
	option := ownerOption
	if o.has('p') {
		option = paneOption
	}
	if !ok || len(o['t']) > 1 || len(rest) != 2 || rest[0] != option {
		return 0, errBadArgv
	}
	t := ""
	if len(o['t']) == 1 {
		t = o['t'][0]
	}
	chainForm := t == "" || strings.HasPrefix(t, "=")
	fail := s.catalogued("reply/no-such-session")
	if option == paneOption {
		fail = s.catalogued("reply/no-such-pane")
	}
	if chainForm {
		fail = chainFailure()
	}
	if s.failChain && s.created {
		return s.reply(chainFailure()), nil
	}
	i, j, found := s.optionTarget(t, option == paneOption)
	if !found {
		return s.reply(fail), nil
	}
	sess := &s.tb.Sessions[i]
	value := rest[1]
	if option == ownerOption {
		if o.has('F') {
			value = expand(value, sessionVars(s.tb, sess))
		}
		sess.Label = value
		s.changed = true
		return 0, nil
	}
	if o.has('F') {
		v := sessionVars(s.tb, sess)
		for k, val := range paneVars(sess.Panes[j]) {
			v[k] = val
		}
		value = expand(value, v)
	}
	sess.Panes[j].AdPane = value
	s.changed = true
	return 0, nil
}

// optionTarget resolves a set-option target to a session index and, for a
// pane option, a pane index. For the session label: a session id ($N) or
// =<name>: matching a stored name exactly. For the pane label: a pane id
// (%N), or =<name>: as above, giving that session's first pane (the fake's
// active pane). Anything else is not found.
func (s *server) optionTarget(t string, pane bool) (sess, paneIdx int, found bool) {
	switch {
	case pane && strings.HasPrefix(t, "%"):
		return s.findPane(t)
	case !pane && strings.HasPrefix(t, "$"):
		sess, found = s.findSession(t)
	case len(t) > 2 && strings.HasPrefix(t, "=") && strings.HasSuffix(t, ":"):
		name := t[1 : len(t)-1]
		for k, have := range s.tb.Sessions {
			if have.Name == name {
				sess, found = k, true
				break
			}
		}
	}
	if found && pane && len(s.tb.Sessions[sess].Panes) == 0 {
		found = false
	}
	return sess, 0, found
}

// opts holds parsed flags: each flag's values in order ("" for a boolean).
type opts map[byte][]string

// has reports whether flag c was given.
func (o opts) has(c byte) bool { return len(o[c]) > 0 }

// getopt parses tmux-style flags: withValue lists flags taking a value
// (attached or the next argument), boolean the flags that take none, which
// may be clustered. Parsing stops at "--" (dropped) or the first argument
// not starting with '-'. ok is false for an unknown flag or a missing value.
func getopt(args []string, withValue, boolean string) (opts, []string, bool) {
	o := opts{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return o, args[i+1:], true
		}
		if len(a) < 2 || a[0] != '-' {
			return o, args[i:], true
		}
		for j := 1; j < len(a); j++ {
			c := a[j]
			switch {
			case strings.IndexByte(withValue, c) >= 0:
				v := a[j+1:]
				if v == "" {
					if i+1 == len(args) {
						return nil, nil, false
					}
					i++
					v = args[i]
				}
				o[c] = append(o[c], v)
				j = len(a)
			case strings.IndexByte(boolean, c) >= 0:
				o[c] = append(o[c], "")
			default:
				return nil, nil, false
			}
		}
	}
	return o, nil, true
}
