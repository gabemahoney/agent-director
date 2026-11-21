package installsh_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/gabemahoney/agent-director/internal/layout"
	"github.com/gabemahoney/agent-director/internal/store"
)

func TestStagedExemptLayoutAfterSettlement(t *testing.T) {
	for _, kind := range []string{"fresh", "v20", "canonical-sources", "refused-before-settlement"} {
		t.Run(kind, func(t *testing.T) {
			h := newMoveHome(t)
			if kind == "v20" {
				r, err := releaseBuildOf("v0.18.4")
				stagedMust(t, err)
				h.InstallRelease(r)
				if h.migStoreVersion(filepath.Join(h.New, "state.db")) != "20" {
					t.Fatal("not a genuine v20 store")
				}
			}
			if kind == "canonical-sources" {
				h.Copy(h.Pair.Main, filepath.Join(h.New, "bin/agent-director"), 0755)
				h.Copy(h.Pair.Admin, filepath.Join(h.New, "admin/agent-director-admin"), 0755)
				h.Pair = movePair{filepath.Join(h.New, "bin/agent-director"), filepath.Join(h.New, "admin/agent-director-admin")}
				moveMustInstall(t, h)
				stagedLayout(t, h, moveVersion)
				return
			}
			if kind == "refused-before-settlement" {
				h.Write(filepath.Join(h.New, "state.db"), "invalid SQLite header", 0600)
				before := h.PhysicalSnapshot()
				r := h.Install()
				if r.Code != 5 {
					t.Fatalf("expected pre-settlement refusal: %+v", r)
				}
				if d := moveDiff(before, h.PhysicalSnapshot()); d != "" {
					t.Fatal(d)
				}
				return
			}
			f := newOutcomeFixture(t, h, "publish-version")
			f.Block("real")
			r := h.StartInstall()
			f.Await()
			// Paused before calling the publication engine: the actual schema
			// settlement has completed and no classifier evidence exists yet.
			if v := h.migStoreVersion(filepath.Join(h.New, "state.db")); v != "21" {
				t.Errorf("publication preceded schema settlement: v%s", v)
			}
			if _, err := os.Lstat(filepath.Join(h.New, "migrate-authorized")); !os.IsNotExist(err) {
				t.Errorf("sentinel not consumed: %v", err)
			}
			for _, rel := range stagedEvidence[:6] {
				if _, err := os.Lstat(filepath.Join(h.New, rel)); !os.IsNotExist(err) {
					t.Errorf("premature layout evidence %s: %v", rel, err)
				}
			}
			f.Continue()
			if res := r.Wait(t); res.Code != 0 {
				t.Fatalf("exempt install: %+v", res)
			}
			stagedLayout(t, h, moveVersion)
		})
	}
}

func TestStagedLayoutEvidenceNeverFallsBack(t *testing.T) {
	for _, rel := range stagedEvidence {
		for _, dangling := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/dangling=%v", rel, dangling), func(t *testing.T) {
				h := newMoveHome(t)
				paths := stagedPoison(h, true) // ambiguous old/new roots must not diagnose DBs
				p := filepath.Join(h.New, rel)
				switch {
				case strings.HasPrefix(rel, "bin/") || strings.HasPrefix(rel, "admin/"):
					target := "../current/" + filepath.Base(rel)
					if !dangling {
						target = "../versions/" + layout.VersionID(moveVersion) + "/" + filepath.Base(rel)
						h.Write(filepath.Join(h.New, strings.TrimPrefix(target, "../")), "incomplete", 0700)
					}
					h.Link(target, p)
				case dangling:
					h.Link("missing-layout-evidence", p)
				case rel == "versions" || rel == "publications":
					stagedMust(t, os.MkdirAll(p, 0700))
				default:
					h.Write(p, "incomplete establishment", 0600)
				}
				before := stagedSnapshot(t, h, paths...)
				r, _ := stagedObserve(t, h)
				if r.Code != 0 && (r.Code != 5 || r.Last() != "install.sh: err_name=ErrVersionInstallRefused") {
					t.Fatalf("evidence fell back or wrong refusal: %+v", r)
				}
				if r.Code == 0 {
					outcomeLines(t, r.Stdout, "  schema  : not inspected (file-only upgrade)")
				}
				before.Unchanged(t, h)
			})
		}
	}
}

