# github-carry-over

What GitHub served for attempt 2 of Mac Run 43265158749
(gabemahoney/agent-director, commit 54419d43d4dadd7c5623ade1bc399349c17cc9f9),
made by "Re-run failed jobs" on 2026-11-12. `TestCarriedOverFromGitHub` reads it.

- `attempt2.json`: the attempt, trimmed to its ID, attempt, `head_sha` and `html_url`.
- `attempt2-jobs.json`: "List jobs for a workflow run attempt" for attempt 2, trimmed
  to the fields `ghrun.Job` reads. `part installer` is listed as job 131348905644 with
  `run_attempt` 2, though it ran in attempt 1 as job 131346928560; GitHub gave it
  attempt 1's start and end times (17:02:59 to 17:04:39Z), which the trim drops.
- `job-131348905644.log`: that job's log, byte for byte. It is attempt 1's log, the
  same bytes GitHub serves for job 131346928560; its record says `attempt: 1`.
- `mac-part-installer-1-record.json`: `record.json` of attempt 1's artifact
  `mac-part-installer-1` (artifact 13965657049), byte for byte.
- `parts.txt` and `expected-failures.txt`: `test/mac/` at the tested commit.
