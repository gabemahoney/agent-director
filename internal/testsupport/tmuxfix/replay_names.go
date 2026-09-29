package tmuxfix

import "github.com/gabemahoney/agent-director/internal/tmux"

// Stored name forms and label shapes of the replay catalogue (SR-20.4),
// recorded on tmux 3.2a; identical on 3.3a.

// StoredName is a session name as given to new-session and the bytes tmux
// stores and lists for it (SR-3.2, SR-3.10).
type StoredName struct {
	// Source cites the Appendix E section.
	Source string
	// Raw is the name passed to -s; Stored is what the listing shows.
	Raw, Stored string
	// LabelByID reports a raw name containing $ or \, which is never
	// chained and is labelled by id (SR-3.5).
	LabelByID bool
}

// Entry is a one-session lookup answer listing the stored name with a
// valid label, for asserting that Session.Name is the stored bytes.
func (n StoredName) Entry() Entry {
	return Answer("lookup/name:"+n.Stored, n.Source, serverPID, f9ServerStart, []Listed{{
		ID: "$0", Created: f9ServerStart, Name: n.Stored,
		Label: LabelValue(Token, "$0", "agent-x"), Want: Valid(Token, "agent-x"),
	}})
}

// StoredNames returns every recorded name form: the $ and \ cases of N1 and
// N5, F3's $7 and x$, T2a's %9 (a name spelled as a pane id), N4's control
// characters, N6's '.', ':' and invalid UTF-8, and U1's UTF-8 name as a -u
// listing shows it.
func StoredNames() []StoredName {
	n := func(source, raw, stored string, byID bool) StoredName {
		return StoredName{Source: source, Raw: raw, Stored: stored, LabelByID: byID}
	}
	return []StoredName{
		n("E.3 N1, N2", `a$b`, `a\$b`, true),
		n("E.3 N1", `a$_b`, `a\$_b`, true),
		n("E.3 N1", `a${b}`, `a\${b}`, true),
		n("E.3 N1, N2", `a$1`, `a$1`, true),
		n("E.3 N1", `a$`, `a$`, true),
		n("E.3 N1, N2", `a$$b`, `a$\$b`, true),
		n("E.3 N1", `a$$`, `a$$`, true),
		n("E.3 N1", `a$-b`, `a$-b`, true),
		n("E.3 N1, N2", `a\b`, `a\\b`, true),
		n("E.3 N1", `$lead`, `\$lead`, true),
		n("E.6 N5", `a$B`, `a\$B`, true),
		n("E.6 N5", `a$Z9`, `a\$Z9`, true),
		n("E.6 N5", `a$$$b`, `a$$\$b`, true),
		n("E.6 N5", `a\$b`, `a\\\$b`, true),
		n("E.6 N5", `a$\b`, `a$\\b`, true),
		n("E.6 N5", `$`, `$`, true),
		n("E.6 N5", `$$`, `$$`, true),
		n("E.6 N5", `x$}`, `x$}`, true),
		n("E.6 N5", `x$(y)`, `x$(y)`, true),
		n("provenance-fresh F3", `$7`, `$7`, true),
		n("provenance-fresh F3", `x$`, `x$`, true),
		n("E.9 T2a", `%9`, `%9`, false),
		n("E.6 N4", "tab\tx", `tab\tx`, false),
		n("E.6 N4", "nl\nx", `nl\nx`, false),
		n("E.6 N4", "esc\033x", `esc\033x`, false),
		n("E.10 N6", "dot.x", "dot_x", false),
		n("E.10 N6", "col:x", "col_x", false),
		n("E.10 N6", "d.o:t", "d_o_t", false),
		n("E.10 N6", "mix.$b", `mix_\$b`, true),
		n("E.10 N6", "bad\xffx", `bad\377x`, false),
		n("E.10 N6", "cut\xc3x", `cut\303x`, false),
		n("E.3 U1, U2, U3 (with -u)", "ü-x", "ü-x", false),
	}
}

// LocaleForm is a name or instance id whose bytes a client without -u
// shows with '_' under LC_ALL=C or with no locale (SR-2.2).
type LocaleForm struct {
	// Source cites the Appendix E section.
	Source string
	// Exact is what a -u client lists; WithoutU what a client without -u lists.
	Exact, WithoutU string
}

