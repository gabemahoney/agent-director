------------------------------ MODULE Hold36h ------------------------------
(***************************************************************************)
(* Bee b.36h: the store's working hold is a read, then a separate write.   *)
(*                                                                         *)
(* Built on b.omt's OmtHold (tickets/Bugs/b.omt/tla/OmtHold.tla): the same *)
(* world and properties, with two changes.                                 *)
(*                                                                         *)
(* 1. A working write (UserPromptSubmit, PreToolUse, PostToolUse, through   *)
(*    ApplyHookTransitionResult) is two steps. The read                    *)
(*    (holdWorkingTransition's holdingRequestSQL) decides held or not.     *)
(*    The write comes later: the held branch's idle_since clear, or the    *)
(*    gated UPDATE to working (applyGatedHookWrite). Between them any      *)
(*    other step may run.                                                  *)
(* 2. The store lock. The relay hook's first write (InsertRelayRequest:    *)
(*    the request and check_permission) is one BEGIN IMMEDIATE             *)
(*    transaction that holds the write lock from its begin to its commit.  *)
(*    A write waits while the lock is held; a read does not wait (WAL) and *)
(*    sees the store as it was before the transaction began.               *)
(*                                                                         *)
(* Write:                                                                  *)
(*   "split" - today: the UPDATE to working carries the gate only, so it   *)
(*             writes working whatever was recorded after the read.        *)
(*   "cond"  - the fix: the UPDATE also requires that no request holds the *)
(*             row (NOT EXISTS ... holdsRowSQL), as the idle Notification's *)
(*             check_permission -> waiting already does. When it matches   *)
(*             nothing because a request now holds, the write is reported  *)
(*             as held: no state write, idle_since cleared.                *)
(*                                                                         *)
(* Design is OmtHold's ("today", "fix", "fixcut"); feature/b.146-relay has *)
(* "fixcut" (b.omt, 4e067b1).                                              *)
(*                                                                         *)
(* Each agent's hooks block that agent: Claude Code waits for a hook       *)
(* before the agent goes on, so an agent has at most one working write in  *)
(* flight and does nothing else meanwhile. The two agents (main and the    *)
(* background subagent) run their hooks concurrently.                      *)
(*                                                                         *)
(* Not modelled: a relay hook that dies before its transaction commits     *)
(* (nothing recorded, the dialog shown: no request can hold the row, an    *)
(* accepted gap of b.146), parallel tool calls of one agent, the gap       *)
(* between the held branch's gate read and its idle_since clear, and       *)
(* everything OmtHold leaves out.                                          *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets

CONSTANTS
    MaxTurns,   \* turns of the main agent (one request each, at most)
    Subagent,   \* a background subagent may ask one request
    Design,     \* "today", "fix", "fixcut" (b.omt's proof rules)
    Write,      \* "split" (today), "cond" (the fix)
    Env,        \* environment behaviours turned on (a subset of EnvSwitches)
    Probe       \* the event whose first occurrence sets the ghost hit ("" for none)

EnvSwitches == {
    "HookDeath",    \* a hook may die (a working hook also after its read: its UPDATE may get SQLITE_BUSY)
    "BothEndsDie",  \* Stop (or, after an interrupt, nothing) and the idle hook may both be lost
    "SlowIdle",     \* the idle hook's read may land after the next turn has begun
    "Claim"}        \* record-pane-answer is used (fair) on a fallen-back request whose dialog is gone

ASSUME Design \in {"today", "fix", "fixcut"}
ASSUME Write \in {"split", "cond"}
ASSUME Env \subseteq EnvSwitches
ASSUME MaxTurns \in 1 .. 3 /\ Subagent \in BOOLEAN

HookDeath == "HookDeath" \in Env
BothEndsDie == "BothEndsDie" \in Env
SlowIdle == "SlowIdle" \in Env
Claim == "Claim" \in Env

SR == MaxTurns + 1                          \* the subagent's request
R == IF Subagent THEN 1 .. SR ELSE 1 .. MaxTurns
Main == 1 .. MaxTurns
Verdicts == {"allow", "deny"}
Agents == {"main", "sub"}
AgentOf(r) == IF r \in Main THEN "main" ELSE "sub"

VARIABLES
    aph,      \* the main agent: "idle" (at its prompt), "turn", "stopdue" (Stop is due)
    turn,     \* turns begun
    stopLost, \* the turn that ended last has no Stop write (died, or an interrupt)
    iq,       \* the idle Notification: "none", "due" (the agent idles), "flight" (sent, hook running)
    isnap,    \* the main-agent requests recorded when Claude Code sent it
    sub,      \* the subagent: "none", "run", "done"
    st,       \* each request: "none", "tx" (its relay hook's transaction is open), "hook"
              \* (its relay hook polls), "pend" (dialog shown), "ans"
    got,      \* what its dialog got: "none", "allow", "deny"
    rec,      \* its record exists (committed)
    ack,      \* its relay hook delivered a verdict (closed)
    pane,     \* "none", "closed" (a pane answer, record-pane-answer or the tool_ran close)
    prov,     \* proven gone (proven_gone_at)
    ptu,      \* its PostToolUse: "none", "due" (its tool ran), "done"
    rs,       \* the row: "waiting", "working", "check_permission"
    idl,      \* idle_since is set
    wr,       \* each agent's working write: "none", "free" (read: not held; the UPDATE is due),
              \* "held" (read: held; the idle_since clear is due)
    lock,     \* the store's write lock: 0 (free), or r (r's InsertRelayRequest transaction holds it)
    rtx,      \* ghost (only with Probe = "txrace"): the agent's read ran inside an open transaction
    ended,    \* ghost: a turn-end hook of the main agent ran after the request was recorded
    stale,    \* ghost: a working write was held only by requests the main turn has ended after
    hit       \* ghost: Probe happened

vars == <<aph, turn, stopLost, iq, isnap, sub, st, got, rec, ack, pane, prov, ptu, rs, idl,
          wr, lock, rtx, ended, stale, hit>>

Hit(tag) == hit' = (hit \/ Probe = tag)

-----------------------------------------------------------------------------
(* Derived facts *)

(* A request holds the row (the hold, the idle exit, find-missing's       *)
(* repair), given the store's ack, pane and proof columns.                *)
HoldsWith(r, a, p, pv) ==
    /\ rec[r] /\ ~a[r] /\ p[r] = "none"
    /\ Design = "today" \/ ~pv[r]
Holding(a, p, pv) == {r \in R : HoldsWith(r, a, p, pv)}

(* A dialog may be on screen, or a relay hook may still answer. A relay   *)
(* hook whose transaction is still open has recorded nothing and shows   *)
(* nothing yet.                                                           *)
MayShow == \E r \in R : st[r] \in {"hook", "pend"}

(* The store lock is free: a write may run. *)
Free == lock = 0

(* Agent a has no hook in flight (Claude Code waits for each hook). *)
Idle(a) == wr[a] = "none"

(* The read of a working write by agent a's hook (holdingRequestSQL),     *)
(* with the store's columns after the hook's own proof and close (p, pv). *)
(* A read does not wait for the lock: inside an open transaction it sees  *)
(* the store before that transaction (the request not yet recorded).     *)
WorkRead(a, p, pv) ==
    LET H == Holding(ack, p, pv) IN
    /\ wr' = [wr EXCEPT ![a] = IF H # {} THEN "held" ELSE "free"]
    /\ rtx' = [rtx EXCEPT ![a] = (Probe = "txrace" /\ ~Free)]
    /\ stale' = (stale \/ (H # {} /\ \A r \in H : ended[r]))

(* Probe hits common to every read. *)
ReadHit(extra) == hit' = (hit \/ extra \/ (Probe = "readintx" /\ ~Free))

(* Turn-end proof: main-agent requests in S. *)
ProveMain(S) == [r \in R |-> prov[r] \/ (r \in S /\ rec[r])]
MainRec == {r \in Main : rec[r]}

-----------------------------------------------------------------------------
Init ==
    /\ aph = "idle" /\ turn = 0 /\ stopLost = FALSE /\ iq = "none" /\ isnap = {}
    /\ sub = "none"
    /\ st = [r \in R |-> "none"] /\ got = [r \in R |-> "none"]
    /\ rec = [r \in R |-> FALSE] /\ ack = [r \in R |-> FALSE]
    /\ pane = [r \in R |-> "none"] /\ prov = [r \in R |-> FALSE]
    /\ ptu = [r \in R |-> "none"]
    /\ rs = "waiting" /\ idl = FALSE
    /\ wr = [a \in Agents |-> "none"] /\ lock = 0 /\ rtx = [a \in Agents |-> FALSE]
    /\ ended = [r \in R |-> FALSE] /\ stale = FALSE /\ hit = FALSE

-----------------------------------------------------------------------------
(* A working write's second step, and its loss *)

(* The write, once the lock is free. Held at the read: the idle_since     *)
(* clear only. Not held at the read: the UPDATE to working; with "cond"   *)
(* it matches nothing when a request now holds the row, and is reported   *)
(* as held (idle_since cleared, no state write).                          *)
WorkWrite(a) ==
    /\ wr[a] # "none" /\ Free
    /\ LET H == Holding(ack, pane, prov)
           free == wr[a] = "free"
           go == free /\ (Write = "split" \/ H = {})
       IN /\ rs' = IF go THEN "working" ELSE rs
          /\ stale' = (stale \/ (free /\ Write = "cond" /\ H # {} /\ \A r \in H : ended[r]))
          /\ hit' = (hit \/ (Probe = "race" /\ free /\ H # {})
                         \/ (Probe = "mainrace" /\ a = "main" /\ free /\ H # {})
                         \/ (Probe = "txrace" /\ rtx[a] /\ free /\ H # {})
                         \/ (Probe = "staleheld" /\ ~free /\ H = {}))
    /\ idl' = FALSE
    /\ wr' = [wr EXCEPT ![a] = "none"] /\ rtx' = [rtx EXCEPT ![a] = FALSE]
    /\ UNCHANGED <<aph, turn, stopLost, iq, isnap, sub, st, got, rec, ack, pane, prov, ptu,
                   lock, ended>>

(* The hook dies after its read, or its UPDATE gives up on the lock      *)
(* (SQLITE_BUSY): nothing written.                                        *)
WorkDrop(a) ==
    /\ HookDeath /\ wr[a] # "none"
    /\ wr' = [wr EXCEPT ![a] = "none"] /\ rtx' = [rtx EXCEPT ![a] = FALSE]
    /\ UNCHANGED <<aph, turn, stopLost, iq, isnap, sub, st, got, rec, ack, pane, prov, ptu, rs,
                   idl, lock, ended, stale, hit>>

-----------------------------------------------------------------------------
(* The main agent *)

(* A prompt: UserPromptSubmit's read (or it dies). Without SlowIdle the   *)
(* idle hook of the last idle period has run before it.                   *)
TurnStart ==
    /\ aph = "idle" /\ turn < MaxTurns /\ Idle("main")
    /\ iq = "flight" => SlowIdle
    /\ aph' = "turn" /\ turn' = turn + 1
    /\ iq' = IF iq = "due" THEN "none" ELSE iq
    /\ \/ WorkRead("main", pane, prov)
          /\ ReadHit(Probe = "held" /\ Holding(ack, pane, prov) # {})
       \/ HookDeath /\ UNCHANGED <<wr, rtx, stale, hit>>
    /\ UNCHANGED <<stopLost, isnap, sub, st, got, rec, ack, pane, prov, ptu, rs, idl, lock,
                   ended>>

(* A PreToolUse of the main agent while none of its dialogs is up. *)
MainWork ==
    /\ aph = "turn" /\ st[turn] \in {"none", "ans"} /\ ptu[turn] # "due" /\ Idle("main")
    /\ WorkRead("main", pane, prov)
    /\ ReadHit(Probe = "mainwork")
    /\ UNCHANGED <<aph, turn, stopLost, iq, isnap, sub, st, got, rec, ack, pane, prov, ptu,
                   rs, idl, lock, ended>>

(* The relay hook's first write (b.146 rule 1): BEGIN IMMEDIATE takes the *)
(* write lock, ...                                                         *)
AskBegin(r) ==
    /\ st[r] = "none" /\ Free /\ Idle(AgentOf(r))
    /\ IF r \in Main THEN aph = "turn" /\ r = turn ELSE sub = "run"
    /\ st' = [st EXCEPT ![r] = "tx"] /\ lock' = r
    /\ UNCHANGED <<aph, turn, stopLost, iq, isnap, sub, got, rec, ack, pane, prov, ptu, rs, idl,
                   wr, rtx, ended, stale, hit>>

(* ... and the commit records the request and check_permission at once   *)
(* and frees the lock.                                                     *)
AskCommit(r) ==
    /\ st[r] = "tx" /\ lock = r
    /\ st' = [st EXCEPT ![r] = "hook"] /\ rec' = [rec EXCEPT ![r] = TRUE]
    /\ rs' = "check_permission" /\ idl' = FALSE /\ lock' = 0
    /\ UNCHANGED <<aph, turn, stopLost, iq, isnap, sub, got, ack, pane, prov, ptu, wr, rtx,
                   ended, stale, hit>>

(* The turn ends: Stop is due. *)
TurnEnd ==
    /\ aph = "turn" /\ st[turn] \in {"none", "ans"} /\ ptu[turn] # "due" /\ Idle("main")
    /\ aph' = "stopdue"
    /\ UNCHANGED <<turn, stopLost, iq, isnap, sub, st, got, rec, ack, pane, prov, ptu, rs, idl,
                   wr, lock, rtx, ended, stale, hit>>

(* A deny interrupts the turn: no Stop, the agent idles. *)
Interrupt ==
    /\ aph = "turn" /\ got[turn] = "deny" /\ Idle("main")
    /\ aph' = "idle" /\ iq' = "due" /\ stopLost' = TRUE
    /\ Hit("interrupt")
    /\ UNCHANGED <<turn, isnap, sub, st, got, rec, ack, pane, prov, ptu, rs, idl, wr, lock, rtx,
                   ended, stale>>

(* Stop: the turn_end proof, then waiting. *)
StopHook ==
    /\ aph = "stopdue" /\ Free
    /\ prov' = ProveMain(MainRec)
    /\ ended' = [r \in R |-> ended[r] \/ r \in MainRec]
    /\ rs' = "waiting" /\ idl' = FALSE
    /\ aph' = "idle" /\ iq' = "due" /\ stopLost' = FALSE
    /\ UNCHANGED <<turn, isnap, sub, st, got, rec, ack, pane, ptu, wr, lock, rtx, stale, hit>>

StopDie ==
    /\ aph = "stopdue" /\ HookDeath
    /\ aph' = "idle" /\ iq' = "due" /\ stopLost' = TRUE
    /\ Hit("stopdeath")
    /\ UNCHANGED <<turn, isnap, sub, st, got, rec, ack, pane, prov, ptu, rs, idl, wr, lock, rtx,
                   ended, stale>>

(* Claude Code sends the idle-prompt Notification (the agent idled 60 s). *)
IdleEmit ==
    /\ iq = "due" /\ aph = "idle"
    /\ iq' = "flight" /\ isnap' = MainRec
    /\ UNCHANGED <<aph, turn, stopLost, sub, st, got, rec, ack, pane, prov, ptu, rs, idl, wr,
                   lock, rtx, ended, stale, hit>>

(* Its hook: the turn_end proof (on the requests recorded at its read,    *)
(* or, fixcut, those recorded when it was sent), then working ->          *)
(* waiting, or check_permission -> waiting when no request holds; it sets *)
(* idle_since. Its writes are conditional UPDATEs: one step.               *)
IdleHook ==
    /\ iq = "flight" /\ Free
    /\ LET S == IF Design = "fixcut" THEN isnap ELSE MainRec
           pv == ProveMain(S)
       IN /\ prov' = pv
          /\ rs' = IF rs = "working" \/ (rs = "check_permission" /\ Holding(ack, pane, pv) = {})
                   THEN "waiting" ELSE rs
          /\ hit' = (hit \/ (Probe = "slowproof" /\ MainRec \ isnap # {}))
    /\ ended' = [r \in R |-> ended[r] \/ r \in isnap]
    /\ idl' = TRUE /\ iq' = "none" /\ isnap' = {}
    /\ UNCHANGED <<aph, turn, stopLost, sub, st, got, rec, ack, pane, ptu, wr, lock, rtx, stale>>

IdleDie ==
    /\ iq = "flight" /\ HookDeath /\ (~stopLost \/ BothEndsDie)
    /\ iq' = "none" /\ isnap' = {}
    /\ Hit("idledeath")
    /\ UNCHANGED <<aph, turn, stopLost, sub, st, got, rec, ack, pane, prov, ptu, rs, idl, wr,
                   lock, rtx, ended, stale>>

-----------------------------------------------------------------------------
(* The subagent (background: it may outlive the main turn) *)

(* The main agent starts it with a tool call: not while one of its hooks *)
(* runs.                                                                   *)
SubStart ==
    /\ Subagent /\ sub = "none" /\ aph = "turn" /\ Idle("main")
    /\ sub' = "run"
    /\ UNCHANGED <<aph, turn, stopLost, iq, isnap, st, got, rec, ack, pane, prov, ptu, rs, idl,
                   wr, lock, rtx, ended, stale, hit>>

(* Its PreToolUse (agent_id set): a working write's read, no proof. *)
SubWork ==
    /\ sub = "run" /\ st[SR] \in {"none", "ans"} /\ ptu[SR] # "due" /\ Idle("sub")
    /\ WorkRead("sub", pane, prov)
    /\ ReadHit(FALSE)
    /\ UNCHANGED <<aph, turn, stopLost, iq, isnap, sub, st, got, rec, ack, pane, prov, ptu,
                   rs, idl, lock, ended>>

(* It ends (OmtHold's SubEnd: when the main agent idles, one more idle   *)
(* Notification, its only end hook).                                      *)
SubEnd ==
    /\ sub = "run" /\ st[SR] \in {"none", "ans"} /\ ptu[SR] # "due" /\ Idle("sub")
    /\ iq # "flight"
    /\ sub' = "done"
    /\ IF aph = "idle" THEN iq' = "due" /\ stopLost' = TRUE
                       ELSE UNCHANGED <<iq, stopLost>>
    /\ UNCHANGED <<aph, turn, isnap, st, got, rec, ack, pane, prov, ptu, rs, idl, wr, lock, rtx,
                   ended, stale, hit>>

-----------------------------------------------------------------------------
(* Requests: the relay hook, the pane, the tool. *)

Answer(r, v) ==
    /\ st' = [st EXCEPT ![r] = "ans"] /\ got' = [got EXCEPT ![r] = v]
    /\ ptu' = IF v = "allow" THEN [ptu EXCEPT ![r] = "due"] ELSE ptu

(* The relay hook delivers a verdict (a caller's, or its timeout deny),  *)
(* acked first (rule 3): a write.                                         *)
RelayDeliver(r) ==
    /\ st[r] = "hook" /\ Free
    /\ \E v \in Verdicts : Answer(r, v)
    /\ ack' = [ack EXCEPT ![r] = TRUE]
    /\ UNCHANGED <<aph, turn, stopLost, iq, isnap, sub, rec, pane, prov, rs, idl, wr, lock, rtx,
                   ended, stale, hit>>

(* It dies (or is killed at its timeout): Claude Code shows the dialog. *)
RelayDie(r) ==
    /\ st[r] = "hook"
    /\ st' = [st EXCEPT ![r] = "pend"]
    /\ UNCHANGED <<aph, turn, stopLost, iq, isnap, sub, got, rec, ack, pane, prov, ptu, rs, idl,
                   wr, lock, rtx, ended, stale, hit>>

(* A person answers the dialog at the pane: nothing recorded. *)
HumanAnswer(r) ==
    /\ st[r] = "pend"
    /\ \E v \in Verdicts : Answer(r, v) /\ Hit(IF v = "deny" THEN "humandeny" ELSE "humanallow")
    /\ UNCHANGED <<aph, turn, stopLost, iq, isnap, sub, rec, ack, pane, prov, rs, idl, wr, lock,
                   rtx, ended, stale>>

(* agent-director's pane answer (send-keys --request-token): recorded. *)
PaneAnswer(r) ==
    /\ st[r] = "pend" /\ Free
    /\ \E v \in Verdicts : Answer(r, v)
    /\ pane' = [pane EXCEPT ![r] = "closed"]
    /\ UNCHANGED <<aph, turn, stopLost, iq, isnap, sub, rec, ack, prov, rs, idl, wr, lock, rtx,
                   ended, stale, hit>>

(* record-pane-answer: the caller saw no dialog for a fallen-back request. *)
ClaimClose(r) ==
    /\ Claim /\ st[r] = "ans" /\ rec[r] /\ ~ack[r] /\ pane[r] = "none" /\ Free
    /\ pane' = [pane EXCEPT ![r] = "closed"]
    /\ UNCHANGED <<aph, turn, stopLost, iq, isnap, sub, st, got, rec, ack, prov, ptu, rs, idl,
                   wr, lock, rtx, ended, stale, hit>>

(* PostToolUse of r's tool: the tool_ran proof and close (writes), then   *)
(* the working write's read.                                               *)
PostToolUse(r) ==
    /\ ptu[r] = "due" /\ Idle(AgentOf(r))
    /\ ptu' = [ptu EXCEPT ![r] = "done"]
    /\ \/ LET pv == [prov EXCEPT ![r] = TRUE]
              p == IF ~ack[r] THEN [pane EXCEPT ![r] = "closed"] ELSE pane
          IN /\ Free
             /\ prov' = pv /\ pane' = p
             /\ WorkRead(AgentOf(r), p, pv)
             /\ ReadHit(FALSE)
       \/ HookDeath /\ UNCHANGED <<prov, pane, wr, rtx, stale, hit>>
    /\ UNCHANGED <<aph, turn, stopLost, iq, isnap, sub, st, got, rec, ack, rs, idl, lock, ended>>

-----------------------------------------------------------------------------
(* find-missing's repair of a stale check_permission (b.146 rule 9): one *)
(* conditional UPDATE.                                                     *)
FMRecon ==
    /\ rs = "check_permission" /\ Free
    /\ Holding(ack, pane, prov) = {}
    /\ \A r \in R : st[r] # "hook"
    /\ rs' = IF idl THEN "waiting" ELSE "working"
    /\ UNCHANGED <<aph, turn, stopLost, iq, isnap, sub, st, got, rec, ack, pane, prov, ptu, idl,
                   wr, lock, rtx, ended, stale, hit>>

-----------------------------------------------------------------------------
Next ==
    \/ TurnStart \/ MainWork \/ TurnEnd \/ Interrupt \/ StopHook \/ StopDie
    \/ IdleEmit \/ IdleHook \/ IdleDie
    \/ SubStart \/ SubWork \/ SubEnd
    \/ \E a \in Agents : WorkWrite(a) \/ WorkDrop(a)
    \/ \E r \in R : AskBegin(r) \/ AskCommit(r) \/ RelayDeliver(r) \/ RelayDie(r)
                    \/ HumanAnswer(r) \/ PaneAnswer(r) \/ ClaimClose(r) \/ PostToolUse(r)
    \/ FMRecon

(* Fair: OmtHold's (Claude Code's own steps, find-missing, and            *)
(* record-pane-answer when Claim), plus a begun transaction commits and a *)
(* working hook that has read finishes its write (or dies). Not fair:     *)
(* prompts, asking, the agents' tool hooks, deaths.                       *)
Fairness ==
    /\ \A r \in R : /\ WF_vars(RelayDeliver(r) \/ RelayDie(r))
                    /\ WF_vars(HumanAnswer(r) \/ PaneAnswer(r))
                    /\ WF_vars(PostToolUse(r))
                    /\ WF_vars(ClaimClose(r))
                    /\ WF_vars(AskCommit(r))
    /\ \A a \in Agents : WF_vars(WorkWrite(a) \/ WorkDrop(a))
    /\ WF_vars(TurnEnd) /\ WF_vars(StopHook \/ StopDie)
    /\ WF_vars(IdleEmit) /\ WF_vars(IdleHook \/ IdleDie)
    /\ WF_vars(SubEnd)
    /\ WF_vars(FMRecon)

Spec == Init /\ [][Next]_vars /\ Fairness

-----------------------------------------------------------------------------
TypeOK ==
    /\ aph \in {"idle", "turn", "stopdue"} /\ turn \in 0 .. MaxTurns
    /\ iq \in {"none", "due", "flight"} /\ sub \in {"none", "run", "done"}
    /\ rs \in {"waiting", "working", "check_permission"}
    /\ lock \in {0} \cup R
    /\ \A a \in Agents : wr[a] \in {"none", "free", "held"}
    /\ \A r \in R : /\ st[r] \in {"none", "tx", "hook", "pend", "ans"}
                    /\ pane[r] \in {"none", "closed"}
                    /\ ptu[r] \in {"none", "due", "done"}
                    /\ (st[r] = "tx") <=> (lock = r)

(* The hold's purpose (b.36h's expected result): the row never reads      *)
(* working while a dialog may be on screen or a relay hook may answer.    *)
WorkingTrue == rs = "working" => ~MayShow

(* b.omt's bug: a working write is held only by requests after which a    *)
(* turn-end hook of the main agent has run.                                *)
NoStaleHold == ~stale

(* Recovery: an idle agent's row settles at waiting (no hook left to run, *)
(* no dialog left).                                                        *)
Rest == /\ aph = "idle" /\ iq = "none" /\ sub # "run" /\ ~MayShow
        /\ \A r \in R : ptu[r] # "due"
        /\ Free /\ \A a \in Agents : Idle(a)
RowSettles == <>[](Rest => rs = "waiting")

(* Probes: each must be violated, to show its path is reached. *)
NeverHit == ~hit
=============================================================================
