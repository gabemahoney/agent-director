package api_test

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func TestSendKeysChunkDrainExactSubmission(t *testing.T) {
	t.Parallel()
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) {
			e := newSubmissionEnv(t)
			spec := codexSpec(store.StatePending, agentAlive, false)
			spec.Opts = append(spec.Opts, apitest.WithAgent(agent))
			r := e.seedRow(t, spec)
			payload := strings.Repeat("a", 511) + "🌍\r\n" + strings.Repeat("界", 6000)
			normalized := strings.ReplaceAll(payload, "\r", "")
			var readyAt time.Time
			readyPolls, zeroSamples := 0, 0
			e.b.ready = func() (bool, error) { readyPolls++; readyAt = e.clock.Now(); return readyPolls >= 3, nil }
			e.b.queue = func() (int, error) { zeroSamples++; return 0, nil }
			var lastWrite time.Time
			e.rec.AfterCall(tmux.CallSendText, func(c tmuxfix.SocketCall, _ error) {
				if e.b.current == nil || e.b.current.BytesAttempted < len(c.Text) || e.b.current.Phase != "text_attempted" {
					t.Fatal("text reached transport before durable intent")
				}
				if zeroSamples < 2 && !lastWrite.IsZero() || !lastWrite.IsZero() && e.clock.Now().Sub(lastWrite) < 40*time.Millisecond {
					t.Fatal("next chunk sent before two spaced empty queue samples")
				}
				if !utf8.ValidString(c.Text) || e.clock.Now().Before(readyAt) || readyPolls < 3 {
					t.Fatal("input before readiness or split UTF-8")
				}
				zeroSamples, lastWrite = 0, e.clock.Now()
			})
			e.rec.AfterCall(tmux.CallSendKey, func(tmuxfix.SocketCall, error) {
				if !e.b.current.EnterAttempted || e.b.current.Phase != "enter_attempted" || e.b.current.BytesAttempted != len(normalized) || zeroSamples < 2 {
					t.Fatal("Enter preceded durable intent or final drain")
				}
			})
			result, err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: payload, AllowPending: true, DeliveryRequestID: "request-1"})
			if err != nil || result.Outcome != "submitted" || result.RequestID != "request-1" || result.RecordID != "new-user-record" || result.RecordSHA256 != "record-hash" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			assertOrdinaryWrites(t, e.killEnv, r, normalized)
		})
	}
}
func TestSendKeysReadinessRefusesBeforeInput(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"missing marker", "unsupported launch", "initial backlog", "terminal query fails"} {
		t.Run(mode, func(t *testing.T) {
			e := newSubmissionEnv(t)
			r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
			switch mode {
			case "missing marker":
				e.b.ready = func() (bool, error) { return false, nil }
			case "unsupported launch":
				e.b.ready = func() (bool, error) { return false, fmt.Errorf("unsupported version") }
			case "initial backlog":
				e.b.queue = func() (int, error) { return 1, nil }
			case "terminal query fails":
				e.b.queue = func() (int, error) { return 0, fmt.Errorf("ioctl failed") }
			}
			_, err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true})
			assertDeliveryError(t, err, "not_started", 0, false)
			e.assertNothingSent(t)
			if e.b.current != nil {
				t.Fatal("attempt allocated before readiness")
			}
			if mode == "missing marker" && e.slept != 12*time.Second {
				t.Fatalf("readiness bound=%v", e.slept)
			}
		})
	}
}
func TestSendKeysUncertainTransportNeverReplays(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"text timeout", "drain timeout", "queue failure", "Enter timeout", "no evidence", "ambiguous evidence", "Enter intent save fails"} {
		t.Run(mode, func(t *testing.T) {
			e := newSubmissionEnv(t)
			r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
			e.b.confirm = func(*api.InputDeliveryAttempt) (*api.InputDeliveryProof, error) { return nil, nil }
			switch mode {
			case "text timeout":
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailTimeout}, tmux.CallSendText)
			case "drain timeout", "queue failure":
				e.b.queue = func() (int, error) {
					if len(e.rec.SocketCallsOf(tmux.CallSendText)) == 0 {
						return 0, nil
					}
					if mode == "queue failure" {
						return 0, fmt.Errorf("ioctl")
					}
					return 128, nil
				}
			case "Enter timeout":
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailTimeout}, tmux.CallSendKey)
			case "ambiguous evidence":
				e.b.confirm = func(*api.InputDeliveryAttempt) (*api.InputDeliveryProof, error) {
					return nil, fmt.Errorf("two matching records")
				}
			case "Enter intent save fails":
				e.b.save = func(a *api.InputDeliveryAttempt) error {
					if a.EnterAttempted {
						return fmt.Errorf("fsync")
					}
					return nil
				}
			}
			p := api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true, DeliveryRequestID: "same-request"}
			_, err := e.send(p)
			enter := mode == "Enter timeout" || mode == "no evidence" || mode == "ambiguous evidence" || mode == "Enter intent save fails"
			outcome := "partial_input_unconfirmed"
			if enter {
				outcome = "submission_unconfirmed"
			}
			d := assertDeliveryError(t, err, outcome, len(skpText), enter)
			if d.RequestID != p.DeliveryRequestID || !strings.Contains(err.Error(), "do not automatically replay text or Enter") {
				t.Fatalf("missing retry contract: %+v %v", d, err)
			}
			texts, enters := len(e.rec.SocketCallsOf(tmux.CallSendText)), len(ordinaryEnters(e.killEnv))
			wantEnters := 0
			if enter && mode != "Enter intent save fails" {
				wantEnters = 1
			}
			if texts != 1 || enters != wantEnters {
				t.Fatalf("initial attempt sent text=%d Enter=%d; want text=1 Enter=%d", texts, enters, wantEnters)
			}
			_, retryErr := e.send(p)
			if retryErr == nil || len(e.rec.SocketCallsOf(tmux.CallSendText)) != texts || len(ordinaryEnters(e.killEnv)) != enters {
				t.Fatalf("retry replayed uncertain input: %v", retryErr)
			}
			if mode == "Enter intent save fails" && enters != 0 {
				t.Fatal("Enter sent without durable intent")
			}
		})
	}
}
func TestSendKeysHookActivityCannotProveSubmission(t *testing.T) {
	t.Parallel()
	for _, state := range []string{store.StateWorking, store.StateAskUser, store.StateCheckPermission, store.StateEnded} {
		t.Run(state, func(t *testing.T) {
			e := newSubmissionEnv(t)
			r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
			e.s.read = func(row api.Spawn) (api.Spawn, error) { row.State = state; return row, nil }
			e.b.confirm = func(*api.InputDeliveryAttempt) (*api.InputDeliveryProof, error) { return nil, nil }
			_, err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true})
			assertDeliveryError(t, err, "submission_unconfirmed", len(skpText), true)
			assertOrdinaryWrites(t, e.killEnv, r, skpText)
		})
	}
}
func TestSendKeysRequestReconciliation(t *testing.T) {
	t.Parallel()
	e := newSubmissionEnv(t)
	r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
	p := api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true, DeliveryRequestID: "original"}
	e.b.confirm = func(*api.InputDeliveryAttempt) (*api.InputDeliveryProof, error) { return nil, nil }
	_, err := e.send(p)
	assertDeliveryError(t, err, "submission_unconfirmed", len(skpText), true)
	e.b.confirm = nil
	result, err := e.send(p)
	if err != nil || result.RequestID != "original" || result.Outcome != "submitted" {
		t.Fatalf("late proof=%+v %v", result, err)
	}
	_, err = e.send(p)
	if err != nil {
		t.Fatal(err)
	}
	assertOrdinaryWrites(t, e.killEnv, r, skpText)
	p.DeliveryRequestID = "intentional-repeat"
	if _, err = e.send(p); err != nil {
		t.Fatal(err)
	}
	if len(e.rec.SocketCallsOf(tmux.CallSendText)) != 2 || len(ordinaryEnters(e.killEnv)) != 2 {
		t.Fatal("new ID did not submit the intentional repeat")
	}
	p.DeliveryRequestID = "original"
	if result, err = e.send(p); err != nil || result.RequestID != "original" {
		t.Fatalf("historical retry=%+v %v", result, err)
	}
	p.Text = "different text"
	_, err = e.send(p)
	assertDeliveryError(t, err, "not_started", 0, false)
	if len(ordinaryEnters(e.killEnv)) != 2 {
		t.Fatal("request collision submitted new input")
	}
}
func TestSendKeysConcurrentWriterRefuses(t *testing.T) {
	t.Parallel()
	e := newSubmissionEnv(t)
	r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
	held, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	e.b.ready = func() (bool, error) { close(held); <-release; return true, nil }
	p := api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true}
	go func() { _, err := e.send(p); done <- err }()
	<-held
	_, err := e.send(p)
	close(release)
	firstErr := <-done
	assertDeliveryError(t, err, "not_started", 0, false)
	if firstErr != nil {
		t.Fatal(firstErr)
	}
	assertOrdinaryWrites(t, e.killEnv, r, skpText)
}

