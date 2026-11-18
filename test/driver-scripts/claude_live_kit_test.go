package driverscripts_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

// liveKitFixture freezes a small git input and substitutes only go build.
// Its synthetic provenance/build outputs never qualify a live provider.
type liveKitFixture struct {
	base, source, identity, archive, commit, version, stubs, binaries, marker string
	env                                                                       []string
}

func newLiveKitFixture(t *testing.T, native bool) *liveKitFixture {
	t.Helper()
	p := &liveKitFixture{base: t.TempDir(), version: "0.0.0-p9-fixture"}
	p.source, p.stubs, p.binaries = filepath.Join(p.base, "source"), filepath.Join(p.base, "stubs"), filepath.Join(p.base, "binaries")
	p.marker = filepath.Join(p.base, "unexpected-execution")
	p.env = []string{"HOME=" + filepath.Join(p.base, "home"), "TMPDIR=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid"}
	for _, name := range []string{"test/claude-live/prepare.sh", "test/claude-live/build-kit.sh", "test/driver/platform-env.sh", "test/driver/sql.sh", "go.mod", "skills/install-agent-director/install.sh"} {
		copyRepoFile(t, p.source, name)
	}
	entries, err := os.ReadDir(filepath.Join(repoRoot(t), "skills/install-agent-director/schedule"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		copyRepoFile(t, p.source, "skills/install-agent-director/schedule/"+e.Name())
	}
	writeFile(t, p.source, "cmd/agent-director/main.go", "package main // committed source\n", 0600)
	p.git(t, "init", "-q")
	p.git(t, "add", "-A")
	p.git(t, "commit", "-qm", "synthetic kit source")
	p.commit = strings.TrimSpace(p.git(t, "rev-parse", "HEAD"))
	p.freeze(t)
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range []string{"agent-director", "agent-director-admin", "claude-live"} {
		if native {
			pkg := "./cmd/" + component
			if component == "claude-live" {
				pkg = "./test/claude-live/cmd/claude-live"
			}
			// clibuild.Build cannot stamp a pair or build the live runner. Keep
			// these builds on the existing bounded driver-script command runner.
			flags := "-s -w -X github.com/gabemahoney/agent-director/internal/version.Version=" + p.version + " -X github.com/gabemahoney/agent-director/internal/version.Commit=" + p.commit
			r := startInLimit(t, repoRoot(t), append(p.env, "CGO_ENABLED=0"), "", 2*time.Minute, realGo, "build", "-ldflags="+flags, "-o", filepath.Join(p.binaries, component), pkg).wait()
			if r.code != 0 {
				t.Fatalf("build %s: %s", component, r)
			}
		} else {
			body := fmt.Sprintf("#!/bin/sh\n[ \"$1\" = version ] || { touch '%s'; exit 90; }\nprintf '%%s\\n' '{\"version\":\"%s\",\"commit\":\"%s\"}'\n", p.marker, p.version, p.commit)
			if component == "claude-live" {
				body = "#!/bin/sh\n[ \"$1\" = verify-kit ] || exit 91\nprintf '{}\\n'\n"
			}
			writeFile(t, p.binaries, component, body, 0755)
		}
	}
	writeFile(t, p.stubs, "go", `#!/bin/bash
set -eu
if [[ $1 != build ]]; then exec "$P9_REAL_GO" "$@"; fi
printf 'build\n' >> "$P9_BUILD_LOG"
sha256sum cmd/agent-director/*.go >> "$P9_BUILD_LOG"
[[ ${P9_FAIL_BUILD:-} != 1 ]] || exit 73
out='' component=''
while (($#)); do
    case "$1" in -o) out=$2; shift;; ./cmd/*|./test/claude-live/cmd/*) component=${1##*/};; esac
    shift
done
mkdir -p "$(dirname "$out")"
cp "$P9_BINARIES/$component" "$out"
`, 0755)
	p.env = append(p.env, "PATH="+p.stubs+":"+os.Getenv("PATH"), "P9_REAL_GO="+realGo,
		"P9_BINARIES="+p.binaries, "P9_BUILD_LOG="+filepath.Join(p.base, "build.log"), "P9_CANARY=excluded-environment-canary")
	return p
}

func (p *liveKitFixture) git(t *testing.T, args ...string) string {
	t.Helper()
	r := runIn(t, p.source, p.env, "", "git", args...)
	if r.code != 0 {
		t.Fatalf("git %v: %s", args, r)
	}
	return r.stdout
}

// freeze matches the explicit SOURCE/IDENTITY_JSON/SOURCE_TAR contract: the
// isolated index includes intended new files, never local excluded material.
func (p *liveKitFixture) freeze(t *testing.T) {
	t.Helper()
	names := strings.Split(strings.TrimSpace(p.git(t, "ls-files")), "\n")
	files := map[string]string{}
	for _, name := range names {
		files[name] = sha256Hex(liveKitRead(t, filepath.Join(p.source, name)))
	}
	p.identity = writeFile(t, p.base, "source-identity.json", string(liveKitJSON(t, map[string]any{
		"revision": p.commitOrHead(t), "tree": strings.TrimSpace(p.git(t, "write-tree")), "files": files,
	})), 0600)
	p.archive = filepath.Join(p.base, "source.tar")
	r := runIn(t, p.source, p.env, "", "tar", "-cf", p.archive, ".")
	if r.code != 0 {
		t.Fatal(r)
	}
}

func (p *liveKitFixture) commitOrHead(t *testing.T) string {
	if p.commit != "" {
		return p.commit
	}
	return strings.TrimSpace(p.git(t, "rev-parse", "HEAD"))
}

func (p *liveKitFixture) prepare(t *testing.T, out string, extra ...string) result {
	t.Helper()
	return runIn(t, p.source, append(append([]string{}, p.env...), extra...), "", "bash", filepath.Join(p.source, "test/claude-live/prepare.sh"), p.source, p.identity, p.archive, out, runtime.GOOS+"/"+runtime.GOARCH, p.version)
}

func liveKitRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func liveKitJSON(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (p *liveKitFixture) unpack(t *testing.T, out string) (string, runner.Kit) {
	t.Helper()
	archive := liveKitRead(t, filepath.Join(out, "claude-live-kit.tar.gz"))
	if got, want := string(liveKitRead(t, filepath.Join(out, "claude-live-kit.tar.gz.sha256"))), sha256Hex(archive)+"  claude-live-kit.tar.gz\n"; got != want {
		t.Fatalf("checksum %q, want %q", got, want)
	}
	files := untar(t, archive)
	var k runner.Kit
	if err := json.Unmarshal(files["kit.json"], &k); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(files["kit.json"], liveKitRead(t, filepath.Join(out, "kit.json"))) {
		t.Fatal("inner/outer manifest differ")
	}
	if len(files) != 10 || len(k.Files) != 9 {
		t.Fatalf("unexpected archive membership: %d files, %d indexed", len(files), len(k.Files))
	}
	root := filepath.Join(out, "kit")
	for name, body := range files {
		// Check members before writing them, including the manifest itself.
		if filepath.IsAbs(name) || filepath.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") {
			t.Fatalf("unsafe archive member %q", name)
		}
		if name != "kit.json" {
			if k.Files[name] != (runner.Artifact{SHA256: sha256Hex(body), Bytes: int64(len(body))}) {
				t.Fatalf("wrong manifest entry %s", name)
			}
		}
		for _, canary := range []string{"excluded-local-canary", "excluded-environment-canary"} {
			if bytes.Contains(body, []byte(canary)) {
				t.Fatalf("local material leaked in %s", name)
			}
		}
		writeFile(t, root, name, string(body), 0755)
	}
	return root, k
}

func TestClaudeLiveKitPreparation(t *testing.T) {
	for _, dirty := range []bool{false, true} {
		t.Run(fmt.Sprintf("frozen-dirty-%v", dirty), func(t *testing.T) {
			p := newLiveKitFixture(t, false)
			if dirty {
				writeFile(t, p.source, "cmd/agent-director/main.go", "package main // modified source\n", 0600)
				writeFile(t, p.source, "cmd/agent-director/new.go", "package main // new source\n", 0600)
				writeFile(t, p.source, "skills/install-agent-director/install.sh", string(liveKitRead(t, filepath.Join(p.source, "skills/install-agent-director/install.sh")))+"\n# snapshot installer\n", 0755)
				p.git(t, "add", "-A")
				p.freeze(t)
			}
			// Local working-copy edits made after freezing are not a build input.
			local := p.source
			frozen := filepath.Join(t.TempDir(), "frozen")
			if r := run(t, nil, "", "cp", "-a", local, frozen); r.code != 0 {
				t.Fatal(r)
			}
			p.source = frozen
			for _, name := range []string{"cmd/agent-director/main.go", "tickets/local", ".scratch/local", ".ad-test-ssh/config", ".config/credentials"} {
				writeFile(t, local, name, "excluded-local-canary", 0600)
			}
			out := filepath.Join(t.TempDir(), "out")
			r := p.prepare(t, out)
			if r.code != 0 {
				t.Fatal(r)
			}
			root, k := p.unpack(t, out)
			diff := p.git(t, "diff", "--binary", "HEAD")
			if k.BaseCommit != p.commit || k.CommitStamp != p.commit || k.SnapshotTree != strings.TrimSpace(p.git(t, "write-tree")) || k.Dirty != dirty || k.SourceSHA256 != sha256Hex(liveKitRead(t, p.archive)) || k.DiffSHA256 != sha256Hex([]byte(diff)) || k.Version != p.version || k.Platform != runtime.GOOS+"/"+runtime.GOARCH {
				t.Fatalf("untruthful provenance: %+v", k)
			}
			for name := range k.Files {
				if strings.HasPrefix(name, "skills/") && !bytes.Equal(liveKitRead(t, filepath.Join(root, name)), liveKitRead(t, filepath.Join(p.source, name))) {
					t.Fatalf("installer asset not from snapshot: %s", name)
				}
			}
			log := string(liveKitRead(t, filepath.Join(p.base, "build.log")))
			for _, name := range []string{"main.go", "new.go"} {
				if name == "new.go" && !dirty {
					continue
				}
				want := sha256Hex(liveKitRead(t, filepath.Join(p.source, "cmd/agent-director", name))) + "  cmd/agent-director/" + name
				if !strings.Contains(log, want) {
					t.Fatalf("build did not consume frozen %s", name)
				}
			}
		})
	}
}

func TestClaudeLiveKitPreparationRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*testing.T, *liveKitFixture, map[string]any)
		extra  string
	}{
		{"malformed", nil, ""},
		{"missing-files", func(t *testing.T, p *liveKitFixture, m map[string]any) { delete(m, "files") }, ""},
		{"incomplete-files", func(t *testing.T, p *liveKitFixture, m map[string]any) {
			delete(m["files"].(map[string]any), "cmd/agent-director/main.go")
			writeFile(t, p.source, "cmd/agent-director/main.go", "package main // not frozen\n", 0600)
		}, ""},
		{"source-hash", func(t *testing.T, p *liveKitFixture, m map[string]any) {
			writeFile(t, p.source, "cmd/agent-director/main.go", "package main // changed\n", 0600)
		}, ""},
		{"wrong-tree", func(t *testing.T, p *liveKitFixture, m map[string]any) { m["tree"] = strings.Repeat("f", 40) }, ""},
		{"unsafe-path", func(t *testing.T, p *liveKitFixture, m map[string]any) {
			m["files"].(map[string]any)["../outside"] = zeroHash
		}, ""},
		{"excluded-path", func(t *testing.T, p *liveKitFixture, m map[string]any) {
			writeFile(t, p.source, "tickets/local", "excluded-local-canary", 0600)
			m["files"].(map[string]any)["tickets/local"] = sha256Hex([]byte("excluded-local-canary"))
		}, ""},
		{"metadata-is-data", func(t *testing.T, p *liveKitFixture, m map[string]any) { m["revision"] = "$(touch " + p.marker + ")" }, ""},
		{"source-symlink", func(t *testing.T, p *liveKitFixture, m map[string]any) {
			path := filepath.Join(p.source, "cmd/agent-director/main.go")
			b := liveKitRead(t, path)
			outside := writeFile(t, p.base, "outside", string(b), 0600)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, path); err != nil {
				t.Fatal(err)
			}
		}, ""},
		{"nonempty-output", nil, ""},
		{"build-failure", nil, "P9_FAIL_BUILD=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newLiveKitFixture(t, false)
			var m map[string]any
			if err := json.Unmarshal(liveKitRead(t, p.identity), &m); err != nil {
				t.Fatal(err)
			}
			if tc.change != nil {
				tc.change(t, p, m)
			}
			writeFile(t, p.base, "source-identity.json", string(liveKitJSON(t, m)), 0600)
			if tc.name == "malformed" {
				writeFile(t, p.base, "source-identity.json", "{", 0600)
			}
			out := filepath.Join(t.TempDir(), "out")
			if tc.name == "nonempty-output" {
				writeFile(t, out, "keep", "keep me", 0600)
			}
			var before string
			if tc.name == "nonempty-output" {
				before = dirState(t, out)
			}
			extra := []string{}
			if tc.extra != "" {
				extra = append(extra, tc.extra)
			}
			r := p.prepare(t, out, extra...)
			if r.code == 0 {
				t.Errorf("unsafe preparation accepted (%s), stderr %q", tc.name, r.stderr)
				if b, err := os.ReadFile(filepath.Join(out, "kit.json")); err == nil {
					t.Logf("published manifest: %s", b)
				}
			}
			if tc.name == "nonempty-output" {
				if r.stderr != "output must be absent or empty\n" || dirState(t, out) != before {
					t.Errorf("refusal changed output or diagnostic: %s", r)
				}
			} else if entries, err := os.ReadDir(out); err == nil && len(entries) != 0 {
				t.Errorf("failed preparation published %v", entries)
			}
			if tc.name != "build-failure" {
				if _, err := os.Stat(filepath.Join(p.base, "build.log")); !os.IsNotExist(err) {
					t.Error("validation reached build boundary")
				}
			}
			if _, err := os.Stat(p.marker); !os.IsNotExist(err) {
				t.Error("metadata/payload executed")
			}
			if tc.name == "nonempty-output" {
				// Follow "output must be absent or empty" literally with a new
				// absent directory; the user's existing output still survives.
				fresh := filepath.Join(t.TempDir(), "fresh")
				if next := p.prepare(t, fresh); next.code != 0 {
					t.Fatal(next)
				}
				p.unpack(t, fresh)
				if dirState(t, out) != before {
					t.Fatal("recovery changed existing output")
				}
			}
		})
	}
}

