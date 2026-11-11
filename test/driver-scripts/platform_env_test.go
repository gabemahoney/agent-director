package driverscripts_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// platformEnvFields is every field of the Environment Record
// (test/driver/platform-env.sh, SRD SR-4.2).
var platformEnvFields = []string{
	"debian_version", "os_id", "os_version_id", "os_version_codename",
	"dpkg_architecture", "uname_machine", "kernel_release",
	"tmux_version", "sqlite3_version", "bash_version", "git_version",
	"node_version", "go_version", "bun_version",
}

// platformEnv checks r is one exit-0 Environment Record line, a JSON object
// whose only key platform_env holds exactly platformEnvFields, each a string
// or null, and returns those fields (nil for null).
func platformEnv(t *testing.T, r result) map[string]*string {
	t.Helper()
	if r.code != 0 || strings.Count(r.stdout, "\n") != 1 || !strings.HasSuffix(r.stdout, "\n") {
		t.Fatalf("want exit 0 and exactly one newline-terminated stdout line: %s", r)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(r.stdout), &top); err != nil || len(top) != 1 || top["platform_env"] == nil {
		t.Fatalf("want a JSON object whose only key is platform_env (%v): %s", err, r)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(top["platform_env"], &raw); err != nil {
		t.Fatalf("platform_env is not an object (%v): %s", err, r)
	}
	var got []string
	for k := range raw {
		got = append(got, k)
	}
	sort.Strings(got)
	want := append([]string(nil), platformEnvFields...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("platform_env fields %v, want exactly %v", got, want)
	}
	fields := map[string]*string{}
	for k, v := range raw {
		if string(v) == "null" {
			fields[k] = nil
			continue
		}
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			t.Fatalf("%s is %s, want a JSON string or null", k, v)
		}
		fields[k] = &s
	}
	return fields
}

// show renders a record's fields for failure messages.
func show(fields map[string]*string) string {
	var b strings.Builder
	for _, k := range platformEnvFields {
		if v := fields[k]; v == nil {
			fmt.Fprintf(&b, " %s=null", k)
		} else {
			fmt.Fprintf(&b, " %s=%q", k, *v)
		}
	}
	return b.String()
}

// sandboxRecord is the in-tree script's record with the image's real PATH.
func sandboxRecord(t *testing.T) map[string]*string {
	t.Helper()
	return platformEnv(t, run(t, nil, "", driverScript(t, "platform-env.sh")))
}

// TestPlatformEnvRecordsTheSandbox: in the Debian 12 sandbox image the record
// names Debian bookworm, and every other field matches its own source (each
// tool's first version line), so the test passes on amd64 and arm64.
func TestPlatformEnvRecordsTheSandbox(t *testing.T) {
	fields := sandboxRecord(t)
	firstLine := func(name string, args ...string) string {
		r := run(t, nil, "", name, args...)
		if r.code != 0 {
			t.Fatalf("%s: %s", name, r)
		}
		return strings.TrimSpace(strings.SplitN(r.stdout, "\n", 2)[0])
	}
	debianVersion, err := os.ReadFile("/etc/debian_version")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"os_id":               "debian",
		"os_version_id":       "12",
		"os_version_codename": "bookworm",
		"debian_version":      strings.TrimSpace(string(debianVersion)),
		"dpkg_architecture":   firstLine("dpkg", "--print-architecture"),
		"uname_machine":       firstLine("uname", "-m"),
		"kernel_release":      firstLine("uname", "-r"),
		"tmux_version":        firstLine("tmux", "-V"),
		"sqlite3_version":     firstLine("sqlite3", "-version"),
		"bash_version":        firstLine("bash", "--version"),
		"git_version":         firstLine("git", "--version"),
		"node_version":        firstLine("node", "--version"),
		"go_version":          firstLine("go", "version"),
		"bun_version":         firstLine("bun", "--version"),
	}
	for k, w := range want {
		if v := fields[k]; v == nil || *v != w {
			t.Errorf("%s: want %q:%s", k, w, show(fields))
		}
	}
}

