package installsh_test

// SR-8.6: group interrupts settle genuine installs at each cutover boundary.
import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const beforeInterruptAction = "re-run this release's installer with a new --journal-backup directory, and let it finish"
const committedInterruptAction = "re-run this release's installer with a new --journal-backup directory, which finishes the install, then run the administrator's verification retry until it exits 0 with authority sqlite"
const cancelledInterruptAction = "the attempt was interrupted before its cutover commit; keep the maintenance window, then re-run this release's installer with a new --journal-backup, which finishes any pending schema step, then run migrate-journal with a new --backup-dir until it exits 0 with authority \"sqlite\""
const lateInterruptLine = "install.sh: an interrupt arrived after migrate-journal's attempt and was ignored, so that the outcome below could be settled; it is the attempt's own."

// J41: "re-run this release's installer with a new --journal-backup
// directory, and let it finish"; follows are part of the before-start rows.
func TestAdviceFollow_J41_CutoverInterrupt(t *testing.T) { cutoverInterruptCases(t, "before") }

// J42: "re-run this release's installer ... then run the administrator's
// verification retry until it exits 0 with authority sqlite" in both forms.
func TestAdviceFollow_J42_CutoverInterrupt(t *testing.T) { cutoverInterruptCases(t, "committed") }

func TestCutoverInterrupt(t *testing.T) { cutoverInterruptCases(t, "running") }