func TestStagedLaterInstallNeverTouchesStore(t *testing.T) {
	// Calibrate swallowed failed child opens, metadata probes and a timestamp
	// write before using the observer to claim zero attempted access.
	h := newMoveHome(t)
	cmd := h.command()
	cmd.Args = []string{h.S.Bash, "-c", `bash -c 'cat "$1" >/dev/null 2>&1 || :; stat "$2" >/dev/null 2>&1 || :; touch "$3"' child "$HOME/state.db-wal" "$HOME/external-runtime.sqlite-shm" "$HOME/state.db-journal"`}
	r, trace := stagedStartTrace(t, h, cmd).Finish(t)
	if r.Code != 0 {
		t.Fatalf("observer calibration: %+v", r)
	}
	for _, want := range []string{"state.db-wal", "external-runtime.sqlite-shm", "state.db-journal"} {
		if !strings.Contains(strings.Join(stagedForbidden(trace, stagedRuntime(t, h).process.Pid), "\n"), want) {
			t.Fatalf("observer missed %s", want)
		}
	}
	// XA1: real, harmless probes against only this case's private runtime.
	// These cover a shell builtin, a direct tgkill syscall and /proc access;
	// a direct self SIGURG must remain allowed like Go's preemption signals.
	for _, kind := range []string{"builtin-kill-zero", "tgkill-zero", "proc-stat", "self-sigurg"} {
		t.Run("observer/"+kind, func(t *testing.T) {
			h := newMoveHome(t)
			pid := stagedRuntime(t, h).process.Pid
			command, want := "", ""
			switch kind {
			case "builtin-kill-zero":
				command = fmt.Sprintf("kill -0 %d", pid)
				want = fmt.Sprintf("kill(%d, 0)", pid)
			case "tgkill-zero":
				command = "python3 -c " + moveQuote(fmt.Sprintf("import ctypes; assert ctypes.CDLL(None).tgkill(%d,%d,0)==0", pid, pid))
				want = fmt.Sprintf("tgkill(%d, %d, 0)", pid, pid)
			case "proc-stat":
				command = fmt.Sprintf("cat /proc/%d/stat >/dev/null", pid)
				want = fmt.Sprintf("/proc/%d/stat", pid)
			case "self-sigurg":
				command = "python3 -c " + moveQuote("import ctypes,os,signal; assert ctypes.CDLL(None).tgkill(os.getpid(),os.getpid(),signal.SIGURG)==0")
			}
			cmd := h.command()
			cmd.Args = []string{h.S.Bash, "-c", command}
			r, trace := stagedStartTrace(t, h, cmd).Finish(t)
			if r.Code != 0 {
				t.Fatalf("process observer calibration: %+v", r)
			}
			hits := stagedForbidden(trace, pid)
			if want != "" && !strings.Contains(strings.Join(hits, "\n"), want) {
				t.Fatalf("observer missed %s probe %s", kind, want)
			}
			if want == "" && (len(hits) != 0 || !strings.Contains(trace, "tgkill(") || !strings.Contains(trace, "SIGURG")) {
				t.Fatalf("self signal wrongly rejected or not exercised: %v", hits)
			}
		})
	}
	for _, custom := range []bool{false, true} {
		t.Run(fmt.Sprintf("custom=%v", custom), func(t *testing.T) {
			h := newMoveHome(t)
			moveMustInstall(t, h)
			paths := stagedPoison(h, custom)
			before := stagedSnapshot(t, h, append(paths, filepath.Join(moveNewRel, "install.lock"), filepath.Join(moveNewRel, "install-contract.json"), filepath.Join(moveNewRel, "install-initialization.json"), filepath.Join(moveNewRel, "versions"), filepath.Join(moveNewRel, "publications"))...)
			h.Pair = stagedPair(t)
			for i := 0; i < 2; i++ {
				r, trace := stagedObserve(t, h, "--keep-prior", "--journal-maintenance-window", "--journal-roots-complete", "--journal-backup", filepath.Join(h.Home, "unused-backup"), "--journal-legacy-root", filepath.Join(h.Old, "input"))
				if r.Code != 0 {
					t.Fatalf("later install: %+v", r)
				}
				outcomeLines(t, r.Stdout, "  schema  : not inspected (file-only upgrade)")
				if !strings.Contains(r.Stdout, fmt.Sprintf("reused=%v", i == 1)) || !strings.Contains(r.Stdout, "unused") {
					t.Fatalf("wrong later summary: %s", r.Stdout)
				}
				if i == 0 && !strings.Contains(trace, "--install-version-facts") {
					t.Fatal("observer missed candidate's staged child")
				}
				before.Unchanged(t, h)
			}
			stagedLayout(t, h, stagedStamp)
			// Holder-only returns before capability, publication or ancillary
			// work, even with broken entries and a nonexistent candidate.
			stagedMust(t, os.Remove(filepath.Join(h.New, "current")))
			h.Pair = movePair{"/no/candidate", "/no/admin"}
			all := h.PhysicalSnapshot()
			r, _ := stagedObserve(t, h, "--check-holders", "--schedule", "--register-mcp")
			if r.Code != 0 || !strings.Contains(r.Stdout, "holder check is unnecessary for file-only upgrades; nothing was installed or changed. Store schema was not inspected.") {
				t.Fatalf("holder-only: %+v", r)
			}
			if d := moveDiff(all, h.PhysicalSnapshot()); d != "" {
				t.Fatal(d)
			}
		})
	}
}

