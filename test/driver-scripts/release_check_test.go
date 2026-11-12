package driverscripts_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/adminapi"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// writeFile writes body at dir/rel, making its parent directories.
func writeFile(t *testing.T, dir, rel, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// copyRepoFile copies the repo's own rel into dir/rel, keeping its mode.
func copyRepoFile(t *testing.T, dir, rel string) []byte {
	t.Helper()
	src := filepath.Join(repoRoot(t), rel)
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, rel, string(b), info.Mode().Perm())
	return b
}

// physical is path with its symlinks resolved, as check.sh names the kit.
func physical(t *testing.T, path string) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestClaudeStandInVersion: under install.sh's `bash -s`, a lone --version
// prints the stand-in line and leaves the rest of the piped script to the
// outer shell; any other invocation is still the interactive shell.
func TestClaudeStandInVersion(t *testing.T) {
	path := "PATH=" + driverScript(t, "stand-in-claude") + ":" + os.Getenv("PATH")
	r := run(t, []string{path}, "outer=yes\nclaude --version\necho \"rc=$? outer=${outer:-no}\"\n", "bash", "-s")
	if want := "2.1.280 (Claude Code stand-in)\nrc=0 outer=yes\n"; r.code != 0 || r.stdout != want || r.stderr != "" || r.elapsed > 5*time.Second {
		t.Errorf("want exit 0 within 5s, stdout %q and no stderr: %s", want, r)
	}
	for _, args := range [][]string{nil, {"--settings", "{}"}, {"--version", "--resume", "x"}} {
		t.Run(fmt.Sprintf("%q", args), func(t *testing.T) {
			r := run(t, nil, "exit 7\n", driverScript(t, "stand-in-claude/claude"), args...)
			if r.code != 7 || strings.Contains(r.stdout, "stand-in") {
				t.Errorf("want the shell to run the piped `exit 7`: %s", r)
			}
		})
	}
}

// releaseEnv is a release.env's values, written in check.sh's key order.
type releaseEnv struct{ mode, tag, version, commit, ad, admin, installSh string }

func (e releaseEnv) String() string {
	return fmt.Sprintf("RELEASE_CHECK_MODE=%s\nRELEASE_TAG=%s\nEXPECTED_VERSION=%s\nEXPECTED_COMMIT=%s\nAD_SHA256=%s\nADMIN_SHA256=%s\nINSTALL_SH_SHA256=%s\n",
		e.mode, e.tag, e.version, e.commit, e.ad, e.admin, e.installSh)
}

const (
	kitCommit = "0123456789abcdef0123456789abcdef01234567"
	zeroHash  = "0000000000000000000000000000000000000000000000000000000000000000"
	// installStub records how it ran and exits non-zero, so check.sh's exit
	// is shown to be the installer's.
	installStub = `#!/bin/bash
printf '%s\n' "$0" "$@" >"$INSTALL_STUB_LOG"
printf 'stub installer: out \\ "q"\n'
printf 'stub installer: err\n' >&2
exit 3
`
)

// newCheckKit is a kit holding the real check.sh, platform-env.sh and
// sql.sh, installStub as install.sh and, for a candidate, a fake payload
// pair; it returns the kit and release.env values that verify.
func newCheckKit(t *testing.T, mode string) (string, releaseEnv) {
	t.Helper()
	kit := physical(t, t.TempDir())
	for _, rel := range []string{"test/release-check/check.sh", "test/driver/platform-env.sh", "test/driver/sql.sh"} {
		copyRepoFile(t, kit, rel)
	}
	writeFile(t, kit, "skills/install-agent-director/install.sh", installStub, 0o755)
	e := releaseEnv{mode: mode, version: "1.2.3", commit: kitCommit, ad: sha256Hex([]byte("ad")), admin: sha256Hex([]byte("admin")), installSh: sha256Hex([]byte(installStub))}
	if mode == "published" {
		e.tag = "v1.2.3"
	} else {
		e.ad, e.admin = sha256Hex([]byte("fake ad\n")), sha256Hex([]byte("fake admin\n"))
		writeFile(t, kit, "payload/agent-director-linux-arm64", "fake ad\n", 0o755)
		writeFile(t, kit, "payload/agent-director-admin-linux-arm64", "fake admin\n", 0o755)
	}
	return kit, e
}

