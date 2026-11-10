package cursortranscript

import (
	"crypto/sha256"
	"encoding/hex"
)

// Record kinds the private Cursor recorder writes and send-keys reads back as
// the gateway's verdict on a submission at Enter (b.bqd).
const (
	KindSubmissionAccepted = "submission_accepted"
	KindSubmissionRefused  = "submission_refused"
	KindInputHeld          = "input_held"
	KindHeldInputRefused   = "held_input_refused"
	KindInputControl       = "input_control"
)

// KindInputObserved records accepted input the gateway stopped before durable
// admission (durable_admission false), followed by its cancellation. It is
// neither an acceptance nor a refusal, so send-keys passes over it.
const KindInputObserved = "input_observed"

// TextSHA256 is the hex SHA-256 of a submitted text. A refusal that is the
// text's first verdict records it as text_sha256, beside the input's
// consumed_at, so a sender can tell its own refusal from another's, an
// identical earlier text's included (b.bqd).
func TextSHA256(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
