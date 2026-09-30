package tmux

import (
	"bytes"
	"strconv"
	"strings"
	"unicode/utf8"
)

// This file holds the pure parsers of the Phase 1 call set: label
// classification (SR-3.4, LFR G1), the lookup answer (SR-3.4, LFR H6), the
// pane listing with its pane labels (SR-2.1, WD 2026-09-29c) and the create
// reply (SR-2.1), and reply recognition from the
// first line of standard error (SR-2.3, SR-2.5). None reads the environment,
// the clock or configuration.

// maxFirstLine bounds CallError.FirstLine (Appendix F.1).
const maxFirstLine = 200

// labelPrefix begins every label agent-director writes (SR-3.4). The label
// is "ad1 <launch token> <session id> <instance id> <store id>" (WD
// 2026-09-29 STORE); ad1 is not bumped, the release being unpublished.
const labelPrefix = "ad1 "

// labelTokenLen is the launch token's length: 64 bits as lowercase hex.
const labelTokenLen = 16

// labelStoreIDLen is the store id's length: the writing store's
// store_meta.store_id, 64 bits as lowercase hex (SR-5.1).
const labelStoreIDLen = 16

// The reply wordings tmux prints on standard error (SR-2.5; verified
// identical on 3.2a and 3.3a, Appendix E.10).
const (
	replyNoServerPrefix  = "no server running on "
	replyConnectPrefix   = "error connecting to "
	replyNoSocketSuffix  = " (No such file or directory)"
	replyDeniedSuffix    = " (Permission denied)"
	replyDuplicatePrefix = "duplicate session: "
)

// Field counts of the parsed lines (SR-2.1, SR-3.4).
const (
	lookupFieldCount      = 6 // id, created, pid, start_time, name, label
	paneFieldCount        = 6 // session id, window, pane index, pane id, pane pid, pane label
	createReplyFieldCount = 5 // session id, pid, start_time, pane id, pane pid
)

// Fixed FirstLine texts for output whose lines must not be quoted: lookup
// lines can carry label values (SR-2.3), and cut-short output is incomplete.
const (
	firstLineBadSessionLine  = "malformed lookup answer: a session line has a missing or unparseable field"
	firstLineSessionInScope  = "malformed lookup answer: a session line follows the scope section"
	firstLineServerDisagrees = "malformed lookup answer: session lines disagree on the server"
	firstLineOutputCut       = "output cut short: its pipes stayed open past the pipe-close wait"
)

// classifyLabel classifies a raw @ad_owner value read on the lookup line of
// session sessionID (SR-3.4, LFR G1; WD 2026-09-29 STORE). It is LabelValid
// only for "ad1 <token> <$N> <instance id> <store id>": ad1 and a space; a
// 16-lowercase-hex token and a space; an embedded session id equal to
// sessionID and a space; then the rest, split at its last space into a
// non-empty instance id with no control character and a 16-lowercase-hex
// store id. The instance id may therefore hold spaces and '#', and is kept
// byte for byte. A four-field value, an empty (trailing-space) store id or
// anything else is LabelNone with every field empty, except that a
// four-field value whose instance id ends in a space and 16 lowercase hex
// reads as a shorter id plus that word as its store id. The raw value is
// never retained, and neither it nor the store id is ever put in a CallError.
func classifyLabel(raw, sessionID string) Label {
	rest, ok := strings.CutPrefix(raw, labelPrefix)
	if !ok || len(rest) <= labelTokenLen || rest[labelTokenLen] != ' ' {
		return Label{}
	}
	token := rest[:labelTokenLen]
	if !isLowerHex(token) {
		return Label{}
	}
	embedded, tail, ok := strings.Cut(rest[labelTokenLen+1:], " ")
	if !ok || embedded != sessionID || !isSessionID(embedded) {
		return Label{}
	}
	last := strings.LastIndexByte(tail, ' ')
	if last < 0 {
		return Label{}
	}
	instanceID, storeID := tail[:last], tail[last+1:]
	if instanceID == "" || hasControlChar(instanceID) {
		return Label{}
	}
	if len(storeID) != labelStoreIDLen || !isLowerHex(storeID) {
		return Label{}
	}
	return Label{Kind: LabelValid, Token: token, InstanceID: instanceID, StoreID: storeID}
}

