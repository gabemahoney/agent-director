package installsh_test

// SR-8.6 restores: genuine releases retain their files and usable stores;
// an incomplete restore keeps only unfinished items for the human procedure.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const restoredHeadline = "install.sh: the cutover was refused before changing the previous install's store; the previous install was put back as it was before the run."

// assertRestored compares the entire home by the move fixture's logical
// store comparison, allowing retained .prior/aside files and an appended
// audit trail from the administrator's refusal.
func assertRestored(t *testing.T, h *moveHome, before moveTree, trailBefore []byte, old, linked bool) {
	t.Helper()
	after := h.Snapshot()
	if !old {
		trail := filepath.Join(h.New, "ad-trail.jsonl")
		trailAfter, err := os.ReadFile(trail)
		if err != nil || !bytes.HasPrefix(trailAfter, trailBefore) {
			t.Errorf("audit trail was not preserved: %v", err)
		}
		delete(before, ".config/agent-director/ad-trail.jsonl")
		delete(after, ".config/agent-director/ad-trail.jsonl")
	}
	for p := range after {
		if strings.HasPrefix(p, ".config/agent-director.after-upgrade-") || strings.HasSuffix(p, ".prior") {
			delete(after, p)
		}
	}
	if _, existed := before[".config"]; !existed {
		delete(after, ".config")
	}
	if d := moveDiff(before, after); d != "" {
		t.Errorf("restore changed the previous install:\n%s", d)
	}
	if got := restoreCopyNames(t, h); len(got) != 0 {
		t.Errorf("copies left after restore: %v", got)
	}
	if old {
		if !linked {
			if _, err := os.Lstat(h.New); !os.IsNotExist(err) {
				t.Errorf("new directory after restore: %v", err)
			}
		}
		assertOldListWorks(t, h)
	} else if res := h.Run(filepath.Join(h.New, "bin", "agent-director"), "list"); res.Code != 0 {
		t.Errorf("previous release's list: exit %d: %s", res.Code, res.Stderr)
	}
}

// Q3: the linked-root refusal changes neither the install nor its external
// target. Follow its action literally, then run the printed retry command.
func followLinkedRootRefusal(t *testing.T, f *cutoverFixture, target string, refuse func() moveResult) (moveResult, string) {
	t.Helper()
	h := f.h
	before := h.PhysicalSnapshot()
	outside := *h
	outside.Home = filepath.Dir(target)
	targetBefore := outside.PhysicalSnapshot()
	callsBefore, _ := os.ReadFile(f.Calls)
	res := refuse()
	assertOwnRefusal(t, res, "legacy_root_symlink",
		"replace the link at "+h.New+" with the directory it points to, then re-run this release's installer with a new --journal-backup directory",
		"install.sh: "+h.New+" is a link; refusing the native-input cutover.",
		"  Replace the link with the directory it points to, then re-run this release's installer with a new backup directory:")
	assertRefusedUnchanged(t, h, before)
	assertRefusedUnchanged(t, &outside, targetBefore)
	callsAfter, _ := os.ReadFile(f.Calls)
	if !bytes.HasPrefix(callsAfter, callsBefore) {
		t.Fatal("candidate call log lost earlier calls")
	}
	for _, call := range strings.Split(string(callsAfter[len(callsBefore):]), "\n") {
		if strings.Contains(call, "migrate-journal") && !strings.Contains(call, "--help") {
			t.Errorf("linked-root refusal reached migrate-journal: %s", call)
		}
	}
	prev := filepath.Join(h.New, "bin", "agent-director")
	if _, err := os.Stat(h.Old); err == nil {
		prev = filepath.Join(h.Old, "bin", "agent-director")
	}
	if list := h.Run(prev, "list"); list.Code != 0 {
		t.Errorf("previous release's list after pre-flight refusal: exit %d: %s", list.Code, list.Stderr)
	}
	// Replace only the link, retaining the exact directory it pointed to.
	if err := os.Remove(h.New); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(target, h.New); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(h.Root, "unlinked-follow-backup")
	return f.Follow(res, false, backup), backup
}

