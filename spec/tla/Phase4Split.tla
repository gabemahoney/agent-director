---------------------------- MODULE Phase4Split ----------------------------
(***************************************************************************)
(* b.zuj v4, actor-split runs of Phase4 (the LABEL design). As in v2/v3:   *)
(* the bot (id b) and the orchestrator (id o) own different rows; every    *)
(* lookup of a row is by that row's own label (or its name, for the        *)
(* holder check); every kill or key acts only on the pane / session its    *)
(* lookup attributed to that row. So the absent actor can affect the       *)
(* present one only through: (1) its find-missing sweeps over all rows     *)
(* -> ForeignSweep; (2) its sessions holding a name the present actor      *)
(* looks up or creates -> ForeignSess (labelled validly with the absent    *)
(* id: someone else's); (3) the shared budgets. The v4 accidents (rename,  *)
(* group, respawn, restart) are environment steps present in both halves.  *)
(***************************************************************************)
EXTENDS Phase4

ForeignSweep ==
  /\ row' = SweepRows /\ row' # row
  /\ UNCHANGED <<TW, hist, nextSid, nextLife, faults, envVars, orchVars, botVars, apprVars, humVars,
                 fmVars, ghostVars>>
AbsentId  == IF ~EnableOrch THEN OrchId ELSE BotId
AbsentNms == IF ~EnableOrch THEN OrchNames ELSE {BotName}
ForeignSess ==
  /\ ~(EnableOrch /\ EnableBot) /\ CanCreate
  /\ \E nm \in AbsentNms, blk \in BOOLEAN :
       /\ Named(nm) = {}
       /\ sess' = sess \cup {[tid |-> NextTid, sc |-> nextSid + 1, name |-> nm,
                              lab |-> Lab(AbsentId, <<8, 8>>, NextTid), pn |-> NextTid, young |-> TRUE,
                              gl |-> <<AbsentId, <<8, 8>>>>, view |-> FALSE]}
       /\ procs' = procs \cup {[pid |-> nextSid + 1, pn |-> NextTid, owner |-> AbsentId, life |-> 0,
                                conv |-> 0, res |-> FALSE, started |-> FALSE,
                                blocked |-> blk /\ AbsentId = BotId, ending |-> FALSE,
                                gl |-> <<AbsentId, <<8, 8>>>>]}
  /\ nextSid' = nextSid + 1
  /\ UNCHANGED <<row, hist, nextLife, faults, envVars, orchVars, botVars, apprVars, humVars, fmVars,
                 ghostVars>>

SplitSpec  == Init /\ [][Next \/ ForeignSweep]_vars
SplitSpecF == Init /\ [][Next \/ ForeignSweep \/ ForeignSess]_vars
FairSplitT == SplitSpec /\ WF_vars(FM_List) /\ WF_vars(FM_Check) /\ WF_vars(Age)
              /\ \A k \in Sids : WF_vars(HookStartS(k))
              /\ WF_vars(B_ApproveLookup) /\ WF_vars(B_ApproveAct) /\ TimeFair /\ LaunchFair

Mine(p)          == p.owner \in Ids /\ p.owner # AbsentId
NoOrphanS        == \A p \in procs : Mine(p) => row[p.owner].st # "none"
NoOrphanLifeS    == \A p \in procs : Mine(p) => (row[p.owner].st # "none" /\ row[p.owner].life = p.life)
OneAgentPerIdS   == \A p1, p2 \in procs : (Mine(p1) /\ p1.owner = p2.owner) => p1 = p2
NoWrongMemoryS   == \A p \in procs : Mine(p) => p.conv = p.life
VerdictSoundS    == \A i \in Ids : (i # AbsentId /\ row[i].st # "none") =>
                      LET v == LookupN(i) IN
                      /\ v.v = "ours" => v.s.gl = CurLaunch(i)
                      /\ v.v \in {"gone", "leftover"} => CurSessions(i) = {}
                      /\ v.v = "leftover" => v.s.gl[1] = i
=============================================================================