// b.ett: run the real publication engine through install.sh, not a canned refusal.
func TestStagedCoreMismatchIdentities(t *testing.T) {
	h := newMoveHome(t)
	repo, err := filepath.Abs("../..")
	stagedMust(t, err)
	original := filepath.Join(repo, "internal/store/busy_send_schema.go")
	data, err := os.ReadFile(original)
	stagedMust(t, err)
	if strings.Count(string(data), "busy-send-core-v1:") != 1 {
		t.Fatal("core variant replacement no longer unique")
	}
	changed := filepath.Join(h.Root, "core.go")
	h.Write(changed, strings.Replace(string(data), "busy-send-core-v1:", "busy-send-core-test:", 1), 0600)
	overlay, err := json.Marshal(map[string]any{"Replace": map[string]string{original: changed}})
	stagedMust(t, err)
	overlayPath := filepath.Join(h.Root, "overlay.json")
	h.Write(overlayPath, string(overlay), 0600)
	flags := fmt.Sprintf("-X %s.Version=%s-anchor -X %s.Commit=%s", moveVersPkg, moveVersion, moveVersPkg, moveCommit)
	runtime, err := moveBuildBinary(repo, filepath.Join(h.Root, "agent-director"), flags, "./cmd/agent-director", "-overlay", overlayPath)
	stagedMust(t, err)
	admin, err := moveBuildBinary(repo, filepath.Join(h.Root, "agent-director-admin"), flags, "./cmd/agent-director-admin", "-overlay", overlayPath)
	stagedMust(t, err)
	if r := h.Run(admin, "--home", h.Home, "publish-version", "--binary", runtime); r.Code != 0 {
		t.Fatalf("anchor publication: %+v", r)
	}
	r := h.Install()
	if r.Code != 5 || r.Last() != "install.sh: err_name=ErrVersionInstallRefused" {
		t.Fatalf("publication mapping: %+v", r)
	}
	lines := preflightEnvelopes(t, r, "ErrVersionInstallRefused")
	if len(lines) != 1 {
		t.Fatalf("publication envelopes: %v", lines)
	}
	var envelope struct {
		Details map[string]json.RawMessage `json:"details"`
	}
	stagedMust(t, json.Unmarshal([]byte(lines[0]), &envelope))
	var reason string
	stagedMust(t, json.Unmarshal(envelope.Details["reason"], &reason))
	if reason != "core_mismatch" {
		t.Fatalf("reason = %q", reason)
	}
	core, digest := store.BusySendCoreContractIdentity()
	for key, version := range map[string]string{"anchored_core": "busy-send-core-test:", "candidate_core": core} {
		want := map[string]string{"core_version": version, "core_declarations_sha256": digest}
		var got map[string]string
		if raw, ok := envelope.Details[key]; ok {
			stagedMust(t, json.Unmarshal(raw, &got))
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("relayed details.%s = %v, want %v", key, got, want)
		}
	}
}