func TestClaudeLiveKitPublicationFailure(t *testing.T) {
	for _, interrupt := range []bool{false, true} {
		t.Run(fmt.Sprintf("interrupt-%v", interrupt), func(t *testing.T) {
			p := newLiveKitFixture(t, false)
			realLN, err := exec.LookPath("ln")
			if err != nil {
				t.Fatal(err)
			}
			// Fail after the first published output, and create an unrelated file
			// at that same boundary: rollback must remove only its own outputs.
			writeFile(t, p.stubs, "ln", `#!/bin/bash
case "$2" in *.sha256)
  printf 'keep me' > "$(dirname "$2")/unrelated"
  if [[ "$P9_INTERRUPT" = true ]]; then kill -TERM "$PPID"; fi
  exit 74;;
esac
exec "$P9_REAL_LN" "$@"
`, 0755)
			out := filepath.Join(t.TempDir(), "out")
			r := p.prepare(t, out, "P9_REAL_LN="+realLN, fmt.Sprintf("P9_INTERRUPT=%v", interrupt))
			if r.code == 0 {
				t.Fatal("publication fault passed")
			}
			if got := dirState(t, out); got != "unrelated -rw------- keep me\n" {
				t.Fatalf("partial publication or lost unrelated file: %q", got)
			}
		})
	}
}

