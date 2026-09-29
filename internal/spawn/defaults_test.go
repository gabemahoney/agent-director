package spawn

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
)

// fakeChecker is a CollisionChecker test double: scripted bool/err return,
// records the lookups so tests can assert it was (or wasn't) consulted.
type fakeChecker struct {
	exists  bool
	err     error
	lookups []string
}

func (f *fakeChecker) LiveSpawnExists(id string) (bool, error) {
	f.lookups = append(f.lookups, id)
	return f.exists, f.err
}

// TestApplyDefaultsMintsUuid4 also pins that an empty id never consults the
// checker: a failing checker is ignored and a fresh id is minted.
func TestApplyDefaultsMintsUuid4(t *testing.T) {
	r := Resolved{SpawnParams: SpawnParams{CWD: "/tmp"}}
	cfg := config.Default()
	checker := &fakeChecker{err: errors.New("store: live spawn lookup: disk I/O error")}
	if err := ApplyDefaults(&r, cfg, checker); err != nil {
		t.Fatalf("ApplyDefaults: %v", err)
	}
	if len(checker.lookups) != 0 {
		t.Fatalf("checker consulted for an empty id: lookups=%v", checker.lookups)
	}
	// UUID4 string form per RFC 4122: 8-4-4-4-12 hex, version nibble = 4,
	// variant nibble in {8,9,a,b}. Use a fail-fast regex assertion.
	uuid4 := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !uuid4.MatchString(r.ClaudeInstanceID) {
		t.Fatalf("ClaudeInstanceID = %q; not a UUID4", r.ClaudeInstanceID)
	}
}

func TestApplyDefaultsRespectsExplicitID(t *testing.T) {
	r := Resolved{SpawnParams: SpawnParams{
		CWD:              "/tmp",
		ClaudeInstanceID: "deadbeef-0000-4000-8000-000000000001",
	}}
	cfg := config.Default()
	checker := &fakeChecker{exists: false}
	if err := ApplyDefaults(&r, cfg, checker); err != nil {
		t.Fatalf("ApplyDefaults: %v", err)
	}
	if r.ClaudeInstanceID != "deadbeef-0000-4000-8000-000000000001" {
		t.Fatalf("explicit id overwritten: %q", r.ClaudeInstanceID)
	}
	if len(checker.lookups) != 1 || checker.lookups[0] != r.ClaudeInstanceID {
		t.Fatalf("collision check not run: lookups=%v", checker.lookups)
	}
}

