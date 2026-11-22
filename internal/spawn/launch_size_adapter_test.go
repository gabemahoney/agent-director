package spawn

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// b.xhh: nil UnsetEnv inherits ExcludeEnv; a deliberately empty list suppresses
// that fallback. An oversized excluded key reveals whether the wrapper ran.
func TestLaunchSizeAdapterUnsetFallback(t *testing.T) {
	const marker = "PRIVATE-ADAPTER-SIZE-MARKER"
	key := marker + strings.Repeat("x", 131072-len(marker))
	for _, tc := range []struct {
		name    string
		unset   []string
		refused bool
	}{
		{"nil falls back", nil, true},
		{"empty stays empty", []string{}, false},
		{"explicit list wins", []string{"OTHER"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckCreateSize(CreateRequest{Command: []string{"agent"}, InstanceID: "id", ExcludeEnv: []string{key}, UnsetEnv: tc.unset})
			if !tc.refused {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			wrapped := fmt.Errorf("outer: %w", err)
			var early *LaunchPreflightError
			var size *tmux.LaunchSizeError
			if !errors.As(wrapped, &early) || !errors.As(wrapped, &size) {
				t.Fatalf("lost preflight/size type through wrapping: %v", wrapped)
			}
			if size.Component != "argument" || size.Index != 5 || size.Size != 131073 || size.Limit != 131072 || size.NUL {
				t.Errorf("unexpected wrapper refusal: %+v", size)
			}
			if !errors.Is(wrapped, size) || !errors.Is(wrapped, early) {
				t.Error("wrapped marker lost its cause identity")
			}
			want := "launch command too large: argument 5 is 131073 bytes including NUL; limit is 131072 bytes; nothing was launched and no row was inserted or changed"
			if err.Error() != want {
				t.Errorf("description = %q, want %q", err.Error(), want)
			}
			if strings.Contains(wrapped.Error(), marker) {
				t.Error("description leaked excluded key")
			}
		})
	}
}

// b.xhh: the early marker unwraps arbitrary causes as well as typed size
// refusals, so callers can classify an error after adding their own context.
func TestLaunchSizeAdapterMarkerUnwrap(t *testing.T) {
	cause := errors.New("budget unavailable")
	early := &LaunchPreflightError{cause: cause}
	wrapped := fmt.Errorf("launch: %w", early)
	var got *LaunchPreflightError
	if !errors.Is(wrapped, cause) || !errors.As(wrapped, &got) || got != early {
		t.Fatal("wrapped early refusal lost its marker or cause")
	}
}
