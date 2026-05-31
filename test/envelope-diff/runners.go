// runners.go provides the two execution paths the envelope-diff harness uses to
// produce JSON byte envelopes from a verb invocation:
//
//   - runCLI: forks the compiled CLI binary as a subprocess and captures the
//     resulting envelope bytes.
//   - runClient: invokes the corresponding pkg/api.Client method in-process and
//     marshals the result or error into the same envelope shape.
//
// Envelope contract (E1 pin):
//
//   - Success: stdout bytes when exit==0 (CLI) / json.Marshal(result) (Client).
//     Both are the raw pkg/api result struct with no {ok,…} wrapper.
//   - Error: stderr bytes when exit!=0 (CLI) / json.Marshal({err_name,
//     err_description}) (Client).
//
// Both runners ensure that the fake-tmux binary (see harness.go) is the first
// "tmux" resolved from PATH so that tmux-dependent verbs produce reproducible
// output on both sides.
package envelope_diff

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	api "github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// ── error envelope ────────────────────────────────────────────────────────────

// apiErrEnvelope is the JSON shape emitted on stderr by the CLI on failure and
// produced by runClient on error. It mirrors cmd/agent-director's errorEnvelope.
type apiErrEnvelope struct {
	ErrName        string `json:"err_name"`
	ErrDescription string `json:"err_description"`
}

// marshalErrEnvelope classifies err via the errnames catalog and returns
// {"err_name":…,"err_description":…} bytes.
func marshalErrEnvelope(err error) []byte {
	name, desc := errnames.Classify(err)
	desc = errnames.TrimNamePrefix(name, desc)
	b, _ := json.Marshal(apiErrEnvelope{ErrName: name, ErrDescription: desc})
	return b
}

// ── dispatch table completeness guard ─────────────────────────────────────────

// clientDispatchFn is the signature for every per-verb dispatch entry.
// It receives an already-open Client and the raw params map, and returns
// the JSON envelope bytes plus a bool indicating whether an error occurred.
type clientDispatchFn func(c *api.Client, params map[string]any) ([]byte, bool)

// dispatch maps each callable verb name to its Client-side dispatch function.
// The init() guard below ensures this table is complete with respect to
// manifest.CallableVerbs() at startup; missing entries cause a panic.
var dispatch = map[string]clientDispatchFn{
	"spawn":          dispatchSpawn,
	"status":         dispatchStatus,
	"get":            dispatchGet,
	"send-keys":      dispatchSendKeys,
	"read-pane":      dispatchReadPane,
	"kill":           dispatchKill,
	"decide":         dispatchDecide,
	"get-permission": dispatchGetPermission,
	"resume":         dispatchResume,
	"find-missing":   dispatchFindMissing,
	"expire":         dispatchExpire,
	"delete":         dispatchDelete,
	"make-template":  dispatchMakeTemplate,
	"list":           dispatchList,
	"pause":          dispatchPause,
	"version":        dispatchVersion,
}

func init() {
	// Build-time assertion: every callable verb must have a dispatch entry.
	// Non-callable verbs (serve, hook, help) are intentionally absent.
	for _, v := range manifest.CallableVerbs() {
		if _, ok := dispatch[v.Name]; !ok {
			panic(fmt.Sprintf("envelope_diff: dispatch table is missing callable verb %q", v.Name))
		}
	}
}

// ── PATH mutation for in-process fake-tmux ────────────────────────────────────

var (
	pathMutateOnce sync.Once
	pathMutateErr  error
)

// ensureFakeTmuxOnPath prepends the fake-tmux binary directory to the current
// process's PATH exactly once per test run. All subsequent calls are no-ops.
// This ensures that in-process api.Client calls (which resolve "tmux" from PATH
// via exec.LookPath) hit the fake instead of the system tmux.
func ensureFakeTmuxOnPath(t *testing.T) {
	t.Helper()
	dir := buildFakeTmux(t) // uses its own sync.Once; idempotent
	pathMutateOnce.Do(func() {
		pathMutateErr = os.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	})
	if pathMutateErr != nil {
		t.Fatalf("ensureFakeTmuxOnPath: %v", pathMutateErr)
	}
}

