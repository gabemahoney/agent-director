package spawn

import (
	"errors"
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

func TestApplyDefaultsMintsUuid4(t *testing.T) {
	r := Resolved{SpawnParams: SpawnParams{CWD: "/tmp"}}
	cfg := config.Default()
	if err := ApplyDefaults(&r, cfg, &fakeChecker{}); err != nil {
		t.Fatalf("ApplyDefaults: %v", err)
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
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := SanitizeSessionName(tc.in)
			if got != tc.want {
				t.Fatalf("SanitizeSessionName(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestComposeSessionName(t *testing.T) {
	// composeSessionName must produce "<sanitized-basename>-<id[:8]>".
	r := Resolved{SpawnParams: SpawnParams{
		CWD:              "/home/horde/projects/foo",
		ClaudeInstanceID: "abcdef1234567890",
	}}
	cfg := config.Default()
	if err := ApplyDefaults(&r, cfg, &fakeChecker{}); err != nil {
		t.Fatalf("ApplyDefaults: %v", err)
	}
	want := "foo-abcdef12"
	if r.TmuxSessionName != want {
		t.Fatalf("TmuxSessionName = %q; want %q", r.TmuxSessionName, want)
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
