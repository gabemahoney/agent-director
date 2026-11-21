package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os/exec"
	"slices"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/installversion"
	"github.com/gabemahoney/agent-director/internal/layout"
	"github.com/gabemahoney/agent-director/internal/resumedelegate"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
)

type busySendConversationBindingReader interface {
	GetBusySendConversationBinding(string) (store.BusySendConversationBinding, error)
}

var _ busySendConversationBindingReader = (*store.Store)(nil)

type resumeCall struct {
	who      caller
	parent   string
	delegate func(ResumeParams, string) (ResumeResult, error)
}

// The runner seam replaces only the child transport, never the selector or
// coverage gate. Test-only Go tmux objects cannot cross a process boundary.
type resumeDelegateRunner func(context.Context, string, resumedelegate.Request, time.Duration) ([]byte, bool, error)

// The discovery seam lets deadline tests stall discovery before a child exists.
type resumeDelegateDiscovery func(context.Context, string) ([]installversion.InstalledVersion, error)

func (c *Client) newResumeCall() resumeCall {
	if request := c.delegatedResume; request != nil {
		who := request.Caller
		// A recipient rechecks the binding but cannot recursively delegate or
		// fall back if the selected runtime cannot serve the current provider.
		return resumeCall{who: caller{process: who.Process, pid: who.PID, hostname: who.Hostname, user: who.User}, parent: who.ParentInstanceID}
	}
	call := resumeCall{who: callerIdentity(), parent: spawn.ParentIDFromEnv()}
	deadline := time.Now().Add(resumeDelegateBudget(c.cfg))
	call.delegate = func(params ResumeParams, provider string) (ResumeResult, error) {
		return c.delegateResume(params, provider, call, deadline)
	}
	return call
}

// The transport's finite outer bound includes the existing resume phase
// ceilings (max(Q+C+2A, 2Q+C)), their pipe waits, pre-trust, bounded store
// operations/owner-release attempts and the CLI return reserve. Each phase
// retains its own configured allowance inside the child. Discovery and setup
// spend this same deadline; a timeout never starts a new attempt or budget.
func resumeDelegateBudget(cfg config.Config) time.Duration {
	q, a, c, w := cfg.Tmux.EffectiveQueryTimeout(), cfg.Tmux.EffectiveActionTimeout(), cfg.Tmux.EffectiveCreateTimeout(), cfg.Tmux.EffectivePipeCloseWait()
	sum := func(parts ...time.Duration) time.Duration {
		var total time.Duration
		for _, part := range parts {
			if part > time.Duration(math.MaxInt64)-total {
				return time.Duration(math.MaxInt64)
			}
			total += part
		}
		return total
	}
	phase := max(sum(q, c, a, a, w, w, w, w), sum(q, c, q, w, w, w))
	busy := time.Duration(cfg.Store.EffectiveBusyTimeoutMs()) * time.Millisecond
	return sum(phase, time.Duration(cfg.PreTrust.EffectiveLockWaitSeconds())*time.Second,
		busy, busy, busy, busy, busy, busy, busy, busy, busy, busy, busy, busy, time.Second)
}

