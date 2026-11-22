package tmux

import (
	"fmt"
	"maps"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/gabemahoney/agent-director/internal/cursorenv"
)

const (
	// The launch contract uses Linux's 4 KiB-page MAX_ARG_STRLEN, including NUL.
	launchEntryLimit = 131072
	// Covers tmux-generated environment and launch overhead not yet composed.
	// This is a conservative allowance, not an OS-defined part of ARG_MAX.
	launchSizeReserve = 32 * 1024
	// Half the 16 KiB command-message ceiling leaves ample protocol headroom
	// on both tmux 3.2a and 3.3a, including the header their client check omits.
	createArgvLimit = 8192
)

// LaunchSizeError describes a refused exec vector without retaining its values.
// Index is one-based for an argument or environment entry, zero for the total.
type LaunchSizeError struct {
	Component   string
	Index       int
	Size, Limit uint64
	NUL         bool
}

func (e *LaunchSizeError) Error() string {
	component := e.Component
	units := "bytes"
	if e.Index > 0 {
		component += fmt.Sprintf(" %d", e.Index)
		units = "bytes including NUL"
	}
	if e.NUL {
		return fmt.Sprintf("launch command contains an unrepresentable NUL byte: %s is %d bytes including its terminator; limit is %d bytes and embedded NUL bytes are not allowed", component, e.Size, e.Limit)
	}
	return fmt.Sprintf("launch command too large: %s is %d %s; limit is %d bytes", component, e.Size, units, e.Limit)
}

// CheckLaunchSize checks a launch using the default tmux binary and empty session
// metadata. Production callers use Client.CheckNewSessionSize with the complete
// create metadata and the actual client binary. A positive budget is injectable;
// zero reads the runtime aggregate exec budget.
func CheckLaunchSize(command []string, envs map[string]string, instanceID string, exclude, unset, forward []string, budget uint64) error {
	return new(Client).CheckNewSessionSize("", "", "", envs, command, "", instanceID, "", exclude, unset, forward, budget)
}

// CheckNewSessionSize measures both execs before a launch-row mutation: the
// selected tmux client invocation with its unmodified filtered environment, and
// the pane vector after explicit overrides. The binary, socket, labels and
// escaping are the same as the final create; only native preparation may still
// change the command/env. Server globals and stack limits can differ from AD's
// known environment/runtime budget, so those remain a conservative estimate.
// Forwarded values stay within this package and never enter a refusal.
func (c *Client) CheckNewSessionSize(socket, name, cwd string, envs map[string]string, command []string, token, instanceID, storeID string, exclude, unset, forward []string, budget uint64) error {
	envs, environ, err := prepareCreateEnvironment(envs, exclude, forward)
	if err != nil {
		return err
	}
	commands := createCommandsWithExclusions(name, cwd, envs, command, token, instanceID, storeID, !NeedsLabelByID(name), unset)
	clientCommand, stdin := c.createClientCommand(socket, commands, len(forward) > 0)
	if err := checkExecSize(clientCommand, environ, budget); err != nil {
		return err
	}
	if err := checkLaunchSize(command, envs, environ, instanceID, unset, budget); err != nil {
		return err
	}
	if stdin {
		// The client vector no longer contains metadata on the stdin route.
		// Check its representability now too, before files/trust/row changes.
		_, err = createConfig(commands)
	}
	return err
}

func checkLaunchSize(command []string, envs map[string]string, environ []string, instanceID string, unset []string, budget uint64) error {
	// Explicit entries replace ambient ones. The create owns the instance id,
	// even when the caller's map contains an older copy.
	merged := make(map[string]string, len(environ)+len(envs)+1)
	for _, entry := range environ {
		key, value, _ := strings.Cut(entry, "=")
		merged[key] = value
	}
	maps.Copy(merged, envs)
	merged[instanceIDEnv] = instanceID
	return checkExecSize(createPaneCommand(command, envs, unset), sortedEnvFlags(merged), budget)
}

