package installsh_test

// Literal follows of docs/operator-actions.md's procedures (SR-8.6
// "Installer": "Restores", "Failures of the restore itself" and "Advice"):
// J43, the v16 refusals' inspection and way back; J44, putting back a kept
// install; J45, the residual's way back. Each continues the outcome test
// that stops before the human step, from that test's own setup, and checks
// only what the doc's steps do. The doc-procedure follower (reusable test
// fixture) is the one reader of these procedures: opsDocSection cuts a
// section and pins its key phrases, opsShell runs its commands with the real
// tools only, and putBack follows "Putting back a kept install".
//
// Nothing here is t.Parallel: put-back check 1 sees every process in the
// container, so no temp path or test name may hold the string
// agent-director-admin, and no administrator may run beside these tests.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// opsSection is one section of docs/operator-actions.md, from its heading to
// the next heading of the same or higher level outside a fence.
type opsSection struct {
	t          *testing.T
	name, text string
}

var opsHeading = regexp.MustCompile(`^(#{1,6}) (.+)$`)

// opsDocSection cuts the section whose heading text is heading.
func opsDocSection(t *testing.T, heading string) *opsSection {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "operator-actions.md"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	level, fenced := 0, false
	for _, line := range strings.Split(string(b), "\n") {
		fence := strings.HasPrefix(strings.TrimSpace(line), "```")
		if m := opsHeading.FindStringSubmatch(line); m != nil && !fenced && !fence {
			if level > 0 && len(m[1]) <= level {
				break
			}
			if level == 0 && m[2] == heading {
				level = len(m[1])
			}
		}
		if fence {
			fenced = !fenced
		}
		if level > 0 {
			out = append(out, line)
		}
	}
	if level == 0 {
		t.Fatalf("docs/operator-actions.md has no section %q", heading)
	}
	return &opsSection{t: t, name: heading, text: strings.Join(out, "\n")}
}

// Pin fails unless the section, whitespace-joined, says each phrase word for
// word: a change to the procedure turns its follow red until it is re-checked.
func (s *opsSection) Pin(phrases ...string) {
	s.t.Helper()
	flat := strings.Join(strings.Fields(s.text), " ")
	for _, p := range phrases {
		if !strings.Contains(flat, strings.Join(strings.Fields(p), " ")) {
			s.t.Fatalf("%q no longer says %q; re-check its literal follow", s.name, p)
		}
	}
}

// Block is the section's one fenced sh block holding want, dedented to its
// fence.
func (s *opsSection) Block(want string) string {
	s.t.Helper()
	var found, cur []string
	indent, in := "", false
	for _, line := range strings.Split(s.text, "\n") {
		switch trimmed := strings.TrimSpace(line); {
		case !in && trimmed == "```sh":
			in, cur, indent = true, nil, line[:len(line)-len(strings.TrimLeft(line, " "))]
		case in && trimmed == "```":
			in = false
			if block := strings.Join(cur, "\n"); strings.Contains(block, want) {
				found = append(found, block)
			}
		case in:
			cur = append(cur, strings.TrimPrefix(line, indent))
		}
	}
	if len(found) != 1 {
		s.t.Fatalf("%q: %d sh blocks hold %q; want exactly one", s.name, len(found), want)
	}
	return found[0]
}

// Inline is the section's one inline command starting with prefix.
func (s *opsSection) Inline(prefix string) string {
	s.t.Helper()
	spans := map[string]bool{}
	for _, m := range regexp.MustCompile("`("+regexp.QuoteMeta(prefix)+"[^`]*)`").FindAllStringSubmatch(s.text, -1) {
		spans[m[1]] = true
	}
	if len(spans) != 1 {
		s.t.Fatalf("%q: inline commands starting %q: %v; want exactly one", s.name, prefix, spans)
	}
	for span := range spans {
		return span
	}
	return ""
}

var opsPlaceholder = regexp.MustCompile(`<[a-z][a-z_. ]*>`)

// Fill replaces each of the doc's placeholders, spelt as the command spells
// it, with its value, and fails when one is absent or any is left.
func (s *opsSection) Fill(command string, pairs ...string) string {
	s.t.Helper()
	for i := 0; i < len(pairs); i += 2 {
		if !strings.Contains(command, pairs[i]) {
			s.t.Fatalf("%q: %q has no %s", s.name, command, pairs[i])
		}
		command = strings.ReplaceAll(command, pairs[i], pairs[i+1])
	}
	if left := opsPlaceholder.FindString(command); left != "" {
		s.t.Fatalf("%q: %q still holds the placeholder %s", s.name, command, left)
	}
	return command
}

// opsShell is the one bash a follower types a procedure's commands into: in
// HOME, with HOME, TMPDIR and the toolbox as PATH, so no case stand-in runs.
// Shell variables last from one command to the next. The shell leads its own
// process group, which Release ends if it outlives its input by moveWait.
type opsShell struct {
	h   *moveHome
	in  io.WriteCloser
	rc  chan string
	n   int
	cmd *exec.Cmd
}