func (c *Client) delegateResume(params ResumeParams, provider string, call resumeCall, deadline time.Time) (ResumeResult, error) {
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return ResumeResult{}, fmt.Errorf("resume delegation: no delegate started: %w", err)
	}
	if c.homeErr != nil {
		return ResumeResult{}, fmt.Errorf("resume delegation: no delegate started: resolve home: %w", c.homeErr)
	}
	discover := c.resumeVersions
	if discover == nil {
		discover = installversion.ListInstalledContext
	}
	versions, err := discover(ctx, layout.Dir(c.home))
	if err != nil {
		return ResumeResult{}, fmt.Errorf("resume delegation: no delegate started: read installed versions: %w", err)
	}
	core, digest := store.BusySendCoreContractIdentity()
	var selected *installversion.InstalledVersion
	for i := range versions {
		if err := ctx.Err(); err != nil {
			return ResumeResult{}, fmt.Errorf("resume delegation: no delegate started: %w", err)
		}
		v := &versions[i]
		if v.CoreVersion == core && v.CoreDeclarationsSHA256 == digest && slices.Contains(v.CoveredAgents, provider) &&
			(selected == nil || v.InstalledSequence > selected.InstalledSequence) {
			selected = v
		}
	}
	if err := ctx.Err(); err != nil {
		return ResumeResult{}, fmt.Errorf("resume delegation: no delegate started: %w", err)
	}
	if selected == nil {
		return ResumeResult{}, &DetailedError{
			Err:     fmt.Errorf("%w: install a version that covers agent %s and retry resume; nothing was written or launched", ErrSpawnNotResumable, provider),
			Details: NotResumableDetails{Cause: "covering_version_unavailable", Provider: provider},
		}
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return ResumeResult{}, err
	}
	request := resumedelegate.Request{
		RequestFormat: resumedelegate.Format1, ResponseFormat: resumedelegate.Format1, Params: paramsJSON,
		Home: c.home, StorePath: c.st.Path(), StoreID: c.st.StoreID(), ConfigPath: c.configPath, TmuxCommand: c.tmuxCommand,
		Caller:   resumedelegate.Caller{Process: call.who.process, PID: call.who.pid, Hostname: call.who.hostname, User: call.who.user, ParentInstanceID: call.parent},
		Deadline: deadline,
	}
	if err := request.Validate(); err != nil {
		return ResumeResult{}, fmt.Errorf("resume delegation: no delegate started: %w", err)
	}
	runner := c.resumeRunner
	if runner == nil {
		runner = runResumeDelegate
	}
	data, started, runErr := runner(ctx, selected.RuntimeExecutable, request, c.cfg.Tmux.EffectivePipeCloseWait())
	if !started {
		return ResumeResult{}, fmt.Errorf("resume delegation: no delegate started: %v", runErr)
	}
	// A complete valid outcome wins even if exit/pipe cleanup also failed.
	// No-response failures, including a timeout, are always outcome unknown.
	response, err := resumedelegate.DecodeResponse(bytes.NewReader(data), request.ResponseFormat)
	if err != nil {
		return ResumeResult{}, fmt.Errorf("delegated resume outcome is unknown; a Launch may have been created; check the agent with get before taking further action")
	}
	if response.Error != nil {
		return ResumeResult{}, &delegatedResumeError{remote: *response.Error}
	}
	var result ResumeResult
	if err := json.Unmarshal(response.Result, &result); err != nil || result.InstanceID == "" || result.InstanceID != params.InstanceID ||
		(result.PreTrust != "ok" && result.PreTrust != "skipped" && result.PreTrust != "failed") {
		return ResumeResult{}, fmt.Errorf("delegated resume outcome is unknown; a Launch may have been created; check the agent with get before taking further action")
	}
	result.delegatedJSON = string(response.Result)
	return result, nil
}

// cappedResumeOutput drains excess output without retaining unbounded memory.
// MaxBytes covers the entire wire message, including its trailing newline.
// Overflow is not a valid response, even if its prefix happens to parse.
type cappedResumeOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *cappedResumeOutput) Write(p []byte) (int, error) {
	n := len(p)
	keep := min(n, resumedelegate.MaxBytes-b.Len())
	_, _ = b.Buffer.Write(p[:keep])
	b.overflow = b.overflow || keep != n
	return n, nil
}

func runResumeDelegate(ctx context.Context, executable string, request resumedelegate.Request, wait time.Duration) ([]byte, bool, error) {
	var input bytes.Buffer
	if err := resumedelegate.Encode(&input, request); err != nil {
		return nil, false, err
	}
	cmd := exec.CommandContext(ctx, executable, resumedelegate.Argument)
	cmd.Stdin = &input
	var output cappedResumeOutput
	cmd.Stdout, cmd.Stderr, cmd.WaitDelay = &output, io.Discard, wait
	if err := cmd.Start(); err != nil {
		return nil, false, err
	}
	err := cmd.Wait()
	if output.overflow {
		return nil, true, fmt.Errorf("resume delegation: oversized response")
	}
	return output.Bytes(), true, err
}

// The private adapter opens only the referenced existing store and checks its
// identity before Resume can perform any defaults, trust, custody or launch
// work. The CLI adapter applies Home before calling this bridge.
func init() {
	resumedelegate.Resume = func(request resumedelegate.Request) (json.RawMessage, error) {
		if err := request.Validate(); err != nil {
			return nil, err
		}
		var params ResumeParams
		if err := resumedelegate.DecodeParams(request.Params, &params); err != nil {
			return nil, err
		}
		if params.InstanceID == "" {
			return nil, fmt.Errorf("%w: instance_id is required", ErrInvalidFlags)
		}
		c, err := New(Options{StorePath: request.StorePath, ConfigPath: request.ConfigPath, TmuxCommand: request.TmuxCommand})
		if err != nil {
			return nil, err
		}
		defer c.Close()
		if c.st.StoreID() != request.StoreID {
			return nil, fmt.Errorf("resume delegation: store identity mismatch; nothing was written or launched")
		}
		if !time.Now().Before(request.Deadline) {
			return nil, fmt.Errorf("resume delegation: deadline expired before resume; nothing was written or launched")
		}
		c.delegatedResume = &request
		result, err := c.Resume(params)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}
}

type delegatedResumeError struct{ remote resumedelegate.Error }

func (e *delegatedResumeError) Error() string     { return e.remote.Description }
func (e *delegatedResumeError) ErrorName() string { return e.remote.Name }
func (e *delegatedResumeError) Is(target error) bool {
	name := errorName(target)
	return name != "ErrInternal" && name == e.remote.Name
}
