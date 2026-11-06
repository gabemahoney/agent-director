package adminapi

import (
	"context"
	"testing"
)

func TestDarwinJournalDescriptorKinds(t *testing.T) {
	t.Parallel()
	for _, typ := range append(append([]string{}, lsofVnodeTypes...), "1400") {
		t.Run("vnode/"+typ, func(t *testing.T) {
			h := newDarwinTestHost(t)
			f := h.file("4", h.db)
			f.typ = typ
			h.processes[200].files = append(h.processes[200].files, f)
			h.check(context.Background(), "old_or_unverified_mutators", h.blocker(200, "agent_director_mutator"))
		})
	}
	for _, typ := range lsofNonVnodeTypes {
		t.Run("non-vnode/"+typ, func(t *testing.T) {
			h := newDarwinTestHost(t)
			// Even a store identity and a root name cannot turn a socket
			// or another non-vnode into filesystem evidence.
			f := h.file("4", h.db)
			f.typ, f.name = typ, h.root+"/private-file"
			h.processes[200].files = append(h.processes[200].files, f)
			h.check(context.Background(), "")
		})
	}
	for _, kind := range []string{"cwd", "rtd", "twd", "non-vnode-without-identity", "missing-device", "missing-inode", "err", "typeless", "typeless-cwd", "unknown-type", "unknown-fd"} {
		t.Run(kind, func(t *testing.T) {
			h := newDarwinTestHost(t)
			f := h.file("4", h.root)
			f.typ = "DIR"
			reason := "old_or_unverified_mutators"
			switch kind {
			case "cwd", "rtd", "twd":
				f.fd = kind
				reason = ""
			case "non-vnode-without-identity":
				f.typ, f.hasDev, f.hasIno = "PIPE", false, false
				reason = ""
			case "missing-device":
				f.hasDev = false
			case "missing-inode":
				f.hasIno = false
			case "err":
				f.fd = "err"
			case "typeless":
				f.hasType = false
			case "typeless-cwd":
				f.fd, f.hasType = "cwd", false
			case "unknown-type":
				f.typ = "PRIVATE_UNKNOWN_KIND"
			case "unknown-fd":
				f.fd = "PRIVATE_UNKNOWN_FD"
			}
			h.processes[200].files = append(h.processes[200].files, f)
			if reason == "" {
				h.check(context.Background(), "")
			} else {
				h.check(context.Background(), reason, h.blocker(200, "unverified_process"))
			}
		})
	}
}

func TestDarwinJournalMalformedListings(t *testing.T) {
	t.Parallel()
	for name, raw := range map[string]string{
		"file-before-process":  "f4\x00tREG\x00\n",
		"field-before-process": "nprivate-path\x00\n",
		"bad-pid":              "pprivate-pid\x00\n",
		"zero-pid":             "p0\x00\n",
		"duplicate-pid":        "p200\x00\np200\x00\n",
	} {
		t.Run(name, func(t *testing.T) {
			h := newDarwinTestHost(t)
			h.raw = []byte(raw)
			h.check(context.Background(), "process_inventory_unreadable")
		})
	}
}
