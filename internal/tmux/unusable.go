package tmux

import (
	"strings"
	"unicode/utf8"
)

// UnusableKind reports why a recorded session name must not be passed to tmux
// (SRD SR-3.2, Appendix F.2). The zero value, UnusableNone, means usable.
type UnusableKind int

// The unusable-name kinds of SR-3.2, in their precedence order.
const (
	// UnusableNone: the name may be passed to tmux.
	UnusableNone UnusableKind = iota
	// UnusableEmpty: the empty name, which tmux reads as "the most recent
	// session".
	UnusableEmpty
	// UnusableControl: the name holds an ASCII control character (a byte
	// 0x00-0x1f or 0x7f), which tmux stores escaped (Appendix E N4).
	UnusableControl
	// UnusableRewritten: the name holds `.` or `:`, which tmux stores as `_`,
	// or bytes that are not valid UTF-8, which tmux stores as backslash-octal
	// escapes (Appendix E N6).
	UnusableRewritten
)

// Unusable classifies a recorded session name (SR-3.2, Appendix F.2). When a
// name has more than one fault the first in the order empty, control
// character, rewritten decides:
//
//   - "" is UnusableEmpty;
//   - a name holding any byte 0x00-0x1f or 0x7f is UnusableControl. The raw
//     bytes are judged one by one, so a name that is also invalid UTF-8 but
//     carries a control byte is a control-character fault; C1 code points
//     and other Unicode characters are not control characters here;
//   - a name holding `.` or `:`, or bytes that are not valid UTF-8, is
//     UnusableRewritten;
//   - anything else is UnusableNone.
//
// The guard only classifies: callers decide what a kind means (Epic 19
// applies it to the verbs). It reads no environment and no clock and makes no
// tmux call. It is separate from internal/spawn's validation of a new
// explicit name, which also refuses `#`, `$`, `\` and long names; the guard
// allows `$`, `\` and `#` in a recorded name.
func Unusable(name string) UnusableKind {
	switch {
	case name == "":
		return UnusableEmpty
	case hasControlChar(name):
		return UnusableControl
	case RewrittenIn(name).Any():
		return UnusableRewritten
	}
	return UnusableNone
}

// Rewritten reports which characters of a name tmux stores differently
// (SR-3.2; Appendix E N6), the faults behind UnusableRewritten. The zero
// value means none.
type Rewritten struct {
	// Dot: the name holds `.`, which tmux stores as `_`.
	Dot bool
	// Colon: the name holds `:`, which tmux stores as `_`.
	Colon bool
	// InvalidUTF8: the name holds bytes that are not valid UTF-8, which tmux
	// stores as backslash-octal escapes.
	InvalidUTF8 bool
}

// Any reports whether r records at least one fault.
func (r Rewritten) Any() bool { return r.Dot || r.Colon || r.InvalidUTF8 }

// RewrittenIn classifies which rewritten characters name holds (SR-3.2,
// SR-1.4): the one place these rules live, used by Unusable and by
// descriptions that name which character tmux stores differently. It judges
// only the rewritten faults, so callers take Unusable's precedence first (an
// empty name or a control character decides before these). It reads no
// environment and no clock and makes no tmux call.
func RewrittenIn(name string) Rewritten {
	return Rewritten{
		Dot:         strings.Contains(name, "."),
		Colon:       strings.Contains(name, ":"),
		InvalidUTF8: !utf8.ValidString(name),
	}
}
