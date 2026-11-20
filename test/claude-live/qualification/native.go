package qualification

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
	"github.com/gabemahoney/agent-director/test/claude-live/signals"
)

// NativeSample contains native measurement only: no AD custody or admission.
type NativeSample struct {
	Rung        Rung           `json:"rung"`
	Session     string         `json:"session"`
	Emission    NativeEmission `json:"emission"`
	Receipt     string         `json:"receipt"`
	Transcript  string         `json:"transcript"`
	Intake      signals.Intake `json:"intake"`
	Whole       bool           `json:"whole"`
	NextRequest bool           `json:"next_request"`
	Error       string         `json:"error,omitempty"`
}

func nativeSession() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func (e *Exercise) startNative(ctx context.Context, route, prompt string, rungs []Rung) (runner.Owned, error) {
	r := e.Run
	id := "live-" + r.Manifest.RunID + "-native-" + route
	work := filepath.Join(r.Evidence.Root, "work-native-"+route)
	if err := os.Mkdir(work, 0700); err != nil {
		return runner.Owned{}, err
	}
	session, err := nativeSession()
	if err != nil {
		return runner.Owned{}, err
	}
	pointer, err := r.RuntimePath("native-" + route + "-pointer")
	if err != nil {
		return runner.Owned{}, err
	}
	if _, err = r.RuntimePath(filepath.Base(pointer) + "-lock"); err != nil {
		return runner.Owned{}, err
	}
	watch, err := r.RuntimePath("native-" + route + "-watch")
	if err != nil {
		return runner.Owned{}, err
	}
	if err = os.WriteFile(pointer, nil, 0600); err != nil {
		return runner.Owned{}, err
	}
	if err = os.WriteFile(watch, nil, 0600); err != nil {
		return runner.Owned{}, err
	}
	plan := NativePlan{Schema: Schema, Run: r.Manifest.RunID, Root: r.Evidence.Root, CWD: work, Instance: id, Session: session, Route: route, Pointer: pointer, Watch: watch, Marks: map[string]string{}, Rungs: rungs}
	for _, v := range rungs {
		path, er := r.RuntimePath("native-" + route + "-" + v.ID)
		if er != nil {
			return runner.Owned{}, er
		}
		plan.Marks[v.ID] = path
	}
	ref := "raw/native-" + route + "-plan.json"
	if err = r.Evidence.JSON(ref, plan); err != nil {
		return runner.Owned{}, err
	}
	hooks := map[string]any{}
	for _, event := range r.Manifest.RecordingHook.Events {
		hook := map[string]any{"type": "command", "command": filepath.Join(r.Kit, "bin/claude-live"), "args": []string{"native-hook", "--native-plan", filepath.Join(r.Evidence.Root, ref), "--event", event}}
		if event == "FileChanged" {
			hook["asyncRewake"] = true
		}
		hooks[event] = []any{map[string]any{"hooks": []any{hook}}}
	}
	settings := "raw/native-" + route + "-settings.json"
	if err = r.Evidence.JSON(settings, map[string]any{"hooks": hooks}); err != nil {
		return runner.Owned{}, err
	}
	settingsPath, err := r.RuntimePath("native-" + route + "-settings.json")
	if err != nil {
		return runner.Owned{}, err
	}
	settingsData, err := runner.Read(r.Evidence.Root, settings)
	if err != nil {
		return runner.Owned{}, err
	}
	if err = os.WriteFile(settingsPath, settingsData, 0600); err != nil {
		return runner.Owned{}, err
	}
	o, err := r.StartNative(ctx, id, work, session, settingsPath, prompt)
	if err != nil {
		return o, err
	}
	if prompt != "" {
		r.Manifest.Cases = append(r.Manifest.Cases, runner.Case{ID: "N7", Mode: "native", InstanceID: o.InstanceID, Text: prompt, Started: o.Started, Ready: true})
	}
	err = r.AwaitEvidence(ctx, 45*time.Second, func(context.Context) (bool, error) {
		if er := e.checkProviderProgress(ctx, o.InstanceID); er != nil {
			return false, er
		}
		facts, er := e.nativeFacts(o)
		if er != nil {
			return false, er
		}
		for _, h := range facts {
			if h.Receipt.Event == "SessionStart" {
				return true, nil
			}
		}
		return false, nil
	})
	return o, err
}

