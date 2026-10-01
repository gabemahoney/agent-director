package apitest

// descriptions_expire.go holds the shared description helper's texts of
// expire's manifest texts (Epic 15): its Description (DescExpireManifest), its
// count, ids, kept and kept_ids result fields (DescExpireField), and SR-18.7's
// cleanup guidance, stated in full in expire's Description
// (DescCleanupGuidance) and as a short pointer in the kill and find-missing
// Descriptions (DescCleanupPointer; Epic 15 build-lead decision 1).

// SR-18.7's phrases that expire's Description states and DescExpireManifest
// and DescCleanupGuidance share: the same user and tmux environment, the rows
// expire keeps, and that agents never run it ("Agents" capitalised, as the
// sentence opens).
const (
	sameUser          = "same user"
	sameTmuxEnv       = "same tmux environment"
	keepsUncheckable  = "keeps and reports rows whose session runs or cannot be checked"
	agentsNeverRun    = "Agents never run"
	operatorScheduled = "operator-scheduled"
)

// scheduleClaims are statements that an expire schedule exists or runs on a
// host (a named job or a stated run frequency), which no cleanup text makes
// (SR-18.7).
var scheduleClaims = []string{"cron", "crontab", "daily", "hourly", "nightly", "weekly", "runs every"}

// DescExpireManifest is expire's manifest Description (SR-18.9, SR-18.7), by
// key phrase: it reads tmux to decide, never kills a session, never touches
// transcripts, agents never run it, it keeps and reports rows whose session
// runs or cannot be checked, it runs as the agents' user in their tmux
// environment, and a run as another user, as root or against another tmux
// server can wrongly delete rows. Never the old "does not touch tmux" claim
// or a wrong run called harmless. Check it with AssertAgentTextCase.
func DescExpireManifest() DescCase {
	return DescCase{
		Name: "expire manifest description",
		Require: []string{
			"reads tmux to decide", "never kills a session", "touches transcripts",
			agentsNeverRun, keepsUncheckable, sameUser, sameTmuxEnv,
			"another user", "as root", "another tmux server", "can wrongly delete rows",
		},
		MustNot: []string{"does not touch tmux", "never touches tmux", "harmless"},
	}
}

// ExpireField names one of expire's result fields for DescExpireField.
type ExpireField string

// The result fields of expire.
const (
	ExpireCount   ExpireField = "count"
	ExpireIDs     ExpireField = "ids"
	ExpireKept    ExpireField = "kept"
	ExpireKeptIDs ExpireField = "kept_ids"
)

// DescExpireField is one of expire's result fields as the manifest states it
// (SR-12.4, SR-18.11), by key phrase. count and ids: rows deleted after tmux
// showed no session of the agent (Gone) and its recorded process was not seen
// running, never the old retention-match meaning; ids only while the row was
// unchanged. kept and kept_ids: rows kept rather than deleted; kept_ids
// reported in the trail (ad.expire.kept). Check it with AssertAgentTextCase.
// It panics on another field.
func DescExpireField(f ExpireField) DescCase {
	deleted := []string{
		"deleted after tmux showed no session of the agent (Gone)",
		"its recorded process was not seen running",
	}
	oldMeaning := []string{"matched the retention window", "rows removed"}
	keptRows := "kept rather than deleted"
	switch f {
	case ExpireCount:
		return DescCase{Name: "expire result field, count", Require: append(deleted, "length of ids"), MustNot: oldMeaning}
	case ExpireIDs:
		return DescCase{
			Name:    "expire result field, ids",
			Require: append(deleted, "Sorted", "unchanged since expire examined it", "Never null"),
			MustNot: oldMeaning,
		}
	case ExpireKept:
		return DescCase{
			Name:    "expire result field, kept",
			Require: []string{keptRows, "length of kept_ids", "may still run", "could not be checked"},
		}
	case ExpireKeptIDs:
		return DescCase{
			Name:    "expire result field, kept_ids",
			Require: []string{keptRows, "Sorted", "ad.expire.kept", "Never null"},
		}
	}
	panic("apitest: DescExpireField: not a result field of expire: " + string(f))
}

// DescCleanupGuidance is SR-18.7's cleanup guidance in full, as expire's
// Description states it: finished rows are removed by an operator-scheduled
// expire at the default retention, run as the agents' user in their tmux
// environment; it keeps and reports rows whose session runs or cannot be
// checked; agents never run it, least of all with a zero window. No text
// states or implies a schedule exists (scheduleClaims). Check it with
// AssertAgentTextCase.
func DescCleanupGuidance() DescCase {
	return DescCase{
		Name: "SR-18.7, cleanup guidance (full)",
		Require: []string{
			"Finished rows are removed by an " + operatorScheduled + " expire", "at the default retention",
			sameUser, sameTmuxEnv, keepsUncheckable, agentsNeverRun, "zero window",
		},
		MustNot: scheduleClaims,
	}
}

// DescCleanupPointer is SR-18.7's cleanup guidance as the short pointer the
// kill and find-missing Descriptions carry (Epic 15 build-lead decision 1):
// agents never run expire, an operator-scheduled cleanup. It must not carry
// the full sentence's phrases (DescCleanupGuidance) instead or as well, nor a
// schedule claim. Check it with AssertAgentTextCase (find-missing's on
// FindMissingOwnText).
func DescCleanupPointer() DescCase {
	return DescCase{
		Name:    "SR-18.7, cleanup guidance (pointer)",
		Require: []string{agentsNeverRun + " expire", operatorScheduled},
		MustNot: append([]string{"at the default retention", keepsUncheckable, "zero window"}, scheduleClaims...),
	}
}
