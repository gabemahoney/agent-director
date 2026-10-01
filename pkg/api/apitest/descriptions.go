package apitest

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// descriptions.go is the shared description helper of SR-20.2: it holds the
// required phrases of the SR-1.4 cases (as DescCase constructors) and the
// forms no description may contain (SR-1.4's session-ending commands, the
// tmux attach commands and the opt-in's SR-6.8 spellings). Tests assert
// error descriptions only through AssertDescription, and agent-facing texts
// that may name kill as a documented procedure (manifest descriptions)
// through AssertAgentText; no test spells these phrases or forms itself. A
// new SR-1.4 case is added here as a constructor, or in a sibling file of
// one verb's cases (descriptions_resume.go, descriptions_kill.go), of the
// pane verbs' cases (descriptions_pane.go), of plain
// spawn's errors after "duplicate session" (descriptions_held.go), of the
// lookup's shared Can't tell cases (descriptions_lookup.go), of SR-18.6's live-row
// sequence, which kill's manifest description states in its short form and
// the find-missing and spawn descriptions point to (descriptions_live_row.go),
// of find-missing's manifest texts and SR-18.2's "not proof" statement
// (descriptions_find_missing.go), of expire's manifest texts and SR-18.7's
// cleanup guidance (descriptions_expire.go), of the shared
// starting-session refusal (descriptions_starting.go), of resume's
// pre-launch check (descriptions_resume_lookup.go), of resume's and reuse's
// errors after "duplicate session" (descriptions_resume_held.go), or of
// reuse's own cases (descriptions_reuse.go).

// DescCase is one SR-1.4 description case: Name (shown in every failure),
// the phrases the description must contain, the case's own must-not phrases
// (case-insensitive, whole words where they are words) and values it must
// never carry (exact substrings, such as the id itself). Only an
// ErrTmuxKillFailed case (DescKillWaitExpired, DescKillUncheckable,
// DescKillNoPane) allows "retry kill later". An unanswered case
// (DescCallTimeout, DescUnrecognisedReply, DescHeldAmbiguous) is an
// ErrTmuxUnresponsive whose retry guidance AfterHeldName replaces.
type DescCase struct {
	Name    string
	Require []string
	MustNot []string
	Forbid  []string

	allowRetryKill bool
	unanswered     bool
	transient      bool // may say "retry later" (DescStillStopping, DescStillStarting)
}

// DescSession is a tmux session a description names: its name (quoted in the
// description, Go quoted-string form) and its tmux id ($N).
type DescSession struct {
	Name string
	ID   string
}

// OperatorActionsTitle is the title of the agent-director README section the
// "Operator actions" pointer names (SR-18.17): the one definition that
// PointsToOperatorActions and the README section check both read.
const OperatorActionsTitle = "Operator actions"

// PointsToOperatorActions returns c also requiring the pointer to the
// OperatorActionsTitle section of the agent-director README (SR-1.4,
// SR-18.17), quoted by its title.
func (c DescCase) PointsToOperatorActions() DescCase {
	c.Require = append(append([]string(nil), c.Require...), strconv.Quote(OperatorActionsTitle), "agent-director README")
	return c
}

// retryKillLater is the one exception to the session-ending forms: the
// refused verb in ErrTmuxKillFailed's description (SR-1.4).
const retryKillLater = "retry kill later"

// form is one forbidden form: what it is (for failures) and its pattern.
type form struct {
	what string
	re   *regexp.Regexp
}

