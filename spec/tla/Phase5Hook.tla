----------------------------- MODULE Phase5Hook -----------------------------
(***************************************************************************)
(* b.zuj v5 -- the hook-identity design of decision-0929c (SRD rev 17,     *)
(* SR-22.9, SR-3.6 adoption by @ad_pane, SR-6.1 kill of every pane).       *)
(* A focused model of the process and pane layer for ONE instance id; the  *)
(* label lookup and its verdicts are Phase4's.                              *)
(*                                                                         *)
(* Every process here is a Claude that carries the instance id (the id is  *)
(* in the tmux session's environment, so every pane of the session gets    *)
(* it). Kinds: the launched agent (the main process of the pane the create *)
(* made; `lau` = its launch), a nested Claude (a child of an agent, in the *)
(* agent's pane), a teammate (the main process of its own pane, split in   *)
(* the agent's session; no @ad_pane), a leftover (its own session with an  *)
(* old label), a stray (outside tmux). A hook is a child of the Claude that *)
(* fires it, so the hook's parent is that Claude process.                  *)
(*                                                                         *)
(* Knobs (the design: Gate="parent", AdoptBy="adpane", KillAll=TRUE):      *)
(*  Gate    "parent"  hook's parent pid+start = recorded pane pid+start    *)
(*          "topmost" the old walk: topmost ancestor carrying the id       *)
(*          "pane"    the hook's pane (TMUX_PANE / label via pane) = rec.  *)
(*  AdoptBy "adpane"  a lost reply adopts the pane whose @ad_pane = token  *)
(*          "anypane" any pane of the labelled session                     *)
(*  KillAll TRUE: the kill waits for every pane process of the session;    *)
(*          FALSE: for the agent process only.                             *)
(* Pids are reused (smallest free pid) when PidPool > 0; start times never *)
(* repeat. A pane closes when its main process exits or it is killed; the  *)
(* other processes in it get SIGHUP and die unless they ignore it (`hup`), *)
(* then they sit in no pane. Kill = listing + kill-pane + kill-session +   *)
(* the wait, as one step (the design's milliseconds window).               *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS MaxClock, MaxLife, MaxFaults, PidPool,
          Gate, AdoptBy, KillAll,
          AllowNested, AllowTeammate, AllowStray, AllowLeftover, AllowRotate,
          AllowHup, AllowLostReply, AllowNullStart, AllowCrash, Probes,
          FMNoPaneGone,  \* find-missing: no pane recorded, the launch's @ad_pane pane is gone -> Gone
          \* decision-0930b Q1: the identity write is its own step after the create
          HookWait,          \* SessionStart waits (pending, no pane, identity write outstanding, in grace)
          AllowSlowIdentity  \* the grace period may pass before the identity write (residual ii)

VARIABLES row, procs, panes, sess, clock, life, faults, rowViol, killViol, seen

vars == <<row, procs, panes, sess, clock, life, faults, rowViol, killViol, seen>>

OldTok == 99                                   \* a leftover's label: an earlier life's token
\* idw: the launch's identity write is outstanding; rp: the create reply it will write
\* (<<pane, pid, start>>); late: the grace period passed before it; lostRep: the reply was lost.
NoRow  == [st |-> "none", tok |-> 0, pn |-> 0, ppid |-> 0, pst |-> 0, sid |-> 0,
           idw |-> FALSE, rp |-> <<0, 0, 0>>, late |-> FALSE, lostRep |-> FALSE]

Init == /\ row = NoRow /\ procs = {} /\ panes = {} /\ sess = {}
        /\ clock = 0 /\ life = 0 /\ faults = 0
        /\ rowViol = FALSE /\ killViol = FALSE /\ seen = {}

Note(c, t) == seen' = IF Probes /\ c THEN seen \cup {t} ELSE seen
Can        == clock < MaxClock
T          == clock + 1                         \* the next start time / pane id / session id / Claude session id
UsedPids   == {p.pid : p \in procs}
HasFree    == PidPool = 0 \/ \E x \in 1..PidPool : x \notin UsedPids
FreePid    == IF PidPool = 0 THEN T
              ELSE CHOOSE x \in 1..PidPool : x \notin UsedPids /\ \A y \in 1..PidPool : y \notin UsedPids => x <= y
HupChoice  == IF AllowHup THEN BOOLEAN ELSE {FALSE}
\* ssw: its SessionStart hook is running; wt: that hook found the identity write outstanding
NewProc(pid, par, pn, lau, h) == [pid |-> pid, st |-> T, par |-> par, pn |-> pn, lau |-> lau,
                                  sid |-> T, started |-> FALSE, ended |-> FALSE, hup |-> h,
                                  ssw |-> FALSE, wt |-> FALSE]
IsAgent(p) == row.tok # 0 /\ p.lau = row.tok    \* the current launch's agent
AgentAlive == \E p \in procs : IsAgent(p)

\* Panes PNs close: their processes other than survivors die; survivors leave every pane.
AfterClose(PNs) ==
  {q \in procs : q.pn \notin PNs} \cup {[q EXCEPT !.pn = 0] : q \in {r \in procs : r.pn \in PNs /\ r.hup}}
PanesLeft(PNs) == {x \in panes : x.pn \notin PNs}
SessLeft(PS)   == {s \in sess : \E x \in PS : x.tid = s.tid}

-----------------------------------------------------------------------------
(* The gate *)
Match(q)  == row.pn # 0 /\ row.ppid = q.pid /\ (row.pst = 0 \/ row.pst = q.st)
Top(p)    == IF \E q \in procs : q.pid = p.par THEN CHOOSE q \in procs : q.pid = p.par ELSE p
GateOK(p) == CASE Gate = "parent"  -> Match(p)
               [] Gate = "topmost" -> Match(Top(p))
               [] OTHER            -> row.pn # 0 /\ p.pn = row.pn
\* An applied hook: the state it writes, the session id, and (NULL start) the start time.
Apply(p, st, sid) ==
  /\ row' = [row EXCEPT !.st = st, !.sid = sid, !.pst = IF @ = 0 /\ row.pn # 0 THEN p.st ELSE @]
  /\ rowViol' = (rowViol \/ ~IsAgent(p))
Ignored(p) ==
  /\ Note(p.lau = 0 /\ p.pn # 0 /\ p.pn = row.pn, "nested_ignored")
  /\ UNCHANGED <<row, rowViol>>

\* SessionStart (startup, or clear/resume after a rotation): whatever the row's state.
\* HookFire: Claude starts the hook (no prompt reaches Claude until it finishes, D1).
HookFire(p) ==
  /\ p.sid # 0 /\ ~p.started /\ ~p.ended /\ ~p.ssw /\ row.st # "none"
  /\ procs' = (procs \ {p}) \cup {[p EXCEPT !.started = TRUE, !.ssw = TRUE,
                                           !.wt = (row.st = "pending" /\ row.pn = 0 /\ row.idw)]}
  /\ UNCHANGED <<row, panes, sess, clock, life, faults, rowViol, killViol, seen>>
\* The wait (decision D): while the row is pending with no pane, its identity write
\* outstanding and the grace period not yet passed.
Waits == HookWait /\ row.st = "pending" /\ row.pn = 0 /\ row.idw /\ ~row.late
\* HookStartRun: the gated write.
HookStartRun(p) ==
  /\ p.ssw /\ ~Waits
  /\ procs' = (procs \ {p}) \cup {[p EXCEPT !.ssw = FALSE]}
  /\ IF GateOK(p)
     THEN /\ Apply(p, "live", p.sid)
          /\ seen' = IF ~Probes THEN seen
                     ELSE seen \cup (IF IsAgent(p) /\ p.sid # p.st THEN {"rotation_applied"} ELSE {})
                               \cup (IF p.wt THEN {"waited_applied"} ELSE {})
     ELSE /\ UNCHANGED <<row, rowViol>>
          /\ seen' = IF ~Probes THEN seen
                     ELSE seen \cup (IF p.lau = 0 /\ p.pn # 0 /\ p.pn = row.pn THEN {"nested_ignored"} ELSE {})
                               \cup (IF p.wt THEN {"waited_ignored"} ELSE {})
  /\ UNCHANGED <<panes, sess, clock, life, faults, killViol>>
\* An ordinary hook (a prompt, a tool, Stop): moves the row as today; records the
\* session id only when the row has none (a lost reply adopted after SessionStart).
HookAct(p) ==
  /\ p.started /\ ~p.ended /\ ~p.ssw /\ row.st # "none"
  /\ IF GateOK(p) THEN Apply(p, "live", IF row.sid = 0 THEN p.sid ELSE row.sid) /\ UNCHANGED seen
     ELSE Ignored(p)
  /\ UNCHANGED <<procs, panes, sess, clock, life, faults, killViol>>
\* SessionEnd (a real end; the process exits afterwards).
HookEnd(p) ==
  /\ p.started /\ ~p.ended /\ ~p.ssw /\ row.st # "none"
  /\ procs' = (procs \ {p}) \cup {[p EXCEPT !.ended = TRUE]}
  /\ IF GateOK(p) THEN Apply(p, "ended", row.sid) /\ UNCHANGED seen ELSE Ignored(p)
  /\ UNCHANGED <<panes, sess, clock, life, faults, killViol>>
\* In-session /clear or /resume: the same process, a new Claude session id; its
\* SessionStart (source clear/resume) follows as HookFire and HookStartRun.
Rotate(p) ==
  /\ AllowRotate /\ Can /\ p.started /\ ~p.ended /\ ~p.ssw /\ p.lau # 0
  /\ procs' = (procs \ {p}) \cup {[p EXCEPT !.sid = T, !.started = FALSE]}
  /\ clock' = clock + 1
  /\ UNCHANGED <<row, panes, sess, life, faults, rowViol, killViol, seen>>

-----------------------------------------------------------------------------
(* Launches, the processes an agent starts, accidents *)
\* spawn / resume / reuse: a new session and pane (@ad_owner and @ad_pane = the token)
\* whose main process is the agent (it starts inside the create call). The row stays
\* pending with no pane until the identity write, a separate later step.
Launch ==
  /\ row.st \in {"none", "ended", "missing"} /\ life < MaxLife /\ Can /\ HasFree
  /\ LET l == life + 1  pid == FreePid IN
       /\ sess'  = sess  \cup {[tid |-> T, tok |-> l]}
       /\ panes' = panes \cup {[pn |-> T, tid |-> T, main |-> pid, mst |-> T, adp |-> l]}
       /\ procs' = procs \cup {NewProc(pid, 0, T, l, FALSE)}
       /\ row' = [st |-> "pending", tok |-> l, pn |-> 0, ppid |-> 0, pst |-> 0, sid |-> row.sid,
                  idw |-> TRUE, rp |-> <<T, pid, T>>, late |-> FALSE, lostRep |-> FALSE]
  /\ life' = life + 1 /\ clock' = clock + 1
  /\ UNCHANGED <<faults, rowViol, killViol, seen>>
\* The identity write (SR-3.6): conditional on the row as the launch left it (still
\* pending, no pane: an adoption in between wins). The reply may have been lost (a
\* fault: nothing written), and the start time may be unreadable (NULL).
RecordIdentity ==
  /\ row.st = "pending" /\ row.idw
  /\ \E lost \in (IF AllowLostReply /\ faults < MaxFaults THEN BOOLEAN ELSE {FALSE}),
        ns \in (IF AllowNullStart THEN BOOLEAN ELSE {FALSE}) :
       /\ row' = IF lost \/ row.pn # 0 THEN [row EXCEPT !.idw = FALSE, !.lostRep = lost]
                 ELSE [row EXCEPT !.idw = FALSE, !.pn = row.rp[1], !.ppid = row.rp[2],
                                  !.pst = IF ns THEN 0 ELSE row.rp[3]]
       /\ faults' = IF lost THEN faults + 1 ELSE faults
       /\ Note(ns /\ ~lost /\ row.pn = 0, "null_start")
  /\ UNCHANGED <<procs, panes, sess, clock, life, rowViol, killViol>>
\* The grace period passes before the identity write (drops the timing assumption).
IdentityLate ==
  /\ AllowSlowIdentity /\ row.st = "pending" /\ row.idw /\ ~row.late
  /\ row' = [row EXCEPT !.late = TRUE]
  /\ UNCHANGED <<procs, panes, sess, clock, life, faults, rowViol, killViol, seen>>
\* a nested `claude` run from an agent's shell: a child in the agent's pane
Nested ==
  /\ AllowNested /\ Can /\ HasFree
  /\ \E p \in procs : p.lau # 0 /\ p.pn # 0 /\ \E h \in HupChoice :
       procs' = procs \cup {NewProc(FreePid, p.pid, p.pn, 0, h)}
  /\ clock' = clock + 1
  /\ UNCHANGED <<row, panes, sess, life, faults, rowViol, killViol, seen>>
\* an agent-teams teammate: the main process of a new pane split in the agent's session
Teammate ==
  /\ AllowTeammate /\ Can /\ HasFree
  /\ \E p \in procs, x \in panes : p.lau # 0 /\ x.pn = p.pn /\ x.main = p.pid /\ \E h \in HupChoice :
       /\ panes' = panes \cup {[pn |-> T, tid |-> x.tid, main |-> FreePid, mst |-> T, adp |-> 0]}
       /\ procs' = procs \cup {NewProc(FreePid, 0, T, 0, h)}
  /\ clock' = clock + 1
  /\ UNCHANGED <<row, sess, life, faults, rowViol, killViol, seen>>
Stray ==
  /\ AllowStray /\ Can /\ HasFree /\ ~\E p \in procs : p.pn = 0 /\ p.par = 0 /\ p.lau = 0
  /\ procs' = procs \cup {NewProc(FreePid, 0, 0, 0, FALSE)}
  /\ clock' = clock + 1
  /\ UNCHANGED <<row, panes, sess, life, faults, rowViol, killViol, seen>>
\* a leftover of an earlier life: its own session and pane, an old label
Leftover ==
  /\ AllowLeftover /\ Can /\ HasFree /\ ~\E s \in sess : s.tok = OldTok
  /\ sess'  = sess  \cup {[tid |-> T, tok |-> OldTok]}
  /\ panes' = panes \cup {[pn |-> T, tid |-> T, main |-> FreePid, mst |-> T, adp |-> OldTok]}
  /\ procs' = procs \cup {NewProc(FreePid, 0, T, 0, FALSE)}
  /\ clock' = clock + 1
  /\ UNCHANGED <<row, life, faults, rowViol, killViol, seen>>
\* a process exits: after its SessionEnd, or any time (a crash; non-agents any time).
\* A pane main's exit closes its pane (remain-on-exit is off).
Exit ==
  \E p \in procs :
    /\ p.ended \/ AllowCrash \/ p.lau = 0
    /\ LET PNs == {x.pn : x \in {y \in panes : y.main = p.pid /\ y.mst = p.st}}
           P2  == {q \in procs \ {p} : q.pn \notin PNs}
                  \cup {[q EXCEPT !.pn = 0] : q \in {r \in procs \ {p} : r.pn \in PNs /\ r.hup}}
       IN /\ procs' = P2
          /\ panes' = PanesLeft(PNs)
          /\ sess'  = SessLeft(PanesLeft(PNs))
    /\ UNCHANGED <<row, clock, life, faults, rowViol, killViol, seen>>

-----------------------------------------------------------------------------
(* agent-director's verbs *)
OursTid   == {s.tid : s \in {s2 \in sess : s2.tok = row.tok /\ row.tok # 0}}
AdoptSet  == {x \in panes : x.tid \in OursTid /\ (AdoptBy = "anypane" \/ x.adp = row.tok)}
\* Adoption (kill, send-keys, pause, find-missing) after a lost reply; tmux may not answer.
Adopt ==
  /\ row.st \in {"pending", "live"} /\ row.pn = 0 /\ AdoptSet # {}
  /\ \E f \in (IF faults < MaxFaults THEN BOOLEAN ELSE {FALSE}) :
       IF f THEN /\ faults' = faults + 1 /\ UNCHANGED <<row, seen>>
       ELSE /\ IF AdoptBy = "adpane" THEN Cardinality(AdoptSet) = 1 ELSE TRUE
            /\ \E x \in AdoptSet :
                 /\ row' = [row EXCEPT !.pn = x.pn, !.ppid = x.main, !.pst = x.mst]
                 /\ Note(TRUE, "adopted")
            /\ UNCHANGED faults
  /\ UNCHANGED <<procs, panes, sess, clock, life, rowViol, killViol>>
\* kill of a live row (pending included), lookup Ours, the agent's pane found:
\* list the session's panes, kill the agent's pane and the session, wait.
Kill ==
  /\ row.st \in {"pending", "live"} /\ row.pn # 0
  /\ \E a \in panes :
       /\ a.pn = row.pn /\ a.main = row.ppid /\ a.tid \in OursTid
       /\ LET S    == {x \in panes : x.tid = a.tid}
              PNs  == {x.pn : x \in S}
              L    == IF KillAll THEN {<<x.main, x.mst>> : x \in S} ELSE {<<a.main, a.mst>>}
              P2   == AfterClose(PNs)
              ok   == ~\E q \in P2 : <<q.pid, q.st>> \in L
              mainSurv == \E q \in P2 : <<q.pid, q.st>> \in {<<x.main, x.mst>> : x \in S}
          IN /\ procs' = P2
             /\ panes' = PanesLeft(PNs)
             /\ sess'  = SessLeft(PanesLeft(PNs))
             /\ killViol' = (killViol \/ (ok /\ mainSurv))
             /\ seen' = IF ~Probes THEN seen
                        ELSE seen \cup (IF ~ok THEN {"kill_failed"} ELSE {})
                                  \cup (IF ok /\ \E q \in P2 : q.pn = 0 /\ q.hup /\ q.par # 0
                                        THEN {"child_outlives_ok_kill"} ELSE {})
  /\ UNCHANGED <<row, clock, life, faults, rowViol>>
\* find-missing (the grace period abstracted): the recorded agent process is gone, or
\* no pane is recorded and the launch's labelled session is gone.
FM ==
  /\ row.st \in {"pending", "live"}
  /\ \/ row.pn # 0 /\ ~\E q \in procs : Match(q)
     \/ row.pn = 0 /\ OursTid = {}
     \/ row.pn = 0 /\ FMNoPaneGone /\ ~\E x \in panes : x.adp = row.tok
  /\ row' = [row EXCEPT !.st = "missing"]
  /\ UNCHANGED <<procs, panes, sess, clock, life, faults, rowViol, killViol, seen>>

Next == \/ \E p \in procs : HookFire(p) \/ HookStartRun(p) \/ HookAct(p) \/ HookEnd(p) \/ Rotate(p)
        \/ Launch \/ RecordIdentity \/ IdentityLate \/ Nested \/ Teammate \/ Stray \/ Leftover \/ Exit
        \/ Adopt \/ Kill \/ FM
Spec     == Init /\ [][Next]_vars
\* a running hook finishes; a launch's identity write completes or fails. HookFire and
\* HookAct stay unfair (an agent may sit at a prompt, or idle).
FairSpec == Spec /\ WF_vars(FM) /\ WF_vars(Adopt) /\ WF_vars(RecordIdentity)
                 /\ WF_vars(\E p \in procs : HookStartRun(p))

-----------------------------------------------------------------------------
(* Properties *)
RowOnlyByAgent == ~rowViol       \* the row's state and session id move only by the current launch's agent
KillAllGone    == ~killViol      \* kill never succeeds while a pane process of the session survives
\* a pending row resolves, unless its agent runs in a recorded pane (visible, and
\* its next hook reports it in)
PendingResolves == (row.st = "pending") ~> (row.st # "pending" \/ (row.pn # 0 /\ AgentAlive))
ClockBound == clock <= MaxClock
\* decision-0930b Q1: a launch whose agent has fired SessionStart leaves pending with no
\* further hook, unless its reply was lost or its identity write came after the grace period.
\* (The loss or lateness may happen after the SessionStart fired, so they excuse on the
\* right-hand side; excluding them only on the left misses a later loss.)
ReportedStart    == row.st = "pending" /\ \E p \in procs : IsAgent(p) /\ p.started
ReportInResolves == ReportedStart ~> (row.st # "pending" \/ ~AgentAlive \/ row.lostRep \/ row.late)
\* the same, without excusing a late identity write: shows residual (ii)
ReportInResolvesT == ReportedStart ~> (row.st # "pending" \/ ~AgentAlive \/ row.lostRep)

\* probes (each must be violated: the case is reachable)
NotSeen(t) == t \notin seen
NotNestedIgnored   == NotSeen("nested_ignored")
NotRotationApplied == NotSeen("rotation_applied")
NotAdopted         == NotSeen("adopted")
NotKillFailed      == NotSeen("kill_failed")
NotChildOutlives   == NotSeen("child_outlives_ok_kill")
NotNullStart       == NotSeen("null_start")
NotTeammateLive    == ~\E p \in procs, x \in panes : p.lau = 0 /\ p.par = 0 /\ x.main = p.pid /\ x.adp = 0
                                                     /\ x.tid \in OursTid /\ row.st = "live"
NotWaitedApplied   == NotSeen("waited_applied")
NotWaitedIgnored   == NotSeen("waited_ignored")
NotPidReused       == ~\E p \in procs : row.ppid = p.pid /\ row.pst # 0 /\ row.pst # p.st
=============================================================================
