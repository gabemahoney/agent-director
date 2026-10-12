package api

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/testsupport/dbpathfix"
)

// installShDbPathRefusal and installShLogRefusal are the first stderr line of
// ad_store_db_path's refusal: the one that reads only [store] db_path, and the
// pre-flight form that also reads [log] error_log_path.
const (
	installShDbPathRefusal = "install.sh: cannot tell which store database agent-director opens; refusing to install."
	installShLogRefusal    = "install.sh: cannot tell which store database or error log agent-director opens; refusing to install."
)

// installShOldBase is the base install.sh passes while a move is due: the
// config file is still in the old directory. The package under test names only
// the new directory; the old one lives here, in the test.
var installShOldBase = dbpathfix.Base{Name: "old", Dir: func(home string) string { return home + "/.agent-director" }}

// installShBases is the base-directory column every case runs under.
var installShBases = []dbpathfix.Base{dbpathfix.NewBase, installShOldBase}

// rawPathKeys reads the two path keys of a config file as written, nil for a
// key that is not set (and for a file that is missing, a directory or not
// TOML, which the cases that use it never are). It is the test's own oracle
// input, not the reader's or config.Load's.
func rawPathKeys(path string) (db, errLog *string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	var f struct {
		Store struct {
			DbPath *string `toml:"db_path"`
		} `toml:"store"`
		Log struct {
			ErrorLogPath *string `toml:"error_log_path"`
		} `toml:"log"`
	}
	if _, err := toml.Decode(string(data), &f); err != nil {
		return nil, nil
	}
	return f.Store.DbPath, f.Log.ErrorLogPath
}

// wantPath is SR-11.7's rule for a path key, written out for any base: an
// absolute value as written, a "~/" value joined onto HOME, any other value
// joined onto base, and an unset or empty one def.
func wantPath(raw *string, def, home, base string) string {
	switch {
	case raw == nil || *raw == "":
		return def
	case strings.HasPrefix(*raw, "~/"):
		return filepath.Join(home, (*raw)[2:])
	case filepath.IsAbs(*raw):
		return *raw
	default:
		return filepath.Join(base, *raw)
	}
}

// TestInstallShDbPathMatchesGo is the b.2io drift guard: for every config and
// HOME in dbpathfix, install.sh's [store] db_path reader names the store the
// binary opens (config.Load, then resolveStorePath), or refuses the file. It
// runs under both base directories: with the new one the reader matches Go's
// resolution; with the old one (a move is due) it gives the same join onto
// the old directory, an absolute or "~/" value unchanged.
func TestInstallShDbPathMatchesGo(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	r := dbpathfix.NewReader(t, filepath.Join("..", "..", "skills", "install-agent-director", "install.sh"))
	dir := t.TempDir()
	for i, c := range dbpathfix.Cases {
		cfg := dbpathfix.WriteConfig(t, dir, i, c)
		for _, home := range dbpathfix.Homes {
			for _, base := range installShBases {
				t.Run(fmt.Sprintf("%s/HOME=%q/base=%s", c.Name, home, base.Name), func(t *testing.T) {
					t.Setenv("HOME", home)
					got := r.ResolveIn(t, cfg, home, base)
					loaded, err := config.Load(cfg)
					want := ""
					if err == nil {
						want, err = resolveStorePath("", loaded)
					}
					switch c.Expect {
					case dbpathfix.Same:
						if err != nil {
							t.Fatalf("Go refuses a config the matrix expects it to read: %v", err)
						}
						rawDb, _ := rawPathKeys(cfg)
						if oracle := wantPath(rawDb, filepath.Join(dbpathfix.NewBase.Dir(home), "state.db"), home, dbpathfix.NewBase.Dir(home)); oracle != want {
							t.Fatalf("the test's join rule gives %q for the new base; Go resolves %q", oracle, want)
						}
						if base.Name == dbpathfix.NewBase.Name {
							// Go's own resolution, for the new base.
							if got.Code != 0 || got.Stdout != want+"\n" || got.Stderr != "" {
								t.Errorf("reader: status %d, stdout %q, stderr %q; want status 0 and Go's store path %q",
									got.Code, got.Stdout, got.Stderr, want)
							}
							return
						}
						oldDir := base.Dir(home)
						wantOld := wantPath(rawDb, filepath.Join(oldDir, "state.db"), home, oldDir)
						if got.Code != 0 || got.Stdout != wantOld+"\n" || got.Stderr != "" {
							t.Errorf("reader: status %d, stdout %q, stderr %q; want status 0 and %q (the value joined onto %q)",
								got.Code, got.Stdout, got.Stderr, wantOld, oldDir)
						}
					case dbpathfix.ReaderRefuses:
						head, _, _ := strings.Cut(got.Stderr, "\n")
						if got.Code != 1 || got.Stdout != "" || head != installShDbPathRefusal {
							t.Errorf("reader: status %d, stdout %q, stderr %q; want a refusal (status 1, nothing on stdout)",
								got.Code, got.Stdout, got.Stderr)
						}
					case dbpathfix.GoRefuses:
						if err == nil {
							t.Errorf("config.Load accepts the config (store %q); the matrix expects it refused", want)
						}
						if got.Code != 0 {
							t.Errorf("reader: status %d, stderr %q; want it to pass the file to the binary", got.Code, got.Stderr)
						}
					}
				})
			}
		}
	}
}