// J39: "Fix the reported cause, then re-run this release's installer with a
// new backup directory"; the same restore rows follow file and piped advice.
func TestAdviceFollow_J39_Restored(t *testing.T) {
	for _, tc := range []struct {
		name                                             string
		old, linked, backupFailed, closed, darwin, piped bool
	}{
		{name: "v0.17.3 in place"},
		{name: "v0.11.1 moved store", old: true, piped: true},
		{name: "v0.11.1 linked new directory", old: true, linked: true},
		{name: "v0.17.3 linked in place", linked: true},
		{name: "v0.17.3 backup failed and reread unavailable", backupFailed: true},
		{name: "v0.17.3 stderr closed", closed: true},
		{name: "v0.17.3 Darwin", darwin: true},
		{name: "v0.11.1 Darwin", old: true, darwin: true, piped: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var h *moveHome
			var c *crossingCase
			if tc.old {
				h = newGenuineOldHome(t)
				linksInHome(h)
			} else {
				c, _ = newCopiesCrossing(t)
				h = c.h
				h.Flags = append(h.Flags, c.valid()...)
				h.Flags = append(h.Flags, "--keep-prior")
			}
			f := newCutoverFixture(t, h)
			target := filepath.Join(h.Root, "linked-parent", "target")
			if tc.linked && !tc.old {
				if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(h.New, target); err != nil {
					t.Fatal(err)
				}
				h.Link(target, h.New)
				follow, backup := followLinkedRootRefusal(t, f, target, func() moveResult { return f.Install(false, false) })
				c.assertCrossed(t, follow, backup)
				return
			}
			var clear func()
			if tc.old {
				f.Choose("child:verify-fails-2")
				clear = f.Clear
			} else if !tc.backupFailed {
				holder := h.HoldSQLite(c.db, "PRAGMA user_version;")
				clear = holder.Kill
			}
			if tc.linked {
				if err := os.MkdirAll(target, 0o700); err != nil {
					t.Fatal(err)
				}
				h.Write(filepath.Join(filepath.Dir(target), "sibling"), "leave this alone\n", 0o600)
				h.Link(target, h.New)
				// A present destination is eligible only as a marked partial move.
				h.Write(filepath.Join(target, ".move-incomplete"), "", 0o600)
				// Let the link appear after pre-flight checked it, at the managed
				// link's copy. The different, injected verifier refusal still
				// exercises SR-2.16's linked restore, without bypassing Q3.
				link := filepath.Join(h.Home, "links", "agent-director")
				h.WrapTool("ln", `if [[ "${!#}" == `+moveQuote(link)+`.restore-*/`+moveQuote(filepath.Base(link))+` ]]; then
    `+moveQuote(h.RealTool("ln"))+` -s -- `+moveQuote(target)+` `+moveQuote(h.New)+` || exit
fi`)
			}
			if tc.backupFailed {
				parent := filepath.Join(h.Root, "full-disk")
				if err := os.Mkdir(parent, 0o700); err != nil {
					t.Fatal(err)
				}
				h.Flags = setFlag(h.Flags, "--journal-backup", filepath.Join(parent, "backup"))
				writable := unwritable(t, parent)
				h.WrapTool("mktemp", `if [[ "$*" == *agent-director.read.* && -f `+moveQuote(f.Finished)+` ]]; then echo 'injected read directory unavailable' >&2; exit 1; fi`)
				clear = func() {
					writable()
					if err := os.Remove(filepath.Join(h.Tools, "mktemp")); err != nil {
						t.Fatal(err)
					}
				}
			}
			if tc.darwin {
				standInDarwin(h)
			}
			before := h.Snapshot()
			trailBefore, _ := os.ReadFile(filepath.Join(h.New, "ad-trail.jsonl"))
			if tc.linked {
				// The restore's expected tree includes the link the wrapper adds.
				if err := os.Remove(h.New); err != nil {
					t.Fatal(err)
				}
			}
			res := f.Install(tc.piped, tc.closed)
			if tc.closed {
				if res.Code != 5 || res.Stderr != "" {
					t.Errorf("closed stderr: exit %d, %q", res.Code, res.Stderr)
				}
			} else {
				env := f.Check(res, "ErrJournalMigrationRefused", "not_committed", false)
				if env.Details.Steps == nil || *env.Details.Steps != tc.old {
					t.Errorf("steps_committed = %v; want %v", env.Details.Steps, tc.old)
				}
				reason := "old_or_unverified_mutators"
				if tc.backupFailed {
					reason = "backup_failed"
				}
				if env.Details.Refusal["reason"] != reason {
					t.Errorf("refusal = %v; want %s", env.Details.Refusal, reason)
				}
				outcomeLines(t, res.Stderr, restoredHeadline,
					"  The binaries, migration sentinel and managed PATH link are as before the run.",
					"  Fix the reported cause, then re-run this release's installer with a new backup directory:")
				if tc.backupFailed {
					outcomeLines(t, res.Stderr, "  The version cross-check at "+c.db+" could not be made; the attempt reported not_committed:", "    injected read directory unavailable")
				}
			}
			assertRestored(t, h, before, trailBefore, tc.old, tc.linked)
			if tc.old {
				for _, prefix := range []string{"  kept    :", "  old link:", "  old PATH:", "  references:"} {
					if strings.Contains(res.Stdout, prefix) {
						t.Errorf("restored run printed move note %q", prefix)
					}
				}
				aside, err := filepath.Glob(h.New + ".after-upgrade-*")
				if err != nil {
					t.Fatal(err)
				}
				if tc.linked {
					if len(aside) != 0 {
						t.Errorf("linked new directory set aside: %v", aside)
					}
					if raw, _ := os.Readlink(h.New); raw != target {
						t.Errorf("link changed to %q", raw)
					}
					entries, err := os.ReadDir(target)
					if err != nil || len(entries) != 1 || entries[0].Name() != ".move-incomplete" {
						t.Fatalf("linked target: %v, %v", entries, err)
					}
					if b, err := os.ReadFile(filepath.Join(target, ".move-incomplete")); err != nil || len(b) != 0 {
						t.Errorf("move marker: %q, %v", b, err)
					}
					if b, err := os.ReadFile(filepath.Join(filepath.Dir(target), "sibling")); err != nil || string(b) != "leave this alone\n" {
						t.Errorf("sibling: %q, %v", b, err)
					}
					outcomeLines(t, res.Stderr, "  move    : undone: "+h.Old+" is back as it was before the run; this run's linked "+h.New+" was emptied; the re-run moves it again")
				} else {
					if len(aside) != 1 {
						t.Fatalf("set-aside directories: %v", aside)
					}
					outcomeLines(t, res.Stderr, fmt.Sprintf("  move    : undone: %s is back as it was before the run; this run's %s was set aside as %s; the re-run moves it again", h.Old, h.New, aside[0]))
				}
			} else {
				for _, pair := range [][2]string{{c.release.Pair.Main, filepath.Join(h.New, "bin", "agent-director.prior")}, {c.release.Pair.Admin, filepath.Join(h.New, "admin", "agent-director-admin.prior")}} {
					if restoreDescribe(t, pair[0]) != restoreDescribe(t, pair[1]) {
						t.Errorf("prior differs from previous release: %s", pair[1])
					}
				}
			}
			clear()
			if !tc.closed {
				backup := filepath.Join(h.Root, "follow-backup")
				var follow moveResult
				if tc.linked {
					if err := os.Remove(filepath.Join(h.Tools, "ln")); err != nil {
						t.Fatal(err)
					}
					follow, backup = followLinkedRootRefusal(t, f, target, func() moveResult { return f.Follow(res, tc.piped, backup) })
				} else {
					follow = f.Follow(res, tc.piped, backup)
				}
				if tc.old {
					assertMovedCrossed(t, h, nil, follow, backup)
				} else {
					c.assertCrossed(t, follow, backup)
				}
			}
			if tc.darwin {
				assertDarwinOnly(t, h)
			}
		})
	}
}

