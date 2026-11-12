//go:build linux

package installsh_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/probe"
)

// b.5pb: find a store holder before the swap, and let operators check for
// holders without changing the install or requiring live rows to be stopped.
func TestInstallHolders(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and installs genuine v0.17.3")
	}
	for _, tc := range []struct {
		name      string
		checkOnly bool
		holder    bool
		live      bool
		current   bool
		marker    string
	}{
		{name: "crossing-preflight", holder: true},
		{name: "check-holders-refusal", checkOnly: true, holder: true, live: true},
		{name: "check-holders-live-rows", checkOnly: true, live: true},
		{name: "check-holders-current", checkOnly: true, current: true},
		{name: "check-holders-existing-kept-marker", checkOnly: true, current: true, marker: "existing"},
		{name: "check-holders-missing-kept-marker", checkOnly: true, current: true, marker: "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var h *moveHome
			var db string
			var flags []string
			if tc.current {
				h = newMoveHome(t)
				moveMustInstall(t, h)
				db = filepath.Join(h.New, "state.db")
				if v, want := moveUserVersion(t, h, db), moveCurrentVersion(t, h); v != want {
					t.Fatalf("fresh install's store is at v%s, want v%s", v, want)
				}
				flags = []string{"--journal-maintenance-window", "--journal-roots-complete",
					"--journal-backup", filepath.Join(h.Root, "unused-backup")}
			} else {
				c := newCrossingCase(t)
				h, db, flags = c.h, c.db, c.valid()
			}
			if tc.live {
				h.Sqlite(db, crossingLiveRowSQL("still-waiting", "waiting"))
			}
			if tc.marker != "" {
				// Neither case has an old install. These reach the two stale
				// marker removal branches without making a directory move due.
				kept := h.Old + ".pre-move-20010203-040506"
				if tc.marker == "existing" {
					h.Write(filepath.Join(kept, "config.toml"), moveOldConfig, 0o600)
				}
				moveMarkerStale(h, kept+"\n") // Writes moveMarkerRel.
			}
			var holder *moveProc
			var holderStart string
			if tc.holder {
				holder, holderStart = installerStoreHolder(t, h, db)
			}
			if tc.checkOnly {
				// Keep ordinary hooks and PATH-link installation enabled, so a
				// mistaken continuation into the install is visible in the home.
				h.Flags = nil
				flags = append(flags, "--check-holders")
				ownedClaude(t, h, "", "none-found-many-others")
				schedStub{h}.Cron("# operator's crontab\n")
			}
			before := h.PhysicalSnapshot()
			res := h.Install(flags...)
			if holder != nil {
				if got, alive, known := probe.NewProcChecker().StartTime(holder.Pid); !alive || !known || got != holderStart {
					t.Error("installer stopped or replaced the holder")
				}
			}
			// Includes both installed binaries, the store and its sidecars,
			// sentinel, restore copies, hooks, schedules and managed PATH link.
			assertRefusedUnchanged(t, h, before)
			if _, err := os.Lstat(journalBackup(flags)); !os.IsNotExist(err) {
				t.Errorf("read-only check made a journal backup, or cannot inspect it: %v", err)
			}
			forbidden := []string{"restore :", "schema  : authorized"}
			if tc.holder {
				forbidden = append(forbidden, "install.sh: pre-flight OK")
			}
			for _, s := range forbidden {
				if strings.Contains(res.Stdout+res.Stderr, s) {
					t.Errorf("holder pre-flight/check reached installation (%q):\n%s\n%s", s, res.Stdout, res.Stderr)
				}
			}
			if tc.checkOnly {
				if writes := (schedStub{h}).CronWrites(); writes != 0 {
					t.Errorf("--check-holders wrote the crontab %d times", writes)
				}
				for _, call := range ownedMCPCalls(ownedCalls(t, h)) {
					if len(call.Args) > 1 && (call.Args[1] == "add" || call.Args[1] == "remove") {
						t.Errorf("--check-holders changed MCP registration: %s", call.line())
					}
				}
			}
			if tc.holder {
				assertInstallerHolderRefusal(t, res, holder.Pid)
			} else {
				moveWant{}.Check(t, h, res)
				if tc.current && !strings.Contains(res.Stdout+res.Stderr, "no holder check") {
					t.Errorf("non-crossing check lacks the no-check-needed explanation:\n%s\n%s", res.Stdout, res.Stderr)
				}
			}
		})
	}
}

