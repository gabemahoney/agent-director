package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/testsupport/dbpathfix"
)

// busyTimeoutCase is one config.toml for install.sh's pre-flight: its [store]
// db_path reader, then its [store] busy_timeout_ms reader (b.c7f). ms is what
// the busy_timeout_ms reader prints, and what the binary's store connections
// use unless goRefuses (the binary then refuses the file at step 3 or 4); with
// ms 0 that reader refuses its last line, saying why, or, with dbPathRefuses,
// the db_path reader stops the install before it.
type busyTimeoutCase struct {
	name, config  string
	noFile        bool
	ms            int
	goRefuses     bool
	why           string
	dbPathRefuses bool
}

// busy is a [store] table setting busy_timeout_ms to v, written as given.
func busy(v string) string { return "[store]\nbusy_timeout_ms = " + v + "\n" }

const notDecimal = "busy_timeout_ms's value is not a whole number in decimal digits, such as"

var busyTimeoutCases = []busyTimeoutCase{
	{name: "missing-file", noFile: true, ms: 10000},
	{name: "empty-file", ms: 10000},
	{name: "store-table-only", config: "[store]\n", ms: 10000},
	{name: "set", config: busy("1234"), ms: 1234},
	{name: "zero", config: busy("0"), ms: 10000},
	{name: "minus-zero", config: busy("-0"), ms: 10000},
	{name: "plus-zero", config: busy("+0"), ms: 10000},
	{name: "one", config: busy("1"), ms: 1},
	{name: "plus-sign", config: busy("+7"), ms: 7},
	{name: "largest", config: busy("2147483647"), ms: 2147483647},
	{name: "underscore", config: busy("1_000"), ms: 1000},
	{name: "underscores-largest", config: busy("2_147_483_647"), ms: 2147483647},
	{name: "comment", config: busy("2500 # ms"), ms: 2500},
	{name: "tight", config: "[store]\nbusy_timeout_ms=2500#ms\n", ms: 2500},
	{name: "tabs-and-trailing-blanks", config: "[store]\n\tbusy_timeout_ms\t=\t2500 \t\n", ms: 2500},
	{name: "header-spaces-comment", config: "[ store ] # timings\nbusy_timeout_ms = 2500\n", ms: 2500},
	{name: "crlf", config: "[store]\r\nbusy_timeout_ms = 2500\r\n", ms: 2500},
	{name: "bom", config: "\xef\xbb\xbf[store]\nbusy_timeout_ms = 2500\n", ms: 2500},
	{name: "bom-crlf", config: "\xef\xbb\xbf[store]\r\nbusy_timeout_ms = 2500\r\n", ms: 2500},
	{name: "no-final-newline", config: "[store]\nbusy_timeout_ms = 2500", ms: 2500},
	{name: "beside-db-path", config: "[store]\ndb_path = \"/x.db\"\nbusy_timeout_ms = 2500\n", ms: 2500},
	{name: "among-tables", config: "[defaults]\nrelay_mode = \"off\"\n[store]\nbusy_timeout_ms = 2500\n[tmux]\nquery_timeout_ms = 500\n",
		ms: 2500},
	// Keys the binary does not read leave the default.
	{name: "other-table", config: "[defaults]\nbusy_timeout_ms = 5\n", ms: 10000},
	{name: "other-table-after-store", config: "[store]\n[relay]\nbusy_timeout_ms = 5\n", ms: 10000},
	{name: "root-key", config: "busy_timeout_ms = 5\n[store]\n", ms: 10000},
	{name: "commented-out", config: "[store]\n# busy_timeout_ms = 5\n", ms: 10000},
	{name: "longer-key", config: "[store]\nbusy_timeout_ms_x = 5\n", ms: 10000},
	{name: "hyphen-key", config: "[store]\nbusy-timeout-ms = 5\n", ms: 10000},
	{name: "go-field-name", config: "[store]\nBusyTimeoutMs = 5\n", ms: 10000},
	{name: "inline-table-in-store", config: "[store]\nt = { busy_timeout_ms = 5 }\n", ms: 10000},

	// The binary refuses these itself; the reader's reads use the default meanwhile.
	{name: "negative", config: busy("-5"), ms: 10000, goRefuses: true},
	{name: "negative-underscored", config: busy("-1_000"), ms: 10000, goRefuses: true},
	{name: "above-largest", config: busy("2147483648"), ms: 10000, goRefuses: true},
	{name: "ten-digits-above-largest", config: busy("9999999999"), ms: 10000, goRefuses: true},
	{name: "int64-max", config: busy("9223372036854775807"), ms: 10000, goRefuses: true},
	{name: "int64-min", config: busy("-9223372036854775808"), ms: 10000, goRefuses: true},
	{name: "beyond-int64", config: busy("99999999999999999999"), ms: 10000, goRefuses: true},
	{name: "other-key-refused", config: "[defaults]\nexpire_retention_days = -1\n" + busy("2500"), ms: 2500, goRefuses: true},

	// The reader refuses these, naming the last line.
	{name: "key-upper", config: "[store]\nBUSY_TIMEOUT_MS = 5\n", why: "Write it as busy_timeout_ms."},
	{name: "key-mixed", config: "[store]\nBusy_Timeout_Ms = 5\n", why: "Write it as busy_timeout_ms."},
	{name: "key-upper-after-key", config: busy("5") + "BUSY_TIMEOUT_MS = 6\n", why: "Write it as busy_timeout_ms."},
	{name: "twice", config: busy("5") + "busy_timeout_ms = 6\n", why: "This sets busy_timeout_ms a second time. Keep one."},
	{name: "twice-same-value", config: busy("5") + "busy_timeout_ms = 5\n", why: "This sets busy_timeout_ms a second time. Keep one."},
	{name: "leading-zero", config: busy("010"), why: notDecimal},
	{name: "zero-zero", config: busy("00"), why: notDecimal},
	{name: "hex", config: busy("0x10"), why: notDecimal},
	{name: "octal", config: busy("0o10"), why: notDecimal},
	{name: "binary", config: busy("0b10"), why: notDecimal},
	{name: "basic-string", config: busy(`"10"`), why: notDecimal},
	{name: "literal-string", config: busy(`'10'`), why: notDecimal},
	{name: "float", config: busy("1.5"), why: notDecimal},
	{name: "float-whole", config: busy("10000.0"), why: notDecimal},
	{name: "exponent", config: busy("1e4"), why: notDecimal},
	{name: "inf", config: busy("inf"), why: notDecimal},
	{name: "boolean", config: busy("true"), why: notDecimal},
	{name: "array", config: busy("[2500]"), why: notDecimal},
	{name: "trailing-underscore", config: busy("1_"), why: notDecimal},
	{name: "leading-underscore", config: busy("_1"), why: notDecimal},
	{name: "double-underscore", config: busy("1__0"), why: notDecimal},
	{name: "underscore-after-sign", config: busy("+_1"), why: notDecimal},
	{name: "double-sign", config: busy("--1"), why: notDecimal},
	{name: "no-value", config: "[store]\nbusy_timeout_ms =\n", why: notDecimal},
	{name: "junk-after-value", config: busy("2500 ms"), why: notDecimal},

	// The binary reads busy_timeout_ms from lines the reader passes over, or
	// not from one it would read; the db_path reader refuses each first.
	{name: "root-dotted", config: "store.busy_timeout_ms = 5\n", dbPathRefuses: true},
	{name: "root-inline", config: "store = { busy_timeout_ms = 5 }\n", dbPathRefuses: true},
	{name: "quoted-key", config: "[store]\n\"busy_timeout_ms\" = 5\n", dbPathRefuses: true},
	{name: "quoted-header", config: "[\"store\"]\nbusy_timeout_ms = 5\n", dbPathRefuses: true},
	{name: "header-mixed-case", config: "[Store]\nbusy_timeout_ms = 5\n", dbPathRefuses: true},
	{name: "store-twice", config: "[store]\n[relay]\npoll_base_ms = 100\n[store]\nbusy_timeout_ms = 5\n", dbPathRefuses: true},
	{name: "multiline-string-hides-lines", config: "[defaults]\nnote = \"\"\"\n[store]\nbusy_timeout_ms = 5\n\"\"\"\n",
		dbPathRefuses: true},
	{name: "array-hides-lines", config: "[store]\nx = [\n[1]\n]\nbusy_timeout_ms = 5\n", dbPathRefuses: true},
}

