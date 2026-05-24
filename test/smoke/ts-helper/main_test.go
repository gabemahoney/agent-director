//go:build helper

package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// TestDispatch covers the four required behaviours:
//
//	(a) success path:   single-line JSON on stdout, empty stderr.
//	(b) missing flag:   non-zero exit, message on stderr, empty stdout.
//	(c) unknown subcmd: non-zero exit, available subcommands listed in stderr.
//	(d) json-schema:    well-formed JSON with an entry per subcommand.
func TestDispatch(t *testing.T) {
	t.Run("a_success_seed_empty_store", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "test.db")
		var stdout, stderr bytes.Buffer
		code := dispatch([]string{"seed-empty-store", "--store", dbPath}, &stdout, &stderr)

		if code != 0 {
			t.Fatalf("expected exit 0, got %d; stderr: %s", code, stderr.String())
		}
		if stderr.Len() != 0 {
			t.Fatalf("expected empty stderr, got: %s", stderr.String())
		}
		line := stdout.String()
		if strings.Count(line, "\n") != 1 {
			t.Fatalf("expected exactly one newline in stdout (single-line JSON), got: %q", line)
		}
		var result map[string]string
		if err := json.Unmarshal([]byte(strings.TrimRight(line, "\n")), &result); err != nil {
			t.Fatalf("stdout is not valid JSON: %v; stdout: %q", err, line)
		}
		if result["path"] == "" {
			t.Fatalf("expected non-empty 'path' field, got: %v", result)
		}
	})

	t.Run("a_success_seed_spawn", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "spawn.db")
		var stdout, stderr bytes.Buffer
		code := dispatch([]string{
			"seed-spawn",
			"--store", dbPath,
			"--state", "waiting",
			"--create-store",
		}, &stdout, &stderr)

		if code != 0 {
			t.Fatalf("expected exit 0, got %d; stderr: %s", code, stderr.String())
		}
		if stderr.Len() != 0 {
			t.Fatalf("expected empty stderr, got: %s", stderr.String())
		}
		line := stdout.String()
		if strings.Count(line, "\n") != 1 {
			t.Fatalf("expected single-line JSON, got: %q", line)
		}
		var result map[string]string
		if err := json.Unmarshal([]byte(strings.TrimRight(line, "\n")), &result); err != nil {
			t.Fatalf("stdout not valid JSON: %v", err)
		}
		if result["claude_instance_id"] == "" {
			t.Fatalf("expected non-empty claude_instance_id, got %v", result)
		}
	})

	t.Run("a_success_seed_parent_child", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "pc.db")

		// Pre-create parent and child rows.
		var dummy bytes.Buffer
		if code := dispatch([]string{
			"seed-spawn", "--store", dbPath,
			"--state", "waiting", "--id", "parent-aaa", "--create-store",
		}, &dummy, &dummy); code != 0 {
			t.Fatalf("setup seed-spawn (parent) failed")
		}
		if code := dispatch([]string{
			"seed-spawn", "--store", dbPath,
			"--state", "waiting", "--id", "child-bbb",
		}, &dummy, &dummy); code != 0 {
			t.Fatalf("setup seed-spawn (child) failed")
		}

		var stdout, stderr bytes.Buffer
		code := dispatch([]string{
			"seed-parent-child",
			"--store", dbPath,
			"--parent-id", "parent-aaa",
			"--child-id", "child-bbb",
		}, &stdout, &stderr)

		if code != 0 {
			t.Fatalf("expected exit 0, got %d; stderr: %s", code, stderr.String())
		}
		if stderr.Len() != 0 {
			t.Fatalf("expected empty stderr, got: %s", stderr.String())
		}
		var result map[string]string
		if err := json.Unmarshal([]byte(strings.TrimRight(stdout.String(), "\n")), &result); err != nil {
			t.Fatalf("stdout not valid JSON: %v", err)
		}
		if result["parent_id"] != "parent-aaa" || result["child_id"] != "child-bbb" {
			t.Fatalf("unexpected result fields: %v", result)
		}
	})

	t.Run("a_success_seed_permission_request", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "perm.db")

		var dummy bytes.Buffer
		if code := dispatch([]string{
			"seed-spawn", "--store", dbPath,
			"--state", "check_permission", "--id", "perm-spawn-1", "--create-store",
		}, &dummy, &dummy); code != 0 {
			t.Fatalf("setup seed-spawn failed")
		}

		var stdout, stderr bytes.Buffer
		code := dispatch([]string{
			"seed-permission-request",
			"--store", dbPath,
			"--spawn-id", "perm-spawn-1",
			"--tool", "Bash",
		}, &stdout, &stderr)

		if code != 0 {
			t.Fatalf("expected exit 0, got %d; stderr: %s", code, stderr.String())
		}
		if stderr.Len() != 0 {
			t.Fatalf("expected empty stderr, got: %s", stderr.String())
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(strings.TrimRight(stdout.String(), "\n")), &result); err != nil {
			t.Fatalf("stdout not valid JSON: %v", err)
		}
		if _, ok := result["request_id"]; !ok {
			t.Fatalf("expected request_id in result, got %v", result)
		}
	})

	t.Run("a_success_seed_template", func(t *testing.T) {
		dir := t.TempDir()
		var stdout, stderr bytes.Buffer
		code := dispatch([]string{
			"seed-template",
			"--templates-dir", dir,
			"--name", "my-tmpl",
			"--body", `cwd = "/tmp"`,
		}, &stdout, &stderr)

		if code != 0 {
			t.Fatalf("expected exit 0, got %d; stderr: %s", code, stderr.String())
		}
		if stderr.Len() != 0 {
			t.Fatalf("expected empty stderr, got: %s", stderr.String())
		}
		var result map[string]string
		if err := json.Unmarshal([]byte(strings.TrimRight(stdout.String(), "\n")), &result); err != nil {
			t.Fatalf("stdout not valid JSON: %v", err)
		}
		if result["path"] == "" {
			t.Fatalf("expected non-empty path, got %v", result)
		}
	})

	t.Run("b_missing_flag_seed_spawn", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		// --store omitted intentionally.
		code := dispatch([]string{"seed-spawn", "--state", "waiting"}, &stdout, &stderr)

		if code == 0 {
			t.Fatal("expected non-zero exit for missing --store")
		}
		if stdout.Len() != 0 {
			t.Fatalf("expected empty stdout on error, got: %s", stdout.String())
		}
		if stderr.Len() == 0 {
			t.Fatal("expected error message on stderr")
		}
	})

	t.Run("b_missing_flag_seed_parent_child", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := dispatch([]string{"seed-parent-child", "--store", "/tmp/x.db"}, &stdout, &stderr)

		if code == 0 {
			t.Fatal("expected non-zero exit for missing --parent-id and --child-id")
		}
		if stdout.Len() != 0 {
			t.Fatalf("expected empty stdout on error, got: %s", stdout.String())
		}
		if stderr.Len() == 0 {
			t.Fatal("expected error message on stderr")
		}
	})

	t.Run("c_unknown_subcommand", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := dispatch([]string{"no-such-cmd"}, &stdout, &stderr)

		if code == 0 {
			t.Fatal("expected non-zero exit for unknown subcommand")
		}
		if stdout.Len() != 0 {
			t.Fatalf("expected empty stdout for unknown subcommand, got: %s", stdout.String())
		}
		errMsg := stderr.String()
		// All available subcommands should be named in the error output.
		for _, sub := range availableSubcmds {
			if !strings.Contains(errMsg, sub) {
				t.Errorf("expected stderr to mention subcommand %q; stderr: %s", sub, errMsg)
			}
		}
	})

	t.Run("c_no_subcommand", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := dispatch([]string{}, &stdout, &stderr)

		if code == 0 {
			t.Fatal("expected non-zero exit with no subcommand")
		}
		if stdout.Len() != 0 {
			t.Fatalf("expected empty stdout, got: %s", stdout.String())
		}
	})

	t.Run("d_json_schema", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := dispatch([]string{"json-schema"}, &stdout, &stderr)

		if code != 0 {
			t.Fatalf("expected exit 0, got %d; stderr: %s", code, stderr.String())
		}
		if stderr.Len() != 0 {
			t.Fatalf("expected empty stderr, got: %s", stderr.String())
		}
		line := stdout.String()
		if strings.Count(line, "\n") != 1 {
			t.Fatalf("expected single-line JSON, got: %q", line)
		}

		var schema map[string]map[string]string
		if err := json.Unmarshal([]byte(strings.TrimRight(line, "\n")), &schema); err != nil {
			t.Fatalf("json-schema output is not valid JSON: %v; stdout: %q", err, line)
		}

		// Every subcommand except json-schema itself must appear.
		wantKeys := []string{
			"seed-spawn",
			"seed-parent-child",
			"seed-permission-request",
			"seed-template",
			"seed-empty-store",
		}
		for _, k := range wantKeys {
			if _, ok := schema[k]; !ok {
				t.Errorf("json-schema missing entry for %q", k)
			}
		}
	})
}