func (e *Exercise) nativeFacts(o runner.Owned) ([]HookFact, error) {
	facts, err := LiveHookFacts(e.Run.Evidence.Root, e.Run.Manifest.RunID, e.Run.Manifest.RecordingHook.Events)
	if err != nil {
		return nil, err
	}
	var out []HookFact
	var starts int64
	for _, h := range facts {
		if runner.Text(h.Payload, "session_id") == o.SessionID && runner.Text(h.Payload, "agent_id") == "" {
			out = append(out, h)
			if h.Receipt.Event == "UserPromptSubmit" {
				starts++
			}
		}
	}
	return out, e.Run.NativeTurns(o.InstanceID, starts)
}

func (e *Exercise) nativeEmission(o runner.Owned, rung Rung) (NativeSample, error) {
	sample := NativeSample{Rung: rung, Session: o.SessionID}
	dir := filepath.Join(e.Run.Evidence.Root, "raw/native-emissions")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return sample, nil
	}
	if err != nil {
		return sample, err
	}
	for _, v := range entries {
		if !v.IsDir() {
			continue
		}
		ref := "raw/native-emissions/" + v.Name() + "/receipt.json"
		b, er := runner.Read(e.Run.Evidence.Root, ref)
		if os.IsNotExist(er) {
			continue
		}
		if er != nil {
			return sample, er
		}
		var rec NativeEmission
		if er = json.Unmarshal(b, &rec); er != nil {
			return sample, er
		}
		if rec.Case == rung.ID && rec.Session == o.SessionID {
			if sample.Receipt != "" {
				return sample, errors.New("duplicate native physical emission")
			}
			sample.Emission = rec
			sample.Receipt = ref
		}
	}
	if sample.Receipt == "" {
		return sample, nil
	}
	b, err := e.Run.ReadTranscript(o)
	if err != nil {
		sample.Error = err.Error()
		return sample, nil
	}
	sample.Transcript = "raw/native-input-" + rung.ID + ".jsonl"
	t, err := signals.ParseTranscript(b, o.SessionID)
	if err != nil {
		sample.Error = err.Error()
		return sample, nil
	}
	mode := "wait_turn"
	if rung.Route == "post_tool_batch" {
		mode = "steer_turn"
	}
	sample.Intake, err = t.NextBodyInput(sample.Emission.Started.UnixMilli(), rung.Body, mode)
	for _, q := range t.Requests {
		if !q.Time.Before(sample.Emission.Started) {
			sample.NextRequest = true
			break
		}
	}
	sample.Whole = err == nil
	if err != nil {
		sample.Error = err.Error()
	}
	return sample, nil
}

// Freeze a native transcript only once, after the bounded observation, since
// raw evidence is immutable and polling must never overwrite a prior file.
func (e *Exercise) freezeNativeSample(o runner.Owned, sample NativeSample) error {
	if sample.Transcript == "" {
		return nil
	}
	b, err := e.Run.ReadTranscript(o)
	if err != nil {
		return err
	}
	return e.Run.Evidence.Put(sample.Transcript, b)
}

