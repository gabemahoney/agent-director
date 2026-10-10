# b.36h: scheduler jobs

Sixteen jobs, one run each, of `tla/Hold36h.tla`. Each `job-<run>/` is self-contained:
`Dockerfile`, the unchanged `tlcjob.sh` (`spec/tla/jobs/lib/tlcjob.sh`), `tla2tools.jar`
(`spec/tla/tla2tools.jar`), the spec, one cfg and `runs`. The Dockerfile gives TLC `-Xmx12g`,
2 GB direct memory, a 3,400 s budget (one run capped at 3,300 s) and a 20 GB disk cap. The last
output line is the verdict: `JOB-VERDICT <cfg>=<result>/<distinct states>`.

Sources: `tla/Hold36h.tla` (the spec), `tla/gencfg.py` (writes `tla/cfg/*.cfg` and
`tla/suite.tsv`), `tla/mkjobs.sh` (writes the job dirs), `tla/runlocal.sh` (2 GB local sanity).

## The question

The working hold (`holdWorkingTransition`) reads `holdingRequestSQL`. When no request holds, the
caller writes the move to `working` with a separate UPDATE whose WHERE has the gate only. The relay
hook's `InsertRelayRequest` (one BEGIN IMMEDIATE transaction: the request, and `check_permission`)
can commit between the two. A read does not wait for the write lock (WAL), so it can run while that
transaction is open and see no request; the UPDATE then waits for the lock and runs after the
commit, overwriting `check_permission` with `working`.

The model is b.omt's `OmtHold` (same world, properties, and `Design`; the branch has `fixcut`) with:
- the working write split into a read step and a write step, with each agent's hooks blocking that
  agent (one working write in flight per agent, main and the background subagent);
- the store lock: `InsertRelayRequest` is a begin step (takes the lock) and a commit step; every
  write waits while the lock is held; reads never wait.

`Write`:
- `split`: today. The UPDATE has the gate only.
- `cond`: the fix. The UPDATE also needs `NOT EXISTS (... holdsRowSQL)`. When it matches nothing
  because a request now holds, the write is reported as held: no state write, `idle_since` cleared.

## Runs

| Job directory | Run | Expected | Local (2 GB) |
|---|---|---|---|
| `job-red_split` | control, today: a subagent's working read, taken while the main agent's request transaction is open, then its UPDATE overwrites `check_permission` | FAIL `WorkingTrue` | FAIL |
| `job-red_split_all` | control, today, every switch, two turns | FAIL `WorkingTrue` | not run |
| `job-ctl_split_nosub` | control: today with the main agent alone is safe (its hooks run one at a time), so the race needs two agents | PASS | PASS, 4,906 |
| `job-cond_safe` | fix: safety, subagent, every switch | PASS | not run |
| `job-cond_safe3` | fix: safety, three turns, every switch | PASS | not run |
| `job-cond_live1` | fix: every property, one turn | PASS | PASS, 7,180 |
| `job-cond_live` | fix: every property, hook deaths, record-pane-answer | PASS | not run |
| `job-cond_live_slow` | fix: every property, slow idle hook | PASS | not run |
| `job-cond_live3` | fix: every property, three turns (the largest) | PASS | not run |
| `job-red_today_stale` | red for `NoStaleHold` (b.omt's proof rule off) | FAIL `NoStaleHold` | FAIL |
| `job-red_today_settle` | red for `RowSettles` (b.omt's proof rule off) | FAIL `RowSettles` | FAIL |
| `job-lim_cond_sub_noclaim` | b.omt's accepted residual, unchanged by the fix | FAIL `RowSettles` | FAIL |
| `job-p_race` | probe: a write read "not held", and at its write a request holds | FAIL `NeverHit` | FAIL |
| `job-p_mainrace` | probe: the same, with the main agent's write and the subagent's request | FAIL `NeverHit` | FAIL |
| `job-p_txrace` | probe: the race through the lock (read inside the open transaction, write after its commit) | FAIL `NeverHit` | FAIL |
| `job-p_staleheld` | probe: the opposite window, a read "held" whose request no longer holds at the write (unchanged by the fix; find-missing repairs the row) | FAIL `NeverHit` | FAIL |

Properties: `WorkingTrue` (the row never reads `working` while a dialog may be up or a relay hook
may answer: b.36h's expected result), `NoStaleHold` and `RowSettles` (b.omt's), `TypeOK`.

Distinct-state counts of the FAIL runs vary between runs (TLC stops at the first violation with
four workers). The PASS runs not run locally are bigger: `OmtHold`'s largest (`fixcut_live3`) was
1.0 M states, and this model adds the in-flight writes and the lock. If `cond_live3` or
`cond_safe3` ends INCOMPLETE (time budget) with no violation, rerun it with a longer `--timeout`
and raise `BUDGET` and the `@3300` in `runs`.

Not modelled: a relay hook that dies before its transaction commits (nothing recorded; an accepted
gap of b.146), parallel tool calls of one agent, the gap between the held branch's gate read and
its `idle_since` clear, and what `OmtHold` leaves out.
