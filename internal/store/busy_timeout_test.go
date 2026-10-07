package store

import (
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
)

// TestOpenersSetBusyTimeout (b.c7f): each opener's connection has the busy
// timeout it was given, Open and OpenOrInit the [store] busy_timeout_ms
// default, and one SQLite would take as turning the wait off (0, a negative
// value, or one above the largest it holds) the default instead.
func TestOpenersSetBusyTimeout(t *testing.T) {
	if DefaultBusyTimeoutMs != config.DefaultStoreBusyTimeoutMs {
		t.Fatalf("DefaultBusyTimeoutMs = %d; want config.DefaultStoreBusyTimeoutMs, %d",
			DefaultBusyTimeoutMs, config.DefaultStoreBusyTimeoutMs)
	}
	aboveLargest := int(int64(config.MaxStoreBusyTimeoutMs) + 1)
	withOpen := func(ms int) func(string) (*Store, error) {
		return func(p string) (*Store, error) { return OpenWithBusyTimeout(p, ms) }
	}
	withOpenOrInit := func(ms int) func(string) (*Store, error) {
		return func(p string) (*Store, error) { return OpenOrInitWithBusyTimeout(p, ms) }
	}
	cases := []struct {
		name string
		open func(path string) (*Store, error)
		want int64
	}{
		{"OpenOrInit", OpenOrInit, DefaultBusyTimeoutMs},
		{"Open", Open, DefaultBusyTimeoutMs},
		{"OpenOrInitWithBusyTimeout", withOpenOrInit(1234), 1234},
		{"OpenWithBusyTimeout", withOpen(1), 1},
		{"largest", withOpen(config.MaxStoreBusyTimeoutMs), config.MaxStoreBusyTimeoutMs},
		{"OpenWithBusyTimeout above the largest", withOpen(aboveLargest), DefaultBusyTimeoutMs},
		{"OpenOrInitWithBusyTimeout above the largest", withOpenOrInit(aboveLargest), DefaultBusyTimeoutMs},
		{"OpenWithBusyTimeout 0", withOpen(0), DefaultBusyTimeoutMs},
		{"OpenOrInitWithBusyTimeout 0", withOpenOrInit(0), DefaultBusyTimeoutMs},
		{"OpenWithBusyTimeout negative", withOpen(-1), DefaultBusyTimeoutMs},
		{"OpenOrInitWithBusyTimeout negative", withOpenOrInit(-1), DefaultBusyTimeoutMs},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")
			seed, err := OpenOrInit(path)
			if err != nil {
				t.Fatalf("OpenOrInit: %v", err)
			}
			_ = seed.Close()
			s, err := tc.open(path)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer s.Close() //nolint:errcheck
			var got int64
			if err := s.db.QueryRow("PRAGMA busy_timeout").Scan(&got); err != nil {
				t.Fatalf("read busy_timeout: %v", err)
			}
			if got != tc.want {
				t.Errorf("PRAGMA busy_timeout = %d; want %d", got, tc.want)
			}
		})
	}
}