func TestStagedPublicationOutcomes(t *testing.T) {
	for _, reason := range []string{"lock_busy", "core_mismatch", "coverage_regression", "version_conflict", "layout_invalid", "io_failure", "empty", "malformed", "unexpected-stdout"} {
		t.Run(reason, func(t *testing.T) {
			h := newMoveHome(t)
			moveMustInstall(t, h)
			paths := stagedPoison(h, true)
			before := stagedSnapshot(t, h, append(paths, moveNewRel+"/versions", moveNewRel+"/publications", moveNewRel+"/current", moveNewRel+"/install-contract.json", moveNewRel+"/install-initialization.json", moveNewRel+"/install.lock")...)
			f := newOutcomeFixture(t, h, "publish-version")
			f.Choose("canned")
			identities := ""
			if reason == "core_mismatch" {
				// b.ett: install.sh must relay both identities verbatim, including extra details.
				identities = `,"anchored_core":{"core_version":"anchor","core_declarations_sha256":"` + strings.Repeat("a", 64) + `"},"candidate_core":{"core_version":"candidate","core_declarations_sha256":"` + strings.Repeat("b", 64) + `"}`
			}
			envelope := fmt.Sprintf(`{"err_name":"ErrVersionInstallRefused","err_description":"chosen refusal","details":{"reason":%q,"retryable":%v,"publication_state":"not_published","durable":false,"recovered_version":"earlier","extension":{"kept":true}%s}}`, reason, reason == "lock_busy" || reason == "io_failure", identities)
			body := "printf '%s' " + moveQuote(envelope) + " >&2; exit 1\n" // no terminating LF, relayed exactly
			unknown := reason == "empty" || reason == "malformed" || reason == "unexpected-stdout"
			if reason == "empty" {
				body = "exit 0\n"
			}
			if reason == "malformed" {
				body = "printf '{broken' >&2; exit 1\n"
			}
			if reason == "unexpected-stdout" {
				body = "echo unexpected; " + body
			}
			h.Write(f.Control+".response", body, 0600)
			r, _ := stagedObserve(t, h)
			if r.Code != 5 || r.Last() != "install.sh: err_name=ErrVersionInstallRefused" {
				t.Fatalf("publication mapping: %+v", r)
			}
			if unknown {
				outcomeLines(t, r.Stderr, `{"err_name":"ErrVersionInstallRefused","err_description":"admin publication result unreadable; rerun the same install for forward recovery","details":{"publication_state":"unknown"}}`)
				if strings.Contains(r.Stderr, `"durable"`) || strings.Contains(r.Stderr, `"reason"`) {
					t.Fatalf("invented outcome: %s", r.Stderr)
				}
			} else if !strings.Contains(r.Stderr, envelope+"\n") {
				t.Fatalf("envelope changed: %s", r.Stderr)
			}
			calls := h.migRead(f.Calls)
			if !strings.Contains(calls, "--home "+h.Home+" publish-version --binary ") || strings.Contains(calls, "--store-path") {
				t.Fatalf("wrong candidate invocation: %s", calls)
			}
			before.Unchanged(t, h)
			if unknown {
				// Literally rerun the SAME install after the unreadable outcome
				// clears; no rollback, store verification or migration occurs.
				f.Clear()
				r, _ = stagedObserve(t, h)
				if r.Code != 0 {
					t.Fatalf("literal same-install recovery: %+v", r)
				}
			}
		})
	}
}

func TestStagedBootstrapRerun(t *testing.T) {
	for _, point := range []string{"after:bootstrap.runtime-entry.rename", "after:bootstrap.admin-entry.rename"} {
		t.Run(point, func(t *testing.T) {
			h := newMoveHome(t)
			f := newOutcomeFixture(t, h, "publish-version")
			h.Pair = stagedPair(t)
			fault := stagedFault(t, h, f, point, "exit")
			r := h.StartInstall()
			f.Await()
			// Process interruption proves forward restart, not power-loss durability.
			for _, rel := range []string{"bin/agent-director", "admin/agent-director-admin"} {
				got := h.Run(filepath.Join(h.New, rel), "version")
				if got.Code != 0 || !strings.Contains(got.Stdout, stagedStamp) {
					t.Fatalf("intermediate entry unusable: %+v", got)
				}
			}
			f.Continue()
			if got := r.Wait(t); got.Code != 5 {
				t.Fatalf("crashed publication: %+v", got)
			}
			stagedMust(t, os.Remove(fault))
			paths := stagedPoison(h, true)
			before := stagedSnapshot(t, h, append(paths, moveNewRel+"/install-contract.json", moveNewRel+"/install.lock", moveNewRel+"/publications", moveNewRel+"/versions")...)
			got, _ := stagedObserve(t, h)
			if got.Code != 0 {
				t.Fatalf("bootstrap forward rerun: %+v", got)
			}
			outcomeLines(t, got.Stdout, "  schema  : not inspected (file-only upgrade)")
			before.Unchanged(t, h)
			stagedLayout(t, h, stagedStamp)
		})
	}
}

