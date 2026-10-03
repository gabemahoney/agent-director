------------------------------- MODULE Phase4 -------------------------------
(***************************************************************************)
(* b.zuj v4 -- PHASE 1 model of the FINAL provenance design, "PO 2026-09-27 *)
(* LABEL" (b.fmk.md "Final decision on session provenance: the label is    *)
(* the proof"; provenance-fresh.md Part 2; SRD t1.fmk.i8 SR-3, SR-6.1,     *)
(* SR-11.3, SR-12.2). A fresh model: the v3 models (Phase1_v3t.tla, and    *)
(* the superseded record+label Phase1.tla) are kept unchanged.             *)
(*                                                                         *)
(* Threat model: ACCIDENTS ONLY. No forged labels. Modelled accidents:     *)
(* renames (a session's name changes; the label stays), grouped / viewing  *)
(* sessions sharing the agent's pane (kill-session of the agent's session  *)
(* then leaves the agent alive), a failed label step, a lost create reply, *)
(* a tmux server restart (every session and process dies; tmux session and *)
(* pane ids restart), a different server answering at the recorded socket  *)
(* (a re-bound path), remain-on-exit (a session outlives its agent) and    *)
(* respawn-pane (a pane runs something else), a leftover session of an    *)
(* earlier life, strays, crashes, the /proc wall.                          *)
(*                                                                         *)
(* STRUCTURE. Agent processes (`procs`), tmux sessions (`sess`) and panes  *)
(* are separate. A session shows one pane `pn`; a grouped viewer shares it. *)
(* A pane's process is the proc with that `pn`. A pane closes when its     *)
(* process dies (unless RemainOnExit) and when it is killed; a session     *)
(* closes when its pane closes; kill-session closes a pane only when no    *)
(* other session shows it. All ids come from one creation clock `nextSid`: *)
(* process pids are global (kernel pid + start time never repeat); tmux    *)
(* session ids and pane ids restart after a server restart (sbase).        *)
(*                                                                         *)
(* THE DESIGN (each amendment has a knob; FALSE = the amendment removed,   *)
(* used only by the controls):                                             *)
(*  A1 TokenKept     the launch token is never cleared at report-in.       *)
(*  A2 ByLabel       the lookup finds the session by its label anywhere on *)
(*                   the server (FALSE: only the session holding the name).*)
(*  A3 DollarById    a `$` name is labelled by session id (FALSE: the      *)
(*                   chained '=$1:' target labels session $1 instead).     *)
(*  A4 LabelRecover  a failed label step relabels by id, else kills the    *)
(*                   new session (a both-attempts failure is one fault).   *)
(*  A5 PaneKill      kill ends the agent's pane, then the session, and     *)
(*                   succeeds only once the agent process is gone.         *)
(*  A6 RecordedSock  every call uses the recorded socket, and a lost       *)
(*                   reply's server identity/pane is adopted (FALSE: calls  *)
(*                   may reach the caller's server, CallerWrongServer; no  *)
(*                   adoption).                                            *)
(*  A7 ProcLiveness  liveness only from the agent process (FALSE: an Ours  *)
(*                   session's presence counts as alive).                  *)
(* Kept: the strict kill P1 (Leftover -> CONFLICT), the held-name end     *)
(* (plain spawn "duplicate session" ends the row; resume/reuse restore),   *)
(* the squat rule as the ordinary Gone rule, SR-6.7 for kill              *)
(* --include-finished, launch pending, history by life.                    *)
(* ActPidCheck: TRUE = a pane listing and the act on that pane id are one *)
(* step (the design's "milliseconds" window); FALSE = separate steps, so a *)
(* server restart between them can hand the pane id to another process    *)
(* (the if-shell guard was declined).                                      *)
(*                                                                         *)
(* b.66h LAUNCH-SCOPED ACTIONS (repo change, not from b.zuj; bee b.66h,    *)
(* launch-scoped-actions.md section 3). kill and send-keys may carry the   *)
(* launch id the caller read (from get, status, spawn or resume). Right    *)
(* after the verb's one row read, before every other check, agent-director *)
(* compares it with the row's current launch and refuses with              *)
(* ErrLaunchChanged when they differ: result "changed", no tmux call, no   *)
(* write. Knobs LaunchObs, LaunchScoped and RelaunchAny (below the         *)
(* CONSTANTS) are definitions, all FALSE, so the b.zuj run-4 cfgs parse    *)
(* unchanged and keep their state graphs; a b.66h cfg (spec/tla/launch/)  *)
(* turns them on by definition override, e.g. `LaunchScoped <- KnobOn`.   *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS MaxSid, MaxLife, MaxFaults, ResumeEnabled, EnableExpire,
          AllowStray, WallInit, EnableOrch, EnableBot, EnableSweeper, OrchNames,
          EnableHumanResume, EnableHumanKill, HistoryByLife, ApproverPrompt, Probes,
          ResumeSendOK, AllowLeftover, EnableCrash,
          \* accidents
          AllowRename, AllowGroup, AllowRespawn, RemainOnExit, AllowLabelFail,
          AllowRestart, WrongServerFault, CallerWrongServer, AllowLostReply,
          \* the design's amendments (FALSE only in controls)
          A1TokenKept, A2ByLabel, A3DollarById, A4LabelRecover, A5PaneKill,
          A6RecordedSock, A7ProcLiveness,
          ActPidCheck,
          Ghosts,             \* FALSE (liveness runs only): the violation flags and the taints
                              \* keep their initial values; no guard or fairness reads them
          \* decision-0929-shutdown.md (b.fmk), decision 1 and 2:
          HookGate,           \* hooks move a row only from its own agent (FALSE = control)
          SpawnScan,          \* a plain spawn refuses while any session carries this id's label
          StoreInLabel,       \* the label carries the store id; another store's label is foreign
          AllowOtherStore,    \* accident: another store's agent with the same id on this server
          LeftoverLabelled    \* FALSE: the leftover's session carries no label (pre-release)

\* b.66h knobs (see the header). Override in a cfg's CONSTANTS section with
\* `<name> <- KnobOn`.
KnobOn       == TRUE
KnobOff      == FALSE
LaunchObs    == FALSE   \* record the launch each caller read; check ScopedActsOnlyOnObservedLaunch
LaunchScoped == FALSE   \* the design: the compare (FALSE with LaunchObs: today's code, the control)
RelaunchAny  == FALSE   \* the human relauncher may resume the orchestrator's row as well as the
                        \* bot's (and the orchestrator's agent writes a transcript, Message), so a
                        \* relaunch can fall between the orchestrator's kill lookup and its act

Ids      == {"b", "o"}
NoId     == "noid"
BotId    == "b"
BotName  == "n"
OrchId   == "o"
DollarNames == {"d"}                 \* "d" stands for a name like "$1"
DollarTid   == 1                     \* the session id such a name's chain targets
RenameTo    == "r"
Names    == {"n", "nx"} \cup OrchNames \cup (IF AllowRename THEN {RenameTo} ELSE {})
Live     == {"pending", "live"}
Terminal == {"ended", "missing"}
NoTok    == <<0, 0>>
\* Row. lk: ground-truth launch key <<life, version at the launch write>>
\* (never changed by any control). tok: the stored launch token (= lk, but
\* the A1 control clears it at report-in). sv: recorded server identity
\* (epoch; 0 = none). pn/ppid: recorded agent pane id and its pid.
NoRow    == [st |-> "none", name |-> "-", life |-> 0, pid |-> 0, sessId |-> FALSE, ver |-> 0,
             fresh |-> FALSE, ey |-> FALSE, tx |-> FALSE, ee |-> 0,
             lk |-> NoTok, tok |-> NoTok, sv |-> 0, pn |-> 0, ppid |-> 0]
\* Label: [id, tok, emb]; valid only if emb = the session's own id.
NoLab    == [id |-> NoId, tok |-> NoTok, emb |-> 0, sto |-> "none"]
\* sto: the agent-director store that wrote the label ("s1" = this store).
LabS(i, t, e, st) == [id |-> i, tok |-> t, emb |-> e, sto |-> st]
Lab(i, t, e) == LabS(i, t, e, "s1")
\* Session: tid (tmux id), sc (creation clock), name, lab, pn (its pane),
\* young (starting bound), gl (ground truth: <<id, launch key>> of the
\* launch that made it, or <<NoId, NoTok>>), view (a grouped viewer).
NoSess   == [tid |-> 0, sc |-> 0, name |-> "-", lab |-> NoLab, pn |-> 0, young |-> FALSE,
             gl |-> <<NoId, NoTok>>, view |-> FALSE]
\* Process: pid (global), pn, owner (NoId = not an agent), life, conv
\* (conversation's life), res (launched by resume), started, blocked
\* (startup prompt), ending (SessionEnd sent), gl (ground-truth launch).
Max(S) == CHOOSE x \in S : \A y \in S : y <= x

VARIABLES
  row, sess, procs, hist, nextSid, nextLife, faults, wall, strayUsed, leftUsed,
  srv, sbase, renameUsed, groupUsed, respawnUsed, restartUsed,
  opc, otaint, oname, olife, okt, over, oreuse, osave, oobs, okl,
  bpc, btaint, blife, bver, latch, bconv, bsave, bobs,
  apc, atg,
  hpc, hver, hconv, hsave, hid,
  kpc, kid, ktg,
  fpc, fsnap,
  handsViol, inertViol, lifeViol, sendViol, histViol, botFinViol, hkViol, killViol, scopeViol, seen
\* b.66h: bobs / oobs, the launch the bot / orchestrator last read (an Obs
\* record; NoObs unless LaunchObs); okl, the launch the orchestrator's kill
\* lookup read (RelaunchAny only); hid, the row the human relauncher works
\* on (always BotId unless RelaunchAny); scopeViol, the ghost of
\* ScopedActsOnlyOnObservedLaunch (FALSE unless LaunchObs).

TW        == <<sess, procs>>
provVars  == <<srv, sbase, renameUsed, groupUsed, respawnUsed, restartUsed>>
envVars   == <<wall, strayUsed, leftUsed, provVars>>
orchVars  == <<opc, otaint, oname, olife, okt, over, oreuse, osave, oobs, okl>>
botVars   == <<bpc, btaint, blife, bver, latch, bconv, bsave, bobs>>
apprVars  == <<apc, atg>>
humVars   == <<hpc, hver, hconv, hsave, kpc, kid, ktg, hid>>
fmVars    == <<fpc, fsnap>>
ghostVars == <<handsViol, inertViol, lifeViol, sendViol, histViol, botFinViol, hkViol, killViol, scopeViol,
               seen>>
vars == <<row, TW, hist, nextSid, nextLife, faults, envVars, orchVars, botVars, apprVars,
          humVars, fmVars, ghostVars>>

NewRow(nm, l, v) == [st |-> "pending", name |-> nm, life |-> l, pid |-> 0, sessId |-> FALSE, ver |-> v,
             fresh |-> TRUE, ey |-> FALSE, tx |-> FALSE, ee |-> 0,
             lk |-> <<l, v>>, tok |-> <<l, v>>, sv |-> 0, pn |-> 0, ppid |-> 0]
NoTarget == [pn |-> 0, pid |-> 0, tid |-> 0, sv |-> 0]
\* b.66h: what a caller reads of row i: the launch (ground truth) and the
\* launch token behind the launch_id it is shown. Observe rewrites v only
\* when LaunchObs holds, so the b.zuj runs keep v = NoObs.
NoObs         == [lk |-> NoTok, tok |-> NoTok]
Obs(i)        == [lk |-> row[i].lk, tok |-> row[i].tok]
Observe(v, i) == v' = IF LaunchObs THEN Obs(i) ELSE v
ObsLaunch(i, ob) == <<i, ob.lk>>
\* The compare: a scoped call whose launch is no longer the row's current one.
Changed(i, ob) == LaunchScoped /\ LaunchObs /\ row[i].tok # ob.tok
\* The rows the human relauncher may resume, and whose agents write a transcript.
RelaunchIds == IF RelaunchAny THEN Ids ELSE {BotId}

-----------------------------------------------------------------------------
Note(tag)      == seen' = IF Probes THEN seen \cup {tag} ELSE seen
NoteIf(c, tag) == seen' = IF Probes /\ c THEN seen \cup {tag} ELSE seen
FaultChoices == IF faults < MaxFaults THEN {FALSE, TRUE} ELSE {FALSE}
Spend(f)     == faults' = IF f THEN faults + 1 ELSE faults
\* A tmux lookup's fault: none | cant (Can't tell) | rebound (another server
\* answers at the recorded socket; one fault) | caller (A6 removed: the
\* caller's own environment points at another server; no fault budget).
LChoicesK == {"none"} \cup (IF faults < MaxFaults
                            THEN {"cant"} \cup (IF WrongServerFault THEN {"rebound"} ELSE {}) ELSE {})
                      \cup (IF ~A6RecordedSock /\ CallerWrongServer THEN {"caller"} ELSE {})
SpendK(k)  == faults' = IF k \in {"cant", "rebound"} THEN faults + 1 ELSE faults
\* g for an atomic verb: a lookup fault as above, or "act" (the tmux act fails).
GChoices   == LChoicesK \cup (IF faults < MaxFaults THEN {"act"} ELSE {})
SpendG(g)  == faults' = IF g \in {"cant", "rebound", "act"} THEN faults + 1 ELSE faults
\* the create call: ok | fail | tmo_made (lost reply; session made, labelled)
\* | tmo_none | labfail (made; label step and relabel both failed).
CChoices   == IF faults < MaxFaults
              THEN {"ok", "fail", "tmo_none"} \cup (IF AllowLostReply THEN {"tmo_made"} ELSE {})
                   \cup (IF AllowLabelFail THEN {"labfail"} ELSE {})
              ELSE {"ok"}
SpendC(o)  == faults' = IF o = "ok" THEN faults ELSE faults + 1
CanCreate  == nextSid < MaxSid
NextTid    == nextSid + 1 - sbase
Named(nm)  == {s \in sess : s.name = nm}
PaneProcs(pn) == {p \in procs : p.pn = pn}
ProcAlive(pid) == \E p \in procs : p.pid = pid
AgentProcs(i) == {p \in procs : p.owner = i}
CurLaunch(i)  == <<i, row[i].lk>>
CurProcs(i)   == {p \in procs : p.gl = CurLaunch(i)}
CanInsert(id) == row[id].st = "none" /\ nextLife < MaxLife
InsertRow(id, nm) ==
  /\ row' = [row EXCEPT ![id] = NewRow(nm, nextLife + 1, 0)]
  /\ nextLife' = nextLife + 1

\* Closing things. A pane closes with all sessions that show it; a session
\* killed alone closes its pane only if no other session shows it.
AfterPane(S, P, pn) == <<{s \in S : s.pn # pn}, {p \in P : p.pn # pn}>>
AfterSess(S, P, s)  ==
  LET S2 == {x \in S : x.tid # s.tid} IN
  IF \E x \in S2 : x.pn = s.pn THEN <<S2, P>> ELSE <<S2, {p \in P : p.pn # s.pn}>>

-----------------------------------------------------------------------------
(* The lookup: one listing on the recorded socket, four verdicts (SR-3.4). *)
LabValid(s)   == s.lab.id \in Ids /\ s.lab.emb = s.tid
StoreOK(s)    == ~StoreInLabel \/ s.lab.sto = "s1"      \* decision 2
CurLabel(i, s) == LabValid(s) /\ StoreOK(s) /\ s.lab.id = i /\ s.lab.tok = row[i].tok
OldLabel(i, s) == LabValid(s) /\ StoreOK(s) /\ s.lab.id = i /\ s.lab.tok # row[i].tok
\* decision 1 (scan): before a plain spawn, any session carrying this id's
\* valid label of this store blocks the launch (CONFLICT, a human cleans up).
ScanBlocks(i) == SpawnScan /\ \E s \in sess : LabValid(s) /\ StoreOK(s) /\ s.lab.id = i
Scope(i) == IF A2ByLabel THEN sess ELSE Named(row[i].name)
LV(v, s) == [v |-> v, s |-> s]
Lookup(i, k) ==
  IF k = "cant" THEN LV("cant", NoSess)
  ELSE IF k \in {"rebound", "caller"}
       THEN (IF row[i].sv # 0 THEN LV("cant", NoSess)   \* a different server: Can't tell
             ELSE LV("gone", NoSess))                   \* no identity yet: it counts (empty)
  ELSE LET C == {s \in Scope(i) : CurLabel(i, s)}
           O == {s \in Scope(i) : OldLabel(i, s)} IN
       IF Cardinality(C) = 1 THEN LV("ours", CHOOSE s \in C : TRUE)
       ELSE IF C # {} THEN LV("cant", NoSess)            \* two current labels
       ELSE IF O # {} THEN LV("leftover", CHOOSE s \in O : TRUE)
       ELSE LV("gone", NoSess)
LookupN(i) == Lookup(i, "none")
\* The name holder's class (HELD, pre-launch checks, messages).
Holder(i, nm) ==
  IF Named(nm) = {} THEN "free"
  ELSE LET s == CHOOSE x \in Named(nm) : TRUE IN
       IF CurLabel(i, s) THEN "cur" ELSE IF OldLabel(i, s) THEN "old"
       ELSE IF LabValid(s) THEN "foreign" ELSE "none"

\* The agent's process: the SessionStart pid, else the recorded pane pid.
AgentPid(i) == IF row[i].pid # 0 THEN row[i].pid ELSE row[i].ppid
\* The agent's pane as the design finds it: the recorded pane id showing the
\* recorded pane pid; with no pane recorded (lost reply) the Ours session's
\* pane is adopted in the same call (A6).
RecPane(i) == row[i].pn # 0 /\ \E p \in procs : p.pn = row[i].pn /\ p.pid = row[i].ppid
PaneTarget(i, lk) ==
  IF RecPane(i) THEN row[i].pn
  ELSE IF row[i].pn = 0 /\ A6RecordedSock /\ lk.v = "ours" THEN lk.s.pn
  ELSE 0

\* The agent process a kill must see gone: the SessionStart pid, else the
\* recorded pane pid, else (a lost reply) the pid of the pane adopted in the
\* same lookup.
KillPid(i, lk) ==
  IF AgentPid(i) # 0 THEN AgentPid(i)
  ELSE LET tp == PaneTarget(i, lk) IN
       IF tp # 0 /\ \E p \in procs : p.pn = tp THEN (CHOOSE p \in procs : p.pn = tp).pid ELSE 0

\* SR-4.2: Ours on a finished row, stopping window first.
OwnClass(s, ey) == IF ey \/ s.young THEN "unavail" ELSE "conflict"
\* resume's / reuse's pre-launch check on a finished row (Table 2, LABEL).
PreLaunch(i, lk, nm) ==
  CASE lk.v = "ours"     -> OwnClass(lk.s, row[i].ey)
    [] lk.v = "leftover" -> "conflict"
    [] lk.v = "cant"     -> "unavail"
    [] lk.v = "gone" /\ ~wall /\ AgentPid(i) # 0 /\ ProcAlive(AgentPid(i)) ->
                            IF row[i].ey THEN "unavail" ELSE "conflict"
    [] Holder(i, nm) # "free" -> "conflict"
    [] OTHER             -> "proceed"
\* After "duplicate session": the holder's class (HELD).
DupClass(i, nm) == IF Holder(i, nm) = "free" THEN "fail" ELSE "conflict"

\* Honest kill (SR-6.1, amendment 5), bot side, atomic. r: ok | notfound |
\* conflict | unavail | killfailed. S/P: the tmux world after.
\* b.66h: ob is the launch the caller read; "changed" comes right after the
\* unknown-id check, in any row state.
KillOut(i, g, ob) ==
  IF row[i].st = "none" THEN [r |-> "notfound", S |-> sess, P |-> procs]
  ELSE IF Changed(i, ob) THEN [r |-> "changed", S |-> sess, P |-> procs]
  ELSE IF row[i].st \in Terminal THEN [r |-> "ok", S |-> sess, P |-> procs]
  ELSE LET lk == Lookup(i, IF g = "act" THEN "none" ELSE g)
           ap == AgentPid(i)
           kp == KillPid(i, lk) IN
       CASE lk.v = "cant"     -> [r |-> "unavail", S |-> sess, P |-> procs]
         [] lk.v = "leftover" -> [r |-> "conflict", S |-> sess, P |-> procs]          \* P1
         [] g = "act" /\ lk.v = "ours" -> [r |-> "killfailed", S |-> sess, P |-> procs]
         [] lk.v = "ours" ->
              IF A5PaneKill
              THEN LET tp == PaneTarget(i, lk)
                       W1 == IF tp # 0 THEN AfterPane(sess, procs, tp) ELSE <<sess, procs>>
                       W2 == IF \E x \in W1[1] : x.tid = lk.s.tid
                             THEN AfterSess(W1[1], W1[2], lk.s) ELSE W1
                       gone == IF wall \/ kp = 0
                               THEN ~\E x \in W2[1] : CurLabel(i, x)
                               ELSE ~\E p \in W2[2] : p.pid = kp
                   IN [r |-> IF gone THEN "ok" ELSE "killfailed", S |-> W2[1], P |-> W2[2]]
              ELSE LET W == AfterSess(sess, procs, lk.s) IN [r |-> "ok", S |-> W[1], P |-> W[2]]
         [] OTHER ->                                                              \* Gone
              IF wall \/ ap = 0 \/ ~ProcAlive(ap) THEN [r |-> "ok", S |-> sess, P |-> procs]
              ELSE IF A5PaneKill /\ RecPane(i) /\ row[i].sv = srv
                   THEN LET W == AfterPane(sess, procs, row[i].pn) IN [r |-> "ok", S |-> W[1], P |-> W[2]]
              ELSE IF A5PaneKill THEN [r |-> "killfailed", S |-> sess, P |-> procs]
              ELSE [r |-> "ok", S |-> sess, P |-> procs]
\* Ground truth for "hands off": a process of the row's current launch, or
\* anything in a pane that a session of that launch shows (e.g. a respawned
\* program inside the agent's own session).
\* b.66h: the same for any launch L = <<id, launch key>>.
InLaunchPane(L, p) == p.gl = L \/ \E s \in sess : s.gl = L /\ ~s.view /\ s.pn = p.pn
InCurPane(i, p) == InLaunchPane(CurLaunch(i), p)
\* Ghosts for any kill: hands off; a reported success leaves no process of
\* that launch. b.66h: the L forms take the launch the kill's lookup read,
\* which a split kill needs once a relaunch can fall between its lookup and
\* its act (RelaunchAny); the i forms take the row's current launch.
KillHandsL(L, P) == handsViol' = (handsViol \/ (Ghosts /\ (\E p \in procs \ P : ~InLaunchPane(L, p))))
KillHands(i, P) == KillHandsL(CurLaunch(i), P)
KillHonestL(i, L, r, P) == killViol' = (killViol \/ (Ghosts /\ ((r = "ok" /\ row[i].st \in Live /\
                                       \E p \in P : p.gl = L))))
KillHonestG(i, r, P) == KillHonestL(i, CurLaunch(i), r, P)
\* b.66h ghost: a scoped kill or send reaches only the launch L its caller
\* read (K: the processes it ends, or the processes of the pane it types to).
ScopeG(L, K) == scopeViol' = (scopeViol \/ (Ghosts /\ LaunchObs /\ \E p \in K : ~InLaunchPane(L, p)))

\* send-keys on a live row (SR-7, Table 2), lookup + deliver atomic: to the
\* agent's pane (recorded pane id with its pid), never name:0.0. b.66h: ob
\* is the launch the caller read; "changed" comes first.
SendOut(i, g, ob) ==
  IF Changed(i, ob) THEN [r |-> "changed", p |-> 0]
  ELSE LET lk == Lookup(i, IF g = "act" THEN "none" ELSE g)
           tp == PaneTarget(i, lk) IN
       CASE lk.v = "ours" /\ g = "act" -> [r |-> "unavail", p |-> 0]
         [] lk.v = "ours" /\ tp # 0     -> [r |-> "ok", p |-> tp]
         [] lk.v = "ours"               -> [r |-> "conflict", p |-> 0]   \* the agent's pane was not found
         [] lk.v = "leftover"           -> [r |-> "conflict", p |-> 0]
         [] lk.v = "gone"               -> [r |-> "gone", p |-> 0]
         [] OTHER                       -> [r |-> "unavail", p |-> 0]

\* History (SR-8.7 + history-by-life).
Cur(i)   == IF row[i].sessId THEN {row[i].life} ELSE {}
HistC(i) == IF HistoryByLife THEN {e \in hist[i] : e = row[i].life} \ Cur(i)
            ELSE hist[i] \ Cur(i)
Sel(i)   == IF row[i].tx THEN row[i].life
            ELSE IF HistC(i) = {} THEN 0 ELSE Max(HistC(i))

\* A launcher between its row write and its create call.
\* (b.66h: the human relauncher's launch counts for its row hid; hid is
\* always BotId unless RelaunchAny.)
InLaunch(i) == \/ IF i = BotId THEN bpc \in {"launch", "retry_launch", "reuse_launch", "rlaunch"}
                  ELSE opc = "launch"
               \/ hpc = "rlaunch" /\ hid = i
\* find-missing (SR-11.1, SR-11.3; amendment 7).
Evidence(i) ==
  LET ap == AgentPid(i) IN
  IF ap = 0 THEN "none" ELSE IF wall THEN "unknown" ELSE IF ProcAlive(ap) THEN "alive" ELSE "dead"
FMDead(i, k) ==
  LET ev == Evidence(i)
      lk == Lookup(i, k) IN
  IF row[i].st = "pending" /\ row[i].fresh THEN FALSE
  ELSE IF ~A7ProcLiveness /\ lk.v = "ours" THEN FALSE          \* CONTROL: presence = alive
  ELSE IF ev = "alive" THEN FALSE
  ELSE IF ev = "dead" THEN TRUE                                \* no tmux call
  ELSE lk.v \in {"gone", "leftover"}                           \* unknown / none: the lookup
SameLife(e) == row[e.id].st \in Live /\ row[e.id].life = e.life /\ row[e.id].ver = e.ver
MarkIf(e) == IF SameLife(e) THEN [row EXCEPT ![e.id].st = "missing", ![e.id].ey = TRUE,
                                             ![e.id].ee = nextSid] ELSE row
SweepRows == [i \in Ids |-> IF row[i].st \in Live /\ FMDead(i, "none")
                            THEN [row[i] EXCEPT !.st = "missing", !.ey = TRUE, !.ee = nextSid]
                            ELSE row[i]]

Snap(i) == <<row[i].ver, row[i].life>>
Restore(i, save, v, l) == IF row[i].st = "pending" /\ Snap(i) = <<v, l>>
                          THEN [row EXCEPT ![i] = [save EXCEPT !.ver = v + 1]]
                          ELSE row
\* resume's claim: finished -> pending; a new launch token; pid and the
\* launch's server/pane identity cleared.
Claim(i, v) == [row EXCEPT ![i].st = "pending", ![i].pid = 0, ![i].fresh = TRUE,
                           ![i].ey = FALSE, ![i].ver = v + 1,
                           ![i].lk = <<row[i].life, v + 1>>, ![i].tok = <<row[i].life, v + 1>>,
                           ![i].sv = 0, ![i].pn = 0, ![i].ppid = 0]
\* The held-name end (SR-9.4): a plain launch's "duplicate session" ends its
\* row (conditional on the insert's version and life).
EndLaunch(i, l) == IF row[i].st = "pending" /\ row[i].life = l /\ row[i].ver = 0
                   THEN [row EXCEPT ![i].st = "ended", ![i].ey = TRUE, ![i].fresh = FALSE,
                                    ![i].ee = nextSid, ![i].ver = 1]
                   ELSE row

\* The create call (SR-3.5) for row i on name nm, when nm is free.
\* Result: new sess/procs, the row after the record write, and whether the
\* launch counts as created (FALSE for fail / tmo_none / an A4 kill).
Create(i, nm, l, cv, rs, o) ==
  LET t   == NextTid
      g   == <<i, row[i].lk>>
      lab == Lab(i, row[i].tok, t)
      misdir == nm \in DollarNames /\ ~A3DollarById /\ \E s \in sess : s.tid = DollarTid
      S0  == IF misdir THEN {IF s.tid = DollarTid THEN [s EXCEPT !.lab = lab] ELSE s : s \in sess}
             ELSE sess
      ns  == [tid |-> t, sc |-> nextSid + 1, name |-> nm,
              lab |-> IF misdir \/ o = "labfail" THEN NoLab ELSE lab,
              pn |-> t, young |-> TRUE, gl |-> g, view |-> FALSE]
      np  == [pid |-> nextSid + 1, pn |-> t, owner |-> i, life |-> l, conv |-> IF Ghosts THEN cv ELSE 0,
              res |-> Ghosts /\ rs,
              started |-> FALSE, blocked |-> i = BotId, ending |-> FALSE, gl |-> g]
      made == o \in {"ok", "tmo_made"} \/ (o = "labfail" /\ ~A4LabelRecover)
      rec  == o = "ok" \/ (o = "labfail" /\ ~A4LabelRecover)
  IN [S |-> IF made THEN S0 \cup {ns} ELSE sess,
      P |-> IF made THEN procs \cup {np} ELSE procs,
      tick |-> o \in {"ok", "tmo_made", "labfail"},
      row |-> IF rec /\ row[i].st = "pending" /\ row[i].life = l
              THEN [row EXCEPT ![i].sv = srv, ![i].pn = t, ![i].ppid = nextSid + 1, ![i].ver = @ + 1]
              ELSE row,
      made |-> made]
DoCreate(c) == /\ sess' = c.S /\ procs' = c.P
               /\ nextSid' = IF c.tick THEN nextSid + 1 ELSE nextSid

-----------------------------------------------------------------------------
Init ==
  /\ row = [id \in Ids |-> NoRow]
  /\ sess = {} /\ procs = {} /\ hist = [id \in Ids |-> {}]
  /\ nextSid = 0 /\ nextLife = 0 /\ faults = 0
  /\ wall \in WallInit                     \* {FALSE, TRUE}; split runs use {TRUE} or {FALSE}
  /\ strayUsed = FALSE /\ leftUsed = FALSE
  /\ srv = 1 /\ sbase = 0 /\ renameUsed = FALSE /\ groupUsed = FALSE /\ respawnUsed = FALSE
  /\ restartUsed = FALSE
  /\ opc = "idle" /\ otaint = FALSE /\ oname = "-" /\ olife = 0 /\ okt = NoTarget
  /\ over = 0 /\ oreuse = FALSE /\ osave = NoRow
  /\ bpc = "idle" /\ btaint = FALSE /\ blife = 0 /\ bver = 0 /\ latch = FALSE
  /\ bconv = 0 /\ bsave = NoRow
  /\ apc = "idle" /\ atg = NoTarget
  /\ hpc = "idle" /\ hver = <<0, 0>> /\ hconv = 0 /\ hsave = NoRow
  /\ kpc = "idle" /\ kid = BotId /\ ktg = NoTarget
  /\ fpc = "idle" /\ fsnap = {}
  /\ handsViol = FALSE /\ inertViol = FALSE /\ lifeViol = FALSE /\ sendViol = FALSE
  /\ histViol = FALSE /\ botFinViol = FALSE /\ hkViol = FALSE /\ killViol = FALSE /\ seen = {}
  /\ bobs = NoObs /\ oobs = NoObs /\ okl = NoObs /\ hid = BotId /\ scopeViol = FALSE   \* b.66h

-----------------------------------------------------------------------------
(* Environment: agents, hooks, time, accidents *)
EU == <<hist, nextSid, nextLife, faults, envVars, orchVars, botVars, apprVars, humVars, fmVars, ghostVars>>

\* A process dies: its pane closes with its sessions, unless remain-on-exit.
ProcGone(p) ==
  /\ procs' = procs \ {p}
  /\ sess' = IF RemainOnExit THEN sess ELSE {s \in sess : s.pn # p.pn}
\* SessionStart: keyed by the id in the agent's environment (self-identification).
\* decision 1 (b): hooks move a row only from the row's own agent. The model's
\* process pid stands for Claude's session id (one per agent process).
\* SessionStart: on a pending row, from the recorded pane process (or any, if
\* no pane is recorded: a lost reply); on another row, not while the recorded
\* agent still runs under another pid. Behind the /proc wall the sender
\* cannot be resolved, so SessionStart is accepted as today.
StartOK(p) ==
  LET i == p.owner IN
  ~HookGate \/ row[i].st = "none" \/ wall
  \/ (row[i].st = "pending" /\ (row[i].ppid = 0 \/ row[i].ppid = p.pid))
  \/ (row[i].st # "pending" /\ ~(row[i].pid # 0 /\ row[i].pid # p.pid /\ ProcAlive(row[i].pid)))
\* SessionEnd (an ordinary hook): only with the row's recorded session.
EndOK(p) == ~HookGate \/ row[p.owner].st = "none" \/ row[p.owner].pid = p.pid
EUn == <<hist, nextSid, nextLife, faults, envVars, orchVars, botVars, apprVars, humVars, fmVars,
         handsViol, inertViol, lifeViol, sendViol, histViol, botFinViol, hkViol, killViol, scopeViol>>
HookStartOf(p) ==
  /\ ~p.started /\ ~p.blocked /\ p.owner # NoId
  /\ procs' = (procs \ {p}) \cup {[p EXCEPT !.started = TRUE]}
  /\ NoteIf(~StartOK(p), "hook_ignored")
  /\ row' = IF row[p.owner].st # "none" /\ StartOK(p)
            THEN [row EXCEPT ![p.owner].st = "live", ![p.owner].pid = p.pid, ![p.owner].sessId = TRUE,
                             ![p.owner].tok = IF A1TokenKept THEN @ ELSE NoTok]   \* A1 control
            ELSE row
  /\ UNCHANGED sess /\ UNCHANGED EUn
HookStart == \E p \in procs : HookStartOf(p)
HookStartS(k) == \E p \in procs : p.pid = k /\ HookStartOf(p)
HookEnd ==
  \E p \in procs :
    /\ p.started /\ ~p.ending /\ p.owner # NoId
    /\ procs' = (procs \ {p}) \cup {[p EXCEPT !.ending = TRUE]}
    /\ NoteIf(~EndOK(p), "hook_ignored")
    /\ row' = IF row[p.owner].st # "none" /\ EndOK(p)
              THEN [row EXCEPT ![p.owner].st = "ended", ![p.owner].ey = TRUE, ![p.owner].ee = nextSid]
              ELSE row
    /\ UNCHANGED sess /\ UNCHANGED EUn
ProcExit == \E p \in procs : p.ending /\ ProcGone(p) /\ UNCHANGED row /\ UNCHANGED EU
Crash    == \E p \in procs : ProcGone(p) /\ UNCHANGED row /\ UNCHANGED EU
\* (b.66h: under RelaunchAny the orchestrator's agent writes a transcript
\* too, so its row can be resumed.)
Message ==
  \E i \in RelaunchIds :
    /\ row[i].st = "live" /\ ~row[i].tx
    /\ \E p \in procs : p.owner = i /\ p.started /\ ~p.ending
    /\ row' = [row EXCEPT ![i].tx = TRUE]
    /\ UNCHANGED TW /\ UNCHANGED EU
Age ==
  \E s \in sess :
    /\ s.young
    /\ sess' = (sess \ {s}) \cup {[s EXCEPT !.young = FALSE]}
    /\ UNCHANGED <<row, procs>> /\ UNCHANGED EU
AgeEnd ==
  \E i \in Ids : row[i].ey /\ row' = [row EXCEPT ![i].ey = FALSE] /\ UNCHANGED TW /\ UNCHANGED EU
AgeRowI(i) ==
  /\ row[i].fresh /\ ~InLaunch(i)
  /\ row' = [row EXCEPT ![i].fresh = FALSE]
  /\ UNCHANGED TW /\ UNCHANGED EU
AgeRow == \E i \in Ids : AgeRowI(i)
EUs == <<hist, nextLife, faults, orchVars, botVars, apprVars, humVars, fmVars, ghostVars>>
\* A stray session (no label) running a non-agent program.
Stray ==
  /\ AllowStray /\ ~strayUsed /\ CanCreate
  /\ \E nm \in Names : Named(nm) = {} /\
       sess' = sess \cup {[NoSess EXCEPT !.tid = NextTid, !.sc = nextSid + 1, !.name = nm,
                                         !.pn = NextTid, !.young = TRUE]}
  /\ procs' = procs \cup {[pid |-> nextSid + 1, pn |-> NextTid, owner |-> NoId, life |-> 0, conv |-> 0,
                           res |-> FALSE, started |-> FALSE, blocked |-> FALSE, ending |-> FALSE,
                           gl |-> <<NoId, NoTok>>]}
  /\ nextSid' = nextSid + 1 /\ strayUsed' = TRUE
  /\ UNCHANGED <<row, wall, leftUsed, provVars>> /\ UNCHANGED EUs
\* A worker left over from an earlier life (its row deleted): its session
\* carries a valid old label; the agent has reported in.
Leftover ==
  /\ AllowLeftover /\ ~leftUsed /\ CanCreate
  /\ \E i \in Ids, blk \in BOOLEAN :
       LET nm == IF i = BotId THEN BotName ELSE CHOOSE x \in OrchNames : TRUE IN
       /\ row[i].st = "none" /\ AgentProcs(i) = {} /\ Named(nm) = {}
       /\ IF i = BotId THEN EnableBot ELSE EnableOrch
       /\ sess' = sess \cup {[tid |-> NextTid, sc |-> nextSid + 1, name |-> nm,
                              lab |-> IF LeftoverLabelled THEN Lab(i, <<9, 9>>, NextTid) ELSE NoLab,
                              pn |-> NextTid, young |-> FALSE,
                              gl |-> <<i, <<9, 9>>>>, view |-> FALSE]}
       /\ procs' = procs \cup {[pid |-> nextSid + 1, pn |-> NextTid, owner |-> i, life |-> 0, conv |-> 0,
                                res |-> FALSE, started |-> TRUE, blocked |-> blk, ending |-> FALSE,
                                gl |-> <<i, <<9, 9>>>>]}
  /\ nextSid' = nextSid + 1 /\ leftUsed' = TRUE
  /\ UNCHANGED <<row, wall, strayUsed, provVars>> /\ UNCHANGED EUs
\* decision 2: another agent-director store (another HOME) runs an agent with
\* the same caller-chosen id on this tmux server. Its label is valid but names
\* store "s2"; its hooks go to its own store (owner NoId here).
OtherStore ==
  /\ AllowOtherStore /\ CanCreate
  /\ \E i \in Ids, nm \in Names :
       /\ Named(nm) = {} /\ (IF i = BotId THEN EnableBot ELSE EnableOrch)
       /\ ~\E s \in sess : s.lab.sto = "s2"
       /\ sess' = sess \cup {[tid |-> NextTid, sc |-> nextSid + 1, name |-> nm,
                              lab |-> LabS(i, <<7, 7>>, NextTid, "s2"), pn |-> NextTid, young |-> FALSE,
                              gl |-> <<NoId, <<7, 7>>>>, view |-> FALSE]}
       /\ procs' = procs \cup {[pid |-> nextSid + 1, pn |-> NextTid, owner |-> NoId, life |-> 0, conv |-> 0,
                                res |-> FALSE, started |-> TRUE, blocked |-> FALSE, ending |-> FALSE,
                                gl |-> <<NoId, <<7, 7>>>>]}
  /\ nextSid' = nextSid + 1
  /\ UNCHANGED <<row, wall, strayUsed, leftUsed, provVars>> /\ UNCHANGED EUs
\* A human or an agent renames a session (the label stays).
Rename ==
  /\ AllowRename /\ ~renameUsed /\ Named(RenameTo) = {}
  /\ \E s \in sess : sess' = (sess \ {s}) \cup {[s EXCEPT !.name = RenameTo]}
  /\ renameUsed' = TRUE /\ Note("renamed")
  /\ UNCHANGED <<row, procs, nextSid, wall, strayUsed, leftUsed, srv, sbase, groupUsed, respawnUsed,
                 restartUsed>>
  /\ UNCHANGED <<hist, nextLife, faults, orchVars, botVars, apprVars, humVars, fmVars,
                 handsViol, inertViol, lifeViol, sendViol, histViol, botFinViol, hkViol, killViol, scopeViol>>
\* A grouped (viewing) session: shares the pane, carries no label (B3).
Group ==
  /\ AllowGroup /\ ~groupUsed /\ CanCreate
  /\ \E s \in sess, nm \in Names :
       /\ ~s.view /\ Named(nm) = {}
       /\ sess' = sess \cup {[tid |-> NextTid, sc |-> nextSid + 1, name |-> nm, lab |-> NoLab,
                              pn |-> s.pn, young |-> TRUE, gl |-> <<NoId, NoTok>>, view |-> TRUE]}
  /\ nextSid' = nextSid + 1 /\ groupUsed' = TRUE /\ Note("grouped")
  /\ UNCHANGED <<row, procs, wall, strayUsed, leftUsed, srv, sbase, renameUsed, respawnUsed, restartUsed>>
  /\ UNCHANGED <<hist, nextLife, faults, orchVars, botVars, apprVars, humVars, fmVars,
                 handsViol, inertViol, lifeViol, sendViol, histViol, botFinViol, hkViol, killViol, scopeViol>>
\* respawn-pane -k: a pane shown by some session now runs something else.
Respawn ==
  /\ AllowRespawn /\ ~respawnUsed /\ CanCreate
  /\ \E s \in sess :
       /\ procs' = {p \in procs : p.pn # s.pn} \cup
                   {[pid |-> nextSid + 1, pn |-> s.pn, owner |-> NoId, life |-> 0, conv |-> 0,
                     res |-> FALSE, started |-> FALSE, blocked |-> FALSE, ending |-> FALSE,
                     gl |-> <<NoId, NoTok>>]}
  /\ nextSid' = nextSid + 1 /\ respawnUsed' = TRUE /\ Note("respawned")
  /\ UNCHANGED <<row, sess, wall, strayUsed, leftUsed, srv, sbase, renameUsed, groupUsed, restartUsed>>
  /\ UNCHANGED <<hist, nextLife, faults, orchVars, botVars, apprVars, humVars, fmVars,
                 handsViol, inertViol, lifeViol, sendViol, histViol, botFinViol, hkViol, killViol, scopeViol>>
\* The tmux server restarts: every session and every process in it dies;
\* session and pane ids restart.
ServerRestart ==
  /\ AllowRestart /\ ~restartUsed
  /\ sess' = {} /\ procs' = {} /\ srv' = srv + 1 /\ sbase' = nextSid /\ restartUsed' = TRUE
  /\ Note("restarted")
  /\ UNCHANGED <<row, nextSid, wall, strayUsed, leftUsed, renameUsed, groupUsed, respawnUsed>>
  /\ UNCHANGED <<hist, nextLife, faults, orchVars, botVars, apprVars, humVars, fmVars,
                 handsViol, inertViol, lifeViol, sendViol, histViol, botFinViol, hkViol, killViol, scopeViol>>
\* A lookup that finds the current label adopts a lost reply's server
\* identity and pane (A6): the pane of the SessionStart pid, else the
\* session's pane.
Adopt ==
  /\ A6RecordedSock
  /\ \E i \in Ids :
       /\ row[i].st \in Live /\ (row[i].sv = 0 \/ row[i].pn = 0)
       /\ LET lk == LookupN(i)
              pp == IF \E p \in procs : p.pn = lk.s.pn THEN (CHOOSE p \in procs : p.pn = lk.s.pn).pid ELSE 0
          IN /\ lk.v = "ours"
             /\ row' = [row EXCEPT ![i].sv = srv, ![i].pn = lk.s.pn, ![i].ppid = pp, ![i].ver = @ + 1]
  /\ Note("adopted")
  /\ UNCHANGED TW /\ UNCHANGED <<nextSid, envVars>>
  /\ UNCHANGED <<hist, nextLife, faults, orchVars, botVars, apprVars, humVars, fmVars,
                 handsViol, inertViol, lifeViol, sendViol, histViol, botFinViol, hkViol, killViol, scopeViol>>

-----------------------------------------------------------------------------
(* Orchestrator on id "o" *)
OU == <<hist, envVars, botVars, apprVars, humVars, fmVars>>
NoGk == <<handsViol, inertViol, lifeViol, sendViol, histViol, botFinViol, hkViol, killViol, scopeViol>>

O_Spawn ==
  /\ EnableOrch /\ opc = "idle" /\ CanInsert(OrchId) /\ ~ScanBlocks(OrchId)
  /\ \E nm \in OrchNames :
       /\ InsertRow(OrchId, nm)
       /\ opc' = "launch" /\ oname' = nm /\ olife' = nextLife + 1
  /\ otaint' = FALSE /\ oreuse' = FALSE
  /\ UNCHANGED <<TW, nextSid, faults, okt, over, osave, oobs, okl, OU, ghostVars>>

O_ReuseLookup ==
  /\ EnableOrch /\ opc = "idle"
  /\ row[OrchId].st \in Terminal /\ nextLife < MaxLife
  /\ \E nm \in OrchNames, k \in LChoicesK :
       LET cls == PreLaunch(OrchId, Lookup(OrchId, k), nm) IN
       /\ SpendK(k)
       /\ IF cls = "proceed"
          THEN /\ opc' = "reuse_reset" /\ oname' = nm /\ over' = row[OrchId].ver
               /\ olife' = row[OrchId].life /\ otaint' = FALSE
          ELSE /\ opc' = "idle" /\ otaint' = (otaint \/ Ghosts) /\ UNCHANGED <<oname, over, olife>>
  /\ UNCHANGED <<row, TW, nextSid, nextLife, okt, oreuse, osave, oobs, okl, OU, ghostVars>>

O_ReuseReset ==
  /\ opc = "reuse_reset"
  /\ IF row[OrchId].st \in Terminal /\ Snap(OrchId) = <<over, olife>>
     THEN /\ row' = [row EXCEPT ![OrchId] = NewRow(oname, nextLife + 1, over + 1)]
          /\ hist' = IF row[OrchId].sessId /\ row[OrchId].tx
                     THEN [hist EXCEPT ![OrchId] = @ \cup {row[OrchId].life}] ELSE hist
          /\ osave' = row[OrchId]
          /\ nextLife' = nextLife + 1 /\ olife' = nextLife + 1 /\ over' = over + 1
          /\ opc' = "launch" /\ oreuse' = TRUE
          /\ inertViol' = (inertViol \/ (Ghosts /\ (otaint)))
     ELSE /\ opc' = "idle" /\ UNCHANGED <<row, hist, nextLife, olife, over, oreuse, osave, inertViol>>
  /\ UNCHANGED <<TW, nextSid, faults, otaint, oname, okt, oobs, okl, envVars, botVars, apprVars, humVars,
                 fmVars, handsViol, lifeViol, sendViol, histViol, botFinViol, hkViol, killViol, scopeViol, seen>>

\* new-session. Plain spawn: "duplicate session" ends the row (HELD); a
\* failure leaves it pending. Reuse: failure or duplicate -> restore.
O_Launch ==
  /\ opc = "launch"
  /\ \E o \in CChoices :
       /\ SpendC(o)
       /\ IF Named(oname) # {}
          THEN LET cls == DupClass(OrchId, oname) IN
               /\ UNCHANGED <<TW, nextSid>>
               /\ row' = IF oreuse THEN Restore(OrchId, osave, over, olife) ELSE EndLaunch(OrchId, olife)
               /\ otaint' = (otaint \/ (Ghosts /\ (cls = "conflict")))
               /\ Note(IF ~oreuse /\ cls = "conflict" THEN "dup_conflict" ELSE "dup_other")
          ELSE LET c == Create(OrchId, oname, olife, olife, FALSE, o) IN
               /\ c.tick => CanCreate                  \* F1: creations within MaxSid (as v3)
               /\ DoCreate(c)
               /\ row' = IF ~c.made /\ oreuse /\ o # "tmo_none" THEN Restore(OrchId, osave, over, olife)
                         ELSE c.row
               /\ UNCHANGED otaint
               /\ Note(IF o = "labfail" THEN "labfail" ELSE IF o = "tmo_made" THEN "lost_reply" ELSE "created")
  /\ opc' = "run" /\ osave' = NoRow
  \* b.66h: the orchestrator reads the launch id its launch left on the row
  \* (from spawn's result, or a get after a failure).
  /\ oobs' = IF LaunchObs THEN [lk |-> row'[OrchId].lk, tok |-> row'[OrchId].tok] ELSE oobs
  /\ UNCHANGED <<hist, nextLife, oname, olife, okt, over, oreuse, okl, OU, NoGk>>

\* kill, step 1: the lookup (and the pane listing) -> a target, or an answer.
\* b.66h: the compare first; "changed" makes no lookup and no tmux call.
O_KillLookup ==
  /\ opc = "run" /\ row[OrchId].st # "none"
  /\ IF Changed(OrchId, oobs)
     THEN /\ opc' = "reread" /\ Note("launch_changed")
          /\ UNCHANGED <<faults, otaint, okt, okl>>
     ELSE /\ \E k \in LChoicesK :
               /\ SpendK(k)
               /\ IF row[OrchId].st \in Terminal
                  THEN opc' = "killed" /\ UNCHANGED <<otaint, okt>>
                  ELSE LET lk == Lookup(OrchId, k)
                           ap == AgentPid(OrchId) IN
                       CASE lk.v \in {"leftover", "cant"} ->                     \* P1 / Can't tell
                                opc' = "run" /\ otaint' = (otaint \/ Ghosts) /\ UNCHANGED okt
                         [] lk.v = "ours" ->
                                opc' = "kill_act" /\ UNCHANGED otaint
                                /\ okt' = [pn |-> PaneTarget(OrchId, lk), pid |-> KillPid(OrchId, lk),
                                           tid |-> lk.s.tid, sv |-> srv]
                         [] OTHER ->                                              \* Gone
                                IF wall \/ ap = 0 \/ ~ProcAlive(ap)
                                THEN opc' = "killed" /\ UNCHANGED <<otaint, okt>>
                                ELSE IF A5PaneKill /\ RecPane(OrchId) /\ row[OrchId].sv = srv
                                THEN opc' = "kill_act" /\ UNCHANGED otaint
                                     /\ okt' = [pn |-> row[OrchId].pn, pid |-> ap, tid |-> 0, sv |-> srv]
                                ELSE IF A5PaneKill THEN opc' = "run" /\ otaint' = (otaint \/ Ghosts) /\ UNCHANGED okt
                                ELSE opc' = "killed" /\ UNCHANGED <<otaint, okt>>
          \* b.66h: the launch this lookup read, for the act's ghosts
          /\ okl' = IF RelaunchAny THEN Obs(OrchId) ELSE okl
          /\ UNCHANGED seen
  /\ UNCHANGED <<row, TW, hist, nextSid, nextLife, oname, olife, over, oreuse, osave, oobs, OU, NoGk>>
\* b.66h: after ErrLaunchChanged the orchestrator reads the row again, then
\* decides again (O_KillLookup with the new launch, or O_Finished).
O_Reread ==
  /\ opc = "reread"
  /\ opc' = "run" /\ Observe(oobs, OrchId)
  /\ UNCHANGED <<row, TW, hist, nextSid, nextLife, faults, otaint, oname, olife, okt, over, oreuse, osave, okl,
                 OU, ghostVars>>

\* The act on a pane id / session id. ActPidCheck: the pane still shows the
\* pid seen by the listing and the server is the same (one step).
ActPane(t)  == IF t.pn = 0 THEN FALSE
               ELSE IF ActPidCheck THEN srv = t.sv /\ \E p \in procs : p.pn = t.pn /\ p.pid = t.pid
               ELSE TRUE
ActWorld(t) ==
  LET W1 == IF A5PaneKill /\ ActPane(t) THEN AfterPane(sess, procs, t.pn) ELSE <<sess, procs>>
      T  == {x \in W1[1] : x.tid = t.tid}
      W2 == IF t.tid # 0 /\ T # {} /\ (~ActPidCheck \/ srv = t.sv)
            THEN AfterSess(W1[1], W1[2], CHOOSE x \in T : TRUE) ELSE W1
  IN W2
\* b.66h: the launch the kill's lookup read and its label (the row's
\* current ones unless RelaunchAny, when a relaunch can fall in between).
OKillL == IF RelaunchAny THEN <<OrchId, okl.lk>> ELSE CurLaunch(OrchId)
OKillLabel(x) == IF RelaunchAny THEN LabValid(x) /\ StoreOK(x) /\ x.lab.id = OrchId /\ x.lab.tok = okl.tok
                 ELSE CurLabel(OrchId, x)
O_KillAct ==
  /\ opc = "kill_act"
  /\ \E f \in FaultChoices :
       LET W    == IF f THEN <<sess, procs>> ELSE ActWorld(okt)
           gone == IF ~A5PaneKill THEN TRUE
                   ELSE IF wall \/ okt.pid = 0 THEN ~\E x \in W[1] : OKillLabel(x)
                   ELSE ~\E p \in W[2] : p.pid = okt.pid
           r    == IF gone THEN "ok" ELSE "killfailed"
       IN /\ Spend(f)
          /\ sess' = W[1] /\ procs' = W[2]
          /\ KillHandsL(OKillL, W[2]) /\ KillHonestL(OrchId, OKillL, r, W[2])
          /\ ScopeG(ObsLaunch(OrchId, oobs), procs \ W[2])
          /\ IF r = "ok" THEN opc' = "killed" /\ UNCHANGED otaint ELSE opc' = "run" /\ otaint' = (otaint \/ Ghosts)
  /\ UNCHANGED <<row, hist, nextSid, nextLife, oname, olife, okt, over, oreuse, osave, oobs, okl, OU,
                 inertViol, lifeViol, sendViol, histViol, botFinViol, hkViol, seen>>

O_AfterKill ==
  /\ opc = "killed"
  /\ row' = SweepRows /\ opc' = "idle"
  /\ UNCHANGED <<TW, hist, nextSid, nextLife, faults, otaint, oname, olife, okt, over, oreuse, osave,
                 oobs, okl, OU, ghostVars>>
O_Finished ==
  /\ opc = "run" /\ row[OrchId].st \in Terminal
  /\ opc' = "idle"
  /\ UNCHANGED <<row, TW, hist, nextSid, nextLife, faults, otaint, oname, olife, okt, over, oreuse, osave,
                 oobs, okl, OU, ghostVars>>

-----------------------------------------------------------------------------
(* Bot server on id "b", name "n" *)
BK == <<envVars, orchVars, apprVars, humVars, fmVars>>

B_Start ==
  /\ EnableBot /\ bpc = "idle" /\ ~latch
  /\ bpc' = "spawn" /\ btaint' = FALSE
  /\ UNCHANGED <<row, TW, hist, nextSid, nextLife, faults, BK, blife, bver, latch, bconv, bsave, bobs, ghostVars>>
B_Unlatch ==
  /\ latch /\ row[BotId].st = "live"
  /\ latch' = FALSE
  /\ UNCHANGED <<row, TW, hist, nextSid, nextLife, faults, BK, bpc, btaint, blife, bver, bconv, bsave, bobs, ghostVars>>
B_SpawnAt(pc, onColl, onIns) ==
  /\ bpc = pc
  /\ IF CanInsert(BotId) /\ ~ScanBlocks(BotId)
     THEN /\ InsertRow(BotId, BotName) /\ bpc' = onIns /\ blife' = nextLife + 1
     ELSE IF CanInsert(BotId)
     THEN /\ bpc' = "idle" /\ UNCHANGED <<row, nextLife, blife>>           \* scan: CONFLICT
     ELSE /\ row[BotId].st # "none"
          /\ bpc' = onColl /\ UNCHANGED <<row, nextLife, blife>>
  /\ UNCHANGED <<TW, hist, nextSid, faults, BK, btaint, bver, latch, bconv, bsave, bobs, ghostVars>>
B_Spawn      == B_SpawnAt("spawn", "get", "launch")
B_RetrySpawn == B_SpawnAt("retry_spawn", "idle", "retry_launch")

B_LaunchAt(pc) ==
  /\ bpc = pc
  /\ \E o \in CChoices :
       /\ SpendC(o)
       /\ IF Named(BotName) # {}
          THEN LET cls == DupClass(BotId, BotName) IN
               /\ UNCHANGED <<TW, nextSid>>
               /\ row' = EndLaunch(BotId, blife)
               /\ latch' = (latch \/ cls = "conflict")
               /\ btaint' = (btaint \/ (Ghosts /\ (cls = "conflict")))
               /\ seen' = IF ~Probes THEN seen
                          ELSE seen \cup {IF cls = "conflict" THEN "dup_conflict" ELSE "dup_other"}
                                    \cup (IF row' # row THEN {"held_end"} ELSE {})
          ELSE LET c == Create(BotId, BotName, blife, blife, FALSE, o) IN
               /\ c.tick => CanCreate                  \* F1: creations within MaxSid (as v3)
               /\ DoCreate(c) /\ row' = c.row
               /\ UNCHANGED <<latch, btaint>>
               /\ Note(IF o = "labfail" THEN (IF c.made THEN "labfail_left" ELSE "labfail_killed")
                       ELSE IF o = "tmo_made" THEN "lost_reply" ELSE "created")
  /\ bpc' = "idle"
  /\ UNCHANGED <<hist, nextLife, BK, blife, bver, bconv, bsave, bobs, NoGk>>
B_Launch      == B_LaunchAt("launch")
B_RetryLaunch == B_LaunchAt("retry_launch")

\* (b.66h: the bot reads the row's launch id here; it scopes the kill or
\* the keys it then sends.)
B_Get ==
  /\ bpc = "get"
  /\ bpc' \in CASE row[BotId].st = "none"     -> {"retry_spawn"}
               [] row[BotId].st \in Terminal -> {IF ResumeEnabled THEN "resume" ELSE "nr_kill"}
               [] row[BotId].st = "live"     -> {"reconnect"}
               [] row[BotId].st = "pending"  -> {"idle", "nr_kill"}
  /\ Observe(bobs, BotId)
  /\ UNCHANGED <<row, TW, hist, nextSid, nextLife, faults, BK, btaint, blife, bver, latch, bconv, bsave, ghostVars>>

\* reconnect over send-keys: keys go to the agent's pane. b.66h: the keys
\* carry the launch id the bot read; on "changed" no tmux call is made (so
\* no tmux fault either) and the bot reads the row again (B_Get).
B_Reconnect ==
  /\ bpc = "reconnect" /\ row[BotId].st = "live"
  /\ \E g \in GChoices :
       LET k == SendOut(BotId, g, bobs) IN
       /\ k.r = "changed" => g = "none"
       /\ SpendG(g)
       /\ bpc' = CASE k.r = "gone"    -> "fm_then_resume"
                   [] k.r = "changed" -> "get"
                   [] OTHER           -> "idle"
       /\ latch' = (latch \/ k.r = "conflict")
       /\ handsViol' = (handsViol \/ (Ghosts /\ ((k.r = "ok" /\ \E p \in PaneProcs(k.p) : ~InCurPane(BotId, p)))))
       /\ ScopeG(ObsLaunch(BotId, bobs), IF k.r = "ok" THEN PaneProcs(k.p) ELSE {})
       /\ NoteIf(k.r = "changed", "launch_changed")
  /\ UNCHANGED <<row, TW, hist, nextSid, nextLife, BK, btaint, blife, bver, bconv, bsave, bobs,
                 inertViol, lifeViol, sendViol, histViol, botFinViol, hkViol, killViol>>
\* (b.66h: the compare comes before send-keys' state guard, so a scoped send
\* to a row that is no longer live and no longer the launch read is refused
\* as "changed".)
B_SendGuard ==
  /\ bpc = "reconnect" /\ row[BotId].st # "live"
  /\ bpc' = IF Changed(BotId, bobs) THEN "get" ELSE "idle"
  /\ NoteIf(Changed(BotId, bobs), "launch_changed")
  /\ UNCHANGED <<row, TW, hist, nextSid, nextLife, faults, BK, btaint, blife, bver, latch, bconv, bsave, bobs, NoGk>>
B_FindMissing ==
  /\ bpc = "fm_then_resume"
  /\ row' = SweepRows /\ bpc' = "resume"
  /\ UNCHANGED <<TW, hist, nextSid, nextLife, faults, BK, btaint, blife, bver, latch, bconv, bsave, bobs, ghostVars>>

B_Resume ==
  /\ bpc = "resume"
  /\ blife' = row[BotId].life
  /\ Observe(bobs, BotId)          \* b.66h: a live row goes to nr_kill, scoped to this launch
  /\ LET sel == Sel(BotId) IN
     CASE row[BotId].st = "none" ->
            bpc' = "retry_spawn" /\ UNCHANGED <<faults, btaint, bver, latch, bconv, histViol, seen>>
       [] row[BotId].st = "pending" ->
            /\ bpc' = "idle" /\ UNCHANGED <<faults, btaint, bver, latch, bconv, histViol>>
            /\ Note("resume_pending_refused")
       [] row[BotId].st = "live" ->
            bpc' = "nr_kill" /\ UNCHANGED <<faults, btaint, bver, latch, bconv, histViol, seen>>
       [] ~row[BotId].sessId \/ sel = 0 ->
            /\ bpc' = "reuse" /\ UNCHANGED <<faults, btaint, bver, latch, bconv, histViol>>
            /\ Note(IF \E e \in hist[BotId] : e # row[BotId].life THEN "hist_refused" ELSE "jsonl")
       [] OTHER ->
            \E k \in LChoicesK :
              LET lk  == Lookup(BotId, k)
                  cls == PreLaunch(BotId, lk, row[BotId].name) IN
              /\ SpendK(k)
              /\ histViol' = (histViol \/ (Ghosts /\ (sel # row[BotId].life)))
              /\ bconv' = IF Ghosts THEN sel ELSE 0
              /\ bpc' = IF cls = "proceed" THEN "claim" ELSE "idle"
              /\ bver' = row[BotId].ver
              /\ btaint' = (btaint \/ (Ghosts /\ (cls \in {"unavail", "conflict"})))
              /\ latch' = (latch \/ cls = "conflict")
              /\ Note(IF lk.v = "ours" THEN "own_refused" ELSE IF lk.v = "leftover" THEN "leftover_refused"
                      ELSE "resume_other")
  /\ UNCHANGED <<row, TW, hist, nextSid, nextLife, BK, bsave,
                 handsViol, inertViol, lifeViol, sendViol, botFinViol, hkViol, killViol, scopeViol>>

B_Claim ==
  /\ bpc = "claim"
  /\ IF row[BotId].st \in Terminal /\ Snap(BotId) = <<bver, blife>>
     THEN /\ row' = Claim(BotId, bver) /\ bsave' = row[BotId] /\ bver' = bver + 1
          /\ bpc' = "rlaunch"
     ELSE /\ UNCHANGED <<row, bsave, bver>> /\ bpc' = "idle"
  /\ UNCHANGED <<TW, hist, nextSid, nextLife, faults, BK, btaint, blife, latch, bconv, bobs, ghostVars>>

\* resume's launch: ok / timeout -> stays pending; failure or duplicate ->
\* conditional restore.
\* (b.66h: ResumeLaunchI is the same for row i; the human relauncher's row
\* may be the orchestrator's under RelaunchAny.)
ResumeLaunchI(i, cv, save, v, l) ==
  \E o \in CChoices :
    /\ SpendC(o)
    /\ IF Named(row[i].name) # {}
       THEN /\ UNCHANGED <<TW, nextSid>> /\ row' = Restore(i, save, v, l)
       ELSE LET c == Create(i, row[i].name, row[i].life, cv, TRUE, o) IN
            /\ c.tick => CanCreate                  \* F1: creations within MaxSid (as v3)
            /\ DoCreate(c)
            /\ row' = IF ~c.made /\ o # "tmo_none" THEN Restore(i, save, v, l) ELSE c.row
ResumeLaunch(cv, save, v, l) == ResumeLaunchI(BotId, cv, save, v, l)
ResumeDupLatch == Named(row[BotId].name) # {} /\ DupClass(BotId, row[BotId].name) = "conflict"
B_RLaunch ==
  /\ bpc = "rlaunch"
  /\ ResumeLaunch(bconv, bsave, bver, blife)
  /\ latch' = (latch \/ ResumeDupLatch)
  /\ bpc' = "idle" /\ bsave' = NoRow
  /\ Note(IF nextSid' > nextSid THEN "bot_resume_launched"
          ELSE IF row' # row THEN "resume_restored" ELSE "bot_resume_nolaunch")
  /\ UNCHANGED <<hist, nextLife, BK, btaint, blife, bver, bconv, bobs, NoGk>>

B_Crash ==
  /\ EnableCrash
  /\ bpc \in {"launch", "retry_launch", "reuse_launch", "rlaunch"}
  /\ faults < MaxFaults /\ faults' = faults + 1
  /\ bpc' = "idle" /\ bsave' = NoRow
  /\ NoteIf(bpc = "rlaunch", "resume_crashed")
  /\ UNCHANGED <<row, TW, hist, nextSid, nextLife, BK, btaint, blife, bver, latch, bconv, bobs, NoGk>>

\* b.66h: the kill carries the launch id the bot read (B_Get or B_Resume);
\* on "changed" no tmux call is made and the bot reads the row again.
B_NrKill ==
  /\ bpc = "nr_kill"
  /\ \E g \in GChoices :
       LET k == KillOut(BotId, g, bobs) IN
       /\ k.r = "changed" => g = "none"
       /\ SpendG(g)
       /\ sess' = k.S /\ procs' = k.P
       /\ KillHands(BotId, k.P) /\ KillHonestG(BotId, k.r, k.P)
       /\ ScopeG(ObsLaunch(BotId, bobs), procs \ k.P)
       /\ botFinViol' = (botFinViol \/ (Ghosts /\ (\E p \in procs \ k.P : p.owner \in Ids /\ row[p.owner].st \in Terminal)))
       /\ btaint' = (btaint \/ (Ghosts /\ (k.r \in {"unavail", "conflict", "killfailed"})))
       /\ bpc' = CASE k.r = "ok"       -> "nr_fm"
                   [] k.r = "notfound" -> "retry_spawn"
                   [] k.r = "changed"  -> "get"
                   [] OTHER            -> "idle"
       /\ latch' = (latch \/ k.r = "conflict")
       /\ seen' = IF ~Probes THEN seen
                  ELSE seen \cup (IF row[BotId].st = "pending" /\ k.r = "conflict" THEN {"p1_conflict"} ELSE {})
                            \cup (IF k.r = "killfailed" THEN {"kill_failed"} ELSE {})
                            \cup (IF k.r = "ok" /\ row[BotId].st \in Live /\ k.P # procs /\
                                     \E s \in sess : s.view THEN {"kill_with_viewer"} ELSE {})
                            \cup (IF k.r = "changed" THEN {"launch_changed"} ELSE {})
  /\ UNCHANGED <<row, hist, nextSid, nextLife, BK, blife, bver, bconv, bsave, bobs,
                 inertViol, lifeViol, sendViol, histViol, hkViol>>
B_NrFm ==
  /\ bpc = "nr_fm"
  /\ row' = SweepRows /\ bpc' = "nr_get"
  /\ UNCHANGED <<TW, hist, nextSid, nextLife, faults, BK, btaint, blife, bver, latch, bconv, bsave, bobs, ghostVars>>
B_NrGet ==
  /\ bpc = "nr_get"
  /\ bpc' = CASE row[BotId].st \in Terminal -> "reuse"
              [] row[BotId].st = "none"     -> "retry_spawn"
              [] OTHER                      -> "idle"
  /\ UNCHANGED <<row, TW, hist, nextSid, nextLife, faults, BK, btaint, blife, bver, latch, bconv, bsave, bobs, ghostVars>>

B_Reuse ==
  /\ bpc = "reuse"
  /\ IF row[BotId].st = "none"
     THEN /\ CanInsert(BotId)
          /\ IF ScanBlocks(BotId)
             THEN bpc' = "idle" /\ UNCHANGED <<row, nextLife, blife>>            \* scan: CONFLICT
             ELSE InsertRow(BotId, BotName) /\ blife' = nextLife + 1 /\ bpc' = "retry_launch"
          /\ UNCHANGED <<faults, bver, latch, btaint>>
     ELSE IF row[BotId].st \in Live
     THEN /\ bpc' = "idle" /\ UNCHANGED <<row, nextLife, faults, blife, bver, latch, btaint>>
     ELSE \E k \in LChoicesK :
            LET cls == PreLaunch(BotId, Lookup(BotId, k), BotName) IN
            /\ SpendK(k)
            /\ bpc' = IF cls = "proceed" THEN "reuse_reset" ELSE "idle"
            /\ bver' = row[BotId].ver /\ blife' = row[BotId].life
            /\ btaint' = (btaint \/ (Ghosts /\ (cls \in {"unavail", "conflict"})))
            /\ latch' = (latch \/ cls = "conflict")
            /\ UNCHANGED <<row, nextLife>>
  /\ UNCHANGED <<TW, hist, nextSid, BK, bconv, bsave, bobs, ghostVars>>
B_ReuseReset ==
  /\ bpc = "reuse_reset"
  /\ IF row[BotId].st \in Terminal /\ Snap(BotId) = <<bver, blife>> /\ nextLife < MaxLife
     THEN /\ row' = [row EXCEPT ![BotId] = NewRow(BotName, nextLife + 1, bver + 1)]
          /\ hist' = IF row[BotId].sessId /\ row[BotId].tx
                     THEN [hist EXCEPT ![BotId] = @ \cup {row[BotId].life}] ELSE hist
          /\ bsave' = row[BotId]
          /\ nextLife' = nextLife + 1 /\ blife' = nextLife + 1 /\ bver' = bver + 1
          /\ bpc' = "reuse_launch"
          /\ inertViol' = (inertViol \/ (Ghosts /\ (btaint)))
     ELSE /\ bpc' = "idle" /\ UNCHANGED <<row, hist, nextLife, blife, bver, bsave, inertViol>>
  /\ UNCHANGED <<TW, nextSid, faults, BK, btaint, latch, bconv, bobs,
                 handsViol, lifeViol, sendViol, histViol, botFinViol, hkViol, killViol, scopeViol, seen>>
B_ReuseLaunch ==
  /\ bpc = "reuse_launch"
  /\ \E o \in CChoices :
       /\ SpendC(o)
       /\ IF Named(BotName) # {}
          THEN LET cls == DupClass(BotId, BotName) IN
               /\ UNCHANGED <<TW, nextSid>>
               /\ row' = Restore(BotId, bsave, bver, blife)
               /\ latch' = (latch \/ cls = "conflict")
               /\ Note("restored")
          ELSE LET c == Create(BotId, BotName, blife, blife, FALSE, o) IN
               /\ c.tick => CanCreate                  \* F1: creations within MaxSid (as v3)
               /\ DoCreate(c) /\ UNCHANGED latch
               /\ row' = IF ~c.made /\ o # "tmo_none" THEN Restore(BotId, bsave, bver, blife) ELSE c.row
               /\ Note(IF ~c.made /\ o # "tmo_none" THEN "restored" ELSE "reuse_created")
  /\ bpc' = "idle" /\ bsave' = NoRow
  /\ UNCHANGED <<hist, nextLife, BK, btaint, blife, bver, bconv, bobs, NoGk>>

\* The startup-prompt approver: send-keys --allow-pending on the bot's
\* pending row. Step 1: the lookup and the pane listing; step 2: the keys to
\* that pane id (ActPidCheck: in the same step as a pid/server check).
ApproverTarget ==
  LET lk == LookupN(BotId) IN
  IF row[BotId].st = "pending" /\ lk.v = "ours" /\ (ResumeSendOK \/ ~row[BotId].sessId)
  THEN PaneTarget(BotId, lk) ELSE 0
B_ApproveLookup ==
  /\ EnableBot /\ apc = "idle"
  /\ LET tp == ApproverTarget IN
     /\ tp # 0 /\ \E p \in procs : p.pn = tp
     /\ LET p == CHOOSE x \in procs : x.pn = tp IN
        /\ apc' = "act" /\ atg' = [pn |-> tp, pid |-> p.pid, tid |-> 0, sv |-> srv]
        \* ground truth: this launch's agent, not yet reported in
        /\ sendViol' = (sendViol \/ (Ghosts /\ (~InCurPane(BotId, p) \/ (p.owner # NoId /\ (p.started \/ p.ending)))))
  /\ Note(IF row[BotId].sessId THEN "approve_resumed" ELSE "approve_pending")
  /\ UNCHANGED <<row, TW, hist, nextSid, nextLife, faults, envVars, orchVars, botVars, humVars, fmVars,
                 handsViol, inertViol, lifeViol, histViol, botFinViol, hkViol, killViol, scopeViol>>
B_ApproveAct ==
  /\ apc = "act"
  /\ LET ok == IF ActPidCheck THEN srv = atg.sv /\ \E p \in procs : p.pn = atg.pn /\ p.pid = atg.pid
               ELSE TRUE
         T  == IF ok THEN PaneProcs(atg.pn) ELSE {} IN
     /\ procs' = (procs \ T) \cup {[p EXCEPT !.blocked = FALSE] : p \in T}
     /\ handsViol' = (handsViol \/ (Ghosts /\ (\E p \in T : ~InCurPane(BotId, p))))
     /\ NoteIf(\E p \in T : p.started, "approve_after_start")
  /\ apc' = "idle" /\ atg' = NoTarget
  /\ UNCHANGED <<row, sess, hist, nextSid, nextLife, faults, envVars, orchVars, botVars, humVars, fmVars,
                 inertViol, lifeViol, sendViol, histViol, botFinViol, hkViol, killViol, scopeViol>>

-----------------------------------------------------------------------------
(* Humans *)
HU == <<envVars, orchVars, botVars, apprVars, fmVars>>

\* (b.66h: the human resumes row hid, chosen here; always BotId unless
\* RelaunchAny.)
H_Resume ==
  /\ EnableHumanResume /\ hpc = "idle"
  /\ \E i \in RelaunchIds :
       /\ row[i].st \in Terminal /\ row[i].sessId /\ Sel(i) # 0
       /\ PreLaunch(i, LookupN(i), row[i].name) = "proceed"
       /\ hid' = i
       /\ hpc' = "claim" /\ hver' = Snap(i) /\ hconv' = IF Ghosts THEN Sel(i) ELSE 0
       /\ histViol' = (histViol \/ (Ghosts /\ (Sel(i) # row[i].life)))
  /\ UNCHANGED <<row, TW, hist, nextSid, nextLife, faults, HU, hsave, kpc, kid, ktg,
                 handsViol, inertViol, lifeViol, sendViol, botFinViol, hkViol, killViol, scopeViol, seen>>
H_Claim ==
  /\ hpc = "claim"
  /\ IF row[hid].st \in Terminal /\ Snap(hid) = hver
     THEN /\ row' = Claim(hid, hver[1]) /\ hsave' = row[hid]
          /\ hver' = <<hver[1] + 1, hver[2]>> /\ hpc' = "rlaunch"
     ELSE /\ UNCHANGED <<row, hsave, hver>> /\ hpc' = "done"
  /\ UNCHANGED <<TW, hist, nextSid, nextLife, faults, HU, hconv, kpc, kid, ktg, hid, ghostVars>>
H_RLaunch ==
  /\ hpc = "rlaunch"
  /\ ResumeLaunchI(hid, hconv, hsave, hver[1], hver[2])
  /\ hpc' = "done" /\ hsave' = NoRow
  \* (b.66h tag: the relaunch lands between the orchestrator's kill lookup
  \* and its act; never with RelaunchAny off, where hid is BotId.)
  /\ seen' = IF ~Probes THEN seen
             ELSE seen \cup {IF nextSid' > nextSid THEN "human_resumed"
                             ELSE IF row' # row THEN "resume_restored" ELSE "human_resume_nolaunch"}
                       \cup (IF hid = OrchId /\ opc = "kill_act" /\ nextSid' > nextSid
                             THEN {"relaunch_in_kill_window"} ELSE {})
  /\ UNCHANGED <<hist, nextLife, HU, hver, hconv, kpc, kid, ktg, hid, NoGk>>
H_Crash ==
  /\ EnableCrash /\ hpc = "rlaunch"
  /\ faults < MaxFaults /\ faults' = faults + 1
  /\ hpc' = "done" /\ hsave' = NoRow
  /\ Note("resume_crashed")
  /\ UNCHANGED <<row, TW, hist, nextSid, nextLife, HU, hver, hconv, kpc, kid, ktg, hid, NoGk>>

\* kill --include-finished (operator only): Ours on a finished row past the
\* stopping window and the starting bound, reported in to this row (SR-6.7:
\* the row has a pid and the session was made before the end).
HK_Lookup ==
  /\ EnableHumanKill /\ kpc = "idle"
  /\ \E i \in Ids, k \in LChoicesK :
       LET lk == Lookup(i, k) IN
       /\ row[i].st \in Terminal /\ ~row[i].ey
       /\ lk.v = "ours" /\ ~lk.s.young
       /\ row[i].pid # 0 /\ lk.s.sc <= row[i].ee
       /\ SpendK(k)
       /\ kpc' = "act" /\ kid' = i
       /\ ktg' = [pn |-> PaneTarget(i, lk), pid |-> KillPid(i, lk), tid |-> lk.s.tid, sv |-> srv]
  /\ UNCHANGED <<row, TW, hist, nextSid, nextLife, HU, hpc, hver, hconv, hsave, hid, ghostVars>>
HK_Act ==
  /\ kpc = "act"
  /\ \E f \in FaultChoices :
       LET W == IF f THEN <<sess, procs>> ELSE ActWorld(ktg)
           K == procs \ W[2] IN
       /\ Spend(f)
       /\ sess' = W[1] /\ procs' = W[2]
       /\ handsViol' = (handsViol \/ (Ghosts /\ (\E p \in K : ~InCurPane(kid, p))))
       \* only the finished row's own last-launch agent, not young-starting,
       \* while the row is still finished and out of its stopping window
       /\ hkViol' = (hkViol \/ (Ghosts /\ (\E p \in K : ~InCurPane(kid, p) \/ row[kid].st \notin Terminal \/ row[kid].ey
                                       \/ (p.owner \in Ids /\ p.started /\ ~p.ending))))  \* a working agent
       /\ Note(IF K # {} THEN "human_kill_finished" ELSE "human_kill_nothing")
  /\ kpc' = "idle" /\ ktg' = NoTarget
  /\ UNCHANGED <<row, hist, nextSid, nextLife, HU, hpc, hver, hconv, hsave, kid, hid,
                 inertViol, lifeViol, sendViol, histViol, botFinViol, killViol, scopeViol>>
H_Unlatch ==
  /\ EnableHumanKill /\ latch
  /\ latch' = FALSE
  /\ UNCHANGED <<row, TW, hist, nextSid, nextLife, faults, envVars, orchVars, apprVars, fmVars, humVars,
                 bpc, btaint, blife, bver, bconv, bsave, bobs, ghostVars>>

-----------------------------------------------------------------------------
(* find-missing cron sweep and expire *)
FM_List ==
  /\ EnableSweeper /\ fpc = "idle"
  /\ LET snap == {[id |-> i, life |-> row[i].life, ver |-> row[i].ver] : i \in {j \in Ids : row[j].st \in Live}}
     IN /\ fsnap' = snap /\ fpc' = IF snap = {} THEN "idle" ELSE "check"
  /\ UNCHANGED <<row, TW, hist, nextSid, nextLife, faults, envVars, orchVars, botVars, apprVars,
                 humVars, ghostVars>>
FM_Check ==
  /\ fpc = "check"
  /\ \E e \in fsnap, k \in LChoicesK :
       /\ fsnap' = fsnap \ {e}
       /\ fpc' = IF fsnap \ {e} = {} THEN "idle" ELSE "check"
       /\ SpendK(k)
       /\ row' = IF row[e.id].st \in Live /\ FMDead(e.id, k) THEN MarkIf(e) ELSE row
       /\ LET marked(i) == row[i].st \in Live /\ row'[i].st = "missing"
              tags == (IF \E i \in Ids : marked(i) /\ row[i].st = "pending" /\ Holder(i, row[i].name) \notin {"free", "cur"}
                       THEN {"squat_gone_mark"} ELSE {})
                      \cup (IF \E i \in Ids : marked(i) /\ Evidence(i) = "dead" /\ \E s \in sess : CurLabel(i, s)
                            THEN {"dead_with_session"} ELSE {})
                      \cup (IF k \in {"rebound", "caller"} /\ row[e.id].sv # 0 THEN {"differs_cant"} ELSE {})
                      \cup (IF k = "cant" THEN {"cant"} ELSE {})
          IN seen' = IF Probes THEN seen \cup tags ELSE seen
  /\ lifeViol' = (lifeViol \/ (Ghosts /\ (\E i \in Ids : row[i].st \in Live /\ row'[i].st = "missing"
                        /\ \A e \in fsnap : e.id = i => ~SameLife(e))))
  /\ UNCHANGED <<TW, hist, nextSid, nextLife, envVars, orchVars, botVars, apprVars, humVars,
                 handsViol, inertViol, sendViol, histViol, botFinViol, hkViol, killViol, scopeViol>>
\* expire: keeps a row whose agent process runs; deletes only on Gone.
Expire ==
  /\ EnableExpire
  /\ \E i \in Ids :
       /\ row[i].st \in Terminal
       /\ ~(~wall /\ AgentPid(i) # 0 /\ ProcAlive(AgentPid(i)))
       /\ LookupN(i).v = "gone"
       /\ row' = [row EXCEPT ![i] = NoRow] /\ hist' = [hist EXCEPT ![i] = {}]
  /\ UNCHANGED <<TW, nextSid, nextLife, faults, envVars, orchVars, botVars, apprVars, humVars, fmVars,
                 ghostVars>>

-----------------------------------------------------------------------------
Env  == HookStart \/ HookEnd \/ ProcExit \/ Crash \/ Message \/ Age \/ AgeEnd \/ AgeRow
        \/ Stray \/ Leftover \/ OtherStore \/ Rename \/ Group \/ Respawn \/ ServerRestart \/ Adopt
Orch == O_Spawn \/ O_ReuseLookup \/ O_ReuseReset \/ O_Launch \/ O_KillLookup
        \/ O_KillAct \/ O_AfterKill \/ O_Finished \/ O_Reread
Bot  == B_Start \/ B_Unlatch \/ B_Spawn \/ B_Launch \/ B_Get \/ B_Reconnect \/ B_SendGuard
        \/ B_FindMissing \/ B_Resume \/ B_Claim \/ B_RLaunch \/ B_Crash \/ B_NrKill \/ B_NrFm
        \/ B_NrGet \/ B_Reuse \/ B_ReuseReset \/ B_ReuseLaunch \/ B_RetrySpawn \/ B_RetryLaunch
        \/ B_ApproveLookup \/ B_ApproveAct
Hum  == H_Resume \/ H_Claim \/ H_RLaunch \/ H_Crash \/ HK_Lookup \/ HK_Act \/ H_Unlatch
Next == Env \/ Orch \/ Bot \/ Hum \/ FM_List \/ FM_Check \/ Expire

Spec == Init /\ [][Next]_vars
Sids == 1..MaxSid
FairSpec == Spec /\ WF_vars(FM_List) /\ WF_vars(FM_Check) /\ WF_vars(Age)
            /\ \A k \in Sids : WF_vars(HookStartS(k))
            /\ WF_vars(B_ApproveLookup) /\ WF_vars(B_ApproveAct)
TimeFair   == WF_vars(AgeEnd) /\ WF_vars(ProcExit) /\ \A i \in Ids : WF_vars(AgeRowI(i))
LaunchFair == WF_vars(B_RLaunch) /\ WF_vars(H_RLaunch) /\ WF_vars(B_Launch)
              /\ WF_vars(B_RetryLaunch) /\ WF_vars(B_ReuseLaunch) /\ WF_vars(O_Launch)
FairSpecT == FairSpec /\ TimeFair /\ LaunchFair

-----------------------------------------------------------------------------
(* Safety *)
AgentP == {p \in procs : p.owner \in Ids}
NoOrphan        == \A p \in AgentP : row[p.owner].st # "none"
NoOrphanLife    == \A p \in AgentP : row[p.owner].st # "none" /\ row[p.owner].life = p.life
OneAgentPerId   == \A p1, p2 \in AgentP : p1.owner = p2.owner => p1 = p2
HandsOff        == ~handsViol          \* kills and keys reach only the row's current launch
NonGoneInert    == ~inertViol
MarksRightLife  == ~lifeViol
ResumeNoEarlierLife == ~histViol
NoWrongMemory   == \A p \in AgentP : p.conv = p.life
GetNoEarlierLife == \A i \in Ids : \A e \in HistC(i) : e = row[i].life
BotNeverKillsFinished == ~botFinViol
KillFinishedOnlyLeftover == ~hkViol
SendOnlyCurrentLaunch == ~sendViol
\* new (amendment 5): a kill that reports success on a live row leaves no
\* process of that row's current launch.
KillHonest      == ~killViol
\* new (amendments 1-4): the lookup's verdict is true to the ground truth.
\* Ours names a session of the row's current (or last) launch; Gone and
\* Leftover only when no session of that launch exists (viewers aside).
CurSessions(i) == {s \in sess : s.gl = CurLaunch(i) /\ ~s.view}
VerdictSound ==
  \A i \in Ids : row[i].st # "none" =>
     LET v == LookupN(i) IN
     /\ v.v = "ours" => v.s.gl = CurLaunch(i)
     /\ v.v \in {"gone", "leftover"} => CurSessions(i) = {}
     /\ v.v = "leftover" => v.s.gl[1] = i            \* decision 2: never another store's agent
\* Action properties.
MarkedMissing(i) == row[i].st \in Live /\ row'[i].st = "missing" /\ row'[i].ver = row[i].ver
\* find-missing never marks a live row whose current launch's agent runs, or
\* a pending row whose launcher is still before its create call.
NoFalseMissing ==
  [][\A i \in Ids : MarkedMissing(i) => ~(InLaunch(i) \/ CurProcs(i) # {})]_vars
IsKill == (bpc = "nr_kill" /\ bpc' # "nr_kill") \/ (opc = "kill_act" /\ opc' # "kill_act")
\* (b.66h: for the orchestrator's split kill, "current" is the launch its
\* lookup read, OKillL; the same as the row's current launch unless
\* RelaunchAny.)
KLaunch(i) == IF i = OrchId /\ opc = "kill_act" THEN OKillL ELSE CurLaunch(i)
KillPendingOnlyCurrent ==
  [][IsKill => \A p \in procs \ procs' :
                 (p.owner \in Ids /\ row[p.owner].st = "pending") => InLaunchPane(KLaunch(p.owner), p)]_vars

\* b.66h. A kill or send-keys scoped to the launch L its caller read acts on
\* L or on nothing: every process a scoped kill ends, and every process in
\* the pane a scoped send types to, belongs to L (ghost scopeViol, set in
\* B_NrKill, B_Reconnect and O_KillAct). Checked only when LaunchObs.
ScopedActsOnlyOnObservedLaunch == ~scopeViol
\* A "changed" refusal touches neither tmux nor the store (action property).
\* The bot leaves nr_kill / reconnect for get (B_NrKill, B_Reconnect,
\* B_SendGuard), and the orchestrator leaves run for reread, only on "changed".
ChangedStep == (bpc \in {"nr_kill", "reconnect"} /\ bpc' = "get") \/ (opc = "run" /\ opc' = "reread")
ChangedIsInert == [][ChangedStep => UNCHANGED <<row, sess, procs>>]_vars

(* Liveness *)
StuckDead(i) == row[i].st = "live" /\ CurProcs(i) = {}
StuckHeals   == \A i \in Ids : StuckDead(i) ~> (row[i].st # "live")
Frozen(i)    == InLaunch(i) /\ ~CanCreate
PendingResolves == \A i \in Ids :
   row[i].st = "pending" ~> (row[i].st # "pending" \/ CurProcs(i) # {} \/ Frozen(i))

\* CI state reduction for SAFETY runs (never liveness): a VIEW that forgets the
\* controllers' stale locals while a controller is idle. Each is rewritten
\* before it is read in the next cycle (bver/blife/bconv/bsave by B_SpawnAt,
\* B_Resume, B_Reuse, B_Claim, B_ReuseReset; hver/hconv by H_Resume and never
\* read after "done"; kid/ktg by HK_Lookup; okt by O_KillLookup; oname/olife/
\* over/osave by O_Spawn, O_ReuseLookup, O_ReuseReset), and no invariant reads
\* them, so merging such states changes no safety verdict.
\* b.66h: hid is read only while hpc is claim or rlaunch (H_Resume writes
\* it); bobs only in nr_kill and reconnect (B_Get and B_Resume, the only ways
\* in, write it); oobs only in run and kill_act (O_Launch and O_Reread, the
\* only ways in from elsewhere, write it); okl only in kill_act (O_KillLookup,
\* the only way in, writes it). StaleL forgets each elsewhere.
BIdle == bpc = "idle"
StaleB == IF BIdle THEN <<0, 0, 0, NoRow>> ELSE <<bver, blife, bconv, bsave>>
StaleH == IF hpc \in {"idle", "done"} THEN <<<<0, 0>>, 0, NoRow, BotId>> ELSE <<hver, hconv, hsave, hid>>
StaleK == IF kpc = "idle" THEN <<BotId, NoTarget>> ELSE <<kid, ktg>>
StaleO == IF opc = "idle" THEN <<"-", 0, 0, NoTarget, NoRow>> ELSE <<oname, olife, over, okt, osave>>
StaleL == <<IF bpc \in {"nr_kill", "reconnect"} THEN bobs ELSE NoObs,
            IF opc \in {"run", "kill_act"} THEN oobs ELSE NoObs,
            IF opc = "kill_act" THEN okl ELSE NoObs>>
CIView == <<row, sess, procs, hist, nextSid, nextLife, faults, envVars,
            opc, otaint, oreuse, StaleO, bpc, btaint, latch, StaleB, apprVars, hpc, StaleH, kpc, StaleK,
            fmVars, ghostVars, StaleL>>

\* Review 2026-09-28 (unbounded-review.md): agents' creations stay within MaxSid.
SidBound == nextSid <= MaxSid

(* Vacuity probes: each must be VIOLATED. *)
NotSeen(t) == t \notin seen
NotV(v)    == ~\E i \in Ids : row[i].st # "none" /\ LookupN(i).v = v
NotOurs     == NotV("ours")
NotLeftover == NotV("leftover")
NotGone     == NotV("gone")
NotCantDiff == NotSeen("differs_cant")
NotCant     == NotSeen("cant")
NotOursRenamed == ~\E i \in Ids : row[i].st # "none" /\ LookupN(i).v = "ours"
                                  /\ LookupN(i).s.name # row[i].name
NotRenamed  == NotSeen("renamed")
NotGrouped  == NotSeen("grouped")
NotKillWithViewer == NotSeen("kill_with_viewer")
NotRespawned == NotSeen("respawned")
NotRespawnRefused == ~\E i \in Ids : row[i].st = "live" /\ LookupN(i).v = "ours" /\ row[i].pn # 0
                                     /\ ~RecPane(i) /\ \E p \in procs : p.pn = row[i].pn
NotDeadWithSession == NotSeen("dead_with_session")
NotRemainDead == ~\E i \in Ids : row[i].st \in Live /\ LookupN(i).v = "ours" /\ CurProcs(i) = {}
NotLabfailKilled == NotSeen("labfail_killed")
NotLostReply == NotSeen("lost_reply")
NotAdopted   == NotSeen("adopted")
NotRestarted == NotSeen("restarted")
NotRestartGone == ~\E i \in Ids : row[i].st # "none" /\ row[i].sv # 0 /\ row[i].sv # srv
                                  /\ LookupN(i).v = "gone"
NotP1Conflict == NotSeen("p1_conflict")
NotHeldEnd   == NotSeen("held_end")
NotSquatGoneMark == NotSeen("squat_gone_mark")
NotKillFailed == NotSeen("kill_failed")
NotDollarById == ~\E s \in sess : s.name \in DollarNames /\ CurLabel(OrchId, s)
NotHumanKillFinished == NotSeen("human_kill_finished")
NotApproveResumed == NotSeen("approve_resumed")
NotHookIgnored   == NotSeen("hook_ignored")
NotScanBlocks    == ~\E i \in Ids : row[i].st = "none" /\ ScanBlocks(i) /\ (IF i = BotId THEN EnableBot ELSE EnableOrch)
NotOtherStore    == ~\E i \in Ids : row[i].st # "none" /\ \E s \in sess : s.lab.sto = "s2" /\ s.lab.id = i
NotLaunchChanged == NotSeen("launch_changed")                  \* b.66h: a scoped call is refused
NotRelaunchInKillWindow == NotSeen("relaunch_in_kill_window")  \* b.66h: lookup, relaunch, act
=============================================================================
