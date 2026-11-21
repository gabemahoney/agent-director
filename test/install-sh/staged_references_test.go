package installsh_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/layout"
)

func TestStagedReferencesArePhysicalNoops(t *testing.T) {
	for _, backend := range []string{"cron", "systemd", "piped-cache"} {
		t.Run(backend, func(t *testing.T) {
			h := newMoveHome(t)
			pathDir := filepath.Join(h.Home, "path")
			stagedMust(t, os.Mkdir(pathDir, 0700))
			h.Flags = []string{"--symlink-dir", pathDir, "--register-mcp", "--schedule"}
			ownedClaude(t, h, "", "current-user")
			h.Write(filepath.Join(h.Home, ".claude/settings.json"), "{\n  \"unrelated\": {\"kept\": true}\n}\n", 0640)
			stub := schedStub{h}
			if backend == "cron" {
				stub.Cron("# user-owned cadence\n")
			} else {
				stub.Systemctl(0)
			}
			if backend == "piped-cache" {
				s := *h.S
				s.Install = schedScript(t, h, false)
				h.S = &s
				stub.Curl()
			}
			if backend == "piped-cache" {
				if r := schedRun(t, h, h.S.Install, true); r.Code != 0 {
					t.Fatalf("piped setup: %+v", r)
				}
			} else {
				moveMustInstall(t, h)
			}
			// Write-through links and noncanonical whitespace/comments are already
			// effective references; a later no-op must retain their physical files.
			settings := filepath.Join(h.Home, ".claude/settings.json")
			cfg := filepath.Join(h.New, "config.toml")
			for _, p := range []string{settings, cfg} {
				stagedMust(t, os.Rename(p, p+".target"))
				h.Link(filepath.Base(p)+".target", p)
			}
			h.Write(cfg+".target", "\ufeff# user comment\r\n[ defaults ] # existing\r\ninject_help_hook = true # keep spelling\r\n[store]\r\nbusy_timeout_ms = 0\r\n", 0640)
			before := stagedSnapshot(t, h, ".claude", moveNewRel+"/config.toml", "path", ".config/systemd/user", moveNewRel+"/schedule")
			settingsNames := ownedDirNames(t, filepath.Dir(settings))
			configNames := mustGlob(t, cfg+".bak.*")
			cronBefore, _ := stub.Crontab()
			cronWrites, sysCalls, mcpCalls := stub.CronWrites(), len(stub.SystemctlCalls()), len(ownedCalls(t, h))
			run := func(cmd *exec.Cmd) moveResult {
				r, _ := stagedObserveCmd(t, h, cmd)
				return r
			}
			r := schedRunArgv(t, h, h.S.Install, backend == "piped-cache", schedArgv(h), run)
			if r.Code != 0 {
				t.Fatalf("later reference install: %+v", r)
			}
			before.Unchanged(t, h)
			if fmt.Sprint(settingsNames) != fmt.Sprint(ownedDirNames(t, filepath.Dir(settings))) || fmt.Sprint(configNames) != fmt.Sprint(mustGlob(t, cfg+".bak.*")) {
				t.Fatal("no-op created a settings/config backup")
			}
			if got, _ := stub.Crontab(); got != cronBefore || stub.CronWrites() != cronWrites {
				t.Fatal("no-op rewrote crontab")
			}
			for _, call := range stub.SystemctlCalls()[sysCalls:] {
				for _, bad := range []string{"daemon-reload", "restart", " enable ", " start "} {
					if strings.Contains(call, bad) {
						t.Errorf("redundant systemd mutation: %s", call)
					}
				}
			}
			for _, call := range ownedMCPCalls(ownedCalls(t, h)[mcpCalls:]) {
				if call.Args[1] != "get" {
					t.Errorf("redundant MCP mutation: %s", call.line())
				}
			}
			// Explicitly requested disabled timers are repaired without replacing
			// identical unit bytes. A changed setting is still written through.
			if backend == "systemd" {
				h.Env["SYSCTL_STUB_FAIL"] = "is-active"
				n := len(stub.SystemctlCalls())
				moveMustInstall(t, h)
				calls := strings.Join(stub.SystemctlCalls()[n:], "\n")
				if !strings.Contains(calls, "--user start ") || strings.Contains(calls, "daemon-reload") || strings.Contains(calls, "restart") {
					t.Fatalf("inactive timer repair: %s", calls)
				}
			}
			h.Write(cfg+".target", "# keep user comment\n[defaults]\ninject_help_hook = false\n", 0640)
			moveMustInstall(t, h)
			if !strings.Contains(h.migRead(cfg), "inject_help_hook = true") {
				t.Fatal("requested help-hook repair did not run")
			}
			if target, err := os.Readlink(cfg); err != nil || target != "config.toml.target" {
				t.Fatalf("repair broke write-through link: %q %v", target, err)
			}
		})
	}
}

