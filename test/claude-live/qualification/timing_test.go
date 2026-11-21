package qualification_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/test/claude-live/internal/qualificationfix"
	q "github.com/gabemahoney/agent-director/test/claude-live/qualification"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
	"github.com/gabemahoney/agent-director/test/claude-live/signals"
)

func TestQualificationRecordedWholeInput(t *testing.T) {
	for _, kind := range []string{"whole", "short-middle", "unicode", "newline", "backslash", "preview", "later-echo", "duplicate", "missing-journal"} {
		t.Run(kind, func(t *testing.T) {
			f := qualificationfix.New(t, "groups")
			in := f.Packing()
			// Independently known separator and captured controlled body. The
			// expected text is never recomputed from altered request evidence.
			body := "[agent-director: message 1 of 1]\n" + runner.Text(in.Snapshot.Tables["messages"][2], "text")
			after := int64(1795022357792)
			transcript := in.Transcript
			want := "fail"
			switch kind {
			case "whole":
				want = "pass"
			case "short-middle":
				body = strings.Replace(body, "PLAIN-SEEN", "PLAIN", 1)
			case "unicode":
				body = strings.Replace(body, "café", "cafe", 1)
			case "newline":
				body = strings.Replace(body, "\n", "\r\n", 1)
			case "backslash":
				body += "\\"
			case "preview":
				body = body[:30]
			case "later-echo":
				after = 1795022334256 // next request is WAKE, not PLAIN
			case "duplicate":
				for i := range transcript.Requests {
					if transcript.Requests[i].Time.UnixMilli() >= after {
						b, _ := json.Marshal(signals.NativeWake(q.Transform(body, "wait_turn")))
						transcript.Requests[i].Messages = append(transcript.Requests[i].Messages, signals.Message{Role: "user", Content: b})
						break
					}
				}
			case "missing-journal":
				transcript.Requests = nil
				want = "not_completed"
			}
			got := q.CheckWholeInput(q.WholeInput{Body: body, Mode: "wait_turn", After: after, Transcript: transcript, Evidence: in.References})
			if got.SignalID != "whole-input" || got.Status != want || !reflect.DeepEqual(got.Evidence, in.References) {
				t.Fatalf("%+v; want %s", got, want)
			}
		})
	}
}

func TestQualificationRecordedPacking(t *testing.T) {
	for _, kind := range []string{"recorded-prefix", "foreign-session", "reversed-accepted-order", "nonminimal-partition", "missing-output", "confirmed-result", "missing-sighting", "successor-before-order"} {
		t.Run(kind, func(t *testing.T) {
			in := qualificationfix.New(t, "groups").Packing()
			want := "fail"
			switch kind {
			case "recorded-prefix":
				want = "pass"
			case "foreign-session":
				in.Snapshot.Tables["handovers"][0]["session_id"] = "another-session"
			case "reversed-accepted-order":
				in.Snapshot.Tables["messages"][2]["accepted_seq"] = int64(1)
			case "nonminimal-partition":
				in.Snapshot.Tables["prompts"][0]["message_count"] = int64(2)
			case "missing-output":
				in.Snapshot.Tables["prompts"][0]["transport"] = "pending"
				want = "not_completed"
			case "confirmed-result":
				in.Snapshot.Tables["messages"][0]["result_class"] = "delivered_confirmed"
			case "missing-sighting":
				in.Snapshot.Tables["prompts"][0]["admission_kind"] = nil
			case "successor-before-order":
				in.Snapshot.Tables["prompts"][1]["issued_at_ms"] = int64(1795022319518)
			}
			got := q.CheckPackingAndOrder(in)
			if got.SignalID != "packing-order" || got.Status != want {
				t.Fatalf("%+v; want %s", got, want)
			}
			if kind == "recorded-prefix" && (got.Observed != 3 || !reflect.DeepEqual(got.Evidence, in.References)) {
				t.Fatalf("wrong physical outputs/evidence: %+v", got)
			}
		})
	}
}