// checkExecSize charges byte strings, their NULs, argv/envp pointers and a
// conservative reserve. Saturating arithmetic keeps an overflow a refusal.
func checkExecSize(command, environ []string, budget uint64) error {
	total := uint64(launchSizeReserve)
	for _, vector := range []struct {
		component string
		entries   []string
	}{{"argument", command}, {"environment entry", environ}} {
		for i, entry := range vector.entries {
			size := addLaunchBytes(uint64(len(entry)), 1)
			if size > launchEntryLimit || strings.IndexByte(entry, 0) >= 0 {
				return &LaunchSizeError{Component: vector.component, Index: i + 1, Size: size, Limit: launchEntryLimit, NUL: strings.IndexByte(entry, 0) >= 0}
			}
			total = addLaunchBytes(total, addLaunchBytes(size, uint64(strconv.IntSize/8)))
		}
	}
	// exec also copies its executable path. For PATH lookup, the reserve covers
	// the prefix; the command's own argv[0] is charged separately above.
	if len(command) > 0 {
		total = addLaunchBytes(total, addLaunchBytes(uint64(len(command[0])), 1))
	}
	if budget == 0 {
		var err error
		budget, err = runtimeLaunchBudget()
		if err != nil {
			return fmt.Errorf("launch exec budget unavailable: %w", err)
		}
	}
	if total >= budget {
		return &LaunchSizeError{Component: "calculated total including reserved overhead", Size: total, Limit: budget}
	}
	return nil
}

func addLaunchBytes(a, b uint64) uint64 {
	if b > math.MaxUint64-a {
		return math.MaxUint64
	}
	return a + b
}

// largeCreate counts the exact packed command-list argv (including escapes,
// separators and NULs), without ever using it as configuration-parser input.
func largeCreate(commands [][]string) bool {
	size := 0
	for _, arg := range commandArgv(commands...) {
		if len(arg) >= createArgvLimit-size {
			return true
		}
		size += len(arg) + 1
	}
	return false
}

// createClientCommand shares route selection and the complete exec vector
// between early preflight and the final create check. It never invokes tmux.
func (c *Client) createClientCommand(socket string, commands [][]string, forwarded bool) ([]string, bool) {
	stdin := forwarded || largeCreate(commands)
	if stdin {
		commands = [][]string{{"start-server"}, {"source-file", "-"}}
	}
	command := append([]string{c.binaryPath(), "-u", "-S", socket}, commandArgv(commands...)...)
	return command, stdin
}

// prepareCreateEnvironment is shared by early preflight and the final create.
// Forwarded values never enter the client environment or a diagnostic.
func prepareCreateEnvironment(envs map[string]string, exclude, forward []string) (map[string]string, []string, error) {
	if len(forward) > 0 {
		envs = maps.Clone(envs)
		if envs == nil {
			envs = make(map[string]string)
		}
		for _, key := range forward {
			if key == "" || strings.ContainsAny(key, "=\x00") {
				return nil, nil, createRefusal("invalid forwarded environment name")
			}
			value := os.Getenv(key)
			if value == "" {
				return nil, nil, createRefusal(fmt.Sprintf("host environment %q is missing or empty", key))
			}
			envs[key] = value
		}
	}
	environ := clientEnv(os.Environ())
	if envs["AGENT_DIRECTOR_AGENT"] == "cursor" {
		environ = cursorenv.WithoutSelection(environ)
	}
	if len(exclude) > 0 || len(forward) > 0 {
		environ = slices.DeleteFunc(environ, func(kv string) bool {
			key, _, _ := strings.Cut(kv, "=")
			return slices.Contains(exclude, key) || slices.Contains(forward, key)
		})
	}
	return envs, environ, nil
}

// createPaneCommand includes the first exec's unset wrapper, before its shell
// removes session globals. Bare new-session -e KEY does not mask globals; the
// wrapper unsets them without changing the server and exec preserves the pane
// PID. Keys and command argv are positional arguments, never shell text.
// Explicit values and the owned instance id win.
func createPaneCommand(command []string, envs map[string]string, unset []string) []string {
	unset = slices.DeleteFunc(slices.Clone(unset), func(key string) bool {
		_, explicit := envs[key]
		return explicit || key == instanceIDEnv
	})
	if len(unset) == 0 {
		return command
	}
	script := strings.Repeat("unset \"$1\" || exit; shift; ", len(unset)) + "exec \"$@\""
	wrapped := []string{"/bin/sh", "-c", script, "sh"}
	wrapped = append(wrapped, unset...)
	return append(wrapped, command...)
}