// TestInstallShErrorLogPathMatchesGo is the guard for the pre-flight form of
// the reader (SR-11.7), which also reads [log] error_log_path under db_path's
// rules except that a '?' is no refusal in it. For LogCases and for every
// db_path case the reader accepts, under each HOME and base: the first line is
// the store path the plain form prints, and the second is the error-log path
// (empty when the file does not set it or sets it empty), which with the new
// base is the one Go resolves (config.Load); with the old base it is the value
// joined onto the old directory, an absolute or "~/" value unchanged. A file
// the reader refuses is refused with the pre-flight wording and nothing on
// stdout.
func TestInstallShErrorLogPathMatchesGo(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	r := dbpathfix.NewReader(t, filepath.Join("..", "..", "skills", "install-agent-director", "install.sh"))
	dir := t.TempDir()
	cases := append(append([]dbpathfix.Case{}, dbpathfix.LogCases...), dbpathfix.Cases...)
	for i, c := range cases {
		if c.Expect == dbpathfix.GoRefuses {
			// The plain reader passes these over; the pre-flight form may read
			// the [log] key a case holds and refuse it, as it should.
			continue
		}
		cfg := dbpathfix.WriteConfig(t, dir, i, c)
		for _, home := range dbpathfix.Homes {
			for _, base := range installShBases {
				t.Run(fmt.Sprintf("%s/HOME=%q/base=%s", c.Name, home, base.Name), func(t *testing.T) {
					t.Setenv("HOME", home)
					got := r.ResolveWithLog(t, cfg, home, base)
					if c.Expect == dbpathfix.ReaderRefuses {
						head, _, _ := strings.Cut(got.Stderr, "\n")
						if got.Code != 1 || got.Stdout != "" || head != installShLogRefusal {
							t.Errorf("reader: status %d, stdout %q, stderr %q; want a refusal with the pre-flight wording (status 1, nothing on stdout)",
								got.Code, got.Stdout, got.Stderr)
						}
						return
					}
					if got.Code != 0 || got.Stderr != "" {
						t.Fatalf("reader: status %d, stderr %q; want status 0", got.Code, got.Stderr)
					}
					gotDb, gotLog := got.Split(t)
					plain := r.ResolveIn(t, cfg, home, base)
					if gotDb+"\n" != plain.Stdout {
						t.Errorf("store path with log = %q; the plain form prints %q", gotDb, plain.Stdout)
					}
					rawDb, rawLog := rawPathKeys(cfg)
					baseDir := base.Dir(home)
					if want := wantPath(rawLog, "", home, baseDir); gotLog != want {
						t.Errorf("error-log path = %q; want %q (value %v, base %s)", gotLog, want, rawLog, baseDir)
					}
					if want := wantPath(rawDb, filepath.Join(baseDir, "state.db"), home, baseDir); gotDb != want {
						t.Errorf("store path = %q; want %q", gotDb, want)
					}
					if base.Name != dbpathfix.NewBase.Name || rawLog == nil {
						return
					}
					// A set key, with the new base: Go's own resolution (an
					// unset one resolves to Go's default, which the reader
					// leaves empty).
					loaded, err := config.Load(cfg)
					if err != nil {
						t.Fatalf("Go refuses a config the matrix expects it to read: %v", err)
					}
					if loaded.Log.ErrorLogPath != gotLog {
						t.Errorf("error-log path = %q; Go resolves %q", gotLog, loaded.Log.ErrorLogPath)
					}
				})
			}
		}
	}
}