func TestStagedOverwrittenEntries(t *testing.T) {
	for _, rel := range []string{"bin/agent-director", "admin/agent-director-admin"} {
		for _, evidence := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/evidence=%v", rel, evidence), func(t *testing.T) {
				h := newMoveHome(t)
				moveMustInstall(t, h)
				stagedMust(t, os.Remove(filepath.Join(h.New, rel)))
				h.Copy(h.S.Current.Main, filepath.Join(h.New, rel), 0755)
				if !evidence {
					stagedMust(t, os.Remove(filepath.Join(h.New, layout.InstallInitialization)))
				}
				paths := stagedPoison(h, false)
				before := stagedSnapshot(t, h, paths...)
				r, _ := stagedObserve(t, h)
				if evidence {
					if r.Code != 0 {
						t.Fatalf("repair: %+v", r)
					}
					stagedLayout(t, h, moveVersion)
				} else if r.Code != 5 || !strings.Contains(r.Stderr, `"reason":"layout_invalid"`) {
					t.Fatalf("unproved repair: %+v", r)
				}
				before.Unchanged(t, h)
			})
		}
	}
}

func TestStagedMissingCurrentAndStore(t *testing.T) {
	for _, shape := range []string{"missing-store", "missing-current", "corrupt-current"} {
		t.Run(shape, func(t *testing.T) {
			h := newMoveHome(t)
			moveMustInstall(t, h)
			h.Write(filepath.Join(h.New, "config.toml"), fmt.Sprintf("[store]\ndb_path = %q\n", filepath.Join(h.Home, "absent/external-runtime.sqlite")), 0600)
			if shape != "missing-store" {
				stagedMust(t, os.Remove(filepath.Join(h.New, "current")))
				if shape == "corrupt-current" {
					h.Write(filepath.Join(h.New, "current"), "not a version link", 0600)
				}
			}
			r, _ := stagedObserve(t, h)
			if shape == "missing-store" {
				if r.Code != 0 {
					t.Fatalf("missing custom store incorrectly blocks publication: %+v", r)
				}
				outcomeLines(t, r.Stdout, "  schema  : not inspected (file-only upgrade)")
			} else if r.Code != 5 || !strings.Contains(r.Stderr, `"reason":"layout_invalid"`) {
				t.Fatalf("invalid current: %+v", r)
			}
		})
	}
}

func TestStagedRecoveredThenRefused(t *testing.T) {
	h := newMoveHome(t)
	f := newOutcomeFixture(t, h, "publish-version")
	h.Pair = stagedPair(t)
	fault := stagedFault(t, h, f, "after:current.rename", "exit")
	r := h.StartInstall()
	f.Await()
	f.Continue()
	if got := r.Wait(t); got.Code != 5 {
		t.Fatalf("pending publication: %+v", got)
	}
	stagedMust(t, os.Remove(fault))
	paths := stagedPoison(h, true)
	before := stagedSnapshot(t, h, append(paths, moveNewRel+"/install-contract.json", moveNewRel+"/install.lock", moveNewRel+"/versions", moveNewRel+"/publications")...)
	// Still a runnable matching-stamp ELF, but not the retained bytes. The
	// real engine must recover the pending selection before refusing this pair.
	copy := filepath.Join(h.Root, "different-runtime")
	h.Copy(h.Pair.Main, copy, 0755)
	file, err := os.OpenFile(copy, os.O_APPEND|os.O_WRONLY, 0)
	stagedMust(t, err)
	_, err = file.WriteString("different candidate bytes")
	stagedMust(t, err)
	stagedMust(t, file.Close())
	h.Pair.Main = copy
	got, _ := stagedObserve(t, h)
	if got.Code != 5 || !strings.Contains(got.Stderr, `"reason":"version_conflict"`) || !strings.Contains(got.Stderr, `"recovered_version":"`+stagedStamp+`"`) {
		t.Fatalf("recovered then refused facts: %+v", got)
	}
	before.Unchanged(t, h)
	stagedLayout(t, h, stagedStamp)
}

