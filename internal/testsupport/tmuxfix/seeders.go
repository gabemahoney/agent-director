// Package tmuxfix — verb-specific Recorder constructors.
//
// These helpers return a *Recorder pre-configured for a specific verb's tmux
// interaction (SRD SR-20.3), so verb tests and smoke-test seeders are
// self-documenting without repeating the session, pane-text and
// process-checker set-up inline.
package tmuxfix

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/procstarttimefix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// RowLabel names the label NewRecorderForReadPane gives the row's session
// (SR-20.3). Every label but RowLabelOtherStore carries the seeded store's id.
type RowLabel int

const (
	// RowLabelCurrent is the row's current label (its id and launch token):
	// the lookup's Ours.
	RowLabelCurrent RowLabel = iota
	// RowLabelOld is the row's id with another launch token (OtherToken):
	// the lookup's Leftover. The row's pane carries OtherToken as @ad_pane.
	RowLabelOld
	// RowLabelForeign is the row's launch token with another instance id.
	RowLabelForeign
	// RowLabelOtherStore is the row's current label ending in another
	// agent-director store's id: the lookup's Gone (WD 2026-09-29 STORE).
	RowLabelOtherStore
	// RowLabelNone is no label (@ad_owner unset).
	RowLabelNone
	// RowLabelMalformed is an own @ad_owner value that does not parse.
	RowLabelMalformed
	// RowLabelBorrowed is an own @ad_owner value copied from another session
	// (it embeds that session's id). The lookup classifies it as it does a
	// malformed value, so the Recorder seeds both alike: a LabelNone label
	// that is set.
	RowLabelBorrowed
)

// ReadPaneOption adjusts what NewRecorderForReadPane seeds.
type ReadPaneOption func(*readPaneSeed)

// readPaneSeed holds NewRecorderForReadPane's option values.
type readPaneSeed struct {
	clock     *Clock
	timeouts  tmux.Timeouts
	session   []RowSessionOption
	label     RowLabel
	leftovers []readPaneLeftover
}

// readPaneLeftover is one WithReadPaneLeftover session.
type readPaneLeftover struct{ name, text string }

// WithReadPaneVirtualTime binds the Recorder to the shared clock c with
// timeouts t (WithVirtualTime) before anything is seeded, so the sessions
// are created at c's current second unless WithReadPaneCreated gives a time.
func WithReadPaneVirtualTime(c *Clock, t tmux.Timeouts) ReadPaneOption {
	return func(o *readPaneSeed) { o.clock, o.timeouts = c, t }
}

// WithReadPaneCreated gives the row's session's creation time, epoch seconds.
func WithReadPaneCreated(epoch int64) ReadPaneOption {
	return func(o *readPaneSeed) { o.session = append(o.session, WithRowSessionCreated(epoch)) }
}

// WithReadPaneName gives the row's session another stored name.
func WithReadPaneName(stored string) ReadPaneOption {
	return func(o *readPaneSeed) { o.session = append(o.session, WithRowSessionName(stored)) }
}

// WithReadPaneLabel gives the row's session another label than its current
// one.
func WithReadPaneLabel(l RowLabel) ReadPaneOption {
	return func(o *readPaneSeed) { o.label = l }
}

// WithReadPaneLeftover seeds, besides the row's session, a leftover of the
// row: a session named name on the row's socket with the row's old label
// (RowLabelOld's) and one new pane carrying OtherToken as @ad_pane, whose
// capture text is text. Give it more than once for more than one leftover.
func WithReadPaneLeftover(name, text string) ReadPaneOption {
	return func(o *readPaneSeed) { o.leftovers = append(o.leftovers, readPaneLeftover{name, text}) }
}