// installShLaunchRefusal is the first stderr line of ad_store_db_path's refusal
// of a [launch] header, key or value. A refusal of a line that is no [launch]
// line (a directory, a UTF-16 byte-order mark, a """ string) keeps the plain
// form's headline, so a [launch] case checks only the stem.
const (
	installShLaunchRefusal = "install.sh: cannot tell which programs agent-director launches (the [launch] table); refusing to install."
	installShRefusalStem   = "install.sh: cannot tell which "
)

// launchReaders are the two copies of the reader a [launch] value is read with:
// install.sh's block, and the verbatim copy in uninstall.sh (SR-11.4).
func launchReaders(t *testing.T) []launchReader {
	t.Helper()
	skills := filepath.Join("..", "..", "skills", "install-agent-director")
	return []launchReader{
		{"install.sh", dbpathfix.NewReader(t, filepath.Join(skills, "install.sh"))},
		{"uninstall.sh", dbpathfix.NewUninstallReader(t, filepath.Join(skills, "uninstall.sh"))},
	}
}

type launchReader struct {
	name string
	r    *dbpathfix.Reader
}

// TestInstallShLaunchPathMatchesGo is the [launch] drift guard (SR-11.1,
// SR-14.3): for every LaunchCases config, under each base and, where a "~/"
// value makes it matter, each HOME, the reader's launch lines are the paths
// config.Load resolves (claude_path, then codex_path), identical under both
// bases, or the reader refuses the file. A refusal where Go loads the file is
// allowed only for a case the matrix marks ReaderRefuses (the reader is
// stricter: letter case, forms it does not read); a case marked BothRefuse is
// refused by config.Load as well. install.sh's block and uninstall.sh's copy
// of it run every case and must give the same result as each other.
func TestInstallShLaunchPathMatchesGo(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	readers := launchReaders(t)
	dir := t.TempDir()
	for i, c := range dbpathfix.LaunchCases {
		cfg := dbpathfix.WriteConfig(t, dir, i, c)
		homes := dbpathfix.Homes[:1]
		if strings.Contains(c.Config, "~/") {
			homes = dbpathfix.Homes
		}
		for _, home := range homes {
			for _, base := range installShBases {
				t.Run(fmt.Sprintf("%s/HOME=%q/base=%s", c.Name, home, base.Name), func(t *testing.T) {
					t.Setenv("HOME", home)
					loaded, goErr := config.Load(cfg)
					var results []dbpathfix.Result
					for _, rd := range readers {
						got := rd.r.ResolveLaunch(t, cfg, home, base)
						results = append(results, got)
						switch c.Expect {
						case dbpathfix.Same, dbpathfix.GoRefuses:
							if (c.Expect == dbpathfix.GoRefuses) != (goErr != nil) {
								t.Fatalf("config.Load error %v; the matrix expects Go to refuse only for GoRefuses", goErr)
							}
							if got.Code != 0 || got.Stderr != "" {
								t.Fatalf("%s: status %d, stderr %q; want status 0", rd.name, got.Code, got.Stderr)
							}
							if c.Expect == dbpathfix.GoRefuses {
								continue
							}
							lines := got.Lines(t, 3)
							if lines[1] != loaded.Launch.ClaudePath || lines[2] != loaded.Launch.CodexPath {
								t.Errorf("%s: claude_path %q, codex_path %q; Go resolves %q and %q",
									rd.name, lines[1], lines[2], loaded.Launch.ClaudePath, loaded.Launch.CodexPath)
							}
						case dbpathfix.ReaderRefuses, dbpathfix.BothRefuse:
							if (c.Expect == dbpathfix.BothRefuse) != (goErr != nil) {
								t.Fatalf("config.Load error %v; the matrix expects Go to refuse only for BothRefuse", goErr)
							}
							head, _, _ := strings.Cut(got.Stderr, "\n")
							if got.Code != 1 || got.Stdout != "" || !strings.HasPrefix(head, installShRefusalStem) {
								t.Errorf("%s: status %d, stdout %q, stderr %q; want a refusal (status 1, nothing on stdout)",
									rd.name, got.Code, got.Stdout, got.Stderr)
							}
						}
					}
					if results[0] != results[1] {
						t.Errorf("uninstall.sh's copy of the reader gives %+v; install.sh's gives %+v", results[1], results[0])
					}
				})
			}
		}
	}
}

// genericRefusal reports whether a LaunchCases refusal is one the reader makes
// of a line in the shapes it refuses for every table (a dotted header or key, a
// multi-line string or array, a UTF-16 byte-order mark), which keep the plain
// form's headline.
func genericRefusal(name string) bool {
	for _, s := range []string{"header-dotted", "key-dotted-in-launch", "key-dotted-key", "multiline-", "array-hides-lines", "utf16-bom"} {
		if strings.Contains(name, s) {
			return true
		}
	}
	return false
}