func newOpsShell(h *moveHome) *opsShell {
	h.t.Helper()
	cmd := exec.Command(h.S.Bash, "--norc", "--noprofile")
	cmd.Dir = h.Home
	cmd.Env = []string{"HOME=" + h.Home, "PATH=" + h.S.Toolbox, "TMPDIR=" + h.Tmp}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	in, err := cmd.StdinPipe()
	if err != nil {
		h.t.Fatal(err)
	}
	// The read end is this fixture's, so Wait never closes it under the scanner.
	out, w, err := os.Pipe()
	if err != nil {
		h.t.Fatal(err)
	}
	cmd.Stdout = w
	err = cmd.Start()
	_ = w.Close()
	if err != nil {
		_ = out.Close()
		h.t.Fatal(err)
	}
	s := &opsShell{h: h, in: in, rc: make(chan string), cmd: cmd}
	quit := make(chan struct{})
	go func() {
		defer close(s.rc)
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			select {
			case s.rc <- sc.Text():
			case <-quit:
				return
			}
		}
	}()
	h.OnRelease(func() {
		close(quit)
		_ = in.Close()
		exited := make(chan struct{})
		go func() { _ = cmd.Wait(); close(exited) }()
		select {
		case <-exited:
		case <-time.After(moveWait):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-exited
		}
		_ = out.Close()
	})
	return s
}

// Run runs command in the shell and returns its status and output.
func (s *opsShell) Run(command string) moveResult {
	h := s.h
	h.t.Helper()
	s.n++
	out, errs := filepath.Join(h.Root, fmt.Sprintf("ops-%d.out", s.n)), filepath.Join(h.Root, fmt.Sprintf("ops-%d.err", s.n))
	if _, err := fmt.Fprintf(s.in, "{\n%s\n} >%s 2>%s </dev/null\nprintf '%%s\\n' \"$?\"\n", command, moveQuote(out), moveQuote(errs)); err != nil {
		h.t.Fatal(err)
	}
	var res moveResult
	select {
	case line, ok := <-s.rc:
		code, err := strconv.Atoi(line)
		if !ok || err != nil {
			h.t.Fatalf("the follower's shell ended running %q (%q)", command, line)
		}
		res.Code = code
	case <-time.After(moveWait):
		h.t.Fatalf("%q did not end within %v", command, moveWait)
	}
	for p, dst := range map[string]*string{out: &res.Stdout, errs: &res.Stderr} {
		b, err := os.ReadFile(p)
		if err != nil {
			h.t.Fatal(err)
		}
		*dst = string(b)
	}
	return res
}

// opsLastEnvelope is res's last envelope, as a user reads details from it.
func opsLastEnvelope(t *testing.T, res moveResult, cause string) cutoverEnvelope {
	t.Helper()
	envs := preflightEnvelopes(t, res, cause)
	var env cutoverEnvelope
	if err := json.Unmarshal([]byte(envs[len(envs)-1]), &env); err != nil {
		t.Fatal(err)
	}
	return env
}

// opsOriginalFlags is flags without the journal flags, which a release that
// predates the cutover does not take.
func opsOriginalFlags(flags []string) []string {
	var out []string
	for i := 0; i < len(flags); i++ {
		switch flags[i] {
		case "--journal-maintenance-window", "--journal-roots-complete":
		case "--journal-backup", "--journal-legacy-root":
			i++
		default:
			out = append(out, flags[i])
		}
	}
	return out
}

// opsStash keeps a copy of everything under h.Root and returns a func that
// puts it back at the same paths, with h's settings, so a follow of one way
// on leaves the outcome as it found it for the test's other way on.
func opsStash(t *testing.T, h *moveHome) func() {
	t.Helper()
	stash := h.Root + ".stash"
	if out, err := exec.Command(h.RealTool("cp"), "-a", "--", h.Root, stash).CombinedOutput(); err != nil {
		t.Fatalf("stash %s: %v: %s", h.Root, err, out)
	}
	s, pair, flags, cwd := h.S, h.Pair, append([]string(nil), h.Flags...), h.Cwd
	env := map[string]string{}
	for k, v := range h.Env {
		env[k] = v
	}
	return func() {
		if err := os.RemoveAll(h.Root); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(stash, h.Root); err != nil {
			t.Fatal(err)
		}
		h.S, h.Pair, h.Flags, h.Cwd, h.Env = s, pair, flags, cwd, env
	}
}

// opsOldTree is tree's entries under ~/.agent-director.
func opsOldTree(tree moveTree) moveTree {
	out := moveTree{}
	for p, v := range tree {
		if p == ".agent-director" || strings.HasPrefix(p, ".agent-director/") {
			out[p] = v
		}
	}
	return out
}

// putBack follows "Putting back a kept install" for one kept outcome, from
// the last envelope's details.copies, details.remove and
// details.attempt_store, in one shell.
type putBack struct {
	t   *testing.T
	h   *moveHome
	doc *opsSection
	sh  *opsShell
	res moveResult
	env cutoverEnvelope
}