func TestSendKeysManualInterferenceBlocksAutomaticInput(t *testing.T) {
	t.Parallel()
	for _, unresolved := range []bool{false, true} {
		t.Run(fmt.Sprint(unresolved), func(t *testing.T) {
			e := newSubmissionEnv(t)
			r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
			p := api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true}
			if unresolved {
				e.b.confirm = func(*api.InputDeliveryAttempt) (*api.InputDeliveryProof, error) { return nil, nil }
				_, err := e.send(p)
				assertDeliveryError(t, err, "submission_unconfirmed", len(skpText), true)
			}
			request := ""
			if e.b.current != nil {
				request = e.b.current.RequestID
			}
			if _, err := e.send(api.SendKeysParams{InstanceID: r.ID, Key: "Escape", AllowPending: true}); err != nil {
				t.Fatal(err)
			}
			if !e.b.current.ManualInterference || request != "" && e.b.current.RequestID != request {
				t.Fatal("manual input erased the unresolved owner")
			}
			texts, enters := len(e.rec.SocketCallsOf(tmux.CallSendText)), len(ordinaryEnters(e.killEnv))
			if _, err := e.send(p); err == nil {
				t.Fatal("automatic input resumed after manual interference")
			}
			if texts != len(e.rec.SocketCallsOf(tmux.CallSendText)) || enters != len(ordinaryEnters(e.killEnv)) {
				t.Fatal("manual composer was replayed")
			}
		})
	}
}

