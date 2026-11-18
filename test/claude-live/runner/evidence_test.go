package runner

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func TestClaudeLiveEvidenceConfinement(t *testing.T) {
	r, _ := runnerFixture(t)
	outside := t.TempDir()
	putFixture(t, filepath.Join(outside, "canary"), []byte("outside"))
	if err := os.Symlink(outside, filepath.Join(r.Evidence.Root, "link")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", ".", "..", "../canary", "/absolute", "raw/../canary", "raw\\canary", "link/canary"} {
		t.Run(name, func(t *testing.T) {
			if p, err := Confined(r.Evidence.Root, name); err == nil {
				t.Errorf("unsafe reference %q resolved to %q", name, p)
			}
			if err := r.Evidence.Put(name, []byte("overwrite")); err == nil {
				t.Errorf("wrote unsafe reference %q", name)
			}
		})
	}
	if string(readFixture(t, filepath.Join(outside, "canary"))) != "outside" {
		t.Fatal("outside file changed")
	}
	if _, err := r.Evidence.Index(); err == nil {
		t.Fatal("index accepted symlink evidence")
	}
	if _, err := Read(filepath.Join(r.Evidence.Root, "link"), "canary"); err == nil {
		t.Fatal("read accepted symlink root")
	}
	if _, err := NewEvidence(r.Evidence.Root); err == nil {
		t.Fatal("reused existing evidence")
	}
}