func TestClaudeLiveKitVerification(t *testing.T) {
	p := newLiveKitFixture(t, true)
	out := filepath.Join(t.TempDir(), "out")
	if r := p.prepare(t, out); r.code != 0 {
		t.Fatal(r)
	}
	root, k := p.unpack(t, out)
	if got, err := runner.VerifyKit(root); err != nil || !reflect.DeepEqual(got, k) {
		t.Fatalf("real verifier: %+v %v", got, err)
	}
	t.Run("record-hook-packaged", func(t *testing.T) {
		evidence := t.TempDir()
		if err := os.MkdirAll(filepath.Join(evidence, "raw/hooks"), 0700); err != nil {
			t.Fatal(err)
		}
		r := run(t, []string{"AGENT_DIRECTOR_INSTANCE_ID=live-p9-record-owned"}, `{"hook_event_name":"Stop"}`, filepath.Join(root, "bin/claude-live"), "record-hook", "--evidence", evidence, "--run-id", "p9-record", "--event", "Stop")
		if r.code != 0 || r.stdout != "" || r.stderr != "" {
			t.Fatal(r)
		}
		inv, err := runner.InspectRecordings(evidence, "p9-record", []string{"Stop"})
		if err != nil || len(inv) != 1 || !inv[0].Complete {
			t.Fatalf("packaged recorder unavailable: %+v %v", inv, err)
		}
	})
	for _, name := range []string{"incomplete", "malformed", "platform", "stamp", "missing-asset", "corrupt", "wrong-executable", "unmanifested", "traversal", "absolute", "symlink", "executable-marker"} {
		t.Run(name, func(t *testing.T) {
			copyRoot := filepath.Join(t.TempDir(), "kit")
			if r := run(t, nil, "", "cp", "-a", root, copyRoot); r.code != 0 {
				t.Fatal(r)
			}
			var specimen runner.Kit
			if err := json.Unmarshal(liveKitRead(t, filepath.Join(copyRoot, "kit.json")), &specimen); err != nil {
				t.Fatal(err)
			}
			payload := "payload/agent-director-admin-linux-" + runtime.GOARCH
			switch name {
			case "incomplete":
				specimen.SourceSHA256 = ""
			case "platform":
				specimen.Platform = "darwin/arm64"
			case "stamp":
				specimen.Version = "0.0.0-other"
			case "missing-asset":
				delete(specimen.Files, "skills/install-agent-director/schedule/agent-director-expire.timer")
			case "corrupt":
				writeFile(t, copyRoot, "skills/install-agent-director/install.sh", "corrupt", 0755)
			case "wrong-executable":
				b := liveKitRead(t, filepath.Join(copyRoot, "payload/agent-director-linux-"+runtime.GOARCH))
				writeFile(t, copyRoot, payload, string(b), 0755)
				specimen.Files[payload] = runner.Artifact{SHA256: sha256Hex(b), Bytes: int64(len(b))}
			case "unmanifested":
				writeFile(t, copyRoot, "extra", "not indexed", 0600)
			case "traversal":
				specimen.Files["../outside"] = runner.Artifact{SHA256: zeroHash}
			case "absolute":
				specimen.Files[filepath.Join(p.base, "outside")] = runner.Artifact{SHA256: zeroHash}
			case "symlink":
				if err := os.Remove(filepath.Join(copyRoot, payload)); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, payload), filepath.Join(copyRoot, payload)); err != nil {
					t.Fatal(err)
				}
			case "executable-marker":
				b := []byte("#!/bin/sh\ntouch '" + p.marker + "'\n")
				writeFile(t, copyRoot, payload, string(b), 0755)
				specimen.Files[payload] = runner.Artifact{SHA256: sha256Hex(b), Bytes: int64(len(b))}
			}
			body := liveKitJSON(t, specimen)
			if name == "malformed" {
				body = []byte("{")
			}
			writeFile(t, copyRoot, "kit.json", string(body), 0600)
			if _, err := runner.VerifyKit(copyRoot); err == nil {
				t.Errorf("invalid kit accepted: %s", name)
			}
			if _, err := os.Stat(p.marker); !os.IsNotExist(err) {
				t.Fatal("verifier executed payload")
			}
		})
	}
	t.Run("candidate-provenance", func(t *testing.T) { testLiveCandidate(t, p, root, k) })
}

