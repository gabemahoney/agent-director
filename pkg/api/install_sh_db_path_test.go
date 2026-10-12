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