func TestStagedSignalCleanup(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			h := newMoveHome(t)
			moveMustInstall(t, h)
			paths := stagedPoison(h, true)
			before := stagedSnapshot(t, h, append(paths, moveNewRel+"/versions", moveNewRel+"/publications", moveNewRel+"/install.lock", moveNewRel+"/install-contract.json", moveNewRel+"/install-initialization.json")...)
			f := newOutcomeFixture(t, h, "publish-version")
			h.Pair = stagedPair(t)
			fault := stagedFault(t, h, f, "after:lock", "continue")
			tr := stagedStartTrace(t, h, h.command())
			f.Await()
			tr.running.Signal(sig)
			r, trace := tr.Finish(t)
			if hits := stagedForbidden(trace, stagedRuntime(t, h).process.Pid); len(hits) != 0 {
				t.Fatalf("signal cleanup runtime access: %v", hits)
			}
			if r.Code != 5 || !strings.Contains(r.Stderr, `"publication_state":"unknown"`) {
				t.Fatalf("signal outcome: %+v", r)
			}
			before.Unchanged(t, h)
			stagedMust(t, os.Remove(fault))
			r, _ = stagedObserve(t, h)
			if r.Code != 0 {
				t.Fatalf("same-install signal recovery: %+v", r)
			}
		})
	}
}

func TestStagedProtectedPathDestinations(t *testing.T) {
	// CR1: a PATH link once overwrote a retained runtime and silently ran B
	// through A's immutable path. Protect current/retained/root/bin and aliases.
	bashes := []string{""}
	if b := os.Getenv("AD_TEST_BASH32"); b != "" {
		bashes = append(bashes, b)
	}
	for _, bash := range bashes {
		t.Run("bash="+filepath.Base(bash), func(t *testing.T) {
			h := newMoveHome(t)
			if bash != "" {
				s := *h.S
				s.Bash = bash
				h.S = &s
			}
			moveMustInstall(t, h)
			h.Pair = stagedPair(t)
			moveMustInstall(t, h)
			before := stagedSnapshot(t, h, moveNewRel+"/versions", moveNewRel+"/bin", moveNewRel+"/admin")
			h.Flags = []string{"--no-hooks"}
			for _, rel := range []string{"versions/" + layout.VersionID(moveVersion), "current", "versions", "bin"} {
				for _, alias := range []bool{false, true} {
					dest := filepath.Join(h.New, rel)
					if alias {
						link := filepath.Join(h.Home, "alias-"+strings.ReplaceAll(rel, "/", "-"))
						h.Link(dest, link)
						dest = link
					}
					r := h.Install("--symlink-dir", dest)
					if r.Code != 0 || !strings.Contains(r.Stdout, "symlink : skipped") {
						t.Errorf("protected %s: %+v", dest, r)
					}
					before.Unchanged(t, h)
					if _, err := os.Lstat(filepath.Join(dest, "agent-director.new")); !os.IsNotExist(err) {
						t.Fatal("temporary PATH link remains")
					}
				}
			}
			// A relative alias must resolve in the actual cwd despite CDPATH.
			h.Link(layout.VersionDir(h.New, moveVersion), filepath.Join(h.Home, "relative"))
			h.Write(filepath.Join(h.Home, "cdpath/relative/marker"), "keep", 0600)
			h.Env["CDPATH"] = filepath.Join(h.Home, "cdpath")
			r := h.Install("--symlink-dir", "relative")
			if r.Code != 0 || !strings.Contains(r.Stdout, "symlink : skipped") {
				t.Fatalf("relative CDPATH: %+v", r)
			}
			before.Unchanged(t, h)
			for _, stamp := range []string{moveVersion, stagedStamp} {
				r := h.Run(filepath.Join(layout.VersionDir(h.New, stamp), "agent-director"), "version")
				var v struct{ Version string }
				if r.Code != 0 || json.Unmarshal([]byte(r.Stdout), &v) != nil || v.Version != stamp {
					t.Fatalf("retained executable changed: %+v", r)
				}
			}
			// Slash-delimited containment must still permit a neighboring name.
			dest := filepath.Join(h.New, "versions-neighbor")
			stagedMust(t, os.Mkdir(dest, 0700))
			r = h.Install("--symlink-dir", dest)
			if r.Code != 0 {
				t.Fatalf("ordinary PATH: %+v", r)
			}
			p := filepath.Join(dest, "agent-director")
			target, err := os.Readlink(p)
			stagedMust(t, err)
			if target != ownedCanonical(h) {
				t.Fatalf("PATH target %q", target)
			}
			linkBefore := stagedSnapshot(t, h, moveNewRel+"/versions-neighbor")
			moveMustInstall(t, h, "--symlink-dir", dest)
			linkBefore.Unchanged(t, h)
		})
	}
}