func testLiveCandidate(t *testing.T, p *liveKitFixture, root string, k runner.Kit) {
	t.Helper()
	for _, outcome := range []string{"success", "installed-hash", "pair-identity", "command-failure"} {
		t.Run(outcome, func(t *testing.T) {
			e, err := runner.NewEvidence(filepath.Join(t.TempDir(), "evidence"))
			if err != nil {
				t.Fatal(err)
			}
			r := &runner.Run{Home: t.TempDir(), Kit: root, Evidence: e, Manifest: runner.Manifest{RunID: "p9-run", ProviderVersion: "synthetic-provider"}}
			calls := 0
			r.Commands = &runner.Commands{Evidence: e, Now: time.Now, Execute: func(ctx context.Context, argv, env []string, out, errout io.Writer) (int, error) {
				calls++
				if reflect.DeepEqual(argv, []string{"claude", "--version"}) {
					fmt.Fprint(out, "synthetic-provider\n")
					return 0, nil
				}
				if len(argv) != 2 || argv[1] != "version" {
					t.Fatalf("unexpected command %q", argv)
				}
				if outcome == "command-failure" {
					return 3, nil
				}
				commit := k.CommitStamp
				if outcome == "pair-identity" && strings.Contains(argv[0], "admin") {
					commit = strings.Repeat("a", 40)
				}
				fmt.Fprintf(out, "{\"version\":%q,\"commit\":%q}", k.Version, commit)
				return 0, nil
			}}
			provider, _, err := r.Commands.Run(context.Background(), "claude-version", time.Second, "claude", "--version")
			if err != nil {
				t.Fatal(err)
			}
			r.ProviderCommand = provider.Stdout
			for _, component := range []string{"agent-director", "agent-director-admin"} {
				subdir := "bin"
				if component == "agent-director-admin" {
					subdir = "admin"
				}
				b := liveKitRead(t, filepath.Join(root, "payload", component+"-linux-"+runtime.GOARCH))
				if outcome == "installed-hash" && component == "agent-director" {
					b = []byte("wrong installed candidate")
				}
				writeFile(t, r.Home, ".config/agent-director/"+subdir+"/"+component, string(b), 0755)
			}
			if outcome != "success" {
				if _, err := r.VerifyCandidate(context.Background(), true); err == nil {
					t.Fatal("invalid installed pair accepted")
				}
				if _, err := os.Stat(filepath.Join(e.Root, "raw/candidate-installed.json")); !os.IsNotExist(err) {
					t.Fatal("failed verification published candidate proof")
				}
				if outcome == "installed-hash" && calls != 1 {
					t.Fatal("mismatched installed binary executed")
				}
				return
			}
			for _, installed := range []bool{false, true} {
				if _, err := r.VerifyCandidate(context.Background(), installed); err != nil {
					t.Fatal(err)
				}
				name := "raw/candidate-payload.json"
				if installed {
					name = "raw/candidate-installed.json"
				}
				before := liveKitRead(t, filepath.Join(e.Root, name))
				var proof runner.CandidateEvidence
				if err := json.Unmarshal(before, &proof); err != nil {
					t.Fatal(err)
				}
				if proof.Schema != runner.Schema || proof.RunID != "p9-run" || !reflect.DeepEqual(proof.Kit, k) || proof.KitSHA256 != sha256Hex(liveKitRead(t, filepath.Join(filepath.Dir(root), "claude-live-kit.tar.gz"))) || proof.Installed != installed || proof.ProviderCommand != provider.Stdout || proof.ProviderVersion != "synthetic-provider" || proof.RuntimeCommand == "" || proof.AdminCommand == "" || proof.RuntimeCommand == proof.AdminCommand {
					t.Fatalf("candidate proof lost provenance: %+v", proof)
				}
				for _, ref := range []string{proof.RuntimeCommand, proof.AdminCommand} {
					if got := string(liveKitRead(t, filepath.Join(e.Root, ref))); got != fmt.Sprintf("{\"version\":%q,\"commit\":%q}", k.Version, k.CommitStamp) {
						t.Fatalf("proof references wrong identity output: %q", got)
					}
				}
				if got := string(liveKitRead(t, filepath.Join(e.Root, proof.ProviderCommand))); got != "synthetic-provider\n" {
					t.Fatalf("wrong provider reference: %q", got)
				}
				if _, err := r.VerifyCandidate(context.Background(), installed); err == nil {
					t.Fatal("immutable candidate proof overwritten")
				}
				if !bytes.Equal(before, liveKitRead(t, filepath.Join(e.Root, name))) {
					t.Fatal("candidate proof changed")
				}
			}
			if err := r.Commands.Finish(); err != nil {
				t.Fatal(err)
			}
			if r.Manifest.Candidate != k.BaseCommit+"/"+k.SnapshotTree || r.Manifest.KitSHA256 == "" {
				t.Fatal("manifest candidate identity missing")
			}
		})
	}
}