func cutoverInterruptCases(t *testing.T, group string) {
	for _, tc := range []struct {
		name, group                                                        string
		old, reader, current, bare, noSignal, file, closed, reread, follow bool
	}{
		{name: "restored", group: "before", follow: true},
		{name: "reader_ended_old_layout", group: "before", old: true, reader: true},
		{name: "not_restored", group: "before", current: true, file: true, follow: true},
		{name: "bare_rename", group: "before", bare: true},
		{name: "bare_rename_exit130_without_signal", group: "before", bare: true, noSignal: true},
		{name: "cancelled", group: "running"},
		{name: "hup_closed_stderr", group: "running", closed: true},
		{name: "hup_then_term_reread", group: "running", reread: true},
		{name: "piped", group: "committed", follow: true},
		{name: "file", group: "committed", file: true, follow: true},
		{name: "reader_ended", group: "committed", reader: true},
	} {
		if tc.group != group {
			continue
		}
		bashes := []string{"bash-5"}
		if group == "before" {
			bashes = append(bashes, "bash-3.2.57")
		}
		for _, shell := range bashes {
			t.Run(tc.name+"/"+shell, func(t *testing.T) {
				bash := os.Getenv("AD_TEST_BASH32")
				if shell == "bash-3.2.57" && bash == "" {
					t.Skip("SKIP: bash 3.2.57 is not available (AD_TEST_BASH32 unset)")
				}
				var h *moveHome
				if tc.old {
					h = newGenuineOldHome(t)
					linksInHome(h)
				} else {
					c, _ := newCopiesCrossing(t)
					h = c.h
					h.Flags = append(h.Flags, c.valid()...)
					// A rename-time interrupt precedes the first audit event;
					// keep an existing empty trail for p5's preservation check.
					h.Write(filepath.Join(h.New, "ad-trail.jsonl"), "", 0o600)
				}
				if shell == "bash-5" {
					bash = h.S.Bash
				}
				version, err := exec.Command(bash, "-c", `printf '%s' "$BASH_VERSION"`).Output()
				prefix := "5."
				if shell == "bash-3.2.57" {
					prefix = "3.2.57"
				}
				if err != nil || !strings.HasPrefix(string(version), prefix) {
					t.Fatalf("%s BASH_VERSION=%q: %v", shell, version, err)
				}
				t.Logf("installer GNU bash %s", version)
				f := newCutoverFixture(t, h)
				db := filepath.Join(h.New, "state.db")
				if tc.current {
					// Reuse J40's genuine failed-first-run setup: install this
					// release while another administrator holds the migration lock.
					lock := db + ".input-migration.lock"
					holder := h.startProc(exec.Command(h.S.Bash, "-c", `exec 9>"$1"; flock -x 9; echo ready; read -r done`, "lock", lock), "", "ready")
					f.Check(f.Install(false, false), "ErrJournalMigrationIncomplete", "unknown", false)
					holder.Kill()
					h.Flags = setFlag(h.Flags, "--journal-backup", filepath.Join(h.Root, "second-backup"))
					h.Write(f.Output, "", 0o600)
				}
				f.Barrier()
				switch group {
				case "before":
					block := "printf 'ready\\n' >" + moveQuote(f.Ready) + "\nexec cat <" + moveQuote(f.Go) + "\n"
					if tc.noSignal {
						block = "exit 130\n"
					}
					if tc.bare {
						admin := filepath.Join(h.New, "admin", "agent-director-admin")
						marker := filepath.Join(h.Root, "rename-blocked")
						h.WrapTool("mv", `if [[ "$#" == 2 && "$1" == `+moveQuote(admin)+`.tmp.* && "$2" == `+moveQuote(admin)+` && ! -e `+moveQuote(marker)+` ]]; then
: >`+moveQuote(marker)+"\n"+block+"fi")
					} else {
						h.WrapTool("mktemp", `if [[ "$#" == 1 && "$1" == `+moveQuote(filepath.Join(h.New, "migrate-authorized")+".tmp.XXXXXX")+` ]]; then
`+block+"fi")
					}
				case "running":
					if tc.reread {
						// b.5pb: reach the post-attempt reread without a
						// holder that pre-flight would correctly reject.
						f.Choose("child:verify-fails-1")
						h.Env["AD_TEST_REREAD_AFTER"] = f.Finished
					} else {
						cancelled, err := json.Marshal(map[string]any{
							"err_name": "ErrJournalMigrationRefused", "err_description": "journal migration refused: interrupted; " + cancelledInterruptAction,
							"details": map[string]any{"cutover": "not_committed", "steps_committed": false,
								"refusal": map[string]any{"reason": "interrupted", "processes": []any{}, "action": cancelledInterruptAction}},
						})
						if err != nil {
							t.Fatal(err)
						}
						h.Write(f.Control+".response", string(cancelled)+"\n", 0o600)
						f.Choose("signal")
					}
				case "committed":
					f.Choose("child:signal-after-fifo")
					// Observe step 5's real version read after the successful
					// attempt, including when its stdout reader has gone.
					h.WrapTool("sqlite3", `if [[ -f `+moveQuote(f.Finished)+` && "$*" == *`+moveQuote(db)+`* && "${!#}" == 'PRAGMA user_version;' ]]; then
: >`+moveQuote(filepath.Join(h.Root, "step5-read"))+`
fi`)
				}
				before := h.Snapshot()
				trailBefore, _ := os.ReadFile(filepath.Join(h.New, "ad-trail.jsonl"))
				running := f.Start(bash, !tc.file, tc.reader, tc.closed)
				if !tc.noSignal {
					f.Await()
					sig := syscall.SIGINT
					if tc.closed || tc.reread {
						sig = syscall.SIGHUP
					}
					running.Signal(sig)
					if tc.reader {
						f.ReaderEnded()
					}
					if tc.reread {
						running.Signal(syscall.SIGTERM)
					} else if group == "committed" {
						f.Continue()
					}
				}
				res := running.Wait(t)
				h.Release() // reap the source writer/reader and release the holder
				cause, cutover, action := "ErrJournalMigrationRefused", "not_committed", beforeInterruptAction
				if tc.current || group == "committed" {
					cause = "ErrJournalMigrationIncomplete"
				}
				if group == "committed" {
					if _, err := os.Stat(filepath.Join(h.Root, "step5-read")); err != nil {
						t.Errorf("step 5 did not re-read the store version: %v", err)
					}
					cutover, action = "committed", committedInterruptAction
				}
				if group == "running" {
					action = cancelledInterruptAction
				}
				if tc.closed {
					if res.Code != 5 || res.Stderr != "" {
						t.Fatalf("closed stderr: %+v", res)
					}
				} else {
					env := f.Check(res, cause, cutover, false)
					if tc.reread {
						if env.Details.Refusal["reason"] != "old_or_unverified_mutators" {
							t.Errorf("attempt refusal changed: %v", env.Details.Refusal)
						}
						outcomeLines(t, res.Stderr, lateInterruptLine, "  The version cross-check at "+db+" could not be made; the attempt reported not_committed:")
					} else {
						if env.Details.Refusal["reason"] != "interrupted" || env.Details.Refusal["action"] != action {
							t.Errorf("interrupt refusal: %v; want action %q", env.Details.Refusal, action)
						}
						if strings.Contains(res.Stderr, lateInterruptLine) {
							t.Error("early interrupt reported as late")
						}
						opening := "install.sh: the install was interrupted (SIGINT) "
						switch group {
						case "before":
							opening += "after the new binaries were installed and before migrate-journal started; migrate-journal never ran, so nothing was migrated."
						case "running":
							opening += "while migrate-journal ran; its own outcome follows."
						case "committed":
							opening += "while migrate-journal ran. It had already begun the cutover commit, so it finished the cutover and exited 0; the install stopped after checking the store's version, before the hook, MCP and schedule steps."
						}
						outcomeLines(t, res.Stderr, opening)
					}
					if strings.Contains(res.Stderr, "A commit may have completed") {
						t.Error("interrupt reports a possible commit")
					}
					if group == "running" && (env.Details.Steps == nil || *env.Details.Steps) {
						t.Errorf("steps_committed: %v", env.Details.Steps)
					}
				}
				if !tc.current && group != "committed" {
					assertRestored(t, h, before, trailBefore, tc.old, false)
					if !tc.closed {
						outcomeLines(t, res.Stderr, restoredHeadline)
					}
					if tc.old {
						aside, err := filepath.Glob(h.New + ".after-upgrade-*")
						if err != nil || len(aside) != 1 {
							t.Fatalf("set-aside: %v, %v", aside, err)
						}
						outcomeLines(t, res.Stderr, fmt.Sprintf("  move    : undone: %s is back as it was before the run; this run's %s was set aside as %s; the re-run moves it again", h.Old, h.New, aside[0]))
					}
				} else {
					for _, pair := range [][2]string{{h.S.Current.Main, filepath.Join(h.New, "bin", "agent-director")}, {h.S.ScheduleProbe.Main, filepath.Join(h.New, "admin", "agent-director-admin")}} {
						if restoreDescribe(t, pair[0]) != restoreDescribe(t, pair[1]) {
							t.Errorf("candidate missing at %s", pair[1])
						}
					}
					if len(restoreCopyNames(t, h)) != 0 {
						t.Error("restore copies retained")
					}
				}
				for _, pattern := range []string{filepath.Join(h.Home, "links", "agent-director.new"), filepath.Join(h.New, "admin", "agent-director-admin.tmp.*")} {
					if paths, err := filepath.Glob(pattern); err != nil || len(paths) != 0 {
						t.Errorf("temporary install paths: %v, %v", paths, err)
					}
				}
				if group == "committed" {
					if got, want := moveUserVersion(t, h, db), moveCurrentVersion(t, h); got != want {
						t.Errorf("step 5 store version %s; want %s", got, want)
					}
					if got := h.Sqlite(db, "SELECT count(*) FROM store_meta WHERE key='native_input_authority' AND value LIKE 'sqlite-v1:%';", "-readonly"); got != "1\n" {
						t.Errorf("authority: %q", got)
					}
					if !tc.reader {
						outcomeLines(t, res.Stdout, "  schema  : migration verified — state.db now at v"+moveCurrentVersion(t, h))
					}
				}
				// J41/J42 follows run once per form under the image's bash 5.
				if !tc.follow || shell != "bash-5" {
					return
				}
				if group == "before" {
					if err := os.Remove(filepath.Join(h.Tools, "mktemp")); err != nil {
						t.Fatal(err)
					}
				}
				f.Clear()
				backup := filepath.Join(h.Root, "interrupt-follow-backup")
				follow := f.Follow(res, !tc.file, backup)
				if group == "before" {
					(&crossingCase{h: h, db: db, version: "15"}).assertCrossed(t, follow, backup)
				} else {
					if follow.Code != 0 {
						t.Fatalf("committed retry: %+v", follow)
					}
					verified := f.runAdvice(res, "<new-verification-backup-directory>", filepath.Join(h.Root, "interrupt-verification-backup"))
					var answer struct {
						Authority string `json:"authority"`
					}
					if err := json.Unmarshal([]byte(verified.Stdout), &answer); err != nil || verified.Code != 0 || answer.Authority != "sqlite" {
						t.Fatalf("verification: %+v (%v)", verified, err)
					}
				}
			})
		}
	}
}
