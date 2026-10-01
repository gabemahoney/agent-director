package realtmux_test

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// kill's name, locale and socket-permission cases on real tmux (SRD SR-20.7,
// SR-3.2, SR-1.8, SR-2.2; PRD AC-LKP-09, AC-LKP-08, AC-CLS-02 kill halves).

// dollarBackslashNames returns the catalogue's stored forms of AC-LKP-09's
// $ and \ cases plus a$B and a$$ (SR-20.7).
func dollarBackslashNames(t testing.TB) []tmuxfix.StoredName {
	t.Helper()
	raws := []string{`a$b`, `a$_b`, `a${b}`, `a$1`, `a$`, `a$$b`, `a\b`, `a$B`, `a$$`}
	var names []tmuxfix.StoredName
	for _, n := range tmuxfix.StoredNames() {
		if slices.Contains(raws, n.Raw) {
			names = append(names, n)
		}
	}
	if len(names) != len(raws) {
		t.Fatalf("the replay catalogue holds %d of the %d names %q", len(names), len(raws), raws)
	}
	return names
}

// namedRow starts the labelled bystanders $0 and $1, then the live row named
// n.Raw (labelled by id); it returns the row and the bystanders' world.
func (f *killFix) namedRow(t *testing.T, n tmuxfix.StoredName) (killRow, map[string]idtPane) {
	t.Helper()
	for range 2 {
		f.agent(t, createSpec{StoreID: f.StoreID})
	}
	if !f.hasSession(t, "$1") {
		t.Fatalf("no session $1 after two creates on a fresh server")
	}
	others := idtWorld(t, f.realTmux)
	r := f.liveRow(t, killRowSpec{Name: n.Raw})
	if f.label(t, r.Reply.SessionID) == "" {
		t.Fatalf("session %s (name %q) carries no label", r.Reply.SessionID, n.Stored)
	}
	return r, others
}

// TestKillDollarAndBackslashNames: kill removes each $ or \ name's session,
// labelled by id, touching no other; the session whose id a$1 spells survives.
func TestKillDollarAndBackslashNames(t *testing.T) {
	for _, n := range dollarBackslashNames(t) {
		t.Run(n.Raw, func(t *testing.T) {
			f := newKillFix(t)
			r, others := f.namedRow(t, n)

			f.kill(t, r.InstanceID, 0).assertSuccess(t, true)
			f.assertSession(t, r.Reply.SessionID, false)
			assertProcs(t, true, r.Reply.PanePID)
			idtSame(t, "the kill", others, idtWorld(t, f.realTmux), idtPaneIDs(others)...)
			f.assertRowUnchanged(t, r.InstanceID, r.Before)
		})
	}
}

// TestKillPrefixNeighbourUntouched: kill of a short name never reaches a
// running neighbour whose name extends it, whether the row has its own session or none.
func TestKillPrefixNeighbourUntouched(t *testing.T) {
	for _, own := range []bool{true, false} {
		desc := "no session yet (pending)"
		if own {
			desc = "own session"
		}
		t.Run(desc, func(t *testing.T) {
			f := newKillFix(t)
			short := uniqueName()
			neighbour := f.agent(t, createSpec{Name: short + "-long", StoreID: f.StoreID})
			id, sessionID := newInstanceID("agent"), ""
			var before apitest.SpawnColumns
			if own {
				r := f.liveRow(t, killRowSpec{Name: short, InstanceID: id})
				before, sessionID = r.Before, r.Reply.SessionID
			} else {
				before = f.seedRow(t, id, short, "pending", store.LaunchIdentity{Token: newToken(t), Socket: f.Socket,
					ServerPID: neighbour.Server.PID, ServerStart: neighbour.Server.Start, ServerStarttime: neighbour.Server.Starttime})
			}
			world := idtWorld(t, f.realTmux)

			f.kill(t, id, 0).assertSuccess(t, own)
			if own {
				f.assertSession(t, sessionID, false)
			}
			idtSame(t, "the kill", world, idtWorld(t, f.realTmux), neighbour.Reply.PaneID)
			assertProcs(t, false, neighbour.Reply.PanePID)
			f.assertRowUnchanged(t, id, before)
		})
	}
}

// TestKillNonASCIINameUnderHostileLocale: under LC_ALL=C and with no locale
// variables, kill finds and ends the session of ü-x for an agent-ü1 id.
func TestKillNonASCIINameUnderHostileLocale(t *testing.T) {
	forms := tmuxfix.LocaleForms() // [0] U2/U3's name ü-x, [1] its instance id agent-ü1
	name, idForm := forms[0], forms[1]
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
			f := newKillFix(t) // a fresh private socket: the create starts its server under this locale
			r := f.liveRow(t, killRowSpec{Name: name.Exact, InstanceID: newInstanceID(idForm.Exact)})

			f.kill(t, r.InstanceID, 0).assertSuccess(t, true)
			f.assertSession(t, r.Reply.SessionID, false)
			assertProcs(t, true, r.Reply.PanePID)
			f.assertRowUnchanged(t, r.InstanceID, r.Before)
		})
	}
}

// TestKillSocketDeniedIsTmuxNotAvailable: with the row's socket at mode 000,
// kill is ErrTmuxNotAvailable naming the socket, and the session runs on.
func TestKillSocketDeniedIsTmuxNotAvailable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: root's access ignores the socket's mode 000, so tmux would still connect")
	}
	f := newKillFix(t)
	r := f.liveRow(t, killRowSpec{})
	chmodSocket(t, f.Socket, 0o000)

	k := f.kill(t, r.InstanceID, 0)
	k.assertRefused(t, "ErrTmuxNotAvailable", apitest.DescSocketPermission(f.Socket), r.Token, f.StoreID)
	chmodSocket(t, f.Socket, 0o600)
	f.assertSession(t, r.Reply.SessionID, true)
	assertProcs(t, false, r.Reply.PanePID)
	f.assertRowUnchanged(t, r.InstanceID, r.Before)
}
