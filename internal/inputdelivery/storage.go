package inputdelivery

import "github.com/gabemahoney/agent-director/internal/inputjournal"

// Store and transport share inputjournal's one persisted protocol. Keep aliases
// here for Backend/Session consumers; never duplicate these records or validators.

// Identity binds one owned launch and process.
type Identity = inputjournal.Identity

// Attempt retains irreversible intent and exact proof.
type Attempt = inputjournal.Attempt

// Watermark retains the transcript boundary before input.
type Watermark = inputjournal.Watermark

// Proof retains exact provider record bytes.
type Proof = inputjournal.Proof

// Evidence binds confirmation to its value and revision.
type Evidence = inputjournal.Evidence

// Capability is the pinned provider contract.
type Capability = inputjournal.Capability

// Launch contains operational authority and evidence provenance.
type Launch = inputjournal.Launch

// ImportRecord is one validated legacy snapshot.
type ImportRecord = inputjournal.ImportRecord

// Guard pins admission to the captured row.
type Guard = inputjournal.Guard

// Persistence is the store-owned input authority contract.
type Persistence = inputjournal.Persistence

type manifest = Capability

var (
	ErrInvalidState = inputjournal.ErrInvalidState
	ErrStale        = inputjournal.ErrStale
	ErrUnconfirmed  = inputjournal.ErrUnconfirmed
)

// ValidateLaunch reuses the canonical persisted protocol validation.
func ValidateLaunch(l Launch) error { return inputjournal.ValidateLaunch(l) }

// ValidateAttempt reuses the canonical persisted protocol validation.
func ValidateAttempt(l Launch, a *Attempt) error { return inputjournal.ValidateAttempt(l, a) }

// ValidateTransition reuses the canonical persisted protocol validation.
func ValidateTransition(old, next *Attempt) error { return inputjournal.ValidateTransition(old, next) }
