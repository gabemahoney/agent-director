package qualification

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
	"github.com/gabemahoney/agent-director/test/claude-live/signals"
)

// RefusalEvidence identifies the active operation, not a delivery confirmation.
// Native diagnostic fields remain in the indexed transcript; no prose heuristic
// or provider error-string alias grants input, admission or order evidence.
type RefusalEvidence struct {
	Instance    string                    `json:"instance"`
	Launch      string                    `json:"launch"`
	Session     string                    `json:"session"`
	Case        string                    `json:"case"`
	WaitingCase string                    `json:"waiting_case"`
	Markers     []signals.ProviderRefusal `json:"markers"`
}

type ProviderRefusalError struct{ Facts RefusalEvidence }

func (e *ProviderRefusalError) Error() string {
	return fmt.Sprintf("provider_refusal: instance %s case %s while awaiting %s", e.Facts.Instance, e.Facts.Case, e.Facts.WaitingCase)
}

func providerRefusalResults(c Capture) []runner.Result {
	var out []runner.Result
	for _, owner := range c.Owners {
		o := owner.Owner
		if o.SessionID == "" || o.LaunchToken == "" || (o.Agent != "" && o.Agent != "claude") || owner.TranscriptError != nil {
			continue
		}
		var cases []runner.Case
		for _, x := range c.Manifest.Cases {
			if x.InstanceID == o.InstanceID && !x.Started.IsZero() && !x.Started.Before(o.Started) && (o.StoppedAt.IsZero() || x.Started.Before(o.StoppedAt)) {
				cases = append(cases, x)
			}
		}
		slices.SortStableFunc(cases, func(a, b runner.Case) int { return a.Started.Compare(b.Started) })
		for i, x := range cases {
			until := o.StoppedAt
			if i+1 < len(cases) && (until.IsZero() || cases[i+1].Started.Before(until)) {
				until = cases[i+1].Started
			}
			markers := owner.Transcript.ProviderRefusals(x.Started, until)
			if len(markers) == 0 {
				continue
			}
			waiting := x.ID
			for _, ob := range c.Observations {
				if ob.Instance == o.InstanceID && ob.Kind == "wait" && !ob.Started.Before(x.Started) && !ob.Started.After(markers[0].Time) {
					waiting = ob.Case
				}
			}
			facts := RefusalEvidence{Instance: o.InstanceID, Launch: o.LaunchToken, Session: o.SessionID, Case: x.ID, WaitingCase: waiting, Markers: markers}
			out = append(out, result("provider_refusal", "not_completed", (&ProviderRefusalError{facts}).Error(), "provider progresses without refusal", facts, owner.TranscriptRef))
		}
	}
	return out
}

func (e *Exercise) checkProviderProgress(ctx context.Context, iid string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.Run.RefreshTranscriptBindings(ctx); err != nil {
		return err
	}
	o, err := e.owner(iid)
	if err != nil {
		return err
	}
	if o.SessionID == "" || o.LaunchToken == "" || (o.Agent != "" && o.Agent != "claude") || o.Exited {
		return nil
	}
	b, err := e.Run.ReadTranscript(o)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	// A live append can leave a partial last row; sealed analysis remains strict.
	n := bytes.LastIndexByte(b, '\n')
	if n < 0 {
		return nil
	}
	b = b[:n+1]
	t, err := signals.ParseSessionTranscript(b, o.SessionID)
	if err != nil {
		return err
	}
	ref := "raw/" + e.Run.OwnedPrefix(o) + "-transcript.jsonl"
	c := Capture{Manifest: e.Run.Manifest, Observations: e.Observations, Owners: []OwnerEvidence{{Owner: o, Transcript: t, TranscriptRef: ref}}}
	rows := providerRefusalResults(c)
	if len(rows) == 0 {
		return nil
	}
	if err = e.Run.Evidence.Put(ref, b); err != nil {
		return err
	}
	for _, row := range rows {
		facts := row.Observed.(RefusalEvidence)
		found := false
		for _, prior := range e.Run.Manifest.Problems {
			if old, ok := prior.Observed.(RefusalEvidence); ok && old.Instance == facts.Instance && old.Launch == facts.Launch && old.Case == facts.Case {
				found = true
			}
		}
		if !found {
			e.Run.Manifest.Problems = append(e.Run.Manifest.Problems, row)
		}
	}
	return &ProviderRefusalError{rows[0].Observed.(RefusalEvidence)}
}