func TestStagedSourcesAndCapability(t *testing.T) {
	for _, later := range []bool{false, true} {
		t.Run(fmt.Sprintf("malformed-capability/later=%v", later), func(t *testing.T) {
			h := newMoveHome(t)
			if later {
				moveMustInstall(t, h)
			}
			f := newOutcomeFixture(t, h, "version-facts")
			f.Choose("canned")
			h.Write(f.Control+".response", "printf '{}'; exit 0\n", 0600)
			before := h.PhysicalSnapshot()
			r := h.Install()
			if r.Code != 3 {
				t.Fatalf("partially advertised capability: %+v", r)
			}
			if d := moveDiff(before, h.PhysicalSnapshot()); d != "" {
				t.Fatal(d)
			}
		})
	}
	for _, later := range []bool{false, true} {
		t.Run(fmt.Sprintf("later=%v", later), func(t *testing.T) {
			h := newMoveHome(t)
			if later {
				moveMustInstall(t, h)
			}
			releasePair := h.Pair
			stub := schedStub{h}
			stub.Curl()
			h.Link("curl-stub", filepath.Join(h.Tools, "curl")) // release preflight also requires curl on PATH
			h.Pair = movePair{}                                 // checksums are a release-source option
			sha := func(p string) string {
				b, err := os.ReadFile(p)
				stagedMust(t, err)
				return fmt.Sprintf("%x", sha256.Sum256(b))
			}
			goodRuntime, goodAdmin := sha(releasePair.Main), sha(releasePair.Admin)
			for _, flag := range []string{"--sha256", "--admin-sha256"} {
				before := h.PhysicalSnapshot()
				runtimeSum, adminSum := goodRuntime, goodAdmin
				if flag == "--sha256" {
					runtimeSum = strings.Repeat("0", 64)
				} else {
					adminSum = strings.Repeat("0", 64)
				}
				r := h.Install("--from-release", moveVersion, "--sha256", runtimeSum, "--admin-sha256", adminSum)
				if r.Code != 3 {
					t.Fatalf("checksum refusal: %+v", r)
				}
				if d := moveDiff(before, h.PhysicalSnapshot()); d != "" {
					t.Fatal(d)
				}
			}
			r := h.Install("--from-release", moveVersion, "--sha256", goodRuntime, "--admin-sha256", goodAdmin)
			if r.Code != 0 {
				t.Fatalf("matching checksums: %+v", r)
			}
			// The candidate may itself be the permanent entry whose current
			// selection the publication replaces; freeze both sources first.
			h.Pair = movePair{filepath.Join(h.New, "bin/agent-director"), filepath.Join(h.New, "admin/agent-director-admin")}
			r, _ = stagedObserve(t, h)
			if r.Code != 0 {
				t.Fatalf("canonical source lifetime: %+v", r)
			}
			stagedLayout(t, h, moveVersion)
			// Existing --from-release download fixture, with both actual assets.
			h.Pair = stagedPair(t)
			stub.Curl()
			h.Pair = movePair{}
			r, _ = stagedObserve(t, h, "--from-release", stagedStamp)
			if r.Code != 0 {
				t.Fatalf("later release source: %+v", r)
			}
			stagedLayout(t, h, stagedStamp)
		})
	}
	for _, later := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy-candidate/later=%v", later), func(t *testing.T) {
			h := newMoveHome(t)
			if later {
				moveMustInstall(t, h)
			}
			r, err := releaseBuildOf("v0.18.4")
			stagedMust(t, err)
			h.Pair = r.Pair
			if later {
				before := h.PhysicalSnapshot()
				got, _ := stagedObserve(t, h)
				if got.Code != 3 {
					t.Fatalf("legacy over layout: %+v", got)
				}
				if d := moveDiff(before, h.PhysicalSnapshot()); d != "" {
					t.Fatal(d)
				}
			} else {
				got := h.Install()
				if got.Code != 0 || !strings.Contains(got.Stdout, "predates versioned installs") {
					t.Fatalf("exempt legacy candidate: %+v", got)
				}
				for _, rel := range stagedEvidence[:6] {
					if _, err := os.Lstat(filepath.Join(h.New, rel)); !os.IsNotExist(err) {
						t.Fatalf("legacy created %s", rel)
					}
				}
			}
		})
	}
}