func TestApplyDefaultsCollisionError(t *testing.T) {
	r := Resolved{SpawnParams: SpawnParams{
		CWD:              "/tmp",
		ClaudeInstanceID: "11111111-1111-4111-8111-111111111111",
	}}
	cfg := config.Default()
	err := ApplyDefaults(&r, cfg, &fakeChecker{exists: true})
	if !errors.Is(err, ErrInstanceIdCollision) {
		t.Fatalf("err = %v; want ErrInstanceIdCollision", err)
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
		{"chain carries ErrCwdNotFound", fmt.Errorf("store: live spawn lookup: %w", ErrCwdNotFound)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const id = "22222222-2222-4222-8222-222222222222"
			r := Resolved{SpawnParams: SpawnParams{CWD: "/tmp", ClaudeInstanceID: id}}
			checker := &fakeChecker{exists: true, err: tc.storeErr}
			err := ApplyDefaults(&r, config.Default(), checker)
			if err == nil {
				t.Fatal("ApplyDefaults returned nil; want the pre-check read error")
			}
			if len(checker.lookups) != 1 || checker.lookups[0] != id {
				t.Fatalf("lookups = %v; want exactly [%s]", checker.lookups, id)
			}
			if got, want := err.Error(), PreCheckReadError(tc.storeErr).Error(); got != want {
				t.Fatalf("err = %q; want PreCheckReadError's %q", got, want)
			}
			if !strings.Contains(err.Error(), "the collision pre-check could not read the store") {
				t.Fatalf("err = %q; missing the pre-check phrase", err)
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

func TestApplyDefaultsRelayModeFallsBackToConfig(t *testing.T) {
	r := Resolved{SpawnParams: SpawnParams{CWD: "/tmp"}}
	cfg := config.Default()
	cfg.Defaults.RelayMode = "on"
	if err := ApplyDefaults(&r, cfg, &fakeChecker{}); err != nil {
		t.Fatalf("ApplyDefaults: %v", err)
	}
	if r.RelayMode != "on" {
		t.Fatalf("RelayMode = %q; want on", r.RelayMode)
	}
}

func TestApplyDefaultsRelayModePreservesExplicit(t *testing.T) {
	r := Resolved{SpawnParams: SpawnParams{CWD: "/tmp", RelayMode: "off"}}
	cfg := config.Default()
	cfg.Defaults.RelayMode = "on" // config says on but caller said off
	if err := ApplyDefaults(&r, cfg, &fakeChecker{}); err != nil {
		t.Fatalf("ApplyDefaults: %v", err)
	}
	if r.RelayMode != "off" {
		t.Fatalf("RelayMode = %q; want off (caller-supplied)", r.RelayMode)
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
		{"123abc", "123abc"},
		{"héllo", "h-llo"},
		// SR-9.2: default names never contain `$` or `\`.
		{"a$b", "a-b"},
		{`a\b`, "a-b"},
		{`$a\b$`, "-a-b-"},
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

// TestComposeSessionName pins "<sanitized-basename>-<sanitized-id[:8]>".
// The `$` / `\` rows pin SR-9.2: a default (composed) name never contains
// either character, whether it came from the cwd basename or the id prefix.
func TestComposeSessionName(t *testing.T) {
	cases := []struct {
		name string
		cwd  string
		id   string
		want string
	}{
		{"plain", "/home/horde/projects/foo", "abcdef1234567890", "foo-abcdef12"},
		{"dollar in basename", "/tmp/a$b", "abcdef1234567890", "a-b-abcdef12"},
		{"backslash in basename", `/tmp/a\b`, "abcdef1234567890", "a-b-abcdef12"},
		{"dollar and backslash in basename", `/tmp/$x\y`, "abcdef1234567890", "-x-y-abcdef12"},
		{"dollar in id prefix", "/home/horde/projects/foo", "ab$def1234567890", "foo-ab-def12"},
		{"backslash in id prefix", "/home/horde/projects/foo", `ab\def1234567890`, "foo-ab-def12"},
		{"dollar and backslash in id prefix", "/home/horde/projects/foo", `a$b\cdef1234567890`, "foo-a-b-cdef"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Resolved{SpawnParams: SpawnParams{CWD: tc.cwd, ClaudeInstanceID: tc.id}}
			if err := ApplyDefaults(&r, config.Default(), &fakeChecker{}); err != nil {
				t.Fatalf("ApplyDefaults: %v", err)
			}
			if r.TmuxSessionName != tc.want {
				t.Fatalf("TmuxSessionName = %q; want %q", r.TmuxSessionName, tc.want)
			}
			if strings.ContainsAny(r.TmuxSessionName, `$\`) {
				t.Fatalf("TmuxSessionName = %q; contains $ or \\ (SR-9.2)", r.TmuxSessionName)
			}
		})
	}
}

// TestApplyDefaultsPreservesUserSuppliedTmuxSessionName pins the Epic 1
// regression: when the caller supplied a non-empty TmuxSessionName,
// ApplyDefaults must leave it byte-for-byte alone — no composeSessionName
// suffix, no sanitization (SR-3.1).
func TestApplyDefaultsPreservesUserSuppliedTmuxSessionName(t *testing.T) {
	r := Resolved{SpawnParams: SpawnParams{
		CWD:                     "/home/horde/projects/foo",
		ClaudeInstanceID:        "abcdef1234567890",
		TmuxSessionName:         "bot-claude-status",
		TmuxSessionNameSupplied: true,
	}}
	if err := ApplyDefaults(&r, config.Default(), &fakeChecker{}); err != nil {
		t.Fatalf("ApplyDefaults: %v", err)
	}
	if r.TmuxSessionName != "bot-claude-status" {
		t.Fatalf("TmuxSessionName = %q; want %q (no decoration)", r.TmuxSessionName, "bot-claude-status")
	}
}

// TestComposeSessionNameSanitizesDotInInstanceID is the b.gqe regression
// test: a dot in ClaudeInstanceID must not survive into TmuxSessionName.
// tmux silently maps '.' → '_' on session creation, causing stored name /
// real name divergence and breaking send-keys.
func TestComposeSessionNameSanitizesDotInInstanceID(t *testing.T) {
	r := Resolved{SpawnParams: SpawnParams{
		CWD:              "/home/horde/projects/foo",
		ClaudeInstanceID: "b.18k-fix-test",
	}}
	cfg := config.Default()
	if err := ApplyDefaults(&r, cfg, &fakeChecker{}); err != nil {
		t.Fatalf("ApplyDefaults: %v", err)
	}
	if strings.Contains(r.TmuxSessionName, ".") {
		t.Fatalf("TmuxSessionName = %q; contains dot (diverges from real tmux name)", r.TmuxSessionName)
	}
	// Pin the exact value: basename "foo", idTail first 8 chars of "b.18k-fix-test"
	// = "b.18k-fi" → sanitized → "b-18k-fi", so result = "foo-b-18k-fi".
	want := "foo-b-18k-fi"
	if r.TmuxSessionName != want {
		t.Fatalf("TmuxSessionName = %q; want %q", r.TmuxSessionName, want)
	}
}

func TestComposeSessionNameAllBadBasename(t *testing.T) {
	r := Resolved{SpawnParams: SpawnParams{
		CWD:              "/////",
		ClaudeInstanceID: "abcdef1234567890",
	}}
	if err := ApplyDefaults(&r, config.Default(), &fakeChecker{}); err != nil {
		t.Fatalf("ApplyDefaults: %v", err)
	}
	if !strings.HasPrefix(r.TmuxSessionName, "root-") {
		t.Fatalf("session name %q; want prefix root-", r.TmuxSessionName)
	}
}