// TestCheckShInstall: install refuses a mismatched hash or malformed
// release.env with one `install: refused` line and exit 2, running nothing;
// verified inputs go to the installer through `bash -s` with exactly the
// mode's flags, its output unchanged, then `install: exit <n>` and its status.
func TestCheckShInstall(t *testing.T) {
	stubSHA := sha256Hex([]byte(installStub))
	for _, tc := range []struct {
		name, mode string
		edit       func(e *releaseEnv)
		refused    string // the one stdout line; "" for verified inputs
	}{
		{"install.sh hash", "published", func(e *releaseEnv) { e.installSh = zeroHash },
			"install: refused skills/install-agent-director/install.sh sha256 mismatch: expected " + zeroHash + ", actual " + stubSHA},
		{"payload hash", "candidate", func(e *releaseEnv) { e.ad = zeroHash },
			"install: refused payload/agent-director-linux-arm64 sha256 mismatch: expected " + zeroHash + ", actual " + sha256Hex([]byte("fake ad\n"))},
		{"leading-v version", "published", func(e *releaseEnv) { e.version = "v1.2.3" },
			"install: refused release.env EXPECTED_VERSION v1.2.3 must be a bare version: not empty, no whitespace, no leading v"},
		{"published tag", "published", func(e *releaseEnv) { e.tag = "v1.2.4" },
			"install: refused release.env RELEASE_TAG v1.2.4 must be v1.2.3 in published mode"},
		{"published pass-through", "published", nil, ""},
		{"candidate pass-through", "candidate", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kit, e := newCheckKit(t, tc.mode)
			if tc.edit != nil {
				tc.edit(&e)
			}
			writeFile(t, kit, "release.env", e.String(), 0o644)
			log := filepath.Join(t.TempDir(), "install.log")
			r := run(t, []string{"INSTALL_STUB_LOG=" + log}, "", filepath.Join(kit, "test/release-check/check.sh"), "install", kit)
			ran, _ := os.ReadFile(log)

			if tc.refused != "" {
				if r.code != 2 || r.stdout != tc.refused+"\n" || r.stderr != "" || ran != nil {
					t.Fatalf("want exit 2, only %q and the installer never run (it recorded %q): %s", tc.refused, ran, r)
				}
				return
			}
			args := []string{"--from-release", "v1.2.3", "--sha256", e.ad, "--admin-sha256", e.admin}
			if tc.mode == "candidate" {
				args = []string{"--binary", kit + "/payload/agent-director-linux-arm64", "--admin-binary", kit + "/payload/agent-director-admin-linux-arm64"}
			}
			if want := "bash\n" + strings.Join(args, "\n") + "\n"; string(ran) != want {
				t.Errorf("installer ran as $0 and arguments %q, want %q (bash -s and the mode's flags)", ran, want)
			}
			if want := "stub installer: out \\ \"q\"\ninstall: exit 3\n"; r.code != 3 || r.stdout != want || r.stderr != "stub installer: err\n" {
				t.Errorf("want exit 3, stdout %q and the installer's stderr unchanged: %s", want, r)
			}
		})
	}
}

