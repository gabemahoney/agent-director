package api_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// seedWaiting seeds a waiting row id into dbPath (creating the store) with opts.
func seedWaiting(t *testing.T, dbPath, id string, opts ...apitest.SpawnOption) {
	t.Helper()
	if _, err := apitest.SeedSpawn(dbPath, id, store.StateWaiting, "/tmp", "off", "", true, opts...); err != nil {
		t.Fatalf("SeedSpawn(%s): %v", id, err)
	}
}

// openDB opens the store at dbPath, closing it when the test ends.
func openDB(t *testing.T, dbPath string) *store.Store {
	t.Helper()
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// jsonOf returns v's JSON encoding.
func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return string(b)
}

// jsonField returns v's JSON object's key value and whether the key is present.
func jsonField(t *testing.T, v any, key string) (json.RawMessage, bool) {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonOf(t, v)), &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	raw, ok := m[key]
	return raw, ok
}

// assertOptionalString checks a nullable string field: got nil and key absent
// from out's JSON when want is "", else got and the JSON value both want.
func assertOptionalString(t *testing.T, out any, key string, got *string, want string) {
	t.Helper()
	raw, ok := jsonField(t, out, key)
	switch {
	case want == "" && (got != nil || ok):
		t.Errorf("%s = %v, JSON %s (present %t); want nil and the key omitted", key, got, raw, ok)
	case want != "" && (got == nil || *got != want || string(raw) != strconv.Quote(want)):
		t.Errorf("%s = %v, JSON %s; want %q", key, got, raw, want)
	}
}

// TestGetCheckPermission pins SR-3.1 on a check_permission row: every open
// request is listed, its tool_input byte for byte (req-review m2); a decided
// request never is (req-review M1); none is a non-nil slice encoding as [].
func TestGetCheckPermission(t *testing.T) {
	t.Parallel()
	tokA, tokB := storefix.TestRequestTokenA, storefix.TestRequestTokenB
	type req struct{ tok, tool, input string }
	a := req{tokA, "Read", `{ "file" : "/tmp/x" , "mode":"rw" }`}
	b := req{tokB, "Bash", `{"cmd":"ls"}`}
	cases := []struct {
		name    string
		open    []req
		decided []string
		want    []req
	}{
		{"one open row", []req{a}, nil, []req{a}},
		{"two open rows", []req{a, b}, nil, []req{a, b}},
		{"no rows", nil, nil, nil},
		{"decided row beside an open one", []req{a, b}, []string{tokA}, []req{b}},
		{"only a decided row", []req{a}, []string{tokA}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := apitest.SeedDecideFixture(t, "on")
			for _, r := range tc.open {
				openAgentRequest(t, s, "id-d-1", r.tok, r.tool, r.input, 0)
			}
			for _, tok := range tc.decided {
				if ok, err := s.DecidePermissionRequest("id-d-1", tok, "allow", "", ""); err != nil || !ok {
					t.Fatalf("DecidePermissionRequest(%s) = %v, %v", tok, ok, err)
				}
			}
			got, err := api.Get(s, "id-d-1")
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.PermissionRequests == nil || len(got.PermissionRequests) != len(tc.want) {
				t.Fatalf("PermissionRequests = %#v; want %d rows, non-nil", got.PermissionRequests, len(tc.want))
			}
			for _, w := range tc.want {
				i := slices.IndexFunc(got.PermissionRequests, func(p api.PermissionRequestInfo) bool { return p.RequestToken == w.tok })
				if i < 0 {
					t.Errorf("no request %s in %+v", w.tok, got.PermissionRequests)
					continue
				}
				p := got.PermissionRequests[i]
				if p.RequestID == 0 || p.ToolName != w.tool || p.ToolInput != w.input || p.RequestedAt.IsZero() {
					t.Errorf("request %s = %+v; want a request id, %s, tool_input %q byte for byte, a requested_at", w.tok, p, w.tool, w.input)
				}
			}
			if len(tc.want) == 0 && !strings.Contains(jsonOf(t, got), `"permission_requests":[]`) {
				t.Errorf("JSON %s; want permission_requests:[]", jsonOf(t, got))
			}
		})
	}
}

// recordingGetStore is a GetStore double that injects read errors and records
// the permission reads and the lives history was read for.
type recordingGetStore struct {
	spawn               store.Spawn
	permErr, historyErr error
	permCalls           int
	historyLives        []int64
}

func (r *recordingGetStore) GetSpawn(id string) (store.Spawn, error) {
	if r.spawn.ClaudeInstanceID == id {
		return r.spawn, nil
	}
	return store.Spawn{}, store.ErrSpawnNotFound
}

func (r *recordingGetStore) OpenPermissionRequestsForSpawn(string) ([]store.PermissionRow, error) {
	r.permCalls++
	return nil, r.permErr
}

func (r *recordingGetStore) ListSessionHistory(_ string, life int64) ([]store.SessionHistoryEntry, error) {
	r.historyLives = append(r.historyLives, life)
	return nil, r.historyErr
}

