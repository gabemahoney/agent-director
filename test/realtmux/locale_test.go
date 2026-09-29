package realtmux_test

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// Locale (SR-2.2, SR-20.7; Appendix E U2 to U5; AC-LKP-08 client half): the
// client passes -u on every call, so non-ASCII names and instance ids come
// back exactly under LC_ALL=C or with no locale variables, and it adds no
// locale variable to the server it starts.

// isLocaleVar reports whether key is LANG, LANGUAGE or an LC_* variable.
func isLocaleVar(key string) bool {
	return key == "LANG" || key == "LANGUAGE" || strings.HasPrefix(key, "LC_")
}

// clearLocale unsets every locale variable of the process for the rest of
// the test, restoring each afterwards (the image sets LANG and LC_ALL).
func clearLocale(t testing.TB) {
	t.Helper()
	var keys []string
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); isLocaleVar(k) {
			keys = append(keys, k)
		}
	}
	unsetEnv(t, append(keys, "LANG", "LANGUAGE", "LC_ALL")...)
}

// localeVars returns the sorted locale entries of a KEY=VALUE list (tmux's
// show-environment lines or a /proc environ); "-KEY" removal lines count.
func localeVars(entries []string) []string {
	var out []string
	for _, kv := range entries {
		k, _, _ := strings.Cut(strings.TrimPrefix(kv, "-"), "=")
		if isLocaleVar(k) {
			out = append(out, kv)
		}
	}
	slices.Sort(out)
	return out
}

// envLines splits show-environment output into its non-empty lines.
func envLines(out string) []string {
	return strings.FieldsFunc(out, func(r rune) bool { return r == '\n' })
}

// TestLocaleNamesAndIDsCompareExactly creates ü-x for an agent-ü1 id on a
// fresh server under each hostile locale; lookup and pane listing see it exactly.
func TestLocaleNamesAndIDsCompareExactly(t *testing.T) {
	forms := tmuxfix.LocaleForms() // [0] U2/U3's name ü-x, [1] its instance id agent-ü1
	name, idForm := forms[0], forms[1]
	if name.WithoutU == "" || idForm.WithoutU == "" {
		t.Fatalf("tmuxfix.LocaleForms: want U2/U3's name and instance id first, got %+v", forms[:2])
	}
	cases := []struct {
		desc string
		set  []string // KEY=VALUE set after every locale variable is removed
	}{
		{desc: "LC_ALL=C", set: []string{"LC_ALL=C"}},
		{desc: "no locale variables"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			clearLocale(t)
			for _, kv := range tc.set {
				k, v, _ := strings.Cut(kv, "=")
				t.Setenv(k, v)
			}
			rt := newRealTmux(t) // fresh private socket: the create starts its server here
			c := rt.mustCreate(t, createSpec{Name: name.Exact, InstanceID: newInstanceID(idForm.Exact)})

			// The environment really hides UTF-8: a client without -u shows '_'.
			if got := rt.raw().withoutU().must(t, "list-sessions", "-F", "#{session_name}"); got != name.WithoutU+"\n" {
				t.Fatalf("list-sessions without -u = %q, want %q: the case's locale is not in effect", got, name.WithoutU+"\n")
			}

			cl := newClient()
			ans, err := cl.Lookup(rt.Socket)
			if err != nil {
				t.Fatalf("Lookup: %s", describe(err))
			}
			var got *tmux.Session
			for i := range ans.Sessions {
				if ans.Sessions[i].ID == c.Reply.SessionID {
					got = &ans.Sessions[i]
				}
			}
			if got == nil {
				t.Fatalf("Lookup lists no session %s (%d sessions)", c.Reply.SessionID, len(ans.Sessions))
			}
			if got.Name != name.Exact {
				t.Errorf("Lookup name = %q, want %q byte for byte", got.Name, name.Exact)
			}
			if got.Label.Kind != tmux.LabelValid {
				t.Fatalf("Lookup label kind = %v, want LabelValid", got.Label.Kind)
			}
			if got.Label.InstanceID != c.InstanceID {
				t.Errorf("Lookup label instance id = %q, want %q byte for byte", got.Label.InstanceID, c.InstanceID)
			}
			if got.Label.Token != c.Token {
				t.Errorf("Lookup label token differs from the one the create passed")
			}

			panes, err := cl.ListPanes(rt.Socket)
			if err != nil {
				t.Fatalf("ListPanes: %s", describe(err))
			}
			want := tmux.Pane{SessionID: c.Reply.SessionID, ID: c.Reply.PaneID, PID: c.Reply.PanePID}
			if !slices.ContainsFunc(panes, func(p tmux.Pane) bool {
				return p.SessionID == want.SessionID && p.ID == want.ID && p.PID == want.PID
			}) {
				t.Errorf("ListPanes = %+v, want a pane %s (pid %d) of session %s", panes, want.ID, want.PID, want.SessionID)
			}

			// The client adds no locale variable: the server's global and the pane's
			// environments hold exactly the case's; the session's holds none.
			wantVars := slices.Clone(tc.set)
			slices.Sort(wantVars)
			for _, env := range []struct {
				what          string
				entries, want []string
			}{
				{"server global environment", envLines(rt.must(t, "show-environment", "-g")), wantVars},
				{"session environment", envLines(rt.must(t, "show-environment", "-t", c.Reply.SessionID)), nil},
				{"pane process environment", procEnviron(t, c.Reply.PanePID), wantVars},
			} {
				if gotVars := localeVars(env.entries); !slices.Equal(gotVars, env.want) {
					t.Errorf("%s locale variables = %q, want %q", env.what, gotVars, env.want)
				}
			}
		})
	}
}