func newPutBack(t *testing.T, h *moveHome, res moveResult, env cutoverEnvelope) *putBack {
	t.Helper()
	doc := opsDocSection(t, "Putting back a kept install")
	doc.Pin("This puts the previous release back from the copies a crossing upgrade kept: after a restore that could not finish (`ErrSchemaVerifyFailed`, the message `the restore could not finish; the install is partly restored`, and `details.copies`, whatever the reason: a relayed `migrate-journal` refusal keeps its own, and `restore_incomplete` is the reason only when nothing was relayed), after `ErrJournalMigrationIncomplete` with reason `outcome_unestablished`, or from the copies a killed run left.",
		"After an unfinished automatic restore, the previous install's store is unchanged, and so is an original store kept inside a pre-move directory. After an `outcome_unestablished` or killed run, the store may have changed: the checks below decide whether a put-back is possible. Putting binaries or directories back rolls back no step committed in the store the attempt ran on (`details.attempt_store`).",
		"If any check below fails, put nothing back and complete forward",
		"**The checks**, in this order, on `details.attempt_store`:",
		"1. No administrator holds the migration lock: no `agent-director-admin` process runs, so this prints nothing.",
		"2. The store has no native-input authority. Read it from a private copy",
		"Nothing printed, or an error naming `no such table: store_meta`, means no authority. A `sqlite-v1:` value means the cutover committed: complete forward. The `rm -rf` removes only the private copy.",
		"3. The copied release opens the store: its `list` exits 0. In place, run the `agent-director` entry's `copy`",
		"It comes last because it runs an old binary. After a move it is step 5 below.",
		"after the three checks: rename each `details.copies` entry onto its `belongs_at`, the `agent-director` binary last, then remove only the `details.remove` paths and each now-empty link-copy directory. Never copy over an installed file, and stop if a `belongs_at` is a directory or a link to one",
		"run every step in one shell, starting with `aside= occupant= nested=`:",
		"1. Run checks 1 and 2 on `details.attempt_store`.",
		"skip this step when the `set aside:` line or `details.attempt_store` names a `~/.config/agent-director.after-upgrade-<YYYYmmdd-HHMMSS>` directory, when `~/.config/agent-director` is absent, or when it is a link whose directory holds only `.move-incomplete`.",
		"If `~/.config/agent-director` is still there, the name was taken: run both lines again.",
		"If `~/.agent-director` exists (the restore stopped because it was occupied, or something recreated it), look at it (`ls -la ~/.agent-director`) and set it aside",
		"When `premove` lies inside `~/.agent-director` (the occupant appeared while the restore ran), first rename it out to the free name it ends in:",
		"If it does not exit 0, undo, in reverse order, only the renames this procedure made, then complete forward:",
		"Then rename every other `details.copies` entry onto its `belongs_at`, as in place, the managed-link copy in the `--symlink-dir` directory included",
		"Then remove only the `details.remove` paths and the empty link-copy directory.")
	// The entry paragraph decides whether a reader may follow the procedure
	// at all: a reported outcome qualifies by its error, message and kept
	// copies, or by its unestablished reason, never by any other reason.
	reason, _ := env.Details.Refusal["reason"].(string)
	unfinished := env.ErrName == "ErrSchemaVerifyFailed" &&
		strings.Contains(res.Stderr, "the restore could not finish; the install is partly restored") &&
		len(env.Details.Copies) > 0
	unestablished := env.ErrName == "ErrJournalMigrationIncomplete" && reason == "outcome_unestablished"
	if !unfinished && !unestablished {
		t.Fatalf("outcome %s, reason %q, %d copies does not meet the procedure's entry paragraph", env.ErrName, reason, len(env.Details.Copies))
	}
	return &putBack{t: t, h: h, doc: doc, sh: newOpsShell(h), res: res, env: env}
}

// putBackLink reports whether a details.copies entry is the managed link's,
// whose copy is the link inside its copy directory.
func putBackLink(copy string) bool {
	return restoreCopyPattern.MatchString(filepath.Base(filepath.Dir(copy)))
}

// Checks runs the doc's checks on details.attempt_store, each as its own
// command in the doc's order, stopping at the first that fails, and returns
// its number (0 when all ran and passed) with its result. Moved, it runs
// checks 1 and 2 only: there the doc's list is step 5, after the renames.
func (p *putBack) Checks(moved bool) (int, moveResult) {
	t, h := p.t, p.h
	t.Helper()
	res := p.sh.Run(p.doc.Block("ps -A -o pid= -o args= | grep '[a]gent-director-admin'"))
	if res.Stdout != "" || res.Stderr != "" {
		return 1, res
	}
	check2 := p.doc.Block("SELECT value FROM store_meta WHERE key = 'native_input_authority';")
	if strings.Contains(check2, "-readonly") {
		t.Errorf("check 2 opens its copy -readonly, which Apple's sqlite3 cannot:\n%s", check2)
	}
	store := p.env.Details.AttemptStore
	files, temps := rollbackStoreCopy(t, store), h.tempEntries()
	res = p.sh.Run(p.doc.Fill(check2, "'<details.attempt_store>'", moveQuote(store)))
	if !reflect.DeepEqual(rollbackStoreCopy(t, store), files) {
		t.Errorf("check 2 changed %s's files", store)
	}
	if got := h.tempEntries(); !reflect.DeepEqual(got, temps) {
		t.Errorf("check 2 left TMPDIR holding %v; it held %v", got, temps)
	}
	if res.Stdout != "" || (res.Stderr != "" && !strings.Contains(res.Stderr, "no such table: store_meta")) {
		return 2, res
	}
	if moved {
		return 0, res
	}
	var bin string
	for _, c := range p.env.Details.Copies {
		if !putBackLink(c.Copy) && filepath.Base(c.Belongs) == "agent-director" {
			bin = c.Copy
		}
	}
	if bin == "" {
		bin = filepath.Join(h.New, "bin", "agent-director")
	}
	if res = p.sh.Run(p.doc.Fill(p.doc.Block("'<copy>' list"), "'<copy>'", moveQuote(bin))); res.Code != 0 {
		return 3, res
	}
	return 0, res
}

