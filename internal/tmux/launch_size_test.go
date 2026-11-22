package tmux

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unsafe"
)

// b.xhh: count bytes including the terminator, not runes or just env values.
func TestLaunchSizeEntryBoundaries(t *testing.T) {
	const marker = "PRIVATE-LAUNCH-SIZE-MARKER"
	for _, vector := range []string{"argument", "environment entry"} {
		prefix := ""
		if vector == "environment entry" {
			prefix = "K="
		}
		for _, tc := range []struct {
			name         string
			entry        string
			refused, nul bool
		}{
			{"last accepted byte", prefix + marker + strings.Repeat("x", 131071-len(prefix)-len(marker)), false, false},
			{"first refused byte", prefix + marker + strings.Repeat("x", 131072-len(prefix)-len(marker)), true, false},
			{"UTF8 accepted", prefix + strings.Repeat("雪", 43689) + strings.Repeat("x", 4-len(prefix)), false, false},
			{"UTF8 refused", prefix + strings.Repeat("雪", 43691), true, false},
			{"embedded NUL", prefix + marker + "\x00tail", true, true},
		} {
			t.Run(vector+"/"+tc.name, func(t *testing.T) {
				entry := tc.entry
				var command, environ []string
				if vector == "argument" {
					command = []string{"agent", entry}
				} else {
					environ = []string{"OK=yes", entry}
				}
				err := checkExecSize(command, environ, 1<<30)
				if !tc.refused {
					if err != nil {
						t.Fatal(err)
					}
					return
				}
				want := LaunchSizeError{Component: vector, Index: 2, Size: uint64(len(entry) + 1), Limit: 131072, NUL: tc.nul}
				got := requireLaunchSizeError(t, err, want)
				var description string
				if tc.nul {
					description = fmt.Sprintf("launch command contains an unrepresentable NUL byte: %s 2 is %d bytes including its terminator; limit is 131072 bytes and embedded NUL bytes are not allowed", vector, len(entry)+1)
				} else {
					description = fmt.Sprintf("launch command too large: %s 2 is %d bytes including NUL; limit is 131072 bytes", vector, len(entry)+1)
				}
				if got.Error() != description {
					t.Errorf("description = %q, want %q", got.Error(), description)
				}
				if strings.Contains(got.Error(), marker) || strings.Contains(got.Error(), "PRIVATE") || strings.Contains(got.Error(), "雪") {
					t.Error("description leaked input value")
				}
				if tc.nul && strings.Contains(got.Error(), "too large") {
					t.Error("NUL refusal described as size overflow")
				}
			})
		}
	}
}

// b.xhh: the reserve, each terminator and pointer, and the separate exec-path
// copy all participate in the strict aggregate boundary.
func TestLaunchSizeAggregateBoundary(t *testing.T) {
	pointer := uint64(unsafe.Sizeof(uintptr(0)))
	for _, tc := range []struct {
		name             string
		command, environ []string
		total            uint64
	}{
		{"reserve only", nil, nil, 32768},
		{"env only", nil, []string{"A=b"}, 32768 + 4 + pointer},
		{"argv env and exec path", []string{"/bin/echo", "", "雪"}, []string{"A=b", "EMPTY="}, 32768 + 36 + 5*pointer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, spare := range []uint64{0, 1} {
				err := checkExecSize(tc.command, tc.environ, tc.total+spare)
				if spare == 1 {
					if err != nil {
						t.Fatalf("one byte under budget: %v", err)
					}
				} else {
					got := requireLaunchSizeError(t, err, LaunchSizeError{Component: "calculated total including reserved overhead", Size: tc.total, Limit: tc.total})
					want := fmt.Sprintf("launch command too large: calculated total including reserved overhead is %d bytes; limit is %d bytes", tc.total, tc.total)
					if got.Error() != want {
						t.Errorf("description = %q, want %q", got.Error(), want)
					}
				}
			}
		})
	}
}

