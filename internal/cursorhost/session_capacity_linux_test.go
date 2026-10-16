//go:build linux

package cursorhost

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestCursorAdmissionBeforeInsertionRetainsCancellationEpoch(t *testing.T) {
	// Serial: real Run, protected records and terminal fixture own environment.
	i := &completeInput{ready: make(chan struct{})}
	c := beginCursorSessionWithInput(t, "", openTranscript, i.start)
	<-i.ready
	c.wait(t, "ready", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor ready")) })
	before, err := c.e.s.GetSpawn(c.e.binding.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	// This is the actual admitted value, parked before Run's emit/mailbox put.
	old := terminalInput{text: "admitted-before-interrupt", accepted: i.admit()}
	if !old.accepted {
		t.Fatal("ordinary input was not admitted")
	}
	i.emit(terminalInput{interrupt: true})
	c.wait(t, "protected consumed control", func() bool {
		for _, r := range c.records(t) {
			if r.Kind == "input_control" {
				return true
			}
		}
		return false
	})
	i.emit(old)
	c.wait(t, "late insertion retired", func() bool {
		for _, r := range c.records(t) {
			if r.Kind == "submission_cancelled" {
				return true
			}
		}
		return false
	})
	if got := c.prompts(t); len(got) != 0 {
		t.Fatalf("cancelled admitted input reached ACP: %q", got)
	}
	raw, err := os.ReadFile(c.e.runtime.Raw)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(old.text)) {
		t.Fatal("cancelled input has raw peer bytes")
	}
	c.wait(t, "recovery", func() bool { return bytes.Contains(c.pane.bytes(), []byte("submit a new prompt to continue")) })
	i.submit(t, "later-explicit-after-admission-race")
	c.wait(t, "later explicit peer receipt", func() bool { return len(c.prompts(t)) == 1 })
	c.stop(t)
	if got := c.prompts(t); !reflect.DeepEqual(got, []string{"later-explicit-after-admission-race"}) {
		t.Fatalf("replay: %q", got)
	}
	after, err := c.e.s.GetSpawn(c.e.binding.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if after.SessionID != before.SessionID || after.LifeNumber != before.LifeNumber {
		t.Fatal("cancellation changed conversation/life")
	}
	var acceptedID string
	cancelled := 0
	for _, r := range c.records(t) {
		if r.Kind == "submission_accepted" && r.Data.Text == old.text {
			acceptedID = r.Data.ID
		}
		if r.Kind == "submission_cancelled" && r.Data.ID == acceptedID && acceptedID != "" {
			if r.Data.Started {
				t.Fatal("late insertion fabricated execution")
			}
			cancelled++
		}
	}
	if acceptedID == "" || cancelled != 1 {
		t.Fatalf("missing unique disposition: id=%s count=%d", acceptedID, cancelled)
	}
	if !bytes.Contains(c.pane.bytes(), []byte("submission "+acceptedID+" cancelled before dispatch")) {
		t.Fatal("cancellation was not visible")
	}
}

func TestCursorWaitingMailboxByteBoundaries(t *testing.T) {
	// Serial: real Run and verified runtime fixture own environment.
	const mib = 1024 * 1024
	for _, scope := range []string{"individual", "aggregate"} {
		for _, delta := range []int{-1, 0, 1} {
			t.Run(fmt.Sprintf("%s/%+d", scope, delta), func(t *testing.T) {
				i := &completeInput{ready: make(chan struct{})}
				c := beginCursorSessionWithInput(t, "HOLD", openTranscript, i.start)
				<-i.ready
				c.wait(t, "active receipt", func() bool { return len(c.prompts(t)) == 1 })
				var want []string
				if scope == "aggregate" {
					for n := 0; n < 7; n++ {
						want = append(want, byteBoundaryText(n, mib))
					}
				}
				last := byteBoundaryText(7, mib+delta)
				if scope == "aggregate" && delta == 1 {
					// Each entry remains individually legal; only the sum exceeds 8MiB.
					want = append(want, byteBoundaryText(7, mib))
					last = "!"
				}
				for _, text := range want {
					i.submit(t, text)
				}
				i.submit(t, last)
				if delta <= 0 {
					want = append(want, last)
				}
				expectedRefusals := 0
				if delta > 0 {
					expectedRefusals = 1
				}
				c.wait(t, "all waiting inputs accounted", func() bool {
					a, refused := 0, 0
					for _, r := range c.records(t) {
						if r.Kind == "submission_accepted" {
							a++
						}
						if r.Kind == "submission_refused" {
							if r.Data.Reason != "capacity" {
								t.Fatalf("wrong byte refusal: %s", r.Data.Reason)
							}
							refused++
						}
					}
					return a == len(want)+1 && refused == expectedRefusals
				})
				var got []string
				ids := map[string]bool{}
				for _, r := range c.records(t) {
					if r.Kind == "submission_accepted" && r.Data.Text != "HOLD" {
						got = append(got, r.Data.Text)
						ids[r.Data.ID] = true
					}
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatal("waiting byte acceptance text/order changed")
				}
				if got := c.prompts(t); !reflect.DeepEqual(got, []string{"HOLD"}) {
					t.Fatal("waiting bytes escaped active turn")
				}
				if delta > 0 {
					c.wait(t, "explicit capacity refusal", func() bool {
						return bytes.Contains(c.pane.bytes(), []byte("Cursor input refused while starting or busy (capacity); nothing sent"))
					})
				}
				i.emit(terminalInput{interrupt: true})
				c.wait(t, "peer receives active cancellation", func() bool {
					for _, frame := range c.e.runtime.Frames(t) {
						if string(frame["method"]) != `"session/cancel"` {
							continue
						}
						var params struct {
							SessionID string `json:"sessionId"`
						}
						if err := json.Unmarshal(frame["params"], &params); err != nil {
							t.Fatal(err)
						}
						if params.SessionID == "synthetic-session-1" {
							return true
						}
					}
					return false
				})
				if err := os.WriteFile(c.e.runtime.Release, nil, 0600); err != nil {
					t.Fatal(err)
				}
				c.wait(t, "cancelled active recovers", func() bool { return bytes.Contains(c.pane.bytes(), []byte("submit a new prompt to continue")) })
				c.wait(t, "each byte reservation cancelled", func() bool {
					n := 0
					for _, r := range c.records(t) {
						if r.Kind == "submission_cancelled" && ids[r.Data.ID] {
							n++
						}
					}
					return n == len(ids)
				})
				expected := []string{"HOLD"}
				if scope == "aggregate" && delta == 1 {
					// Follow the capacity advice: after space is available, explicitly
					// submit the exact refused text; inspection alone did not queue it.
					i.submit(t, last)
					c.wait(t, "explicitly resubmitted refused text completes", func() bool {
						var resubmittedID string
						for _, r := range c.records(t) {
							if r.Kind == "submission_accepted" && r.Data.Text == last {
								resubmittedID = r.Data.ID
							}
							if r.Kind == "turn_outcome" && resubmittedID != "" && r.Data.ID == resubmittedID {
								return true
							}
						}
						return false
					})
					expected = append(expected, last)
				}
				// Refill the same byte capacity and drain it through ACP, proving release
				// accounting and FIFO without replaying any of the cancelled entries.
				var refill []string
				for n := 0; n < len(want); n++ {
					refill = append(refill, byteBoundaryText(n+10, len(want[n])))
				}
				if len(refill) == 0 {
					refill = append(refill, byteBoundaryText(10, mib))
				}
				acceptedBeforeRefill := rotationRecordCount(c, t, "submission_accepted")
				var consumed []terminalInput
				for n, text := range refill {
					input := terminalInput{text: text}
					c.wait(t, fmt.Sprintf("refill consumption authority %d", n+1), func() bool {
						input.accepted = i.admit()
						return input.accepted
					})
					consumed = append(consumed, input)
				}
				for _, input := range consumed {
					i.emit(input)
				}
				// Observe each acceptance separately; the first peer wait must not
				// also time the full batch's protected persistence and hook setup.
				for n := range consumed {
					c.wait(t, fmt.Sprintf("refill acceptance %d", n+1), func() bool {
						return rotationRecordCount(c, t, "submission_accepted") >= acceptedBeforeRefill+n+1
					})
				}
				for n := range refill {
					c.wait(t, fmt.Sprintf("refill peer receipt %d", n+1), func() bool {
						raw, err := os.ReadFile(c.e.runtime.Trace)
						if err != nil {
							t.Fatal(err)
						}
						end := bytes.LastIndexByte(raw, '\n')
						return end >= 0 && bytes.Count(raw[:end], []byte(`"method":"session/prompt"`)) >= n+len(expected)+1
					})
				}
				c.stop(t)
				if got := c.prompts(t); !reflect.DeepEqual(got, append(expected, refill...)) {
					t.Fatal("refill order/text mismatch or cancelled replay")
				}
				cancelled := map[string]int{}
				for _, r := range c.records(t) {
					if r.Kind == "submission_cancelled" && ids[r.Data.ID] {
						if r.Data.Started {
							t.Fatal("waiting entry fabricated execution")
						}
						cancelled[r.Data.ID]++
					}
				}
				for id := range ids {
					if cancelled[id] != 1 || !bytes.Contains(c.pane.bytes(), []byte("submission "+id+" cancelled before dispatch")) {
						t.Fatalf("incomplete cancellation %s: %v", id, cancelled)
					}
				}
				raw, err := os.ReadFile(c.e.runtime.Raw)
				if err != nil {
					t.Fatal(err)
				}
				for _, text := range want {
					if bytes.Contains(raw, []byte(text)) {
						t.Fatal("cancelled waiting text has raw peer bytes")
					}
				}
			})
		}
	}
}

func byteBoundaryText(index, size int) string {
	prefix := fmt.Sprintf("entry-%02d:", index)
	remaining := size - len(prefix)
	return prefix + strings.Repeat("🦋", remaining/4) + strings.Repeat("x", remaining%4)
}