// J44 runs the occupied old path row, continuing it with the put-back.
func TestRestoreOutcomeIncomplete(t *testing.T) {
	t.Run("after link and sentinel restore", func(t *testing.T) { restoreOutcomeIncomplete(t, true) })
}

// restoreOutcomeIncomplete runs one restore that cannot finish: partial, the
// administrator's removal failing after the link and sentinel are back;
// otherwise the occupied old path, for which it returns the home, the run,
// its last envelope and the snapshot taken before the run.
func restoreOutcomeIncomplete(t *testing.T, partial bool) (*moveHome, moveResult, cutoverEnvelope, moveTree) {
	var h *moveHome
	var p copyPaths
	var db, savedMain string
	if partial {
		c, paths := newCopiesCrossing(t)
		h, p, db = c.h, paths, c.db
		h.Flags = append(h.Flags, c.valid()...)
		// P5-T2: restoration must remove an administrator absent beforehand,
		// after putting back the sentinel and a distinguishable raw link.
		for _, path := range []string{p.admin, p.link} {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}
		h.Link("../.config/agent-director/bin/agent-director", p.link)
		savedMain = restoreDescribe(t, p.bin)
	} else {
		h = newGenuineOldHome(t)
		_, p.link = linksInHome(h)
		db = filepath.Join(h.New, "state.db")
	}
	link := p.link
	f := newCutoverFixture(t, h)
	if partial {
		f.Choose("child:verify-fails-1")
		h.WrapTool("rm", `if [[ -f `+moveQuote(f.Finished)+` && "${!#}" == `+moveQuote(p.admin)+` ]]; then echo 'injected administrator removal failure' >&2; exit 1; fi`)
	} else {
		f.Choose("child:verify-fails-2")
		h.Write(f.After, "mkdir "+moveQuote(h.Old)+"\nprintf 'occupant\\n' >"+moveQuote(filepath.Join(h.Old, "occupant"))+"\n", 0o700)
	}
	before := h.Snapshot()
	res := f.Install(false, false)
	env := f.Check(res, "ErrSchemaVerifyFailed", "not_committed", true)
	if env.Details.AttemptStore != db {
		t.Errorf("attempt store: %s", env.Details.AttemptStore)
	}
	if partial {
		if len(env.Details.Copies) != 1 || env.Details.Copies[0].Belongs != p.bin {
			t.Fatalf("remaining copies: %+v; want only the previous CLI", env.Details.Copies)
		}
		copy := env.Details.Copies[0].Copy
		if restoreDescribe(t, copy) != savedMain {
			t.Errorf("retained CLI copy changed: %s", copy)
		}
		rel, err := filepath.Rel(h.Home, copy)
		if err != nil {
			t.Fatal(err)
		}
		if copies := restoreCopyNames(t, h); len(copies) != 1 || copies[0] != rel {
			t.Errorf("copies on disk: %v; want only %s", copies, rel)
		}
		if len(env.Details.Remove) != 1 || env.Details.Remove[0] != p.admin {
			t.Errorf("remaining removals: %v; want only %s", env.Details.Remove, p.admin)
		}
		after := h.Snapshot()
		for _, path := range []string{p.link, p.sentinel, db} {
			rel, err := filepath.Rel(h.Home, path)
			if err != nil {
				t.Fatal(err)
			}
			if after[rel] != before[rel] {
				t.Errorf("completed item or store changed at %s: got %s; want %s", path, after[rel], before[rel])
			}
		}
		for _, pair := range [][2]string{{p.bin, h.S.Current.Main}, {p.admin, h.S.ScheduleProbe.Main}} {
			if restoreDescribe(t, pair[0]) != restoreDescribe(t, pair[1]) {
				t.Errorf("restore advanced past its failure at %s", pair[0])
			}
		}
		outcomeLines(t, res.Stderr,
			"install.sh: the restore could not finish; the install is partly restored: cannot remove "+p.admin+": injected administrator removal failure",
			"  put back "+p.link,
			"  put back "+p.sentinel)
		return h, res, env, before
	}
	want := map[string]bool{h.Old: false, link: false}
	for _, copy := range env.Details.Copies {
		if _, exists := want[copy.Belongs]; !exists {
			t.Errorf("unexpected kept copy: %+v", copy)
		}
		want[copy.Belongs] = true
		if copy.Belongs == link && restoreDescribe(t, copy.Copy) != "link\t"+filepath.Join(h.Old, "bin", "agent-director") {
			t.Error("kept link changed")
		}
		if copy.Belongs == h.Old {
			// Compare the entire pre-move tree at its kept location.
			got := h.Snapshot()
			rel, _ := filepath.Rel(h.Home, copy.Copy)
			for p, value := range before {
				if p == ".agent-director" || strings.HasPrefix(p, ".agent-director/") {
					if got[rel+strings.TrimPrefix(p, ".agent-director")] != value {
						t.Errorf("kept old tree changed: %s", p)
					}
				}
			}
		}
	}
	for path, found := range want {
		if !found {
			t.Errorf("copy for %s not reported", path)
		}
	}
	if b, err := os.ReadFile(filepath.Join(h.Old, "occupant")); err != nil || string(b) != "occupant\n" {
		t.Errorf("occupant changed: %q, %v", b, err)
	}
	outcomeLines(t, res.Stderr,
		"install.sh: the restore could not finish; the install is partly restored: "+h.Old+" is occupied",
		"  Put the copies listed in details.copies back with mv, as docs/operator-actions.md \"Putting back a kept install\" describes.",
		"  Use its moved-install form, which puts the pre-move directory back before checking its list.")
	return h, res, env, before
}