func (e *Exercise) waitNativeSample(ctx context.Context, o runner.Owned, rung Rung) (NativeSample, error) {
	var sample NativeSample
	err := e.Run.AwaitEvidence(ctx, 45*time.Second, func(context.Context) (bool, error) {
		if er := e.checkProviderProgress(ctx, o.InstanceID); er != nil {
			return false, er
		}
		facts, er := e.nativeFacts(o)
		if er != nil {
			return false, er
		}
		sample, er = e.nativeEmission(o, rung)
		if er != nil {
			return false, er
		}
		if sample.Receipt == "" || !sample.NextRequest {
			return false, nil
		}
		for _, h := range facts {
			if h.Receipt.Event == "Stop" && !h.Receipt.Received.Before(sample.Emission.Started) {
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		err = e.workloadTimeout(err, o.InstanceID, rung.ID, "response-still-active")
		sample.Error = errors.Join(errors.New(sample.Error), err).Error()
	}

	writeErr := errors.Join(e.freezeNativeSample(o, sample), e.Run.Evidence.JSON("raw/native-sample-"+rung.ID+".json", sample))
	return sample, errors.Join(err, writeErr)
}

func NativeMeasurements(ctx context.Context, e *Exercise) error {
	wake, err := NativeRungs("wake")
	if err != nil {
		return err
	}
	o, err := e.startNative(ctx, "wake", "", wake)
	if err != nil {
		return err
	}
	for _, v := range wake {
		if v.ID == "N6" {
			b, er := runner.Read(e.Run.Evidence.Root, "raw/native-sample-N5.json")
			if er != nil {
				return er
			}
			var prev NativeSample
			if er = json.Unmarshal(b, &prev); er != nil {
				return er
			}
			if !prev.Whole {
				break
			}
		}
		if err = e.reserve(v.ID); err != nil {
			return err
		}
		now := e.Run.Now().UTC()
		e.Run.Manifest.Cases = append(e.Run.Manifest.Cases, runner.Case{ID: v.ID, Mode: "native", InstanceID: o.InstanceID, Text: v.ProviderInput, Started: now, Ready: true})
		var plan NativePlan
		b, er := runner.Read(e.Run.Evidence.Root, "raw/native-wake-plan.json")
		if er != nil {
			return er
		}
		if er = json.Unmarshal(b, &plan); er != nil {
			return er
		}
		if err = os.WriteFile(plan.Pointer, []byte(v.ID), 0600); err != nil {
			return err
		}
		if err = os.WriteFile(plan.Watch, []byte(v.ID+"\n"), 0600); err != nil {
			return err
		}
		if _, err = e.waitNativeSample(ctx, o, v); err != nil {
			return err
		}
	}
	if err = e.Run.StopNative(ctx, o.InstanceID); err != nil {
		return err
	}
	o, steer, err := startNativeSteer(ctx, e)
	if err != nil {
		return err
	}
	// Observe actual boundaries; absence never licenses another trigger prompt.
	err = e.Run.AwaitEvidence(ctx, 90*time.Second, func(context.Context) (bool, error) {
		if er := e.checkProviderProgress(ctx, o.InstanceID); er != nil {
			return false, er
		}
		facts, er := e.nativeFacts(o)
		if er != nil {
			return false, er
		}
		count := 0
		batches := map[string]bool{}
		open := map[string]bool{}
		stopped := false
		for _, h := range facts {
			if h.Receipt.Event == "PreToolUse" {
				input, _ := h.Payload["tool_input"].(map[string]any)
				command, _ := input["command"].(string)
				bg, _ := input["run_in_background"].(bool)
				id := runner.Text(h.Payload, "tool_use_id")
				if runner.Text(h.Payload, "tool_name") != "Bash" || command != "for i in 1 2; do sleep 1; done" || bg || id == "" || len(open) != 0 || batches[id] {
					return false, e.workload("insufficient-batches", o.InstanceID, "N7", "native parent did not supply the separate foreground calls; prefix retained", h.Reference)
				}
				open[id] = true
			}
			if h.Receipt.Event == "PostToolBatch" {
				ids := toolIDs(h.Payload)
				if len(ids) != 1 || batches[ids[0]] || !open[ids[0]] {
					return false, e.workload("insufficient-batches", o.InstanceID, "N7", "parallel/combined/repeated parent batch cannot supply five separate measurements", h.Reference)
				}
				batches[ids[0]] = true
				delete(open, ids[0])
				count++
			}
			if h.Receipt.Event == "Stop" {
				stopped = true
			}
		}
		if stopped && count < len(steer) {
			return false, e.workload("insufficient-batches", o.InstanceID, "N7", "native parent stopped before five separate parent PostToolBatch boundaries")
		}
		return count >= len(steer) && stopped, nil
	})
	var samples []NativeSample
	for _, v := range steer {
		s, er := e.nativeEmission(o, v)
		if er != nil {
			return er
		}
		if er = e.freezeNativeSample(o, s); er != nil {
			return er
		}
		samples = append(samples, s)
	}
	if er := e.Run.Evidence.JSON("raw/native-steer-samples.json", samples); er != nil {
		return er
	}
	stopErr := e.Run.StopNative(ctx, o.InstanceID)
	if err != nil {
		return errors.Join(e.workloadTimeout(err, o.InstanceID, "N7", "insufficient-batches"), stopErr)
	}
	return stopErr
}

// StartNativeSteer starts the actual N7 measurement with its normal reservation,
// fixed route and workload. It shares the phase's ownership and cleanup scope.
// This exported entry is for test use only. Replay tests must reuse it for fresh
// N7 setup instead of fabricating a wake ladder or bypassing confinement; see
// the reuse instructions in docs/architecture.md.
// Production phase consumers must use NativeMeasurements/Dispatch instead.
func StartNativeSteer(ctx context.Context, e *Exercise) (runner.Owned, error) {
	o, _, err := startNativeSteer(ctx, e)
	return o, err
}

func startNativeSteer(ctx context.Context, e *Exercise) (runner.Owned, []Rung, error) {
	steer, err := NativeRungs("post_tool_batch")
	if err != nil {
		return runner.Owned{}, nil, err
	}
	if err = e.reserve("N7"); err != nil {
		return runner.Owned{}, nil, err
	}
	prompt := "Native qualification: run EXACTLY five separate foreground Bash calls, each literally `for i in 1 2; do sleep 1; done`, one per response. Treat hook padding as inert. After the fifth, finish. Never use subagents or background work."
	o, err := e.startNative(ctx, "post_tool_batch", prompt, steer)
	return o, steer, err
}

// TextLimitsBacklog imports cap/drain evidence under its original owners;
// imported paths are validated by the file-only finalizer, never executed.
func TextLimitsBacklog(ctx context.Context, e *Exercise) error {
	if err := e.Run.Evidence.JSON("raw/import-requests.json", e.Options); err != nil {
		return err
	}
	if err := NativeMeasurements(ctx, e); err != nil {
		var outcome *workloadError
		if errors.As(err, &outcome) && outcome.Outcome.Code == "insufficient-batches" {
			for _, o := range e.Run.Manifest.Owned {
				if o.Native && !o.Exited {
					return err
				}
			}
			return errors.Join(err, LargeBacklog(ctx, e))
		}
		return err
	}
	return LargeBacklog(ctx, e)
}

func nativeRuntime(plan NativePlan) error {
	dir := filepath.Dir(plan.Pointer)
	if !strings.HasPrefix(filepath.Base(dir), "claude-live-runtime-") || filepath.Dir(dir) != runner.VMRoot || filepath.Dir(plan.Watch) != dir || plan.Pointer == plan.Watch {
		return errors.New("native runtime paths not private siblings")
	}
	if _, err := runner.Confined(dir, filepath.Base(plan.Pointer)); err != nil {
		return err
	}
	if _, err := runner.Confined(dir, filepath.Base(plan.Watch)); err != nil {
		return err
	}
	seen := map[string]bool{plan.Pointer: true, plan.Watch: true}
	for _, rung := range plan.Rungs {
		p := plan.Marks[rung.ID]
		if filepath.Dir(p) != dir || seen[p] {
			return errors.New("native marker outside owned runtime or duplicated")
		}
		seen[p] = true
		if _, err := runner.Confined(dir, filepath.Base(p)); err != nil {
			return err
		}
	}
	return nil
}
