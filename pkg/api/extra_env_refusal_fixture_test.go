package api_test

// extra_env_refusal_fixture_test.go holds the spawn and resume checks shared by
// the tests of an extra_env key both verbs refuse: b.nas's reserved HOME
// (reserved_home_test.go) and b.vpb's malformed key (invalid_env_key_test.go).

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// envRefusal is an extra env refused for key with the error name, whose
// description says the key reason (e.g. "sets HOME, which is reserved"), then
// ": " and detail ("": not checked).
type envRefusal struct {
	extraEnv             map[string]string
	key                  string
	name, reason, detail string
	cfgs                 []trustConfig // directories holding a .claude.json the refusal leaves untouched
}

// assertSpawnRefused spawns with r's extra env, from the call or, with
// template, a template's: r's refusal quoting the key, no row written, no tmux
// call, no .claude.json touched.
func assertSpawnRefused(t *testing.T, r envRefusal, template bool) {
	t.Helper()
	env := newSpawnEnv(t)
	own := seedTrustConfig(t, env.home, trustLacksEntry)
	p := api.SpawnParams{CWD: t.TempDir(), ExtraEnv: r.extraEnv}
	if template {
		body := "[extra_env]\n"
		for k, v := range r.extraEnv {
			body += fmt.Sprintf("%q = %q\n", k, v)
		}
		if _, err := apitest.SeedTemplate(filepath.Join(env.home, ".agent-director", "templates"), "refused-env", body); err != nil {
			t.Fatalf("SeedTemplate: %v", err)
		}
		p.Template, p.ExtraEnv = "refused-env", nil
	}
	rows := listIDs(t, env.c)

	_, err := env.c.Spawn(p)

	assertOneName(t, err, r.name)
	q := strconv.Quote(r.key)
	want := r.name + ": extra_env key " + q + " " + r.reason + ": " + r.detail
	if desc := errText(err); !strings.HasPrefix(desc, want) || !strings.Contains(desc, "; remove "+q+" from extra_env, and ") {
		t.Errorf("description %q\nwant it to start %q and say to remove that key", desc, want)
	}
	assertNoTmuxCalls(t, env.rec)
	if ids := listIDs(t, env.c); !reflect.DeepEqual(ids, rows) {
		t.Errorf("List ids = %q; want unchanged %q", ids, rows)
	}
	for _, cfg := range append([]trustConfig{own}, r.cfgs...) {
		cfg.check(t, "", false, "after the refused spawn")
	}
}

// assertResumeRefused resumes a row in state whose stored extra env is r's,
// its transcript removed when noTranscript: r's refusal before the transcript
// lookup, no tmux call, nothing written (row, history, trail), no .claude.json
// touched.
func assertResumeRefused(t *testing.T, r envRefusal, state string, noTranscript bool) {
	t.Helper()
	e := newKillEnv(t)
	spec := e.resumableSpec(rlkSettled(e), agentGone, apitest.WithExtraEnv(r.extraEnv))
	spec.State = state
	row := e.seedResumableRow(t, spec)
	if noTranscript {
		if err := os.Remove(row.JSONLPath); err != nil {
			t.Fatalf("remove transcript: %v", err)
		}
	}
	before := e.snapshotResume(t, row)

	_, err := e.resume(row.ID)

	assertOneName(t, err, r.name)
	want := r.name + ": resume of instance " + row.ID + ": the row's extra_env key " + strconv.Quote(r.key) + " " + r.reason + ": " + r.detail
	if desc := errText(err); !strings.HasPrefix(desc, want) || !strings.Contains(desc, "nothing was written and nothing was launched") {
		t.Errorf("description %q\nwant it to start %q and say nothing was written or launched", desc, want)
	}
	e.assertKillCalls(t)
	e.assertResumeWroteNothing(t, before)
	for _, cfg := range r.cfgs {
		cfg.check(t, "", false, "after the refused resume")
	}
}