// TestCheckShRunFails: with an installed pair whose admin binary reports the
// wrong commit and whose other verbs fail at once, every check has its line
// and the run ends VERDICT: FAIL non-zero; so does an unexpected error. Each
// FAIL line, and each PASS line of a check in which a call failed, is
// followed, on stderr, by its diagnostics block holding the calls' complete
// output, and run leaves nothing behind outside TMPDIR.
func TestCheckShRunFails(t *testing.T) {
	kit, e := newCheckKit(t, "published")
	writeFile(t, kit, "release.env", e.String(), 0o644)
	writeFile(t, kit, "test/driver/pane-hook.sh", "#!/bin/sh\nexit 0\n", 0o755)
	home := t.TempDir()
	versionJSON := `{"version":"1.2.3","commit":"%s","config_dir":"~/.config/agent-director"}`
	// spawn fails with a stdout marker and a stderr longer than a FAIL line's
	// reason, whose tail only the diagnostics keep.
	spawnOut := `{"error":"stub spawn stdout"}`
	spawnErr := "stub agent-director: spawn fails " + strings.Repeat("x", 420) + "\nstub agent-director: spawn stderr tail"
	// With STUB_STATE set, the rows work instead, except that each row's
	// first status and first read-pane fail, so its polls recover.
	writeFile(t, home, ".config/agent-director/bin/agent-director", `#!/bin/bash
[ "$1" = version ] && { printf '%s\n' '`+fmt.Sprintf(versionJSON, kitCommit)+`'; exit 0; }
[ "$1" = spawn ] && printf '%s\n' "$@" >"$SPAWN_LOG.$3"
if [ -z "$STUB_STATE" ]; then
	[ "$1" = spawn ] && { printf '%s\n' '`+spawnOut+`'; printf '%s\n' '`+spawnErr+`' >&2; exit 5; }
	echo "stub agent-director: $1 fails" >&2; exit 1
fi
verb=$1
shift
id=$(cat "$STUB_STATE/last" 2>/dev/null) text="" records=""
while [ $# -gt 0 ]; do
	case $1 in
	--instance-id) id=$2; shift ;;
	--text) text=$2; shift ;;
	--extra-env) case $2 in AD_CODEX_RECORD_DIR=*) records=${2#*=} ;; esac; shift ;;
	esac
	shift
done
state=waiting
[ ! -e "$STUB_STATE/killed-$id" ] || state=ended
first() { [ ! -e "$STUB_STATE/$1-$id" ] && touch "$STUB_STATE/$1-$id"; }
case $verb in
spawn)
	echo "$id" >"$STUB_STATE/last"
	rm -f "$STUB_STATE/killed-$id"
	[ -z "$records" ] || { mkdir -p "$records/$id" && echo 1 >"$records/$id/pid"; }
	printf '{"instance_id":"%s"}\n' "$id" ;;
status)
	first status && { echo "stub status stdout"; echo "stub status stderr" >&2; exit 3; }
	printf '{"state":"%s"}\n' "$state" ;;
get) printf '{"instance_id":"%s","state":"%s"}\n' "$id" "$state" ;;
list) printf '{"spawns":[{"instance_id":"%s","state":"%s"}]}\n' "$id" "$state" ;;
send-keys) printf '%s' "$text" >"$STUB_STATE/pane-$id" ;;
read-pane)
	first read-pane && { echo "stub read-pane stdout"; echo "stub read-pane stderr" >&2; exit 4; }
	jq -cn --arg p "$(cat "$STUB_STATE/pane-$id")" '{pane: $p}' ;;
kill) touch "$STUB_STATE/killed-$id" ;;
find-missing) ;;
*) echo "stub agent-director: $verb fails" >&2; exit 1 ;;
esac
`, 0o755)
	adminDir := filepath.Join(home, ".config/agent-director/admin")
	writeFile(t, adminDir, "help.txt", adminapi.ApprovalStatement+"\n\nUsage: agent-director-admin <verb>\n", 0o644)
	writeFile(t, adminDir, "agent-director-admin", fmt.Sprintf(`#!/bin/sh
case "$1" in
version) printf '%%s\n' '%s' ;;
help) cat "$(dirname "$0")/help.txt" ;;
*) exit 1 ;;
esac
`, fmt.Sprintf(versionJSON, "fedcba9876543210fedcba9876543210fedcba98")), 0o755)
	adBin := filepath.Join(home, ".config/agent-director/bin/agent-director")
	checkSh := filepath.Join(kit, "test/release-check/check.sh")
	// cut is a FAIL line's one-line reason, at most 400 characters.
	cut := func(s string) string {
		if s = strings.ReplaceAll(s, "\n", " | "); len(s) > 400 {
			s = s[:400] + "..."
		}
		return s
	}
	spawnReason := func(agent string) string { return "spawn --agent " + agent + " exited 5: " + cut(spawnErr) }
	rows := []string{
		"claude row: FAIL " + cut(spawnReason("claude")),
		"codex row: FAIL " + cut(spawnReason("codex")),
		"reuse: FAIL the claude row did not finish, so there is no finished id to reuse",
		"VERDICT: FAIL",
	}
	staleTmp := func() []string {
		m, err := filepath.Glob("/tmp/release-check*")
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	tmpBefore := staleTmp()
	noEnv := "FAIL no release.env in " + kit

	adminMismatch := []string{
		"version: PASS",
		"admin version: FAIL agent-director-admin reports commit fedcba9876543210fedcba9876543210fedcba98; expected " + kitCommit,
		"admin help: PASS",
	}

	for _, tc := range []struct {
		name, arg string
		setup     func()
		recovered []string // the checks whose polls recover: their rows work
		want      []string // the lines after the Environment Record; nil: no record or checks
	}{
		{"mismatched admin commit", kit, nil, nil, append(adminMismatch, rows...)},
		{"recovered polls", kit, nil, []string{"claude row", "codex row"}, append(adminMismatch,
			"claude row: PASS", "codex row: PASS", "reuse: PASS", "VERDICT: FAIL")},
		{"no release.env", kit, func() { _ = os.Remove(filepath.Join(kit, "release.env")) }, nil, append([]string{
			"version: " + noEnv, "admin version: " + noEnv, "admin help: PASS",
		}, rows...)},
		{"no kit directory", filepath.Join(kit, "missing"), nil, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setup != nil {
				tc.setup()
			}
			tmp, spawnLog := t.TempDir(), filepath.Join(t.TempDir(), "spawn")
			env := []string{"HOME=" + home, "TMPDIR=" + tmp, "SPAWN_LOG=" + spawnLog, "PATH=" + filepath.Dir(adBin) + ":" + os.Getenv("PATH")}
			if tc.recovered != nil {
				env = append(env, "STUB_STATE="+t.TempDir())
			}
			// Both streams in one, as run.log has them.
			r := run(t, env, "", "bash", "-c", `exec "$0" "$@" 2>&1`, checkSh, "run", tc.arg)
			defer func() {
				if left, _ := os.ReadDir(tmp); len(left) != 0 || !reflect.DeepEqual(staleTmp(), tmpBefore) {
					t.Errorf("run left %v in its TMPDIR, or /tmp/release-check* went from %v to %v", left, tmpBefore, staleTmp())
				}
			}()
			if tc.want == nil {
				if want := "environment: unavailable no kit directory at " + tc.arg + "\nVERDICT: FAIL\n"; r.code != 1 || r.stdout != want {
					t.Errorf("want exit 1 and output %q: %s", want, r)
				}
				return
			}

			// Split the output into its stdout lines and the diagnostics
			// blocks, each right after its own check's (or the record's) line.
			var lines []string
			blocks := map[string]string{}
			all := strings.Split(strings.TrimSuffix(r.stdout, "\n"), "\n")
			for i := 0; i < len(all); i++ {
				name, isDiag := strings.CutPrefix(all[i], "diagnostics ")
				name, isBegin := strings.CutSuffix(name, ": begin")
				if !isDiag || !isBegin {
					lines = append(lines, all[i])
					continue
				}
				if len(lines) == 0 {
					t.Errorf("diagnostics %s precedes every line: %s", name, r)
				} else if last := lines[len(lines)-1]; last != name+": PASS" && !strings.HasPrefix(last, name+": FAIL ") && (name != "environment" || len(lines) != 1) {
					t.Errorf("diagnostics %s does not follow its own line: %s", name, r)
				}
				j := i
				for j < len(all) && all[j] != "diagnostics "+name+": end" {
					j++
				}
				if j == len(all) {
					t.Fatalf("diagnostics %s has no end: %s", name, r)
				}
				blocks[name] = strings.Join(all[i:j+1], "\n") + "\n"
				i = j
			}
			want := strings.Join(tc.want, "\n")
			if r.code != 1 || !strings.HasPrefix(lines[0], `{"platform_env":`) || strings.Join(lines[1:], "\n") != want {
				t.Errorf("want exit 1, the Environment Record, then exactly:\n%s\ngot: %s", want, r)
			}
			for _, l := range lines[1:] {
				if name, _, ok := strings.Cut(l, ": FAIL "); ok && blocks[name] == "" {
					t.Errorf("%q has no diagnostics block: %s", l, r)
				}
				if name, ok := strings.CutSuffix(l, ": PASS"); ok && (blocks[name] != "") != slices.Contains(tc.recovered, name) {
					t.Errorf("%q has a diagnostics block %v, want one only if a call in it failed: %s", l, blocks[name] != "", r)
				}
			}
			// Each recovered poll's block holds the failed call whole, then
			// the same call succeeding, and no reason.
			for _, name := range tc.recovered {
				b := blocks[name]
				if strings.Contains(b, "\nreason:\n") {
					t.Errorf("%s passed, but its diagnostics give a reason:\n%s", name, b)
				}
				for _, c := range []struct {
					verb, args string
					code       int
				}{{"status", "", 3}, {"read-pane", " --n-lines 50", 4}} {
					m := regexp.MustCompile(`(?m)^call (\d+): (` + regexp.QuoteMeta(adBin+" "+c.verb+" --instance-id ") + `\S+` + regexp.QuoteMeta(c.args) + `)$`).FindStringSubmatch(b)
					if m == nil {
						t.Errorf("%s diagnostics have no %s call:\n%s", name, c.verb, b)
						continue
					}
					n, _ := strconv.Atoi(m[1])
					out, errOut := "stub "+c.verb+" stdout", "stub "+c.verb+" stderr"
					want := fmt.Sprintf("call %[1]d: %[2]s\ncall %[1]d: exit %[3]d\ncall %[1]d: stdout: %[4]d bytes:\n| %[5]s\ncall %[1]d: stderr: %[6]d bytes:\n| %[7]s\ncall %[8]d: %[2]s\ncall %[8]d: exit 0\n",
						n, m[2], c.code, len(out)+1, out, len(errOut)+1, errOut, n+1)
					if !strings.Contains(b, want) {
						t.Errorf("%s diagnostics lack the failed then recovered %s call:\n%s\nin:\n%s", name, c.verb, want, b)
					}
				}
			}

			args, err := os.ReadFile(spawnLog + ".claude")
			if err != nil {
				t.Fatal(err)
			}
			spawned := strings.Split(strings.TrimSuffix(string(args), "\n"), "\n")
			if len(spawned) < 5 || spawned[3] != "--cwd" {
				t.Fatalf("spawn ran with %q, want a --cwd fourth", spawned)
			}
			if cwd := spawned[4]; !strings.HasPrefix(cwd, physical(t, tmp)+"/release-check.") || !strings.HasSuffix(cwd, "/claude-row") {
				t.Errorf("the claude row's cwd %s is not in a private directory under TMPDIR %s", cwd, tmp)
			}
			if tc.recovered != nil {
				return
			}
			wantBlock := "diagnostics claude row: begin\nreason:\n| " + spawnReason("claude") + "\n" +
				"call 1: " + adBin + " " + strings.Join(spawned, " ") + "\ncall 1: exit 5\n" +
				fmt.Sprintf("call 1: stdout: %d bytes:\n| %s\n", len(spawnOut)+1, spawnOut) +
				fmt.Sprintf("call 1: stderr: %d bytes:\n| %s\n", len(spawnErr)+1, strings.ReplaceAll(spawnErr, "\n", "\n| ")) +
				"diagnostics claude row: end\n"
			if blocks["claude row"] != wantBlock {
				t.Errorf("claude row diagnostics:\n%s\nwant:\n%s", blocks["claude row"], wantBlock)
			}
		})
	}
}