// TestPlatformEnvAbsentTool: with one listed tool missing from PATH its
// fields are null, the rest match the full-PATH record, and exit is 0.
func TestPlatformEnvAbsentTool(t *testing.T) {
	full := sandboxRecord(t)
	tools := map[string][]string{
		"dpkg":    {"dpkg_architecture"},
		"uname":   {"uname_machine", "kernel_release"},
		"tmux":    {"tmux_version"},
		"sqlite3": {"sqlite3_version"},
		"git":     {"git_version"},
		"node":    {"node_version"},
		"go":      {"go_version"},
		"bun":     {"bun_version"},
	}
	for absent, nulled := range tools {
		t.Run(absent, func(t *testing.T) {
			// bash runs the scripts (#!/usr/bin/env bash) and dirname locates sql.sh.
			bin := t.TempDir()
			for _, tool := range []string{"bash", "dirname", "dpkg", "uname", "tmux", "sqlite3", "git", "node", "go", "bun"} {
				if tool == absent {
					continue
				}
				target, err := exec.LookPath(tool)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(bin, tool)); err != nil {
					t.Fatal(err)
				}
			}
			fields := platformEnv(t, run(t, []string{"PATH=" + bin}, "", driverScript(t, "platform-env.sh")))
			want := map[string]*string{}
			for k, v := range full {
				want[k] = v
			}
			for _, k := range nulled {
				want[k] = nil
			}
			if !reflect.DeepEqual(fields, want) {
				t.Errorf("without %s:\ngot: %s\nwant:%s", absent, show(fields), show(want))
			}
		})
	}
}

// TestPlatformEnvVersionLineVerbatim: a version is the program's first
// stdout line, trimmed and otherwise unparsed, JSON-escaped by the script's
// bash encoder; a program that exits non-zero records null.
func TestPlatformEnvVersionLineVerbatim(t *testing.T) {
	full := sandboxRecord(t)
	stubs := t.TempDir()
	for name, body := range map[string]string{
		"git":  "printf '  git \"q\" back\\\\slash\\ttab \\033[0m caf\\303\\251  \\nsecond line\\n'",
		"node": "echo v99.0.0; exit 1",
	} {
		if err := os.WriteFile(filepath.Join(stubs, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fields := platformEnv(t, run(t, []string{"PATH=" + stubs + ":" + os.Getenv("PATH")}, "", driverScript(t, "platform-env.sh")))
	want := map[string]*string{}
	for k, v := range full {
		want[k] = v
	}
	git := "git \"q\" back\\slash\ttab \x1b[0m café"
	want["git_version"], want["node_version"] = &git, nil
	if !reflect.DeepEqual(fields, want) {
		t.Errorf("with a stub git and a failing node:\ngot: %s\nwant:%s", show(fields), show(want))
	}
}

// TestPlatformEnvWithoutSQLSh: a copy of the script alone records
// sqlite3_version null and says on stderr to copy sql.sh with it; doing
// exactly that restores the full record (literal-follow of the note).
func TestPlatformEnvWithoutSQLSh(t *testing.T) {
	full := sandboxRecord(t)
	dir := t.TempDir()
	copyTo := func(name string) string {
		b, err := os.ReadFile(driverScript(t, name))
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(dir, name)
		if err := os.WriteFile(dst, b, 0o755); err != nil {
			t.Fatal(err)
		}
		return dst
	}
	script := copyTo("platform-env.sh")

	r := run(t, nil, "", script)
	fields := platformEnv(t, r)
	want := map[string]*string{}
	for k, v := range full {
		want[k] = v
	}
	want["sqlite3_version"] = nil
	if !reflect.DeepEqual(fields, want) {
		t.Errorf("without sql.sh:\ngot: %s\nwant:%s", show(fields), show(want))
	}
	note := "platform-env: no sql.sh beside this script in " + dir + ", so sqlite3_version is null; copy sql.sh with platform-env.sh\n"
	if r.stderr != note {
		t.Errorf("stderr %q, want the note %q", r.stderr, note)
	}

	copyTo("sql.sh")
	r = run(t, nil, "", script)
	if fields := platformEnv(t, r); !reflect.DeepEqual(fields, full) || r.stderr != "" {
		t.Errorf("after copying sql.sh beside it, want the in-tree record and no stderr:\ngot: %s\nwant:%s\nstderr %q", show(fields), show(full), r.stderr)
	}
}
