package api_test

// one_name_kill_optin_test.go holds the SR-1.5 one-name rows of kill with the
// finished-row opt-in (SR-6.5; Epic 18): one per error it returns that kill
// without it does not, driven through api.Kill on the kill fixture
// (kill_optin_fixture_test.go) and checked by assertOneName. They run under
// TestOneNameKillOptInReturnedErrors, so -run OneName selects them.

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
)

// TestOneNameKillOptInReturnedErrors: every error kill with the opt-in adds
// matches exactly one catalogued sentinel (as TestOneNameReturnedErrors).
func TestOneNameKillOptInReturnedErrors(t *testing.T) {
	for _, row := range oneNameKillOptInRows() {
		t.Run(row.name, func(t *testing.T) { assertOneName(t, row.run(t), row.want) })
	}
}

// oneNameKillOptIn is a row that runs api.Kill with the opt-in on a row
// seeded with spec after setup (when set) prepares e and r.
func oneNameKillOptIn(name, want string, spec killRowSpec, setup func(t *testing.T, e *killEnv, r *killRow)) oneNameRow {
	return oneNameRow{name: "kill with the opt-in/" + name, want: want, run: func(t *testing.T) error {
		e := newKillEnv(t)
		r := e.seedRow(t, spec)
		if setup != nil {
			setup(t, e, &r)
		}
		_, err := e.killOptIn(r.ID)
		return err
	}}
}

// oneNameKillOptInRows are the opt-in's returned errors: the live-row refusal.
func oneNameKillOptInRows() []oneNameRow {
	return []oneNameRow{
		oneNameKillOptIn("live row", "ErrSpawnNotResumable", killRowSpec{State: store.StateWorking}, nil),
	}
}