// TestInstallShBusyTimeoutMatchesGo is the b.c7f drift guard: install.sh's
// busy_timeout_ms reader prints the busy timeout config.Load gives, or refuses.
func TestInstallShBusyTimeoutMatchesGo(t *testing.T) {
	installSh := filepath.Join("..", "..", "skills", "install-agent-director", "install.sh")
	dbPath, r := dbpathfix.NewReader(t, installSh), dbpathfix.NewBusyTimeoutReader(t, installSh)
	for _, c := range busyTimeoutCases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if !c.noFile {
				if err := os.WriteFile(path, []byte(c.config), 0o600); err != nil {
					t.Fatalf("write config: %v", err)
				}
			}
			if res := dbPath.Resolve(t, path, "/home/u"); (res.Code != 0) != c.dbPathRefuses {
				t.Fatalf("db_path reader: status %d, stderr %q; want it to refuse the file: %t", res.Code, res.Stderr,
					c.dbPathRefuses)
			}
			if c.dbPathRefuses {
				return
			}
			got := r.BusyTimeout(t, path)
			cfg, err := config.Load(path)

			if c.ms == 0 {
				lines := strings.Split(strings.TrimSuffix(c.config, "\n"), "\n")
				for _, want := range []string{
					"install.sh: cannot tell how long agent-director waits for a locked store database; refusing to install.\n",
					"  config  : " + path + "\n",
					fmt.Sprintf("  line %d  : %s\n", len(lines), lines[len(lines)-1]),
					c.why,
					"Nothing was installed or changed. Re-run this install after the change.",
				} {
					if !strings.Contains(got.Stderr, want) {
						t.Errorf("reader stderr lacks %q:\n%s", want, got.Stderr)
					}
				}
				if got.Code != 1 || got.Stdout != "" || !strings.HasPrefix(got.Stderr, "install.sh: cannot tell how long") {
					t.Errorf("reader: status %d, stdout %q; want a refusal first on stderr (status 1, nothing on stdout)",
						got.Code, got.Stdout)
				}
				return
			}
			if want := fmt.Sprintf("%d\n", c.ms); got.Code != 0 || got.Stdout != want || got.Stderr != "" {
				t.Errorf("reader: status %d, stdout %q, stderr %q; want status 0 and %q", got.Code, got.Stdout, got.Stderr, want)
			}
			switch {
			case c.goRefuses && err == nil:
				t.Errorf("config.Load accepts the config (busy timeout %d); the case expects it refused",
					cfg.Store.EffectiveBusyTimeoutMs())
			case !c.goRefuses && err != nil:
				t.Errorf("config.Load: %v; the case expects busy timeout %d", err, c.ms)
			case !c.goRefuses && cfg.Store.EffectiveBusyTimeoutMs() != c.ms:
				t.Errorf("the binary's busy timeout is %d; the reader prints %d", cfg.Store.EffectiveBusyTimeoutMs(), c.ms)
			}
		})
	}
}
