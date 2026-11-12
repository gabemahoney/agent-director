package driverscripts_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// prepRepo is a temp git repo whose tag v1.2.3 holds the kit's layout files,
// copied from this repo, at package.json version 1.2.3; its working tree
// differs from the tag. It has a matching SHA256SUMS, payload pair and
// agent-director stubs on PATH, which touch marker if anything runs them.
type prepRepo struct {
	dir, preKit, commit string
	files               map[string][]byte // the tag's layout files
	env                 []string
	sums, payload       string
	adSHA, adminSHA     string
	marker, prepareSh   string
}

func newPrepRepo(t *testing.T) *prepRepo {
	t.Helper()
	base := t.TempDir()
	p := &prepRepo{dir: filepath.Join(base, "repo"), files: map[string][]byte{}, marker: filepath.Join(base, "ran")}
	p.prepareSh = filepath.Join(repoRoot(t), "test/release-check/prepare.sh")
	stubs := filepath.Join(base, "stubs")
	exe := "#!/bin/sh\ntouch " + p.marker + "\n"
	for _, name := range []string{"agent-director", "agent-director-admin"} {
		writeFile(t, stubs, name, exe, 0o755)
	}
	p.env = []string{
		"HOME=" + filepath.Join(base, "home"), "TMPDIR=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"PATH=" + stubs + ":" + os.Getenv("PATH"),
	}
	git := func(args ...string) string {
		r := runIn(t, p.dir, p.env, "", "git", args...)
		if r.code != 0 {
			t.Fatalf("git %v: %s", args, r)
		}
		return strings.TrimSpace(r.stdout)
	}
	writeFile(t, p.dir, "pkg/ts-bun-client/package.json", `{"name":"x","version":"1.2.3"}`+"\n", 0o644)
	git("init", "-q")
	git("add", "-A")
	git("commit", "-qm", "before the kit")
	p.preKit = git("rev-parse", "HEAD")

	var layout []string
	for _, d := range []string{"test/release-check", "test/driver/stand-in-claude", "test/driver/stand-in-codex"} {
		entries, err := os.ReadDir(filepath.Join(repoRoot(t), d))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			layout = append(layout, d+"/"+e.Name())
		}
	}
	layout = append(layout, "test/driver/platform-env.sh", "test/driver/sql.sh", "test/driver/pane-hook.sh", "skills/install-agent-director/install.sh")
	for _, rel := range layout {
		p.files[rel] = copyRepoFile(t, p.dir, rel)
	}
	writeFile(t, p.dir, "README", "not in the kit\n", 0o644)
	git("add", "-A")
	git("commit", "-qm", "kit")
	git("tag", "-a", "v1.2.3", "-m", "release")
	git("tag", "-a", "v9.9.9", "-m", "wrong version")
	p.commit = git("rev-parse", "v1.2.3^{commit}")
	writeFile(t, p.dir, "test/driver/pane-hook.sh", "working-tree change\n", 0o755)
	writeFile(t, p.dir, "test/release-check/untracked", "untracked\n", 0o644)

	p.payload = filepath.Join(base, "payload")
	writeFile(t, p.payload, "agent-director-linux-arm64", exe+"# ad\n", 0o755)
	writeFile(t, p.payload, "agent-director-admin-linux-arm64", exe+"# admin\n", 0o755)
	p.adSHA, p.adminSHA = sha256Hex([]byte(exe+"# ad\n")), sha256Hex([]byte(exe+"# admin\n"))
	p.sums = writeFile(t, base, "SHA256SUMS", p.adSHA+"  dist/agent-director-linux-arm64\n"+
		p.adminSHA+"  dist/agent-director-admin-linux-arm64\n"+zeroHash+"  dist/agent-director-darwin-arm64\n", 0o644)
	return p
}

// prepare runs prepare.sh in the repo's directory sub ("" for its root).
func (p *prepRepo) prepare(t *testing.T, sub string, args ...string) result {
	t.Helper()
	return runIn(t, filepath.Join(p.dir, sub), p.env, "", p.prepareSh, args...)
}

// untar is the regular files of a .tar.gz, by path.
func untar(t *testing.T, archive []byte) map[string][]byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return files
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			b, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			files[strings.TrimPrefix(h.Name, "./")] = b
		}
	}
}