func TestQualificationTimingIntervals(t *testing.T) {
	f := qualificationfix.New(t, "groups")
	c := f.Capture()
	text := runner.Text(c.Owners[0].Snapshot.Tables["messages"][0], "text")
	start := time.UnixMilli(1795022318885).UTC()
	c.Manifest.Cases = []runner.Case{{ID: "unit-timing", InstanceID: c.Owners[0].Owner.InstanceID, Mode: "wait_turn", Text: text, Started: start, Ended: start.Add(20 * time.Millisecond)}}
	got := q.ExtractTimings(c)
	if len(got) != 1 {
		t.Fatalf("timings: %+v", got)
	}
	x := got[0]
	if !x.Whole || x.Complete.UnixMilli() != 1795022319519 || x.Sighting.UnixMilli() != 1795022334256 || x.Order.UnixMilli() != 1795022334256 || x.Issue.UnixMilli() != 1795022319516 || x.Complete.Sub(x.Send).Milliseconds() != 634 || x.Sighting.Sub(x.Complete).Milliseconds() != 14737 || x.Intake.UnixMilli() != 1795022334275 || x.Request != "00000000-0000-4000-8000-000000000106" || x.FirstResponseText.UnixMilli() != 1795022357101 || x.Intake.Sub(x.Send).Milliseconds() != 15390 || x.FirstResponseText.Sub(x.Intake).Milliseconds() != 22826 || x.FirstResponseText.Sub(x.Send).Milliseconds() != 38216 || x.ResponseMeaning != "first assistant text only; semantic acknowledgment and delivery confirmation are not inferred" {
		t.Fatalf("distinct timing facts lost: %+v", x)
	}
	// This request first emits thinking/tool activity, then receives tool
	// results in another request before its first text at the pinned time.
	toolSeen := false
	for _, row := range c.Owners[0].Transcript.Rows {
		if row.RequestRef == x.Request && row.Timestamp.UnixMilli() == 1795022336046 {
			toolSeen = true
		}
	}
	if !toolSeen {
		t.Fatal("recorded pre-text tool activity disappeared")
	}
	var withoutText []signals.TranscriptRow
	for _, row := range c.Owners[0].Transcript.Rows {
		texts, err := signals.Texts(row.Message)
		if row.Type != "assistant" || err != nil || len(texts) == 0 {
			withoutText = append(withoutText, row)
		}
	}
	c.Owners[0].Transcript.Rows = withoutText
	missingText := q.ExtractTimings(c)[0]
	if !missingText.FirstResponseText.IsZero() || missingText.Intake != x.Intake || missingText.Request != x.Request || !missingText.Whole {
		t.Fatalf("missing first text became another timing fact: %+v", missingText)
	}
	for _, pair := range []struct{ id, schedule string }{{"S1-S2", "tool-boundaries"}, {"O1-O2", "turn-ends"}, {"F3-F4", "forced-wakes"}} {
		a := q.Timing{Case: strings.Split(pair.id, "-")[0], Complete: start, Sighting: start.Add(time.Second), Intake: start.Add(2 * time.Second)}
		b := q.Timing{Case: strings.Split(pair.id, "-")[1], Complete: start.Add(3 * time.Second), Sighting: start.Add(5 * time.Second), Intake: start.Add(8 * time.Second)}
		g := q.NewGap(pair.id, pair.schedule, a, b)
		if g.ID != pair.id || g.Schedule != pair.schedule || *g.CompleteMS != 3000 || *g.SightingMS != 4000 || *g.IntakeMS != 6000 {
			t.Fatalf("gaps collapsed: %+v", g)
		}
		b.Sighting = time.Time{}
		if q.NewGap(pair.id, pair.schedule, a, b).SightingMS != nil {
			t.Fatal("missing sighting became zero latency")
		}
	}
}

