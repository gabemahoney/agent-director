package installsh_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/layout"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// Work 8 follows qb's final commands with the shared doc reader, persistent
// shell and offline fetch substitution. Runtime history is real SQLite; only
// release downloads and the lost admin output are substituted.
func TestStagedDocumentUpgradeRollbackRecovery(t *testing.T) {
	h := newMoveHome(t)
	moveMustInstall(t, h)
	db := filepath.Join(h.New, "state.db")
	id, err := apitest.SeedSpawn(db, "", "working", h.Home, "off", "kept-session", false, apitest.WithPID(stagedRuntime(t, h).process.Pid))
	stagedMust(t, err)
	pair := store.BusySendPair{CallerUUID: "d5805656-bf93-480c-9137-0764327b4280", DeliveryRequestID: "kept-acceptance"}
	storefix.SeedBusySendResult(t, db, storefix.BusySendResultSeed{InstanceID: id, Pair: pair, Class: store.BusySendClassSentUnconfirmed, Final: true})
	readResult := func() store.BusySendResultLookup {
		s, err := store.Open(db)
		stagedMust(t, err)
		result, err := s.ReadBusySendResult(pair.CallerUUID, pair.DeliveryRequestID)
		stagedMust(t, err)
		stagedMust(t, s.Close())
		return result
	}
	wantResult := readResult()
	if wantResult.Lookup != store.BusySendLookupFound {
		t.Fatalf("missing setup acceptance: %+v", wantResult)
	}
	runtimeBefore := stagedSnapshot(t, h, moveNewRel+"/state.db")
	retainedBefore := stagedSnapshot(t, h, moveNewRel+"/versions", moveNewRel+"/publications", moveNewRel+"/install-contract.json", moveNewRel+"/install.lock")
	sh, finish := stagedOpsShell(t, h)
	must := func(command string) moveResult {
		t.Helper()
		r := sh.RunWithin(command, upgradeInstallWait)
		if r.Code != 0 {
			t.Fatalf("document command %s: %+v", command, r)
		}
		return r
	}
	// A real live process owns the seeded row, and the follower is explicitly
	// AD-managed. The later branch must neither exclude it nor coordinate it.
	must("export AGENT_DIRECTOR_INSTANCE_ID=" + moveQuote(id))
	classify := docSection(t, upgradeSkill, "Choose the upgrade path first")
	if r := must(classify.Block("ad_upgrade_root=")); r.Stdout != "file-only\n" {
		t.Fatalf("classification: %+v", r)
	}
	later := docSection(t, upgradeSkill, "File-only upgrade")
	later.Pin("It may run from an AD-managed session or one using AD MCP tools", "Unknown or unreadable output proves no rollback", "An ordinary later upgrade finishes here.", "Do not start the generic exempt steps.")
	h.Pair = stagedPair(t)
	follow := &upgradeFollow{t: t, h: h, releaseTag: "v" + stagedStamp}
	install := follow.releaseCommand(later, false)
	must(install)
	verify := must(later.Block("agent-director version"))
	lines := strings.Split(strings.TrimSpace(verify.Stdout), "\n")
	var v struct{ Version string }
	if len(lines) != 4 || json.Unmarshal([]byte(lines[0]), &v) != nil || v.Version != stagedStamp || lines[1] != "versions/"+layout.VersionID(stagedStamp) || lines[2] != "../current/agent-director" || lines[3] != "../current/agent-director-admin" {
		t.Fatalf("file-only verification: %+v", verify)
	}
	inspect := opsDocSection(t, "Inspecting a versioned install")
	inspect.Pin("Run each administrator command by full path, as a human or with explicit human approval for that individual run.")
	// The test operator approves each command separately, as the procedure
	// requires; no blanket administrator authorization is inferred.
	for _, command := range strings.Split(inspect.Block("version-facts"), "\n") {
		must(command)
	}
	finish() // snapshot/assertion filesystem reads are outside observation
	beforeRollback := stagedSnapshot(t, h, moveNewRel+"/versions", moveNewRel+"/publications")
	sh, finish = stagedOpsShell(t, h)
	rollback := opsDocSection(t, "Rolling back a versioned install")
	rollback.Pin("Rollback publishes retained files and preserves runtime acceptance history.", "It restores no database or `.prior` copy and truncates no publication history.")
	got := must(rollback.Fill(rollback.Block("rollback-version"), "<retained-stamp>", moveVersion))
	var raw map[string]any
	stagedMust(t, json.Unmarshal([]byte(got.Stdout), &raw))
	if raw["publication_state"] != "published" || raw["durable"] != true || raw["published_version"] != moveVersion {
		t.Fatalf("rollback did not publish: %s", got.Stdout)
	}
	finish()
	stagedLayout(t, h, moveVersion) // XA3: verify selection before another install can mask it
	sh, finish = stagedOpsShell(t, h)
	// Discard only the real admin's stdout AFTER its operation has completed.
	// The literal rerun must keep the same pair and flags, with no restore.
	f := newOutcomeFixture(t, h, "publish-version")
	h.Write(f.After, "printf '{unreadable' >"+moveQuote(f.Output)+"\n", 0600)
	must("export SCHED_TEST_DELEGATE=" + moveQuote(h.Env["SCHED_TEST_DELEGATE"]) + " SCHED_TEST_DELEGATE_EXEC=1")
	follow.releaseTag = "v" + moveVersion
	uncertainInstall := follow.releaseCommand(later, false)
	uncertain := sh.RunWithin(uncertainInstall, upgradeInstallWait)
	if uncertain.Code != 5 || !strings.Contains(uncertain.Stderr, `"publication_state":"unknown"`) || strings.Contains(uncertain.Stderr, `"reason"`) {
		t.Fatalf("uncertain publication: %+v", uncertain)
	}
	opsDocSection(t, "Recovering a versioned install").Pin("Rerun the **same install** with the same tag, pair and flags for forward recovery after correcting the cause.")
	f.Clear()
	must(uncertainInstall)
	finish()
	runtimeBefore.Unchanged(t, h)
	retainedBefore.Unchanged(t, h)
	beforeRollback.Unchanged(t, h)
	stagedLayout(t, h, moveVersion)
	if got := readResult(); !reflect.DeepEqual(got, wantResult) {
		t.Fatalf("rollback changed acceptance/result: got %+v; want %+v", got, wantResult)
	}
}