// TestInstallShLaunchHeadline: a refusal of a [launch] header, key or value
// carries the [launch] headline, not the store's (SR-11.1), in the launch form
// and in the form that also reads [log].
func TestInstallShLaunchHeadline(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	t.Setenv("HOME", dbpathfix.Homes[0])
	r := dbpathfix.NewReader(t, filepath.Join("..", "..", "skills", "install-agent-director", "install.sh"))
	dir := t.TempDir()
	for i, c := range dbpathfix.LaunchCases {
		if (c.Expect != dbpathfix.BothRefuse && c.Expect != dbpathfix.ReaderRefuses) || c.Form != dbpathfix.File || genericRefusal(c.Name) {
			continue
		}
		cfg := dbpathfix.WriteConfig(t, dir, i, c)
		t.Run(c.Name, func(t *testing.T) {
			for form, got := range map[string]dbpathfix.Result{
				"launch":     r.ResolveLaunch(t, cfg, dbpathfix.Homes[0], dbpathfix.NewBase),
				"log+launch": r.ResolveLogLaunch(t, cfg, dbpathfix.Homes[0], dbpathfix.NewBase),
			} {
				head, _, _ := strings.Cut(got.Stderr, "\n")
				if got.Code != 1 || got.Stdout != "" || head != installShLaunchRefusal {
					t.Errorf("%s form: status %d, stdout %q, stderr %q; want status 1 and the [launch] headline",
						form, got.Code, got.Stdout, got.Stderr)
				}
			}
		})
	}
}

// TestInstallShLaunchLogForm: the pre-flight form that also reads [log]
// (the move is due) prints the same launch paths after the error-log line as
// the launch form does, for every LaunchCases file the reader accepts, and
// refuses the files the launch form refuses.
func TestInstallShLaunchLogForm(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	t.Setenv("HOME", dbpathfix.Homes[0])
	r := dbpathfix.NewReader(t, filepath.Join("..", "..", "skills", "install-agent-director", "install.sh"))
	dir := t.TempDir()
	for i, c := range dbpathfix.LaunchCases {
		cfg := dbpathfix.WriteConfig(t, dir, i, c)
		for _, base := range installShBases {
			t.Run(fmt.Sprintf("%s/base=%s", c.Name, base.Name), func(t *testing.T) {
				plain := r.ResolveLaunch(t, cfg, dbpathfix.Homes[0], base)
				got := r.ResolveLogLaunch(t, cfg, dbpathfix.Homes[0], base)
				if plain.Code != 0 {
					if got.Code != 1 || got.Stdout != "" {
						t.Errorf("log+launch form: status %d, stdout %q; want a refusal like the launch form's (status %d)", got.Code, got.Stdout, plain.Code)
					}
					return
				}
				if got.Code != 0 || got.Stderr != "" {
					t.Fatalf("log+launch form: status %d, stderr %q; want status 0", got.Code, got.Stderr)
				}
				want, have := plain.Lines(t, 3), got.Lines(t, 4)
				if have[0] != want[0] || have[2] != want[1] || have[3] != want[2] {
					t.Errorf("log+launch lines %q; the launch form's are %q (store, claude_path, codex_path)", have, want)
				}
			})
		}
	}
}

// TestInstallShUninstallReaderMatchesInstall: uninstall.sh's copy of the
// reader gives the same result as install.sh's for the db_path and error-log
// matrices too, in the launch form it reads with, so an edit to one copy that
// misses the other fails here (SR-11.4).
func TestInstallShUninstallReaderMatchesInstall(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	t.Setenv("HOME", dbpathfix.Homes[0])
	readers := launchReaders(t)
	dir := t.TempDir()
	cases := append(append([]dbpathfix.Case{}, dbpathfix.Cases...), dbpathfix.LogCases...)
	for i, c := range cases {
		cfg := dbpathfix.WriteConfig(t, dir, i, c)
		for _, base := range installShBases {
			t.Run(fmt.Sprintf("%s/base=%s", c.Name, base.Name), func(t *testing.T) {
				want := readers[0].r.ResolveLaunch(t, cfg, dbpathfix.Homes[0], base)
				if got := readers[1].r.ResolveLaunch(t, cfg, dbpathfix.Homes[0], base); got != want {
					t.Errorf("uninstall.sh's copy of the reader gives %+v; install.sh's gives %+v", got, want)
				}
			})
		}
	}
}
