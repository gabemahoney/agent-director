import {readFileSync} from 'node:fs';

const dir='tickets/handoff/b.ftd/Plans/b.jf1/e3-task2/final-tests/';
export function correctFinalReport(report) {
  const log=readFileSync(dir+'09-full-test-sandbox.log','utf8');
  const blocks=[...log.matchAll(/    --- FAIL: (TestDecideMaxWaitUnderHeldLock\/[^\n]+)\n([\s\S]*?)(?=    --- FAIL:|\nagent-director:|\nFAIL)/g)];
  if(blocks.length!==2) throw Error('Expected exactly two historical deadline failures');
  const failures=blocks.map(([,heading,body])=>({
    failed_test:heading.replace(/ \([^)]*\)$/,''),
    elapsed:body.match(/decide returned after ([^;]+);/)[1],
    asserted_bound:'1.5s', max_wait_ms:500, total_real_sleep:'0s',
    phases:body.split('\n').filter(x=>x.includes('decide phase ')).map(x=>x.trim()),
    intended_postcommit_lock_reached:false,
  }));
  const historical={log:'09-full-test-sandbox.log',exit_code:2,failures,
    cause:'Unknown. Both calls exceeded the unchanged 1.5s bound before any sleep callback; the intended after-commit fixture lock was never acquired and cannot explain these failures. The store method timings include internal execution and scheduling and do not isolate connection acquisition, SQL/COMMIT, cleanup or synchronous postcommit trail emission.',
    scope:'No pkg/api source or test changes in Task2. This is a separate historical deadline failure, not a rotation or pane-notice synchronization failure.',
    inputs:'second-settled-input-manifest.json',post_inputs:'second-after-input-manifest.json',input_drift:false,
    final_green:'13-full-test-sandbox.log passes pkg/api; passing does not causally resolve the historical failures',
    disposition:'Bounded follow-up evidence in ../timing-investigation/FINAL-report.json; explicit coordinator technical disposition still required.'};
  report.failed_receipts=report.failed_receipts.filter(x=>!x.failures?.some(y=>y.failed_test?.startsWith('TestDecideMaxWaitUnderHeldLock/')));
  report.failed_receipts.push(historical);
  report.historical_failures_with_unknown_cause=[historical];
  report.result='Final full unit suite passed; historical deadline failures have unknown cause and require technical disposition';
  report.ticket_statuses['t3.jf1.r8.sr.e9']='done -> active for bounded timing investigation; disposition pending';
  report.full_suite.result='passed; refill and pane-notice test synchronization corrected; separate historical pkg/api deadline failures remain causally unresolved';
  report.full_suite.unresolved_failures_scope='The final 13 run has no failing tests. The separate historical_failures_with_unknown_cause field retains failures not causally explained by that green run.';
  report.full_suite.count_limit='Go output reports package totals, not test/leaf counts; Bun totals are exact. Runs 04 and 09 failed. Run 09 included both the corrected pane-notice observation failure and two separate pkg/api deadline failures of unknown cause. Final 13 is retained unchanged; no new full run in the bounded follow-up.';
  report.incomplete_work=['Coordinator technical disposition of historical pkg/api deadline failures remains pending after bounded investigation.'];
  return report;
}