// rename puts each entry back with the in-place block's mv, the
// agent-director binary last, then runs its rm for each details.remove path
// and its rmdir for each link-copy directory.
func (p *putBack) rename(entries []struct{ Copy, Belongs string }) {
	t := p.t
	t.Helper()
	lines := strings.Split(p.doc.Block("mv -f -- '<copy>' '<belongs_at>'"), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[1], "rm -- ") || !strings.HasPrefix(lines[2], "rmdir -- ") {
		t.Fatalf("the in-place block is no longer mv, rm, rmdir: %q", lines)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		last := func(c struct{ Copy, Belongs string }) bool {
			return !putBackLink(c.Copy) && filepath.Base(c.Belongs) == "agent-director"
		}
		return !last(entries[i]) && last(entries[j])
	})
	run := func(cmd string) {
		if res := p.sh.Run(cmd); res.Code != 0 || res.Stderr != "" {
			t.Fatalf("%s: exit %d: %s", cmd, res.Code, res.Stderr)
		}
	}
	for _, c := range entries {
		if fi, err := os.Stat(c.Belongs); err == nil && fi.IsDir() {
			t.Fatalf("%s is a directory or a link to one: the doc says stop", c.Belongs)
		}
		run(p.doc.Fill(lines[0], "'<copy>'", moveQuote(c.Copy), "'<belongs_at>'", moveQuote(c.Belongs)))
	}
	for _, r := range p.env.Details.Remove {
		run(p.doc.Fill(lines[1], "'<details.remove path>'", moveQuote(r)))
	}
	for _, c := range entries {
		if putBackLink(c.Copy) {
			run(p.doc.Fill(lines[2], "'<link copy directory>'", moveQuote(filepath.Dir(c.Copy))))
		}
	}
}

// copies is details.copies, those whose belongs_at is skip left out.
func (p *putBack) copies(skip string) []struct{ Copy, Belongs string } {
	var out []struct{ Copy, Belongs string }
	for _, c := range p.env.Details.Copies {
		if c.Belongs != skip {
			out = append(out, struct{ Copy, Belongs string }{c.Copy, c.Belongs})
		}
	}
	return out
}

// InPlace puts back every details.copies entry, after the three checks.
func (p *putBack) InPlace() {
	p.t.Helper()
	for _, c := range p.env.Details.Copies {
		if c.Belongs == p.h.Old {
			p.t.Fatalf("an entry belongs at %s: the in-place form does not apply", p.h.Old)
		}
	}
	p.rename(p.copies(""))
}

// Moved follows the moved-install form's six steps. beforeList, if set, runs
// between step 4's rename and step 5's list. It returns the check that
// failed (1 or 2), 5 when step 5's list failed and the undo ran, or 0 when
// the previous install is back, with that step's result.
func (p *putBack) Moved(beforeList func()) (int, moveResult) {
	t, h := p.t, p.h
	t.Helper()
	premoveCopy := ""
	for _, c := range p.env.Details.Copies {
		if c.Belongs == h.Old {
			premoveCopy = c.Copy
		}
	}
	if premoveCopy == "" {
		t.Fatalf("no details.copies entry belongs at %s: %+v", h.Old, p.env.Details.Copies)
	}
	must := func(cmd string) moveResult {
		res := p.sh.Run(cmd)
		if res.Code != 0 {
			t.Fatalf("%s: exit %d: %s", cmd, res.Code, res.Stderr)
		}
		return res
	}
	must(p.doc.Inline("aside= occupant= nested="))
	// Step 1.
	if n, res := p.Checks(true); n != 0 {
		return n, res
	}
	// Step 2.
	skip := strings.Contains(p.env.Details.AttemptStore, h.New+".after-upgrade-")
	for _, line := range strings.Split(p.res.Stderr, "\n") {
		if strings.HasPrefix(line, "  set aside: "+h.New+".after-upgrade-") {
			skip = true
		}
	}
	if fi, err := os.Lstat(h.New); os.IsNotExist(err) {
		skip = true
	} else if err != nil {
		t.Fatal(err)
	} else if fi.Mode()&os.ModeSymlink != 0 {
		entries, err := os.ReadDir(h.New)
		if err == nil && len(entries) == 1 && entries[0].Name() == ".move-incomplete" {
			skip = true
		}
	}
	if !skip {
		aside := p.doc.Block(`[ ! -e "$aside" ] && mv -- ~/.config/agent-director "$aside"`)
		for try := 0; ; try++ {
			p.sh.Run(aside)
			if _, err := os.Lstat(h.New); os.IsNotExist(err) {
				break
			} else if try == 2 {
				t.Fatalf("%s is still there after running step 2 three times", h.New)
			}
		}
	}
	// Step 3.
	must(p.doc.Fill(p.doc.Inline("premove='<copy>'"), "'<copy>'", moveQuote(premoveCopy)))
	if _, err := os.Lstat(h.Old); err == nil {
		must(p.doc.Inline("ls -la ~/.agent-director"))
		if strings.HasPrefix(premoveCopy, h.Old+"/") {
			must(p.doc.Block(`mv -- "$nested" "$premove"`))
		}
		must(p.doc.Block(`mv -- ~/.agent-director "$occupant"`))
	}
	// Step 4.
	must(p.doc.Block(`mv -- "$premove" ~/.agent-director`))
	if beforeList != nil {
		beforeList()
	}
	// Step 5.
	list := p.sh.Run(p.doc.Block("~/.agent-director/bin/agent-director list"))
	if list.Code != 0 {
		must(p.doc.Block(`if [ -n "$aside" ]; then mv -- "$aside" ~/.config/agent-director; fi`))
		return 5, list
	}
	// Step 6.
	must(p.doc.Block(`if [ -L "$aside" ]; then`))
	p.rename(p.copies(h.Old))
	return 0, list
}

