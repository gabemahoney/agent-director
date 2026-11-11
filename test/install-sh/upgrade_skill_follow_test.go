package installsh_test

// J47: a literal follow of the upgrade skill,
// skills/upgrade-agent-director/SKILL.md (SR-2.11's maintenance boundary and
// SR-2.14's one-run rule, put into practice), from a genuine v0.17.3 install
// (crossingCase) whose store holds an ended row and, in most rows, a live row
// whose agent is gone. upgradeFollow reads each step's commands from the
// skill with docSection and types them into one opsShell, filling only the
// skill's placeholders; an operator stand-in answers the skill's questions.
// The agents' tmux server is one this test starts, which the follower's shell
// reaches through TMUX_TMPDIR, so the place checks see live pane processes.
// Step 6 is not followed: resume relaunches each agent, which the toolbox's
// claude stand-in cannot run.
//
// Nothing here is t.Parallel, as in operator_actions_follow_test.go.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// upgradeSkill is the upgrade skill, from the repo root.
const upgradeSkill = "skills/upgrade-agent-director/SKILL.md"

// upgradeSweepPause stands in for step 2's "about 5 s" between sweeps.
const upgradeSweepPause = 100 * time.Millisecond

// upgradeInstallWait bounds step 3's install, a whole crossing upgrade.
const upgradeInstallWait = 5 * time.Minute

// b.5i3: the installer and binaries must come from the same release.
func upgradeNetworkInstall(t *testing.T, command string) string {
	t.Helper()
	m := regexp.MustCompile(`^curl -fsSL https://raw\.githubusercontent\.com/gabemahoney/agent-director/([^/\s]+)/skills/install-agent-director/install\.sh \| bash -s -- --from-release (\S+) `).FindStringSubmatch(command)
	if m == nil {
		t.Fatalf("step 3 must fetch a release's installer and name its --from-release tag: %q", command)
	}
	if m[1] != m[2] || !regexp.MustCompile(`^(<tag>|v[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?)$`).MatchString(m[1]) {
		t.Fatalf("step 3 installer ref %q and --from-release %q must name the same release tag, never main", m[1], m[2])
	}
	return m[0]
}

func TestUpgradeSkillReleaseInstaller(t *testing.T) {
	s := docSection(t, upgradeSkill, "3. Install")
	upgradeNetworkInstall(t, s.Block("install.sh | bash -s --"))
}

// upgradeNoRows is list's output with no row, as step 1 and step 2 quote it.
const upgradeNoRows = `{"spawns":[]}`

// The questions the skill has the follower ask the operator.
const (
	askStillRuns = "No live row's tmux session is there: does any of those agents still run?"                                     // step 1
	askStop      = "Will you stop whatever starts agents, every serve and every caller, and may each live row listed be stopped?" // step 1
	askWindow    = `Do steps 1 and 2 of "Upgrading the native-input journal" hold?`                                               // step 3
	askVerify    = "May the filled-in migrate-journal verification run?"                                                          // step 4
)

// step1Next is where step 1 leads.
type step1Next int

const (
	stopHere step1Next = iota // the skill's "stop here"
	toStep2
	toStep3 // no live row
	again   // a place check failed: step 1 again, from the agents' environment
)

// j47Tmux is the agents' tmux server. Its TMUX_TMPDIR is short, for the
// socket path's limit, and outside h.Tmp, which install.sh must leave empty.
type j47Tmux struct {
	h   *moveHome
	dir string
}

func newJ47Tmux(t *testing.T, h *moveHome) *j47Tmux {
	t.Helper()
	dir, err := os.MkdirTemp("", "j47-tmux-")
	if err != nil {
		t.Fatal(err)
	}
	x := &j47Tmux{h: h, dir: dir}
	t.Cleanup(func() {
		_ = x.command("kill-server").Run()
		_ = os.RemoveAll(dir)
	})
	return x
}

func (x *j47Tmux) command(args ...string) *exec.Cmd {
	cmd := exec.Command(x.h.RealTool("tmux"), args...)
	cmd.Dir = x.h.Root
	cmd.Env = []string{"HOME=" + x.h.Home, "PATH=" + x.h.S.Toolbox, "TMPDIR=" + x.dir, "TMUX_TMPDIR=" + x.dir}
	return cmd
}

