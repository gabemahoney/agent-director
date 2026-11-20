package qualification

import (
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
	"slices"
	"strings"
)

func CheckGroupSchedule(c Capture, plans []GroupPlan) runner.Result {
	missing := func(reason string) runner.Result {
		return result("group-schedule", "not_completed", reason, "S1/S2 in G0, then O1/Stop/O2", nil)
	}
	bad := func(reason string, observed any) runner.Result {
		return result("group-schedule", "fail", reason, "S1/S2 in G0, then O1/Stop/O2", observed)
	}
	if groupPartial(c, "steer_turn") || groupPartial(c, "wait_turn") {
		return missing("group admission closed before its complete held partition; actual starts retained separately")
	}
	g0, ok := c.caseByID("G0")
	if !ok {
		return missing("G0 corpus missing")
	}
	owner, ok := c.owner(g0.InstanceID, "")
	if !ok {
		return missing("G0 owner missing")
	}
	var posts []HookFact
	var stops []HookFact
	for _, h := range c.Hooks {
		if runner.Text(h.Payload, "session_id") != owner.Owner.SessionID || runner.Text(h.Payload, "agent_id") != "" || h.Receipt.Received.Before(g0.Started) {
			continue
		}
		if h.Receipt.Event == "PostToolBatch" {
			posts = append(posts, h)
		}
		if h.Receipt.Event == "Stop" {
			stops = append(stops, h)
		}
	}
	slices.SortFunc(posts, func(a, b HookFact) int { return a.Receipt.Received.Compare(b.Receipt.Received) })
	slices.SortFunc(stops, func(a, b HookFact) int { return a.Receipt.Received.Compare(b.Receipt.Received) })
	if len(posts) < 2 || len(stops) < 2 {
		return missing("G0 two parent boundaries and ordinary Stop progression missing")
	}
	if !posts[1].Receipt.Received.Before(stops[0].Receipt.Received) {
		return bad("second steer boundary is outside G0", posts[1])
	}
	var steer, ordinary []Timing
	timings := ExtractTimings(c)
	var checks []runner.Result
	for _, plan := range plans {
		for i, part := range plan.Parts {
			t, ok := timingByID(timings, plan.Cases[part.First])
			if !ok || !t.Whole {
				return missing("whole timed group intake missing")
			}
			if plan.Mode == "steer_turn" {
				if i >= 2 {
					return bad("unexpected extra steer prompt", plan)
				}
				o, p, h, err := c.prompt(plan.Texts[part.First])
				if err != nil {
					return errorResult("group-schedule", err)
				}
				body, err := PromptBody(o.Snapshot, p)
				if err != nil {
					return errorResult("group-schedule", err)
				}
				if runner.Text(h, "launch_token") != owner.Owner.LaunchToken || runner.Text(h, "boundary") != "tool_boundary" {
					return bad("steer bound outside G0 Launch/tool boundary", h)
				}
				checks = append(checks, CheckWholeInput(WholeInput{body, "steer_turn", posts[i].Receipt.Received.UnixMilli(), o.Transcript, []string{posts[i].Reference, o.SnapshotRef, o.TranscriptRef}}))
				steer = append(steer, t)
			} else {
				ordinary = append(ordinary, t)
			}
		}
	}
	if len(steer) != 2 || len(ordinary) != 2 {
		return missing("two steer and two ordinary prompts required")
	}
	if steer[1].Intake.After(ordinary[0].Issue) || steer[1].Complete.After(ordinary[0].Issue) || !steer[0].Intake.Before(steer[1].Intake) {
		return bad("S1/S2 did not finish before O1", []Timing{steer[0], steer[1], ordinary[0]})
	}
	for i, t := range ordinary {
		prior := steer[1]
		if i == 1 {
			prior = ordinary[0]
		}
		found := false
		for _, h := range stops {
			if !h.Receipt.Received.Before(prior.Intake) && h.Receipt.Received.UnixMilli() <= t.Issue.UnixMilli() {
				found = true
			}
		}
		if !found {
			return bad("ordinary prompt preceded its predecessor Stop", t)
		}
		if !strings.HasPrefix(t.Case, "O") {
			return bad("ordinary group relabelled", t)
		}
	}
	checks = append(checks, result("group-progression", "pass", "G0 consecutive boundaries, S2-before-O1 and O1/Stop/O2 established", nil, []Timing{steer[0], steer[1], ordinary[0], ordinary[1]}))
	return combine("group-schedule", checks...)
}