// tmuxEndingForms, tmuxAttachForms and optInForms are forbidden in every
// agent-facing text; commandForms (kill or pause named as a command to run)
// only in error descriptions (SR-1.4, SR-6.8, SR-20.2). tmuxAttachForms keeps
// agent-visible text from telling an agent to run a tmux command: "tmux
// attach" matches as whole words only, so "tmux session" and "tmux
// attached" pass. The by-hand attach command of ad.launch.name_held (a trail
// read by humans) and the README's "Operator actions" are not checked here.
var (
	tmuxEndingForms = []form{
		{"the tmux command kill-session", regexp.MustCompile(`(?i)\bkill-session\b`)},
		{"the tmux command kill-server", regexp.MustCompile(`(?i)\bkill-server\b`)},
	}
	tmuxAttachForms = []form{
		{"the tmux command attach-session", regexp.MustCompile(`(?i)\battach-session\b`)},
		{"the tmux command tmux attach", regexp.MustCompile(`(?i)\btmux\s+attach\b`)},
	}
	optInForms = []form{
		{"the opt-in's flag (--include-finished, include-finished, include_finished, IncludeFinished)", regexp.MustCompile(`(?i)include[-_]?finished`)},
	}
	commandForms = []form{
		{"kill or pause as an agent-director command", regexp.MustCompile(`(?i)\bagent-director\s+(kill|pause)\b`)},
		{"kill or pause as a command to run", regexp.MustCompile(`(?i)\b(run|call|use|retry|try|invoke)\s+(the\s+)?(kill|pause)\b`)},
		{"kill or pause quoted as a command", regexp.MustCompile("(?i)[`\"](kill|pause)[`\"]")},
		{"kill or pause given a flag", regexp.MustCompile(`(?i)\b(kill|pause)\s+-`)},
	}
	descriptionForms = concatForms(commandForms, tmuxEndingForms, tmuxAttachForms, optInForms)
	agentTextForms   = concatForms(tmuxEndingForms, tmuxAttachForms, optInForms)
)

// labelValue matches a label's raw value (`ad1 <16-hex token> ...`), which no
// description may carry (SR-1.4).
var labelValue = regexp.MustCompile(`\bad1 [0-9a-f]{16}\b`)

func concatForms(lists ...[]form) []form {
	var out []form
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

// AssertDescription checks an error description against case c: every
// required phrase present; none of c's must-not phrases, no session-ending
// command form (SR-1.4; "retry kill later" excepted for an ErrTmuxKillFailed
// case only), no tmux attach command (attach-session, tmux attach), no
// opt-in spelling (SR-6.8), no label raw value, and none of the caller's
// forbid values (another row's id, session-environment values, a token, a
// store id, a label value; empty values are ignored).
func AssertDescription(t testing.TB, desc string, c DescCase, forbid ...string) {
	t.Helper()
	label := "description case " + strconv.Quote(c.Name)
	checkPhrases(t, label, desc, c, forbid...)
	if c.allowRetryKill {
		desc = strings.ReplaceAll(desc, retryKillLater, "")
	}
	checkForms(t, label, desc, descriptionForms)
}

// AssertAgentText checks a text agent-director shows agents but which may
// name kill as a documented procedure (a manifest description, help): no
// opt-in spelling, no tmux session-ending command and no tmux attach command
// (attach-session, tmux attach) (SR-1.4, SR-6.8, SR-20.2). what names the
// text in failures.
func AssertAgentText(t testing.TB, what, text string) {
	t.Helper()
	checkForms(t, what, text, agentTextForms)
}

// AssertAgentTextCase is AssertAgentText plus case c's required phrases,
// must-not phrases and forbid values, for a rule a manifest text must state
// (such as DescSpawnLaunchTimeoutRule). kill named as a documented procedure
// stays allowed, as in AssertAgentText.
func AssertAgentTextCase(t testing.TB, what, text string, c DescCase) {
	t.Helper()
	checkPhrases(t, what+", case "+strconv.Quote(c.Name), text, c)
	checkForms(t, what, text, agentTextForms)
}

// checkPhrases checks text for c's required phrases, must-not phrases and
// forbid values (c.Forbid and forbid). label names the text in failures.
func checkPhrases(t testing.TB, label, text string, c DescCase, forbid ...string) {
	t.Helper()
	for _, p := range c.Require {
		if !strings.Contains(text, p) {
			t.Errorf("%s: missing required phrase %q in %q", label, p, text)
		}
	}
	for _, p := range c.MustNot {
		if p == "" {
			continue
		}
		if m := mustNotPattern(p).FindString(text); m != "" {
			t.Errorf("%s: contains must-not phrase %q (%q) in %q", label, p, m, text)
		}
	}
	for _, v := range append(append([]string(nil), c.Forbid...), forbid...) {
		if v != "" && strings.Contains(text, v) {
			t.Errorf("%s: contains forbidden value %q in %q", label, v, text)
		}
	}
	if m := labelValue.FindString(text); m != "" {
		t.Errorf("%s: contains a label value %q in %q", label, m, text)
	}
}

func checkForms(t testing.TB, what, text string, forms []form) {
	t.Helper()
	for _, f := range forms {
		if m := f.re.FindString(text); m != "" {
			t.Errorf("%s: contains %s (%q) in %q", what, f.what, m, text)
		}
	}
}

// mustNotPattern matches p case-insensitively, bounded as a whole word at
// each end that is a word character.
func mustNotPattern(p string) *regexp.Regexp {
	expr := regexp.QuoteMeta(p)
	if isWordByte(p[0]) {
		expr = `\b` + expr
	}
	if isWordByte(p[len(p)-1]) {
		expr += `\b`
	}
	return regexp.MustCompile("(?i)" + expr)
}

func isWordByte(b byte) bool {
	return b == '_' || '0' <= b && b <= '9' || 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z'
}

// seconds renders d as descriptions give an effective timeout ("within 5 s",
// "within 0.3 s").
func seconds(d time.Duration) string {
	return "within " + inSeconds(d)
}

// inSeconds renders d as descriptions give an effective value in seconds
// ("90 s", "0.3 s").
func inSeconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + " s"
}