// parseLookup parses the lookup's standard output by the rule of SR-3.4 (LFR
// H6): session lines first; the scope section starts at the first line that
// is not a session line and runs to the end; any non-empty scope line sets
// ScopeValue. A session-shaped line with a field that does not parse, a
// session-shaped line in the scope section, or session lines that disagree on
// the server make the answer malformed: ok is false and firstLine is a fixed
// description that quotes no line (a line can carry a label value).
func parseLookup(out []byte) (ans LookupAnswer, ok bool, firstLine string) {
	lines := strings.Split(string(out), "\n")
	i := 0
	for ; i < len(lines); i++ {
		if !isSessionShaped(lines[i]) {
			break
		}
		s, pid, start, good := parseSessionLine(lines[i])
		if !good {
			return LookupAnswer{}, false, firstLineBadSessionLine
		}
		if len(ans.Sessions) > 0 && (pid != ans.ServerPID || start != ans.ServerStart) {
			return LookupAnswer{}, false, firstLineServerDisagrees
		}
		ans.ServerPID, ans.ServerStart = pid, start
		ans.Sessions = append(ans.Sessions, s)
	}
	for ; i < len(lines); i++ {
		if isSessionShaped(lines[i]) {
			return LookupAnswer{}, false, firstLineSessionInScope
		}
		if lines[i] != "" {
			ans.ScopeValue = true
		}
	}
	return ans, true, ""
}

// isSessionShaped reports whether line begins with a session id and a tab.
func isSessionShaped(line string) bool {
	id, _, ok := strings.Cut(line, "\t")
	return ok && isSessionID(id)
}

// parseSessionLine parses one session line: id, created, server pid, server
// start, name, and the label as everything after the fifth tab.
func parseSessionLine(line string) (s Session, pid int, start int64, ok bool) {
	f := strings.SplitN(line, "\t", lookupFieldCount)
	if len(f) != lookupFieldCount {
		return Session{}, 0, 0, false
	}
	created, ok1 := parseDecimal64(f[1])
	pid, ok2 := parseDecimalInt(f[2])
	start, ok3 := parseDecimal64(f[3])
	if !ok1 || !ok2 || !ok3 {
		return Session{}, 0, 0, false
	}
	return Session{ID: f[0], Created: created, Name: f[4], Label: classifyLabel(f[5], f[0])}, pid, start, true
}

// parsePanes parses the pane listing's standard output: each non-empty line
// is session id, window index, pane index, pane id, pane pid and pane label,
// tab separated, the label being everything after the fifth tab. A line that
// does not parse makes the listing malformed; ok is false and firstLine is
// the listing's first line cut before its label field (paneListingFirstLine).
func parsePanes(out []byte) (panes []Pane, ok bool, firstLine string) {
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		p, good := parsePaneLine(line)
		if !good {
			return nil, false, paneListingFirstLine(out)
		}
		panes = append(panes, p)
	}
	return panes, true, ""
}

// parsePaneLine parses one list-panes -a line.
func parsePaneLine(line string) (Pane, bool) {
	f := strings.SplitN(line, "\t", paneFieldCount)
	if len(f) != paneFieldCount || !isSessionID(f[0]) || !isPaneID(f[3]) {
		return Pane{}, false
	}
	window, ok1 := parseDecimalInt(f[1])
	index, ok2 := parseDecimalInt(f[2])
	pid, ok3 := parseDecimalInt(f[4])
	if !ok1 || !ok2 || !ok3 {
		return Pane{}, false
	}
	return Pane{SessionID: f[0], Window: window, Index: index, ID: f[3], PID: pid,
		AdPane: classifyPaneLabel(f[5], f[3])}, true
}

// classifyPaneLabel returns the launch token of a raw #{@ad_pane} value read
// on the listing line of pane paneID, or "" (WD 2026-09-29c). The value
// counts only when it is exactly "<16 lowercase hex token> <pane id>" with
// that pane id equal to paneID: tmux lists a server, window, session or
// global value on every pane with no value of its own (a server value even on
// a pane with one), so a value naming another pane, or none, is borrowed and
// never counts (the scope guard of SR-3.4 and SR-3.6). The raw value is never
// retained.
func classifyPaneLabel(raw, paneID string) string {
	token, embedded, ok := strings.Cut(raw, " ")
	if !ok || len(token) != labelTokenLen || !isLowerHex(token) || embedded != paneID {
		return ""
	}
	return token
}

