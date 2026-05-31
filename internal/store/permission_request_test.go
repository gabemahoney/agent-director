package store

import (
	"database/sql"
	"errors"
	"testing"
)

// TestGetPermissionRequestReturnsErrNoRows pins the sql.ErrNoRows contract for
// three negative lookup cases: no row at all, existing token on wrong instance,
// and existing instance with wrong token.
func TestGetPermissionRequestReturnsErrNoRows(t *testing.T) {
	cases := []struct {
		name       string
		queryID    string
		queryToken string
		setup      func(t *testing.T, s *Store)
	}{
		{
			name:       "no_row_at_all",
			queryID:    "absent",
			queryToken: tokenA,
		},
		{
			name:       "existing_token_mismatched_instance",
			queryID:    "wrong-instance",
			queryToken: tokenA,
			setup: func(t *testing.T, s *Store) {
				t.Helper()
				seedSpawnForPerm(t, s, "real-instance", "on")
				if err := s.UpsertOpenPermissionRequest("real-instance", tokenA, "Bash", `{}`, 0); err != nil {
					t.Fatalf("setup upsert: %v", err)
				}
			},
		},
		{
			name:       "existing_instance_mismatched_token",
			queryID:    "real-instance2",
			queryToken: tokenB,
			setup: func(t *testing.T, s *Store) {
				t.Helper()
				seedSpawnForPerm(t, s, "real-instance2", "on")
				if err := s.UpsertOpenPermissionRequest("real-instance2", tokenA, "Bash", `{}`, 0); err != nil {
					t.Fatalf("setup upsert: %v", err)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := openTestStore(t)
			if tc.setup != nil {
				tc.setup(t, s)
			}
			_, err := s.GetPermissionRequest(tc.queryID, tc.queryToken)
			if !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("err = %v; want sql.ErrNoRows", err)
			}
		})
	}
}

// TestOpenPermissionRequestsForSpawn pins the 0/1/2 open-row cases and asserts
// that decided rows are excluded from the result.
func TestOpenPermissionRequestsForSpawn(t *testing.T) {
	t.Run("zero_open_rows", func(t *testing.T) {
		s := openTestStore(t)
		const id = "spawn-zero"
		seedSpawnForPerm(t, s, id, "on")

		rows, err := s.OpenPermissionRequestsForSpawn(id)
		if err != nil {
			t.Fatalf("OpenPermissionRequestsForSpawn: %v", err)
		}
		if len(rows) != 0 {
			t.Errorf("len = %d; want 0", len(rows))
		}
	})

	t.Run("one_open_row", func(t *testing.T) {
		s := openTestStore(t)
		const id = "spawn-one"
		seedSpawnForPerm(t, s, id, "on")
		if err := s.UpsertOpenPermissionRequest(id, tokenA, "Bash", `{}`, 0); err != nil {
			t.Fatalf("upsert: %v", err)
		}

		rows, err := s.OpenPermissionRequestsForSpawn(id)
		if err != nil {
			t.Fatalf("OpenPermissionRequestsForSpawn: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("len = %d; want 1", len(rows))
		}
		if rows[0].RequestToken != tokenA {
			t.Errorf("rows[0].RequestToken = %q; want %q", rows[0].RequestToken, tokenA)
		}
	})

	t.Run("two_open_rows", func(t *testing.T) {
		s := openTestStore(t)
		const id = "spawn-two"
		seedSpawnForPerm(t, s, id, "on")
		if err := s.UpsertOpenPermissionRequest(id, tokenA, "Bash", `{}`, 0); err != nil {
			t.Fatalf("upsert tokenA: %v", err)
		}
		if err := s.UpsertOpenPermissionRequest(id, tokenB, "Read", `{}`, 0); err != nil {
			t.Fatalf("upsert tokenB: %v", err)
		}

		rows, err := s.OpenPermissionRequestsForSpawn(id)
		if err != nil {
			t.Fatalf("OpenPermissionRequestsForSpawn: %v", err)
		}
		if len(rows) != 2 {
			t.Fatalf("len = %d; want 2", len(rows))
		}
	})

	t.Run("decided_row_excluded", func(t *testing.T) {
		s := openTestStore(t)
		const id = "spawn-decided"
		seedSpawnForPerm(t, s, id, "on")
		if err := s.UpsertOpenPermissionRequest(id, tokenA, "Bash", `{}`, 0); err != nil {
			t.Fatalf("upsert tokenA: %v", err)
		}
		if err := s.UpsertOpenPermissionRequest(id, tokenB, "Read", `{}`, 0); err != nil {
			t.Fatalf("upsert tokenB: %v", err)
		}
		if _, err := s.DecidePermissionRequest(id, tokenA, "allow", ""); err != nil {
			t.Fatalf("decide tokenA: %v", err)
		}

		rows, err := s.OpenPermissionRequestsForSpawn(id)
		if err != nil {
			t.Fatalf("OpenPermissionRequestsForSpawn: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("len = %d after deciding one row; want 1", len(rows))
		}
		if rows[0].RequestToken != tokenB {
			t.Errorf("rows[0].RequestToken = %q; want %q (undecided row)", rows[0].RequestToken, tokenB)
		}
	})
}
