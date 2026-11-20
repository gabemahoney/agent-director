package qualification

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

// StandInMCP is the harmless setup server. It never reads user configuration,
// credentials or files, and records only requests to this owned test server.
func StandInMCP(root, run string, input io.Reader, output io.Writer) error {
	if run == "" || !strings.HasPrefix(os.Getenv("AGENT_DIRECTOR_INSTANCE_ID"), "live-"+run+"-") {
		return errors.New("MCP stand-in requires an owned qualification Launch")
	}
	dir, err := runner.Confined(root, "raw/mcp")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	scan := bufio.NewScanner(input)
	scan.Buffer(make([]byte, 4096), 1<<20)
	for scan.Scan() {
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  map[string]any  `json:"params"`
		}
		if err = json.Unmarshal(scan.Bytes(), &req); err != nil {
			return err
		}
		f, er := os.CreateTemp(dir, "request-*.json")
		if er != nil {
			return er
		}
		_, er = f.Write(append(append([]byte(nil), scan.Bytes()...), '\n'))
		er = errors.Join(er, f.Close())
		if er != nil {
			return er
		}
		if len(req.ID) == 0 {
			continue
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": req.Params["protocolVersion"], "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "qualification-stand-in", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "qualification_echo", "description": "Echo a harmless qualification message", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"message": map[string]string{"type": "string"}}, "required": []string{"message"}}}}}
		case "tools/call":
			result = map[string]any{"content": []any{map[string]string{"type": "text", "text": "QUALIFICATION-MCP-READY"}}}
		case "ping":
			result = map[string]any{}
		default:
			if err = json.NewEncoder(output).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "method unavailable"}}); err != nil {
				return err
			}
			continue
		}
		if err = json.NewEncoder(output).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}); err != nil {
			return err
		}
	}
	return scan.Err()
}

func setupFiles(e *Exercise, work string) error {
	r := e.Run
	mcp := map[string]any{"mcpServers": map[string]any{"qualification": map[string]any{"command": filepath.Join(r.Kit, "bin/claude-live"), "args": []string{"stand-in-mcp", "--evidence", r.Evidence.Root, "--run-id", r.Manifest.RunID}}}}
	if err := r.Evidence.JSON("raw/setup-mcp.json", mcp); err != nil {
		return err
	}
	body := "Call the qualification MCP echo tool with message READY. Then reply SETUP-READY in five words or fewer. Do not call any other tool."
	return r.Evidence.Put("work-setup-fresh/.claude/commands/qualification.md", []byte(body+"\n"))
}

// SetupUserHook is deliberately harmless and separate from the passive
// recorder: its receipts prove installer preservation without double-counting
// a parent event or copying unrelated user settings.
func SetupUserHook(root, run, event string, input io.Reader) error {
	if err := runner.ValidateHome(runner.TestHome); err != nil {
		return err
	}
	if !strings.HasPrefix(os.Getenv("AGENT_DIRECTOR_INSTANCE_ID"), "live-"+run+"-") {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(input, 1<<20))
	if err != nil {
		return err
	}
	var payload runner.Row
	if err = json.Unmarshal(data, &payload); err != nil {
		return err
	}
	if runner.Text(payload, "hook_event_name") != event {
		return errors.New("setup fixture event mismatch")
	}
	parent, err := runner.Confined(root, "raw/setup-user-hooks")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(parent, "invocation-")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, "input.json"), data, 0600); err != nil {
		return err
	}
	receipt, err := json.Marshal(map[string]any{"event": event, "run": run, "sha256": runner.Digest(data), "received": time.Now().UTC(), "behavior": "silent no-op"})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "receipt.json"), receipt, 0600)
}
