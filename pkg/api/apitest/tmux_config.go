package apitest

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/gabemahoney/agent-director/internal/config"
)

// TmuxSetting is one [tmux] key and the TOML value WriteTmuxConfig writes for
// it. Build one with TmuxInt (the normal case, including 0 and negative
// values) or, for the malformed cases, TmuxFloat, TmuxString or TmuxBool,
// which write a raw non-integer TOML value. The key is a config.TmuxKey;
// config.TmuxKeys() lists all nine in table order, so tests never spell a key
// name (SR-20.2, SR-20.3).
type TmuxSetting struct {
	key   config.TmuxKey
	value any
}

// TmuxInt sets key k to the TOML integer v, the normal case. v is written as
// given: 0 and negative values are written too, for the default (0 falls back
// to the key's default) and refusal cases.
func TmuxInt(k config.TmuxKey, v int64) TmuxSetting {
	return TmuxSetting{key: k, value: v}
}

// TmuxFloat sets key k to the raw TOML float v (always written as a float,
// e.g. 1.0, never as an integer), for the malformed cases.
func TmuxFloat(k config.TmuxKey, v float64) TmuxSetting {
	return TmuxSetting{key: k, value: v}
}

// TmuxString sets key k to the raw TOML string v, for the malformed cases.
func TmuxString(k config.TmuxKey, v string) TmuxSetting {
	return TmuxSetting{key: k, value: v}
}

// TmuxBool sets key k to the raw TOML boolean v, for the malformed cases.
func TmuxBool(k config.TmuxKey, v bool) TmuxSetting {
	return TmuxSetting{key: k, value: v}
}

// WriteTmuxConfig writes a config file at path whose [tmux] table holds
// exactly the given settings; a key with no setting is omitted, and a later
// setting of the same key replaces an earlier one. It creates the parent
// directories (0o700) and writes the file owner-only (0o600), replacing any
// existing file. path is the test's choice, e.g. a throwaway HOME's
// .agent-director/config.toml for CLI tests or a file under t.TempDir(); it
// must never be the real ~/.agent-director. An I/O or encoding error fails
// the test through t.
//
// It is the only way pkg/api, CLI and MCP tests write [tmux] timing settings
// (SR-20.2, SR-20.3). Outside internal/config and its own tests, no test
// spells a [tmux] key or writes [tmux] TOML by hand: a key name in an
// assertion comes from k.Name(), and a default or safe minimum comes from
// the internal/config constants (Default*, Min*) or
// config.PendingGraceMinimumSeconds, never a literal. Inline key spellings
// and literal defaults drift when a key, default or minimum changes; this
// writer takes every name from internal/config, so a rename breaks the build
// instead of silently testing the wrong key.
func WriteTmuxConfig(t testing.TB, path string, settings ...TmuxSetting) {
	t.Helper()
	writeConfig(t, path, map[string]any{"tmux": tmuxTable(settings)})
}

// WriteRetentionConfig is WriteTmuxConfig with [defaults]
// expire_retention_days set to the TOML integer days as well (b.sgw), for
// the accepted and the refused (negative, above config.MaxExpireRetentionDays)
// values alike. It is the only way pkg/api, CLI and MCP tests write that key.
func WriteRetentionConfig(t testing.TB, path string, days int64, settings ...TmuxSetting) {
	t.Helper()
	writeConfig(t, path, map[string]any{
		"defaults": map[string]any{"expire_retention_days": days},
		"tmux":     tmuxTable(settings),
	})
}

// tmuxTable is the [tmux] table holding settings, a later setting of a key
// replacing an earlier one.
func tmuxTable(settings []TmuxSetting) map[string]any {
	table := make(map[string]any, len(settings))
	for _, s := range settings {
		table[s.key.Name()] = s.value
	}
	return table
}

// writeConfig encodes tables as TOML and writes them to path as
// WriteTmuxConfig describes.
func writeConfig(t testing.TB, path string, tables map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(tables); err != nil {
		t.Fatalf("write config: encode: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("write config: MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write config: WriteFile: %v", err)
	}
}
