import fs from 'node:fs';
import crypto from 'node:crypto';
import {execFileSync} from 'node:child_process';
const base='tickets/handoff/b.ftd/Plans/b.jf1/e3-task3';
const out=base+'/final-tests';
const sha=p=>crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex');
const write=(p,v)=>fs.writeFileSync(out+'/'+p,JSON.stringify(v,null,2)+'\n');
const read=p=>JSON.parse(fs.readFileSync(p));
const paths=[...new Set(execFileSync('git',['ls-files','-co','--exclude-standard','-z'],{encoding:'utf8'}).split('\0').filter(p=>p&&!p.split('/').includes('.claude')&&!p.startsWith('tickets/handoff/')&&fs.existsSync(p)&&fs.statSync(p).isFile()))].sort();
const current=Object.fromEntries(paths.map(p=>[p,sha(p)]));
const own=['internal/store/advice_follow_decision_test.go','internal/store/deadline_boundary_test.go','pkg/api/decide_absolute_deadline_test.go','internal/store/relay_writes_test.go','internal/store/cursor_rotation_test.go'];
const before=read(out+'/input-manifest-before.json');
const changes=[...new Set([...Object.keys(before),...paths])].filter(p=>before[p]!==current[p]);
if(changes.some(p=>!own.includes(p)))throw Error('Unexpected input drift: '+JSON.stringify(changes));
const sources=read(base+'/deadline-review-fix/FINAL-report.json').source_after_sha256;
for(const [p,h]of Object.entries(sources))if(current[p]!==h)throw Error('Source drift '+p);
if(current['pkg/api/decide_trail_deadline_test.go']!=='032950e86d9919e2c48ae79189751e36f8875312a78bf1410973954fdde86d88')throw Error('Protected regression drift');
if(current['docs/architecture.md']!=='9593447ca9fe0640268803d42c07f5cbe9e5865be306c2a9b91388307ca7b509')throw Error('Architecture drift');
const phase=process.argv[2];
write('input-manifest-'+phase+'.json',current);
write('integrity-'+phase+'.json',{file_count:paths.length,source_sha256:sources,protected_regression:current['pkg/api/decide_trail_deadline_test.go'],architecture:current['docs/architecture.md'],changes_from_entry:changes,own_test_sha256:Object.fromEntries(own.map(p=>[p,current[p]])),head:execFileSync('git',['rev-parse','HEAD'],{encoding:'utf8'}).trim()});
if(phase==='pre-full'){
 write('quality-review.json',{
  guide:'docs/test-writing-guide.md: Pre-Submit Checklist and Red Flags',
  method:'All Task3 changed source diffs/new source, deadline/store/trail/API/public tests, collector helpers/five cases/coverage/operator instructions and full named reports reviewed as one coverage set. Existing Task1/2 owners preserved and independently hash-bound; no claim of a fresh manual review of every unchanged test.',
  findings:[{id:'Q1',disposition:'closed by advice work',issue:'Store COMMIT-response phase test alone did not execute public recovery advice.',resolution:'External store_test public Decide/GetPermission composition reuses internal Store test-only real SQLite driver boundary; no production seam. Real COMMIT succeeds then response expires, actual classifier returns ErrInternal and exact inspection phrase, fresh independent public reader sees allow, two reads preserve facts, exactly one verdict UPDATE/COMMIT. Distinct API recovery boundary complements SQL phase owner.'}],
  mandatory_unresolved:[],
  ownership:{
   causal_deadline:'pkg/api/decide_trail_deadline_test.go retained byte-identically; old exact64a RED/new GREEN with unchanged 500ms/1.5s assertions',
   sql_cleanup:'internal/store/deadline_boundary_test.go owns phase matrix/setup context classification/safe reuse-discard/quarantine/original deadline/zero wait',
   audit:'internal/trail/deadline_test.go owns 1 active+64 queue/rejection/status/FIFO/drain/failure; API Client test owns deferred audit return and commit/called order',
   public:'Two cursor_pane_controls files parameterize four genuine callers and representative distinct faults. Task1 controls and Task2 exhaustive rotation matrices are retained, not copied.',
   shipped:'Five authored collector compositions add installed Bun tarball/build/declaration/consumer evidence. They are static authored only.',
   authenticated:'O1-O6 and actual human/authenticated runtime predicates remain pending; synthetic received bytes/frames/source-derived questions never relabeled authenticated.'
  },
  checklist:{parameterization:'Five SQL phases, three setup errors, four callers, three controls and four representative faults parameterized. All new Go files below 500 lines; existing larger files already feature grouped/parameterized.',fixtures:'Existing relayEnv/parked decorator, apitest.SeedDecideFixtureAgent/procfix, deadlineBoundary/execHookConnector, cursorfix/newCursorPaneScenario/public adapter/owned-launch/sql reused. No copied controller/retry/queue implementation, helper-only tests, parallel exhaustive fault matrix, or newly unused helper found.',isolation:'Per-test real SQLite; test singleton holders serial with reasons; public fixtures serial for HOME/PATH/tmux environment; independent tests parallel. Bun execution remains sequential. Finite contexts/event channels/process waits and owned cleanup retained.',preservation:'Entry manifest binds all unchanged guards: Task1 R1-R5/E2 bounded pending/hash/dialog/answer gates, frame/request minima, postwait authority, independent permission/plan/unknown holds, question denial, ack before bytes and native pause/exact owned death. No deadline/SQL timeout/assertion relaxation, parallelism reduction, retry-until-green, or concurrent send atomicity promise.',evidence:'Received raw bytes versus complete frames and source-derived versus received traffic are distinct; P1 startup/P2 completed publication/P3 partial-byte corrections reviewed. No audit fsync/crash durability claim; cleanup quarantines leases and original deadline/one-second zero-work semantics agree with docs. Native uninterruptible/descheduling limits remain explicit.',formal:'No actual spec/tla input change; zero formal/parser/TLC/Java jobs.'},
  doc_consistency:'Five edited product docs remain unchanged from entry. Canonical changed recovery is caller-contract Permission relay. Architecture final 9593447 retained; inherited whole-base four partitions plus five verified deltas is aggregate coverage, not a fresh whole-file read or clean-document claim; deferred A2-A6/B3-B8/C2-C5/D1-D2 and inherited guide normalization remain deferred.',
  collector_gate:{groups:17,expected_cases:172,executed:false,installed_dependency_network_prerequisite:true,raw_archive_mount_required:true,makefile_extra_args_variable:false},
  historical09:'Both historical failure causes remain UNKNOWN. Controlled regression does not reconstruct them.'
 });
}
console.log(JSON.stringify({phase,files:paths.length,changes,source_unchanged:true,protected_unchanged:true}));
