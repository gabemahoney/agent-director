package driverscripts_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// b.ftd E7 Task2: a diagnostic projection must remain useful without leaking
// raw protocol content, even for malformed/oversized and missing child files.
func TestCursorHelpDiagnosticProjection(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	trace := `{"id":"acp-1","method":"session/prompt","params":{"prompt":"PRIVATE_PROMPT","mcpServers":[{"env":"PRIVATE_ENV"}]}}` + "\n"
	trace += strings.Repeat(`{"id":"acp-many","result":{"text":"PRIVATE_TOOL_RESULT"}}`+"\n", 1200)
	trace += "malformed PRIVATE_MALFORMED\n"
	for name, data := range map[string]string{
		"trace.jsonl":                trace,
		"mcp.jsonl":                  `{"event":"response","response":{"id":"mcp-1","result":{"instructions":"PRIVATE_HELP"}}}` + "\n",
		"child-before-settings.json": `{"extra_env":"PRIVATE_SETTINGS"}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	helper := driverScript(t, "cursor-callers-help-diagnostics.cjs")
	script := `const d=require(process.argv[1]); process.stdout.write(d.capture({dir:process.argv[2],store:process.argv[2]+'/gone.db',own:'co-test',socket:process.argv[2]+'/gone.sock',stage:'child.before-ready',registered:'after'}));`
	r := run(t, nil, "", "node", "-e", script, helper, dir)
	if r.code != 0 {
		t.Fatal(r)
	}
	if len(r.stdout) > 98304 {
		t.Fatalf("unbounded output: %d", len(r.stdout))
	}
	for _, s := range []string{"PRIVATE_PROMPT", "PRIVATE_ENV", "PRIVATE_TOOL_RESULT", "PRIVATE_HELP", "PRIVATE_SETTINGS", "PRIVATE_MALFORMED"} {
		if strings.Contains(r.stdout+r.stderr, s) {
			t.Errorf("leaked %s", s)
		}
	}
	for _, s := range []string{`"truncated":true`, `"code":"MALFORMED_JSON"`, `"code":"ENOENT"`, `"child":"child-before-co-test"`, `"category":"startup-binding"`, `"category":"protected-transcript-tail"`, `"category":"pane-process"`, `"incomplete":true`, `"sha256"`} {
		if !strings.Contains(r.stdout, s) {
			t.Errorf("missing %s: %s", s, r.stdout)
		}
	}
	if !strings.Contains(r.stdout, `"child":"child-after-co-test"`) {
		t.Error("unpersisted registered child was lost")
	}
	for _, line := range strings.Split(strings.TrimSpace(r.stdout), "\n") {
		if !json.Valid([]byte(line)) {
			t.Errorf("invalid metadata JSON: %s", line)
		}
	}
}

// b.ftd E7 Task2: use the actual runner and actual exit functions so the
// original exit, emission ordering, cleanup and silent PASS are observable.
func TestCursorHelpFailureReachesRunner(t *testing.T) {
	t.Parallel()
	source, err := os.ReadFile(driverScript(t, "cursor-callers-help.sh"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(source)
	start := strings.Index(body, "ch_exit() {")
	if start < 0 {
		t.Fatal("missing exit functions")
	}
	end := strings.Index(body[start:], "trap ch_exit EXIT") + start
	if start < 0 || end < start {
		t.Fatal("missing exit functions")
	}
	for _, tc := range []struct {
		name, exit, export, cleanup, helper string
		want                                []string
		quiet                               bool
	}{
		{name: "original failure", exit: "37", want: []string{"exit=37", "emission=before-cleanup", "HELP_CASE_EXIT original=37 evidence=0 cleanup=0"}},
		{name: "diagnostics fail preserve original", exit: "37", helper: "return 19", want: []string{"exit=37", "collector_exit=19", "HELP_CASE_EXIT original=37 evidence=0 cleanup=0"}},
		{name: "export only failure", exit: "0", export: "return 23", want: []string{"exit=23", "emission=before-cleanup", "HELP_CASE_EXIT original=0 evidence=23 cleanup=0"}},
		{name: "cleanup only failure", exit: "0", cleanup: "return 29", want: []string{"exit=29", "emission=deferred-after-cleanup", "HELP_CASE_EXIT original=0 evidence=0 cleanup=29"}},
		{name: "passing case", exit: "0", quiet: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner, root := shellPlan(t)
			state := t.TempDir()
			export, cleanup, helper := tc.export, tc.cleanup, tc.helper
			if export == "" {
				export = ":"
			}
			if cleanup == "" {
				cleanup = ":"
			}
			if helper == "" {
				helper = `printf '{"category":"ACP-trace","snapshot":"before-cleanup"}\n'`
			}
			script := `set -euo pipefail
case_dir="$M.dir"; mkdir -p "$case_dir"
id=co-unit; STORE="$case_dir/store"; socket="$case_dir/socket"; SOURCE_DIR="$case_dir/source"; check_id=child.before-ready
ch_peer_socket=''; ch_known_children=()
timeout() { ` + helper + `; }
ch_export() { ` + export + `; }
cleanup_cursor_case() { printf 'INDEPENDENT_CLEANUP_DONE\n'; ` + cleanup + `; }
ch_peer_evidence() { :; }
` + body[start:end] + `
trap ch_exit EXIT
exit ` + tc.exit + "\n"
			plan := "---\nid: t2.p\n---\n```bash\n" + script + "```\n"
			if err := os.WriteFile(filepath.Join(root, "plan", "t2.p", "t2.p.md"), []byte(plan), 0600); err != nil {
				t.Fatal(err)
			}
			r := run(t, []string{"EPIC=plan", "TESTPLAN_ROOT=" + root, "DRIVER_MODE=shell", "M=" + filepath.Join(state, "m"), "RESETS=" + filepath.Join(state, "resets")}, "", "bash", runner)
			got := r.stdout + r.stderr
			if tc.quiet {
				if r.code != 0 || strings.Contains(got, "HELP_FAILURE_METADATA_BEGIN") {
					t.Fatalf("PASS diagnostics or wrong exit: %s", r)
				}
				return
			}
			if r.code != 1 {
				t.Fatalf("runner verdict changed: %s", r)
			}
			for _, s := range tc.want {
				if !strings.Contains(got, s) {
					t.Errorf("missing %s: %s", s, r)
				}
			}
			emitted, cleaned := strings.Index(r.stderr, "HELP_FAILURE_METADATA_BEGIN"), strings.Index(r.stderr, "INDEPENDENT_CLEANUP_DONE")
			if emitted < 0 || cleaned < 0 || (tc.cleanup == "" && emitted > cleaned) || (tc.cleanup != "" && emitted < cleaned) {
				t.Errorf("incorrect evidence/cleanup order: %s", r.stderr)
			}
		})
	}
}

// b.ftd: exercise actual output saturation, including a child registered before
// settings/rows exist, and refuse a fixture symlink before reading its target.
func TestCursorHelpDiagnosticBudgets(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	script := `const fs=require('fs'),cp=require('child_process'),path=require('path');
 const dir=process.argv[2], d=require(process.argv[1]);
 const event=Object.fromEntries(['id','method','kind','event','attempt','control','pid','start','code','signal','instance_id','session_id','life_number','launch_token','connection_id','stop_reason'].map(k=>[k,'x'.repeat(192)]));
 event.params={prompt:'PRIVATE_CONTENT'};
 fs.mkdirSync(dir+'/home');
 const raw=(JSON.stringify(event)+'\n').repeat(8);
 for(const prefix of ['', 'child-cli-','child-configured-','child-before-','child-after-','peer-'])for(const suffix of ['trace.jsonl','mcp.jsonl'])fs.writeFileSync(dir+'/'+prefix+suffix,raw);
 fs.writeFileSync(dir+'/home/transcript',raw);
 cp.spawnSync=(exe,args)=>({status:0,stdout:exe==='tmux'?'':JSON.stringify(args.at(-1).includes('FROM spawns')?[{jsonl_path:dir+'/home/transcript'}]:[])});
 const output=d.capture({dir,store:dir+'/db',own:'co-bounds',socket:dir+'/sock',stage:'unit-budget',registered:'cli configured before after',peerSocket:dir+'/peer.sock'});
 const records=output.trim().split('\n').map(JSON.parse), sizes={};
 for(const row of records)sizes[row.child]=(sizes[row.child]||0)+Buffer.byteLength(JSON.stringify(row)+'\n');
 if(Buffer.byteLength(output)>d.LIMIT.bytes || Object.values(sizes).some(n=>n>d.LIMIT.childBytes))throw Error('output budget exceeded '+JSON.stringify(sizes));
 if(!records.some(row=>row.omitted_categories?.length) || !records.at(-1).incomplete)throw Error('missing explicit omission summary');
 if(output.includes('PRIVATE_CONTENT'))throw Error('payload leak');
 fs.unlinkSync(dir+'/trace.jsonl');fs.symlinkSync(dir+'/home/transcript',dir+'/trace.jsonl');
 const guarded=d.capture({dir,store:dir+'/db',own:'co-link',socket:dir+'/sock',stage:'symlink'});
 if(!guarded.includes('"code":"SYMLINK"'))throw Error('symlink read accepted');
 const f=d.frame({id:'rpc-1',method:'session/prompt',params:{prompt:'PRIVATE_CONTENT'},response:{id:'rpc-1',error:{code:-32000,message:'PRIVATE_CONTENT'}}});
 if(f.id!=='rpc-1' || f.error_code!==-32000 || f.params.bytes!==28 || !/^[a-f0-9]{64}$/.test(f.params.sha256) || JSON.stringify(f).includes('PRIVATE_CONTENT'))throw Error('projection lost correlation or leaked');
 console.log(JSON.stringify({bytes:Buffer.byteLength(output),sizes,incomplete:true,symlink:'refused'}));`
	r := run(t, nil, "", "node", "-e", script, driverScript(t, "cursor-callers-help-diagnostics.cjs"), dir)
	if r.code != 0 {
		t.Fatal(r)
	}
	t.Log(strings.TrimSpace(r.stdout))
}
