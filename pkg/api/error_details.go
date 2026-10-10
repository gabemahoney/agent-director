package api

import (
	"errors"
	"time"
)

// DetailedError is a verb error that carries err_details: an object of the
// facts a caller needs to act on the refusal, as fields (b.146 rule 15,
// decision 8 A), so no caller has to parse the English description. Err is
// the error itself: its chain holds the catalogued sentinel (errors.Is
// matches through DetailedError), or none for an unnamed error (ErrInternal),
// and its text is the description. Details is the err_details object, one of
// RelayFallenBackDetails, PaneAnswerInProgressDetails, ClaimTooSoonDetails,
// PaneChangedDetails and PaneKeySentDetails.
//
// The CLI writes Details as the error envelope's err_details key, the MCP
// server as its error data's err_details, and the TypeScript client exposes
// it as the error's errDetails. The key is optional: an error without details
// has none, and a caller that does not know the key ignores it.
type DetailedError struct {
	// Err is the error: its chain holds the catalogued sentinel.
	Err error
	// Details is the err_details object.
	Details any
}

// Error returns Err's text: the error's description.
func (e *DetailedError) Error() string { return e.Err.Error() }

// Unwrap returns Err, so errors.Is and errors.As see the sentinel.
func (e *DetailedError) Unwrap() error { return e.Err }

// ErrDetails returns err's err_details object: the Details of the first
// DetailedError in err's chain, or nil when there is none (b.146 rule 15).
func ErrDetails(err error) any {
	var de *DetailedError
	if errors.As(err, &de) {
		return de.Details
	}
	return nil
}

// RelayFallenBackDetails is ErrRelayFallenBack's err_details (b.146 rule 15),
// from decide and from send-keys: the request that fell back, with the same
// fields get-permission and get's permission_requests give it (its delivery
// facts at the refusal, pane_answer none or intent), the Spawn's state, and
// every other request of the Spawn that still awaits an answer.
type RelayFallenBackDetails struct {
	PermissionRequestInfo
	// State is the Spawn's state when the refusal was made.
	State string `json:"state"`
	// OpenRequests is every other request of the Spawn that still awaits an
	// answer (not acked, not answered at the pane, not closed), oldest first;
	// [] when none, and null when they could not be read.
	OpenRequests []OpenRequestFacts `json:"open_requests"`
}

// OpenRequestFacts is one entry of RelayFallenBackDetails.OpenRequests: an
// open request of the Spawn other than the one refused, with its delivery
// state, whether its relay hook runs (true, false, or null when it cannot be
// checked) and its pane_answer.
type OpenRequestFacts struct {
	// RequestToken is the request's token.
	RequestToken string `json:"request_token"`
	// ToolName is the tool the request asks for.
	ToolName string `json:"tool_name"`
	// RequestedAt is when the request was recorded.
	RequestedAt time.Time `json:"requested_at"`
	// Delivery is its delivery state (delivered, not_confirmed or
	// fallen_back).
	Delivery string `json:"delivery"`
	// HookAlive is whether its relay hook runs; null when it cannot be
	// checked.
	HookAlive *bool `json:"hook_alive"`
	// PaneAnswer is its pane_answer (none or intent on an open request).
	PaneAnswer string `json:"pane_answer"`
}

// PaneAnswerInProgressDetails is ErrPaneAnswerInProgress's err_details
// (b.146 problem 2): the request whose pane answer through send-keys is still
// being sent, the verdict that answer claims, when its intent was written,
// and whether its sender process runs (true; null when it cannot be checked,
// in which case the intent counts as in progress until not_before).
type PaneAnswerInProgressDetails struct {
	// RequestToken is the request's token.
	RequestToken string `json:"request_token"`
	// PaneAs is the verdict the pane answer in progress claims.
	PaneAs string `json:"pane_as"`
	// PaneIntentAt is when that pane answer's intent was written.
	PaneIntentAt time.Time `json:"pane_intent_at"`
	// SenderAlive is true when its sender process runs; null when it cannot
	// be checked.
	SenderAlive *bool `json:"sender_alive"`
	// NotBefore is, when the sender cannot be checked, the time the intent
	// stops counting as in progress; null otherwise.
	NotBefore *time.Time `json:"not_before"`
}

// ClaimTooSoonDetails is ErrClaimTooSoon's err_details (b.146 rule 13): the
// request, whether its relay hook runs (true, false, or null when it cannot
// be checked), when a reader first found it gone (null when not yet), and the
// earliest time record-pane-answer can accept a record for it: hook_gone_at
// plus 2 s once the hook is gone; for a hook that cannot be checked (or a
// request recorded before schema v7), its confirm_by plus 2 s while it may
// still answer, and afterwards whenever that is earlier than hook_gone_at plus
// 2 s; null only while the hook is seen running.
type ClaimTooSoonDetails struct {
	// RequestToken is the request's token.
	RequestToken string `json:"request_token"`
	// HookAlive is whether its relay hook runs; null when it cannot be
	// checked.
	HookAlive *bool `json:"hook_alive"`
	// HookGoneAt is when a reader first found its relay hook gone; null when
	// none has.
	HookGoneAt *time.Time `json:"hook_gone_at"`
	// NotBefore is the earliest time a record can be accepted; null while the
	// relay hook is seen running.
	NotBefore *time.Time `json:"not_before"`
}

// PaneKeySentDetails is the err_details of a pane answer through send-keys
// whose one key was sent but whose last write, pane_answer sent, failed
// (b.146 rule 8): an unnamed error (ErrInternal), told by these details from
// one that sent nothing. The request still reads pane_answer intent; the
// caller reads the pane before anything else is sent to it, and closes the
// request with record-pane-answer if the pane shows it answered.
type PaneKeySentDetails struct {
	// KeySent is true: the pane answer's key was sent to the agent's pane.
	KeySent bool `json:"key_sent"`
	// RequestToken is the request the pane answer named.
	RequestToken string `json:"request_token"`
}

// PaneChangedDetails is ErrPaneChanged's err_details (b.146 rule 7): the
// number of trailing pane lines captured and compared (n_lines), and the
// request named, if any. It never carries the pane's new hash: a caller reads
// the pane again (read-pane) before it retries.
type PaneChangedDetails struct {
	// NLines is the number of trailing pane lines captured and compared.
	NLines int `json:"n_lines"`
	// RequestToken is the request the call named; omitted for a plain
	// send-keys.
	RequestToken string `json:"request_token,omitempty"`
}
