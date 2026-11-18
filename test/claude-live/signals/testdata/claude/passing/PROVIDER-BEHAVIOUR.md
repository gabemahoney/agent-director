# Provider behaviour recorded from capture 6 (MI decision A2, 2026-11-18)

**Provider:** Claude Code 2.1.311. **Run:** 00000000-0000-4000-8000-000000000021, continuation launch.

## Wake rendering after a Stop continuation

When an AD wake's queued task-notification reaches the model during a Stop-hook continuation, Claude Code renders it differently from the primary launch.

- **What changes:** it arrives as a **system-role** request blob, not a user-role `<system-reminder>` blob. The outer `<system-reminder>` wrapper is gone, and the blob is labelled `Stop hook blocking error from command "FileChanged"`.
- **Where:** `raw/live-00000000-0000-4000-8000-000000000021-continuation-transcript.jsonl`, line 59, in request 3 (17:20:11.800Z). The queued attachment is at line 61 (`queued_command`, `commandMode` `task-notification`, written 17:19:43.620Z).
- **The AD block is whole:** `[agent-director: message 1 of 1] Probe CONTINUATION-WAKE-V1 …`, with the same escaping as the primary launch.
- **The wake is sighted and acted on:** the sighting is at 17:20:11.776Z. At line 63, the model runs the wake's requested 20-second loop.
- **The steer comes later:** it arrives whole in request 4 (17:20:34.399Z), after the wake.

**P-b relies on this behaviour.** A wake delivered during a Stop continuation is sighted and taken whole before the later STEER, in this system-role form. The analyzer accepts this form only for this path, and only as an exact single match of the attachment's rendered text without the outer wrapper (repair 14). The user-role form is unchanged.

**Who needs this:** z9's shakedown and the Model Docs should treat this as observed provider behaviour. If a later Claude Code version changes the form, the analyzer will report the wake input as missing, not as passing.
