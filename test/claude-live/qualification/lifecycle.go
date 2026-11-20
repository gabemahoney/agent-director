package qualification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

type LostClientPlan struct {
	Root     string `json:"root"`
	Run      string `json:"run"`
	Instance string `json:"instance"`
	Case     string `json:"case"`
	Text     string `json:"text"`
}
type LostClientReceipt struct {
	PID          int       `json:"pid"`
	ProcessStart string    `json:"process_start"`
	Pair         string    `json:"pair"`
	Accepted     time.Time `json:"accepted"`
}

// LostReplyClient is a real public Go caller. Acceptance finishes and the
// store handle closes, then this caller withholds its response to its own
// parent. Killing it exercises a lost client reply without delaying AD or
// touching a transaction, callback or live store row.
func LostReplyClient(planPath string) error {
	if err := runner.ValidateHome(runner.TestHome); err != nil {
		return err
	}
	b, err := runner.Read(filepath.Dir(planPath), filepath.Base(planPath))
	if err != nil {
		return err
	}
	var p LostClientPlan
	if err = json.Unmarshal(b, &p); err != nil {
		return err
	}
	if filepath.Dir(p.Root) != runner.VMRoot || filepath.Dir(planPath) != filepath.Join(p.Root, "raw") || !strings.HasPrefix(p.Instance, "live-"+p.Run+"-") || p.Case != "L2" {
		return errors.New("lost-reply client plan outside owned scope")
	}
	client, err := api.New(api.Options{Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		return err
	}
	mode, confirmed := "wait_turn", true
	_, sendErr := client.SendKeysContext(context.Background(), api.SendKeysParams{InstanceID: p.Instance, Text: p.Text, SendMode: &mode, ConfirmedDelivery: &confirmed, CallerUUID: &p.Run, DeliveryRequestID: p.Case})
	closeErr := client.Close()
	if err = errors.Join(sendErr, closeErr); err != nil {
		return err
	}
	start, alive, known := probe.NewProcChecker().StartTime(os.Getpid())
	if !alive || !known || start == "" {
		return errors.New("client process identity unavailable")
	}
	rec := LostClientReceipt{PID: os.Getpid(), ProcessStart: start, Pair: p.Case, Accepted: time.Now().UTC()}
	b, err = json.Marshal(rec)
	if err != nil {
		return err
	}
	tmp, err := runner.Confined(p.Root, "raw/lost-client-accepted.tmp")
	if err != nil {
		return err
	}
	if err = os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	if err = os.Rename(tmp, filepath.Join(p.Root, "raw/lost-client-accepted.json")); err != nil {
		return err
	}
	timer := time.NewTimer(60 * time.Second)
	defer timer.Stop()
	<-timer.C
	return errors.New("client was not interrupted within the planned lost-reply window")
}

func (e *Exercise) lostReply(ctx context.Context, iid, text string) error {
	r := e.Run
	if err := e.reserve("L2"); err != nil {
		return err
	}
	plan := LostClientPlan{r.Evidence.Root, r.Manifest.RunID, iid, "L2", text}
	ref := "raw/lost-client-plan.json"
	if err := r.Evidence.JSON(ref, plan); err != nil {
		return err
	}
	started := r.Now().UTC()
	r.Manifest.Cases = append(r.Manifest.Cases, runner.Case{ID: "L2", Text: text, Mode: "wait_turn", InstanceID: iid, RequestID: "L2", Started: started, Command: ref, Ready: true})
	childCtx, cancel := context.WithCancel(ctx)
	type finished struct {
		command runner.Command
		err     error
	}
	done := make(chan finished, 1)
	go func() {
		c, _, err := r.Commands.Run(childCtx, "lost-reply-client", 75*time.Second, filepath.Join(r.Kit, "bin/claude-live"), "qualification-lost-client", "--client-plan", filepath.Join(r.Evidence.Root, ref))
		done <- finished{c, err}
	}()
	joined := false
	defer func() {
		cancel()
		if !joined {
			<-done
		}
	}()
	var receipt LostClientReceipt
	err := r.AwaitEvidence(ctx, 20*time.Second, func(context.Context) (bool, error) {
		select {
		case f := <-done:
			joined = true
			return false, fmt.Errorf("client ended before interruption: %w", f.err)
		default:
		}
		b, er := runner.Read(r.Evidence.Root, "raw/lost-client-accepted.json")
		if os.IsNotExist(er) {
			return false, nil
		}
		if er != nil {
			return false, er
		}
		if er = json.Unmarshal(b, &receipt); er != nil {
			return false, er
		}
		return receipt.PID > 0 && receipt.ProcessStart != "", nil
	})
	if err != nil {
		return err
	}
	s, err := e.observe(ctx, iid, "lost-client-durable-acceptance")
	if err != nil {
		return err
	}
	found := 0
	for _, m := range s.Tables["messages"] {
		if runner.Text(m, "text") == text {
			found++
		}
	}
	if found != 1 {
		return errors.New("lost-reply acceptance lacks exactly one stored owner")
	}
	gone, err := runner.ProcessGone(receipt.PID, receipt.ProcessStart)
	if err != nil || gone {
		return errors.Join(err, errors.New("client already exited; lost reply was not exercised"))
	}
	if _, _, err = r.Commands.Run(ctx, "kill-owned-client-only", 5*time.Second, "kill", "-KILL", strconv.Itoa(receipt.PID)); err != nil {
		return err
	}
	f := <-done
	joined = true
	if f.command.ExitCode == 0 {
		return errors.New("client was not interrupted")
	}
	if err = r.AwaitEvidence(ctx, 5*time.Second, func(context.Context) (bool, error) { return runner.ProcessGone(receipt.PID, receipt.ProcessStart) }); err != nil {
		return err
	}
	if err = r.Evidence.JSON("raw/lost-client-interrupted.json", map[string]any{"receipt": receipt, "command": f.command, "response_lost": true, "agent_not_killed": true}); err != nil {
		return err
	}
	if err = e.sendPair(ctx, iid, "L2-repeat-before-crash", "L2", text, "wait_turn", true, false); err != nil {
		return err
	}
	_, err = e.lookup(ctx, "L2", "L2-lookup-before-crash")
	return err
}

func Lifecycle(ctx context.Context, e *Exercise) error {
	r := e.Run
	release := filepath.Join(r.Evidence.Root, "work-L1", "release-initial-never")
	iid, err := e.spawn(ctx, "L1", "This local timing fixture waits for a file controlled by the test runner in this run directory. Run this exact single foreground Bash call, without background work: `"+controlledTool(release)+"`. Finish briefly only after it exits.")
	if err != nil {
		return err
	}
	if _, err = e.wait(ctx, iid, ready); err != nil {
		return err
	}
	if err = e.capability(ctx, iid); err != nil {
		return err
	}
	if _, err = e.toolWindow(ctx, iid, 1, false); err != nil {
		return err
	}
	second := filepath.Join(r.Evidence.Root, "work-L1", "release-resumed-never")
	text := "L2: This local timing fixture waits for a file controlled by the test runner. After resume run this exact single foreground Bash call: `" + controlledTool(second) + "`. Then finish briefly. No background work."
	if err = e.lostReply(ctx, iid, text); err != nil {
		return err
	}
	if err = e.readOnlyWindow(ctx, iid, "lifecycle-busy-held"); err != nil {
		return err
	}
	if err = e.stop(ctx, iid, "L1-crash-owned-agent", false); err != nil {
		return err
	}
	if err = e.resume(ctx, iid, "L2-resume", false); err != nil {
		return err
	}
	// Two independent public callers overlap the real SessionStart/ordinary
	// callback. Both refer to the original pair; no third parent start is spent.
	forceDone := make(chan error, 1)
	go func() { forceDone <- e.forceCommand(ctx, iid, "L2-concurrent-force") }()
	repeatErr := e.sendPair(ctx, iid, "L2-repeat-during-resume", "L2", text, "wait_turn", true, false)
	forceErr := <-forceDone
	if err = errors.Join(repeatErr, forceErr); err != nil {
		return err
	}
	if _, err = e.wait(ctx, iid, ready); err != nil {
		return err
	}
	if _, err = e.toolWindow(ctx, iid, 2, false); err != nil {
		return err
	}
	if _, err = e.observe(ctx, iid, "L2-resumed-custody"); err != nil {
		return err
	}
	if _, err = e.lookup(ctx, "L2", "L2-lookup-after-resume"); err != nil {
		return err
	}
	if err = e.send(ctx, iid, "lifecycle-final-held", "Retain this named never-issued Message for fresh reuse.", "wait_turn", true, false); err != nil {
		return err
	}
	if err = e.readOnlyWindow(ctx, iid, "lifecycle-final-held"); err != nil {
		return err
	}
	if err = e.stop(ctx, iid, "L2-end-before-boundary", false); err != nil {
		return err
	}
	if err = e.resume(ctx, iid, "lifecycle-fresh-reuse", true); err != nil {
		return err
	}
	if _, err = e.wait(ctx, iid, ready); err != nil {
		return err
	}
	if _, err = e.observe(ctx, iid, "lifecycle-fresh-reuse-final"); err != nil {
		return err
	}
	if _, err = e.lookup(ctx, "lifecycle-final-held", "lifecycle-fresh-final-lookup"); err != nil {
		return err
	}
	return r.Evidence.JSON("raw/import-requests.json", e.Options)
}

// forceCommand avoids concurrently mutating the Exercise slice; Commands is
// the existing synchronized subprocess seam and retains all timing/output.
func (e *Exercise) forceCommand(ctx context.Context, iid, id string) error {
	_, _, err := e.Run.AD(ctx, id, "force-delivery", "--instance-id", iid)
	return err
}

func (e *Exercise) readOnlyWindow(ctx context.Context, iid, id string) error {
	if _, err := e.observe(ctx, iid, id+"-reads-before"); err != nil {
		return err
	}
	for i, args := range [][]string{{"get", "--instance-id", iid}, {"status", "--instance-id", iid}, {"list", "--tmux-session-name", iid}, {"get-delivery", "--caller-uuid", e.Run.Manifest.RunID, "--delivery-request-id", "L2"}, {"sweep"}} {
		if _, _, err := e.Run.AD(ctx, fmt.Sprintf("%s-read-%d", id, i), args...); err != nil {
			return err
		}
	}
	_, err := e.observe(ctx, iid, id+"-reads-after")
	return err
}