// NewRecorderForReadPane returns a Recorder and a process-checker fake for
// read-pane on the row instanceID of the store at dbPath (SR-20.3). The
// Recorder holds the row's session, seeded by SeedRowSession with the row's
// current label unless WithReadPaneLabel gives another, and the row's pane
// (a new one when the row records none) captures paneText by its pane id.
// Options also give a clock, a creation time, another name and leftovers.
//
// The fake answers every server the Recorder bound as alive with its process
// start time, and the row's agent process and every seeded pane process as
// alive with the start time the row records for it, else
// procstarttimefix.LinuxProcStarttime. The test gives it to the code under
// test (in pkg/api's tests, api.SetProcCheckerForTest) and may change it
// afterwards.
func NewRecorderForReadPane(t testing.TB, dbPath, instanceID, paneText string, opts ...ReadPaneOption) (*Recorder, *procfix.Checker) {
	t.Helper()
	var o readPaneSeed
	for _, opt := range opts {
		opt(&o)
	}
	row, storeID := readRow(t, dbPath, instanceID)
	id := row.Identity
	r := NewRecorder()
	if o.clock != nil {
		r.WithVirtualTime(o.clock, o.timeouts)
	}
	sessionOpts := o.session
	if o.label != RowLabelCurrent {
		label, set := rowLabel(o.label, id.Token, instanceID, storeID)
		sessionOpts = append(sessionOpts, WithRowSessionLabel(label, set))
	}
	own := r.SeedRowSession(t, dbPath, instanceID, sessionOpts...)
	r.SetCapture(id.Socket, own.Panes[0].ID, paneText)

	pc := procfix.New()
	if row.PID > 0 {
		pc.Set(row.PID, procfix.Alive(startOr(row.ProcStarttime)))
	}
	pc.Set(own.Panes[0].PID, procfix.Alive(startOr(id.PaneStarttime)))
	old := Valid(OtherToken, instanceID, storeID)
	for _, lo := range o.leftovers {
		s := r.seedLeftover(id.Socket, SeedSession{Name: lo.name, Label: old,
			Panes: []SeedPane{{AdPane: OtherToken}}})
		r.SetCapture(id.Socket, s.Panes[0].ID, lo.text)
		pc.Set(s.Panes[0].PID, procfix.Alive(procstarttimefix.LinuxProcStarttime))
	}
	for _, s := range r.Servers() {
		if s.Running {
			pc.Set(s.PID, procfix.Alive(startOr(s.ProcStart)))
		}
	}
	return r, pc
}

// rowLabel is the label l for the row instanceID with launch token token in
// the store storeID, and whether it is set (SeedSession.LabelSet).
func rowLabel(l RowLabel, token, instanceID, storeID string) (tmux.Label, bool) {
	switch l {
	case RowLabelOld:
		return Valid(OtherToken, instanceID, storeID), true
	case RowLabelForeign:
		return Valid(token, instanceID+"-foreign", storeID), true
	case RowLabelOtherStore:
		return Valid(token, instanceID, anotherStoreID(storeID)), true
	case RowLabelNone:
		return tmux.Label{}, false
	case RowLabelMalformed, RowLabelBorrowed:
		return tmux.Label{}, true
	}
	return Valid(token, instanceID, storeID), true
}

// anotherStoreID is a well-formed store id other than storeID: the
// catalogue's OtherStoreID, or StoreID when storeID is OtherStoreID.
func anotherStoreID(storeID string) string {
	if storeID == OtherStoreID {
		return StoreID
	}
	return OtherStoreID
}

// startOr is start, or procstarttimefix.LinuxProcStarttime when it is empty.
func startOr(start string) string {
	if start == "" {
		return procstarttimefix.LinuxProcStarttime
	}
	return start
}

// seedLeftover adds s to socket's server (one is bound: the row's session is
// seeded first) and returns it as stored.
func (r *Recorder) seedLeftover(socket string, s SeedSession) SeedSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.addSession(r.serverFor(socket), s)
}

// NewRecorderForResume returns a Recorder whose HasSession always returns
// false — the precondition resume requires so its session-existence check
// does not report ErrTmuxSessionCreate before the new session is launched.
// (HasSession defaults to false already; this constructor documents the
// requirement explicitly so seeders are self-explanatory.)
func NewRecorderForResume() *Recorder {
	return NewRecorder().WithHasSession(false)
}

// NewRecorderForPause returns a Recorder suitable for the pause verb. Pause
// sends one /exit SendKeys call; the recorder's default no-op return is
// sufficient. The note here documents that pause's poll-loop reads the store
// (not the tmux client), so no scripted tmux response is needed to make
// pause terminate — the caller's context deadline controls exit.
func NewRecorderForPause() *Recorder {
	// No special scripted responses needed: pause polls GetSpawnState on the
	// store to detect ended, and the caller's context deadline prevents a hang.
	return NewRecorder()
}