func TestSendKeysLostLaunchStopsBeforeEnter(t *testing.T) {
	t.Parallel()
	e := newSubmissionEnv(t)
	r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
	e.rec.RemoveSessionAfter(tmux.CallSendText, r.Socket, r.Session.ID)
	_, err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: strings.Repeat("x", 1024), AllowPending: true})
	assertDeliveryError(t, err, "partial_input_unconfirmed", 512, false)
	if len(e.rec.SocketCallsOf(tmux.CallSendText)) != 1 || len(ordinaryEnters(e.killEnv)) != 0 {
		t.Fatal("input continued after launch ownership was lost")
	}
}

func TestSendKeysDrainResetsOnBacklog(t *testing.T) {
	t.Parallel()
	for _, queueDuration := range []time.Duration{0, 75 * time.Millisecond} {
		t.Run(queueDuration.String(), func(t *testing.T) {
			e := newSubmissionEnv(t)
			r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
			// Initial-empty check, then a drain reset before each subsequent chunk/Enter.
			samples := []int{0, 0, 7, 0, 0, 0, 7, 0, 0}
			type sample struct {
				start, end time.Time
				queue      int
			}
			var calls []sample
			index := 0
			e.b.queue = func() (int, error) {
				if index >= len(samples) {
					t.Fatal("unexpected extra queue sample")
				}
				q := samples[index]
				index++
				start := e.clock.Now()
				e.clock.Advance(queueDuration)
				calls = append(calls, sample{start, e.clock.Now(), q})
				return q, nil
			}
			assertDrain := func(first, second int) {
				t.Helper()
				if len(calls) <= second || calls[first].queue != 0 || calls[second].queue != 0 || calls[second].start.Sub(calls[first].end) < 40*time.Millisecond {
					t.Fatalf("qualifying zeros were not separated after completed sample: %+v", calls)
				}
			}
			e.rec.AfterCall(tmux.CallSendText, func(_ tmuxfix.SocketCall, _ error) {
				if len(e.rec.SocketCallsOf(tmux.CallSendText)) == 2 {
					if index != 5 {
						t.Fatalf("second chunk before backlog reset/drain: %d samples", index)
					}
					assertDrain(3, 4)
				}
			})
			e.rec.AfterCall(tmux.CallSendKey, func(c tmuxfix.SocketCall, _ error) {
				if c.Key == "Enter" && index != len(samples) {
					t.Fatal("nonempty queue did not reset the drain samples")
				}
				if c.Key == "Enter" {
					assertDrain(7, 8)
				}
			})
			payload := strings.Repeat("x", 513)
			if _, err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: payload, AllowPending: true}); err != nil {
				t.Fatal(err)
			}
			assertOrdinaryWrites(t, e.killEnv, r, payload)
		})
	}
}

func TestSendKeysReadinessWithFixedClockIsBounded(t *testing.T) {
	t.Parallel()
	e := newSubmissionEnv(t)
	r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
	now := e.clock.Now()
	e.env.Relay.Now = func() time.Time { return now }
	polls := 0
	e.b.ready = func() (bool, error) { polls++; return false, nil }
	_, err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true})
	assertDeliveryError(t, err, "not_started", 0, false)
	if polls > 121 || e.slept > 12100*time.Millisecond {
		t.Fatalf("unbounded readiness: polls=%d sleep=%v", polls, e.slept)
	}
	e.assertNothingSent(t)
}

func TestSendKeysSlowQueueCannotSupplyDrainSpacing(t *testing.T) {
	t.Parallel()
	e := newSubmissionEnv(t)
	r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
	// The injected clock advances inside Queue, but not between its calls.
	// Slow samples therefore cannot qualify as the required spaced zeros.
	e.env.Sleep = func(time.Duration) {}
	var lastEnd time.Time
	samples := 0
	e.b.queue = func() (int, error) {
		start := e.clock.Now()
		if samples > 1 && start.Sub(lastEnd) >= 40*time.Millisecond {
			t.Fatal("fixture unexpectedly supplied the required spacing")
		}
		e.clock.Advance(75 * time.Millisecond)
		lastEnd = e.clock.Now()
		samples++
		return 0, nil
	}
	_, err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true})
	assertDeliveryError(t, err, "partial_input_unconfirmed", len(skpText), false)
	if len(e.rec.SocketCallsOf(tmux.CallSendText)) != 1 || len(ordinaryEnters(e.killEnv)) != 0 {
		t.Fatal("slow queue calls satisfied spacing or caused replay")
	}
}