// ── CLI subprocess runner ─────────────────────────────────────────────────────

// runCLI executes the CLI binary at binPath as a subprocess against the
// fixture store at dbPath. args is the full verb + argument list (e.g.
// []string{"status", "<id>"}).
//
// Envelope reduction:
//
//   - exit 0 → stdout bytes (success envelope).
//   - exit != 0 → stderr bytes (error envelope).
//
// The subprocess environment is kept minimal: HOME is set to the parent of
// .agent-director/ (derived from dbPath) so the CLI resolves its store at
// dbPath without reading the test runner's real config. PATH is prepended with
// the fake-tmux directory so tmux-dependent verbs hit the fake binary.
//
// runCLI does NOT call t.Fatal on a non-zero exit code; callers decide how to
// interpret the returned envelope and exit code.
func runCLI(t *testing.T, binPath, dbPath string, args ...string) (envelope []byte, exitCode int) {
	t.Helper()

	fakeTmuxDir := buildFakeTmux(t)

	// dbPath = <homeDir>/.agent-director/state.db
	// homeDir is the fake HOME so ~/.agent-director/state.db resolves to dbPath.
	homeDir := filepath.Dir(filepath.Dir(dbPath))

	cmd := exec.Command(binPath, args...)
	cmd.Env = []string{
		"HOME=" + homeDir,
		"PATH=" + fakeTmuxDir + ":" + os.Getenv("PATH"),
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return stderr.Bytes(), ee.ExitCode()
		}
		t.Fatalf("runCLI: unexpected exec error: %v", err)
	}
	return stdout.Bytes(), 0
}

// ── in-process Client runner ──────────────────────────────────────────────────

// runClient opens a pkg/api.Client bound to dbPath, dispatches the named verb
// with the given params, and returns the JSON envelope bytes.
//
//   - On success: json.Marshal(result) → (bytes, false). No {ok,…} wrapper.
//   - On error: json.Marshal({err_name,err_description}) → (bytes, true).
//
// params must contain the verb's parameters encoded as a map[string]any using
// snake_case JSON-style keys (e.g. {"claude_instance_id":"…"}).
//
// The fake-tmux binary is ensured on PATH (once per process) before the Client
// is constructed so that any tmux.Client internal to the verb calls resolves to
// the fake binary.
func runClient(t *testing.T, dbPath, verb string, params map[string]any) (envelope []byte, errReturned bool) {
	t.Helper()

	ensureFakeTmuxOnPath(t)

	// dbPath = <homeDir>/.agent-director/state.db; derive configPath to ensure
	// the Client uses the same (absent) config as the CLI subprocess — both
	// fall through to config.Default() when the file is missing.
	homeDir := filepath.Dir(filepath.Dir(dbPath))
	configPath := filepath.Join(homeDir, ".agent-director", "config.toml")

	c, err := api.New(api.Options{
		StorePath:       dbPath,
		ConfigPath:      configPath,
		CreateIfMissing: true,
	})
	if err != nil {
		b := marshalErrEnvelope(err)
		return b, true
	}
	defer c.Close()

	fn, ok := dispatch[verb]
	if !ok {
		// This should never happen if init() passed, but be safe.
		t.Fatalf("runClient: no dispatch entry for verb %q", verb)
	}
	return fn(c, params)
}

// ── helpers ───────────────────────────────────────────────────────────────────

// remarshal converts src (typically map[string]any) to dst via a JSON
// round-trip. Used for params structs that carry json struct tags.
func remarshal(src, dst any) error {
	b, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}

// successEnvelope marshals a success result struct to JSON bytes.
func successEnvelope(result any) ([]byte, bool) {
	b, err := json.Marshal(result)
	if err != nil {
		return marshalErrEnvelope(fmt.Errorf("ErrJSONMarshal: %w", err)), true
	}
	return b, false
}

