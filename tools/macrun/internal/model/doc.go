// Package model is macrun's result model, and it is reusable: case IDs, the
// plan and its setup errors, results and reasons, part records and their
// marked log lines, and the judging of completeness and verdicts. macrun's
// plan, part and verdict subcommands share it, so every job of a Mac Run
// names cases, writes records and reads them the same way. Later macrun
// work, such as time limits, testplan parts and the release gate, must use
// it rather than a second result model.
//
// Its JSON field names, enum strings and log line form are read across jobs
// and from earlier runs' artifacts, so renaming one breaks the Mac Run.
//
// It depends only on the standard library and reads no files.
package model