// b.xhh: use the arithmetic seam for unallocatable vector sizes; no forged
// oversized string/slice headers and no process-wide limit changes.
func TestLaunchSizeSaturatingAddition(t *testing.T) {
	for _, tc := range []struct{ a, b, want uint64 }{
		{3, 4, 7},
		{math.MaxUint64 - 1, 1, math.MaxUint64},
		{math.MaxUint64 - 1, 2, math.MaxUint64},
		{math.MaxUint64, 1, math.MaxUint64},
		{1, math.MaxUint64, math.MaxUint64},
	} {
		if got := addLaunchBytes(tc.a, tc.b); got != tc.want {
			t.Errorf("addLaunchBytes(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// b.xhh: merged entries replace stale copies rather than charging both;
// the instance id is always owned by the new launch.
func TestLaunchSizeEnvironmentMerge(t *testing.T) {
	pointer := uint64(unsafe.Sizeof(uintptr(0)))
	for _, tc := range []struct {
		name                     string
		envs                     map[string]string
		ambient                  []string
		extraBytes, extraEntries uint64
	}{
		{"explicit replaces ambient", map[string]string{"K": "n"}, []string{"K=" + strings.Repeat("x", 131072)}, 4, 1},
		{"owned replaces ambient id", nil, []string{instanceIDEnv + "=" + strings.Repeat("x", 131072)}, 0, 0},
		{"owned replaces explicit id", map[string]string{instanceIDEnv: strings.Repeat("x", 131072)}, nil, 0, 0},
		{"ambient retained", map[string]string{"K": "n"}, []string{"OTHER=value"}, 16, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// agent\0 + exec-path copy + AGENT_DIRECTOR_INSTANCE_ID=id\0.
			total := uint64(32768+42) + tc.extraBytes + (2+tc.extraEntries)*pointer
			for _, spare := range []uint64{0, 1} {
				err := checkLaunchSize([]string{"agent"}, tc.envs, tc.ambient, "id", nil, total+spare)
				if spare == 1 {
					if err != nil {
						t.Fatal(err)
					}
				} else {
					requireLaunchSizeError(t, err, LaunchSizeError{Component: "calculated total including reserved overhead", Size: total, Limit: total})
				}
			}
		})
	}
}

// b.xhh: preflight checks the client env too, except excluded/forwarded keys.
// Forwarded values are removed from that client but still charged to the pane.
func TestLaunchSizePreparedEnvironment(t *testing.T) {
	const key = "XHH_SIZE_TEST_PAYLOAD"
	const marker = "PRIVATE-FORWARDED-SIZE-MARKER"
	t.Setenv(key, marker+strings.Repeat("x", 131072-len(marker)))
	for _, tc := range []struct {
		name             string
		envs             map[string]string
		exclude, forward []string
		refused          bool
	}{
		{"ambient", nil, nil, nil, true},
		{"override still reaches client", map[string]string{key: "small"}, nil, nil, true},
		{"excluded ambient", nil, []string{key}, nil, false},
		{"excluded with explicit replacement", map[string]string{key: "small"}, []string{key}, nil, false},
		{"forwarded reaches pane only", map[string]string{key: "small"}, nil, []string{key}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prepared, client, err := prepareCreateEnvironment(tc.envs, tc.exclude, tc.forward)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, entry := range client {
				if strings.HasPrefix(entry, key+"=") {
					found = true
				}
			}
			if want := len(tc.exclude) == 0 && len(tc.forward) == 0; found != want {
				t.Errorf("key present in client = %v, want %v", found, want)
			}
			if len(tc.forward) != 0 {
				if len(prepared[key]) != 131072 {
					t.Error("forwarded host value did not replace explicit value")
				}
				if tc.envs[key] != "small" {
					t.Error("forwarding mutated caller env map")
				}
			} else if tc.envs != nil && prepared[key] != "small" {
				t.Error("explicit excluded value was lost")
			}
			err = CheckLaunchSize([]string{"agent"}, tc.envs, "id", tc.exclude, nil, tc.forward, 1<<30)
			if !tc.refused {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var size *LaunchSizeError
			if !errors.As(err, &size) {
				t.Fatalf("want size refusal, got %v", err)
			}
			if size.Component != "environment entry" || size.Size != uint64(len(key)+1+131072+1) || size.Limit != 131072 || size.NUL {
				t.Errorf("wrong environment size refusal: %+v", size)
			}
			if strings.Contains(err.Error(), marker) {
				t.Error("description leaked forwarded value")
			}
		})
	}
}

// b.xhh: exclusions cost the actual shell wrapper, with explicit values and
// the owned id protected. Keys stay positional, including shell metacharacters.
func TestLaunchSizeExclusionWrapper(t *testing.T) {
	command := []string{"agent", "payload"}
	for _, tc := range []struct {
		name  string
		envs  map[string]string
		unset []string
		want  []string
	}{
		{"none", nil, nil, command},
		{"explicit and owned win", map[string]string{"KEEP": "value"}, []string{"KEEP", instanceIDEnv}, command},
		{"remaining key wrapped", map[string]string{"KEEP": "value"}, []string{"KEEP", instanceIDEnv, "DROP;$(false)"}, []string{"/bin/sh", "-c", `unset "$1" || exit; shift; exec "$@"`, "sh", "DROP;$(false)", "agent", "payload"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := slices.Clone(tc.unset)
			if got := createPaneCommand(command, tc.envs, tc.unset); !slices.Equal(got, tc.want) {
				t.Errorf("wrapper = %q, want %q", got, tc.want)
			}
			if !slices.Equal(tc.unset, before) {
				t.Error("wrapper mutated caller exclusion list")
			}
		})
	}
	// A server-global key is excluded from the pane env, but its positional
	// argument in the first shell exec must still respect the per-entry cap.
	requireLaunchSizeError(t, checkLaunchSize(command, nil, nil, "id", []string{strings.Repeat("x", 131072)}, 1<<30), LaunchSizeError{Component: "argument", Index: 5, Size: 131073, Limit: 131072})
}

// b.xhh: threshold counts packed bytes, including empty args, escaped final
// semicolons, labels and command separators, rather than just the payload.
func TestLaunchSizePackedThreshold(t *testing.T) {
	for _, tc := range []struct {
		name           string
		commands       [][]string
		packedOverhead int
	}{
		{"single", [][]string{{""}}, 1},
		{"labels separators and escapes", [][]string{{"new-session", "雪;", ""}, {"set-option", "@ad_owner", "label;"}}, 50},
	} {
		for _, size := range []int{8191, 8192, 8193} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, size), func(t *testing.T) {
				commands := make([][]string, len(tc.commands))
				for i := range commands {
					commands[i] = slices.Clone(tc.commands[i])
				}
				commands[0][len(commands[0])-1] = strings.Repeat("x", size-tc.packedOverhead)
				if got := largeCreate(commands); got != (size > 8192) {
					t.Errorf("packed size %d: stdin = %v, want %v", size, got, size > 8192)
				}
			})
		}
	}
}

func requireLaunchSizeError(t *testing.T, err error, want LaunchSizeError) *LaunchSizeError {
	t.Helper()
	var got *LaunchSizeError
	if !errors.As(err, &got) {
		t.Fatalf("want LaunchSizeError, got %v", err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("size refusal = %+v, want %+v", got, want)
	}
	return got
}
