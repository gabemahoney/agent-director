package apitest

import (
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
)

// writeAndLoad writes the settings with WriteTmuxConfig under a not-yet-existing
// throwaway .agent-director dir and loads the file with config.Load.
func writeAndLoad(t *testing.T, settings ...TmuxSetting) (config.Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "home", ".agent-director", "config.toml")
	WriteTmuxConfig(t, path, settings...)
	return config.Load(path)
}

// assertTmux checks every key's Value and Effective: want[k] for keys in want,
// the key's default otherwise.
func assertTmux(t *testing.T, got config.Tmux, want map[config.TmuxKey]int64) {
	t.Helper()
	for _, k := range config.TmuxKeys() {
		v, ok := want[k]
		if !ok {
			v = k.DefaultValue()
		}
		if got.Value(k) != v {
			t.Errorf("Value(%s) = %d; want %d", k.Name(), got.Value(k), v)
		}
		if eff := unitDuration(k, v); got.Effective(k) != eff {
			t.Errorf("Effective(%s) = %v; want %v", k.Name(), got.Effective(k), eff)
		}
	}
}

func unitDuration(k config.TmuxKey, v int64) time.Duration {
	if k.Unit() == config.TmuxUnitMilliseconds {
		return time.Duration(v) * time.Millisecond
	}
	return time.Duration(v) * time.Second
}

// requireConfigError fails unless err is a *config.ConfigError and returns its
// description without the path (t.TempDir paths embed the subtest's key name).
func requireConfigError(t *testing.T, err error) string {
	t.Helper()
	var ce *config.ConfigError
	if !errors.As(err, &ce) || ce.Err == nil {
		t.Fatalf("config.Load error = %v (%T); want *config.ConfigError with a cause", err, err)
	}
	return ce.Err.Error()
}

// roundTripValue is a valid non-default value for k: one above its default,
// checked against k's safe minimum and the pending grace period's rule.
func roundTripValue(t *testing.T, k config.TmuxKey) int64 {
	t.Helper()
	v := k.DefaultValue() + 1
	if minimum, ok := config.Default().Tmux.Minimum(k); ok && v < minimum {
		t.Fatalf("precondition: %s value %d below its minimum %d", k.Name(), v, minimum)
	}
	create, pipe := config.TmuxCreateTimeoutMs.DefaultValue(), config.TmuxPipeCloseWaitMs.DefaultValue()
	switch k {
	case config.TmuxCreateTimeoutMs:
		create = v
	case config.TmuxPipeCloseWaitMs:
		pipe = v
	}
	if grace := config.TmuxPendingGraceSeconds.DefaultValue(); grace < config.PendingGraceMinimumSeconds(create, pipe) {
		t.Fatalf("precondition: %s value %d raises the grace minimum above the default grace %d", k.Name(), v, grace)
	}
	return v
}

// TestWriteTmuxConfig_PerSetting pins, for each tmux setting, that a
// written value, an explicit 0 and a negative value reach config.Load.
func TestWriteTmuxConfig_PerSetting(t *testing.T) {
	t.Parallel()
	for _, k := range config.TmuxKeys() {
		t.Run(k.Name(), func(t *testing.T) {
			t.Parallel()

			t.Run("round_trip", func(t *testing.T) {
				t.Parallel()
				v := roundTripValue(t, k)
				cfg, err := writeAndLoad(t, TmuxInt(k, v))
				if err != nil {
					t.Fatalf("config.Load: %v", err)
				}
				assertTmux(t, cfg.Tmux, map[config.TmuxKey]int64{k: v})
			})

			t.Run("zero", func(t *testing.T) {
				t.Parallel()
				cfg, err := writeAndLoad(t, TmuxInt(k, 0))
				if err != nil {
					t.Fatalf("config.Load: %v", err)
				}
				if got := cfg.Tmux.Value(k); got != 0 {
					t.Errorf("Value(%s) = %d; want 0 as written", k.Name(), got)
				}
				if got, want := cfg.Tmux.Effective(k), unitDuration(k, k.DefaultValue()); got != want {
					t.Errorf("Effective(%s) = %v; want default %v", k.Name(), got, want)
				}
			})

			t.Run("negative", func(t *testing.T) {
				t.Parallel()
				_, err := writeAndLoad(t, TmuxInt(k, -1))
				if msg := requireConfigError(t, err); !strings.Contains(msg, k.Name()) {
					t.Errorf("message does not name %s: %s", k.Name(), msg)
				}
			})
		})
	}
}

// TestWriteTmuxConfig_NoSettings pins that an empty tmux table loads with every default.
func TestWriteTmuxConfig_NoSettings(t *testing.T) {
	t.Parallel()
	cfg, err := writeAndLoad(t)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	assertTmux(t, cfg.Tmux, nil)
}

// TestWriteTmuxConfig_RawValues pins that a non-integer TOML value fails config.Load.
func TestWriteTmuxConfig_RawValues(t *testing.T) {
	t.Parallel()
	keys := config.TmuxKeys()
	first, mid, last := keys[0], keys[len(keys)/2], keys[len(keys)-1]
	cases := []struct {
		name    string
		setting TmuxSetting
	}{
		{"float", TmuxFloat(first, float64(first.DefaultValue()))},
		{"string", TmuxString(mid, strconv.FormatInt(mid.DefaultValue(), 10))},
		{"bool", TmuxBool(last, true)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := writeAndLoad(t, tc.setting)
			requireConfigError(t, err)
		})
	}
}
