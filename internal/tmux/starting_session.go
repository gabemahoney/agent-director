package tmux

import "time"

// This file holds the session-age helper (SRD SR-3.9) and the pure
// starting-session rule (SR-4.2, SR-4.3): the one place the stopping window
// and the starting-session bound are compared. resume's pre-launch check uses
// it. Its later callers are resume's re-lookup after "duplicate session",
// reuse (the old row's lookup and the re-lookup after "duplicate session")
// and kill's finished-row opt-in, which applies steps 1 and 2 and replaces
// step 3 with its reported-in rule (SR-6.5, SR-6.7). pkg/api builds the refusal errors
// from the classification.
// Like agent_process.go it takes plain values: it reads no clock (the verb
// passes its injected current instant), no environment and no configuration
// (the verb passes the effective bound and window, SR-4.1), and makes no tmux
// call, write or log.

// Elapsed is a measured time since an instant, as of the verb's injected
// current instant. An instant in the future is reported as Future, never as a
// negative or clamped duration, so a caller cannot misread it.
type Elapsed struct {
	// Since is the current instant minus the instant; zero when Future.
	Since time.Duration
	// Future reports that the instant is after the current instant.
	Future bool
}

// Under reports whether e is strictly less than limit. A Future instant is
// under any limit, 0 included: it is decided outright (SR-3.9, SR-4.2).
func (e Elapsed) Under(limit time.Duration) bool {
	return e.Future || e.Since < limit
}

// elapsedSince measures now minus then; then after now is Future.
func elapsedSince(now, then time.Time) Elapsed {
	if then.After(now) {
		return Elapsed{Future: true}
	}
	return Elapsed{Since: now.Sub(then)}
}

// SessionAge returns the age of a session whose lookup line gave created as
// #{session_created}, whole seconds since the Unix epoch (Session.Created),
// as of now, the verb's injected current instant (SR-3.9). A creation time in
// the future is Future, younger than any bound.
//
// Age never decides ownership: which launch a session belongs to is decided
// by its label (SR-3.4). agent-director and the tmux server share the host
// clock, so only clock steps matter: a forward step can make a session old
// early, a backward step keeps it young longer. An unparseable creation field
// never reaches this helper: the lookup reads it as a malformed listing, Can't
// tell (the lookup/created-unparseable entry pinned in parse_lookup_test.go).
func SessionAge(now time.Time, created int64) Elapsed {
	return elapsedSince(now, time.Unix(created, 0))
}

// StartingSessionOutcome is the starting-session rule's outcome (SR-4.2).
// The zero value is not an outcome.
type StartingSessionOutcome int

// The three outcomes of SR-4.2, the first that applies deciding.
const (
	// StillStopping: step 1, the row ended less than the stopping window
	// ago (ErrTmuxUnresponsive, "appears to still be stopping").
	StillStopping StartingSessionOutcome = iota + 1
	// StillStarting: step 2, the session is younger than the
	// starting-session bound (ErrTmuxUnresponsive, "appears to still be
	// starting").
	StillStarting
	// PastBoth: neither step applied. resume and reuse answer the own-id
	// conflict (ErrTmuxSessionConflict, "this row's own id"); kill's opt-in
	// applies its reported-in rule instead (SR-6.7).
	PastBoth
)

// StartingSessionInput is what the starting-session rule judges: the row as
// the verb examined it, the lookup's answer, and the verb's injected instant
// and effective settings.
type StartingSessionInput struct {
	// EndedAt is the examined row's ended_at, the one the verb read before
	// its move to pending or its reset, never a re-read (both clear it); nil
	// when NULL or, in reuse's narrow read, unparseable text. nil skips the
	// stopping window.
	EndedAt *time.Time
	// RecordsPID reports that the examined row records a pid.
	RecordsPID bool
	// RecordsSessionID reports that the examined row records a session id.
	// A row recording neither a pid nor a session id skips the stopping
	// window: no agent ever reported in to it.
	RecordsSessionID bool
	// Session is the lookup's Ours session, whose Created gives its age;
	// nil means no session of the launch was found while the row's agent
	// process still runs (Gone with the process judged alive by the
	// start-time reader; a process that cannot be checked never reaches the
	// rule, LFR M6), which skips the age step.
	Session *Session
	// Now is the verb's injected current instant, read once after the
	// lookup returned.
	Now time.Time
	// Bound is the effective starting-session bound, used as given (no
	// fallback, no minimum check; safe minimum 60 s, SR-4.1).
	Bound time.Duration
	// Window is the effective stopping window, used as given (no fallback,
	// no minimum check; safe minimum 30 s, SR-4.1).
	Window time.Duration
}

// StartingSessionClass is the starting-session rule's classification, with
// the facts the refusal descriptions need (SR-1.4).
type StartingSessionClass struct {
	// Outcome is the rule's outcome.
	Outcome StartingSessionOutcome
	// WindowChecked reports that the stopping window was checked: false when
	// the row records no ended_at, or neither a pid nor a session id.
	WindowChecked bool
	// SessionPresent reports that the lookup found the Ours session (false:
	// Gone while the agent process runs).
	SessionPresent bool
	// Window and Bound are the effective stopping window and starting-session
	// bound the rule used.
	Window time.Duration
	Bound  time.Duration
	// SinceEnd is the time since the examined ended_at; zero unless
	// WindowChecked.
	SinceEnd Elapsed
	// Age is the session's age (SessionAge); zero unless the age step ran.
	Age Elapsed
}

// StartingSession applies the starting-session rule (SR-4.2) to a row the
// verb examined as finished whose lookup found Ours, or Gone while the row's
// agent process still runs. The first that applies decides:
//
//  1. StillStopping: the row ended less than in.Window ago (an ended_at in
//     the future counts as inside), whatever the session's age. Skipped when
//     the row records no ended_at, or neither a pid nor a session id.
//  2. StillStarting: the session is younger than in.Bound (a creation time
//     in the future counts as younger). Skipped when there is no session.
//  3. PastBoth: otherwise.
//
// Both comparisons are strictly less than, so at the defaults 89 s is inside
// the window and 90 s is not, and a 299 s session is young and a 300 s one is
// not. The window and the bound are independent. It reads no clock, touches
// nothing and logs nothing; the caller acts on the outcome.
func StartingSession(in StartingSessionInput) StartingSessionClass {
	c := StartingSessionClass{
		SessionPresent: in.Session != nil,
		Window:         in.Window,
		Bound:          in.Bound,
	}
	if in.EndedAt != nil && (in.RecordsPID || in.RecordsSessionID) {
		c.WindowChecked = true
		c.SinceEnd = elapsedSince(in.Now, *in.EndedAt)
		if c.SinceEnd.Under(in.Window) {
			c.Outcome = StillStopping
			return c
		}
	}
	if in.Session != nil {
		c.Age = SessionAge(in.Now, in.Session.Created)
		if c.Age.Under(in.Bound) {
			c.Outcome = StillStarting
			return c
		}
	}
	c.Outcome = PastBoth
	return c
}