// LocaleForms returns U2/U3's name and instance id and U4's name. Appendix
// E recorded the id as bot-ü1; its requirements name it agent-ü1.
func LocaleForms() []LocaleForm {
	return []LocaleForm{
		{Source: "E.3 U2, U3", Exact: "ü-x", WithoutU: "_-x"},
		{Source: "E.3 U2, U3 (instance id)", Exact: "agent-ü1", WithoutU: "agent-_1"},
		{Source: "E.3 U4 (created under LC_ALL=C without -u)", Exact: "ö-y"},
	}
}

// LabelShape is one @ad_owner value on the line of session LabelLineID and
// the typed result the lookup must give it (SR-3.4, AC-LKP-05).
type LabelShape struct {
	// Name identifies the shape.
	Name string
	// Source cites the requirement or transcript.
	Source string
	// Value is the raw value after the line's fifth tab; a "\n" in it puts
	// the rest on an extra line of the listing.
	Value string
	// Want is the class of the session's label.
	Want tmux.Label
	// ScopeValue is the answer's ScopeValue (the extra-line shape only).
	ScopeValue bool
}

// LabelLineID is the session id of the line every LabelShape is listed on.
const LabelLineID = "$1"

// Entry is the one-session lookup answer listing the shape.
func (s LabelShape) Entry() Entry {
	e := Answer("lookup/label:"+s.Name, s.Source, serverPID, f9ServerStart, []Listed{{
		ID: LabelLineID, Created: f9ServerStart, Name: "a", Label: s.Value, Want: s.Want,
	}})
	e.Lookup.ScopeValue = s.ScopeValue
	return e
}

// LabelShapes returns the AC-LKP-05 shapes (each LabelNone), the control
// characters of LFR G1, and valid labels, an id with '#' (F7), spaces or
// UTF-8 (U2) included.
func LabelShapes() []LabelShape {
	own := func(id string) string { return LabelValue(Token, LabelLineID, id) }
	none := func(name, source, value string) LabelShape {
		return LabelShape{Name: name, Source: source, Value: value}
	}
	valid := func(name, source, id string) LabelShape {
		return LabelShape{Name: name, Source: source, Value: own(id), Want: Valid(Token, id)}
	}
	return []LabelShape{
		none("no-space", "AC-LKP-05", "ad1_"+Token+"_$1_agent-x"),
		none("version-ad2", "AC-LKP-05", "ad2 "+Token+" $1 agent-x"),
		none("token-uppercase", "AC-LKP-05", "ad1 3F9C1E7A2B6D4058 $1 agent-x"),
		none("token-15-hex", "AC-LKP-05", "ad1 "+Token[:15]+" $1 agent-x"),
		none("token-17-hex", "AC-LKP-05", "ad1 "+Token+"0 $1 agent-x"),
		none("token-placeholder", "AC-LKP-05; provenance-fresh F9's TOK1", "ad1 TOK1 $1 agent-x"),
		none("embedded-other-session", "AC-LKP-05; provenance-fresh F2, F9b", LabelValue(Token, "$0", "agent-x")),
		none("embedded-pane-id", "AC-LKP-05", LabelValue(Token, "%1", "agent-x")),
		none("empty-instance-id", "AC-LKP-05; LFR G1", own("")),
		none("no-instance-id", "AC-LKP-05", "ad1 "+Token+" $1"),
		none("empty-value", "AC-LKP-05; SR-3.5: empty means unset", ""),
		none("id-with-esc", "LFR G1", own("agent\x1bx")),
		none("id-with-tab", "LFR G1", own("agent\tx")),
		none("id-with-del", "LFR G1", own("agent\x7fx")),
		{Name: "extra-line", Source: "AC-LKP-05; LFR H6: the rest is a scope-section line",
			Value: own("agent-x") + "\nx", Want: Valid(Token, "agent-x"), ScopeValue: true},
		valid("valid", "SR-3.4", "agent-x"),
		valid("valid-hash", "provenance-fresh F7", "id#1"),
		valid("valid-spaces", "SR-3.4: the id is everything after the third space", "agent x y"),
		valid("valid-utf8", "E.3 U2, U3", "agent-ü1"),
	}
}