func TestStagedAncillaryFailurePreservesPublication(t *testing.T) {
	for _, failure := range []string{"hooks", "mcp", "schedule"} {
		t.Run(failure, func(t *testing.T) {
			h := newMoveHome(t)
			moveMustInstall(t, h)
			paths := stagedPoison(h, true)
			before := stagedSnapshot(t, h, append(paths, moveNewRel+"/versions", moveNewRel+"/install-contract.json", moveNewRel+"/install-initialization.json", moveNewRel+"/install.lock")...)
			h.Pair = stagedPair(t)
			want := 4
			switch failure {
			case "hooks":
				h.Flags = []string{"--no-symlink"}
				h.Write(filepath.Join(h.Home, ".claude/settings.json"), "{invalid", 0600)
			case "mcp":
				want = 6
				h.Flags = append(h.Flags, "--register-mcp")
				h.WriteTool("claude", `if [[ "$1" == --version ]]; then echo '2.1.296 (Claude Code)'; exit 0; fi; echo 'injected MCP failure' >&2; exit 1`)
			case "schedule":
				want = 0
				stub := schedStub{h}
				stub.Systemctl(0)
				h.Env["SYSCTL_STUB_FAIL"] = "daemon-reload"
				h.Flags = append(h.Flags, "--schedule")
			}
			r, _ := stagedObserve(t, h)
			if r.Code != want {
				t.Fatalf("ancillary exit wanted %d: %+v", want, r)
			}
			before.Unchanged(t, h)
			// A scheduling failure is an existing warning-only outcome.
			if failure == "schedule" && !strings.Contains(r.Stderr, "--schedule was asked for and installed nothing") {
				t.Fatalf("missing schedule warning: %+v", r)
			}
			// Every injected ancillary failure here follows publication.
			stagedLayout(t, h, stagedStamp)
		})
	}
}

func stagedUninstall(t *testing.T, h *moveHome) moveResult {
	t.Helper()
	script, err := filepath.Abs("../../skills/install-agent-director/uninstall.sh")
	stagedMust(t, err)
	cmd := h.command()
	cmd.Args = []string{h.S.Bash, script}
	r, trace := stagedStartTrace(t, h, cmd).Finish(t)
	if hits := stagedForbidden(trace, stagedRuntime(t, h).process.Pid); len(hits) != 0 {
		t.Fatalf("uninstall attempted runtime access: %v", hits)
	}
	return r
}

func TestStagedUninstallRemovesAllEvidence(t *testing.T) {
	for _, shape := range []string{"complete", "partial", "dangling", "external"} {
		t.Run(shape, func(t *testing.T) {
			h := newMoveHome(t)
			if shape == "complete" || shape == "partial" {
				moveMustInstall(t, h)
				h.Pair = stagedPair(t)
				moveMustInstall(t, h)
				h.Write(filepath.Join(h.New, "versions/.staging-interrupted/payload"), "unfinished", 0600)
				h.Copy(filepath.Join(h.New, layout.InstallContract), filepath.Join(h.New, ".tmp-123"), 0600)
				h.Link("versions/"+layout.VersionID(moveVersion), filepath.Join(h.New, ".tmp-link-123"))
				h.Link("../current/agent-director", filepath.Join(h.New, "bin/.tmp-link-456"))
				h.Link("../current/agent-director-admin", filepath.Join(h.New, "admin/.tmp-link-789"))
				if shape == "partial" {
					stagedMust(t, os.Remove(filepath.Join(h.New, "current")))
					stagedMust(t, os.Remove(filepath.Join(h.New, layout.InstallInitialization)))
				}
			} else {
				for _, rel := range stagedEvidence {
					target := filepath.Join(h.Home, "external", strings.ReplaceAll(rel, "/", "-"))
					if shape == "external" {
						h.Write(filepath.Join(target, "user-file"), "do not follow layout links", 0600)
					}
					h.Link(target, filepath.Join(h.New, rel))
				}
			}
			paths := stagedPoison(h, true)
			for _, rel := range []string{"templates/user.toml", "transcripts/user.jsonl", "unrelated", ".tmp-999", ".tmp-link-user"} {
				h.Write(filepath.Join(h.New, rel), "unrelated retained data", 0600)
				paths = append(paths, moveNewRel+"/"+rel)
			}
			paths = append(paths, "external", moveNewRel+"/config.toml")
			before := stagedSnapshot(t, h, paths...)
			r := stagedUninstall(t, h)
			if r.Code != 0 {
				t.Fatalf("uninstall: %+v", r)
			}
			before.Unchanged(t, h)
			for _, rel := range append(append([]string{}, stagedEvidence...), ".tmp-123", ".tmp-link-123", "bin/.tmp-link-456", "admin/.tmp-link-789") {
				if _, err := os.Lstat(filepath.Join(h.New, rel)); !os.IsNotExist(err) {
					t.Errorf("classifier/owned resource survived: %s (%v)", rel, err)
				}
			}
			if r := stagedUninstall(t, h); r.Code != 0 {
				t.Fatalf("no-files rerun: %+v", r)
			}
		})
	}
}

