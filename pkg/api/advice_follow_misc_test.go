package api_test

// advice_follow_misc_test.go (b.fji F2, F3, G1): list's label refusal,
// decide's missing request token and api.New's uninitialised store, each
// followed literally.

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestAdviceFollow_F2_ListLabelKeyValueForm: F2 "%q is not in key=value
// form"; list re-issued with the label as key=value returns the row.
func TestAdviceFollow_F2_ListLabelKeyValueForm(t *testing.T) {
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{Opts: []apitest.SpawnOption{apitest.WithRawLabels(`{"team":"alpha"}`)}})
	e.seedRow(t, killRowSpec{})

	_, err := api.List(e.st, api.ListParams{Labels: []string{"team"}})
	adviceAssertAdvice(t, err, api.ErrListInvalidLabel, `"team" is not in key=value form`)

	res, err := api.List(e.st, api.ListParams{Labels: []string{"team=alpha"}})
	if err != nil {
		t.Fatalf("list --label team=alpha: %v", err)
	}
	if len(res.Spawns) != 1 || res.Spawns[0].ClaudeInstanceID != r.ID {
		t.Errorf("list --label team=alpha = %+v; want only %s", res.Spawns, r.ID)
	}
}

// TestAdviceFollow_F3_DecideRequestTokenRequired: F3 "request_token is
// required"; decide re-issued with the token get shows applies the decision.
func TestAdviceFollow_F3_DecideRequestTokenRequired(t *testing.T) {
	e := newKillEnv(t)
	r := seedRelayRow(t, e, storefix.TestRequestTokenA)
	now := time.Now()
	params := api.DecideParams{ClaudeInstanceID: r.ID, Decision: "allow"}

	_, err := api.Decide(e.st, relayGuardWindow, now, params)
	adviceAssertAdvice(t, err, api.ErrMissingRequestToken, "request_token is required")

	row, err := api.Get(e.st, r.ID)
	if err != nil || len(row.PermissionRequests) != 1 {
		t.Fatalf("get = %+v, %v; want one open permission request", row.PermissionRequests, err)
	}
	params.RequestToken = row.PermissionRequests[0].RequestToken
	if _, err := api.Decide(e.st, relayGuardWindow, now, params); err != nil {
		t.Fatalf("decide with get's request_token: %v", err)
	}
	if pr, err := e.st.GetPermissionRequest(r.ID, params.RequestToken); err != nil || pr.Decision != "allow" {
		t.Errorf("request decision = %q (%v); want allow", pr.Decision, err)
	}
}

// TestAdviceFollow_G1_StoreNotInitialized: G1 Go doc "Initialize the store
// first or set CreateIfMissing: true."; api.New then opens the store either way.
func TestAdviceFollow_G1_StoreNotInitialized(t *testing.T) {
	adviceAssertGoDoc(t, "aliases.go", "ErrStoreNotInitialized", "Initialize the store first or set CreateIfMissing: true.")
	cases := []struct {
		name   string
		follow func(t *testing.T, opts *api.Options)
	}{
		{"initialize the store first", func(t *testing.T, opts *api.Options) {
			if _, err := apitest.InitStore(opts.StorePath); err != nil {
				t.Fatalf("InitStore: %v", err)
			}
		}},
		{"set CreateIfMissing: true", func(_ *testing.T, opts *api.Options) { opts.CreateIfMissing = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			opts := api.Options{StorePath: filepath.Join(dir, "state.db"), ConfigPath: filepath.Join(dir, "config.toml")}
			if c, err := api.New(opts); !errors.Is(err, api.ErrStoreNotInitialized) {
				if c != nil {
					_ = c.Close()
				}
				t.Fatalf("api.New = %v; want ErrStoreNotInitialized", err)
			}

			tc.follow(t, &opts)
			c, err := api.New(opts)

			if err != nil {
				t.Fatalf("api.New after %q: %v", tc.name, err)
			}
			defer c.Close() //nolint:errcheck
			if _, err := c.List(api.ListParams{}); err != nil {
				t.Errorf("list on the opened store: %v", err)
			}
		})
	}
}
