------------------------------ MODULE B146R5Cx ------------------------------
(***************************************************************************)
(* b.ssz (Codex support): a copy of b.146's B146R5.tla (spec/tla/B146R5.tla *)
(* on design/b146-timeouts, SHA-256                                        *)
(* 53daaa8c01bc89bb4fa6e7b98ca0010167b40423b1b414444d989241f788d2a8, the   *)
(* round-5 model the adopted hold, Unproven + Bypass, passed on) with one  *)
(* knob, CxR5 (a zero-arity definition, FALSE; a cfg turns it on with     *)
(* `CxR5 <- KnobOn`). With CxR5 FALSE the model's steps are B146R5's.     *)
(* CxR5 = Codex's events (tickets/Ideas/b.ssz/srd-design-brief.md):        *)
(*   - PostToolUse proves nothing gone and closes nothing (Codex's request *)
(*     carries no tool_use_id: no tool_ran; ToolUseClose has nothing to    *)
(*     match);                                                             *)
(*   - no idle-prompt Notification: Stop goes straight to rest, and a lost *)
(*     Stop is the both-ends-lost residual (BothEndsDie);                  *)
(*   - Codex's Interrupt after an Escape at the dialog writes what Stop    *)
(*     writes (waiting, the turn_end proof) under the brief's rule, so     *)
(*     this one-turn model's Stop after a denied dialog stands for it.     *)
(* See tickets/Ideas/b.ssz/tla/README.md.                                   *)
(*                                                                         *)
(* --- B146R5's header follows ---                                          *)
(*                                                                         *)
(* Bee b.146, round 5: B146Final (round 4, unchanged below) plus the       *)
(* candidate guards against a stray plain send-keys answering a dialog AD  *)
(* believes closed (round 4's problem 6). See spec/tla/r5/README.md and    *)
(* tickets/Bugs/b.146/stray-enter-v5.md.                                    *)
(*                                                                         *)
(* The pane is bytes with no meaning: Scrn is what Claude Code has drawn   *)
(* (the dialog shown, the turn's output, the tools' output, keys it read,  *)
(* and Noise, a change nobody asked for: a status line, a footer). A guard *)
(* may only compare Scrn with an earlier Scrn (equal or not); it never     *)
(* learns which request, if any, the bytes show.                           *)
(*                                                                         *)
(* R5, a subset of R5Options (R5 = {} is round 4 exactly):                 *)
(*   "Unproven"   a request stays "maybe on screen" from its first write   *)
(*                until Claude Code proves it moved past it: the            *)
(*                PostToolUse of its tool, or the turn's end (Stop, the    *)
(*                idle Notification). AD's own records (ack, sent, claim)  *)
(*                are not proof. Plain send-keys is refused meanwhile.     *)
(*   "IdleGate"   plain send-keys only while the last hook AD heard was    *)
(*                SessionStart, Stop or the idle Notification.             *)
(*   "IdleSnap"   (with IdleGate) AD keeps the pane as it was at that      *)
(*                hook; plain send-keys also needs the pane unchanged.     *)
(*   "SameScreen" the sender (or AD, twice) captures the pane, and the     *)
(*                send needs it unchanged since (a required hash, or a     *)
(*                quiescence window: the same in this abstraction).        *)
(*   "Bypass"     a held plain send-keys may go with --expect-pane-sha256  *)
(*                from an intelligence that looked: it answers a dialog it *)
(*                sees, or sends the held keys at a prompt it sees. It     *)
(*                looks when a send is held, or when AD reports a request  *)
(*                closed but not yet proven gone (LookAsked).              *)
(*                                                                         *)
(* --- round 4's header follows ---                                         *)
(*                                                                         *)
(* Bee b.146, round 4: one integrated model of the decided design (all ten *)
(* decisions, tickets/Bugs/b.146/decisions-v2.md). See                     *)
(* spec/tla/final/README.md for the runs, and                               *)
(* tickets/Bugs/b.146/timeout-design-final.md for the results.             *)
(*                                                                         *)
(* Rounds 1-3 checked slices: TimeoutsRelay (timing), DeathRelay and       *)
(* DeathLifecycle (death at any step), PaneAnswer (the pane). This spec    *)
(* puts them in one state space, so that their interactions are checked:   *)
(*                                                                         *)
(*   - Claude Code: one spawn row and its agent. SessionStart, then one    *)
(*     turn (UserPromptSubmit), N permission requests (a later one may     *)
(*     start while an earlier dialog is up), a PostToolUse for each tool   *)
(*     that ran, Stop, the idle-prompt Notification. Each lifecycle hook   *)
(*     may die before its write (HookDeath; Claude Code's kill at the      *)
(*     hook's timeout is one such death). The agent may crash (Crash).     *)
(*   - The relay hook of each request: the request and check_permission   *)
(*     (one transaction with Atomic), its poll, the ack before the answer  *)
(*     (AckFirst), the timeout deny-and-ack, the exit. It may die at any   *)
(*     step; Claude Code kills it at its timeout (phase 2). A hook gone    *)
(*     without an answer leaves Claude Code's dialog, drawn after it       *)
(*     (DrawLag: a little later), queued behind others.                    *)
(*   - The store: one writer at a time. Another process may hold the write *)
(*     lock for a while (MaxGrabs times, each hold finite). Every AD write  *)
(*     is one atomic statement or transaction: a process that dies rolls    *)
(*     back, so death "during" a write is death before or after it. A      *)
(*     writer that waits for the lock may get a busy failure (BusyFails:   *)
(*     some busy_timeout_ms ran out) or wait on (a large busy_timeout_ms):  *)
(*     both are possible in every run, so one run covers every value.      *)
(*   - decide, with its own clock (ticks; each cfg says how long a tick    *)
(*     is): start-up (Sigma), the process check, the verdict UPDATE (its   *)
(*     lock wait cut at MaxWait with Bound: decision 9 B, a store-busy     *)
(*     refusal), the ack wait (AckWait, decision 1 A), the reply (Phi),    *)
(*     against its caller's deadline (CallerDeadline: Slack's 3 s, or the  *)
(*     TS client's). It may die. Its caller (CSCB's click handler and      *)
(*     poller) follows up with get-permission.                             *)
(*   - get-permission: the process check, then the record (CheckFirst), and *)
(*     the delivery fields (ReadFields, decision 8). ReaderWrites: whether *)
(*     a read verb writes hook_gone_at, and how.                           *)
(*   - The pane: NI pane answerers (the intelligence: an orchestrator LLM,  *)
(*     or a human through CSCB's hands), each with its own send-keys. Each  *)
(*     reads the pane, then answers the dialog it saw (send-keys            *)
(*     --request-token, one key, --expect-pane-sha256, --as: decisions 4 B *)
(*     and 7 A) or, seeing no dialog, records record-pane-answer (decision *)
(*     6). A stray sender types a command and Enter (plain send-keys). A   *)
(*     human types at tmux. send-keys may die after its intent or after     *)
(*     typing.                                                             *)
(*   - find-missing (decision 3 A: an operator schedules it; fair but      *)
(*     unscheduled): it repairs a stale check_permission (Reconcile), a    *)
(*     live pending row (Promote), and a row whose agent is gone (marks it *)
(*     missing and closes its requests in one transaction).                *)
(*                                                                         *)
(* Time. Each request has three phases from its hook's start: 0, before   *)
(* the hook's poll end; 1, past it (the timeout path); 2, Claude Code has  *)
(* killed a hook still alive, and its settled_at has passed. These are not *)
(* tied to decide's clock: a hook event can fall anywhere inside decide's  *)
(* ticks, which over-approximates real timing. The 2 s minimum hook-gone   *)
(* age is the event Age(r): it can happen only once the dialog is drawn,   *)
(* unless DrawLate (a dialog drawn more than 2 s late: decision 5's fourth *)
(* window).                                                                 *)
(*                                                                         *)
(* Switches. Off names the mechanisms of the design turned off (a control  *)
(* run turns off one); Env names the environment behaviours turned on (the *)
(* accepted windows and residuals are on only in limit runs). Mechanisms   *)
(* holds the decided design plus four that round 4 adds: InsertCut,        *)
(* LiveSender, IdleMark and ParentCheck (round 2's section 6 proposal).    *)
(* Probe names an event whose first occurrence sets the ghost hit; a probe *)
(* run checks NeverHit.                                                    *)
(***************************************************************************)
EXTENDS Naturals, Sequences, FiniteSets

CONSTANTS
    N,               \* permission requests in the agent's turn
    NI,              \* pane answerers, each with its own send-keys (2: a double click)
    DVerdicts,       \* the verdicts decide's caller may send
    Off,             \* mechanisms of the design turned off (a subset of Mechanisms)
    Env,             \* environment behaviours turned on (a subset of EnvSwitches)
    LiveCheck,       \* rule 14: "exact" (pid, start time, namespace), "time" (can't tell:
                     \*   the settled_at fallback), "wrongns" (control: another namespace)
    Pick,            \* decision 4: "token" (B) or "oldest" (round 2's A)
    ReaderWrites,    \* hook_gone_at by a read verb: "block" (waits for the lock), "try"
                     \*   (only if the lock is free, never waits), "none"
    MaxGrabs,        \* how many times another process takes the store's write lock
    \* ---- decide's timing, in ticks of 0.5 s ----
    MaxWait,         \* --max-wait-ms (0: not passed)
    AckWait,         \* the ack wait (1 s)
    Sigma,           \* start-up before decide reads its clock
    Phi,             \* the reply after its last wait
    CallerDeadline,  \* the caller gives up (Slack's 3 s); 0: none modelled
    Probe,           \* the event whose first occurrence sets the ghost hit
    R5               \* round 5's guards on (a subset of R5Options)

(* The mechanisms of the decided design. A control run turns one off.    *)
Mechanisms == {
    "Atomic",         \* rule 1: the request and check_permission in one transaction
    "AckFirst",       \* rule 3: the hook commits "delivering" (the ack) before it answers
    "Deadline",       \* rule 4: the hook acks only with time left to answer and exit
    "DeliveryField",  \* decision 1 A: decide reports delivered / not_confirmed / fallen back
    "ReadFields",     \* decision 8 A: get-permission reports the delivery fields
    "CheckFirst",     \* rule 5's note: a reader checks the hook process before the record
    "PlainGuard",     \* decision 4 B: plain send-keys refused while a request is fallen back
    "OneKey",         \* decision 7 A: a pane answer is one key, never followed by Enter
    "HashCheck",      \* decision 7 A: --expect-pane-sha256, required on a pane answer
    "Claim",          \* decision 6 (a): record-pane-answer
    "ClaimAge",       \* decision 5: the 2 s minimum hook-gone age
    "ToolUseClose",   \* decision 6 (b): PostToolUse closes the request by its tool_use_id
    "PaneIntent",     \* rule 8: the pane answer is written down before the keys
    "IntentAwaits",   \* a pane answer of unknown outcome holds the row like an open request
    "Reconcile",      \* rule 9: find-missing repairs a stale check_permission
    "Promote",        \* rule 10: find-missing promotes a live pending row
    "Sweep",          \* decision 3 A: find-missing runs (an operator schedules it)
    "CloseOnMissing", \* rule 12: the missing mark and the close of its requests together
    "Bound",          \* decision 9 B: decide's lock waits end at MaxWait: store-busy refusal
    "InsertCut",      \* round 4: the hook's first write's lock wait ends at its deadline
    "LiveSender",     \* round 4: a pane answer is refused while another one's sender lives
    "IdleMark",       \* round 4: the idle Notification is remembered; the repair uses it
    "ParentCheck"}    \* round 2's proposal (its section 6): the hook acks only while the
                      \*   Claude Code that started it is alive

(* Environment behaviours. The passing runs turn on the first group.     *)
EnvSwitches == {
    "BusyFails",      \* a lock wait may end in a busy failure (busy_timeout_ms ran out)
    "RelayDeath",     \* a relay hook may die after its first write
    "HookDeath",      \* SessionStart, UserPromptSubmit, PostToolUse, Stop or idle may die
    "DecideDeath",    \* decide may die (once per request)
    "SKDeath",        \* each send-keys may die once, after its intent or after typing
    "Human",          \* a human types at tmux
    "Stray",          \* a stray plain send-keys (a command and Enter)
    "DrawLag",        \* a dialog is drawn a little after its hook is gone
    "Distinct",       \* dialogs of different requests differ (else decision 5, window 3)
    "Crash",          \* the agent may crash
    \* the residuals and windows the decisions accept: limit runs only
    "BlindDeath",     \* a relay hook may die before its first write (decision 2)
    "LastGasp",       \* a relay hook may die between its ack and its exit (decision 5, 1)
    "Lag",            \* keys may land after other events (decision 5, window 2)
    "DrawLate",       \* a dialog may be drawn more than 2 s late (decision 5, window 4)
    "BothEndsDie",    \* Stop and the idle Notification may both die (decision 2)
    \* controls
    "BusyAfterCommit",  \* decide says store-busy after its verdict committed
    \* round 5
    "Noise"}          \* the pane changes once for no reason AD knows (a status line, a footer)

R5Options == {"Unproven", "IdleGate", "IdleSnap", "SameScreen", "Bypass"}

ASSUME Off \subseteq Mechanisms /\ Env \subseteq EnvSwitches /\ R5 \subseteq R5Options
ASSUME LiveCheck \in {"exact", "time", "wrongns"}
ASSUME Pick \in {"oldest", "token"}
ASSUME ReaderWrites \in {"block", "try", "none"}

On(m) == m \notin Off
Atomic == On("Atomic")
AckFirst == On("AckFirst")
Deadline == On("Deadline")
DeliveryField == On("DeliveryField")
ReadFields == On("ReadFields")
CheckFirst == On("CheckFirst")
PlainGuard == On("PlainGuard")
OneKey == On("OneKey")
HashCheck == On("HashCheck")
Claim == On("Claim")
ClaimAge == On("ClaimAge")
ToolUseClose == On("ToolUseClose")
PaneIntent == On("PaneIntent")
IntentAwaits == On("IntentAwaits")
Reconcile == On("Reconcile")
Promote == On("Promote")
Sweep == On("Sweep")
CloseOnMissing == On("CloseOnMissing")
Bound == On("Bound")
InsertCut == On("InsertCut")
LiveSender == On("LiveSender")
IdleMark == On("IdleMark")
ParentCheck == On("ParentCheck")

BusyFails == "BusyFails" \in Env
RelayDeath == "RelayDeath" \in Env
HookDeath == "HookDeath" \in Env
DecideDeath == "DecideDeath" \in Env
SKDeath == "SKDeath" \in Env
Human == "Human" \in Env
Stray == "Stray" \in Env
DrawLag == "DrawLag" \in Env
Distinct == "Distinct" \in Env
Crash == "Crash" \in Env
BlindDeath == "BlindDeath" \in Env
LastGasp == "LastGasp" \in Env
Lag == "Lag" \in Env
DrawLate == "DrawLate" \in Env
BothEndsDie == "BothEndsDie" \in Env
BusyAfterCommit == "BusyAfterCommit" \in Env
Noise == "Noise" \in Env

\* b.ssz knob (see the header). A definition, not a CONSTANT, so B146R5's cfgs parse unchanged.
KnobOn  == TRUE
KnobOff == FALSE
CxR5    == FALSE

Unproven == "Unproven" \in R5
IdleGate == "IdleGate" \in R5 \/ "IdleSnap" \in R5
IdleSnap == "IdleSnap" \in R5
SameScreen == "SameScreen" \in R5
Bypass == "Bypass" \in R5

R == 1 .. N
I == 1 .. NI
Live == {"pending", "waiting", "working", "check_permission"}
LivePcs == {"t", "i", "x", "p", "a", "d", "e"}
FirstPc == IF Atomic THEN "x" ELSE "t"
Max(a, b) == IF a > b THEN a ELSE b
Min(a, b) == IF a < b THEN a ELSE b
DMax == Max(CallerDeadline, Sigma + Max(MaxWait, 1) + AckWait + Phi + 1)

VARIABLES
    \* Claude Code
    proc,     \* the agent process: "alive", "gone"
    aph,      \* "ss", "idle", "turn", "stop", "notif", "notifL" (Stop lost), "rest"
    dl,       \* each request's dialog: "none", "hook" (hook running), "lag", "pend", "ans"
    got,      \* what each request's dialog got: "none", "allow", "deny"
    scr,      \* the request whose dialog is shown; 0 = the prompt
    inq,      \* keys typed and not yet read by Claude Code
    ptu,      \* each request's PostToolUse: "none", "due" (its tool ran), "done"
    idl,      \* IdleMark: the idle Notification came after the last other hook
    \* the relay hooks
    h,        \* each relay hook: "idle", a step in LivePcs, "done", "dead"
    ph,       \* each request's phase (0, 1, 2)
    \* the store
    lk,       \* the write lock: "free", "env" (held by another process)
    envN,     \* times another process took it
    rs,       \* the spawn row: "pending", "waiting", "working", "check_permission", "missing"
    rec,      \* the request's record exists
    dec,      \* its decision: "none", "allow", "deny"
    ack,      \* the hook committed "delivering"
    pane,     \* the pane answer: "none", "intent", "sent", "outside", "ran"
    pas,      \* the caller's --as: "none", "allow", "deny", "unknown"
    cls,      \* closed by find-missing (the agent is gone)
    gone,     \* hook_gone_at is written
    old,      \* ... at least 2 s ago
    att,      \* attempted_decision is written
    \* decide (one call at a time) and its caller
    dpc,      \* "idle", "start", "chk", "ref", "w", "aw", "awr", "fin"
    dr, dv,   \* its request and verdict
    de,       \* its clock (ticks since it started)
    dmark,    \* when its ack wait (then its reply) began
    dack,     \* the record read's result ("ack", "closed", "open")
    dout,     \* what it will reply
    dwrote,   \* this call committed its verdict
    dpoll,    \* the ack wait's next read is due
    dtries,   \* calls made per request
    cb,       \* what the caller believes per request: "none", "calling", "ok", "delivered",
              \*   "notconf", "fallen", "already", "busy", "lost", "late", "closed"
    cv,       \* the verdict the caller believes delivered
    \* get-permission (the caller's poller)
    gpc,      \* "idle", "g1", "g2", "gw"
    gr, gsk,  \* its request; without CheckFirst, the record read before the check
    \* the pane answerers, their send-keys, the stray sender
    oc,       \* "idle", "read"
    snap,     \* what each saw on the pane (the shown request, 0 the prompt)
    sk,       \* each send-keys: "idle", "typing" (keys sent, "sent" not recorded)
    skt,      \* the request it is answering
    skq,      \* the keys it will type
    skd,      \* it has died once
    st,       \* the stray sender: "want", "done"
    \* ghosts
    typed,    \* AD typed keys recorded on this request
    silent,   \* a dialog was allowed by a key nobody sent as its answer
    wrong,    \* an answer meant for one request closed another's dialog
    misfire,  \* an answer landed on the prompt
    race,     \* AD typed while a relay hook was alive
    late,     \* decide's caller gave up before its reply
    busylie,  \* decide said store-busy after its verdict committed
    falsefb,  \* a reader reported fallen_back while the store had the ack or the hook lived
    hit,      \* Probe happened
    \* round 5 (each is constant unless its option or switch is on)
    unp,      \* Unproven: per request, not yet proven gone by a Claude Code hook
    quiet,    \* IdleGate: the last hook AD heard was SessionStart, Stop or the idle Notification
    snapOK,   \* IdleSnap: the pane is unchanged since AD's capture at that hook
    sOK,      \* SameScreen: the pane is unchanged since the stray sender's capture
    nz        \* Noise has happened

ccV == <<proc, aph, dl, got, scr, inq, ptu, idl>>
hookV == <<h, ph>>
storeV == <<lk, envN, rs, rec, dec, ack, pane, pas, cls, gone, old, att>>
decV == <<dpc, dr, dv, de, dmark, dack, dout, dwrote, dpoll, dtries>>
callV == <<cb, cv>>
gpV == <<gpc, gr, gsk>>
paneV == <<oc, snap, sk, skt, skq, skd, st>>
ghostV == <<typed, silent, wrong, misfire, race, late, busylie, falsefb, hit>>
r5V == <<unp, quiet, snapOK, sOK, nz>>
vars == <<ccV, hookV, storeV, decV, callV, gpV, paneV, ghostV, r5V>>

Hit(tag) == hit' = (hit \/ Probe = tag)

-----------------------------------------------------------------------------
(* Derived facts *)

Asked(r) == dl[r] # "none"
HookAlive(r) == h[r] \in LivePcs
(* A live relay hook whose request AD has a record of. Before its first   *)
(* write a hook is unknown to AD; keys typed then land on the prompt      *)
(* before its dialog is drawn (Calm), or, with Lag, are decision 5's      *)
(* second window.                                                          *)
KnownHookAlive == \E r \in R : rec[r] /\ HookAlive(r)

(* What a reader's process check says (rule 14). *)
JudgedAlive(r) ==
    CASE LiveCheck = "exact" -> HookAlive(r)
      [] LiveCheck = "time"  -> h[r] # "idle" /\ ph[r] < 2
      [] OTHER               -> FALSE
AnyJudgedAlive == \E r \in R : rec[r] /\ JudgedAlive(r)

Answered(r) == pane[r] \in {"sent", "outside", "ran"}
(* The record is closed: delivered, answered at the pane, or closed with  *)
(* its agent (rule 12: written by find-missing's mark, and read as closed *)
(* on a missing row, which also covers a record an orphan hook wrote      *)
(* after the mark).                                                        *)
Closed(r) ==
    \/ IF AckFirst THEN ack[r] ELSE dec[r] # "none"
    \/ Answered(r) \/ cls[r]
    \/ CloseOnMissing /\ rs = "missing"

(* AD reports r fallen back: ErrRelayFallenBack from decide and send-keys,  *)
(* delivery "fallen_back" on get and get-permission (rule 5, round 3 wording: *)
(* "hook gone, no pane answer recorded through AD").                         *)
Reported(r) == rec[r] /\ ~Closed(r) /\ pane[r] \in {"none", "intent"} /\ ~JudgedAlive(r)
Open == {r \in R : Reported(r)}
Oldest == CHOOSE r \in Open : \A q \in Open : r <= q

(* A request that still holds the row in check_permission. *)
Awaiting(r) ==
    rec[r] /\ ~Closed(r) /\ (pane[r] = "none" \/ (IntentAwaits /\ pane[r] = "intent"))
AnyAwaiting == \E r \in R : Awaiting(r)

SenderAlive(r) == \E i \in I : sk[i] \in {"intent", "typing"} /\ skt[i] = r

(* What a record read finds. *)
RecK(r) == IF ack[r] THEN "ack" ELSE IF Closed(r) THEN "closed" ELSE "open"

Quiet == Lag \/ inq = <<>>
(* Without Lag, nothing changes the screen between a send-keys's check   *)
(* (its capture and intent) and its keys landing. With Lag it can: a      *)
(* human key, a new dialog or the stray in those milliseconds is decision *)
(* 5's second window.                                                      *)
Calm == Lag \/ (inq = <<>> /\ \A i \in I : sk[i] # "intent")
Hash(s) == IF s = 0 THEN 0 ELSE IF Distinct THEN s ELSE N + 1
NextScr(s) == LET P == {q \in R : q # s /\ dl[q] = "pend"} IN IF P = {} THEN {0} ELSE P

Key(k, tag, t) == [k |-> k, tag |-> tag, t |-> t]
Keys(t, k) == <<Key(k, "ans", t)>> \o (IF OneKey THEN <<>> ELSE <<Key("enter", "extra", t)>>)
StrayKeys == <<Key("txt", "cmd", 0), Key("enter", "cmd", 0)>>

(* Row writes by hooks: only a live row; a working transition is held while *)
(* a request awaits its answer (hook_writes.go's retention hold).            *)
RowTo(s) == IF rs \in Live THEN s ELSE rs
WorkingRow(aw) == IF rs \notin Live THEN rs ELSE IF aw THEN rs ELSE "working"

(* hook_gone_at, written by whoever first finds r fallen back. *)
MarkGone(r) == gone' = IF Reported(r) THEN [gone EXCEPT ![r] = TRUE] ELSE gone

(* The hook of r is gone without an answer: Claude Code draws its dialog. *)
DialogUp(r) ==
    IF proc # "alive" THEN UNCHANGED <<dl, scr>>
    ELSE IF DrawLag THEN dl' = [dl EXCEPT ![r] = "lag"] /\ UNCHANGED scr
    ELSE dl' = [dl EXCEPT ![r] = "pend"] /\ scr' = IF scr = 0 THEN r ELSE scr

CutDue == Bound /\ MaxWait > 0 /\ de >= MaxWait
AWEnd == Min(DMax, IF Bound /\ MaxWait > 0 THEN Min(dmark + AckWait, MaxWait) ELSE dmark + AckWait)

(* Round 5. The pane's bytes: what Claude Code has drawn. A guard compares *)
(* them only with an earlier copy. A key Claude Code read changes them.    *)
(* The turn's phase as drawn: start-up, the prompt, the turn's output,   *)
(* its end. The Stop and idle hooks themselves draw nothing.              *)
VisPh == CASE aph = "ss" -> 0 [] aph = "idle" -> 1 [] aph = "turn" -> 2 [] OTHER -> 3
Scrn == <<scr, VisPh, got, ptu, nz>>
Changed == Scrn' # Scrn \/ Len(inq') < Len(inq)

SnapT == snapOK' = IF IdleSnap THEN snapOK /\ ~Changed ELSE snapOK
SnapCap == snapOK' = IF IdleSnap THEN TRUE ELSE snapOK
SOKT == sOK' = IF SameScreen THEN sOK /\ ~Changed ELSE sOK
QuietTo(b) == quiet' = IF IdleGate THEN b ELSE quiet
UnpSet(r) == unp' = IF Unproven THEN [unp EXCEPT ![r] = TRUE] ELSE unp
UnpClear(r) == unp' = IF Unproven THEN [unp EXCEPT ![r] = FALSE] ELSE unp
UnpAll == unp' = IF Unproven THEN [q \in R |-> FALSE] ELSE unp

(* The round-5 guards on a plain send-keys. *)
R5OK ==
    /\ Unproven => \A r \in R : ~unp[r]
    /\ IdleGate => quiet
    /\ IdleSnap => snapOK
(* A plain send-keys that only round 5 refuses. *)
StrayHeld == st \in {"want", "read"} /\ ~R5OK
(* AD reports a request closed (delivered, answered at the pane) that     *)
(* Claude Code has not yet proven gone (Unproven's field on get and       *)
(* get-permission): a dialog may sit on screen.                           *)
UnprovenClosed == Unproven /\ \E r \in R : unp[r] /\ Closed(r)
(* Bypass: a reason AD gives an intelligence to look at the pane. *)
LookAsked == StrayHeld \/ UnprovenClosed

-----------------------------------------------------------------------------
Init ==
    /\ proc = "alive" /\ aph = "ss" /\ dl = [r \in R |-> "none"] /\ got = [r \in R |-> "none"]
    /\ scr = 0 /\ inq = <<>> /\ ptu = [r \in R |-> "none"] /\ idl = FALSE
    /\ h = [r \in R |-> "idle"] /\ ph = [r \in R |-> 0]
    /\ lk = "free" /\ envN = 0 /\ rs = "pending"
    /\ rec = [r \in R |-> FALSE] /\ dec = [r \in R |-> "none"] /\ ack = [r \in R |-> FALSE]
    /\ pane = [r \in R |-> "none"] /\ pas = [r \in R |-> "none"] /\ cls = [r \in R |-> FALSE]
    /\ gone = [r \in R |-> FALSE] /\ old = [r \in R |-> FALSE] /\ att = [r \in R |-> FALSE]
    /\ dpc = "idle" /\ dr = 1 /\ dv = "allow" /\ de = 0 /\ dmark = 0
    /\ dack = "open" /\ dout = "none" /\ dwrote = FALSE /\ dpoll = FALSE
    /\ dtries = [r \in R |-> 0]
    /\ cb = [r \in R |-> "none"] /\ cv = [r \in R |-> "none"]
    /\ gpc = "idle" /\ gr = 1 /\ gsk = "open"
    /\ oc = [i \in I |-> "idle"] /\ snap = [i \in I |-> 0]
    /\ sk = [i \in I |-> "idle"] /\ skt = [i \in I |-> 0] /\ skd = [i \in I |-> FALSE]
    /\ skq = [i \in I |-> <<>>]
    /\ st = IF Stray THEN "want" ELSE "done"
    /\ typed = [r \in R |-> FALSE]
    /\ silent = FALSE /\ wrong = FALSE /\ misfire = FALSE /\ race = FALSE
    /\ late = FALSE /\ busylie = FALSE /\ falsefb = FALSE /\ hit = FALSE
    /\ unp = [r \in R |-> FALSE] /\ quiet = FALSE /\ snapOK = FALSE /\ sOK = FALSE /\ nz = FALSE

-----------------------------------------------------------------------------
(* The store's write lock, held now and then by another process. *)

EnvGrab ==
    /\ lk = "free" /\ envN < MaxGrabs
    /\ lk' = "env" /\ envN' = envN + 1
    /\ UNCHANGED <<ccV, hookV, rs, rec, dec, ack, pane, pas, cls, gone, old, att,
                   decV, callV, gpV, paneV, ghostV>>

EnvRelease ==
    /\ lk = "env"
    /\ lk' = "free"
    /\ UNCHANGED <<ccV, hookV, envN, rs, rec, dec, ack, pane, pas, cls, gone, old, att,
                   decV, callV, gpV, paneV, ghostV>>

-----------------------------------------------------------------------------
(* Claude Code: the lifecycle hooks. Each is one gated store write, or a  *)
(* death before it (its write is lost).                                     *)

SessionStart ==
    /\ aph = "ss" /\ proc = "alive" /\ lk = "free"
    /\ rs' = RowTo("waiting") /\ aph' = "idle" /\ idl' = FALSE
    /\ QuietTo(TRUE) /\ UNCHANGED <<unp, nz>>
    /\ UNCHANGED <<proc, dl, got, scr, inq, ptu, hookV, lk, envN, rec, dec, ack, pane, pas,
                   cls, gone, old, att, decV, callV, gpV, paneV, ghostV>>

(* The turn starts (the spawn's prompt): UserPromptSubmit writes working. *)
TurnStart ==
    /\ aph = "idle" /\ proc = "alive" /\ lk = "free"
    /\ rs' = WorkingRow(AnyAwaiting) /\ aph' = "turn" /\ idl' = FALSE
    /\ QuietTo(FALSE) /\ UNCHANGED <<unp, nz>>
    /\ UNCHANGED <<proc, dl, got, scr, inq, ptu, hookV, lk, envN, rec, dec, ack, pane, pas,
                   cls, gone, old, att, decV, callV, gpV, paneV, ghostV>>

Ask(r) ==
    /\ aph = "turn" /\ proc = "alive" /\ dl[r] = "none" /\ IF r = 1 THEN TRUE ELSE Asked(r - 1)
    /\ dl' = [dl EXCEPT ![r] = "hook"] /\ h' = [h EXCEPT ![r] = FirstPc]
    /\ UNCHANGED <<proc, aph, got, scr, inq, ptu, idl, ph, storeV, decV, callV, gpV, paneV,
                   ghostV>>

(* PostToolUse of a tool that ran: working (held while another request    *)
(* awaits), and with ToolUseClose the close of its own request when it is *)
(* still open: the tool ran, so its dialog was answered allow.             *)
(* b.ssz, CxR5: no tool_use_id, so no close and no proof.                  *)
PostToolUse(r) ==
    /\ ptu[r] = "due" /\ proc = "alive" /\ lk = "free"
    /\ LET closeIt == ToolUseClose /\ ~CxR5 /\ rec[r] /\ ~Closed(r) /\ pane[r] \in {"none", "intent"}
           pane2 == IF closeIt THEN [pane EXCEPT ![r] = "ran"] ELSE pane
           awaitOther == \E q \in R : q # r /\ Awaiting(q)
           awaitSelf == Awaiting(r) /\ ~closeIt
       IN /\ pane' = pane2
          /\ dec' = IF closeIt THEN [dec EXCEPT ![r] = "allow"] ELSE dec
          /\ rs' = WorkingRow(awaitOther \/ awaitSelf)
          /\ hit' = (hit \/ (Probe = "ran" /\ closeIt)
                         \/ (Probe = "lateptu" /\ pane[r] = "outside"))
    /\ ptu' = [ptu EXCEPT ![r] = "done"] /\ idl' = FALSE
    \* round 5: its tool ran, so Claude Code is past r's dialog (b.ssz: no proof with CxR5)
    /\ IF CxR5 THEN UNCHANGED unp ELSE UnpClear(r)
    /\ QuietTo(FALSE) /\ UNCHANGED nz
    /\ UNCHANGED <<proc, aph, dl, got, scr, inq, hookV, lk, envN, rec, ack, pas, cls, gone,
                   old, att, decV, callV, gpV, paneV, typed, silent, wrong, misfire, race,
                   late, busylie, falsefb>>

TurnEnd ==
    /\ aph = "turn" /\ \A r \in R : dl[r] = "ans" /\ ptu[r] # "due"
    /\ aph' = "stop"
    /\ UNCHANGED <<proc, dl, got, scr, inq, ptu, idl, hookV, storeV, decV, callV, gpV, paneV,
                   ghostV>>

(* b.ssz, CxR5: no idle Notification follows: straight to rest.           *)
StopHook ==
    /\ aph = "stop" /\ proc = "alive" /\ lk = "free"
    /\ rs' = RowTo("waiting") /\ aph' = (IF CxR5 THEN "rest" ELSE "notif") /\ idl' = FALSE
    \* round 5: the turn ended, so no dialog of it is up
    /\ UnpAll /\ QuietTo(TRUE) /\ UNCHANGED nz
    /\ UNCHANGED <<proc, dl, got, scr, inq, ptu, hookV, lk, envN, rec, dec, ack, pane, pas,
                   cls, gone, old, att, decV, callV, gpV, paneV, ghostV>>

(* The idle-prompt Notification: working -> waiting; with IdleMark it is *)
(* remembered (until the next hook), and a check_permission that no      *)
(* request holds also goes to waiting.                                   *)
IdleNotif ==
    /\ ~CxR5   \* b.ssz: Codex sends none (aph never reaches notif with CxR5)
    /\ aph \in {"notif", "notifL"} /\ proc = "alive" /\ lk = "free"
    /\ rs' = IF rs = "working" \/ (IdleMark /\ rs = "check_permission" /\ ~AnyAwaiting)
             THEN "waiting" ELSE rs
    /\ aph' = "rest" /\ idl' = IdleMark
    /\ UnpAll /\ QuietTo(TRUE) /\ UNCHANGED nz
    /\ UNCHANGED <<proc, dl, got, scr, inq, ptu, hookV, lk, envN, rec, dec, ack, pane, pas,
                   cls, gone, old, att, decV, callV, gpV, paneV, ghostV>>

(* A lifecycle hook dies before its write. *)
LifeDie ==
    /\ HookDeath /\ proc = "alive"
    /\ \/ aph = "ss" /\ aph' = "idle" /\ Hit("ssdeath") /\ UNCHANGED ptu
       \/ aph = "idle" /\ aph' = "turn" /\ Hit("upsdeath") /\ UNCHANGED ptu
       \/ /\ aph = "stop" /\ (CxR5 => BothEndsDie)
          /\ aph' = (IF CxR5 THEN "rest" ELSE "notifL")
          /\ Hit("stopdeath") /\ UNCHANGED ptu
       \/ /\ (aph = "notif" \/ (aph = "notifL" /\ BothEndsDie))
          /\ aph' = "rest" /\ Hit("idledeath") /\ UNCHANGED ptu
       \/ \E r \in R : ptu[r] = "due" /\ ptu' = [ptu EXCEPT ![r] = "done"] /\ Hit("ptudeath")
                       /\ UNCHANGED aph
    /\ UNCHANGED <<proc, dl, got, scr, inq, idl, hookV, storeV, decV, callV, gpV, paneV,
                   typed, silent, wrong, misfire, race, late, busylie, falsefb>>

(* The agent crashes: its screen and keys are gone; its relay hooks may  *)
(* live on (orphans) until their own deadline. A crash between a hook's   *)
(* ack and its exit is decision 5's first window (LastGasp).              *)
AgentCrash ==
    /\ Crash /\ proc = "alive"
    /\ (\E r \in R : h[r] = "e") => LastGasp
    /\ proc' = "gone" /\ aph' = "rest" /\ scr' = 0 /\ inq' = <<>>
    /\ dl' = [r \in R |-> IF dl[r] \in {"hook", "lag", "pend"} THEN "ans" ELSE dl[r]]
    /\ ptu' = [r \in R |-> IF ptu[r] = "due" THEN "done" ELSE ptu[r]]
    /\ Hit("crash")
    /\ UNCHANGED <<got, idl, hookV, storeV, decV, callV, gpV, paneV, typed, silent, wrong,
                   misfire, race, late, busylie, falsefb>>

-----------------------------------------------------------------------------
(* The relay hook of request r. *)

RelayUnch == <<callV, gpV, paneV, typed, silent, wrong, misfire, race, late, busylie, falsefb>>

(* ParentCheck: the hook acks only while the Claude Code that started it  *)
(* lives (its parent pid is unchanged); an orphan exits without an answer. *)
ParentOK == ParentCheck => proc = "alive"

(* The request and check_permission, one transaction (Atomic). *)
HX(r) ==
    /\ h[r] = "x" /\ lk = "free"
    /\ h' = [h EXCEPT ![r] = "p"] /\ rec' = [rec EXCEPT ![r] = TRUE]
    /\ rs' = RowTo("check_permission") /\ idl' = FALSE
    /\ UnpSet(r) /\ QuietTo(FALSE) /\ UNCHANGED nz
    /\ UNCHANGED <<proc, aph, dl, got, scr, inq, ptu, ph, lk, envN, dec, ack, pane, pas, cls,
                   gone, old, att, decV, RelayUnch, hit>>

(* Without Atomic: check_permission, then the request. *)
HT(r) ==
    /\ h[r] = "t" /\ lk = "free"
    /\ h' = [h EXCEPT ![r] = "i"] /\ rs' = RowTo("check_permission") /\ idl' = FALSE
    /\ UnpSet(r) /\ QuietTo(FALSE) /\ UNCHANGED nz
    /\ UNCHANGED <<proc, aph, dl, got, scr, inq, ptu, ph, lk, envN, rec, dec, ack, pane, pas,
                   cls, gone, old, att, decV, RelayUnch, hit>>

HI(r) ==
    /\ h[r] = "i" /\ lk = "free"
    /\ h' = [h EXCEPT ![r] = "p"] /\ rec' = [rec EXCEPT ![r] = TRUE]
    /\ UnpSet(r) /\ UNCHANGED <<quiet, nz>>
    /\ UNCHANGED <<ccV, ph, lk, envN, rs, dec, ack, pane, pas, cls, gone, old, att, decV,
                   RelayUnch, hit>>

(* Its first write cannot get the lock: a busy failure, or (InsertCut) its *)
(* own deadline ended the wait. Fail-closed deny, nothing recorded.        *)
HFirstFail(r) ==
    /\ h[r] \in {"x", "t", "i"} /\ lk = "env"
    /\ BusyFails \/ (InsertCut /\ ph[r] >= 1)
    /\ h' = [h EXCEPT ![r] = "done"]
    /\ IF proc = "alive" THEN dl' = [dl EXCEPT ![r] = "ans"] /\ got' = [got EXCEPT ![r] = "deny"]
                         ELSE UNCHANGED <<dl, got>>
    /\ Hit("firstfail")
    /\ UNCHANGED <<proc, aph, scr, inq, ptu, idl, ph, storeV, decV, RelayUnch>>

(* Poll: a decision read goes to the ack; at the poll end (phase 1) the  *)
(* timeout path.                                                          *)
HP(r) ==
    /\ h[r] = "p"
    /\ \/ (dec[r] # "none" \/ cls[r]) /\ h' = [h EXCEPT ![r] = IF AckFirst THEN "a" ELSE "e"]
       \/ dec[r] = "none" /\ ~cls[r] /\ ph[r] >= 1 /\ h' = [h EXCEPT ![r] = "d"]
    /\ UNCHANGED <<ccV, ph, storeV, decV, RelayUnch, hit>>

(* The ack: only for a decided, unacked record with no pane answer that is *)
(* not closed; otherwise the hook exits without an answer.                *)
HA(r) ==
    /\ h[r] = "a" /\ lk = "free" /\ Calm
    /\ IF dec[r] # "none" /\ ~ack[r] /\ pane[r] = "none" /\ ~cls[r] /\ ParentOK
       THEN h' = [h EXCEPT ![r] = "e"] /\ ack' = [ack EXCEPT ![r] = TRUE] /\ UNCHANGED <<dl, scr>>
       ELSE h' = [h EXCEPT ![r] = "done"] /\ DialogUp(r) /\ UNCHANGED ack
    /\ UNCHANGED <<proc, aph, got, inq, ptu, idl, ph, lk, envN, rs, rec, dec, pane, pas, cls,
                   gone, old, att, decV, RelayUnch, hit>>

(* The timeout path. AckFirst: deny and ack in one statement, guarded the *)
(* same way; a decision recorded first is acked instead. Without it      *)
(* (today): records deny if undecided and answers what is recorded.      *)
HD(r) ==
    /\ h[r] = "d" /\ lk = "free" /\ Calm
    /\ IF ~AckFirst
       THEN /\ dec' = [dec EXCEPT ![r] = IF dec[r] = "none" THEN "deny" ELSE dec[r]]
            /\ h' = [h EXCEPT ![r] = "e"] /\ UNCHANGED <<ack, dl, scr>>
       ELSE IF dec[r] = "none" /\ pane[r] = "none" /\ ~cls[r] /\ ParentOK
       THEN /\ dec' = [dec EXCEPT ![r] = "deny"] /\ ack' = [ack EXCEPT ![r] = TRUE]
            /\ h' = [h EXCEPT ![r] = "e"] /\ UNCHANGED <<dl, scr>>
       ELSE IF dec[r] # "none" /\ ~ack[r] /\ pane[r] = "none" /\ ~cls[r] /\ ParentOK
       THEN h' = [h EXCEPT ![r] = "a"] /\ UNCHANGED <<dec, ack, dl, scr>>
       ELSE h' = [h EXCEPT ![r] = "done"] /\ DialogUp(r) /\ UNCHANGED <<dec, ack>>
    /\ UNCHANGED <<proc, aph, got, inq, ptu, idl, ph, lk, envN, rs, rec, pane, pas, cls, gone,
                   old, att, decV, RelayUnch, hit>>

(* The answer and the exit: Claude Code applies it; no dialog is drawn. *)
HE(r) ==
    /\ h[r] = "e"
    /\ h' = [h EXCEPT ![r] = "done"]
    /\ LET v == IF dec[r] = "none" THEN "deny" ELSE dec[r] IN
       IF proc = "alive"
       THEN /\ dl' = [dl EXCEPT ![r] = "ans"] /\ got' = [got EXCEPT ![r] = v]
            /\ ptu' = IF v = "allow" THEN [ptu EXCEPT ![r] = "due"] ELSE ptu
       ELSE UNCHANGED <<dl, got, ptu>>
    /\ UNCHANGED <<proc, aph, scr, inq, idl, ph, storeV, decV, RelayUnch, hit>>

(* Death: before the first write only with BlindDeath; between the ack and *)
(* the exit only with LastGasp. Claude Code draws the dialog.             *)
HDie(r) ==
    /\ RelayDeath /\ HookAlive(r) /\ Calm
    /\ h[r] = FirstPc => BlindDeath
    /\ (h[r] = "e" /\ AckFirst) => LastGasp
    /\ h' = [h EXCEPT ![r] = "dead"] /\ DialogUp(r)
    /\ hit' = (hit \/ (Probe = "relaydeath" /\ h[r] # FirstPc) \/ (Probe = "gasp" /\ h[r] = "e"))
    /\ UNCHANGED <<proc, aph, got, inq, ptu, idl, ph, storeV, decV, RelayUnch>>

(* Time passes for request r. At phase 2 Claude Code kills a hook still   *)
(* alive (an orphan of a crashed agent is not killed). Deadline: an acked *)
(* hook exits before its kill. InsertCut: a hook still waiting for its    *)
(* first write gives up before its kill (HFirstFail).                     *)
Tick(r) ==
    /\ h[r] # "idle" /\ ph[r] < 2 /\ (HookAlive(r) \/ LiveCheck = "time") /\ Calm
    /\ ph[r] = 1 => ~(Deadline /\ h[r] = "e")
    \* a hook still before its first write at its kill is in its lock wait (a stall
    \* that long is a death before the first write: BlindDeath); InsertCut ends that
    \* wait first (HFirstFail)
    /\ (ph[r] = 1 /\ h[r] \in {"x", "t", "i"}) => (~InsertCut /\ lk = "env")
    /\ ph' = [ph EXCEPT ![r] = ph[r] + 1]
    /\ IF ph[r] = 1 /\ HookAlive(r) /\ proc = "alive"
       THEN /\ h' = [h EXCEPT ![r] = "dead"] /\ DialogUp(r)
            /\ hit' = (hit \/ Probe = "killed" \/ (Probe = "traceless" /\ ~rec[r]))
       ELSE UNCHANGED <<h, dl, scr, hit>>
    /\ UNCHANGED <<proc, aph, got, inq, ptu, idl, storeV, decV, RelayUnch>>

Draw(r) ==
    /\ dl[r] = "lag" /\ proc = "alive" /\ Calm
    /\ dl' = [dl EXCEPT ![r] = "pend"] /\ scr' = IF scr = 0 THEN r ELSE scr
    /\ UNCHANGED <<proc, aph, got, inq, ptu, idl, hookV, storeV, decV, callV, gpV, paneV,
                   ghostV>>

(* hook_gone_at is 2 s old. The timing grant: Claude Code has drawn the  *)
(* dialog by then, unless DrawLate.                                       *)
Age(r) ==
    /\ gone[r] /\ ~old[r] /\ (dl[r] # "lag" \/ DrawLate)
    /\ old' = [old EXCEPT ![r] = TRUE]
    /\ UNCHANGED <<ccV, hookV, lk, envN, rs, rec, dec, ack, pane, pas, cls, gone, att, decV,
                   callV, gpV, paneV, ghostV>>

-----------------------------------------------------------------------------
(* Keys: Claude Code reads one key and applies it to the screen. *)

Consume ==
    /\ inq # <<>> /\ proc = "alive"
    /\ LET e == Head(inq) IN
       /\ inq' = Tail(inq)
       /\ IF scr > 0 /\ e.k # "txt"
          THEN LET v == IF e.k = "esc" THEN "deny" ELSE "allow" IN
               \E nx \in NextScr(scr) :
                 /\ dl' = [dl EXCEPT ![scr] = "ans"] /\ got' = [got EXCEPT ![scr] = v]
                 /\ ptu' = IF v = "allow" THEN [ptu EXCEPT ![scr] = "due"] ELSE ptu
                 /\ scr' = nx
                 /\ silent' = (silent \/ (e.tag # "ans" /\ v = "allow"))
                 /\ wrong' = (wrong \/ (e.tag = "ans" /\ e.t # scr))
                 /\ UNCHANGED misfire
          ELSE /\ UNCHANGED <<dl, got, ptu, scr, silent, wrong>>
               /\ misfire' = (misfire \/ (scr = 0 /\ e.tag = "ans"))
    /\ UNCHANGED <<proc, aph, idl, hookV, storeV, decV, callV, gpV, paneV, typed, race, late,
                   busylie, falsefb, hit>>

(* A human at tmux answers the shown dialog. AD records nothing. *)
HumanKey ==
    /\ Human /\ Calm /\ scr > 0 /\ proc = "alive"
    /\ \E v \in {"allow", "deny"}, nx \in NextScr(scr) :
         /\ dl' = [dl EXCEPT ![scr] = "ans"] /\ got' = [got EXCEPT ![scr] = v]
         /\ ptu' = IF v = "allow" THEN [ptu EXCEPT ![scr] = "due"] ELSE ptu
         /\ scr' = nx
    /\ Hit("human")
    /\ UNCHANGED <<proc, aph, inq, idl, hookV, storeV, decV, callV, gpV, paneV, typed, silent,
                   wrong, misfire, race, late, busylie, falsefb>>

-----------------------------------------------------------------------------
(* decide, one call at a time, by the caller of a request it can see     *)
(* (a click: not fair). Its clock is de; DTick advances it while decide   *)
(* starts up, waits for the lock, waits for the ack, or replies. Its      *)
(* other steps take no time.                                              *)

DecUnch == <<gpV, paneV, typed, silent, wrong, misfire, race>>

DCall(r, v) ==
    /\ dpc = "idle" /\ rec[r] /\ dtries[r] < 2 /\ v \in DVerdicts
    /\ cb[r] \in {"none", "busy", "lost", "late"}
    /\ dpc' = (IF Sigma > 0 THEN "start" ELSE "chk") /\ dr' = r /\ dv' = v
    /\ dtries' = [dtries EXCEPT ![r] = dtries[r] + 1]
    /\ cb' = [cb EXCEPT ![r] = "calling"]
    /\ hit' = (hit \/ (Probe = "busyretry" /\ cb[r] = "busy"))
    /\ UNCHANGED <<ccV, hookV, storeV, de, dmark, dack, dout, dwrote, dpoll, cv, DecUnch, late,
                   busylie, falsefb>>

DStart ==
    /\ dpc = "start"
    /\ dpc' = "chk"
    /\ UNCHANGED <<ccV, hookV, storeV, dr, dv, de, dmark, dack, dout, dwrote, dpoll,
                   dtries, callV, DecUnch, late, busylie, falsefb, hit>>

(* The process check, then the record: refuse a closed or decided one, a   *)
(* fallen-back one (ErrRelayFallenBack, after writing attempted_decision  *)
(* and hook_gone_at), else write. Without DeliveryField (today): the      *)
(* relay window's cutoff (phase 0) instead of the process.                *)
DChk ==
    /\ dpc = "chk"
    /\ IF ~DeliveryField
       THEN IF ph[dr] = 0 THEN dpc' = "w" /\ UNCHANGED <<dout, dmark>>
            ELSE dpc' = "fin" /\ dmark' = de
                 /\ dout' = IF dec[dr] = "none" /\ ~cls[dr] THEN "fallen" ELSE "already"
       ELSE IF rs \notin Live \/ Closed(dr) \/ dec[dr] # "none" \/ Answered(dr)
       THEN dpc' = "fin" /\ dout' = "already" /\ dmark' = de
       ELSE IF JudgedAlive(dr) THEN dpc' = "w" /\ UNCHANGED <<dout, dmark>>
       ELSE dpc' = "ref" /\ UNCHANGED <<dout, dmark>>
    /\ UNCHANGED <<ccV, hookV, storeV, dr, dv, de, dack, dwrote, dpoll, dtries, callV,
                   DecUnch, late, busylie, falsefb, hit>>

(* The refusal's write: attempted_decision and hook_gone_at, one statement. *)
DRef ==
    /\ dpc = "ref" /\ lk = "free"
    /\ att' = [att EXCEPT ![dr] = TRUE] /\ MarkGone(dr)
    /\ dpc' = "fin" /\ dout' = "fallen" /\ dmark' = de
    /\ UNCHANGED <<ccV, hookV, lk, envN, rs, rec, dec, ack, pane, pas, cls, old, dr, dv, de,
                   dack, dwrote, dpoll, dtries, callV, DecUnch, late, busylie, falsefb, hit>>

(* ... cut by the bound or a busy failure: the refusal stands, nothing written. *)
DRefCut ==
    /\ dpc = "ref" /\ lk = "env" /\ (CutDue \/ BusyFails)
    /\ dpc' = "fin" /\ dout' = "fallen" /\ dmark' = de
    /\ Hit("refcut")
    /\ UNCHANGED <<ccV, hookV, storeV, dr, dv, de, dack, dwrote, dpoll, dtries, callV,
                   DecUnch, late, busylie, falsefb>>

(* The verdict UPDATE, guarded: undecided, unacked, no pane answer, not  *)
(* closed, its row live (rule 12).                                        *)
DW ==
    /\ dpc = "w" /\ lk = "free"
    /\ IF dec[dr] = "none" /\ ~ack[dr] /\ pane[dr] = "none" /\ ~cls[dr] /\ rs \in Live
       THEN /\ dec' = [dec EXCEPT ![dr] = dv] /\ dwrote' = TRUE /\ dmark' = de
            /\ IF DeliveryField THEN dpc' = "aw" /\ dpoll' = TRUE /\ UNCHANGED dout
                                ELSE dpc' = "fin" /\ dout' = "ok" /\ UNCHANGED dpoll
       ELSE dpc' = "fin" /\ dout' = "already" /\ dmark' = de /\ UNCHANGED <<dec, dwrote, dpoll>>
    /\ UNCHANGED <<ccV, hookV, lk, envN, rs, rec, ack, pane, pas, cls, gone, old, att, dr, dv,
                   de, dack, dtries, callV, DecUnch, late, busylie, falsefb, hit>>

(* The lock wait ends without the lock: the bound (Bound) or a busy      *)
(* failure. Store-busy, nothing written.                                  *)
DWCut ==
    /\ dpc = "w" /\ lk = "env" /\ (CutDue \/ BusyFails)
    /\ dpc' = "fin" /\ dout' = "busy" /\ dmark' = de
    /\ UNCHANGED <<ccV, hookV, storeV, dr, dv, de, dack, dwrote, dpoll, dtries, callV,
                   DecUnch, late, busylie, falsefb, hit>>

(* The ack wait: each read is the process check and the record read. With *)
(* CheckFirst the check comes first: a hook found gone writes nothing more, *)
(* so the record read after it is final, and the two can be taken as one   *)
(* step. Without it (a control) the record is read first (DPollRec), and   *)
(* the check comes later (DPoll).                                          *)
DPollRes(alive, rk) ==
    CASE rk = "ack" -> "delivered"
      [] rk = "closed" -> "already"
      [] ~alive -> "fallen"
      [] de >= AWEnd -> IF BusyAfterCommit /\ CutDue THEN "busy" ELSE "notconf"
      [] OTHER -> "wait"

DPollRec ==
    /\ dpc = "aw" /\ dpoll /\ ~CheckFirst
    /\ dack' = RecK(dr) /\ dpc' = "awr"
    /\ UNCHANGED <<ccV, hookV, storeV, dr, dv, de, dmark, dout, dwrote, dpoll, dtries, callV,
                   DecUnch, late, busylie, falsefb, hit>>

DPoll ==
    /\ \/ dpc = "aw" /\ dpoll /\ CheckFirst
       \/ dpc = "awr"
    /\ LET res == DPollRes(JudgedAlive(dr), IF CheckFirst THEN RecK(dr) ELSE dack) IN
       IF res = "wait"
       THEN dpc' = "aw" /\ dpoll' = FALSE /\ UNCHANGED <<dout, dmark, falsefb>>
       ELSE /\ dpc' = "fin" /\ dout' = res /\ dmark' = de /\ dpoll' = FALSE
            /\ falsefb' = (falsefb \/ (res = "fallen" /\ (ack[dr] \/ HookAlive(dr))))
    /\ dack' = "open"
    /\ UNCHANGED <<ccV, hookV, storeV, dr, dv, de, dwrote, dtries, callV, DecUnch,
                   late, busylie, hit>>

DWaiting ==
    \/ dpc = "start" /\ de < Sigma
    \/ dpc \in {"w", "ref"} /\ lk = "env" /\ ~CutDue
    \/ dpc = "aw" /\ ~dpoll
    \/ dpc = "fin" /\ de < dmark + Phi

(* decide's clock. Its caller gives up at CallerDeadline. *)
DTick ==
    /\ dpc # "idle" /\ DWaiting
    /\ de < DMax \/ (dpc = "aw" /\ ~dpoll)
    /\ de' = Min(de + 1, DMax)
    /\ dpoll' = (dpc = "aw")
    /\ IF CallerDeadline > 0 /\ de + 1 = CallerDeadline /\ cb[dr] = "calling"
       THEN cb' = [cb EXCEPT ![dr] = "late"] /\ late' = TRUE
       ELSE UNCHANGED <<cb, late>>
    /\ UNCHANGED <<ccV, hookV, storeV, dpc, dr, dv, dmark, dack, dout, dwrote, dtries,
                   cv, DecUnch, busylie, falsefb, hit>>

(* decide's variables at rest. *)
DIdle ==
    /\ dpc' = "idle" /\ dr' = 1 /\ dv' = "allow" /\ de' = 0 /\ dmark' = 0 /\ dack' = "open"
    /\ dout' = "none" /\ dwrote' = FALSE /\ dpoll' = FALSE

(* The reply. A caller that gave up gets nothing. *)
DRep ==
    /\ dpc = "fin"
    /\ DIdle
    /\ IF cb[dr] = "calling"
       THEN /\ cb' = [cb EXCEPT ![dr] = dout]
            /\ cv' = IF dout \in {"ok", "delivered"} THEN [cv EXCEPT ![dr] = dv] ELSE cv
       ELSE UNCHANGED <<cb, cv>>
    /\ busylie' = (busylie \/ (dout = "busy" /\ dwrote))
    /\ hit' = (hit \/ Probe = dout
                   \/ (Probe = "busyfallen" /\ dtries[dr] = 2 /\ dout = "fallen")
                   \/ (Probe = "notconfacked" /\ dout = "notconf" /\ ack[dr]))
    /\ UNCHANGED <<ccV, hookV, storeV, dtries, DecUnch, late, falsefb>>

DDie ==
    /\ DecideDeath /\ dpc # "idle" /\ dtries[dr] = 1
    /\ DIdle
    /\ cb' = IF cb[dr] = "calling" THEN [cb EXCEPT ![dr] = "lost"] ELSE cb
    /\ Hit("decdeath")
    /\ UNCHANGED <<ccV, hookV, storeV, dtries, cv, DecUnch, late, busylie, falsefb>>

-----------------------------------------------------------------------------
(* get-permission: the caller's poller follows up a call that did not end *)
(* in a final answer. The process check, then the record (CheckFirst).    *)

GpUnch == <<decV, paneV, typed, silent, wrong, misfire, race, late, busylie>>

(* The reply, from a process check (alive) and a record read (rk). *)
GRes(r, alive, rk) ==
    IF ~ReadFields THEN (IF dec[r] # "none" THEN "ok" ELSE "pending")
    ELSE CASE rk = "ack" -> "delivered"
           [] rk = "closed" -> "closed"
           [] ~alive -> "fallen"
           [] OTHER -> "notconf"

GReply(r, res) ==
    /\ gpc' = "idle" /\ gr' = 1 /\ gsk' = "open"
    /\ cb' = IF res \in {"delivered", "ok", "fallen", "closed"} THEN [cb EXCEPT ![r] = res] ELSE cb
    /\ cv' = IF res \in {"delivered", "ok"} THEN [cv EXCEPT ![r] = dec[r]] ELSE cv
    /\ falsefb' = (falsefb \/ (res = "fallen" /\ (ack[r] \/ HookAlive(r))))

(* The reply, and with it hook_gone_at by ReaderWrites: "try" writes it   *)
(* only when the lock is free; "block" waits for the lock (GW).           *)
GAnswer(r, res) ==
    IF res = "fallen" /\ ReaderWrites = "block" /\ ~gone[r]
    THEN /\ gpc' = "gw" /\ gr' = r /\ gsk' = "open"
         /\ UNCHANGED <<storeV, callV, falsefb, hit>>
    ELSE /\ GReply(r, res)
         /\ IF res = "fallen" /\ ReaderWrites = "try" /\ lk = "free"
            THEN MarkGone(r) /\ Hit("grw") ELSE UNCHANGED <<gone, hit>>
         /\ UNCHANGED <<lk, envN, rs, rec, dec, ack, pane, pas, cls, old, att>>

(* With CheckFirst the check and the read are one step (as DPoll). *)
GCall(r) ==
    /\ gpc = "idle" /\ rec[r]
    /\ cb[r] \in {"ok", "notconf", "already", "busy", "lost", "late"}
    /\ IF CheckFirst
       THEN GAnswer(r, GRes(r, JudgedAlive(r), RecK(r)))
       ELSE /\ gpc' = "g2" /\ gr' = r /\ gsk' = RecK(r)
            /\ UNCHANGED <<storeV, callV, falsefb, hit>>
    /\ UNCHANGED <<ccV, hookV, GpUnch>>

G2 ==
    /\ gpc = "g2"
    /\ GAnswer(gr, GRes(gr, JudgedAlive(gr), gsk))
    /\ UNCHANGED <<ccV, hookV, GpUnch>>

(* A read verb that writes hook_gone_at and waits for the write lock. *)
GW ==
    /\ gpc = "gw" /\ lk = "free"
    /\ MarkGone(gr) /\ GReply(gr, "fallen") /\ Hit("grw")
    /\ UNCHANGED <<ccV, hookV, lk, envN, rs, rec, dec, ack, pane, pas, cls, old, att, GpUnch>>

-----------------------------------------------------------------------------
(* The pane answerers. Each reads the pane when a request is fallen back, *)
(* then acts on what it saw: answers the dialog with send-keys            *)
(* --request-token, or, seeing the prompt, records record-pane-answer.    *)

(* Round 5 (Bypass): a plain send-keys held by a round-5 guard, or a     *)
(* request reported closed but not proven gone, is also a report: the    *)
(* caller has an intelligence look.                                       *)
OrcRead(i) ==
    /\ oc[i] = "idle" /\ Quiet /\ (Open # {} \/ (Bypass /\ LookAsked)) /\ proc = "alive"
    /\ oc' = [oc EXCEPT ![i] = "read"] /\ snap' = [snap EXCEPT ![i] = scr]
    /\ UNCHANGED <<ccV, hookV, storeV, decV, callV, gpV, sk, skt, skq, skd, st, ghostV>>

SendSet == <<pane, pas, gone, sk, skt, skq>>

(* A pane answer recorded on request a: the intent (PaneIntent) is        *)
(* committed under the check; the keys ks are typed by a later tmux call  *)
(* (SKType); then "sent" (SKDone). send-keys may die at either point.     *)
SendOn(i, a, ks, v) ==
    /\ IF PaneIntent
       THEN pane' = [pane EXCEPT ![a] = "intent"] /\ pas' = [pas EXCEPT ![a] = v]
       ELSE UNCHANGED <<pane, pas>>
    /\ MarkGone(a)
    /\ hit' = (hit \/ (Probe = "retried" /\ pane[a] = "intent"))
    /\ sk' = [sk EXCEPT ![i] = "intent"] /\ skt' = [skt EXCEPT ![i] = a]
    /\ skq' = [skq EXCEPT ![i] = ks]

SKType(i) ==
    /\ sk[i] = "intent" /\ proc = "alive"
    /\ inq' = inq \o skq[i] /\ sk' = [sk EXCEPT ![i] = "typing"]
    \* the check refused while any known hook lived; a hook of another request may start
    \* since (the keys still land on the dialog checked, Calm); the race that matters is
    \* with the hook of the request answered
    /\ typed' = [typed EXCEPT ![skt[i]] = TRUE]
    /\ race' = (race \/ (rec[skt[i]] /\ HookAlive(skt[i])))
    /\ skq' = [skq EXCEPT ![i] = <<>>]
    /\ UNCHANGED <<proc, aph, dl, got, scr, ptu, idl, hookV, storeV, decV, callV, gpV, oc, snap,
                   skt, skd, st, silent, wrong, misfire, late, busylie, falsefb, hit>>

(* Without Lag, keys already typed land before AD captures the pane again *)
(* (Quiet). With Lag they may not: a retry after a sender that died after *)
(* typing can then type a second answer (decision 5's second window).     *)
(* The requests a pane answerer may answer for what it saw (as in OrcAct). *)
TOf(i) ==
    LET s == snap[i] IN
    IF Distinct THEN (IF s > 0 /\ Reported(s) THEN {s} ELSE {})
    ELSE (IF s > 0 THEN Open ELSE {})

(* Round 5 (Bypass): the answerer looked because a plain send-keys is held, *)
(* and AD reports no request for what it saw: OrcBlindAct, not OrcAct.     *)
BlindCase(i) ==
    /\ Bypass /\ oc[i] = "read" /\ LookAsked /\ TOf(i) = {}
    /\ snap[i] > 0 \/ Open = {}

OrcAct(i) ==
    /\ oc[i] = "read" /\ sk[i] = "idle" /\ lk = "free" /\ Quiet /\ ~BlindCase(i)
    /\ oc' = [oc EXCEPT ![i] = "idle"] /\ snap' = [snap EXCEPT ![i] = 0]
    /\ LET s == snap[i]
           hashOK == HashCheck => Hash(scr) = Hash(s)
           T == IF Distinct THEN (IF s > 0 /\ Reported(s) THEN {s} ELSE {})
                ELSE (IF s > 0 THEN Open ELSE {})
       IN IF s = 0
          THEN IF Claim /\ Open # {}
               THEN \E r \in Open :
                      IF hashOK /\ (LiveSender => ~SenderAlive(r))
                      THEN IF ~ClaimAge \/ old[r]
                           THEN /\ pane' = [pane EXCEPT ![r] = "outside"]
                                /\ pas' = [pas EXCEPT ![r] = "unknown"]
                                /\ MarkGone(r) /\ Hit("claimed")
                                /\ UNCHANGED <<sk, skt, skq>>
                           ELSE /\ MarkGone(r) /\ Hit("toosoon")   \* ErrClaimTooSoon
                                /\ UNCHANGED <<pane, pas, sk, skt, skq>>
                      ELSE UNCHANGED <<SendSet, hit>>
               ELSE UNCHANGED <<SendSet, hit>>
          ELSE IF T = {} THEN UNCHANGED <<SendSet, hit>>
          ELSE \E t \in T, k \in {"yes", "esc"} :
                 LET v == IF k = "esc" THEN "deny" ELSE "allow"
                     ok == /\ ~AnyJudgedAlive /\ hashOK /\ Len(inq) <= 2
                 IN IF Pick = "token"
                    THEN IF /\ ok /\ Reported(t)
                            /\ pane[t] = "intent" => (HashCheck /\ (LiveSender => ~SenderAlive(t)))
                         THEN SendOn(i, t, Keys(t, k), v)
                         ELSE /\ UNCHANGED SendSet
                              /\ hit' = (hit \/ (Probe = "senderheld" /\ ok /\ Reported(t)
                                                 /\ pane[t] = "intent" /\ SenderAlive(t)))
                    ELSE IF ok /\ Open # {} THEN SendOn(i, Oldest, Keys(t, k), "none")
                         ELSE UNCHANGED <<SendSet, hit>>
    /\ UNCHANGED <<proc, aph, dl, got, scr, inq, ptu, idl, hookV, lk, envN, rs, rec, dec, ack,
                   cls, old, att, decV, callV, gpV, skd, st, typed, silent, wrong, misfire, race,
                   late, busylie, falsefb>>

(* send-keys records "sent" and the caller's --as as the decision. *)
SKDone(i) ==
    /\ sk[i] = "typing" /\ lk = "free"
    /\ sk' = [sk EXCEPT ![i] = "idle"] /\ skt' = [skt EXCEPT ![i] = 0]
    /\ LET a == skt[i] IN
       IF PaneIntent /\ pane[a] = "intent"
       THEN /\ pane' = [pane EXCEPT ![a] = "sent"]
            /\ dec' = IF pas[a] \in {"allow", "deny"} THEN [dec EXCEPT ![a] = pas[a]] ELSE dec
            /\ Hit("sent")
       ELSE UNCHANGED <<pane, dec, hit>>
    /\ UNCHANGED <<ccV, hookV, lk, envN, rs, rec, ack, pas, cls, gone, old, att, decV, callV,
                   gpV, oc, snap, skq, skd, st, typed, silent, wrong, misfire, race, late,
                   busylie, falsefb>>

(* send-keys dies after its intent (nothing typed) or after typing. *)
SKDie(i) ==
    /\ SKDeath /\ ~skd[i] /\ sk[i] \in {"intent", "typing"}
    /\ sk' = [sk EXCEPT ![i] = "idle"] /\ skd' = [skd EXCEPT ![i] = TRUE]
    /\ skt' = [skt EXCEPT ![i] = 0] /\ skq' = [skq EXCEPT ![i] = <<>>]
    /\ Hit("skdeath")
    /\ UNCHANGED <<ccV, hookV, storeV, decV, callV, gpV, oc, snap, st, typed, silent,
                   wrong, misfire, race, late, busylie, falsefb>>

(* The stray sender: a plain send-keys of a command and Enter. Refused    *)
(* while a relay hook may be alive (ErrSendKeysWhileRelayed), and with    *)
(* PlainGuard while a request is fallen back (ErrRelayFallenBack); it     *)
(* retries. Pick = "oldest" (round 2's 4(a)): recorded as the oldest      *)
(* fallen-back request's pane answer.                                     *)
(* Round 5: SameScreen first captures the pane (StrayRead); a send whose  *)
(* capture no longer matches is refused and captures again (StrayStale).  *)
(* R5OK: the other round-5 guards.                                        *)
StraySend ==
    /\ IF SameScreen THEN st = "read" /\ sOK ELSE st = "want"
    /\ proc = "alive" /\ Calm /\ lk = "free" /\ Len(inq) <= 2
    /\ ~AnyJudgedAlive
    /\ (Pick = "token" /\ PlainGuard) => Open = {}
    /\ R5OK /\ UNCHANGED <<unp, quiet, nz>>
    /\ st' = "done"
    /\ IF Pick = "oldest" /\ Open # {} /\ PaneIntent
       THEN /\ pane' = [pane EXCEPT ![Oldest] = "sent"]
            /\ typed' = [typed EXCEPT ![Oldest] = TRUE]
       ELSE UNCHANGED <<pane, typed>>
    /\ inq' = inq \o StrayKeys /\ race' = (race \/ KnownHookAlive)
    /\ UNCHANGED <<proc, aph, dl, got, scr, ptu, idl, hookV, lk, envN, rs, rec, dec, ack, pas,
                   cls, gone, old, att, decV, callV, gpV, oc, snap, sk, skt, skq, skd, silent,
                   wrong, misfire, late, busylie, falsefb, hit>>

-----------------------------------------------------------------------------
(* Round 5 *)

R5Unch == <<ccV, hookV, storeV, decV, callV, gpV, oc, snap, sk, skt, skq, skd, ghostV,
            unp, quiet, nz>>

(* The round-5 actions leave snapOK and sOK to Track (in Next), which    *)
(* needs every other primed variable first.                               *)
StrayRead ==
    /\ SameScreen /\ st = "want" /\ proc = "alive"
    /\ st' = "read"
    /\ UNCHANGED R5Unch

StrayStale ==
    /\ SameScreen /\ st = "read" /\ ~sOK
    /\ st' = "want"
    /\ UNCHANGED R5Unch

(* The pane changes for no reason AD knows: a status line, a footer. *)
NoiseStep ==
    /\ Noise /\ ~nz /\ proc = "alive"
    /\ nz' = TRUE
    /\ UNCHANGED <<ccV, hookV, storeV, decV, callV, gpV, paneV, ghostV, unp, quiet>>

(* Bypass: the intelligence looked (BlindCase). On a dialog AD reports no  *)
(* request for, it answers it: one key, no Enter, with the hash of what it *)
(* saw; AD records nothing. At the prompt, it sends the held keys (if any) *)
(* with the hash. Plain send-keys' own refusals still apply.               *)
OrcBlindAct(i) ==
    /\ BlindCase(i) /\ sk[i] = "idle" /\ Quiet /\ proc = "alive"
    /\ oc' = [oc EXCEPT ![i] = "idle"] /\ snap' = [snap EXCEPT ![i] = 0]
    /\ LET s == snap[i]
           ok == /\ Hash(scr) = Hash(s) /\ ~AnyJudgedAlive
                 /\ (Pick = "token" /\ PlainGuard) => Open = {}
       IN IF ~ok \/ (s = 0 /\ ~StrayHeld) THEN UNCHANGED <<inq, st, race, hit>>
          ELSE IF s > 0
          THEN /\ \E k \in {"yes", "esc"} : inq' = inq \o <<Key(k, "ans", s)>>
               /\ race' = (race \/ KnownHookAlive) /\ Hit("bypassdlg") /\ UNCHANGED st
          ELSE /\ inq' = inq \o StrayKeys /\ st' = "done"
               /\ race' = (race \/ KnownHookAlive) /\ Hit("bypassprompt")
    /\ UNCHANGED <<proc, aph, dl, got, scr, ptu, idl, hookV, storeV, decV, callV, gpV, sk, skt,
                   skq, skd, typed, silent, wrong, misfire, late, busylie, falsefb,
                   unp, quiet, nz>>

-----------------------------------------------------------------------------
(* find-missing (decision 3 A: fair but unscheduled). Each repair is one *)
(* guarded transaction, so its death is a run that did not happen.       *)

FMRecon ==
    /\ Sweep /\ Reconcile /\ rs = "check_permission" /\ lk = "free" /\ proc = "alive"
    /\ ~\E q \in R : Awaiting(q) \/ (rec[q] /\ JudgedAlive(q))
    /\ rs' = IF IdleMark /\ idl THEN "waiting" ELSE "working"
    /\ hit' = (hit \/ Probe = "recon" \/ (Probe = "reconmid" /\ aph = "turn"))
    /\ UNCHANGED <<ccV, hookV, lk, envN, rec, dec, ack, pane, pas, cls, gone, old, att, decV,
                   callV, gpV, paneV, typed, silent, wrong, misfire, race, late, busylie,
                   falsefb>>

FMPromote ==
    /\ Sweep /\ Promote /\ rs = "pending" /\ proc = "alive" /\ aph # "ss" /\ lk = "free"
    /\ rs' = "waiting"
    /\ Hit("promote")
    /\ UNCHANGED <<ccV, hookV, lk, envN, rec, dec, ack, pane, pas, cls, gone, old, att, decV,
                   callV, gpV, paneV, typed, silent, wrong, misfire, race, late, busylie,
                   falsefb>>

FMMissing ==
    /\ Sweep /\ proc = "gone" /\ rs \in Live /\ lk = "free"
    /\ rs' = "missing"
    /\ IF CloseOnMissing
       THEN /\ cls' = [q \in R |-> cls[q] \/ (rec[q] /\ ~Closed(q))]
            /\ dec' = [q \in R |-> IF rec[q] /\ ~Closed(q) /\ dec[q] = "none" THEN "deny" ELSE dec[q]]
       ELSE UNCHANGED <<cls, dec>>
    /\ Hit("missing")
    /\ UNCHANGED <<ccV, hookV, lk, envN, rec, ack, pane, pas, gone, old, att, decV, callV, gpV,
                   paneV, typed, silent, wrong, misfire, race, late, busylie, falsefb>>

-----------------------------------------------------------------------------
HookStep(r) == HX(r) \/ HT(r) \/ HI(r) \/ HP(r) \/ HA(r) \/ HD(r) \/ HE(r)
DStep == DStart \/ DChk \/ DRef \/ DW \/ DPollRec \/ DPoll \/ DRep
Life == SessionStart \/ TurnStart \/ TurnEnd \/ StopHook \/ IdleNotif

(* Round 4's actions that do not touch round 5's state: their steps keep  *)
(* it, and track the pane's bytes.                                        *)
Plain ==
    \/ EnvGrab \/ EnvRelease
    \/ TurnEnd \/ LifeDie \/ AgentCrash
    \/ \E r \in R : Ask(r) \/ HP(r) \/ HA(r) \/ HD(r) \/ HE(r) \/ HFirstFail(r) \/ HDie(r)
                    \/ Tick(r) \/ Draw(r) \/ Age(r) \/ GCall(r)
    \/ \E r \in R, v \in DVerdicts : DCall(r, v)
    \/ DStep \/ DRefCut \/ DWCut \/ DTick \/ DDie
    \/ G2 \/ GW
    \/ Consume \/ HumanKey
    \/ \E i \in I : OrcRead(i) \/ OrcAct(i) \/ SKType(i) \/ SKDone(i) \/ SKDie(i)
    \/ FMRecon \/ FMPromote \/ FMMissing

(* The pane's bytes after a step: AD's idle capture (IdleSnap) is taken  *)
(* at an idle hook (cap) and spoilt by any change; the stray's capture    *)
(* (SameScreen) is taken by StrayRead.                                    *)
Track(cap, sread) ==
    /\ snapOK' = IF IdleSnap THEN cap \/ (snapOK /\ ~Changed) ELSE snapOK
    /\ sOK' = IF SameScreen THEN sread \/ (sOK /\ ~Changed) ELSE sOK

(* Whole steps (TLC needs every primed variable, also for fairness):     *)
(* P, a round-4 action that keeps round 5's state; E, a round-5-aware     *)
(* action; C, an idle hook (AD's capture).                                *)
P(A) == A /\ UNCHANGED <<unp, quiet, nz>> /\ Track(FALSE, FALSE)
E(A) == A /\ Track(FALSE, FALSE)
C(A) == A /\ Track(TRUE, FALSE)

Next ==
    \/ P(Plain)
    \/ C(SessionStart \/ StopHook \/ IdleNotif)
    \/ E(\/ TurnStart
         \/ \E r \in R : PostToolUse(r) \/ HX(r) \/ HT(r) \/ HI(r)
         \/ StraySend \/ StrayStale \/ NoiseStep
         \/ \E i \in I : OrcBlindAct(i))
    \/ StrayRead /\ Track(FALSE, TRUE)

(* Fair: Claude Code's own steps (the agent goes on; a live hook takes its *)
(* next step, or dies: death is never forced, never excluded); time; the  *)
(* other process releases the lock; decide finishes what it starts and   *)
(* its bound cuts; the poller follows up; the pane answerers act on what  *)
(* AD reports; send-keys finishes; the stray retries; find-missing runs   *)
(* now and then. Not fair: deaths, failures, the other process taking the *)
(* lock, clicks (decide calls), the human, the crash.                      *)
(* Round 4's fairness, over whole steps (P, E, C). *)
Fairness ==
    /\ WF_vars(P(EnvRelease))
    /\ WF_vars(C(SessionStart)) /\ WF_vars(E(TurnStart)) /\ WF_vars(P(TurnEnd))
    /\ WF_vars(C(StopHook)) /\ WF_vars(C(IdleNotif))
    /\ \A r \in R : /\ WF_vars(P(Ask(r))) /\ WF_vars(E(PostToolUse(r)))
                    /\ WF_vars(E(HX(r) \/ HT(r) \/ HI(r)) \/ P(HP(r) \/ HA(r) \/ HD(r) \/ HE(r)))
                    /\ WF_vars(P(Tick(r))) /\ WF_vars(P(Draw(r))) /\ WF_vars(P(Age(r)))
                    /\ WF_vars(P(GCall(r)))
                    /\ WF_vars(P(HFirstFail(r) /\ InsertCut /\ ph[r] >= 1))
    /\ WF_vars(P(DStep)) /\ WF_vars(P(DTick)) /\ WF_vars(P(DRefCut /\ CutDue))
    /\ WF_vars(P(DWCut /\ CutDue))
    /\ WF_vars(P(G2)) /\ WF_vars(P(GW))
    /\ WF_vars(P(Consume))
    /\ \A i \in I : WF_vars(P(OrcRead(i))) /\ WF_vars(P(OrcAct(i))) /\ WF_vars(P(SKType(i)))
                    /\ WF_vars(P(SKDone(i)))
    /\ WF_vars(E(StraySend))
    /\ WF_vars(P(FMRecon)) /\ WF_vars(P(FMPromote)) /\ WF_vars(P(FMMissing))
    \* round 5: the stray sender captures and retries; the intelligence acts on a held send
    /\ WF_vars(StrayRead /\ Track(FALSE, TRUE)) /\ WF_vars(E(StrayStale))
    /\ \A i \in I : WF_vars(E(OrcBlindAct(i)))

Spec == Init /\ [][Next]_vars /\ Fairness

-----------------------------------------------------------------------------
(* Safety: no caller is told something false; no silent allow; nothing   *)
(* half-recorded; deadlines.                                              *)

TypeOK ==
    /\ proc \in {"alive", "gone"}
    /\ rs \in Live \cup {"missing"}
    /\ lk \in {"free", "env"}
    /\ de \in 0 .. DMax
    /\ dpc \in {"idle", "start", "chk", "ref", "w", "aw", "awr", "fin"}
    /\ gpc \in {"idle", "g1", "g2", "gw"}
    /\ \A r \in R : pane[r] \in {"none", "intent", "sent", "outside", "ran"}

(* No dialog is allowed by a key that nobody sent as its answer. *)
NoSilentAllow == ~silent

(* An answer closes the dialog it was sent for, and never lands in the chat. *)
AnswersLand == ~wrong /\ ~misfire

(* AD never types a pane answer while the hook of the request it answers  *)
(* may still answer it, and a plain send-keys never types while a relay   *)
(* hook it has a record of is alive.                                      *)
NoRace == ~race

(* "Fallen back" means the hook is gone, never "slow". *)
FallenTrue == \A r \in R : Reported(r) => ~HookAlive(r)

(* "Fallen back, no pane answer recorded through AD" is true. *)
ClaimV3 == \A r \in R : (Reported(r) /\ pane[r] = "none") => ~typed[r]

(* What AD records closing a request is what closed its dialog (keys sent *)
(* to an agent that then crashed reached nobody; find-missing reports the *)
(* agent missing).                                                         *)
RecordTrue ==
    \A r \in R :
        /\ (pane[r] = "sent" /\ inq = <<>> /\ proc = "alive") =>
               dl[r] = "ans" /\ (pas[r] \in {"allow", "deny"} => got[r] = pas[r])
        /\ pane[r] \in {"outside", "ran"} => dl[r] = "ans"
        /\ pane[r] = "ran" => got[r] = "allow"

(* An ack on a finished request is what the agent got. *)
AckTrue == \A r \in R : (ack[r] /\ h[r] \in {"done", "dead"}) => got[r] = dec[r]

(* What decide (or get-permission) told the caller was delivered was. *)
DecideTruthful ==
    \A r \in R : (~HookAlive(r) /\ cb[r] \in {"ok", "delivered"}) => got[r] = cv[r]

(* A delivered verdict is never contradicted by the record afterwards. *)
NoContradiction == \A r \in R : cb[r] = "delivered" => dec[r] = cv[r]

(* A store-busy refusal leaves nothing recorded by that call. *)
BusyClean == ~busylie

(* fallen_back is reported only when the store has no ack and the hook is gone. *)
FieldsTrue == ~falsefb

(* decide replies before its caller's deadline. *)
DecideInTime == ~late

(* A read verb never waits for the write lock. *)
ReadNeverWaits == ~(gpc = "gw" /\ lk = "env")

(* Recovery *)

(* Every dialog closes. *)
DialogsClose == <>[](\A r \in R : dl[r] = "ans" \/ (proc = "gone" /\ dl[r] = "none"))

(* Every request record closes. *)
RecordsClose == <>[](\A r \in R : rec[r] => Closed(r))

(* No row stays pending, check_permission or working: an idle agent's row *)
(* reads waiting; a gone agent's reads missing.                           *)
RowSettles ==
    /\ <>[]((proc = "alive" /\ aph = "rest") => rs = "waiting")
    /\ <>[](proc = "gone" => rs = "missing")

(* The stray sender gets through: no agent is locked out of plain send-keys. *)
StraySettles == <>(st = "done" \/ proc = "gone")

(* Every caller ends with a final answer. *)
CallerSettles == <>[](\A r \in R : cb[r] \in {"none", "delivered", "fallen", "closed"})

(* A state constraint for two-request safety runs: at most one decide call *)
(* in the run (fin1 covers decide's own paths, retries and deaths).        *)
CallsMade == LET S[k \in 0 .. N] == IF k = 0 THEN 0 ELSE S[k - 1] + dtries[k] IN S[N]
OneCall == CallsMade <= 1

(* Probes: each must be violated in a passing run. *)
NeverHit == ~hit
NeverTwoOpen == Cardinality(Open) < 2
NeverOpenOnWaitingRow == ~(rs = "waiting" /\ Open # {})
NeverStrayHeldYoung == ~(st = "want" /\ \E r \in Open : ~old[r])
NeverLate == ~late

(* Round 5 probes. *)
(* A plain send-keys held by a round-5 guard. *)
NeverHeld == ~(StrayHeld /\ proc = "alive")
(* ... while no dialog is up and AD delivered every verdict: a false alarm, *)
(* the guard's cost.                                                        *)
NeverHeldFalse ==
    ~(StrayHeld /\ proc = "alive" /\ scr = 0 /\ (\A r \in R : dl[r] # "pend")
      /\ \E r \in R : cb[r] = "delivered")
(* ... in a turn that has had no permission request. Unproven must keep   *)
(* this (a property); IdleGate breaks it.                                 *)
NeverHeldNoRequest == ~(StrayHeld /\ proc = "alive" /\ \A r \in R : ~rec[r])
(* ... during the turn (a probe for IdleGate's cost). *)
NeverHeldInTurnNoRequest ==
    ~(StrayHeld /\ proc = "alive" /\ aph = "turn" /\ \A r \in R : ~rec[r])
=============================================================================
