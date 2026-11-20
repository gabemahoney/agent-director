package qualification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

// CandidateKinds reads the exact packaged launch switch and string constants.
// It has no separate list whose removal/support claims can drift from AD.
func CandidateKinds(launch, constants []byte) ([]string, error) {
	fs := token.NewFileSet()
	names, err := parser.ParseFile(fs, "spawns.go", constants, 0)
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, decl := range names.Decls {
		g, ok := decl.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, spec := range g.Specs {
			v, ok := spec.(*ast.ValueSpec)
			if !ok || len(v.Names) != 1 || len(v.Values) != 1 {
				continue
			}
			literal, ok := v.Values[0].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				continue
			}
			value, er := strconv.Unquote(literal.Value)
			if er == nil {
				values[v.Names[0].Name] = value
			}
		}
	}
	file, err := parser.ParseFile(fs, "launch.go", launch, 0)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, decl := range file.Decls {
		f, ok := decl.(*ast.FuncDecl)
		if !ok || f.Name.Name != "launchAgentError" || f.Body == nil {
			continue
		}
		ast.Inspect(f.Body, func(node ast.Node) bool {
			clause, ok := node.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, expr := range clause.List {
				selector, ok := expr.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				pkg, ok := selector.X.(*ast.Ident)
				if !ok || pkg.Name != "store" {
					continue
				}
				value, ok := values[selector.Sel.Name]
				if !ok {
					continue
				}
				if !slices.Contains(out, value) {
					out = append(out, value)
				}
			}
			return true
		})
	}
	if len(out) == 0 || !slices.Contains(out, "claude") {
		return nil, errors.New("candidate launch-kind proof is absent or unrecognized")
	}
	return out, nil
}

type UncoveredAgent struct {
	Kind                string          `json:"kind"`
	ExecutablePresent   bool            `json:"executable_present"`
	Authentication      string          `json:"authentication"`
	Usability           string          `json:"usability"`
	Reason              string          `json:"reason"`
	Instance            string          `json:"instance,omitempty"`
	Calls               []UncoveredCall `json:"calls"`
	RequestedOtherTurns int             `json:"requested_other_turns"`
	NativeProof         bool            `json:"native_proof"`
	Exited              bool            `json:"exited"`
}
type UncoveredCall struct {
	Case     string         `json:"case"`
	Expected string         `json:"expected"`
	Command  runner.Command `json:"command"`
}

func Uncovered(ctx context.Context, e *Exercise) (err error) {
	r := e.Run
	launch, err := runner.Read(r.Kit, "qualification/launch.go")
	if err != nil {
		return err
	}
	constants, err := runner.Read(r.Kit, "qualification/spawns.go")
	if err != nil {
		return err
	}
	kinds, err := CandidateKinds(launch, constants)
	if err != nil {
		return err
	}
	if err = r.Evidence.JSON("raw/uncovered-kinds.json", kinds); err != nil {
		return err
	}
	var agents []UncoveredAgent
	defer func() { err = errors.Join(err, r.Evidence.JSON("raw/uncovered-agents.json", agents)) }()
	for _, kind := range kinds {
		if kind == "claude" {
			continue
		}
		item := UncoveredAgent{Kind: kind, Authentication: "unknown", Usability: "unknown"}
		executable := kind
		if kind == "cursor" {
			executable = "cursor-agent"
		}
		if path, er := exec.LookPath(executable); er == nil {
			item.ExecutablePresent = true
			var args []string
			switch kind {
			case "codex":
				args = []string{path, "login", "status"}
			case "cursor":
				args = []string{path, "status"}
			case "opencode":
				args = []string{path, "auth", "list"}
			}
			if len(args) > 0 {
				c, _ := r.Commands.RunStatus(ctx, "uncovered-"+kind+"-auth-status", 10*time.Second, args...)
				item.Calls = append(item.Calls, UncoveredCall{"auth-status", "safe status only; no login automation", c})
				if c.ExitCode == 0 {
					item.Authentication = "credentials_reported"
				}
			}
		}
		// Installation or a credential listing does not prove usable sign-in.
		// The owned native ordinary-send path below must provide its own outcome.
		agentErr := e.uncoveredAgent(ctx, &item)
		agents = append(agents, item)
		if agentErr != nil {
			return agentErr
		}
	}
	return r.Evidence.JSON("raw/import-requests.json", e.Options)
}

