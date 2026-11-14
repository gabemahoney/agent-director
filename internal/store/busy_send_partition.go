package store

import (
	"errors"
	"fmt"
)

// The inbox's prompt partition (SR-2.3): a frozen hand-over's Messages, in
// accepted order, packed into the fewest prompts that fit the route's complete
// prompt limit. Packing is pure: it reads no store, makes no provider, process
// or file call, and keeps no limit of its own (the capability table's mode
// limits, busy_send_capabilities.go, and the route's measure decide every fit).
//
// A prompt's body is each Message's separator line followed by its original
// text, with two LF bytes between Messages:
//
//	[agent-director: message 1 of 2]\n<text 1>\n\n[agent-director: message 2 of 2]\n<text 2>
//
// N and M are local to the prompt. Bodies are never split, truncated, escaped
// or joined with other input, and no caller identity appears in them. The body
// is reproduced from the Messages' retained text and the stored membership, so
// the store keeps no second copy of a payload.

// BusySendPromptMeasure returns the complete rendered size, in bytes, of a
// prompt whose body is body: the body plus everything the route's own wrapper
// adds, as the provider receives it. A route supplies it; it must be pure (no
// I/O), never return less than len(body), and never shrink when a Message is
// added to the body. nil measures the body plus the mode's whole wrapper
// budget (BusySendModeLimits.WrapperBytes).
type BusySendPromptMeasure func(body []byte) int64

// BusySendPromptPart is one prompt of a partition: Count Messages starting at
// First (indexes into the packed texts) and the prompt's complete measured size.
type BusySendPromptPart struct {
	First, Count  int
	RenderedBytes int64
}

// ErrBusySendPromptUnfit means a Message cannot fit a prompt alone under the
// mode's prompt limit with the route's measured wrapper: the route's wrapper
// exceeds the table's wrapper budget, or the table changed under accepted
// custody. Nothing is claimed; the Message stays Held.
var ErrBusySendPromptUnfit = errors.New("store: busy-send Message does not fit a prompt alone")

// errBusySendMeasure reports a route measure that breaks its contract.
var errBusySendMeasure = errors.New("store: busy-send route measure smaller than the prompt body")

// RenderBusySendPromptBody renders the body of one prompt carrying texts, in
// order (SR-2.3). It is the one renderer: the partitioner measures what it
// renders, and a route issues what it renders from the frozen membership.
func RenderBusySendPromptBody(texts [][]byte) []byte {
	size := busySendBodyBytes(texts, len(texts))
	body := make([]byte, 0, size)
	for i, t := range texts {
		if i > 0 {
			body = append(body, '\n', '\n')
		}
		body = append(body, BusySendSeparator(i+1, len(texts))...)
		body = append(body, t...)
	}
	return body
}

// busySendBodyBytes is the length of the body RenderBusySendPromptBody makes
// for the first n of texts, computed without rendering it.
func busySendBodyBytes(texts [][]byte, n int) int {
	size := 2 * (n - 1)
	for i := 0; i < n; i++ {
		size += len(BusySendSeparator(i+1, n)) + len(texts[i])
	}
	return size
}

// PartitionBusySendPrompts packs texts, in accepted order, into the fewest
// prompts whose complete measured size fits limits.PromptBytes: each prompt is
// the longest prefix of the remaining whole Messages that fits, its size
// measured on its actual rendering, so changing separator widths and the
// route's wrapper are counted. A Message that cannot fit alone is
// ErrBusySendPromptUnfit; a measure that breaks its contract is an error.
func PartitionBusySendPrompts(texts [][]byte, limits BusySendModeLimits, measure BusySendPromptMeasure) ([]BusySendPromptPart, error) {
	if measure == nil {
		measure = func(body []byte) int64 { return int64(len(body)) + limits.WrapperBytes }
	}
	fit := func(part [][]byte) (int64, bool, error) {
		if int64(busySendBodyBytes(part, len(part))) > limits.PromptBytes {
			return 0, false, nil
		}
		body := RenderBusySendPromptBody(part)
		size := measure(body)
		if size < int64(len(body)) {
			return 0, false, errBusySendMeasure
		}
		return size, size <= limits.PromptBytes, nil
	}
	var parts []BusySendPromptPart
	for first := 0; first < len(texts); {
		part := BusySendPromptPart{First: first}
		for count := 1; first+count <= len(texts); count++ {
			size, ok, err := fit(texts[first : first+count])
			if err != nil {
				return nil, err
			}
			if !ok {
				break
			}
			part.Count, part.RenderedBytes = count, size
		}
		if part.Count == 0 {
			return nil, fmt.Errorf("%w: Message %d of %d, %d text bytes, prompt limit %d", ErrBusySendPromptUnfit,
				first+1, len(texts), len(texts[first]), limits.PromptBytes)
		}
		parts = append(parts, part)
		first += part.Count
	}
	return parts, nil
}