// paneListingFirstLine is a malformed pane listing's FirstLine: its first
// line cut before the fifth tab, so the pane label field is never quoted
// (SR-2.3, SR-15), then trimmed and capped like firstLineOf. The continuation
// of a label value holding a newline is never a listing's first line.
func paneListingFirstLine(out []byte) string {
	line, _, _ := strings.Cut(string(out), "\n")
	if f := strings.SplitN(line, "\t", paneFieldCount); len(f) == paneFieldCount {
		line = strings.Join(f[:paneFieldCount-1], "\t")
	}
	return truncateFirstLine(strings.TrimSpace(line))
}

// parseCreateReply parses the create's standard output: exactly one complete
// line, newline included, "<$N> <server pid> <server start> <%N> <pane pid>".
// Anything else, a line cut short included, does not parse.
func parseCreateReply(out []byte) (CreateReply, bool) {
	line, ok := strings.CutSuffix(string(out), "\n")
	if !ok || strings.Contains(line, "\n") {
		return CreateReply{}, false
	}
	f := strings.Split(line, " ")
	if len(f) != createReplyFieldCount || !isSessionID(f[0]) || !isPaneID(f[3]) {
		return CreateReply{}, false
	}
	serverPID, ok1 := parseDecimalInt(f[1])
	serverStart, ok2 := parseDecimal64(f[2])
	panePID, ok3 := parseDecimalInt(f[4])
	if !ok1 || !ok2 || !ok3 {
		return CreateReply{}, false
	}
	return CreateReply{SessionID: f[0], ServerPID: serverPID, ServerStart: serverStart, PaneID: f[3], PanePID: panePID}, true
}

// recognizeReply classifies a failed call from the first line of its standard
// error only (SR-2.3, SR-2.5). The socket replies carry their socket path; the
// permission reply is recognised on every call kind (AC-CLS-02); the
// duplicate-session prefix only on the create. Anything else is
// FailUnrecognized carrying the trimmed first line.
func recognizeReply(call Call, stderr []byte) (f Failure, socket, firstLine string) {
	line := trimmedFirstLine(stderr)
	if s, ok := strings.CutPrefix(line, replyNoServerPrefix); ok {
		return FailNoServer, s, ""
	}
	if rest, ok := strings.CutPrefix(line, replyConnectPrefix); ok {
		if s, ok := strings.CutSuffix(rest, replyDeniedSuffix); ok {
			return FailSocketDenied, s, ""
		}
		if s, ok := strings.CutSuffix(rest, replyNoSocketSuffix); ok {
			return FailNoSocket, s, ""
		}
	}
	if call == CallCreate && strings.HasPrefix(line, replyDuplicatePrefix) {
		return FailDuplicate, "", ""
	}
	return FailUnrecognized, "", truncateFirstLine(line)
}

// firstLineOf returns b's first line, trimmed of surrounding whitespace and
// cut to at most maxFirstLine bytes.
func firstLineOf(b []byte) string { return truncateFirstLine(trimmedFirstLine(b)) }

// trimmedFirstLine returns b's first line, trimmed of surrounding whitespace.
func trimmedFirstLine(b []byte) string {
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		b = b[:i]
	}
	return strings.TrimSpace(string(b))
}

// truncateFirstLine cuts s to at most maxFirstLine bytes, backing off to a
// UTF-8 sequence start so a valid multi-byte character is never split.
func truncateFirstLine(s string) string {
	if len(s) <= maxFirstLine {
		return s
	}
	cut := maxFirstLine
	for back := 0; back < utf8.UTFMax && cut > 0 && !utf8.RuneStart(s[cut]); back++ {
		cut--
	}
	return s[:cut]
}

// isSessionID reports whether s is "$" followed by decimal digits.
func isSessionID(s string) bool { return len(s) > 1 && s[0] == '$' && isDigits(s[1:]) }

// isPaneID reports whether s is "%" followed by decimal digits.
func isPaneID(s string) bool { return len(s) > 1 && s[0] == '%' && isDigits(s[1:]) }

// isDigits reports whether s is non-empty and all ASCII decimal digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// isLowerHex reports whether s is all lowercase hexadecimal characters.
func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// hasControlChar reports whether s contains an ASCII control character
// (0x00-0x1f or 0x7f).
func hasControlChar(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}

// parseDecimal64 parses s as an unsigned decimal (digits only, no sign).
func parseDecimal64(s string) (int64, bool) {
	if !isDigits(s) {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

// parseDecimalInt parses s as an unsigned decimal int (digits only, no sign).
func parseDecimalInt(s string) (int, bool) {
	if !isDigits(s) {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}