// J43: "inspect state.db's store_meta table and its native_input_authority
// row, or restore state.db's pre-upgrade copy, as docs/operator-actions.md
// "Upgrading the native-input journal" says, then re-run this install".
func TestAdviceFollow_J43_V16StoreInspection(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and installs v0.17.3")
	}
	doc := opsDocSection(t, "A store at schema v16 or later that the upgrade cannot read")
	doc.Pin("Keep writers stopped and read the authority from a private copy, with `store` set to the message's `state.db` path",
		"The `rm -rf` removes only the private copy. A `sqlite-v1:` value means the authority is recorded: re-run the install. Anything else (nothing, or an error): restore the stopped-writer pre-upgrade copy of the store as [Going back to the previous release](#going-back-to-the-previous-release) describes, or get help. Never write `store_meta` by hand.")
	back := opsDocSection(t, "Going back to the previous release")
	back.Pin("Before upgrading, stop all writers and keep a copy of the actual store",
		"restore the matching pre-upgrade DB and input evidence at their original paths. Restore the DB with its companion files, removing a `-wal` or `-shm` that the copy lacks",
		"Install the matching previous release with its own `install.sh` and the original flags")
	inspect := doc.Block("SELECT value FROM store_meta WHERE key = 'native_input_authority';")
	if strings.Contains(inspect, "-readonly") {
		t.Errorf("the inspection opens its copy -readonly, which Apple's sqlite3 cannot:\n%s", inspect)
	}
	for _, tc := range crossingV16Rows {
		t.Run(tc.reason, func(t *testing.T) {
			c, kept := crossingV16Refused(t, tc)
			h := c.h
			files, temps := rollbackStoreCopy(t, c.db), h.tempEntries()
			got := newOpsShell(h).Run(doc.Fill(inspect, "'<state.db path>'", moveQuote(c.db)))
			if !reflect.DeepEqual(rollbackStoreCopy(t, c.db), files) {
				t.Error("the inspection changed the store's files")
			}
			if now := h.tempEntries(); !reflect.DeepEqual(now, temps) {
				t.Errorf("the inspection left TMPDIR holding %v; it held %v", now, temps)
			}
			wantErr := ""
			if tc.reason == "cutover_state_unreadable" {
				wantErr = "no such table: store_meta"
			}
			if got.Stdout != "" || (wantErr == "") != (got.Stderr == "") || !strings.Contains(got.Stderr, wantErr) {
				t.Fatalf("inspection: stdout %q, stderr %q; want no value and the error %q", got.Stdout, got.Stderr, wantErr)
			}
			// Anything but a sqlite-v1: value: restore the pre-upgrade copy and
			// install the previous release, as "Going back" says in place.
			rollbackRestoreStore(t, h, c.db, kept)
			h.InstallRelease(c.release)
			if v := moveUserVersion(t, h, c.db); v != "15" {
				t.Errorf("restored store at v%s; want v15", v)
			}
		})
	}
}

