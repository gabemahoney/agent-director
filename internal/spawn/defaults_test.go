package spawn

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
)

// fakeChecker is a CollisionChecker test double: scripted state/exists/err
// return, records the lookups so tests can assert it was (or wasn't) consulted.
type fakeChecker struct {
	state   string
	exists  bool
	err     error
	lookups []string
}

func (f *fakeChecker) SpawnState(id string) (string, bool, error) {
	f.lookups = append(f.lookups, id)
	return f.state, f.exists, f.err
}

// uuid4 is RFC 4122's version-4 string form.
var uuid4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// TestApplyDefaultsPreCheckOutcomes pins SR-9.3's one pre-check read: minted,
// no row, nil checker and a finished row with the reuse opt-in each give their
// IDCheck; a live row, and a finished row without the opt-in (b.hjs), collide.
func TestApplyDefaultsPreCheckOutcomes(t *testing.T) {
	const id = "deadbeef-0000-4000-8000-000000000001"
	const finished = "ErrInstanceIdCollision: " + id // no "already live", no reuse hint
	const live = finished + " already live"
	cases := []struct {
		name      string
		id        string
		reuse     bool
		checker   *fakeChecker // nil passes no CollisionChecker
		want      IDCheck
		wantErr   string // the exact ErrInstanceIdCollision text; "" for none
		wantReads int
	}{
		{"minted UUID4 makes no read", "", false, &fakeChecker{state: "waiting", exists: true, err: errors.New("disk I/O error")}, IDMinted, "", 0},
		{"no row", id, false, &fakeChecker{}, IDNoRow, "", 1},
		{"ended row", id, false, &fakeChecker{state: "ended", exists: true}, 0, finished, 1},
		{"missing row", id, false, &fakeChecker{state: "missing", exists: true}, 0, finished, 1},
		{"ended row with reuse", id, true, &fakeChecker{state: "ended", exists: true}, IDFinishedRow, "", 1},
		{"missing row with reuse", id, true, &fakeChecker{state: "missing", exists: true}, IDFinishedRow, "", 1},
		{"no checker", id, false, nil, IDNotChecked, "", 0},
		{"pending row", id, false, &fakeChecker{state: "pending", exists: true}, 0, live, 1},
		{"waiting row", id, false, &fakeChecker{state: "waiting", exists: true}, 0, live, 1},
		{"waiting row with reuse", id, true, &fakeChecker{state: "waiting", exists: true}, 0, live, 1},
		{"check_permission row", id, false, &fakeChecker{state: "check_permission", exists: true}, 0, live, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Resolved{SpawnParams: SpawnParams{CWD: "/tmp", ClaudeInstanceID: tc.id, ReuseFinished: tc.reuse}}
			var checker CollisionChecker
			if tc.checker != nil {
				checker = tc.checker
			}
			got, err := ApplyDefaults(&r, config.Default(), checker)
			if tc.wantErr != "" {
				if !errors.Is(err, ErrInstanceIdCollision) || err.Error() != tc.wantErr {
					t.Fatalf("err = %v; want ErrInstanceIdCollision %q", err, tc.wantErr)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("ApplyDefaults = (%v, %v); want (%v, nil)", got, err, tc.want)
			}
			if tc.id != "" && r.ClaudeInstanceID != tc.id {
				t.Fatalf("explicit id overwritten: %q", r.ClaudeInstanceID)
			}
			if tc.id == "" && !uuid4.MatchString(r.ClaudeInstanceID) {
				t.Fatalf("minted ClaudeInstanceID = %q; not a UUID4", r.ClaudeInstanceID)
			}
			if tc.checker != nil && len(tc.checker.lookups) != tc.wantReads {
				t.Fatalf("lookups = %v; want %d read(s)", tc.checker.lookups, tc.wantReads)
			}
		})
	}
}