func (e *Exercise) awaitProgress(ctx context.Context, iid string, limit time.Duration, dialog bool, predicate func(runner.Snapshot) bool) (runner.Snapshot, error) {
	s, err := e.Run.AwaitStateChecked(ctx, iid, limit, dialog, func(ctx context.Context, s runner.Snapshot) error {
		if err := e.checkProviderProgress(ctx, iid); err != nil {
			return err
		}
		return e.fallback(s)
	}, predicate)
	if err != nil && strings.Contains(err.Error(), "unexpected dialog") {
		return s, e.workload("precondition-not-established", iid, "", "parent entered an unrelated question/Dialog; no answer or further workload input sent")
	}
	return s, err
}

func (e *Exercise) waitForCase(ctx context.Context, iid, id, state string, body func() error) error {
	at := len(e.Observations)
	e.Observations = append(e.Observations, Observation{Case: id, Instance: iid, Kind: "wait", State: state, Started: e.Run.Now().UTC()})
	err := body()
	e.Observations[at].Ended = e.Run.Now().UTC()
	return err
}

// Only a provably untouched suffix after a bound typed-controls refusal is
// incomplete. Attempted, conflicting and unexpected starts keep failing.
func refusalBlockedSuffix(c Capture, ids []string) map[string]runner.Result {
	blocked := map[string]runner.Result{}
	if c.Manifest.Phase != "typed-controls" {
		return blocked
	}
	for _, p := range c.Manifest.Problems {
		if p.Status == "fail" {
			return blocked
		}
	}
	seen := map[string]bool{}
	for _, ch := range c.After.Charges {
		if ch.Attempt != c.After.Identity.Attempt || ch.Phase != "typed-controls" {
			continue
		}
		if seen[ch.ID] || strings.HasPrefix(ch.ID, "unexpected-") {
			return blocked
		}
		seen[ch.ID] = true
	}
	first := len(ids)
	for i, id := range ids {
		if requirePhysicalCase(c, id, "typed-controls") != nil {
			first = i
			break
		}
	}
	if first == 0 || first == len(ids) {
		return blocked
	}
	for _, id := range ids[first:] {
		for _, x := range c.Manifest.Cases {
			if x.ID == id {
				return blocked
			}
		}
		for _, ch := range c.After.Charges {
			if ch.ID == id && ch.Attempt == c.After.Identity.Attempt {
				return blocked
			}
		}
		for _, ob := range c.Observations {
			if ob.Case == id && (ob.Kind == "send" || ob.Kind == "typed-control" || ob.Kind == "startup") {
				return blocked
			}
		}
		for _, cmd := range c.Commands {
			if cmd.Name == id || cmd.Name == id+"-enter" {
				return blocked
			}
		}
	}
	rows := providerRefusalResults(c)
	if len(rows) == 0 {
		return blocked
	}
	// Every issued prefix must precede the refusal that stopped the suffix.
	cause := rows[0]
	facts := cause.Observed.(RefusalEvidence)
	for _, id := range ids[:first] {
		x, ok := c.caseByID(id)
		if !ok || x.Started.After(facts.Markers[0].Time) {
			return blocked
		}
	}
	for _, id := range ids[first:] {
		blocked[id] = result("physical/"+id, "not_completed", "not attempted after provider_refusal", "one planned parent start", map[string]any{"blocked_by": facts}, cause.Evidence...)
	}
	return blocked
}