// unresponsiveMustNot is what no ErrTmuxUnresponsive description may say.
var unresponsiveMustNot = []string{"dead", "gone"}

// DescKillSentinelText is the text of a tmux sentinel that kill's
// descriptions wrap (ErrTmuxNotAvailable, ErrTmuxKillFailed,
// ErrTmuxUnresponsive, ErrTmuxSessionConflict): neither "dead" nor "gone"
// (SR-1.4's must-nots) and, per Epic 10's error-names Task, not the bare word
// "kill" either, since "retry kill later" belongs to ErrTmuxKillFailed's
// description and never to a sentinel text. name is the sentinel's name.
// AssertDescription adds the session-ending command forms.
func DescKillSentinelText(name string) DescCase {
	return DescCase{
		Name:    name + " sentinel text",
		MustNot: append(append([]string(nil), unresponsiveMustNot...), "kill"),
	}
}

// DescInstanceIDControlChar is ErrInvalidFlags for an explicit instance id
// with a control character; the description never carries the id.
func DescInstanceIDControlChar(id string) DescCase {
	return DescCase{
		Name:    "ErrInvalidFlags, control character",
		Require: []string{"the instance id contains a control character"},
		Forbid:  []string{id, strings.Trim(strconv.Quote(id), `"`)},
	}
}

// DescPreCheckRead is ErrInternal for a collision pre-check read failure.
func DescPreCheckRead() DescCase {
	return DescCase{
		Name:    "ErrInternal, pre-check read failure",
		Require: []string{"the collision pre-check could not read the store"},
	}
}

// LaunchTimeout parameterises DescLaunchTimeout. Timeout is the create call's
// effective timeout; Unrecognised is a non-zero exit with an unparseable
// reply instead; RowReset is reuse's "the row was reset" (Epic 17;
// DescCase.withRowReset).
type LaunchTimeout struct {
	InstanceID   string
	Timeout      time.Duration
	Unrecognised bool
	RowReset     bool
}

// rowStaysPending is the row sentence of a launch failure that leaves the
// row as the launch's write left it: a plain spawn's after any create
// failure, and every launch verb's after a launch timeout (SR-1.4). A
// resume's other launch failures give the restore's result instead
// (AfterResumeRestore).
const rowStaysPending = "the row stays pending"

// The launch-timeout rule's phrases, shared by the error description
// (DescLaunchTimeout) and the spawn manifest text (DescSpawnLaunchTimeoutRule).
const (
	sessionMayExist  = "the session may have been created"
	launchRetryRule  = "do not retry until get shows the row ended or missing"
	scanLeftoverWord = "left over from an earlier life"
)

// DescLaunchTimeout is ErrTmuxUnresponsive for a launch whose session
// creation timed out or gave an unrecognised reply.
func DescLaunchTimeout(p LaunchTimeout) DescCase {
	req := []string{
		p.InstanceID, string(tmux.CallCreate), sessionMayExist,
		rowStaysPending, launchRetryRule,
	}
	if p.Unrecognised {
		req = append(req, "tmux gave a reply agent-director does not recognise")
	} else {
		req = append(req, seconds(p.Timeout))
	}
	return DescCase{
		Name:    "ErrTmuxUnresponsive, launch timeout",
		Require: req,
		MustNot: append([]string{"nothing was done", "retry later"}, unresponsiveMustNot...),
	}.withRowReset(p.RowReset)
}

