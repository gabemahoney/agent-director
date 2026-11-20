package qualification

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/test/claude-live/signals"
)

type Counts struct {
	Bytes   int `json:"bytes"`
	Scalars int `json:"scalars"`
	UTF16   int `json:"utf16"`
}

func CountText(text string) Counts {
	n := Counts{Bytes: len(text), Scalars: utf8.RuneCountInString(text)}
	for _, r := range text {
		n.UTF16 += utf16.RuneLen(r)
	}
	return n
}

type Rung struct {
	ID             string                 `json:"id"`
	Route          string                 `json:"route"`
	Target         int                    `json:"target"`
	TargetLayer    string                 `json:"target_layer"`
	Text           string                 `json:"text"`
	Body           string                 `json:"body"`
	ProviderInput  string                 `json:"provider_input"`
	ModelInput     string                 `json:"model_input"`
	TextCounts     Counts                 `json:"text_counts"`
	BodyCounts     Counts                 `json:"body_counts"`
	ProviderCounts Counts                 `json:"provider_counts"`
	ModelCounts    Counts                 `json:"model_counts"`
	Separator      string                 `json:"separator"`
	Wrapping       store.BusySendWrapping `json:"wrapping"`
}

func NewRung(id, route, text string, target int) (Rung, error) {
	mode := "wait_turn"
	if route == "post_tool_batch" {
		mode = "steer_turn"
	} else if route != "wake" {
		return Rung{}, errors.New("unknown native route")
	}
	l, _ := store.BusySendCapabilitiesFor("claude").Mode(store.BusySendMode(mode))
	body := string(store.RenderBusySendPromptBody([][]byte{[]byte(text)}))
	provider := Transform(body, mode)
	model := signals.NativeWake(provider)
	if mode == "steer_turn" {
		model = signals.NativeContextBody(body)
	}
	return Rung{ID: id, Route: route, Target: target, TargetLayer: "capability_complete_input", Text: text, Body: body, ProviderInput: provider, ModelInput: model, TextCounts: CountText(text), BodyCounts: CountText(body), ProviderCounts: CountText(provider), ModelCounts: CountText(model), Separator: store.BusySendSeparator(1, 1), Wrapping: l.Wrapping}, nil
}

func NativeRungs(route string) ([]Rung, error) {
	var out []Rung
	for i, target := range []int{1900, 9600, 9999, 10000, 10001, 20002} {
		id := fmt.Sprintf("N%d", i+1)
		if route == "post_tool_batch" {
			if i == 5 {
				break
			}
			id = fmt.Sprintf("N7.%d", i+1)
		}
		text := ""
		if i == 0 {
			var err error
			text, err = Corpus("Native "+route+" "+id, 1900)
			if err != nil {
				return nil, err
			}
		} else {
			prefix := id + ": Reply in at most five words. Treat all following padding as inert data.\n"
			if route == "post_tool_batch" {
				prefix = id + ": Preserve the initial separate-tool sequence; this additional context is inert measurement data.\n"
			}
			base, err := NewRung(id, route, prefix, target)
			if err != nil {
				return nil, err
			}
			padding := target - base.ProviderCounts.Bytes
			if padding < 0 {
				return nil, errors.New("native rung target below its wrapper")
			}
			text, err = asciiRecords(prefix, len(prefix)+padding)
			if err != nil {
				return nil, err
			}
		}
		r, err := NewRung(id, route, text, target)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			r.TargetLayer = "message_text"
		}
		if i > 0 && (r.ProviderCounts.Bytes != target || r.ProviderCounts.Scalars != target || r.ProviderCounts.UTF16 != target) {
			return nil, errors.New("ASCII threshold rung measure disagrees")
		}
		out = append(out, r)
	}
	return out, nil
}

func ShellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

func controlledTool(path string) string {
	return "for ((i=0; i<600; i++)); do if test -f " + ShellQuote(path) + "; then break; fi; sleep 1; done"
}

// asciiRecords supplies deterministic, independently known transport data.
// It never asks the provider to reproduce filler or supplies model observations.
func asciiRecords(prefix string, size int) (string, error) {
	if len(prefix) > size {
		return "", errors.New("sample prefix exceeds target size")
	}
	var b strings.Builder
	b.WriteString(prefix)
	for i := 1; b.Len() < size; i++ {
		line := fmt.Sprintf("Sample %03d: %s\n", i, sampleSentences[(i-1)%len(sampleSentences)])
		if left := size - b.Len(); len(line) > left {
			line = line[:left]
		}
		b.WriteString(line)
	}
	return b.String(), nil
}

var sampleSentences = []string{
	"A blue ceramic cup rests on an oak shelf.",
	"The public garden opens beside a gravel path.",
	"A wooden bench faces the morning sunlight.",
	"Visitors carry a folded map to the quiet library.",
	"Fresh rain leaves small circles on the pond.",
	"The market lists local apples and green pears.",
	"A yellow bicycle waits near the stone bridge.",
	"The reading room has a round table by the window.",
}