func TestQualificationTypedPair(t *testing.T) {
	for _, state := range []string{"long-tool", "between-tools", "idle"} {
		for _, kind := range []string{"matched", "missing-boundary", "late-boundary", "changed-text", "partial-input", "negative-offset"} {
			t.Run(state+"/"+kind, func(t *testing.T) {
				p := q.TypedPair{State: state, TextAD: "controlled café\ntext", TextControl: "controlled café\ntext", ADTrigger: "ad-trigger", ControlTrigger: "native-trigger", ADBoundary: 1, ControlBoundary: 1, ADWhole: true, ControlWhole: true, ADOffset: time.Second, ControlOffset: 2 * time.Second, Evidence: []string{"raw/unit-pair.json"}}
				want := "fail"
				switch kind {
				case "matched":
					want = "pass"
				case "missing-boundary":
					p.ADBoundary = 0
					want = "not_completed"
				case "late-boundary":
					p.ADBoundary = 2
				case "changed-text":
					p.TextAD += "x"
				case "partial-input":
					p.ADWhole = false
				case "negative-offset":
					p.ADOffset = -1
				}
				got := q.CheckTypedPair(p)
				if got.Status != want || got.SignalID != "typed-"+state {
					t.Fatalf("%+v; want %s", got, want)
				}
			})
		}
	}
}

// Synthetic observations exercise rung arithmetic only, never a provider bound.
func TestQualificationNativeLadder(t *testing.T) {
	if got := q.CountText("é😀"); got != (q.Counts{Bytes: 6, Scalars: 2, UTF16: 3}) {
		t.Fatalf("unit measures: %+v", got)
	}
	for _, route := range []string{"wake", "post_tool_batch"} {
		kinds := []string{"bracket", "lower-bound", "missing-rung", "wrong-complete-size", "wrong-route", "nonmonotone", "no-next-request"}
		if route == "wake" {
			kinds = append(kinds, "missing-upward")
		} else {
			kinds = append(kinds, "one-batch-five-commands")
		}
		for _, kind := range kinds {
			t.Run(route+"/"+kind, func(t *testing.T) {
				rungs, err := q.NativeRungs(route)
				if err != nil {
					t.Fatal(err)
				}
				var samples []q.NativeSample
				for _, r := range rungs {
					samples = append(samples, q.NativeSample{Rung: r, Session: "unit", Receipt: "unit/" + r.ID, Transcript: "unit/journal", Whole: true, NextRequest: true, Emission: q.NativeEmission{Written: time.Unix(1, 0)}})
				}
				want := "pass"
				switch kind {
				case "bracket":
					for i := 3; i < len(samples); i++ {
						samples[i].Whole = false
					}
				case "missing-upward":
					if route == "wake" {
						samples = samples[:5]
						want = "not_completed"
					}
				case "missing-rung":
					samples = append(samples[:2], samples[3:]...)
					want = "not_completed"
				case "wrong-complete-size":
					samples[1].Rung.ProviderCounts.Bytes--
					want = "fail"
				case "wrong-route":
					samples[1].Rung.Route = "unrelated-route"
					want = "fail"
				case "nonmonotone":
					samples[2].Whole = false
					want = "fail"
				case "no-next-request":
					samples[1].NextRequest = false
					want = "not_completed"
				case "one-batch-five-commands":
					// R13-2: five commands in one completed parent batch are
					// one boundary, even if five sample labels name that output.
					for i := range samples {
						samples[i].Receipt = "unit/one-PostToolBatch"
					}
					want = "not_completed"
				}
				got := q.JudgeSizeLadder(route, samples)
				if got.Status != want {
					t.Fatalf("%+v; want %s", got, want)
				}
				if want == "pass" {
					b := got.Observed.(q.SizeBound)
					if kind == "bracket" {
						if b.WholeThrough != 9999 || b.FirstNonWhole != 10000 || b.LowerBound {
							t.Fatalf("dishonest bracket: %+v", b)
						}
					} else if !b.LowerBound || b.FirstNonWhole != 0 {
						t.Fatalf("false ceiling: %+v", b)
					}
				}
			})
		}
	}
}