// DescCallTimeout is ErrTmuxUnresponsive for a call (other than a launch's
// create) that did not answer within its effective timeout.
func DescCallTimeout(call tmux.Call, timeout time.Duration) DescCase {
	return DescCase{
		Name:       "ErrTmuxUnresponsive, " + string(call) + " timeout",
		Require:    []string{string(call), seconds(timeout), "nothing was done", "retry later"},
		MustNot:    unresponsiveMustNot,
		unanswered: true,
	}
}

// DescUnrecognisedReply is ErrTmuxUnresponsive for a call whose reply tmux
// gave but agent-director does not recognise; firstLine, when not empty, is
// the reply's first line as the description must show it (trimmed to 200
// bytes).
func DescUnrecognisedReply(call tmux.Call, firstLine string) DescCase {
	req := []string{string(call), "tmux gave a reply agent-director does not recognise"}
	if firstLine != "" {
		req = append(req, firstLine)
	}
	return DescCase{
		Name:       "ErrTmuxUnresponsive, " + string(call) + " unrecognised reply",
		Require:    req,
		MustNot:    unresponsiveMustNot,
		unanswered: true,
	}
}

// UnlabelledSession parameterises DescUnlabelledSession: the session's name
// and tmux id, whether it was ended by that id, and the launch's row
// sentence: PlainSpawn for a plain spawn (the row stays pending), or Restore
// for a resume (the restore's result, SR-8.5). Set at most one of the two.
type UnlabelledSession struct {
	Name       string
	SessionID  string
	Ended      bool
	PlainSpawn bool
	Restore    ResumeRestore
}

// DescUnlabelledSession is ErrTmuxSessionCreate for a created session that
// could not be labelled (SR-3.5).
func DescUnlabelledSession(p UnlabelledSession) DescCase {
	req := []string{strconv.Quote(p.Name), "the session was created but could not be labelled"}
	if p.Ended {
		req = append(req, "ended by its tmux id "+p.SessionID)
	} else {
		req = append(req, "an unlabelled session may still run")
	}
	if p.PlainSpawn {
		req = append(req, rowStaysPending)
	}
	c := DescCase{Name: "ErrTmuxSessionCreate, created but not labelled", Require: req}
	if p.Restore.Outcome != RestoreNone {
		c = c.AfterResumeRestore(p.Restore)
	}
	return c
}

// SessionCreateFailed parameterises DescSessionCreateFailed: Name is the
// requested session name; Duplicate is a create refused with "duplicate
// session" rather than another create failure (no server, no socket, a
// non-zero exit with no reply).
type SessionCreateFailed struct {
	Name      string
	Duplicate bool
}

// DescSessionCreateFailed is ErrTmuxSessionCreate for a create that failed
// without creating a session. SR-1.4's one row for it, "duplicate session"
// whose re-lookup is Gone, requires the quoted name and that session creation
// failed; Duplicate applies that row. SR-1.4 has no row for the other create
// failures (SR-9.4: "Any other failure: ErrTmuxSessionCreate as today"), so
// without Duplicate the case requires no phrase and AssertDescription checks
// only the forbidden forms. A created session that could not be labelled is
// DescUnlabelledSession.
func DescSessionCreateFailed(p SessionCreateFailed) DescCase {
	if !p.Duplicate {
		return DescCase{Name: "ErrTmuxSessionCreate, create failed"}
	}
	return DescCase{
		Name:    "ErrTmuxSessionCreate, duplicate session",
		Require: []string{strconv.Quote(p.Name), "session creation failed"},
	}
}

// DescTmuxNotRun is ErrTmuxNotAvailable when the tmux binary could not be
// run. SR-1.4 has no row for this case (SR-1.2 names only its class), so it
// requires no phrase and AssertDescription checks only the forbidden forms.
func DescTmuxNotRun() DescCase {
	return DescCase{Name: "ErrTmuxNotAvailable, tmux could not be run"}
}

// DescSocketPermission is ErrTmuxNotAvailable for a socket this user may not
// use. It never reads as a missing binary, which is how the tmux binary not
// running is described (the sentinel's own text is "tmux: not available").
func DescSocketPermission(socket string) DescCase {
	return DescCase{
		Name:    "ErrTmuxNotAvailable, socket permission",
		Require: []string{socket, "not accessible to this user"},
		MustNot: []string{"binary not available"},
	}
}

// DescSocketDir is ErrTmuxNotAvailable for an unusable socket directory:
// reason is tmux's reason in tmux's own words (as tmux.SocketDirError gives it).
func DescSocketDir(socket, dir, reason string) DescCase {
	return DescCase{
		Name:    "ErrTmuxNotAvailable, unusable socket directory",
		Require: []string{socket, dir, reason, "nothing was launched"},
	}
}

