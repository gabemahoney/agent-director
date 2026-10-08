package tmux

// PaneMatch is the outcome of PaneByToken. The zero value, PaneNone, means no
// pane carries the token.
type PaneMatch int

// The three outcomes of PaneByToken.
const (
	// PaneNone: no pane's @ad_pane names the token.
	PaneNone PaneMatch = iota
	// PaneOne: exactly one distinct pane id carries the token.
	PaneOne
	// PaneMany: more than one distinct pane id carries the token.
	PaneMany
)

// PaneByToken picks, from one pane listing, the pane whose @ad_pane names the
// launch token token (SRD SR-3.6, SR-3.7, SR-11.3; WD 2026-09-29c). A pane
// carries the token when its Pane.AdPane equals it; the client has already
// applied the scope guard (a value counts only when it embeds its own line's
// pane id), and an empty token matches no pane.
//
// Panes are counted by distinct pane id: list-panes -a lists a shared pane (a
// grouped session, a linked window) once per session showing its window
// (SR-3.7), and those entries are one pane. The outcomes:
//
//   - PaneOne: exactly one distinct pane id carries the token; the Pane is
//     its first entry in listing order (its ID and PID are the pane's; its
//     SessionID, Window and Index are that entry's);
//   - PaneNone: none carries it; the Pane is zero;
//   - PaneMany: more than one distinct pane id carries it; the Pane is zero.
//
// It serves adoption (SR-3.6, the row's token), a leftover's pane (SR-3.7,
// the leftover label's token, as read-pane and kill's finished-row opt-in on
// this id's own abandoned launch use it; b.6sa) and the SR-11.3 decision for
// a row that records no pane. What each outcome means is the caller's.
// Adoption (SR-3.6) and a leftover's pane (SR-3.7) take a pane only on
// PaneOne; for adoption, none
// and more than one alike adopt no pane (the pane verbs refuse with "the
// agent's pane was not found"). In the SR-11.3 check of a row that records no
// pane, PaneNone counts as Gone and PaneMany is unverified, never Gone. It
// makes no tmux call.
func PaneByToken(panes []Pane, token string) (Pane, PaneMatch) {
	if token == "" {
		return Pane{}, PaneNone
	}
	var found Pane
	match := PaneNone
	for _, p := range panes {
		if p.AdPane != token {
			continue
		}
		switch {
		case match == PaneNone:
			found, match = p, PaneOne
		case p.ID != found.ID:
			return Pane{}, PaneMany
		}
	}
	return found, match
}
