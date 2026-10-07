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
// given: 0 and negative values are written too, for the default and refusal
// cases. 0 falls back to the key's default; for pending_grace_seconds, to the
// larger of its default and its derived minimum
// (config.PendingGraceMinimumSeconds; b.9e1).
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
// values alike. Unlike WriteKeysConfig, which leaves a 0 field's key out, it
// writes an explicit 0 too.
func WriteRetentionConfig(t testing.TB, path string, days int64, settings ...TmuxSetting) {
	t.Helper()
	writeConfig(t, path, map[string]any{
		"defaults": map[string]any{"expire_retention_days": days},
		"tmux":     tmuxTable(settings),
	})
}

// ConfigKeys are the keys outside [tmux] with a range that WriteKeysConfig
// sets: [defaults] expire_retention_days (b.sgw), [relay] timeout_seconds and
// [pause] timeout_seconds (b.8q2), [pre_trust] lock_wait_seconds (b.kr4) and
// [store] busy_timeout_ms (b.c7f). A 0 field leaves its key out, which loads
// as a written 0 does: the key's default.
type ConfigKeys struct {
	RetentionDays, RelayTimeoutSeconds, PauseTimeoutSeconds, PreTrustLockWaitSeconds, StoreBusyTimeoutMs int64
}

// WriteKeysConfig is WriteTmuxConfig with keys' non-zero fields set as well,
// to TOML integers, for the accepted and the refused values alike. It is the
// way CLI tests combine those keys with [tmux] settings in one file.
func WriteKeysConfig(t testing.TB, path string, keys ConfigKeys, settings ...TmuxSetting) {
	t.Helper()
	tables := map[string]any{"tmux": tmuxTable(settings)}
	for _, k := range []struct {
		table, key string
		value      int64
	}{
		{"defaults", "expire_retention_days", keys.RetentionDays},
		{"relay", "timeout_seconds", keys.RelayTimeoutSeconds},
		{"pause", "timeout_seconds", keys.PauseTimeoutSeconds},
		{"pre_trust", "lock_wait_seconds", keys.PreTrustLockWaitSeconds},
		{"store", "busy_timeout_ms", keys.StoreBusyTimeoutMs},
	} {
		if k.value != 0 {
			tables[k.table] = map[string]any{k.key: k.value}
		}
	}
	writeConfig(t, path, tables)
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