// TestApplyDefaultsPreCheckReadError pins SR-9.3/SR-1.8: a failed collision
// pre-check read maps through PreCheckReadError, never to a sentinel, even
// when the store error's own chain carries one.
func TestApplyDefaultsPreCheckReadError(t *testing.T) {
	sentinels := []error{
		ErrCwdMissing, ErrCwdNotAPath, ErrCwdNotFound, ErrCwdNotADirectory,
		ErrRelayModeInvalid, ErrSpawnDeniedFlag, ErrReservedEnvKey,
		ErrInstanceIdCollision, ErrTmuxSessionNameEmpty,
		ErrTmuxSessionNameInvalid, ErrTmuxSessionNameTooLong, ErrClaudeJSONMissing,
	}
	cases := []struct {
		name     string
		storeErr error
	}{
		{"plain store error", errors.New("store: live spawn lookup: disk I/O error")},
		{"chain carries ErrInstanceIdCollision", fmt.Errorf("store: live spawn lookup: %w", ErrInstanceIdCollision)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const id = "22222222-2222-4222-8222-222222222222"
			r := Resolved{SpawnParams: SpawnParams{CWD: "/tmp", ClaudeInstanceID: id}}
			checker := &fakeChecker{state: "waiting", exists: true, err: tc.storeErr}
			_, err := ApplyDefaults(&r, config.Default(), checker)
			if err == nil {
				t.Fatal("ApplyDefaults returned nil; want the pre-check read error")
			}
			if len(checker.lookups) != 1 || checker.lookups[0] != id {
				t.Fatalf("lookups = %v; want exactly [%s]", checker.lookups, id)
			}
			if got, want := err.Error(), PreCheckReadError(tc.storeErr).Error(); got != want {
				t.Fatalf("err = %q; want PreCheckReadError's %q", got, want)
			}
			if errors.Is(err, tc.storeErr) {
				t.Fatalf("err = %v; wraps the store error (want text only)", err)
			}
			for _, s := range sentinels {
				if errors.Is(err, s) {
					t.Fatalf("err = %v; matches %v (want no catalogued sentinel)", err, s)
				}
			}
		})
	}
}

func TestSanitizeSessionNameCases(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"foo", "foo"},
		{"foo-bar_baz", "foo-bar_baz"},
		{"foo bar!", "foo-bar-"},
		{"////", "root"},
		{"", "root"},
		{"--", "root"},
		{"héllo", "h-llo"},
		{`$a\b$`, "-a-b-"}, // SR-9.2: default names never contain `$` or `\`
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := SanitizeSessionName(tc.in)
			if got != tc.want {
				t.Fatalf("SanitizeSessionName(%q) = %q; want %q", tc.in, got, tc.want)
			}
			if strings.ContainsAny(got, `$\`) {
				t.Fatalf("SanitizeSessionName(%q) = %q; contains $ or \\ (SR-9.2)", tc.in, got)
			}
		})
	}
}

// TestApplyDefaultsNameAndRelayMode pins the composed name
// "<sanitized-basename>-<sanitized-id[:8]>" (never `$` or `\`, SR-9.2; no '.',
// b.gqe), a supplied name kept verbatim (SR-3.1), and relay_mode from the
// config only when the caller gave none.
func TestApplyDefaultsNameAndRelayMode(t *testing.T) {
	const id = "abcdef1234567890"
	cases := []struct{ name, cwd, id, tmuxName, relay, want, wantRelay string }{
		{"plain", "/home/horde/projects/foo", id, "", "", "foo-abcdef12", "on"},
		{"dollar and backslash in basename", `/tmp/$x\y`, id, "", "", "-x-y-abcdef12", "on"},
		{"dollar and backslash in id prefix", "/p/foo", `a$b\cdef1234567890`, "", "", "foo-a-b-cdef", "on"},
		{"dot in id", "/p/foo", "b.18k-fix-test", "", "", "foo-b-18k-fi", "on"},
		{"all-bad basename", "/////", id, "", "", "root-abcdef12", "on"},
		{"supplied name and relay mode kept", "/p/foo", id, "bot-claude-status", "off", "bot-claude-status", "off"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Resolved{SpawnParams: SpawnParams{CWD: tc.cwd, ClaudeInstanceID: tc.id, RelayMode: tc.relay,
				TmuxSessionName: tc.tmuxName, TmuxSessionNameSupplied: tc.tmuxName != ""}}
			cfg := config.Default()
			cfg.Defaults.RelayMode = "on"
			if _, err := ApplyDefaults(&r, cfg, &fakeChecker{}); err != nil {
				t.Fatalf("ApplyDefaults: %v", err)
			}
			if r.TmuxSessionName != tc.want || r.RelayMode != tc.wantRelay {
				t.Fatalf("name, relay mode = %q, %q; want %q, %q", r.TmuxSessionName, r.RelayMode, tc.want, tc.wantRelay)
			}
		})
	}
}
