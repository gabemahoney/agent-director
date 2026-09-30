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
	case strings.ContainsAny(name, ".:") || !utf8.ValidString(name):
		return UnusableRewritten
	}
	return UnusableNone
}