// J44: "Put the copies listed in details.copies back with mv, as
// docs/operator-actions.md "Putting back a kept install" describes."
func TestAdviceFollow_J44_PutBackKeptInstall(t *testing.T) {
	// assertMovedBack checks the previous install is back after the moved
	// form (oldTreeBack compares the pre-move tree before step 5): the new
	// directory set aside and the managed link at the old binary again, with
	// no copy left.
	assertMovedBack := func(t *testing.T, h *moveHome) {
		t.Helper()
		if _, err := os.Lstat(h.New); !os.IsNotExist(err) {
			t.Errorf("%s after the put-back: %v", h.New, err)
		}
		if aside, _ := filepath.Glob(h.New + ".after-upgrade-*"); len(aside) != 1 {
			t.Errorf("set-aside new directories: %v; want one", aside)
		}
		if kept, _ := filepath.Glob(h.Old + ".pre-move-*"); len(kept) != 0 {
			t.Errorf("pre-move directories left: %v", kept)
		}
		link := filepath.Join(h.Home, "links", "agent-director")
		if got := restoreDescribe(t, link); got != "link\t"+filepath.Join(h.Old, "bin", "agent-director") {
			t.Errorf("managed link: %s", got)
		}
		if got := restoreCopyNames(t, h); len(got) != 0 {
			t.Errorf("copies left: %v", got)
		}
	}
	oldTreeBack := func(t *testing.T, h *moveHome, before moveTree) func() {
		return func() {
			if d := moveDiff(opsOldTree(before), opsOldTree(h.Snapshot())); d != "" {
				t.Errorf("~/.agent-director is not the pre-move directory:\n%s", d)
			}
		}
	}

	t.Run("occupied old path", func(t *testing.T) {
		h, res, env, before := restoreOutcomeIncomplete(t, false)
		// The relayed refusal keeps its own reason; the entry paragraph still
		// admits the outcome by its error, message and copies.
		if got := env.Details.Refusal["reason"]; got != "old_or_unverified_mutators" {
			t.Fatalf("reason %v; want the relayed old_or_unverified_mutators", got)
		}
		pb := newPutBack(t, h, res, env)
		if n, list := pb.Moved(oldTreeBack(t, h, before)); n != 0 {
			t.Fatalf("put-back stopped at step %d: exit %d: %s%s", n, list.Code, list.Stdout, list.Stderr)
		}
		assertMovedBack(t, h)
		occupant, _ := filepath.Glob(h.Old + ".recreated-*")
		if len(occupant) != 1 {
			t.Fatalf("set-aside occupants: %v; want one", occupant)
		}
		if b, err := os.ReadFile(filepath.Join(occupant[0], "occupant")); err != nil || string(b) != "occupant\n" {
			t.Errorf("occupant: %q, %v", b, err)
		}
	})

	for _, tc := range []struct {
		name    string
		outside bool
	}{{"unestablished move, store inside", false}, {"unestablished move, store outside", true}} {
		t.Run(tc.name, func(t *testing.T) {
			var opts []moveOldOpt
			if tc.outside {
				store := func(h *moveHome) string { return filepath.Join(h.Home, "elsewhere", "state.db") }
				opts = append(opts, withMoveStore(store), withMoveConfig(func(h *moveHome) string {
					return moveOldConfig + "db_path = " + strconv.Quote(store(h)) + "\n"
				}))
			}
			h := newGenuineOldHome(t, opts...)
			linksInHome(h)
			f := newCutoverFixture(t, h)
			f.Choose("child:exit-after-step")
			before := h.Snapshot()
			res := f.Install(false, false)
			env := opsLastEnvelope(t, res, "ErrJournalMigrationIncomplete")
			if env.Details.Refusal["reason"] != "outcome_unestablished" {
				t.Fatalf("outcome %v; want outcome_unestablished", env.Details.Refusal)
			}
			after := h.PhysicalSnapshot()
			pb := newPutBack(t, h, res, env)
			if !tc.outside {
				rel, err := filepath.Rel(h.New, env.Details.AttemptStore)
				if err != nil || strings.HasPrefix(rel, "..") {
					t.Fatalf("attempt store %s is not inside %s", env.Details.AttemptStore, h.New)
				}
				version := h.Sqlite(env.Details.AttemptStore, "PRAGMA user_version;", "-readonly")
				if n, list := pb.Moved(oldTreeBack(t, h, before)); n != 0 {
					t.Fatalf("put-back stopped at step %d: exit %d: %s%s", n, list.Code, list.Stdout, list.Stderr)
				}
				assertMovedBack(t, h)
				// Putting the install back does not revert the set-aside
				// store's committed steps.
				aside, _ := filepath.Glob(h.New + ".after-upgrade-*")
				if len(aside) == 1 {
					if got := h.Sqlite(filepath.Join(aside[0], rel), "PRAGMA user_version;", "-readonly"); got != version {
						t.Errorf("set-aside store version %q; want the attempt's %q", got, version)
					}
				}
				return
			}
			// The outside store's own earlier steps committed, so the
			// previous release's list fails and the undo leaves the new
			// release as the run left it. That list still checkpoints the
			// store's -wal as it closes, so the store is compared by content.
			store := env.Details.AttemptStore
			content := h.Sqlite(store, "PRAGMA user_version;\n.dump\n", "-readonly")
			if n, list := pb.Moved(nil); n != 5 {
				t.Fatalf("put-back ended at step %d (exit %d); want step 5's list to fail and the undo to run", n, list.Code)
			}
			if got := h.Sqlite(store, "PRAGMA user_version;\n.dump\n", "-readonly"); got != content {
				t.Error("the undone put-back changed the store's content")
			}
			now := h.PhysicalSnapshot()
			rel, err := filepath.Rel(h.Home, store)
			if err != nil {
				t.Fatal(err)
			}
			for _, tr := range []moveTree{after, now} {
				for _, dir := range []string{".", ".config", filepath.Dir(rel)} {
					tr[dir] = physicalTimes.ReplaceAllString(tr[dir], " ")
				}
				for _, suffix := range []string{"", "-wal", "-shm"} {
					delete(tr, rel+suffix)
				}
			}
			if d := moveDiff(after, now); d != "" {
				t.Errorf("the undone put-back changed the home:\n%s", d)
			}
		})
	}

	t.Run("kept copies over a committed cutover", func(t *testing.T) {
		c, _ := newCopiesCrossing(t)
		h := c.h
		h.Flags = append(h.Flags, c.valid()...)
		f := newCutoverFixture(t, h)
		f.Choose("child:exit-after-cutover")
		res := f.Install(false, false)
		env := opsLastEnvelope(t, res, "ErrJournalMigrationIncomplete")
		if env.Details.Refusal["reason"] != "outcome_unestablished" || len(env.Details.Copies) == 0 {
			t.Fatalf("outcome %+v; want outcome_unestablished with copies kept", env.Details)
		}
		store := env.Details.AttemptStore
		if fi, err := os.Stat(store + "-wal"); err != nil || fi.Size() == 0 {
			t.Fatalf("%s-wal is absent or empty (%v): check 2 would not need to read it", store, err)
		}
		copies := map[string]string{}
		for _, k := range env.Details.Copies {
			copies[k.Copy] = restoreDescribe(t, k.Copy)
		}
		files := rollbackStoreCopy(t, store)
		pb := newPutBack(t, h, res, env)
		// Check 1 sees an administrator, then nothing once it has stopped.
		busy := filepath.Join(h.Root, "busy", "agent-director-admin")
		h.Copy(h.RealTool("sleep"), busy, 0o755)
		admin := exec.Command(busy, "300")
		if err := admin.Start(); err != nil {
			t.Fatal(err)
		}
		stop := func() { _ = admin.Process.Kill(); _ = admin.Wait() }
		h.OnRelease(stop)
		if n, got := pb.Checks(false); n != 1 || !strings.Contains(got.Stdout, busy) {
			t.Fatalf("check %d failed (%q) with %s running; want check 1, naming it", n, got.Stdout, busy)
		}
		stop()
		n, got := pb.Checks(false)
		if n != 2 || !strings.HasPrefix(got.Stdout, "sqlite-v1:") {
			t.Fatalf("check %d failed: %q, %q; want check 2 to print the sqlite-v1: authority", n, got.Stdout, got.Stderr)
		}
		// Complete forward: nothing is put back, and check 3 never ran.
		for _, pair := range [][2]string{{h.S.Current.Main, filepath.Join(h.New, "bin", "agent-director")}, {h.S.ScheduleProbe.Main, filepath.Join(h.New, "admin", "agent-director-admin")}} {
			if restoreDescribe(t, pair[0]) != restoreDescribe(t, pair[1]) {
				t.Errorf("this release is not installed at %s", pair[1])
			}
		}
		for copy, desc := range copies {
			if restoreDescribe(t, copy) != desc {
				t.Errorf("kept copy changed: %s", copy)
			}
		}
		if !reflect.DeepEqual(rollbackStoreCopy(t, store), files) {
			t.Error("the checks changed the store's files")
		}
	})

	t.Run("v0.17.3 unestablished, copies put back in place", func(t *testing.T) {
		row := forwardOutcomeCase{name: "v0.17.3 unestablished", unestablished: true, cutover: "unknown", reason: "outcome_unestablished"}
		forwardOutcome(t, row, func(f *cutoverFixture, res moveResult, env cutoverEnvelope, db string) {
			h := f.h
			defer opsStash(t, h)()
			r := genuineRelease(t, "v0.17.3")
			want := map[string]string{}
			for _, c := range env.Details.Copies {
				want[c.Belongs] = restoreDescribe(t, c.Copy)
			}
			pb := newPutBack(t, h, res, env)
			if n, got := pb.Checks(false); n != 0 {
				t.Fatalf("check %d failed: exit %d: %s%s", n, got.Code, got.Stdout, got.Stderr)
			}
			pb.InPlace()
			for _, pair := range [][2]string{{r.Pair.Main, filepath.Join(h.New, "bin", "agent-director")}, {r.Pair.Admin, filepath.Join(h.New, "admin", "agent-director-admin")}} {
				if restoreDescribe(t, pair[0]) != restoreDescribe(t, pair[1]) {
					t.Errorf("v0.17.3 is not back at %s", pair[1])
				}
			}
			for belongs, desc := range want {
				if restoreDescribe(t, belongs) != desc {
					t.Errorf("%s is not as before the run", belongs)
				}
			}
			if got := restoreCopyNames(t, h); len(got) != 0 {
				t.Errorf("copies left: %v", got)
			}
			if list := h.Run(filepath.Join(h.New, "bin", "agent-director"), "list"); list.Code != 0 {
				t.Errorf("v0.17.3's list: exit %d: %s", list.Code, list.Stderr)
			}
		})
	})
}