func TestStagedDocumentClassification(t *testing.T) {
	// The executable doc classifier must agree for every independent evidence
	// item, including dangling links, without touching poisoned runtime data.
	for _, rel := range stagedEvidence {
		t.Run(rel, func(t *testing.T) {
			h := newMoveHome(t)
			stagedPoison(h, true)
			target := "missing-evidence"
			if strings.HasPrefix(rel, "bin/") || strings.HasPrefix(rel, "admin/") {
				target = "../current/" + filepath.Base(rel)
			}
			h.Link(target, filepath.Join(h.New, rel))
			sh, finish := stagedOpsShell(t, h)
			r := sh.Run(docSection(t, upgradeSkill, "Choose the upgrade path first").Block("ad_upgrade_root="))
			if r.Code != 0 || r.Stdout != "file-only\n" {
				t.Fatalf("document classification: %+v", r)
			}
			finish()
		})
	}
}

// Both documented exceptional recoveries use the same non-purge teardown.
// Substitute only its offline download, then execute the literal shell line
// and the shared classifier under whole-descendant observation.
func stagedDocTeardown(t *testing.T, h *moveHome, section *opsSection, tagPlaceholder string) {
	t.Helper()
	command := section.Fill(section.Block("curl -fL -o ./ad-uninstall.sh"), tagPlaceholder, "v"+moveVersion)
	lines := strings.Split(command, "\n")
	wantDownload := "curl -fL -o ./ad-uninstall.sh https://raw.githubusercontent.com/gabemahoney/agent-director/v" + moveVersion + "/skills/install-agent-director/uninstall.sh"
	if len(lines) != 2 || lines[0] != wantDownload || lines[1] != "bash ./ad-uninstall.sh" {
		t.Fatalf("unexpected non-purge recipe: %q", command)
	}
	script, err := filepath.Abs("../../skills/install-agent-director/uninstall.sh")
	stagedMust(t, err)
	h.Copy(script, filepath.Join(h.Home, "ad-uninstall.sh"), 0700)
	sh, finish := stagedOpsShell(t, h)
	for _, entry := range h.command().Env {
		k, v, ok := strings.Cut(entry, "=")
		if ok && strings.HasPrefix(k, "INSTALL_SH_TEST_") {
			if r := sh.Run("export " + k + "=" + moveQuote(v)); r.Code != 0 {
				t.Fatal(r)
			}
		}
	}
	if r := sh.Run(lines[1]); r.Code != 0 {
		t.Fatalf("document non-purge teardown: %+v", r)
	}
	if r := sh.Run(docSection(t, upgradeSkill, "Choose the upgrade path first").Block("ad_upgrade_root=")); r.Code != 0 || r.Stdout != "exempt\n" {
		t.Fatalf("post-teardown classifier: %+v", r)
	}
	finish()
	for _, rel := range stagedEvidence {
		if _, err := os.Lstat(filepath.Join(h.New, rel)); !os.IsNotExist(err) {
			t.Fatalf("teardown left classifier evidence %s: %v", rel, err)
		}
	}
}