func TestClaudeLiveEvidenceImmutableBoundedAndIndexed(t *testing.T) {
	r, _ := runnerFixture(t)
	b := []byte("whole\x00evidence\n雪")
	if err := r.Evidence.Put("raw/exact", b); err != nil {
		t.Fatal(err)
	}
	if err := r.Evidence.Put("raw/exact", []byte("replacement")); err == nil {
		t.Fatal("overwrote finalized artifact")
	}
	if err := r.Evidence.JSON("manifest.json", map[string]string{"excluded": "index"}); err != nil {
		t.Fatal(err)
	}
	index, err := r.Evidence.Index()
	if err != nil || !reflect.DeepEqual(index, map[string]Artifact{"raw/exact": {SHA256: Digest(b), Bytes: int64(len(b))}}) {
		t.Fatalf("index: %+v %v", index, err)
	}
	for _, path := range []string{r.Evidence.Root, filepath.Join(r.Evidence.Root, "raw"), filepath.Join(r.Evidence.Root, "raw/exact")} {
		st, err := os.Stat(path)
		if err != nil || st.Mode().Perm()&0077 != 0 {
			t.Fatalf("public evidence: %s %v", path, err)
		}
	}
	large := filepath.Join(r.Evidence.Root, "large")
	putFixture(t, large, nil)
	if err := os.Truncate(large, MaxArtifactBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(r.Evidence.Root, "large"); err == nil {
		t.Fatal("read accepted oversized file")
	}
	if _, err := Read(r.Evidence.Root, "raw"); err == nil {
		t.Fatal("read accepted directory")
	}
}

func TestClaudeLiveCollectorReadOnlyScopedAllowlist(t *testing.T) {
	r, _ := runnerFixture(t)
	path, owned := seedRunner(t, r, "waiting", true, apitest.WithExtraEnv(map[string]string{"PRIVATE_TEST": "synthetic-store-secret"}), apitest.WithRawLabels(`{"secret":"synthetic-label-secret"}`))
	_, foreign := seedRunner(t, r, "waiting", true)
	var ownedMessage string
	for _, o := range []Owned{owned, foreign} {
		id := storefix.SeedBusySendResult(t, path, storefix.BusySendResultSeed{InstanceID: o.InstanceID,
			Pair: store.BusySendPair{CallerUUID: uuid.NewString(), DeliveryRequestID: uuid.NewString()}, Class: store.BusySendClassSentUnconfirmed,
			Transport: store.BusySendTransportWritten})
		if o.InstanceID == owned.InstanceID {
			ownedMessage = id
		}
	}
	// Seeding is finished. The collector must neither chmod nor migrate this DB.
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	before := readFixture(t, path)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := Observe(ctx, path, owned.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	columns := map[string]string{
		"spawns":    "instance_id state session_id jsonl_path pid proc_starttime launch_token tmux_socket launch_started_at turn_starts",
		"launches":  "launch_id store_id instance_id launch_token phase provider route_kind route_id connection_generation wake_path recorded_at_ms ended_at_ms end_cause",
		"messages":  "message_id instance_id text text_sha256 accepted_seq accepted_at_ms send_mode place handover_id bound_launch_id bound_session_id result_class result_reason",
		"handovers": "handover_id instance_id launch_id launch_token session_id boundary forced force_id expected_turn_starts intent_turn_starts intent_running_turn claimed_at_ms closed_at_ms",
		"prompts":   "handover_id prompt_index message_count emission issued_at_ms transport transport_at_ms order_kind order_at_ms admission_kind admitted_at_ms",
		"members":   "handover_id member_index message_id prompt_index", "forces": "force_id instance_id launch_id handover_id outcome reason started_at_ms ended_at_ms",
	}
	if len(s.Tables) != len(columns) || s.InstanceID != owned.InstanceID || s.Observed.IsZero() {
		t.Fatalf("snapshot shape: %+v", s)
	}
	for table, wantColumns := range columns {
		rows, ok := s.Tables[table]
		if !ok || (table != "forces" && len(rows) != 1) {
			t.Fatalf("%s exported %d rows; want one owned row", table, len(rows))
		}
		for _, row := range rows {
			var keys []string
			for k := range row {
				keys = append(keys, k)
			}
			want := strings.Fields(wantColumns)
			sort.Strings(keys)
			sort.Strings(want)
			if !reflect.DeepEqual(keys, want) {
				t.Fatalf("%s columns: %v, want %v", table, keys, want)
			}
		}
	}
	if Text(s.Tables["messages"][0], "message_id") != ownedMessage || Text(s.Tables["members"][0], "message_id") != ownedMessage || Text(s.Tables["prompts"][0], "handover_id") != Text(s.Tables["handovers"][0], "handover_id") {
		t.Fatal("snapshot broke message/prompt/hand-over binding")
	}
	b, _ := json.Marshal(s)
	for _, forbidden := range []string{foreign.InstanceID, "synthetic-store-secret", "synthetic-label-secret", "extra_env", "agent_args", "labels"} {
		if bytes.Contains(b, []byte(forbidden)) {
			t.Fatalf("collector exported forbidden value %q", forbidden)
		}
	}
	r.Manifest.Owned = []Owned{owned}
	if err := r.Bind(s); err != nil || r.Manifest.Owned[0].LaunchID != Text(s.Tables["launches"][0], "launch_id") {
		t.Fatalf("Launch binding: %+v %v", r.Manifest.Owned, err)
	}
	if !bytes.Equal(before, readFixture(t, path)) {
		t.Fatal("collector changed database bytes/schema")
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0640 {
		t.Fatalf("collector changed permissions: %v", err)
	}
	db, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "DELETE FROM spawns"); err == nil {
		t.Fatal("collector connection allows writes")
	}
}

func TestClaudeLiveCollectorRefusesInvalidSourcesAndBindings(t *testing.T) {
	r, _ := runnerFixture(t)
	path, owned := seedRunner(t, r, "waiting", true)
	r.Manifest.Owned = []Owned{owned}
	s, err := Observe(context.Background(), path, owned.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, id, token, socket string
		turns                   int64
	}{
		{"unowned", "foreign", owned.LaunchToken, owned.Socket, 0},
		{"replacement", owned.InstanceID, "different", owned.Socket, 0},
		{"socket", owned.InstanceID, owned.LaunchToken, "/foreign/socket", 0},
		{"turn-budget", owned.InstanceID, owned.LaunchToken, owned.Socket, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r.Manifest.Owned = []Owned{owned}
			r.Manifest.ObservedTurns = 0
			row := Row{}
			for k, v := range s.Tables["spawns"][0] {
				row[k] = v
			}
			row["launch_token"], row["tmux_socket"], row["turn_starts"] = tc.token, tc.socket, tc.turns
			if err := r.Bind(Snapshot{InstanceID: tc.id, Tables: map[string][]Row{"spawns": {row}}}); err == nil {
				t.Fatal("unsafe binding accepted")
			}
			if tc.name != "turn-budget" && (!reflect.DeepEqual(r.Manifest.Owned, []Owned{owned}) || r.Manifest.ObservedTurns != 0) {
				t.Fatal("refused ownership partially overwrote identity/turn count")
			}
		})
	}
	for _, source := range []string{filepath.Join(t.TempDir(), "missing.db"), filepath.Join(t.TempDir(), "old.db")} {
		if filepath.Base(source) == "old.db" {
			// A genuine unsupported SQLite file, not a hand-built AD schema.
			db, err := sql.Open("sqlite", source)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("PRAGMA user_version=1"); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
		}
		before, beforeErr := os.ReadFile(source)
		if _, err := Observe(context.Background(), source, owned.InstanceID); err == nil {
			t.Fatal("invalid store observation passed")
		}
		after, afterErr := os.ReadFile(source)
		if !bytes.Equal(before, after) || os.IsNotExist(beforeErr) != os.IsNotExist(afterErr) {
			t.Fatal("collector created or migrated store")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Observe(ctx, owned.InstanceID); err == nil {
		t.Fatal("observation ignored cancellation")
	}
}

func TestClaudeLiveCollectRetainsOverBudgetEvidence(t *testing.T) {
	r, _ := runnerFixture(t)
	_, owned := seedRunner(t, r, "waiting", true, apitest.WithTurnStarts(7))
	r.Manifest.Owned = []Owned{owned}
	s, err := r.Observe(context.Background(), owned.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.BindOwned(s); err != nil {
		t.Fatalf("budget revoked ownership: %v", err)
	}
	bound := r.Manifest.Owned[0]
	if !errors.Is(r.CheckTurnBudget(), ErrTurnBudget) {
		t.Fatal("excess turn accepted")
	}
	// An older observation cannot make a later observed breach disappear.
	s.Tables["spawns"][0]["turn_starts"] = int64(1)
	if err := r.BindOwned(s); err != nil || r.Manifest.ObservedTurns != 7 || !errors.Is(r.CheckTurnBudget(), ErrTurnBudget) {
		t.Fatal("observed turn maximum regressed")
	}
	paneCalls := 0
	r.Commands.Execute = func(ctx context.Context, argv, environment []string, out, errout io.Writer) (int, error) {
		paneCalls++
		want := []string{filepath.Join(r.Home, ".config/agent-director/bin/agent-director"), "read-pane", "--instance-id", owned.InstanceID, "--n-lines", "80"}
		if !reflect.DeepEqual(argv, want) {
			t.Errorf("unexpected collection command: %q", argv)
		}
		_, err := io.WriteString(out, "retained pane\n")
		return 0, err
	}
	if err := r.Collect(context.Background(), owned.InstanceID, "over-budget"); !errors.Is(err, ErrTurnBudget) {
		t.Fatalf("collection did not return budget failure: %v", err)
	}
	if paneCalls != 1 || r.Manifest.Owned[0] != bound {
		t.Fatal("budget prevented pane collection or changed identity")
	}
	exported := readJSON[Snapshot](t, filepath.Join(r.Evidence.Root, "raw/over-budget-store.json"))
	if Number(exported.Tables["spawns"][0], "turn_starts") != 7 {
		t.Fatal("budget evidence not retained")
	}
	if string(readFixture(t, filepath.Join(r.Evidence.Root, "raw/commands/0001.stdout"))) != "retained pane\n" {
		t.Fatal("pane evidence not retained")
	}
}

func TestClaudeLiveCollectionRejectsForeignFiles(t *testing.T) {
	for _, failure := range []string{"outside", "symlink", "wrong-session", "malformed-trail"} {
		t.Run(failure, func(t *testing.T) {
			r, _ := runnerFixture(t)
			_, owned := seedRunner(t, r, "waiting", true)
			putFixture(t, owned.Transcript, []byte("owned transcript\n"))
			trail := filepath.Join(r.Home, ".config/agent-director/ad-trail.jsonl")
			line, _ := json.Marshal(Row{"instance_id": owned.InstanceID, "event": "retained"})
			ownedTrail := append(line, '\n')
			putFixture(t, trail, append(append([]byte(nil), ownedTrail...), []byte("{\"instance_id\":\"foreign\"}\n")...))
			switch failure {
			case "outside":
				owned.Transcript = filepath.Join(t.TempDir(), owned.SessionID+".jsonl")
				putFixture(t, owned.Transcript, []byte("foreign"))
			case "symlink":
				target := filepath.Join(t.TempDir(), owned.SessionID+".jsonl")
				putFixture(t, target, []byte("foreign"))
				if err := os.Remove(owned.Transcript); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, owned.Transcript); err != nil {
					t.Fatal(err)
				}
			case "wrong-session":
				owned.SessionID = "other"
			case "malformed-trail":
				putFixture(t, trail, []byte("not JSON\n"))
			}
			v, err := r.ExecutePhase(context.Background(), func(context.Context, *Run) error { return r.CollectFiles(owned) }, nil)
			if err != nil || v.Status != "not_completed" {
				t.Fatalf("invalid collection passed: %+v %v", v, err)
			}
			if failure != "malformed-trail" {
				if !bytes.Equal(readFixture(t, filepath.Join(r.Evidence.Root, "raw/"+owned.InstanceID+"-trail.jsonl")), ownedTrail) {
					t.Fatal("transcript failure suppressed the independent scoped trail")
				}
			} else if string(readFixture(t, filepath.Join(r.Evidence.Root, "raw/"+owned.InstanceID+"-transcript.jsonl"))) != "owned transcript\n" {
				t.Fatal("trail failure discarded valid transcript evidence")
			}
		})
	}
}

func TestClaudeLiveTranscriptPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", t.TempDir()) // Resolution must use the explicit HOME only.
	for _, tc := range []struct{ name, cwd, session, recorded, want string }{
		{"fallback", "/work/a_b/雪", "parent", "", filepath.Join(home, ".claude/projects/-work-a-b--/parent.jsonl")},
		{"stored", "", "parent", filepath.Join(home, ".claude/projects/old-project/parent.jsonl"), filepath.Join(home, ".claude/projects/old-project/parent.jsonl")},
		{"missing-cwd", "", "parent", "", ""},
		{"relative-cwd", "work", "parent", "", ""},
		{"unclean-cwd", "/work/../other", "parent", "", ""},
		{"missing-session", "/work", "", "", ""},
		{"foreign-session", "/work", "../other", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, err := TranscriptPath(home, tc.cwd, tc.session, tc.recorded)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("unsafe identity resolved to %q", path)
				}
			} else if err != nil || path != tc.want {
				t.Fatalf("transcript path = %q, %v; want %q", path, err, tc.want)
			}
		})
	}
}

