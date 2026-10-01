package apitest

// descriptions_kill_optin.go holds the shared description helper's SR-1.4
// cases for kill's operator-only finished-row opt-in (SR-6.5, Epic 18): the
// live-row refusal (DescKillOptInLiveRow). The opt-in's spellings, kill as a
// command and the tmux session-ending forms are rejected for every case by
// AssertDescription; a case adds only its own must-nots.

// DescKillOptInLiveRow is ErrSpawnNotResumable for kill's finished-row opt-in
// on a live row, pending included (SR-1.4, SR-6.5; PRD OQ13): the instance
// id; "the row is live" and its state; that the finished-row option applies
// only to an ended or missing row; that no lookup was made and nothing was
// sent. It names no other command (SR-6.8), such as find-missing or list
// --tmux-session-name, and needs no "Operator actions" pointer.
func DescKillOptInLiveRow(instanceID, state string) DescCase {
	return DescCase{
		Name: "ErrSpawnNotResumable, kill's finished-row opt-in on a live row",
		Require: []string{
			instanceID, "the row is live", "(state " + state + ")",
			"the finished-row option applies only to an ended or missing row",
			"no lookup was made and nothing was sent",
		},
		MustNot: []string{"find-missing", listSessionName},
	}
}