// assertPrepared: r is a finished preparation of the tag's commit in mode
// into out, whose archive holds exactly the committed layout files,
// release.env and, for a candidate, the payload pair, and nothing ran.
func assertPrepared(t *testing.T, p *prepRepo, mode string, r result, out string) {
	t.Helper()
	if r.code != 0 {
		t.Fatalf("want exit 0: %s", r)
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := []string{"INSTRUCTIONS.txt", "release-check-kit.tar.gz", "release-check-kit.tar.gz.sha256"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("out-dir holds %v, want %v", names, want)
	}
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	archive := read("release-check-kit.tar.gz")
	archiveSHA := sha256Hex(archive)
	if got, want := string(read("release-check-kit.tar.gz.sha256")), archiveSHA+"  release-check-kit.tar.gz\n"; got != want {
		t.Errorf("hash file %q, want %q", got, want)
	}

	env := releaseEnv{mode: mode, version: "1.2.3", commit: p.commit, ad: p.adSHA, admin: p.adminSHA,
		installSh: sha256Hex(p.files["skills/install-agent-director/install.sh"])}
	if mode == "published" {
		env.tag = "v1.2.3"
	}
	want := map[string][]byte{"release.env": []byte(env.String())}
	if mode == "candidate" {
		for _, asset := range []string{"agent-director-linux-arm64", "agent-director-admin-linux-arm64"} {
			b, err := os.ReadFile(filepath.Join(p.payload, asset))
			if err != nil {
				t.Fatal(err)
			}
			want["payload/"+asset] = b
		}
	}
	for rel, b := range p.files {
		want[rel] = b
	}
	if got := untar(t, archive); !reflect.DeepEqual(got, want) {
		var gotNames, wantNames []string
		for k := range got {
			gotNames = append(gotNames, k)
		}
		for k := range want {
			wantNames = append(wantNames, k)
		}
		sort.Strings(gotNames)
		sort.Strings(wantNames)
		t.Errorf("archive differs from the committed layout:\ngot files  %v\nwant files %v\ngot release.env %q\nwant %q", gotNames, wantNames, got["release.env"], want["release.env"])
	}

	instructions := string(read("INSTRUCTIONS.txt"))
	for _, s := range []string{archiveSHA, "1.2.3", p.commit} {
		if !strings.Contains(instructions, s) {
			t.Errorf("INSTRUCTIONS.txt lacks %q", s)
		}
	}
	for _, s := range []string{"tickets/", ".scratch", p.dir} {
		if strings.Contains(instructions, s) {
			t.Errorf("INSTRUCTIONS.txt names the host path %q", s)
		}
	}
	if _, err := os.Stat(p.marker); err == nil {
		t.Error("preparation ran a payload or agent-director program")
	}
}

// TestPreparePackagesTheCommittedTree: both modes archive the commit's own
// layout bytes, not the working tree's, with the exact release.env, also
// when run from a subdirectory with paths relative to it.
func TestPreparePackagesTheCommittedTree(t *testing.T) {
	p := newPrepRepo(t)
	const sub = "test/release-check"
	rel := func(path string) string {
		r, err := filepath.Rel(filepath.Join(p.dir, sub), path)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	for _, tc := range []struct {
		name, mode, dir string
		args            func(out string) []string
	}{
		{"published", "published", "", func(out string) []string { return []string{"v1.2.3", p.sums, out} }},
		{"candidate", "candidate", "", func(out string) []string { return []string{"--candidate", p.commit, "1.2.3", p.sums, p.payload, out} }},
		{"candidate from " + sub, "candidate", sub, func(out string) []string {
			return []string{"--candidate", p.commit, "1.2.3", rel(p.sums), rel(p.payload), rel(out)}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "out")
			assertPrepared(t, p, tc.mode, p.prepare(t, tc.dir, tc.args(out)...), out)
		})
	}
}

// dirState is a directory's entries with their modes and bytes.
func dirState(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		body, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		b.WriteString(e.Name() + " " + info.Mode().String() + " " + string(body) + "\n")
	}
	return b.String()
}