func (e *Exercise) uncoveredAgent(ctx context.Context, item *UncoveredAgent) error {
	r := e.Run
	kind := item.Kind
	id := "live-" + r.Manifest.RunID + "-uncovered-" + kind
	work := filepath.Join(r.Evidence.Root, "work-uncovered-"+kind)
	if err := os.Mkdir(work, 0700); err != nil {
		return err
	}
	item.Instance = id
	c, b, spawnErr := r.AD(ctx, "uncovered-"+kind+"-spawn", "spawn", "--agent", kind, "--cwd", work, "--instance-id", id, "--tmux-session-name", id)
	item.Calls = append(item.Calls, UncoveredCall{"plain-spawn", "native/pane behavior; no busy-send default", c})
	if spawnErr != nil {
		item.Reason = "owned launch unavailable: inspect exact public error; installation alone grants no usability"
		if er := r.Evidence.Put("raw/uncovered-"+kind+"-unavailable.json", b); er != nil {
			return er
		}
		snapshot, er := r.Observe(ctx, id)
		if er != nil {
			return er
		}
		if len(snapshot.Tables["spawns"]) > 0 {
			r.Manifest.Owned = append(r.Manifest.Owned, runner.Owned{Agent: kind, InstanceID: id, CWD: work, Socket: r.Socket, Started: c.Started})
			if er = r.BindOwned(snapshot); er != nil {
				return er
			}
			return errors.New("uncovered spawn returned an error with a surviving owned row; stop for canonical cleanup and inspection")
		}
		item.Exited = true
		return nil
	}
	r.Manifest.Owned = append(r.Manifest.Owned, runner.Owned{Agent: kind, InstanceID: id, CWD: work, Socket: r.Socket, Started: c.Started})
	// Other providers can have no Claude session_id; use their own durable
	// process identity, and do not parse a screen or start a Claude helper.
	_, readyErr := r.AwaitState(ctx, id, 65*time.Second, true, func(s runner.Snapshot) bool {
		if len(s.Tables["spawns"]) != 1 {
			return false
		}
		row := s.Tables["spawns"][0]
		return runner.Text(row, "state") != "pending" && runner.Number(row, "pid") > 0 && runner.Text(row, "proc_starttime") != ""
	})
	if readyErr != nil {
		item.Reason = "native startup/sign-in usability not established within the owned bounded observation"
		if err := e.stop(ctx, id, "uncovered-"+kind+"-unavailable-end", false); err != nil {
			return err
		}
		item.Exited = true
		return nil
	}
	c, b, err := r.AD(ctx, "uncovered-"+kind+"-get", "get", "--instance-id", id)
	if err != nil {
		return err
	}
	item.Calls = append(item.Calls, UncoveredCall{"get-coverage", "busy_send not_covered", c})
	var get struct {
		BusySend string `json:"busy_send"`
	}
	if err = json.Unmarshal(b, &get); err != nil {
		return err
	}
	if get.BusySend != "not_covered" {
		return errors.New("uncovered candidate kind unexpectedly reports covered; stop for MI")
	}
	// Every explicitly present busy-send send choice, including false, must
	// refuse without silently typing. These calls have no ordinary input.
	if _, err = e.observe(ctx, id, "uncovered-"+kind+"-before-refusals"); err != nil {
		return err
	}
	for i, option := range [][]string{{"--send-mode", "wait_turn"}, {"--send-mode", "steer_turn"}, {"--confirmed-delivery=false"}, {"--caller-uuid", r.Manifest.RunID}, {"--force=false"}, {"--force"}} {
		args := []string{"send-keys", "--instance-id", id, "--text", "Harmless refusal probe; do not submit."}
		args = append(args, option...)
		call, _, _ := r.AD(ctx, fmt.Sprintf("uncovered-%s-send-refusal-%d", kind, i), args...)
		item.Calls = append(item.Calls, UncoveredCall{fmt.Sprintf("busy-choice-%d", i), "ErrCantSend/agent_not_covered; no input", call})
		if call.ExitCode == 0 {
			return errors.New("uncovered busy choice unexpectedly succeeded; no further input")
		}
	}

	for _, verb := range []string{"force", "conditional-pause"} {
		args := []string{"force-delivery", "--instance-id", id}
		expected := "force_result refused/agent_not_covered"
		if verb == "conditional-pause" {
			args = []string{"pause", "--instance-id", id, "--only-if-waiting"}
			expected = "ErrSpawnNotPausable/cause=agent_not_covered"
		}
		call, data, callErr := r.AD(ctx, "uncovered-"+kind+"-"+verb, args...)
		item.Calls = append(item.Calls, UncoveredCall{verb, expected, call})
		if verb == "force" {
			var output struct {
				Force *api.ForceResult `json:"force_result"`
			}
			if callErr != nil || json.Unmarshal(data, &output) != nil || output.Force == nil || output.Force.ForceID == "" || output.Force.Outcome != "refused" || output.Force.Reason != "agent_not_covered" {
				return errors.New("uncovered force violated its structured refusal contract")
			}
		} else if callErr == nil {
			return errors.New("uncovered conditional pause unexpectedly succeeded")
		}
	}
	if _, err = e.observe(ctx, id, "uncovered-"+kind+"-after-refusals"); err != nil {
		return err
	}
	requestID := "uncovered-" + kind + "-ordinary"
	c, b, err = r.AD(ctx, requestID, "send-keys", "--instance-id", id, "--text", "Reply with at most five words: qualification native compatibility.", "--delivery-request-id", requestID)
	item.RequestedOtherTurns = 1
	item.Calls = append(item.Calls, UncoveredCall{"native-ordinary", "legacy/native request ID meaning; provider proof when offered", c})
	var sent api.SendKeysResult
	_ = json.Unmarshal(b, &sent)
	item.NativeProof = err == nil && sameNativeReceipt(sent, sent, requestID)
	if item.NativeProof {
		item.Usability = "native_input_confirmed"
	} else {
		item.Reason = "ordinary native/pane request retains its actual outcome; signed-in model usability not proven by installation or pane dispatch"
	}
	if item.NativeProof {
		repeat, repeatData, repeatErr := r.AD(ctx, requestID+"-repeat", "send-keys", "--instance-id", id, "--text", "Reply with at most five words: qualification native compatibility.", "--delivery-request-id", requestID)
		item.Calls = append(item.Calls, UncoveredCall{"native-id-repeat", "same native request, no replay", repeat})
		var firstValue, repeatValue api.SendKeysResult
		if repeatErr != nil || json.Unmarshal(b, &firstValue) != nil || json.Unmarshal(repeatData, &repeatValue) != nil || !sameNativeReceipt(firstValue, repeatValue, requestID) {
			return errors.New("native repeat changed or failed its original proof; no retry")
		}
	}

	// Escape is the explicit harmless raw-key compatibility control on this
	// owned session. No dialog answer, text-driven menu choice or token guessed.
	c, b, err = r.AD(ctx, "uncovered-"+kind+"-raw-Escape", "send-keys", "--instance-id", id, "--key", "Escape")
	item.Calls = append(item.Calls, UncoveredCall{"raw-key", "legacy raw-key path retained", c})
	var raw map[string]any
	if err != nil || json.Unmarshal(b, &raw) != nil || raw == nil || len(raw) != 0 {
		return errors.New("native raw Escape failed its legacy empty-object contract")
	}
	if err = e.stop(ctx, id, "uncovered-"+kind+"-end", false); err != nil {
		return err
	}
	item.Exited = true
	for i, option := range [][]string{{"--send-mode", "wait_turn"}, {"--confirmed-delivery=false"}, {"--confirm-from-transcripts=false"}} {
		for _, verb := range []string{"resume", "reuse", "template", "spawn"} {
			var args []string
			switch verb {
			case "resume":
				args = []string{"resume", "--instance-id", id}
			case "reuse":
				args = []string{"spawn", "--reuse-finished", "--instance-id", id, "--agent", kind, "--cwd", work}
			case "template":
				args = []string{"make-template", "--name", fmt.Sprintf("live-%s-%s-%d", r.Manifest.RunID, kind, i), "--agent", kind}
			case "spawn":
				args = []string{"spawn", "--instance-id", fmt.Sprintf("%s-refused-%d", id, i), "--agent", kind, "--cwd", work}
			}
			args = append(args, option...)
			call, _, _ := r.AD(ctx, fmt.Sprintf("uncovered-%s-%s-default-%d", kind, verb, i), args...)
			item.Calls = append(item.Calls, UncoveredCall{fmt.Sprintf("%s-default-%d", verb, i), "ErrCantSend/agent_not_covered; no mutation", call})
			if call.ExitCode == 0 {
				if verb == "resume" || verb == "reuse" || verb == "spawn" {
					attemptedID := id
					if verb == "spawn" {
						attemptedID = fmt.Sprintf("%s-refused-%d", id, i)
					}
					r.Manifest.Owned = append(r.Manifest.Owned, runner.Owned{Agent: kind, InstanceID: attemptedID, CWD: work, Socket: r.Socket, Started: call.Started})
				}
				return errors.New("uncovered default unexpectedly succeeded; inspect owned artifact before continuing")
			}
		}
	}
	_, err = e.observe(ctx, id, "uncovered-"+kind+"-finished")
	return err
}