// DescIdentityWriteWarn is the one client-log WARN line a failed identity
// write gives (SR-3.6). It is a log line, not an SR-1.4 error description:
// the SRD fixes only that it is one WARN line, so the case requires the WARN
// level and the instance id. Pass the launch token and the store id as forbid.
func DescIdentityWriteWarn(instanceID string) DescCase {
	return DescCase{Name: "identity-write WARN line", Require: []string{"WARN", instanceID}}
}

// scanNamed is how many leftover sessions the scan's refusal names before
// giving the rest as a count (SR-1.4).
const scanNamed = 3

// rowEndedStatements are statements that a row was ended, which the scan's
// refusals never make (no row exists).
var rowEndedStatements = []string{"row was ended", "row has ended", "rows were ended", "ended the row"}

// DescScanLeftover is ErrTmuxSessionConflict for plain spawn's label scan
// finding a leftover of instanceID; sessions are all those found, in the
// order the description names them (lowest $N first).
func DescScanLeftover(instanceID string, sessions []DescSession) DescCase {
	return earlierLifeCase("ErrTmuxSessionConflict, scan leftover", instanceID, sessions, nothingWrittenNoRow)
}

// namedSessions splits sessions (in the order a description names them) into
// the phrases it must contain, each named session's quoted name and tmux id
// up to scanNamed and then the rest's count, and the quoted names it must not.
func namedSessions(sessions []DescSession) (named, unnamed []string) {
	for i, s := range sessions {
		if i < scanNamed {
			named = append(named, strconv.Quote(s.Name), s.ID)
		} else {
			unnamed = append(unnamed, strconv.Quote(s.Name))
		}
	}
	if more := len(sessions) - scanNamed; more > 0 {
		named = append(named, fmt.Sprintf("%d more", more))
	}
	return named, unnamed
}

// DescSpawnLaunchTimeoutRule is the launch-timeout rule as the spawn manifest
// description states it (SR-18.1): the create is bounded, a timeout returns
// ErrTmuxUnresponsive, the session may exist, the new row stays pending, and
// the caller does not retry until get shows the row ended or missing. Check
// it with AssertAgentTextCase.
func DescSpawnLaunchTimeoutRule() DescCase {
	return DescCase{
		Name: "spawn manifest, launch-timeout rule",
		Require: []string{
			"bounded by the create timeout", "ErrTmuxUnresponsive", sessionMayExist,
			"row stays pending", launchRetryRule, "would start a second agent",
		},
		MustNot: append([]string{"retry later"}, unresponsiveMustNot...),
	}
}

// DescLaunchStartedAtField is launch_started_at as a manifest result text
// states it (SR-22.2): RFC3339 UTC with millisecond precision, shown only on
// a pending row and omitted otherwise. listRow is list's composite spawns
// text, which also names the field and its type. Check it with
// AssertAgentTextCase.
func DescLaunchStartedAtField(listRow bool) DescCase {
	req := []string{"RFC3339 UTC with millisecond precision", "pending", "omitted"}
	if listRow {
		req = append(req, "launch_started_at (timestamp?)")
	}
	return DescCase{Name: "manifest result, launch_started_at", Require: req}
}

// DescSpawnScanRefusal is the label scan's refusal as the spawn manifest
// description states it (SR-9.3, SR-18): a leftover of an explicit id is
// ErrTmuxSessionConflict, a CONFLICT pointing to "Operator actions", and
// nothing is written. Check it with AssertAgentTextCase.
func DescSpawnScanRefusal() DescCase {
	return DescCase{
		Name: "spawn manifest, label scan refusal",
		Require: []string{
			"ErrTmuxSessionConflict", "CONFLICT", scanLeftoverWord,
			strconv.Quote(OperatorActionsTitle), "nothing is written",
		},
		MustNot: rowEndedStatements,
	}
}

// DescTmuxSocketField is tmux_socket as get's manifest result text states it
// (SR-3.3, SR-16.1): the tmux socket the row's launch uses, omitted for a row
// from before this release. Check it with AssertAgentTextCase.
func DescTmuxSocketField() DescCase {
	return DescCase{
		Name:    "manifest result, tmux_socket",
		Require: []string{"tmux socket the row's launch uses", "omitted for a row from before this release"},
	}
}