// J45: "To go back instead, restore the store from the journal backup
// directory ... (filled before any schema step), and install the previous
// release with its own installer, as docs/operator-actions.md "Going back to
// the previous release" describes."
func TestAdviceFollow_J45_ResidualWayBack(t *testing.T) {
	backupNamed := regexp.MustCompile(`restore the store from the journal backup directory (\S+) \(filled before any schema step\)`)
	// restore follows "The way back" in sh: from the journal backup
	// directory the message names, after checking manifest.json maps as the
	// doc says.
	restore := func(t *testing.T, h *moveHome, sh *opsShell, res moveResult, db string) {
		t.Helper()
		doc := opsDocSection(t, "`install.sh` stops with `ErrJournalMigrationIncomplete`")
		doc.Pin("The way back restores the store from the journal backup directory the message names, which `migrate-journal` filled before any schema step. Keep writers stopped. `COMPLETE` must exist, and `manifest.json`'s `Database` names the store; `database`, `database-wal` and `database-shm` are its copies:",
			"The legacy input roots are unchanged, because no cutover committed; the backup's `legacy-<k>/` holds the copy of `manifest.json`'s `Roots[k]`.",
			"after an old-layout move, follow its numbered steps, where this restore takes the place of step 4.")
		m := backupNamed.FindStringSubmatch(res.Stderr)
		if m == nil {
			t.Fatalf("the message names no journal backup directory:\n%s", res.Stderr)
		}
		backup := m[1]
		var manifest struct {
			Database string
			Roots    []string
		}
		b, err := os.ReadFile(filepath.Join(backup, "manifest.json"))
		if err != nil || json.Unmarshal(b, &manifest) != nil {
			t.Fatalf("manifest.json: %q, %v", b, err)
		}
		if manifest.Database != db {
			t.Errorf("manifest.json's Database %q; want the store %s", manifest.Database, db)
		}
		for k, root := range manifest.Roots {
			if _, err := os.Lstat(root); err != nil {
				continue
			}
			if fi, err := os.Stat(filepath.Join(backup, fmt.Sprintf("legacy-%d", k))); err != nil || !fi.IsDir() {
				t.Errorf("Roots[%d] %s has no legacy-%d/ copy: %v", k, root, k, err)
			}
		}
		got := sh.Run(doc.Fill(doc.Block("jq -r .Database"), "'<journal backup directory>'", moveQuote(backup)))
		if got.Code != 0 || got.Stdout != filepath.Join(backup, "COMPLETE")+"\n" || got.Stderr != "" {
			t.Fatalf("the way back's restore: exit %d, %q, %q", got.Code, got.Stdout, got.Stderr)
		}
	}
	// installOld installs r with its own install.sh and the original flags,
	// from the directory the upgrade ran in.
	installOld := func(t *testing.T, h *moveHome, r releaseBuild) {
		t.Helper()
		s, pair, flags := h.S, h.Pair, h.Flags
		old := *h.S
		old.Install = r.Install
		h.S, h.Pair, h.Flags = &old, r.Pair, opsOriginalFlags(h.Flags)
		moveMustInstall(t, h)
		h.S, h.Pair, h.Flags = s, pair, flags
	}

	t.Run("v0.16.0 in place", func(t *testing.T) {
		row := forwardOutcomeCase{name: "J39 v0.16.0 residual", residual: true, mode: "child:verify-fails-2", cutover: "not_committed", reason: "old_or_unverified_mutators"}
		forwardOutcome(t, row, func(f *cutoverFixture, res moveResult, env cutoverEnvelope, db string) {
			h := f.h
			defer opsStash(t, h)()
			back := opsDocSection(t, "Going back to the previous release")
			back.Pin("Restore the DB with its companion files, removing a `-wal` or `-shm` that the copy lacks",
				"Install the matching previous release with its own `install.sh` and the original flags")
			restore(t, h, newOpsShell(h), res, db)
			installOld(t, h, genuineRelease(t, "v0.16.0"))
			if list := h.Run(filepath.Join(h.New, "bin", "agent-director"), "list"); list.Code != 0 {
				t.Errorf("v0.16.0's list: exit %d: %s", list.Code, list.Stderr)
			}
			if v := moveUserVersion(t, h, db); v != "14" {
				t.Errorf("restored store at v%s; want v14", v)
			}
		})
	})

	t.Run("v0.11.1 moved, store outside", func(t *testing.T) {
		row := forwardOutcomeCase{name: "J39 v0.11.1 moved script note", residual: true, moved: true, rebase: true, movedScript: true, mode: "child:verify-fails-2", cutover: "not_committed", reason: "old_or_unverified_mutators"}
		forwardOutcome(t, row, func(f *cutoverFixture, res moveResult, env cutoverEnvelope, db string) {
			h := f.h
			back := opsDocSection(t, "Going back to the previous release")
			back.Pin("2. Set the new directory aside:",
				"3. If `~/.agent-director` exists again (something recreated it since the upgrade), set it aside too. Then rename the newest kept directory back.",
				"Run the first command only when the old path exists. The newest name is the one with the latest timestamp.",
				"7. From the same directory, install the previous release with that release's own `install.sh`, fetched from its tag, with the flags the original install used")
			sh := newOpsShell(h)
			run := func(cmd string) moveResult {
				got := sh.Run(cmd)
				if got.Code != 0 || got.Stderr != "" {
					t.Fatalf("%s: exit %d: %s", cmd, got.Code, got.Stderr)
				}
				return got
			}
			// Step 1 has no schedule, agent or caller to stop here.
			run(back.Block("mv -- ~/.config/agent-director ~/.config/agent-director.after-upgrade-"))
			step3 := strings.Split(back.Block("ls -d ~/.agent-director.pre-move-*"), "\n")
			if len(step3) != 3 {
				t.Fatalf("step 3's commands: %q", step3)
			}
			if _, err := os.Lstat(h.Old); err == nil {
				run(step3[0])
			}
			kept := strings.Fields(run(step3[1]).Stdout)
			sort.Strings(kept)
			if len(kept) == 0 {
				t.Fatal("no kept pre-move directory")
			}
			newest := strings.TrimPrefix(filepath.Base(kept[len(kept)-1]), ".agent-director.pre-move-")
			run(back.Fill(step3[2], "<newest>", newest))
			// Step 4: this restore, of the store kept outside AD's directory.
			restore(t, h, sh, res, db)
			// Steps 5 and 6: --no-hooks, and no MCP registration.
			installOld(t, h, genuineRelease(t, "v0.11.1"))
			if fi, err := os.Lstat(h.Old); err != nil || !fi.IsDir() {
				t.Fatalf("%s is not the pre-move directory: %v", h.Old, err)
			}
			if left, _ := filepath.Glob(h.Old + ".pre-move-*"); len(left) != 0 {
				t.Errorf("pre-move directories left: %v", left)
			}
			assertOldListWorks(t, h)
			if v := moveUserVersion(t, h, db); v != "5" {
				t.Errorf("restored store at v%s; want v5", v)
			}
		})
	})
}
