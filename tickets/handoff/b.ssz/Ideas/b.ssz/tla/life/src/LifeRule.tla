------------------------------ MODULE LifeRule ------------------------------
(***************************************************************************)
(* b.ssz Epic 5: Gabe's final life rule (2026-10-12), one instance id.     *)
(*                                                                         *)
(*  1. A new life begins only when the agent changes (reuse naming the     *)
(*     other agent). A same-agent reuse, a resume and the agent's own      *)
(*     session change keep the life.                                       *)
(*  2. Subagent records belong to a life: they carry across launches of   *)
(*     one life and never across an agent switch. Codex writes none.       *)
(*  3. AD-agent children (rows whose parent_id is this id) are not tied    *)
(*     to a life: a switch neither hides nor stops them.                   *)
(*  4. Calls show only the current life's life-scoped data: subagent       *)
(*     records (list-subagents) and session history (prior_sessions).      *)
(*     resume relaunches only the latest session. Nothing shown is false.  *)
(*                                                                         *)
(* One row. A launch is spawn, resume or reuse; it begins pending, and the *)
(* agent's report-in makes it live. Hooks write only while live (the gate  *)
(* needs the recorded pane). Every launch gets a fresh launch token.       *)
(*                                                                         *)
(* Resume (Gabe's resume rule): resume relaunches only the latest session *)
(* of the current life, the row's current session; never an older one.    *)
(* Folders: a spawn or reuse sets the row's folder (its cwd and agent      *)
(* config folder, abstracted to one value in Locs); resume keeps it. A     *)
(* session can be reopened only from the folder it ran in. Only the        *)
(* HistoryFallback control can reach an older session, and then only a     *)
(* folder check keeps it safe (NoFolderCheck).                             *)
(*                                                                         *)
(* Knobs (all FALSE = the design):                                         *)
(*   LifePerLaunch    control: every reuse starts a new life (B today).    *)
(*   NeverNewLife     control: a switch keeps the life too.               *)
(*   NoLifeFilter     control: records keyed and shown across lives        *)
(*                    (b.xjn as merged).                                   *)
(*   NoClose          control: no write closes a running record (not the  *)
(*                    end, the mark nor a launch). Only the launch close   *)
(*                    would be redundant: a row is finished, and so closed,*)
(*                    before any launch (run 1: same state count).         *)
(*   ChildFollowsLife control: list --parent filtered by the parent's     *)
(*                    life (the withdrawn parent_life_number design).      *)
(*   NoFolderCheck    control: resume's fallback ignores the folder.      *)
(*   HistoryFallback  control: resume falls back to an older history     *)
(*                    entry when the latest session's transcript is gone   *)
(*                    (B today, b.v2c/b.5jm). Gabe's resume rule removes it.*)
(*   ShortFormsPass   control: a session-picking argument reaches the agent *)
(*                    on a switching reuse (b.d6q's -r/-c). Design: dropped *)
(*                    on a switch (a fresh start, Gabe), refused on a same- *)
(*                    agent reuse, stripped from what resume replays.      *)
(*   SamePickPass     control: the same, on a same-agent reuse or replayed *)
(*                    by resume only (ShortFormsPass covers the switch).   *)
(*   SwitchBackReturns control: a switch back to an agent returns to that *)
(*                    agent's last life (Gabe's added rule forbids it).    *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets

CONSTANTS Sids, Kids, Locs, MaxLaunch, MaxSess,
          LifePerLaunch, NeverNewLife, NoLifeFilter, NoClose,
          ChildFollowsLife, NoFolderCheck, SwitchBackReturns, HistoryFallback,
          ShortFormsPass, SamePickPass

Agents  == {"claude", "codex"}
None    == "none"
NoSess  == [id |-> 0, by |-> None, loc |-> None]          \* no session
NoPrior == [agent |-> None, life |-> 0, lau |-> 0, sess |-> NoSess, loc |-> None,
            swTok |-> 0, swSes |-> 0, sameTok |-> 0, sameSes |-> 0,
            lastLife |-> [a \in {"claude", "codex"} |-> 0]]

VARIABLES
  st,       \* "none" | "pending" | "live" | "finished"
  agent,    \* the row's agent, or None before the spawn
  life,     \* spawns.life_number
  loc,      \* the row's folder (cwd and agent config folder)
  lau,      \* the current launch token
  ntok,     \* tokens handed out
  sess,     \* the current session [id, by, loc], or NoSess
  nses,     \* session ids handed out
  hist,     \* session_history: [id, life, by, loc]
  subs,     \* subagent entries: [sid, life, lau, st, by]
  prior,    \* a reuse's saved life, for the restore, or NoPrior
  kidSt,    \* each child's state ("none" = not spawned)
  kidLife,  \* the parent's life when the child was spawned (control only)
  relaunch, \* ghost: the session the latest resume relaunched, or NoSess
  sameTok,  \* ghost: the token of the latest same-agent reuse (0: none)
  sameSes,  \* ghost: the last session id handed out before that reuse
  swTok,    \* ghost: the token of the latest agent switch (0: none)
  swSes,    \* ghost: the last session id handed out before that switch
  lastLife  \* each agent's last life + 1, 0 if never (used only by SwitchBackReturns)

vars == <<st, agent, life, loc, lau, ntok, sess, nses, hist, subs, prior,
          kidSt, kidLife, relaunch, sameTok, sameSes, swTok, swSes, lastLife>>

Tracked == agent = "claude"

Init == /\ st = "none" /\ agent = None /\ life = 0 /\ loc = None /\ lau = 0
        /\ ntok = 0 /\ sess = NoSess /\ nses = 0 /\ hist = {} /\ subs = {}
        /\ prior = NoPrior
        /\ kidSt = [k \in Kids |-> "none"] /\ kidLife = [k \in Kids |-> 0]
        /\ relaunch = NoSess /\ sameTok = 0 /\ sameSes = 0
        /\ swTok = 0 /\ swSes = 0 /\ lastLife = [a \in Agents |-> 0]

CloseRun(S)    == IF NoClose THEN S ELSE {IF r.st = "run" THEN [r EXCEPT !.st = "closed"] ELSE r : r \in S}
LaunchClose(S) == CloseRun(S)
ArchiveSess    == IF sess = NoSess THEN hist
                  ELSE hist \cup {[id |-> sess.id, life |-> life, by |-> sess.by, loc |-> sess.loc]}

(***************************************************************************)
(* What the calls show.                                                    *)
(***************************************************************************)
ShownSubs == {r \in subs : NoLifeFilter \/ r.life = life}                \* list-subagents
ShownHist == {h \in hist : h.life = life /\ h.id # sess.id}              \* prior_sessions
\* What resume may fall back to: the visible history, from this folder.
Fallback  == {h \in ShownHist : NoFolderCheck \/ h.loc = loc}
Spawned   == {k \in Kids : kidSt[k] # "none"}
ShownKids == {k \in Spawned : ~ChildFollowsLife \/ kidLife[k] = life}    \* list --parent

AsSess(h) == [id |-> h.id, by |-> h.by, loc |-> h.loc]
\* A new life is one more than the current one: lives only grow (a switch
\* back to an earlier agent is a new life, never a return to its old one).
NextLife == life + 1

(***************************************************************************)
(* Actions.                                                                *)
(***************************************************************************)
Spawn(a, l) ==
  /\ st = "none" /\ ntok < MaxLaunch
  /\ st' = "pending" /\ agent' = a /\ loc' = l /\ ntok' = ntok + 1 /\ lau' = ntok + 1
  /\ UNCHANGED <<life, sess, nses, hist, subs, prior, kidSt, kidLife, relaunch, sameTok, sameSes, swTok, swSes, lastLife>>

\* The agent reports in: a launch with no session gets a new one.
ReportIn ==
  /\ st = "pending" /\ (sess # NoSess \/ nses < MaxSess)
  /\ st' = "live" /\ prior' = NoPrior
  /\ IF sess = NoSess
       THEN sess' = [id |-> nses + 1, by |-> agent, loc |-> loc] /\ nses' = nses + 1
       ELSE UNCHANGED <<sess, nses>>
  /\ UNCHANGED <<agent, life, loc, lau, ntok, hist, subs, kidSt, kidLife, relaunch, sameTok, sameSes, swTok, swSes, lastLife>>

\* The agent ends or find-missing marks it: running records close.
Finish ==
  /\ st = "live"
  /\ st' = "finished" /\ subs' = CloseRun(subs)
  /\ UNCHANGED <<agent, life, loc, lau, ntok, sess, nses, hist, prior, kidSt, kidLife, relaunch, sameTok, sameSes, swTok, swSes, lastLife>>

\* resume: same agent, life and folder, a new launch. It relaunches the
\* current session, the latest (B's fallback to an older history entry is the
\* HistoryFallback control, ResumeFallback below).
ResumeCurrent ==
  /\ st = "finished" /\ ntok < MaxLaunch /\ sess # NoSess
  /\ st' = "pending" /\ ntok' = ntok + 1 /\ lau' = ntok + 1
  /\ subs' = LaunchClose(subs) /\ relaunch' = sess
  /\ UNCHANGED <<agent, life, loc, sess, nses, hist, prior, kidSt, kidLife, sameTok, sameSes, swTok, swSes, lastLife>>

ResumeFallback(h) ==
  /\ HistoryFallback
  /\ st = "finished" /\ ntok < MaxLaunch /\ h \in Fallback
  /\ st' = "pending" /\ ntok' = ntok + 1 /\ lau' = ntok + 1
  /\ subs' = LaunchClose(subs) /\ relaunch' = AsSess(h)
  /\ hist' = ArchiveSess /\ sess' = AsSess(h)
  /\ UNCHANGED <<agent, life, loc, nses, prior, kidSt, kidLife, sameTok, sameSes, swTok, swSes, lastLife>>

\* reuse_finished naming agent a, in folder l: the session is archived in the
\* life it ran in; a new life only on a switch (or always, under LifePerLaunch).
Reuse(a, l) ==
  /\ st = "finished" /\ ntok < MaxLaunch
  /\ prior' = [agent |-> agent, life |-> life, lau |-> lau, sess |-> sess, loc |-> loc,
              swTok |-> swTok, swSes |-> swSes, sameTok |-> sameTok, sameSes |-> sameSes,
              lastLife |-> lastLife]
  /\ hist' = ArchiveSess
  /\ sess' = NoSess
  /\ life' = IF SwitchBackReturns /\ a # agent /\ lastLife[a] > 0 THEN lastLife[a] - 1
            ELSE IF ~NeverNewLife /\ (LifePerLaunch \/ a # agent) THEN NextLife ELSE life
  /\ lastLife' = [lastLife EXCEPT ![agent] = life + 1]   \* 0: the agent never ran
  /\ swTok' = IF a # agent THEN ntok + 1 ELSE swTok
  /\ swSes' = IF a # agent THEN nses ELSE swSes
  /\ agent' = a /\ loc' = l
  /\ st' = "pending" /\ ntok' = ntok + 1 /\ lau' = ntok + 1
  /\ subs' = LaunchClose(subs)
  /\ sameTok' = IF a = agent THEN ntok + 1 ELSE sameTok
  /\ sameSes' = IF a = agent THEN nses ELSE sameSes
  /\ relaunch' = NoSess
  /\ UNCHANGED <<nses, kidSt, kidLife>>

\* A failed reuse whose restore applies: the earlier agent, life, folder,
\* launch and session come back. The archive entry stays; no hook wrote.
ReuseRestore ==
  /\ st = "pending" /\ prior # NoPrior
  /\ st' = "finished" /\ agent' = prior.agent /\ life' = prior.life /\ loc' = prior.loc
  /\ lau' = prior.lau /\ sess' = prior.sess /\ prior' = NoPrior
  \* The undone reuse never happened: its ghosts go back too (job 4's lr_S
  \* counterexample was this ghost left set, a model bug).
  /\ swTok' = prior.swTok /\ swSes' = prior.swSes /\ lastLife' = prior.lastLife
  /\ sameTok' = prior.sameTok /\ sameSes' = prior.sameSes
  /\ UNCHANGED <<ntok, nses, hist, subs, kidSt, kidLife, relaunch>>

\* A launch that never reports in and is not restored (a timeout, then
\* find-missing): the row stays as the launch left it.
PendingGiveUp ==
  /\ st = "pending"
  /\ st' = "finished" /\ prior' = NoPrior /\ subs' = CloseRun(subs)
  /\ UNCHANGED <<agent, life, loc, lau, ntok, sess, nses, hist, kidSt, kidLife, relaunch, sameTok, sameSes, swTok, swSes, lastLife>>

\* The agent's own session change (/clear, its own /resume): archived in the
\* current life; the new session is the same agent's, in the same folder.
Rotate ==
  /\ st = "live" /\ sess # NoSess /\ nses < MaxSess
  /\ hist' = ArchiveSess
  /\ sess' = [id |-> nses + 1, by |-> agent, loc |-> loc] /\ nses' = nses + 1
  /\ UNCHANGED <<st, agent, life, loc, lau, ntok, subs, prior, kidSt, kidLife, relaunch, sameTok, sameSes, swTok, swSes, lastLife>>

\* A SubagentStart: the entry of this id in the current life (any life under
\* NoLifeFilter) is reopened and moved to the current launch; else a new one.
SubStart(s) ==
  /\ st = "live" /\ Tracked
  /\ LET mine == {r \in subs : r.sid = s /\ (NoLifeFilter \/ r.life = life)}
     IN subs' = IF mine # {}
                  THEN (subs \ mine) \cup {[r EXCEPT !.st = "run", !.lau = lau] : r \in mine}
                  ELSE subs \cup {[sid |-> s, life |-> life, lau |-> lau,
                                   st |-> "run", by |-> agent]}
  /\ UNCHANGED <<st, agent, life, loc, lau, ntok, sess, nses, hist, prior, kidSt, kidLife, relaunch, sameTok, sameSes, swTok, swSes, lastLife>>

\* A SubagentStop acts only on an entry of the current launch.
SubStop(s) ==
  /\ st = "live" /\ Tracked
  /\ \E r \in subs : r.sid = s /\ r.lau = lau /\ r.st = "run"
  /\ subs' = {IF r.sid = s /\ r.lau = lau THEN [r EXCEPT !.st = "closed"] ELSE r : r \in subs}
  /\ UNCHANGED <<st, agent, life, loc, lau, ntok, sess, nses, hist, prior, kidSt, kidLife, relaunch, sameTok, sameSes, swTok, swSes, lastLife>>

\* The live agent spawns an AD agent with this id as its parent.
KidSpawn(k) ==
  /\ st = "live" /\ kidSt[k] = "none"
  /\ kidSt' = [kidSt EXCEPT ![k] = "live"] /\ kidLife' = [kidLife EXCEPT ![k] = life]
  /\ UNCHANGED <<st, agent, life, loc, lau, ntok, sess, nses, hist, subs, prior, relaunch, sameTok, sameSes, swTok, swSes, lastLife>>

KidEnd(k) ==
  /\ kidSt[k] = "live"
  /\ kidSt' = [kidSt EXCEPT ![k] = "finished"]
  /\ UNCHANGED <<st, agent, life, loc, lau, ntok, sess, nses, hist, subs, prior, kidLife, relaunch, sameTok, sameSes, swTok, swSes, lastLife>>

\* A reuse that switches the agent and asks, in its arguments, to resume an
\* earlier session h (Gabe's key case: Claude, Codex, then Claude with
\* --resume). Gabe's correction (2026-10-12): not an error. In the design the
\* request resumes nothing: the launch starts the new agent fresh, exactly as
\* a switching reuse without the argument (the argument is not passed on).
\* Under the ShortFormsPass control (b.d6q: the argument reaches the agent)
\* the new agent reopens h.
ResumeAcrossTaken(a, h) ==
  /\ st = "finished" /\ ntok < MaxLaunch
  /\ prior' = NoPrior /\ hist' = ArchiveSess
  /\ life' = NextLife /\ agent' = a
  /\ lastLife' = [lastLife EXCEPT ![agent] = life + 1]
  /\ swTok' = ntok + 1 /\ swSes' = nses
  /\ st' = "pending" /\ ntok' = ntok + 1 /\ lau' = ntok + 1
  /\ subs' = LaunchClose(subs)
  /\ sess' = AsSess(h) /\ relaunch' = AsSess(h)
  /\ UNCHANGED <<loc, nses, kidSt, kidLife, sameTok, sameSes>>

\* A same-agent reuse whose arguments pick an earlier session h, or a resume
\* that replays such a stored argument. Design (b.d6q folded in): refused on
\* a same-agent reuse, and stripped from what resume replays, so it is no
\* transition. Under the SamePickPass control it reaches the agent, which
\* reopens h, for example after Claude, Codex, a fresh Claude, then a
\* same-agent reuse with -r <first Claude session>.
SamePick(h) ==
  /\ SamePickPass
  /\ st = "finished" /\ ntok < MaxLaunch /\ h \in hist /\ h.by = agent
  /\ st' = "pending" /\ ntok' = ntok + 1 /\ lau' = ntok + 1
  /\ subs' = LaunchClose(subs) /\ hist' = ArchiveSess
  /\ sess' = AsSess(h) /\ relaunch' = AsSess(h) /\ prior' = NoPrior
  /\ UNCHANGED <<agent, life, loc, nses, kidSt, kidLife, sameTok, sameSes, swTok, swSes, lastLife>>

ReuseResumeAcross(a, h) ==
  /\ a # agent /\ h \in hist
  /\ IF ShortFormsPass THEN ResumeAcrossTaken(a, h) ELSE Reuse(a, loc)

Next ==
  \/ \E a \in Agents, l \in Locs : Spawn(a, l) \/ Reuse(a, l)
  \/ ReportIn \/ Finish \/ ResumeCurrent \/ ReuseRestore \/ PendingGiveUp \/ Rotate
  \/ \E h \in hist : ResumeFallback(h)
  \/ \E a \in Agents, h \in hist : ReuseResumeAcross(a, h)
  \/ \E h \in hist : SamePick(h)
  \/ \E s \in Sids : SubStart(s) \/ SubStop(s)
  \/ \E k \in Kids : KidSpawn(k) \/ KidEnd(k)

Spec == Init /\ [][Next]_vars

(***************************************************************************)
(* Never false.                                                            *)
(***************************************************************************)
TypeOK == /\ st \in {"none", "pending", "live", "finished"}
          /\ agent \in Agents \cup {None}
          /\ loc \in Locs \cup {None}
          /\ life \in Nat /\ lau \in Nat

\* list-subagents shows only records the row's current agent wrote: no
\* Claude record on a Codex row.
SubsTruth == st # "none" => \A r \in ShownSubs : r.by = agent

\* tracked false comes with subagents [].
UntrackedEmpty == (st # "none" /\ ~Tracked) => ShownSubs = {}

\* A record shown running is of the current launch of a live row.
NoStaleRunning == \A r \in ShownSubs : r.st = "run" => (st = "live" /\ r.lau = lau)

\* prior_sessions offers only the current agent's sessions, and the current
\* session is the current agent's.
HistTruth == /\ \A h \in ShownHist : h.by = agent
             /\ sess # NoSess => sess.by = agent

\* resume relaunches only a session the current agent can open from the
\* row's current folder.
ResumeOpens == relaunch # NoSess => (relaunch.by = agent /\ relaunch.loc = loc)

\* Gabe's added rule: a switch gives up everything before it, for good, a
\* switch back included. No shown session, no resumable session and no shown
\* record dates from before the latest switch.
SwitchForgets ==
  /\ \A h \in ShownHist : h.id > swSes
  /\ \A r \in ShownSubs : r.lau >= swTok
  /\ relaunch # NoSess => relaunch.id > swSes

\* Gabe's resume rule: resume relaunches the row's current session, the
\* latest of the current life, and never an older one.
ResumeLatest == [][(relaunch' # NoSess /\ relaunch' # relaunch) => relaunch'.id = sess.id]_vars
\* ... in particular never a session from before a same-agent reuse.
NeverOlder == ~(sameTok > 0 /\ lau > sameTok /\ relaunch # NoSess /\ relaunch.id <= sameSes)

\* Gabe's key error case: Claude runs and ends, Codex runs and ends, then
\* Claude is launched on the id with --resume. AD never picks up the old
\* Claude session (that agent would have a gap in its memory): once the id
\* has switched, neither the current session nor any resumed one is a
\* session from before the latest switch.
\* Exactly that case: the row runs Claude after a Codex life that followed a
\* Claude life; its current or resumed session is never one of the Claude
\* sessions from before the Codex life. (SwitchForgets is the general form.)
OldClaude == {h.id : h \in {x \in hist : x.by = "claude" /\ \E y \in hist : y.by = "codex" /\ y.id > x.id}}
KeyCaseNoOldSession ==
  (agent = "claude" /\ \E y \in hist : y.by = "codex") =>
    /\ sess.id \notin OldClaude
    /\ relaunch.id \notin OldClaude

\* list --parent lists every child, whatever life spawned it (rule 3).
KidsShown == Spawned \subseteq ShownKids

\* Rule 1: the life changes exactly when the agent does (the restore puts
\* both back together).
LifeIffAgent == [][st # "none" => ((life' # life) <=> (agent' # agent))]_vars

\* Rule 2: within a life, a shown record never disappears and a shown
\* session never disappears (except into the current session).
CarryInLife ==
  [][(st # "none" /\ life' = life /\ agent' = agent) =>
       /\ \A r \in ShownSubs : \E r2 \in ShownSubs' : r2.sid = r.sid /\ r2.life = r.life
       /\ \A h \in ShownHist : (\E h2 \in ShownHist' : h2.id = h.id) \/ sess'.id = h.id
    ]_vars

\* Rule 3: a switch neither stops nor changes a child.
KidsUntouched == [][(st # "none" /\ agent' # agent) => kidSt' = kidSt]_vars

(***************************************************************************)
(* Probes: each must be reached (its invariant must FAIL).                 *)
(***************************************************************************)
\* Claude records kept in the store but hidden on a Codex row.
NotHidden == ~(agent = "codex" /\ \E r \in subs : r.by = "claude")
\* A record written before a same-agent reuse is still shown after it.
NotCarried == ~(sameTok > 0 /\ lau >= sameTok /\ \E r \in ShownSubs : r.lau < sameTok)
\* A child spawned before a switch is listed after it.
NotKidAfterSwitch == ~(\E k \in ShownKids : kidLife[k] < life)
\* A session archived by a same-agent reuse is offered after it.
NotHistCarried == ~(sameTok > 0 /\ lau >= sameTok /\ \E h \in ShownHist : h.id <= sameSes)
\* resume falls back to a session from before a same-agent reuse.
NotResumedOld == ~(sameTok > 0 /\ lau > sameTok /\ relaunch # NoSess /\ relaunch.id <= sameSes)
\* A resume happens.
NotResumed == relaunch = NoSess
\* The key case's setting: Claude, then Codex, then Claude again on the id,
\* with the first Claude session still in the store.
NotKeyCase == ~(agent = "claude" /\ swTok > 0 /\ \E h \in hist : h.by = "claude" /\ h.id <= swSes
                 /\ \E h2 \in hist : h2.by = "codex")
\* A switch back to an agent that has earlier sessions in the store.
NotSwitchedBack == ~(swTok > 0 /\ \E h \in hist : h.by = agent /\ h.id <= swSes)
=============================================================================