// TestPrepareRefusals: each refusal exits 2 with its exact advice before
// writing: an absent out-dir is not created, a pre-existing one is left as
// it was. Followed rows then do exactly what the advice says, rerun the same
// command and get a full preparation (docs/test-writing-guide.md
// "Literal-follow tests for error advice").
func TestPrepareRefusals(t *testing.T) {
	p := newPrepRepo(t)
	base := t.TempDir()
	noAdmin := writeFile(t, base, "sums-no-admin", p.adSHA+"  dist/agent-director-linux-arm64\n", 0o644)
	dupAD := writeFile(t, base, "sums-dup", p.adSHA+"  agent-director-linux-arm64\n"+p.adSHA+"  dist/agent-director-linux-arm64\n"+p.adminSHA+"  agent-director-admin-linux-arm64\n", 0o644)
	badPayload := filepath.Join(base, "bad-payload")
	writeFile(t, badPayload, "agent-director-linux-arm64", "#!/bin/sh\ntouch "+p.marker+"\n# ad\n", 0o755)
	writeFile(t, badPayload, "agent-director-admin-linux-arm64", "tampered\n", 0o755)

	for i, tc := range []struct {
		name, out string // out: absent, empty or non-empty before the run
		dir       string // where the command runs, under the repo
		args      func(out string) []string
		advice    func(out string) string
		follow    func(dir *string, args []string, out string) // nil: not followed
		mode      string
	}{
		{"tag without v", "absent", "", func(out string) []string { return []string{"1.2.3", p.sums, out} },
			func(string) string { return "tag 1.2.3 has no leading v: pass a release tag v<version>" }, nil, ""},
		{"tag version differs from package.json", "empty", "", func(out string) []string { return []string{"v9.9.9", p.sums, out} },
			func(string) string {
				return "tag v9.9.9 names version 9.9.9 but its pkg/ts-bun-client/package.json .version is 1.2.3: pass the tag for that tree's release, v1.2.3"
			}, nil, ""},
		{"pre-kit commit", "absent", "", func(out string) []string { return []string{"--candidate", p.preKit, "1.2.3", p.sums, p.payload, out} },
			func(string) string {
				return "commit " + p.preKit + " has no test/release-check/ (it predates the Release Check Kit): pass a tag or commit that has it"
			}, nil, ""},
		{"SHA256SUMS missing an arm64 entry", "absent", "", func(out string) []string { return []string{"v1.2.3", noAdmin, out} },
			func(string) string {
				return "SHA256SUMS has 0 entries for agent-director-admin-linux-arm64; it needs exactly one: pass the release's own SHA256SUMS"
			}, func(_ *string, args []string, _ string) { args[1] = p.sums }, "published"},
		{"SHA256SUMS duplicating an arm64 entry", "empty", "", func(out string) []string { return []string{"v1.2.3", dupAD, out} },
			func(string) string {
				return "SHA256SUMS has 2 entries for agent-director-linux-arm64; it needs exactly one: pass the release's own SHA256SUMS"
			}, nil, ""},
		{"payload hash mismatch", "empty", "", func(out string) []string { return []string{"--candidate", p.commit, "1.2.3", p.sums, badPayload, out} },
			func(string) string {
				return "payload agent-director-admin-linux-arm64 sha256 mismatch: SHA256SUMS has " + p.adminSHA + ", the file is " + sha256Hex([]byte("tampered\n")) + "; pass the pair SHA256SUMS describes"
			}, func(_ *string, args []string, _ string) { args[4] = p.payload }, "candidate"},
		{"non-empty out-dir", "non-empty", "", func(out string) []string { return []string{"v1.2.3", p.sums, out} },
			func(out string) string { return "out-dir " + out + " is not empty: pass an absent or empty directory" },
			func(_ *string, _ []string, out string) {
				if err := os.Remove(filepath.Join(out, "keep")); err != nil {
					t.Fatal(err)
				}
			}, "published"},
		{"run from .git", "absent", ".git", func(out string) []string { return []string{"v1.2.3", p.sums, out} },
			func(string) string {
				return "run prepare.sh from inside the agent-director git repository's working tree"
			}, func(dir *string, _ []string, _ string) { *dir = "" }, "published"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(base, "out-"+string(rune('a'+i)))
			if tc.out != "absent" {
				if err := os.Mkdir(out, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if tc.out == "non-empty" {
				writeFile(t, out, "keep", "keep me\n", 0o600)
			}
			var before string
			if tc.out != "absent" {
				before = dirState(t, out)
			}
			dir, args := tc.dir, tc.args(out)
			r := p.prepare(t, dir, args...)
			if want := "prepare.sh: " + tc.advice(out) + "\n"; r.code != 2 || r.stdout != "" || r.stderr != want {
				t.Fatalf("want exit 2 and stderr exactly %q: %s", want, r)
			}
			if tc.out == "absent" {
				if _, err := os.Lstat(out); !os.IsNotExist(err) {
					t.Errorf("the refusal created out-dir %s (%v)", out, err)
				}
			} else if after := dirState(t, out); after != before {
				t.Errorf("the refusal changed out-dir %s:\nbefore %q\nafter  %q", out, before, after)
			}
			if tc.follow == nil {
				return
			}
			tc.follow(&dir, args, out)
			assertPrepared(t, p, tc.mode, p.prepare(t, dir, args...), out)
		})
	}
}
