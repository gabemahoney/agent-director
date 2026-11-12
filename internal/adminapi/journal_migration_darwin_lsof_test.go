package adminapi

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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
			h.check(context.Background(), "old_or_unverified_mutators", h.blocker(200))
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
	for _, kind := range []string{"cwd", "rtd", "twd", "non-vnode-without-identity", "typeless-cwd"} {
		t.Run(kind, func(t *testing.T) {
			h := newDarwinTestHost(t)
			f := h.file("4", h.root)
			f.typ = "DIR"
			switch kind {
			case "cwd", "rtd", "twd":
				f.fd = kind
			case "non-vnode-without-identity":
				f.typ, f.hasDev, f.hasIno = "PIPE", false, false
			case "typeless-cwd":
				f.fd, f.hasType = "cwd", false
			}
			h.processes[200].files = append(h.processes[200].files, f)
			h.check(context.Background(), "")
		})
	}
	// SR-8.6: incomplete entries need an identity-confirmed, gated path.
	// A FIFO without a device is included in the missing-device row.
	for _, kind := range []string{"missing-device", "missing-inode", "err", "typeless", "unknown-type", "unknown-fd"} {
		for _, result := range []string{"db", "wal", "shm", "root", "store-eval", "store-kernel", "outside", "relative", "err-text", "case-root-other", "case-store-other", "failed-root", "failed-store"} {
			t.Run(kind+"/"+result, func(t *testing.T) {
				h := newDarwinTestHost(t)
				fsys := &darwinTestFS{journalFS: osJournalFS{}}
				h.v.fs = fsys
				storePath := h.db
				path := h.db
				switch result {
				case "wal":
					path += "-wal"
				case "shm":
					path += "-shm"
				case "root", "failed-root":
					path = filepath.Join(h.root, "held")
				case "outside":
					path = h.root + "-outside/held"
				case "relative":
					path = "relative/private-file"
				case "err-text":
					path = "unreadable private descriptor"
				case "case-root-other":
					other := filepath.Join(filepath.Dir(h.root), "ROOT")
					if err := os.Mkdir(other, 0700); err != nil {
						t.Fatal(err)
					}
					path = filepath.Join(other, "held")
				case "case-store-other":
					path = filepath.Join(filepath.Dir(h.db), "STORE.DB")
				case "store-eval":
					alias := filepath.Join(filepath.Dir(h.db), "store-alias")
					fsys.aliases = map[string]string{alias: h.db}
					h.db = alias // path is the resolved form of the configured DB
				case "store-kernel":
					path = h.db + "-kernel"
					fsys.aliases = map[string]string{path: h.db}
					fsys.kernel = map[string]string{h.db: path}
				}
				gated := result != "outside" && result != "relative" && result != "err-text"
				if kind == "missing-device" && result == "root" {
					if err := syscall.Mkfifo(path, 0600); err != nil {
						t.Fatal(err)
					}
				} else if gated && result != "store-eval" && result != "store-kernel" {
					h.write(path, []byte("held"))
				}
				// Deliberately use a store identity even for negative paths:
				// a partial/unknown entry cannot bypass its name gate with it.
				f := h.file("4", storePath)
				f.name = path
				switch kind {
				case "missing-device":
					f.typ, f.hasDev = "FIFO", false
				case "missing-inode":
					f.hasIno = false
				case "err":
					f.fd = "err"
				case "typeless":
					f.hasType = false
				case "unknown-type":
					f.typ = "PRIVATE_UNKNOWN_KIND"
				case "unknown-fd":
					f.fd = "PRIVATE_UNKNOWN_FD"
				}
				if strings.HasPrefix(result, "failed-") {
					// Use a distinct spelling, so only the listed-path lookup
					// fails; configured store/root validation still succeeds.
					f.name = filepath.Join(filepath.Dir(path), strings.ToUpper(filepath.Base(path)))
					fsys.before = func(op, p string) error {
						if p == f.name {
							return syscall.EIO
						}
						return nil
					}
				}
				h.processes[200].files = append(h.processes[200].files, f)
				positive := gated && !strings.HasPrefix(result, "case-") && !strings.HasPrefix(result, "failed-")
				if positive {
					h.check(context.Background(), "old_or_unverified_mutators", h.blocker(200))
				} else {
					h.check(context.Background(), "")
				}
				if !gated {
					for _, op := range []string{"lstat", "eval", "kernel", "readdir"} {
						if fsys.saw(op, f.name) {
							t.Fatalf("ungated name looked up through %s", op)
						}
					}
				} else if strings.HasPrefix(result, "failed-") && !fsys.saw("lstat", f.name) {
					t.Fatal("failed lookup was not reached")
				}
			})
		}
	}
}

func TestDarwinJournalEscapedForms(t *testing.T) {
	t.Parallel()
	for _, mark := range []string{`\`, "^"} {
		for _, target := range []string{"root", "store"} {
			for _, full := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/full=%t", mark, target, full), func(t *testing.T) {
					h := newDarwinTestHost(t)
					fsys := &darwinTestFS{journalFS: osJournalFS{}, fold: true}
					h.v.fs = fsys
					path := h.db + mark + "escaped"
					if target == "root" {
						h.root += mark + "escaped"
						if err := os.Mkdir(h.root, 0700); err != nil {
							t.Fatal(err)
						}
						path = filepath.Join(h.root, "held")
					} else {
						h.db = path
					}
					h.write(path, nil)
					f := h.file("4", path)
					// Uppercase the listed form to distinguish it from the
					// configured paths that collecting evidence must inspect.
					f.name = filepath.Join(filepath.Dir(h.db), strings.ToUpper(strings.TrimPrefix(path, filepath.Dir(h.db)+"/")))
					f.hasDev = full
					h.processes[200].files = append(h.processes[200].files, f)
					if full {
						h.check(context.Background(), "old_or_unverified_mutators", h.blocker(200))
					} else {
						h.check(context.Background(), "")
					}
					for _, op := range []string{"lstat", "eval", "kernel", "readdir"} {
						if fsys.saw(op, f.name) {
							t.Fatalf("escaped listed form looked up through %s", op)
						}
					}
				})
			}
		}
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
