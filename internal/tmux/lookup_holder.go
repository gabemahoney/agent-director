package tmux

import (
	"slices"
	"strings"
)

// This file holds the name holder of SR-3.10: the stored forms a name is
// compared against, and the match over a lookup answer's sessions. Only tmux
// 3.2a/3.3a escaping is implemented; the 3.4 and 3.5a forms are
// informational only (Appendix E X1).

// StoredForms returns the forms a name is compared against as a name holder
// (SR-3.10, Appendix F.2): the raw name alone for a name with neither $ nor
// \, and otherwise the raw name and tmux 3.2a/3.3a's escaped form, which
// stores every \ as \\ and every $ immediately followed by an ASCII letter,
// _ or { as \$, everything else unchanged (Appendix E N1, N2, N5). For
// example a$b gives [a$b a\$b], and a$1, whose escaped form is itself, gives
// [a$1]. The result never holds the same form twice.
func StoredForms(name string) []string {
	if !strings.ContainsAny(name, `$\`) {
		return []string{name}
	}
	escaped := escapeStoredName(name)
	if escaped == name {
		return []string{name}
	}
	return []string{name, escaped}
}

// escapeStoredName is tmux 3.2a/3.3a's stored form of a new session name
// (SR-3.10): \ becomes \\, and $ becomes \$ when the next byte is an ASCII
// letter, _ or {. It works on bytes, so bytes that are not valid UTF-8 pass
// through unchanged.
func escapeStoredName(name string) string {
	var b strings.Builder
	b.Grow(len(name) + 4)
	for i := 0; i < len(name); i++ {
		switch c := name[i]; {
		case c == '\\':
			b.WriteString(`\\`)
		case c == '$' && i+1 < len(name) && escapesDollar(name[i+1]):
			b.WriteString(`\$`)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// escapesDollar reports whether a $ followed by c is stored escaped: c is an
// ASCII letter, _ or { (SR-3.10).
func escapesDollar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c == '{'
}

// matchHolder finds the holder of name among sessions (SR-3.10): the session
// whose stored Name bytes equal one of StoredForms(name) exactly, with no
// normalisation and no unescaping. It returns a copy of the one matching
// session, or nil with ambiguous false when none matches (the name is not
// held), or nil with ambiguous true when more than one matches (Can't tell
// for the holder check). An empty name means no holder check: nil, false.
func matchHolder(sessions []Session, name string) (holder *Session, ambiguous bool) {
	if name == "" {
		return nil, false
	}
	forms := StoredForms(name)
	for i := range sessions {
		if !slices.Contains(forms, sessions[i].Name) {
			continue
		}
		if holder != nil {
			return nil, true
		}
		s := sessions[i]
		holder = &s
	}
	return holder, false
}
