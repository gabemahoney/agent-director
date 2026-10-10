---------------------------- MODULE B3ieSettle ----------------------------
(***************************************************************************)
(* b.3ie: settle time for status/get/list ("no state is set until it has   *)
(* settled"). One spawn row with three columns:                            *)
(*   st     the raw state, as today (`state`)                              *)
(*   stamp  when the raw value last changed, in ms (`state_changed_ms`)    *)
(*   base   the last settled value as of the last raw change               *)
(*          (`settled_state`)                                              *)
(*                                                                         *)
(* Write rule (each write is ONE SQL UPDATE: its SET sees the pre-update   *)
(* row and `now` is taken inside the statement, under the write lock; Tw   *)
(* is the writer process's settle_ms, read from config when it started):   *)
(*   hook write of a non-terminal V                                        *)
(*     V = st : nothing of the three columns changes (no clock restart)    *)
(*     V # st : base := IF Tw = 0 \/ now - stamp >= Tw THEN st ELSE base;  *)
(*              st := V; stamp := now                                      *)
(*   immediate write (any terminal value, hook or not; every non-hook      *)
(*   write: find-missing mark/repair, EndHeldLaunch, MoveToPending,        *)
(*   ResetForReuse, resume/reuse restore, InsertPending):                  *)
(*     st := V; base := V; stamp := now when V # st                        *)
(* Read rule (status/get/list and list --state; pure, one step, no wait;   *)
(* Tr is the reader's settle_ms):                                          *)
(*   reported = IF Tr = 0 \/ now - stamp >= Tr THEN st ELSE base           *)
(*   (a negative age counts as not settled)                                *)
(* Guards (send-keys, decide, find-missing, resume, pause, kill, expire,   *)
(* spawn held/reuse) read st only.                                         *)
(*                                                                         *)
(* A ghost history `hist` of raw changes (value, time, kind, writer's T)   *)
(* defines the reference: FoldC/FoldV/BeeOK below. Event records (`ev`)    *)
(* hold what the last step observed and are cleared by every other step,  *)
(* so an invariant over ev is checked in the state right after the event.  *)
(*                                                                         *)
(* Mutations (Mut) seed one wrong implementation each; see README.md.      *)
(***************************************************************************)
EXTENDS Integers, Sequences, FiniteSets, TLC

CONSTANTS
  NHooks,           \* concurrent hook writer processes
  MaxT,             \* settle_ms domain is 0..MaxT
  InitT,            \* settle_ms in config at the start
  MaxCfgChanges,    \* ConfigChange steps allowed (0: T never changes)
  MaxTime,          \* clock bound, ms
  MaxWrites,        \* bound on write steps (hook starts plus command writes)
  InitStates,       \* raw states a migrated row may start in
  FreshInsert,      \* TRUE: a row InsertPending wrote at time 0 is also an initial state
  HookVals,         \* values hooks write
  HookFromTerminal, \* TRUE: a hook may move a terminal row (source: the SR-22.9 gate only)
  Commands,         \* TRUE: command (non-hook) writes run
  Guards,           \* TRUE: guard reads run
  ReadClock,        \* "inline" (clock and row in one step), "before" (clock, then row), "after" (row, then clock)
  Mut               \* "none" or a seeded mutation

States   == {"pending", "waiting", "working", "check_permission", "ask_user", "ended", "missing"}
Terminal == {"ended", "missing"}
Live     == States \ Terminal
None     == "none"
\* state_changed_ms = 0 after the migration: older than any settle time.
MigStamp == -(MaxT + 1)

ASSUME /\ NHooks \in Nat \ {0} /\ MaxT \in Nat /\ InitT \in 0..MaxT
       /\ MaxCfgChanges \in Nat /\ MaxTime \in Nat /\ MaxWrites \in Nat
       /\ InitStates \subseteq States /\ HookVals \subseteq States \ {"missing", "pending"}
       /\ FreshInsert \in BOOLEAN /\ HookFromTerminal \in BOOLEAN
       /\ Commands \in BOOLEAN /\ Guards \in BOOLEAN
       /\ ReadClock \in {"inline", "before", "after"}
       /\ Mut \in {"none", "strictgt", "prevraw", "samerestart", "stalebase", "nowoutside",
                   "termsettles", "cmdsettles", "guardreported", "today", "insertnobase",
                   "negagesettles", "immnostamp"}
       /\ InitStates # {} \/ FreshInsert

VARIABLES
  now,      \* the host's ms clock
  cfgT,     \* settle_ms in config.toml now
  nCfg,     \* ConfigChange steps taken
  nW,       \* write steps taken
  st, base, stamp,  \* the row's three columns
  hist,     \* ghost: every raw value change, [v, t, k ("imm"|"hook"), tw]
  lastBy,   \* ghost: the hook process that made the last raw change (0: not a hook)
  hp,       \* hook processes
  rp,       \* the reader process (status/get/list)
  ev        \* the last step's observation (cleared by every other step)

vars == <<now, cfgT, nCfg, nW, st, base, stamp, hist, lastBy, hp, rp, ev>>

NoSnap      == [st |-> None, base |-> None, stamp |-> 0]
IdleHook    == [pc |-> "idle", T |-> 0, v |-> None, t0 |-> 0, snap |-> NoSnap]
IdleReader  == [pc |-> "idle", T |-> 0, clk |-> 0, k0 |-> 0, t0 |-> 0,
                sst |-> None, sbase |-> None, sstamp |-> 0, simm |-> FALSE]
NoEv        == [kind |-> "none", val |-> None, raw |-> None, T |-> 0, imm |-> FALSE,
                lin |-> TRUE, linvar |-> TRUE, bee |-> TRUE, sup |-> TRUE,
                rep |-> None, dup |-> FALSE]

----------------------------------------------------------------------------
(* The settle test and the read rule, with the seeded mutations.           *)

IsSettled(age, T) ==
  CASE Mut = "strictgt"      -> age > T                       \* `>` for `>=`, no T = 0 short-cut
    [] Mut = "negagesettles" -> T = 0 \/ age >= T \/ age < 0   \* unsigned/abs age
    [] OTHER                 -> T = 0 \/ age >= T

Reported(s, b, sp, T, clk) ==
  IF Mut = "today" THEN s                                     \* control: no settling
  ELSE IF IsSettled(clk - sp, T) THEN s ELSE b

RowRec == [st |-> st, base |-> base, stamp |-> stamp]

\* The base a changing hook write stores, from the row `src` it sees.
NewBase(src, Tw, wnow) ==
  IF Mut = "prevraw" THEN src.st                              \* a "previous raw" column
  ELSE IF IsSettled(wnow - src.stamp, Tw) THEN src.st ELSE src.base

\* An immediate write of V.
ImmCols(V) ==
  [st |-> V, base |-> V,
   stamp |-> IF V # st /\ Mut # "immnostamp" THEN now ELSE stamp]

\* A hook write of p.v, as the store applies it.
HookCols(p) ==
  LET V    == p.v
      src  == IF Mut = "stalebase" THEN p.snap ELSE RowRec     \* stale: Go-side read before the UPDATE
      wnow == IF Mut = "nowoutside" THEN p.t0 ELSE now         \* clock taken before the write lock
  IN  IF ~HookFromTerminal /\ st \in Terminal THEN RowRec      \* the guard the design assumed
      ELSE IF V \in Terminal /\ Mut # "termsettles" THEN ImmCols(V)
      ELSE IF src.st = V /\ Mut # "samerestart" THEN RowRec    \* same value: no clock restart
      ELSE [st |-> V, base |-> NewBase(src, p.T, wnow), stamp |-> wnow]

\* A command write of V (every command move changes the value).
CmdCols(V) ==
  IF Mut = "cmdsettles"
  THEN [st |-> V, base |-> NewBase(RowRec, cfgT, now), stamp |-> now]
  ELSE ImmCols(V)

----------------------------------------------------------------------------
(* The reference, over the ghost history.                                  *)

\* Fold over the raw segments: an immediate segment is settled at once; a
\* hook segment is settled when it held >= its threshold, up to the next
\* change (or tau for the last). The threshold of a segment that a hook
\* write ended is that write's T when useTw (FoldV), else T (FoldC); the
\* last segment's is always T (the reader's).
RECURSIVE FoldS(_, _, _, _, _, _)
FoldS(h, T, tau, useTw, s, i) ==
  IF i > Len(h) THEN s
  ELSE LET n   == Len(h)
           thr == IF i < n /\ useTw /\ h[i + 1].k = "hook" THEN h[i + 1].tw ELSE T
           end == IF i < n THEN h[i + 1].t ELSE tau
           s2  == IF h[i].k = "imm" THEN h[i].v
                  ELSE IF thr = 0 \/ end - h[i].t >= thr THEN h[i].v ELSE s
       IN  FoldS(h, T, tau, useTw, s2, i + 1)

FoldC(h, T, tau) == FoldS(h, T, tau, FALSE, None, 1)   \* SpecSettled, one T throughout
FoldV(h, T, tau) == FoldS(h, T, tau, TRUE, None, 1)    \* each segment judged with the T in force when it ended

\* The bee's form: x was held raw >= T (or written at once), and no later
\* raw value has held T since.
BeeOK(h, T, tau, x) ==
  LET n == Len(h)
      End(i) == IF i < n THEN h[i + 1].t ELSE tau
      Settles(i) == h[i].k = "imm" \/ T = 0 \/ End(i) - h[i].t >= T
  IN  \E i \in 1..n : h[i].v = x /\ Settles(i) /\ \A j \in (i + 1)..n : ~Settles(j)

HistVals == {hist[i].v : i \in DOMAIN hist}
LastImm  == hist[Len(hist)].k = "imm"

\* Linearization points of a read whose window opened with k0 changes
\* recorded at time t0 and closes now: (k changes recorded, instant tau).
LinPoints(k0, t0) ==
  {p \in (k0..Len(hist)) \X (t0..now) :
     /\ hist[p[1]].t <= p[2]
     /\ p[1] < Len(hist) => p[2] <= hist[p[1] + 1].t}

ReadEv(s, b, sp, imm, T, clk, k0, t0) ==
  LET val == Reported(s, b, sp, T, clk)
      pts == LinPoints(k0, t0)
  IN  [kind |-> "read", val |-> val, raw |-> s, T |-> T, imm |-> imm,
       lin    |-> \E p \in pts : FoldC(SubSeq(hist, 1, p[1]), T, p[2]) = val,
       linvar |-> \E p \in pts : FoldV(SubSeq(hist, 1, p[1]), T, p[2]) = val,
       bee    |-> \E p \in pts : BeeOK(SubSeq(hist, 1, p[1]), T, p[2], val),
       sup    |-> val \in HistVals,
       rep |-> None, dup |-> FALSE]

----------------------------------------------------------------------------
Init ==
  /\ now = 0 /\ cfgT = InitT /\ nCfg = 0 /\ nW = 0
  /\ \/ \E s0 \in InitStates :                                  \* a migrated row
          /\ st = s0 /\ base = s0 /\ stamp = MigStamp
          /\ hist = <<[v |-> s0, t |-> MigStamp, k |-> "imm", tw |-> 0]>>
     \/ /\ FreshInsert                                          \* InsertPending at 0
        /\ st = "pending" /\ stamp = 0
        /\ base = IF Mut = "insertnobase" THEN None ELSE "pending"
        /\ hist = <<[v |-> "pending", t |-> 0, k |-> "imm", tw |-> 0]>>
  /\ lastBy = 0
  /\ hp = [h \in 1..NHooks |-> IdleHook]
  /\ rp = IdleReader
  /\ ev = NoEv

Tick ==
  /\ now < MaxTime
  /\ now' = now + 1
  /\ ev' = NoEv
  /\ UNCHANGED <<cfgT, nCfg, nW, st, base, stamp, hist, lastBy, hp, rp>>

ConfigChange ==
  /\ nCfg < MaxCfgChanges
  /\ cfgT' \in (0..MaxT) \ {cfgT}
  /\ nCfg' = nCfg + 1
  /\ ev' = NoEv
  /\ UNCHANGED <<now, nW, st, base, stamp, hist, lastBy, hp, rp>>

\* A hook process starts: it reads config (its T) and its payload (V).
HookStart(h) ==
  /\ hp[h].pc = "idle"
  /\ nW < MaxWrites
  /\ \E v \in HookVals :
       hp' = [hp EXCEPT ![h] = [pc |-> "ready", T |-> cfgT, v |-> v,
                                t0 |-> IF Mut = "nowoutside" THEN now ELSE 0,
                                snap |-> IF Mut = "stalebase" THEN RowRec ELSE NoSnap]]
  /\ nW' = nW + 1
  /\ ev' = NoEv
  /\ UNCHANGED <<now, cfgT, nCfg, st, base, stamp, hist, lastBy, rp>>

\* Its one UPDATE, atomic under the write lock.
HookWrite(h) ==
  LET p       == hp[h]
      cols    == HookCols(p)
      changed == cols.st # st
      kind    == IF cols.st \in Terminal THEN "imm" ELSE "hook"
      dup     == /\ p.v = st /\ hist[Len(hist)].k = "hook" /\ hist[Len(hist)].v = p.v
                 /\ lastBy \notin {0, h} /\ now - hist[Len(hist)].t <= 1
  IN  /\ p.pc = "ready"
      /\ st' = cols.st /\ base' = cols.base /\ stamp' = cols.stamp
      /\ hist' = IF changed
                 THEN Append(hist, [v |-> cols.st, t |-> now, k |-> kind,
                                    tw |-> IF kind = "hook" THEN p.T ELSE 0])
                 ELSE hist
      /\ lastBy' = IF changed THEN h ELSE lastBy
      /\ hp' = [hp EXCEPT ![h] = IdleHook]
      /\ ev' = [NoEv EXCEPT !.kind = "hook", !.dup = dup]
      /\ UNCHANGED <<now, cfgT, nCfg, nW, rp>>

\* Command (non-hook) moves, each enabled by the raw state its WHERE guards:
\* find-missing's mark (live -> missing) and check_permission repair;
\* MoveToPending / ResetForReuse (finished -> pending); resume/reuse restore
\* and EndHeldLaunch (pending -> ended/missing).
CmdMoves ==
  {<<s, "missing">> : s \in Live}
  \cup {<<"check_permission", v>> : v \in {"waiting", "working"}}
  \cup {<<s, "pending">> : s \in Terminal}
  \cup {<<"pending", v>> : v \in Terminal}

Cmd ==
  /\ Commands
  /\ nW < MaxWrites
  /\ \E m \in CmdMoves :
       /\ m[1] = st
       /\ LET cols == CmdCols(m[2]) IN
            /\ st' = cols.st /\ base' = cols.base /\ stamp' = cols.stamp
            /\ hist' = Append(hist, [v |-> m[2], t |-> now, k |-> "imm", tw |-> 0])
  /\ lastBy' = 0
  /\ nW' = nW + 1
  /\ ev' = NoEv
  /\ UNCHANGED <<now, cfgT, nCfg, hp, rp>>

\* A guard's read (send-keys, decide incl. err_details.state, find-missing,
\* resume, pause, kill, expire, spawn held/reuse): what it acts on.
Guard ==
  /\ Guards
  /\ LET rep == Reported(st, base, stamp, cfgT, now) IN
       ev' = [NoEv EXCEPT !.kind = "guard",
                          !.val = IF Mut = "guardreported" THEN rep ELSE st,
                          !.raw = st, !.rep = rep, !.T = cfgT]
  /\ UNCHANGED <<now, cfgT, nCfg, nW, st, base, stamp, hist, lastBy, hp, rp>>

\* The reader process: config first (its T), then the read.
RCfg ==
  /\ rp.pc = "idle"
  /\ rp' = [IdleReader EXCEPT !.pc = "cfg", !.T = cfgT]
  /\ ev' = NoEv
  /\ UNCHANGED <<now, cfgT, nCfg, nW, st, base, stamp, hist, lastBy, hp>>

RReadInline ==
  /\ ReadClock = "inline" /\ rp.pc = "cfg"
  /\ ev' = ReadEv(st, base, stamp, LastImm, rp.T, now, Len(hist), now)
  /\ rp' = IdleReader
  /\ UNCHANGED <<now, cfgT, nCfg, nW, st, base, stamp, hist, lastBy, hp>>

RClkBefore ==
  /\ ReadClock = "before" /\ rp.pc = "cfg"
  /\ rp' = [rp EXCEPT !.pc = "clk", !.clk = now, !.k0 = Len(hist), !.t0 = now]
  /\ ev' = NoEv
  /\ UNCHANGED <<now, cfgT, nCfg, nW, st, base, stamp, hist, lastBy, hp>>

RRowBefore ==
  /\ rp.pc = "clk"
  /\ ev' = ReadEv(st, base, stamp, LastImm, rp.T, rp.clk, rp.k0, rp.t0)
  /\ rp' = IdleReader
  /\ UNCHANGED <<now, cfgT, nCfg, nW, st, base, stamp, hist, lastBy, hp>>

RRowAfter ==
  /\ ReadClock = "after" /\ rp.pc = "cfg"
  /\ rp' = [rp EXCEPT !.pc = "row", !.sst = st, !.sbase = base, !.sstamp = stamp,
                      !.simm = LastImm, !.k0 = Len(hist), !.t0 = now]
  /\ ev' = NoEv
  /\ UNCHANGED <<now, cfgT, nCfg, nW, st, base, stamp, hist, lastBy, hp>>

RClkAfter ==
  /\ rp.pc = "row"
  /\ ev' = ReadEv(rp.sst, rp.sbase, rp.sstamp, rp.simm, rp.T, now, rp.k0, rp.t0)
  /\ rp' = IdleReader
  /\ UNCHANGED <<now, cfgT, nCfg, nW, st, base, stamp, hist, lastBy, hp>>

Next ==
  \/ Tick \/ ConfigChange
  \/ \E h \in 1..NHooks : HookStart(h) \/ HookWrite(h)
  \/ Cmd \/ Guard
  \/ RCfg \/ RReadInline \/ RClkBefore \/ RRowBefore \/ RRowAfter \/ RClkAfter

Spec == Init /\ [][Next]_vars

----------------------------------------------------------------------------
(* Invariants. "state" forms quantify over the row and the clock; "read"  *)
(* forms check what a reader process returned.                             *)

HistRec == [v : States, t : MigStamp..MaxTime, k : {"imm", "hook"}, tw : 0..MaxT]

TypeOK ==
  /\ now \in 0..MaxTime /\ cfgT \in 0..MaxT /\ nCfg \in 0..MaxCfgChanges /\ nW \in 0..MaxWrites
  /\ st \in States /\ base \in States \cup {None} /\ stamp \in MigStamp..MaxTime
  /\ Len(hist) >= 1 /\ \A i \in DOMAIN hist : hist[i] \in HistRec
  /\ hist[Len(hist)].v = st                                   \* the ghost tracks the raw column
  /\ lastBy \in 0..NHooks
  /\ \A h \in 1..NHooks : hp[h].pc \in {"idle", "ready"}
  /\ rp.pc \in {"idle", "cfg", "clk", "row"}
  /\ ev.kind \in {"none", "read", "guard", "hook"}

\* 1. T = 0 => reported = raw.
ZeroIsRaw ==
  /\ Reported(st, base, stamp, 0, now) = st
  /\ ev.kind = "read" /\ ev.T = 0 => ev.val = ev.raw

\* 2. T unchanged: reported = SpecSettled(hist, T, now).
SettledIsSpec ==
  nCfg = 0 => Reported(st, base, stamp, cfgT, now) = FoldC(hist, cfgT, now)

\* 2, the bee's form: a reported X was held raw >= T (or written at once) and
\* is the last value that did.
HeldOrLastSettled ==
  nCfg = 0 => BeeOK(hist, cfgT, now, Reported(st, base, stamp, cfgT, now))

\* 2, as a reader sees it: the answer is the settled value at some instant
\* of the read's window, in both forms.
ReadsLinearizable ==
  ev.kind = "read" /\ nCfg = 0 => ev.lin /\ ev.bee

\* 3. Terminal states and command-written values are reported at once.
ImmediateAtOnce ==
  /\ (LastImm \/ st \in Terminal) => \A T \in 0..MaxT : Reported(st, base, stamp, T, now) = st
  /\ ev.kind = "read" /\ ev.imm => ev.val = ev.raw

\* 4. No guard reads the reported state.
GuardsReadRaw == ev.kind = "guard" => ev.val = ev.raw

\* 5. With T changing: reported is a value the raw history held.
NeverUnsupported ==
  /\ \A T \in 0..MaxT : Reported(st, base, stamp, T, now) \in HistVals
  /\ ev.kind = "read" => ev.sup

\* 5, strong: the answer is well defined by the history and the T's in force:
\* each earlier segment judged with the T of the write that ended it, the
\* current one with the reader's T.
WellDefined ==
  /\ \A T \in 0..MaxT : Reported(st, base, stamp, T, now) = FoldV(hist, T, now)
  /\ ev.kind = "read" => ev.linvar

\* Limit (expected to fail when T changes): the bee's form judged with the
\* reader's own T for every segment.
BeeFormReaderT == ev.kind = "read" => ev.bee

----------------------------------------------------------------------------
(* Probes: each is written to be violated; a violation proves the case is *)
(* reached.                                                                *)

N == Len(hist)
H(i) == hist[i]

\* A -> B -> A within T, and a read inside T of the B change returns A.
ProbeFlap ==
  ~(/\ ev.kind = "read" /\ ev.T > 0 /\ N >= 3
    /\ H(N).v = H(N - 2).v /\ H(N - 1).k = "hook" /\ H(N).k = "hook"
    /\ now - H(N - 1).t < ev.T /\ ev.val = H(N).v)

\* The short turn: waiting -> working -> waiting within T reads waiting.
ProbeShortTurn ==
  ~(/\ ev.kind = "read" /\ ev.T > 0 /\ N >= 3
    /\ H(N).v = "waiting" /\ H(N - 1).v = "working" /\ H(N - 2).v = "waiting"
    /\ H(N - 1).k = "hook" /\ H(N).k = "hook"
    /\ now - H(N - 1).t < ev.T /\ ev.val = "waiting")

\* A read during an unsettled hook change returns the old settled value.
ProbeHoldsOld ==
  ~(/\ ev.kind = "read" /\ N >= 2 /\ H(N).k = "hook"
    /\ ev.raw = H(N).v /\ ev.val = H(N - 1).v /\ ev.val # ev.raw)

Chain ==
  /\ N >= 3
  /\ H(N - 2).v # H(N - 1).v /\ H(N - 1).v # H(N).v /\ H(N - 2).v # H(N).v
  /\ H(N - 1).k = "hook" /\ H(N).k = "hook"
  /\ H(N).t - H(N - 1).t < ev.T                               \* B never settled

\* A -> B -> C within T reads A ...
ProbeChainHoldsA ==
  ~(ev.kind = "read" /\ Chain /\ ev.raw = H(N).v /\ ev.val = H(N - 2).v)

\* ... then C once C has held T from the B -> C change.
ProbeChainReachesC ==
  ~(ev.kind = "read" /\ ev.T > 0 /\ Chain /\ now - H(N).t >= ev.T /\ ev.val = H(N).v)

\* A duplicate hook write (same value, another hook process, <= 1 ms after
\* its change) lands.
ProbeDup == ~(ev.kind = "hook" /\ ev.dup)

\* Two hook processes in flight with the same value at once.
ProbeTwoHooks ==
  ~(\E i, j \in 1..NHooks : i # j /\ hp[i].pc = "ready" /\ hp[j].pc = "ready" /\ hp[i].v = hp[j].v)

\* A read whose T differs from the T the current segment's writer used.
ProbeTChangeRead ==
  ~(ev.kind = "read" /\ H(N).k = "hook" /\ H(N).tw # ev.T)

\* A guard acts on raw while the reported state differs.
ProbeGuardDiffers == ~(ev.kind = "guard" /\ ev.raw # ev.rep)

\* A hook moves a terminal row (source allows it: SR-22.9 gate only).
ProbeHookLeavesTerminal == ~(N >= 2 /\ H(N - 1).v \in Terminal /\ H(N).k = "hook")

\* A command write lands while a hook change is still unsettled.
ProbeCmdCutsSettle ==
  ~(N >= 2 /\ H(N).k = "imm" /\ H(N - 1).k = "hook" /\ H(N).t - H(N - 1).t < cfgT)

=============================================================================