// agent starts session name with one pane running sleep, an agent stand-in
// that lasts until its pane is killed, and returns the pane's id and pid and
// the pid's start time as a row records it (/proc's stat field 22).
func (x *j47Tmux) agent(t *testing.T, name string) (paneID string, pid int, start string) {
	t.Helper()
	if out, err := x.command("new-session", "-d", "-s", name, "-c", x.h.Root, "sleep 600").CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session %s: %v: %s", name, err, out)
	}
	out, err := x.command("list-panes", "-s", "-t", "="+name, "-F", "#{pane_id} #{pane_pid}").CombinedOutput()
	f := strings.Fields(string(out))
	if err != nil || len(f) != 2 {
		t.Fatalf("tmux list-panes %s: %v: %q", name, err, out)
	}
	if pid, err = strconv.Atoi(f[1]); err != nil {
		t.Fatal(err)
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatal(err)
	}
	i := strings.LastIndexByte(string(stat), ')')
	fields := strings.Fields(string(stat)[i+1:])
	if i < 0 || len(fields) < 20 {
		t.Fatalf("/proc/%d/stat: %q", pid, stat)
	}
	return f[0], pid, fields[19]
}

// liveAgent starts row id's agent in session ad-<id> and adds its row in
// state working.
func (x *j47Tmux) liveAgent(t *testing.T, c *crossingCase, id string) {
	t.Helper()
	pane, pid, start := x.agent(t, "ad-"+id)
	c.h.Sqlite(c.db, j47LiveAgentSQL(id, "working", pane, pid, start))
}

// j47LiveAgentSQL adds crossingLiveRowSQL's row id in state, with the agent in
// pane paneID (pid, start) as its SessionStart and pane identity, so
// v0.17.3's find-missing leaves it live and its kill ends that pane.
func j47LiveAgentSQL(id, state, paneID string, pid int, start string) string {
	return crossingLiveRowSQL(id, state) + fmt.Sprintf("UPDATE spawns SET pid = %d, proc_starttime = '%s', pane_id = '%s', pane_pid = %d, pane_starttime = '%s' WHERE instance_id = '%s';\n",
		pid, start, paneID, pid, start, id)
}

// upgradeRow is a list row, as the follower reads it.
type upgradeRow struct {
	ID      string `json:"instance_id"`
	State   string `json:"state"`
	Session string `json:"tmux_session_name"`
}

// upgradeFollow follows the skill's steps in one shell.
type upgradeFollow struct {
	t  *testing.T
	c  *crossingCase
	h  *moveHome
	sh *opsShell
	// operator answers each question the skill asks (an ask constant);
	// asked records them.
	operator func(question string) bool
	asked    []string
	// move, when set, moves the shell into the agents' tmux environment:
	// until then it is on another tmux server.
	move func()
	// afterStop and afterSweep, when set, each run once: after step 1 has
	// stopped the rows, and after step 2's sweep has left no live row. Each
	// is an agent a caller started late.
	afterStop, afterSweep func()
	stopped               map[string]bool   // the rows step 1 stopped
	sessions              map[string]string // each listed row's tmux session name
	marked                [][]string        // each find-missing's ids, sorted, in order
	installs              []moveResult      // each step 3 install
	backup                string            // the last install's --journal-backup
	steps1                int               // how many times step 1 started
}

// newUpgradeFollow is the follower in c's home, as the agents' user. With
// inTmux its shell is in the agents' tmux environment (TMUX_TMPDIR) from the
// start; without, step 1's move puts it there.
func newUpgradeFollow(t *testing.T, c *crossingCase, x *j47Tmux, inTmux bool, operator func(string) bool) *upgradeFollow {
	t.Helper()
	f := &upgradeFollow{t: t, c: c, h: c.h, sh: newOpsShell(c.h), operator: operator, stopped: map[string]bool{}, sessions: map[string]string{}}
	f.move = func() { f.must("export TMUX_TMPDIR=" + moveQuote(x.dir)) }
	if inTmux {
		f.move()
		f.move = nil
	}
	return f
}

func (f *upgradeFollow) must(command string) moveResult {
	f.t.Helper()
	res := f.sh.Run(command)
	if res.Code != 0 {
		f.t.Fatalf("%s: exit %d\nstdout: %s\nstderr: %s", command, res.Code, res.Stdout, res.Stderr)
	}
	return res
}

func (f *upgradeFollow) ask(question string) bool {
	f.asked = append(f.asked, question)
	return f.operator(question)
}