// DR2 continues the ORIGINAL exempt workflow after its publication result was
// lost. Only the retry and its file-only verification run in the zero-access
// interval; the saved journal settlement and step 6 follow afterwards.
func (f *upgradeFollow) publicationRecovery(failed moveResult) {
	t, h := f.t, f.h
	t.Helper()
	docSection(t, upgradeSkill, "Exempt upgrade").Pin("Run only one installer at a time throughout the exempt attempt and its verification, recovery or restore settlement.")
	section := docSection(t, upgradeSkill, "Publication failure after an exempt install")
	section.Pin("all stopped writers and launchers stay excluded, along with a second installer.", "Retain its pre-install schema result", "run [the file classifier]", "Do not run step 4's generic `list` block", "restart every stopped agent, `serve`, caller and launcher, parents before children.")
	if !strings.Contains(failed.Stderr, `"publication_state":"unknown"`) {
		t.Fatalf("not an uncertain first publication: %+v", failed)
	}
	if r := f.must(docSection(t, upgradeSkill, "Choose the upgrade path first").Block("ad_upgrade_root=")); r.Stdout != "file-only\n" {
		t.Fatalf("publication did not leave layout evidence: %+v", r)
	}
	later := docSection(t, upgradeSkill, "File-only upgrade")
	run := func(command string) moveResult {
		cmd := h.command()
		cmd.Args = []string{h.S.Bash, "-c", command}
		r, _ := stagedObserveCmd(t, h, cmd)
		if r.Code != 0 {
			t.Fatalf("file-only continuation: %+v", r)
		}
		return r
	}
	retried := run(f.releaseCommand(later, false))
	if !strings.Contains(retried.Stdout, "reused=true") || !strings.Contains(retried.Stdout, "not inspected (file-only upgrade)") {
		t.Fatalf("retry did not recover the original publication: %+v", retried)
	}
	run(later.Block("agent-director version"))
	if f.crossing {
		if !f.ask(askVerify) {
			t.Fatal("operator did not approve original journal settlement")
		}
		command := section.Fill(section.Block("migrate-journal"), "<store>", moveQuote(f.store), "<new-absolute-directory>", moveQuote(f.c.newBackup()))
		r := f.must(command)
		f.journalVerifications++
		var answer struct{ Authority string }
		if json.Unmarshal([]byte(r.Stdout), &answer) != nil || answer.Authority != "sqlite" {
			t.Fatalf("original exempt journal verification: %+v", r)
		}
	}
}