// TestGetReadsAndErrors pins SR-3.1 and SR-5.9 with a recording store: the
// permission rows are read only in check_permission, history once for the
// row's own life, and either read's error propagates (b.v2c AC6/AC8).
func TestGetReadsAndErrors(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	cases := []struct {
		name                string
		state               string
		permErr, historyErr error
		permCalls           int
	}{
		{"waiting skips the permission read", store.StateWaiting, nil, nil, 0},
		{"check_permission read error", store.StateCheckPermission, boom, nil, 1},
		{"session history read error", store.StateEnded, nil, boom, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &recordingGetStore{spawn: store.Spawn{ClaudeInstanceID: "id-g", State: tc.state, LifeNumber: 3},
				permErr: tc.permErr, historyErr: tc.historyErr}
			want := tc.permErr
			if want == nil {
				want = tc.historyErr
			}
			got, err := api.Get(f, "id-g")
			if !errors.Is(err, want) || (err == nil && len(got.PermissionRequests) != 0) {
				t.Errorf("Get = %+v, %v; want error %v", got.PermissionRequests, err, want)
			}
			if f.permCalls != tc.permCalls || !slices.Equal(f.historyLives, []int64{3}) {
				t.Errorf("permission reads %d, history lives %v; want %d and [3]", f.permCalls, f.historyLives, tc.permCalls)
			}
		})
	}
}

// TestLivenessFieldsGetAndList pins SR-8.3 on get and list: the nullable
// liveness fields are omitted while NULL; liveness_unverified_since is
// normalized to RFC3339 UTC from SQLite CURRENT_TIMESTAMP text, kept when
// already RFC3339 and passed through verbatim when neither (never dropped).
func TestLivenessFieldsGetAndList(t *testing.T) {
	t.Parallel()
	rows := []struct{ id, since, note, wantSince string }{
		{"live-null", "", "", ""},
		{"live-sqlite-text", "2026-01-02 15:04:05", "probe_eacces", "2026-01-02T15:04:05Z"},
		{"live-rfc3339", "2026-09-19T12:34:56Z", "probe wall", "2026-09-19T12:34:56Z"},
		{"live-unparseable", "not-a-time", "", "not-a-time"},
	}
	dbPath := filepath.Join(t.TempDir(), "state.db")
	for _, r := range rows {
		var opts []apitest.SpawnOption
		if r.since != "" {
			opts = append(opts, apitest.WithLivenessUnverifiedSince(r.since))
		}
		if r.note != "" {
			opts = append(opts, apitest.WithLivenessNote(r.note))
		}
		seedWaiting(t, dbPath, r.id, opts...)
	}
	s := openDB(t, dbPath)
	list, err := api.List(s, api.ListParams{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, r := range rows {
		got, err := api.Get(s, r.id)
		if err != nil {
			t.Fatalf("Get(%s): %v", r.id, err)
		}
		i := slices.IndexFunc(list.Spawns, func(l api.ListRow) bool { return l.ClaudeInstanceID == r.id })
		if i < 0 {
			t.Fatalf("List has no row %s", r.id)
		}
		l := list.Spawns[i]
		t.Run(r.id, func(t *testing.T) {
			assertOptionalString(t, got, "liveness_unverified_since", got.LivenessUnverifiedSince, r.wantSince)
			assertOptionalString(t, got, "liveness_note", got.LivenessNote, r.note)
			assertOptionalString(t, l, "liveness_unverified_since", l.LivenessUnverifiedSince, r.wantSince)
			assertOptionalString(t, l, "liveness_note", l.LivenessNote, r.note)
		})
	}
}

// TestGetJSONLPathAndNoExtraEnv pins SR-9.3/SR-10.3 on get: jsonl_path is the
// persisted path verbatim, or "" with the key present on a legacy row; extra_env,
// a spawn input only, never reaches the output, neither key nor value.
func TestGetJSONLPathAndNoExtraEnv(t *testing.T) {
	t.Parallel()
	const path = "/home/user/.claude-custom/projects/-tmp/sess.jsonl"
	dbPath := filepath.Join(t.TempDir(), "state.db")
	seedWaiting(t, dbPath, "id-jsonl-set", apitest.WithJsonlPath(path),
		apitest.WithExtraEnv(map[string]string{"SECRET_TOKEN": "leak-me-not"}))
	seedWaiting(t, dbPath, "id-jsonl-legacy")
	s := openDB(t, dbPath)
	for id, want := range map[string]string{"id-jsonl-set": path, "id-jsonl-legacy": ""} {
		got, err := api.Get(s, id)
		if err != nil {
			t.Fatalf("Get(%s): %v", id, err)
		}
		if raw, ok := jsonField(t, got, "jsonl_path"); got.JSONLPath != want || !ok || string(raw) != strconv.Quote(want) {
			t.Errorf("%s: JSONLPath = %q, JSON %s (present %t); want %q", id, got.JSONLPath, raw, ok, want)
		}
		if out := jsonOf(t, got); strings.Contains(out, "extra_env") || strings.Contains(out, "leak-me-not") {
			t.Errorf("%s: get output carries extra_env: %s", id, out)
		}
	}
}