// list runs a list command and returns its rows, and whether it printed the
// skill's no-row output.
func (f *upgradeFollow) list(command string) ([]upgradeRow, bool) {
	f.t.Helper()
	res := f.must(command)
	var out struct {
		Spawns []upgradeRow `json:"spawns"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
		f.t.Fatalf("%s: %v: %q", command, err, res.Stdout)
	}
	none := strings.TrimSpace(res.Stdout) == upgradeNoRows
	if none != (len(out.Spawns) == 0) {
		f.t.Fatalf("%s printed %q: no row, but not %s", command, res.Stdout, upgradeNoRows)
	}
	for _, r := range out.Spawns {
		f.sessions[r.ID] = r.Session
	}
	return out.Spawns, none
}

// sweep runs a find-missing command and returns the ids it marked.
func (f *upgradeFollow) sweep(command string) []string {
	f.t.Helper()
	res := f.must(command)
	var found struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &found); err != nil {
		f.t.Fatalf("%s: %v: %q", command, err, res.Stdout)
	}
	ids := append([]string{}, found.IDs...)
	sort.Strings(ids)
	f.marked = append(f.marked, ids)
	return found.IDs
}

// listSessions runs step 1's tmux list-sessions and returns its output, ""
// when no tmux server answers this shell's socket.
func (f *upgradeFollow) listSessions(one *opsSection) string {
	f.t.Helper()
	command := one.Inline("tmux list-sessions")
	res := f.sh.Run(command)
	if res.Code != 0 && res.Stdout == "" && (strings.HasPrefix(res.Stderr, "no server running on ") || strings.HasPrefix(res.Stderr, "error connecting to ")) {
		return ""
	}
	if res.Code != 0 {
		f.t.Fatalf("%s: exit %d: %s", command, res.Code, res.Stderr)
	}
	return res.Stdout
}

// agentRuns is step 1's pane check for row id: its session's pane pid, from
// the tmux list-panes command, answers the ps -p command.
func (f *upgradeFollow) agentRuns(one *opsSection, id string) bool {
	f.t.Helper()
	panes := f.sh.Run(one.Inline("tmux list-panes"))
	if panes.Code != 0 {
		return false
	}
	for _, line := range strings.Split(panes.Stdout, "\n") {
		if name, pid, ok := strings.Cut(line, " "); ok && name == f.sessions[id] {
			ps := f.sh.Run(one.Fill(one.Inline("ps -p"), "<pid>", pid))
			for _, l := range strings.Split(ps.Stdout, "\n") {
				if fs := strings.Fields(l); ps.Code == 0 && len(fs) > 0 && fs[0] == pid {
					return true
				}
			}
			return false
		}
	}
	return false
}

func (f *upgradeFollow) stepOne() *opsSection {
	f.t.Helper()
	s := docSection(f.t, upgradeSkill, "1. Stop the agents")
	s.Pin("The upgrade runs only from a shell or agent that agent-director did not start. agent-director sets",
		"for every Claude, Codex and Cursor agent it starts:",
		"If it prints `started by agent-director`, stop here and hand the upgrade to the user or to a session agent-director did not start, saying why: your own row is live, so the install refuses, and your own hooks would run the new binary mid-upgrade. Do the same if this session has agent-director's MCP tools: their `agent-director serve` must stop too.",
		"List the live rows:",
		"If it prints `"+upgradeNoRows+"`, skip the checks below and ask the operator. Otherwise, while the agents still run, check that this shell is in their environment. Each must hold:",
		"- `id -un` prints the owner that `ls -ld ~/.config/agent-director` shows, and not `root`.",
		"- `tmux list-sessions` shows the `tmux_session_name` of each live row whose agent still runs. A live row whose session is not there is one for step 2's sweep to clear, not a failed check. If no live row's session is there, ask the operator whether any of those agents still runs: if one does, this check fails.",
		"- For a live row whose session is there, `tmux list-panes -a -F '#{session_name} #{pane_pid}'` gives its pid, and `ps -p <pid>` prints that process.",
		"If one fails, this shell is another user's, root's, or on another container, tmux server or PID namespace, where a sweep can mark running agents `missing`",
		"Move to the agents' own shell and tmux environment and start step 1 again.",
		"Ask the operator to stop whatever starts agents (a scheduler, a loop, a caller), every `agent-director serve` (each session with agent-director's MCP tools) and every caller, as",
		"says, and to confirm that you may stop each live row listed. If they say no, stop here and change nothing. Once they confirm, check that no `serve` runs: this prints nothing.",
		"With no live row, go to step 3. Otherwise stop each one, keeping its id for step 6: `pause` a `waiting` row; `kill` any other live row, or one `pause` refuses.")
	return s
}

// stepOneStops is step 1's stop block: its pause line, then its kill line.
func (f *upgradeFollow) stepOneStops(s *opsSection) (pause, kill string) {
	f.t.Helper()
	stops := strings.Split(s.Block("pause --instance-id"), "\n")
	if len(stops) != 2 || !strings.HasSuffix(stops[0], " pause --instance-id <id>") || !strings.HasSuffix(stops[1], " kill --instance-id <id>") {
		f.t.Fatalf("step 1's stop block is no longer pause, then kill: %q", stops)
	}
	return stops[0], stops[1]
}

// step1 follows "1. Stop the agents".
func (f *upgradeFollow) step1() step1Next {
	t := f.t
	t.Helper()
	f.steps1++
	s := f.stepOne()
	switch got := f.sh.Run(s.Block("AGENT_DIRECTOR_INSTANCE_ID")); {
	case got.Code != 0 || got.Stderr != "":
		t.Fatalf("step 1's check: exit %d, %q", got.Code, got.Stderr)
	case got.Stdout == "started by agent-director\n":
		return stopHere
	case got.Stdout != "":
		t.Fatalf("step 1's check printed %q", got.Stdout)
	}
	// This shell has no MCP tools.
	rows, none := f.list(s.Block("agent-director list --state"))
	if !none {
		user := strings.TrimSpace(f.must(s.Inline("id -un")).Stdout)
		owner := strings.Fields(f.must(s.Inline("ls -ld ~/.config/agent-director")).Stdout)
		if user == "" || user == "root" || len(owner) < 3 || owner[2] != user {
			t.Fatalf("id -un %q, ls -ld %q: not the store's owner, or root", user, owner)
		}
		sessions, shown, failed := "\n"+f.listSessions(s), 0, false
		for _, r := range rows {
			if !strings.Contains(sessions, "\n"+r.Session+":") {
				continue // for step 2's sweep
			}
			shown++
			failed = failed || !f.agentRuns(s, r.ID)
		}
		if shown == 0 && f.ask(askStillRuns) {
			failed = true
		}
		if failed {
			if f.move == nil {
				t.Fatalf("step 1's place checks fail in the agents' own tmux environment; rows %+v, sessions:%s", rows, sessions)
			}
			f.move()
			f.move = nil
			return again
		}
	}
	if !f.ask(askStop) {
		return stopHere
	}
	// The operator has stopped them: none runs here.
	serve := s.Block("[a]gent-director serve")
	if got := f.sh.Run(serve); got.Stdout != "" || got.Stderr != "" {
		t.Fatalf("%s: a serve runs: %q, %q", serve, got.Stdout, got.Stderr)
	}
	if none {
		return toStep3
	}
	pause, kill := f.stepOneStops(s)
	for _, r := range rows {
		if r.State == "waiting" && f.sh.Run(s.Fill(pause, "<id>", r.ID)).Code == 0 {
			f.stopped[r.ID] = true
			continue
		}
		f.must(s.Fill(kill, "<id>", r.ID))
		f.stopped[r.ID] = true
	}
	if f.afterStop != nil {
		f.afterStop()
		f.afterStop = nil
	}
	return toStep2
}

// step2Lines is step 2's block: find-missing, then list.
func (f *upgradeFollow) step2Lines() (findMissing, list string) {
	t := f.t
	t.Helper()
	s := docSection(t, upgradeSkill, "2. Sweep")
	s.Pin("From the same shell, run the installed release's `find-missing`, then list again:",
		"`find-missing`'s `ids` names the rows it marked `missing`; a row `kill` ended stays live until then. Repeat both about 5 s apart, at most three times, until `list` prints `"+upgradeNoRows+"`. Go to step 3 only then.",
		"A row still live goes back to step 1: either its agent still runs (step 1 stops it, the operator confirming), or the sweep ran in the wrong place (step 1's checks find it; then sweep from the agents' environment). Never mark, delete or edit rows by hand.")
	lines := strings.Split(s.Block("find-missing"), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], " find-missing") || !strings.Contains(lines[1], " list --state ") {
		t.Fatalf("step 2's block is no longer find-missing, then list: %q", lines)
	}
	return lines[0], lines[1]
}

// step2 follows "2. Sweep": true leads to step 3, false back to step 1.
func (f *upgradeFollow) step2() bool {
	f.t.Helper()
	findMissing, list := f.step2Lines()
	for try := 1; try <= 3; try++ {
		f.sweep(findMissing)
		if _, none := f.list(list); none {
			return true
		}
		if try < 3 {
			time.Sleep(upgradeSweepPause)
		}
	}
	return false
}

// step3 follows "3. Install" and returns the install's result.
func (f *upgradeFollow) step3() moveResult {
	t, h := f.t, f.h
	t.Helper()
	s := docSection(t, upgradeSkill, "3. Install")
	s.Pin("The journal flags assert the operator's maintenance window: first confirm with the operator that steps 1 and 2 of",
		"hold. Then, from this shell:",
		"Add `--journal-legacy-root <dir>` for each additional input root, and the original install's other",
		"Exit 0: step 4. Exit 5 or 6: step 5. Another exit:")
	if !f.ask(askWindow) {
		t.Fatal("no case here has the operator deny the maintenance window")
	}
	block := s.Block("install.sh | bash -s --")
	networkInstall := upgradeNetworkInstall(t, block)
	f.backup = f.c.newBackup()
	pairs := []string{"<new-absolute-directory>", moveQuote(f.backup)}
	if strings.Contains(networkInstall, "<tag>") {
		pairs = append(pairs, "<tag>", "v"+moveVersion)
	}
	block = s.Fill(block, pairs...)
	networkInstall = upgradeNetworkInstall(t, block)
	// The one substitution beyond the placeholders: jobs have no network, so
	// curl's fetch of install.sh becomes cat of the fixture's loose copy of
	// this tree's install.sh, still piped to bash -s --, and --from-release
	// becomes this tree's pair (the candidate binaries), as J43 to J45
	// install, under the test switches every h install runs with (no host
	// crontab or systemctl). h.Flags are the original install's other flags,
	// the ones the v0.17.3 install used; no extra input root needs
	// --journal-legacy-root.
	words := []string{"cat", moveQuote(h.S.Install), "|"}
	for _, e := range h.command().Env {
		if k, v, _ := strings.Cut(e, "="); strings.HasPrefix(k, "INSTALL_SH_TEST_") {
			words = append(words, k+"="+moveQuote(v))
		}
	}
	words = append(words, "bash", "-s", "--", "--binary", moveQuote(h.Pair.Main), "--admin-binary", moveQuote(h.Pair.Admin))
	for _, flag := range h.Flags {
		words = append(words, moveQuote(flag))
	}
	rest := strings.TrimPrefix(block, networkInstall)
	res := f.sh.RunWithin(strings.Join(words, " ")+" "+rest, upgradeInstallWait)
	f.installs = append(f.installs, res)
	return res
}

// step4 follows "4. Verify".
func (f *upgradeFollow) step4() {
	t := f.t
	t.Helper()
	s := docSection(t, upgradeSkill, "4. Verify")
	s.Pin("`version` shows the release just installed, and `list` exits 0. Then prepare the administrator's",
		"filled in with the store's path (`[store] db_path`'s, when set), a new backup directory and step 3's `--journal-legacy-root` list as `--legacy-root`:",
		"`agent-director-admin` runs only with a human's approval for that run",
		"show the operator the filled-in command, and run it only once they approve that run; otherwise they run it. It must exit 0 with `\"authority\": \"sqlite\"`. An error goes to step 5.")
	lines := strings.Split(s.Block("agent-director version"), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], " version") || !strings.HasSuffix(lines[1], " list") {
		t.Fatalf("step 4's first block is no longer version, then list: %q", lines)
	}
	if got := f.must(lines[0]); !strings.Contains(got.Stdout, moveVersion) {
		t.Errorf("version %q is not the release just installed, %s", got.Stdout, moveVersion)
	}
	f.must(lines[1])
	verify := s.Fill(s.Block("migrate-journal"), "<new-absolute-directory>", moveQuote(f.c.newBackup()))
	if !f.ask(askVerify) {
		t.Fatal("no case here has the operator run the verification")
	}
	res := f.sh.RunWithin(verify, upgradeInstallWait)
	var answer struct {
		Authority string `json:"authority"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &answer); res.Code != 0 || err != nil || answer.Authority != "sqlite" {
		t.Fatalf("verification: exit %d, %s, %s (%v)", res.Code, res.Stdout, res.Stderr, err)
	}
}

// step5 follows "5. On a refusal" for step 3's result res (the home as in
// before it ran) and returns the step it leads to: 4 for exit 6, else 1
// (agents_live, through steps 1 and 2 to step 3).
func (f *upgradeFollow) step5(res moveResult, before moveTree) int {
	t := f.t
	t.Helper()
	s := docSection(t, upgradeSkill, "5. On a refusal")
	s.Pin("`install.sh`'s last stderr line names the cause, `install.sh: err_name=<Name>`; `agent-director-admin`'s JSON envelope names it as `err_name`. The envelope gives `details.refusal.reason` and `action`. Find that reason's row in",
		"Show the operator the output (the envelope, when there is one) and that row: the operator performs its fix, up to its re-run of the install or its verification, which stay yours. Then resume at that step: step 3 to re-run the install, or step 4 for the verification, under that step's rules.",
		"For `agents_live` or `ErrAgentsLive`, instead, the row's stops are step 1's and its `find-missing` is step 2's: do those steps, with step 1's checks and the operator's confirmation, then step 3.",
		"On exit 6, the install finished but the MCP registration did not: tell the operator so, hand them its stderr to register by hand, as",
		"says, then go on with step 4 and step 6.",
		"For a row that leads to neither step, or an `err_name` or reason with no row, stop and hand the operator the output.")
	if res.Code == 6 {
		t.Logf("exit 6, handed to the operator to register by hand:\n%s", res.Stderr)
		return 4
	}
	cause := strings.TrimPrefix(res.Last(), "install.sh: err_name=")
	env := opsLastEnvelope(t, res, cause)
	if reason, _ := env.Details.Refusal["reason"].(string); res.Code != 5 || reason != "agents_live" {
		t.Fatalf("exit %d, %s, reason %q: no case here follows another reason's row", res.Code, cause, reason)
	}
	opsDocSection(t, "Upgrade refusal reasons").Pin("| `agents_live` | With no move due, the store holds rows in a live state (`pending`, `waiting`, `working`, `check_permission`, `ask_user`). The message lists each row's state and instance id, with the command that stops its agent. | No | Stop each listed agent with the command under it, clear the row of an agent you have established is gone with `find-missing` (as the agents' user), then re-run the same install command from a shell agent-director did not start")
	opsDocSection(t, "`install.sh` refuses with `ErrJournalMigrationRefused`").Pin(
		"**Live agents** (`agents_live`, with no move due). The message lists each row in a live state, and under it the command that stops its agent, run with the installed binary, which is still the previous release's:",
		"Stop whatever starts agents (a scheduler, a loop, a caller) first. A row whose agent you have established is gone stays live until `~/.config/agent-director/bin/agent-director find-missing` clears it; run it as the agents' user, in their tmux environment.")
	// The listed rows and the command under each, as the message gives them.
	var rows []liveRow
	var stops []string
	in := false
	for _, line := range strings.Split(res.Stderr, "\n") {
		switch {
		case strings.HasPrefix(line, "  Rows in a live state "):
			in = true
		case in && strings.HasPrefix(line, "      "):
			stops = append(stops, strings.TrimSpace(line))
		case in && strings.HasPrefix(line, "    "):
			state, id, _ := strings.Cut(strings.TrimSpace(line), " ")
			rows = append(rows, liveRow{id, state})
		default:
			in = false
		}
	}
	want, findMissing := f.c.assertLiveRefused(t, res, rows, before)
	if !reflect.DeepEqual(stops, want) {
		t.Fatalf("the commands under the rows %q; the refusal gives %q", stops, want)
	}
	// The row's commands are step 1's stops and step 2's find-missing, with
	// ~ expanded, so going by way of steps 1 and 2 runs them.
	home := func(command string) string { return strings.ReplaceAll(command, "~/", f.h.Home+"/") }
	pause, kill := f.stepOneStops(f.stepOne())
	for i, r := range rows {
		stop := kill
		if r.state == "waiting" {
			stop = pause
		}
		if got := home(strings.ReplaceAll(stop, "<id>", r.id)); got != stops[i] {
			t.Errorf("step 1 stops %s with %q; the row gives %q", r.id, got, stops[i])
		}
	}
	if sweep, _ := f.step2Lines(); home(sweep) != findMissing {
		t.Errorf("step 2 sweeps with %q; the row gives %q", home(sweep), findMissing)
	}
	return 1
}

// run follows the skill from step 1 to step 4, or to a "stop here" (false).
func (f *upgradeFollow) run() bool {
	t := f.t
	t.Helper()
	for pass := 0; pass < 4; pass++ {
		switch f.step1() {
		case stopHere:
			return false
		case again:
			continue
		case toStep2:
			if !f.step2() {
				continue
			}
			if f.afterSweep != nil {
				f.afterSweep()
				f.afterSweep = nil
			}
		}
		before := f.h.PhysicalSnapshot()
		switch res := f.step3(); res.Code {
		case 0:
			f.step4()
			return true
		case 5, 6:
			if f.step5(res, before) == 4 {
				f.step4()
				return true
			}
		default:
			t.Fatalf("install: exit %d, which no case here follows\nstdout:\n%s\nstderr:\n%s", res.Code, res.Stdout, res.Stderr)
		}
	}
	t.Fatal("four passes through step 1 did not finish the upgrade")
	return false
}

// TestAdviceFollow_J47_UpgradeSkill: the upgrade skill followed step by
// step from a genuine v0.17.3 install. A crashed agent's row, its session
// gone, is stopped, swept missing by v0.17.3's find-missing, and the install
// and its verification pass. With no live row, the follow goes straight to
// the install. A shell on another tmux server fails step 1's place check,
// moves to the agents' server and passes. An agent a caller starts after
// the stops stays live through the sweep, so step 2 sends the follow back to
// step 1, which stops it. One started after the sweep makes the install
// refuse with agents_live; step 5 sends the follow through steps 1 and 2 to a
// passing install. An operator who says no, and a shell agent-director
// started, stop the follow with nothing changed.
func TestAdviceFollow_J47_UpgradeSkill(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and installs v0.17.3")
	}
	// setup is a v0.17.3 home whose store holds row-ended and, with dead set,
	// row-dead: live, its recorded agent gone and its session with it.
	setup := func(t *testing.T, dead bool) (*crossingCase, *j47Tmux) {
		t.Helper()
		c := newCrossingCase(t)
		x := newJ47Tmux(t, c.h)
		rows := crossingLiveRowSQL("row-ended", "ended")
		if dead {
			rows += crossingLiveRowSQL("row-dead", "working")
		}
		c.h.Sqlite(c.db, rows)
		return c, x
	}
	rowStates := func(c *crossingCase) string {
		return c.h.Sqlite(c.db, "SELECT instance_id, state FROM spawns ORDER BY instance_id;", "-readonly")
	}
	// yes is an operator who confirms, and knows of no agent still running.
	yes := func(q string) bool { return q != askStillRuns }
	// passed checks a follow that ended with one passing install.
	passed := func(t *testing.T, c *crossingCase, f *upgradeFollow, ok bool, installs int, asked []string, marked [][]string, rows string) {
		t.Helper()
		if !ok {
			t.Fatal("the follow stopped before the install")
		}
		if !reflect.DeepEqual(f.asked, asked) {
			t.Errorf("operator asked %q; want %q", f.asked, asked)
		}
		if !reflect.DeepEqual(f.marked, marked) {
			t.Errorf("find-missing marked %q; want %q", f.marked, marked)
		}
		if len(f.installs) != installs {
			t.Fatalf("%d installs; want %d", len(f.installs), installs)
		}
		c.assertCrossed(t, f.installs[len(f.installs)-1], f.backup)
		if got := rowStates(c); got != rows {
			t.Errorf("rows after the upgrade:\n%s\nwant:\n%s", got, rows)
		}
	}

	t.Run("gone agent swept", func(t *testing.T) {
		c, x := setup(t, true)
		f := newUpgradeFollow(t, c, x, true, yes)
		passed(t, c, f, f.run(), 1, []string{askStillRuns, askStop, askWindow, askVerify},
			[][]string{{"row-dead"}}, "row-dead|missing\nrow-ended|ended\n")
	})

	t.Run("no live row", func(t *testing.T) {
		c, x := setup(t, false)
		f := newUpgradeFollow(t, c, x, true, yes)
		passed(t, c, f, f.run(), 1, []string{askStop, askWindow, askVerify}, nil, "row-ended|ended\n")
	})

	t.Run("another tmux server", func(t *testing.T) {
		c, x := setup(t, true)
		x.liveAgent(t, c, "row-live")
		// The operator knows row-live's agent runs.
		f := newUpgradeFollow(t, c, x, false, func(string) bool { return true })
		passed(t, c, f, f.run(), 1, []string{askStillRuns, askStop, askWindow, askVerify},
			[][]string{{"row-dead", "row-live"}}, "row-dead|missing\nrow-ended|ended\nrow-live|missing\n")
		if f.steps1 != 2 {
			t.Errorf("step 1 ran %d times; want the failed check, then step 1 again", f.steps1)
		}
		if err := x.command("has-session", "-t", "=ad-row-live").Run(); err == nil {
			t.Error("row-live's session is still there after its kill")
		}
	})

	t.Run("agent still running after the stops", func(t *testing.T) {
		c, x := setup(t, true)
		f := newUpgradeFollow(t, c, x, true, yes)
		f.afterStop = func() { x.liveAgent(t, c, "row-live") }
		// Three sweeps leave row-live live, so step 1 again sees its session
		// and stops it, and the next sweep marks it.
		passed(t, c, f, f.run(), 1, []string{askStillRuns, askStop, askStop, askWindow, askVerify},
			[][]string{{"row-dead"}, {}, {}, {"row-live"}}, "row-dead|missing\nrow-ended|ended\nrow-live|missing\n")
		if err := x.command("has-session", "-t", "=ad-row-live").Run(); err == nil {
			t.Error("row-live's session is still there after its kill")
		}
	})

	t.Run("agent started after the sweep", func(t *testing.T) {
		c, x := setup(t, true)
		f := newUpgradeFollow(t, c, x, true, yes)
		f.afterSweep = func() { x.liveAgent(t, c, "row-live") }
		passed(t, c, f, f.run(), 2, []string{askStillRuns, askStop, askWindow, askStop, askWindow, askVerify},
			[][]string{{"row-dead"}, {"row-live"}}, "row-dead|missing\nrow-ended|ended\nrow-live|missing\n")
		if f.installs[0].Code != 5 || f.steps1 != 2 {
			t.Errorf("the first install exit %d, step 1 run %d times; want the refusal, then step 1 again", f.installs[0].Code, f.steps1)
		}
		if err := x.command("has-session", "-t", "=ad-row-live").Run(); err == nil {
			t.Error("row-live's session is still there after its kill")
		}
	})

	t.Run("operator says no", func(t *testing.T) {
		c, x := setup(t, true)
		before, rows := c.h.Snapshot(), rowStates(c)
		f := newUpgradeFollow(t, c, x, true, func(string) bool { return false })
		if f.run() {
			t.Fatal("the follow went on after the operator said no")
		}
		if want := []string{askStillRuns, askStop}; !reflect.DeepEqual(f.asked, want) || len(f.marked) != 0 || len(f.installs) != 0 {
			t.Errorf("asked %q, %d sweeps, %d installs; want %q and nothing run after it", f.asked, len(f.marked), len(f.installs), want)
		}
		if d := moveDiff(before, c.h.Snapshot()); d != "" {
			t.Errorf("the home changed:\n%s", d)
		}
		if got := rowStates(c); got != rows {
			t.Errorf("rows %q; want %q", got, rows)
		}
	})

	t.Run("started by agent-director", func(t *testing.T) {
		c, x := setup(t, true)
		before := c.h.PhysicalSnapshot()
		f := newUpgradeFollow(t, c, x, true, yes)
		f.must("export AGENT_DIRECTOR_INSTANCE_ID=parent-agent")
		if f.run() {
			t.Fatal("a shell agent-director started went on with the upgrade")
		}
		// The two exports and step 1's check, and nothing after it.
		if f.sh.n != 3 || len(f.asked) != 0 {
			t.Errorf("%d shell commands, operator asked %q; want only step 1's check", f.sh.n, f.asked)
		}
		if d := moveDiff(before, c.h.PhysicalSnapshot()); d != "" {
			t.Errorf("the home changed:\n%s", d)
		}
	})
}