func TestStagedRetainedCustomStoreExemptRecovery(t *testing.T) {
	h := newMoveHome(t)
	r, err := releaseBuildOf("v0.18.4")
	stagedMust(t, err)
	db := filepath.Join(h.Home, "outside/external-runtime.sqlite")
	h.Write(filepath.Join(h.New, "config.toml"), fmt.Sprintf("[store]\ndb_path = %q\n", db), 0600)
	h.InstallRelease(r)
	if h.migStoreVersion(db) != "20" {
		t.Fatal("expected genuine custom v20 store")
	}
	h.Sqlite(db, "INSERT INTO spawns(instance_id,state,cwd,tmux_session_name,relay_mode,agent) VALUES('retained','ended','/work','retained','off','codex');")
	// A layout does not attest this custom store. Establish only the program
	// layout as setup, preserving the historical database intentionally.
	res := h.Run(h.S.Current.Admin, "--home", h.Home, "publish-version", "--binary", h.S.Current.Main)
	if res.Code != 0 {
		t.Fatalf("layout setup: %+v", res)
	}
	before := stagedSnapshot(t, h, "outside", moveNewRel+"/config.toml")
	res, _ = stagedObserve(t, h)
	if res.Code != 0 {
		t.Fatalf("file-only below-v21: %+v", res)
	}
	before.Unchanged(t, h)
	// Follow the exceptional recovery with explicit operator authorization.
	// Every fixture row is ended and no writer holds the store. Inventory uses
	// the matching previous executable, as the document instructs when the
	// current entry cannot read that older schema.
	recovery := opsDocSection(t, "An old configured store under a versioned layout")
	recovery.Pin("with operator authorization and all writers stopped.", "Preserve the same store, sidecars, config and `db_path`; do not choose a replacement DB.", "Continue at exempt step 3 with exclusion still in force", "restart **every** agent, `serve` and caller, parents before children.")
	sh := newOpsShell(h)
	schema := docSection(t, upgradeSkill, "Read the store schema")
	res = sh.Run(schema.Fill(schema.Block("PRAGMA user_version;"), "<store>", moveQuote(db)))
	if res.Code != 0 || res.Stdout != "20\n" {
		t.Fatalf("pre-teardown schema: %+v", res)
	}
	inventory := docSection(t, upgradeSkill, "Check the agents and holders").Block("agent-director list --state")
	inventory = strings.Replace(inventory, "~/.config/agent-director/bin/agent-director", moveQuote(r.Pair.Main), 1)
	res = sh.Run(inventory)
	if res.Code != 0 || strings.TrimSpace(res.Stdout) != `{"spawns":[]}` {
		t.Fatalf("old-release stopped-agent inventory: %+v", res)
	}
	h.Release() // schema/inventory reads end before teardown observation
	stagedDocTeardown(t, h, recovery, "<tag>")
	before.Unchanged(t, h)
	sh = newOpsShell(h)
	follow := &upgradeFollow{t: t, h: h}
	res = sh.RunWithin(follow.releaseCommand(recovery, false), upgradeInstallWait)
	if res.Code != 0 {
		t.Fatalf("document exempt reinstall: %+v", res)
	}
	h.Release()
	outcomeLines(t, res.Stdout, "  schema  : authorized migration v20→v21 (sentinel "+filepath.Join(filepath.Dir(db), "migrate-authorized")+")")
	if h.migStoreVersion(db) != "21" || h.Sqlite(db, "SELECT instance_id FROM spawns;") != "retained\n" {
		t.Fatal("exempt recovery did not retain and upgrade the same custom store")
	}
	stagedLayout(t, h, moveVersion)
}