// b.5pb: inventory runs before install.sh makes the store and install root
// private or moves the old layout. Permissions must not hide a real holder
// or reject the holder-free upgrade that the installer's preparation accepts.
func TestInstallHoldersNonPrivate(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and installs genuine v0.17.3")
	}
	for _, tc := range []struct {
		name string
		mode os.FileMode
		move bool
	}{
		{name: "in-place-0755", mode: 0o755},
		{name: "in-place-group-writable", mode: 0o775},
		{name: "move-due", mode: 0o755, move: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCrossingCase(t)
			h := c.h
			root, db := h.New, c.db
			if tc.move {
				// Retain the genuine v0.17.3 pair and store, in the old layout.
				if err := os.Rename(h.New, h.Old); err != nil {
					t.Fatal(err)
				}
				root, db = h.Old, filepath.Join(h.Old, "state.db")
			}
			for path, mode := range map[string]os.FileMode{root: tc.mode, db: 0o644} {
				if err := os.Chmod(path, mode); err != nil {
					t.Fatal(err)
				}
			}
			flags := c.valid()
			holder, start := installerStoreHolder(t, h, db)
			before := h.PhysicalSnapshot()
			for _, checkOnly := range []bool{true, false} {
				args := append([]string{}, flags...)
				if checkOnly {
					args = append(args, "--check-holders")
				}
				res := h.Install(args...)
				assertInstallerHolderRefusal(t, res, holder.Pid)
				assertRefusedUnchanged(t, h, before)
				if strings.Contains(res.Stdout, "install.sh: pre-flight OK") {
					t.Error("holder passed pre-flight on a non-private store")
				}
				if got, alive, known := probe.NewProcChecker().StartTime(holder.Pid); !alive || !known || got != start {
					t.Fatal("inventory stopped or replaced the non-private store's holder")
				}
			}
			holder.Kill()
			res := h.Install(append(flags, "--check-holders")...)
			if res.Code != 0 {
				t.Errorf("holder-free check: exit %d\n%s\n%s", res.Code, res.Stdout, res.Stderr)
			}
			assertRefusedUnchanged(t, h, before)
			if _, err := os.Lstat(journalBackup(flags)); !os.IsNotExist(err) {
				t.Errorf("inventory created a backup or left it uncheckable: %v", err)
			}
			res = h.Install(flags...)
			if tc.move {
				assertMovedCrossed(t, h, nil, res, journalBackup(flags))
			} else {
				c.assertCrossed(t, res, journalBackup(flags))
			}
			for path, want := range map[string]os.FileMode{h.New: 0o700, c.db: 0o600} {
				if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != want {
					t.Errorf("prepared permissions at %s: %v (%v); want %04o", path, fi, err, want)
				}
			}
		})
	}
}

func installerStoreHolder(t *testing.T, h *moveHome, db string) (*moveProc, string) {
	t.Helper()
	// Readiness follows opening fd 3; the shell then retains stdin until reaped.
	holder := h.startProc(exec.Command(h.S.Bash, "-c",
		`exec 3<"$1" || exit 1; echo ready; IFS= read -r done`, "holder", db), "", "ready")
	start, alive, known := probe.NewProcChecker().StartTime(holder.Pid)
	if !alive || !known {
		t.Fatal("holder process identity unavailable after readiness")
	}
	return holder, start
}

// b.5pb, code-review r1: a failed second release allocation must clean up
// the first download file, even though source resolution never finished.
func TestCheckHoldersReleaseAllocationFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the installer fixture")
	}
	h := newMoveHome(t)
	schedStub{h}.Curl()
	h.WriteTool("curl", "exec "+moveQuote(filepath.Join(h.Tools, "curl-stub"))+` "$@"`)
	h.Pair = movePair{} // --from-release supplies both sources.
	calls := filepath.Join(h.Root, "release-allocations.log")
	h.WriteTool("mktemp", `case "${!#}" in
    */agent-director.XXXXXX)
        path=$(`+moveQuote(h.RealTool("mktemp"))+` "$TMPDIR/agent-director.XXXXXX") || exit 1
        printf 'allocated\n' >>`+moveQuote(calls)+`
        printf '%s\n' "$path"
        ;;
    */agent-director-admin.XXXXXX)
        printf 'refused\n' >>`+moveQuote(calls)+`
        echo 'injected second release allocation failure' >&2
        exit 1
        ;;
    *) exec `+moveQuote(h.RealTool("mktemp"))+` "$@" ;;
esac`)
	before := h.PhysicalSnapshot()
	res := h.Install("--check-holders", "--from-release", "v9.9.9")
	if res.Code == 0 {
		t.Errorf("second release allocation failure exited 0:\n%s\n%s", res.Stdout, res.Stderr)
	}
	if got := h.migRead(calls); got != "allocated\nrefused\n" {
		t.Errorf("release allocation sequence = %q; want first allocated, second refused", got)
	}
	assertRefusedUnchanged(t, h, before) // Also requires an empty TMPDIR.
}

// assertInstallerHolderRefusal checks the relayed process envelope rather
// than assertOwnRefusal, whose install.sh-owned refusals have no processes.
func assertInstallerHolderRefusal(t *testing.T, res moveResult, pid int) {
	t.Helper()
	envs := preflightEnvelopes(t, res, "ErrJournalMigrationRefused")
	if len(envs) != 1 {
		t.Fatalf("got %d error envelopes; want one relayed holder refusal:\n%s", len(envs), res.Stderr)
	}
	var env struct {
		Details struct {
			Refusal struct {
				Reason    string `json:"reason"`
				Processes []struct {
					PID int `json:"pid"`
				} `json:"processes"`
			} `json:"refusal"`
			Cutover        string `json:"cutover"`
			StepsCommitted *bool  `json:"steps_committed"`
		} `json:"details"`
	}
	if err := json.Unmarshal([]byte(envs[0]), &env); err != nil {
		t.Fatal(err)
	}
	d := env.Details
	if d.Refusal.Reason != "old_or_unverified_mutators" || d.Cutover != "not_committed" || d.StepsCommitted == nil || *d.StepsCommitted {
		t.Errorf("want an uncommitted old_or_unverified_mutators refusal:\n%s", envs[0])
	}
	found := false
	for _, p := range d.Refusal.Processes {
		found = found || p.PID == pid
	}
	if !found {
		t.Errorf("refusal does not name holder pid %d:\n%s", pid, envs[0])
	}
	if !strings.Contains(res.Stderr, "Nothing was installed or changed.") {
		t.Errorf("pre-flight refusal lacks the unchanged-install explanation:\n%s", res.Stderr)
	}
}