func TestClaudeLiveTranscriptFallback(t *testing.T) {
	for _, scenario := range []string{"null", "stored", "inflight", "cwd", "session", "path", "child-only", "inflight-only", "missing-file"} {
		t.Run(scenario, func(t *testing.T) {
			r, _ := runnerFixture(t)
			path, owned := seedRunner(t, r, "waiting", scenario == "stored")
			beforeDB := readFixture(t, path)
			r.Manifest.Owned = []Owned{owned}
			snapshot, err := r.Observe(context.Background(), owned.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			if scenario != "stored" && snapshot.Tables["spawns"][0]["jsonl_path"] != nil {
				t.Fatal("expected a real SQL NULL transcript path")
			}
			if err := r.BindOwned(snapshot); err != nil {
				t.Fatal(err)
			}
			bound := r.Manifest.Owned[0]
			if bound.CWD != owned.CWD {
				t.Fatal("store observation discarded the owned spawn cwd")
			}
			// Reuse the structural hook fixture with this synthetic parent's facts.
			input := readJSON[Row](t, "../../../internal/hook/testdata/hook-payloads/stop.json")
			input["cwd"], input["session_id"], input["transcript_path"] = owned.CWD, owned.SessionID, owned.Transcript
			switch scenario {
			case "cwd":
				input["cwd"] = filepath.Join(owned.CWD, "other")
			case "session":
				input["session_id"] = "another-session"
			case "path":
				input["transcript_path"] = filepath.Join(r.Home, ".claude/projects/other", owned.SessionID+".jsonl")
			case "child-only":
				input["agent_id"] = "subagent"
			case "missing-file":
				if err := os.Remove(owned.Transcript); err != nil {
					t.Fatal(err)
				}
			}
			hooks := filepath.Join(r.Evidence.Root, "raw/hooks")
			if err := os.MkdirAll(hooks, 0700); err != nil {
				t.Fatal(err)
			}
			if scenario == "inflight" || scenario == "inflight-only" {
				if err := os.Mkdir(filepath.Join(hooks, "invocation-inflight"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if scenario != "inflight-only" {
				t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", owned.InstanceID)
				encoded, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				RecordHook(r.Evidence.Root, r.Manifest.RunID, "Stop", bytes.NewReader(encoded))
			}
			r.Manifest.RecordingHook = RecordingHook{Installed: true, Events: []string{"Stop"}, Directory: "raw/hooks"}
			validRecording := scenario == "null" || scenario == "stored" || scenario == "inflight" || scenario == "missing-file"
			if err := CheckTranscriptRecordings(r.Evidence.Root, r.Manifest.RunID, owned); (err == nil) != validRecording {
				t.Fatalf("parent recording check = %v; valid=%v", err, validRecording)
			}
			got, err := r.ReadTranscript(bound)
			wantRead := validRecording && scenario != "missing-file"
			if wantRead {
				if err != nil || !bytes.Equal(got, readFixture(t, owned.Transcript)) || r.Manifest.Owned[0].Transcript != owned.Transcript {
					t.Fatalf("fallback did not read and retain the exact parent path: %q %v %+v", got, err, r.Manifest.Owned[0])
				}
			} else if err == nil || len(got) != 0 || r.Manifest.Owned[0] != bound {
				t.Fatalf("failed read exposed or rebound evidence: %q %v %+v", got, err, r.Manifest.Owned[0])
			}
			if scenario == "null" {
				// Read-only observation still says NULL after a successful read.
				next, err := r.Observe(context.Background(), owned.InstanceID)
				if err != nil || next.Tables["spawns"][0]["jsonl_path"] != nil {
					t.Fatalf("fallback wrote the store: %+v %v", next, err)
				}
				if err := r.BindOwned(next); err != nil {
					t.Fatal(err)
				}
				again, err := r.ReadTranscript(r.Manifest.Owned[0])
				if err != nil || !bytes.Equal(again, got) || r.Manifest.Owned[0].Transcript != owned.Transcript {
					t.Fatalf("subsequent NULL observation broke fallback: %v", err)
				}
			}
			if !bytes.Equal(beforeDB, readFixture(t, path)) {
				t.Fatal("transcript resolution changed store bytes")
			}
		})
	}
}

func TestClaudeLiveNonRegularEvidence(t *testing.T) {
	for _, kind := range []string{"socket", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			r, _ := runnerFixture(t)
			path := filepath.Join(r.Evidence.Root, "entry")
			if kind == "socket" {
				socketFile(t, path)
			} else {
				if err := syscall.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
				// Keep a peer so a regressed open-first reader reports failure
				// instead of hanging this package before its type check.
				fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_NONBLOCK, 0)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = syscall.Close(fd) })
			}
			if _, err := Read(r.Evidence.Root, "entry"); err == nil || err.Error() != `evidence "entry" is not a bounded regular file` {
				t.Fatalf("Read did not refuse %s before opening it: %v", kind, err)
			}
			st, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			index, err := r.Evidence.Index()
			want := fmt.Sprintf("non-regular artifact %q (%s)", "entry", st.Mode().Type())
			if err == nil || err.Error() != want || len(index) != 0 {
				t.Fatalf("Index accepted %s: %+v %v", kind, index, err)
			}
		})
	}
}