// strParam extracts a string value from params by key, returning "" if absent.
func strParam(params map[string]any, key string) string {
	if v, ok := params[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// strSliceParam extracts a []string from params by key.
func strSliceParam(params map[string]any, key string) []string {
	v, ok := params[key]
	if !ok {
		return nil
	}
	switch val := v.(type) {
	case []string:
		return val
	case []any:
		out := make([]string, 0, len(val))
		for _, item := range val {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// intParam extracts an int from params by key, returning 0 if absent.
func intParam(params map[string]any, key string) int {
	v, ok := params[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// boolParam extracts a bool from params by key, returning false if absent.
func boolParam(params map[string]any, key string) bool {
	if v, ok := params[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

// strMapParam extracts a map[string]string from params by key.
func strMapParam(params map[string]any, key string) map[string]string {
	v, ok := params[key]
	if !ok {
		return nil
	}
	switch m := v.(type) {
	case map[string]string:
		return m
	case map[string]any:
		out := make(map[string]string, len(m))
		for k, val := range m {
			if s, ok := val.(string); ok {
				out[k] = s
			}
		}
		return out
	}
	return nil
}

// ── per-verb dispatch functions ───────────────────────────────────────────────

func dispatchSpawn(c *api.Client, params map[string]any) ([]byte, bool) {
	p := api.SpawnParams{
		CWD:              strParam(params, "cwd"),
		Template:         strParam(params, "template"),
		ClaudeInstanceID: strParam(params, "claude_instance_id"),
		RelayMode:        strParam(params, "relay_mode"),
		ClaudeArgs:       strSliceParam(params, "claude_args"),
		ExtraEnv:         strMapParam(params, "extra_env"),
		AgentDirectorLabels: strMapParam(params, "labels"),
		NoPreTrust:       boolParam(params, "no_pre_trust"),
	}
	if tmuxName, ok := params["tmux_session_name"].(string); ok {
		p.TmuxSessionName = tmuxName
		p.TmuxSessionNameSupplied = true
	}
	res, err := c.Spawn(p)
	if err != nil {
		return marshalErrEnvelope(err), true
	}
	return successEnvelope(res)
}

func dispatchStatus(c *api.Client, params map[string]any) ([]byte, bool) {
	id := strParam(params, "claude_instance_id")
	res, err := c.Status(id)
	if err != nil {
		return marshalErrEnvelope(err), true
	}
	return successEnvelope(res)
}

func dispatchGet(c *api.Client, params map[string]any) ([]byte, bool) {
	id := strParam(params, "claude_instance_id")
	res, err := c.Get(id)
	if err != nil {
		return marshalErrEnvelope(err), true
	}
	return successEnvelope(res)
}

func dispatchSendKeys(c *api.Client, params map[string]any) ([]byte, bool) {
	var p api.SendKeysParams
	if err := remarshal(params, &p); err != nil {
		return marshalErrEnvelope(err), true
	}
	res, err := c.SendKeys(p)
	if err != nil {
		return marshalErrEnvelope(err), true
	}
	return successEnvelope(res)
}

func dispatchReadPane(c *api.Client, params map[string]any) ([]byte, bool) {
	var p api.ReadPaneParams
	if err := remarshal(params, &p); err != nil {
		return marshalErrEnvelope(err), true
	}
	res, err := c.ReadPane(p)
	if err != nil {
		return marshalErrEnvelope(err), true
	}
	return successEnvelope(res)
}

func dispatchKill(c *api.Client, params map[string]any) ([]byte, bool) {
	var p api.KillParams
	if err := remarshal(params, &p); err != nil {
		return marshalErrEnvelope(err), true
	}
	res, err := c.Kill(p)
	if err != nil {
		return marshalErrEnvelope(err), true
	}
	return successEnvelope(res)
}

func dispatchDecide(c *api.Client, params map[string]any) ([]byte, bool) {
	var p api.DecideParams
	if err := remarshal(params, &p); err != nil {
		return marshalErrEnvelope(err), true
	}
	res, err := c.Decide(p)
	if err != nil {
		return marshalErrEnvelope(err), true
	}
	return successEnvelope(res)
}

func dispatchGetPermission(c *api.Client, params map[string]any) ([]byte, bool) {
	var p api.GetPermissionParams
	if err := remarshal(params, &p); err != nil {
		return marshalErrEnvelope(err), true
	}
	res, err := c.GetPermission(p)
	if err != nil {
		return marshalErrEnvelope(err), true
	}
	return successEnvelope(res)
}

func dispatchResume(c *api.Client, params map[string]any) ([]byte, bool) {
	var p api.ResumeParams
	if err := remarshal(params, &p); err != nil {
		return marshalErrEnvelope(err), true
	}
	res, err := c.Resume(p)
	if err != nil {
		return marshalErrEnvelope(err), true
	}
	return successEnvelope(res)
}

func dispatchFindMissing(c *api.Client, _ map[string]any) ([]byte, bool) {
	res, err := c.FindMissing(context.Background())
	if err != nil {
		return marshalErrEnvelope(err), true
	}
	return successEnvelope(res)
}

func dispatchExpire(c *api.Client, params map[string]any) ([]byte, bool) {
	// "older_than" is an optional duration string like "7d", "2h", "0d".
	// If absent or empty, pass nil so the Client uses the config default.
	var olderThan *time.Duration
	if raw := strParam(params, "older_than"); raw != "" {
		d, err := parseDuration(raw)
		if err != nil {
			return marshalErrEnvelope(fmt.Errorf("expire: parse older_than %q: %w", raw, err)), true
		}
		olderThan = &d
	}
	res, err := c.Expire(olderThan)
	if err != nil {
		return marshalErrEnvelope(err), true
	}
	return successEnvelope(res)
}

// parseDuration parses a duration string as accepted by the expire verb CLI.
// Supports standard Go durations (e.g. "2h") plus day suffixes ("7d").
func parseDuration(s string) (time.Duration, error) {
	if len(s) > 0 && s[len(s)-1] == 'd' {
		n := 0
		if _, err := fmt.Sscanf(s[:len(s)-1], "%d", &n); err != nil {
			return 0, fmt.Errorf("invalid day count: %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

func dispatchDelete(c *api.Client, params map[string]any) ([]byte, bool) {
	ids := strSliceParam(params, "claude_instance_id")
	if ids == nil {
		ids = strSliceParam(params, "ids")
	}
	res, err := c.Delete(ids)
	if err != nil {
		return marshalErrEnvelope(err), true
	}
	return successEnvelope(res)
}

func dispatchMakeTemplate(c *api.Client, params map[string]any) ([]byte, bool) {
	p := api.MakeTemplateParams{
		Name:                strParam(params, "name"),
		CWD:                 strParam(params, "cwd"),
		RelayMode:           strParam(params, "relay_mode"),
		ClaudeArgs:          strSliceParam(params, "claude_args"),
		ExtraEnv:            strMapParam(params, "extra_env"),
		AgentDirectorLabels: strMapParam(params, "labels"),
		Overwrite:           boolParam(params, "overwrite"),
	}
	res, err := c.MakeTemplate(p)
	if err != nil {
		return marshalErrEnvelope(err), true
	}
	return successEnvelope(res)
}

func dispatchList(c *api.Client, params map[string]any) ([]byte, bool) {
	p := api.ListParams{
		State:           strSliceParam(params, "state"),
		Labels:          strSliceParam(params, "labels"),
		Parent:          strParam(params, "parent"),
		Cwd:             strParam(params, "cwd"),
		TmuxSessionName: strParam(params, "tmux_session_name"),
		Limit:           intParam(params, "limit"),
	}
	res, err := c.List(p)
	if err != nil {
		return marshalErrEnvelope(err), true
	}
	return successEnvelope(res)
}

func dispatchPause(c *api.Client, params map[string]any) ([]byte, bool) {
	var p api.PauseParams
	if err := remarshal(params, &p); err != nil {
		return marshalErrEnvelope(err), true
	}
	res, err := c.Pause(context.Background(), p)
	if err != nil {
		return marshalErrEnvelope(err), true
	}
	return successEnvelope(res)
}

func dispatchVersion(c *api.Client, _ map[string]any) ([]byte, bool) {
	res, err := c.Version()
	if err != nil {
		return marshalErrEnvelope(err), true
	}
	return successEnvelope(res)
}
