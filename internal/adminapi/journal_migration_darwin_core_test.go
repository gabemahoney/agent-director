package adminapi

import (
	"bytes"
	"context"
	"debug/macho"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/procstarttimefix"
)

// SR-8.6: both open and mapped files carry authority by identity, including
// the first store snapshot and root files no longer reachable by their name.
func TestDarwinJournalFileEvidence(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"db", "wal", "shm", "root", "unlinked", "refused", "case", "recreated"} {
		for _, fd := range []string{"4", "txt"} {
			t.Run(target+"/"+fd, func(t *testing.T) {
				h := newDarwinTestHost(t)
				path := h.db
				switch target {
				case "wal":
					path += "-wal"
				case "shm":
					path += "-shm"
				case "root", "unlinked", "refused", "case":
					path = filepath.Join(h.root, "private-file")
				}
				if target == "refused" {
					deep := filepath.Join(h.root, "protected-directory")
					if err := os.Mkdir(deep, 0700); err != nil {
						t.Fatal(err)
					}
					path = filepath.Join(deep, "private-file")
				}
				h.write(path, []byte("private-file-content"))
				entry := h.file(fd, path)
				switch target {
				case "unlinked":
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				case "refused":
					h.v.fs = &darwinTestFS{journalFS: osJournalFS{}, before: func(op, p string) error {
						if p == filepath.Dir(path) || strings.HasPrefix(p, filepath.Dir(path)+"/") {
							return syscall.EACCES
						}
						return nil
					}}
				case "case":
					entry.name = filepath.Join(filepath.Dir(h.root), "ROOT", "PRIVATE-FILE")
					h.v.fs = &darwinTestFS{journalFS: osJournalFS{}, fold: true}
				case "recreated":
					h.inListing = func() {
						replacement := h.db + ".new"
						h.write(replacement, []byte("new-store"))
						if err := os.Rename(replacement, h.db); err != nil {
							t.Fatal(err)
						}
					}
				}
				h.processes[200].files = append(h.processes[200].files, entry)
				h.check(context.Background(), "old_or_unverified_mutators", h.blocker(200, "agent_director_mutator"))
			})
		}
	}
}

func TestDarwinJournalRootFormsAndLookupGate(t *testing.T) {
	t.Parallel()
	for _, form := range []string{"outside-refused", "outside", "eval", "kernel", "kernel-error", "eval-error"} {
		t.Run(form, func(t *testing.T) {
			h := newDarwinTestHost(t)
			fsys := &darwinTestFS{journalFS: osJournalFS{}, fold: true}
			h.v.fs = fsys
			path := filepath.Join(h.root, "unlinked")
			h.write(path, []byte("secret"))
			entry := h.file("7", path)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			reason := "old_or_unverified_mutators"
			switch form {
			case "outside", "outside-refused":
				entry.name = h.root + "-unrelated/private-file"
				fsys.before = func(op, p string) error {
					if form == "outside-refused" && strings.HasPrefix(p, h.root+"-unrelated") {
						return syscall.EPERM
					}
					return nil
				}
				reason = ""
			case "eval":
				// An alias stands for a system-resolved form distinct from
				// the caller's spelling; only fs changes its interpretation.
				alias := filepath.Join(filepath.Dir(h.root), "ROOT-ALIAS")
				if err := os.Mkdir(alias, 0700); err != nil {
					t.Fatal(err)
				}
				fsys.aliases = map[string]string{alias: h.root}
				h.root = alias
			case "kernel":
				volume := filepath.Join(filepath.Dir(h.root), "kernel-volume")
				alias := filepath.Join(volume, "root")
				fsys.aliases = map[string]string{volume: filepath.Dir(h.root)}
				fsys.kernel = map[string]string{h.root: alias}
				entry.name = filepath.Join(alias, "unlinked")
			case "kernel-error", "eval-error":
				op := strings.TrimSuffix(form, "-error")
				fsys.before = func(gotOp, p string) error {
					if gotOp == op && p == h.root {
						return syscall.EIO
					}
					return nil
				}
				reason = "legacy_root_unreadable"
			}
			h.processes[200].files = append(h.processes[200].files, entry)
			if reason == "old_or_unverified_mutators" {
				h.check(context.Background(), reason, h.blocker(200, "agent_director_mutator"))
			} else {
				h.check(context.Background(), reason)
			}
			if strings.HasPrefix(form, "outside") && (fsys.saw("lstat", entry.name) || fsys.saw("open", entry.name)) {
				t.Fatal("unrelated file was looked up")
			}
			if form == "kernel" && !fsys.saw("lstat", entry.name) {
				t.Fatal("kernel form did not reach ancestor lookup")
			}
		})
	}
}

func TestDarwinJournalImages(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"name", "thin-main", "thin-dependency", "universal-later", "universal-other", "universal-no-metadata", "universal-not-executable", "replaced", "removed", "changed", "plain", "not-executable", "open-refused", "lookup-refused", "no-txt", "first-txt"} {
		t.Run(kind, func(t *testing.T) {
			h := newDarwinTestHost(t)
			fsys := &darwinTestFS{journalFS: osJournalFS{}}
			h.v.fs = fsys
			role := "unverified_process"
			image := h.image
			switch kind {
			case "name":
				image = filepath.Join(filepath.Dir(image), "renamed-agent-director-helper")
				h.write(image, []byte("unreadable-image-content"))
				role = "agent_director_mutator"
			case "thin-main", "thin-dependency":
				h.write(image, darwinThinImage(t, strings.TrimPrefix(kind, "thin-"), macho.TypeExec))
				role = "agent_director_mutator"
			case "universal-later":
				h.write(image, darwinUniversalImage(t, "other", "dependency"))
				role = "agent_director_mutator"
			case "universal-other":
				h.write(image, darwinUniversalImage(t, "other", "other"))
				role = ""
			case "universal-no-metadata":
				h.write(image, darwinUniversalImage(t, "none", "none"))
				role = ""
			case "universal-not-executable":
				h.write(image, darwinUniversalImage(t, "non-executable", "non-executable"))
			case "plain":
				h.write(image, []byte("private-not-an-image"))
			case "not-executable":
				h.write(image, darwinThinImage(t, "main", macho.TypeObj))
			}
			h.processes[200].files = []lsofFile{h.file("txt", image)}
			switch kind {
			case "replaced":
				replacement := image + ".new"
				h.write(replacement, darwinThinImage(t, "main", macho.TypeExec))
				if err := os.Rename(replacement, image); err != nil {
					t.Fatal(err)
				}
			case "removed":
				if err := os.Remove(image); err != nil {
					t.Fatal(err)
				}
			case "changed":
				h.start = procstarttimefix.DarwinProcStarttime
				h.procs.Set(200, procfix.Alive(h.start))
			case "open-refused", "lookup-refused":
				op := "open"
				if kind == "lookup-refused" {
					op = "lstat"
				}
				fsys.before = func(gotOp, p string) error {
					if gotOp == op && p == image {
						return syscall.EACCES
					}
					return nil
				}
			case "no-txt":
				h.processes[200].files = nil
			case "first-txt":
				// The first txt is the image; a later mapping's module
				// metadata must not substitute for the executable.
				mapped := image + "-mapped"
				h.write(mapped, darwinThinImage(t, "main", macho.TypeExec))
				h.processes[200].files = append(h.processes[200].files, h.file("txt", mapped))
				role = ""
			}
			if role == "" {
				h.check(context.Background(), "")
			} else {
				h.check(context.Background(), "old_or_unverified_mutators", h.blocker(200, role))
			}
			if (kind == "replaced" || kind == "removed" || kind == "lookup-refused" || kind == "changed") && fsys.saw("open", image) {
				t.Fatal("opened an image without established identity and ctime")
			}
		})
	}
}

func TestDarwinJournalSelectionAndReconciliation(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"other-uid", "zombie", "selection-error", "missing-self", "pid-zero", "missing-listed", "exits", "new-before", "new-between", "new-at-listing", "new-after", "bad-start", "changed-start", "unreadable-first", "unreadable-second", "mutator-precedence", "sorted"} {
		t.Run(kind, func(t *testing.T) {
			h := newDarwinTestHost(t)
			reason := "old_or_unverified_mutators"
			want := []JournalProcess{h.blocker(200, "unverified_process")}
			switch kind {
			case "other-uid":
				h.processes[200].uid++
				h.processes[200].files = []lsofFile{h.file("4", h.db)}
				reason, want = "", nil
			case "zombie":
				h.procs.Set(200, procfix.Zombie())
				reason, want = "", nil
			case "selection-error":
				h.selectionErr = errors.New("private-kernel-diagnostic")
				reason, want = "process_inventory_unreadable", nil
			case "missing-self":
				delete(h.processes, 100)
				reason, want = "process_inventory_unreadable", nil
			case "pid-zero":
				h.add(0, h.v.euid, 0)
				reason, want = "process_inventory_unreadable", nil
			case "missing-listed":
				h.inListing = func() { delete(h.processes, 200) }
			case "exits":
				h.afterListing = func() { h.procs.Set(200, procfix.Gone()) }
				reason, want = "", nil
			case "new-before", "new-between", "new-at-listing", "new-after":
				delete(h.processes, 200)
				at := h.selectedAt.Add(-time.Microsecond)
				if kind == "new-between" {
					at = h.selectedAt
				}
				if kind == "new-at-listing" {
					at = h.listedAt
				}
				if kind == "new-after" {
					at = h.listedAt.Add(time.Microsecond)
				}
				h.inListing = func() {
					h.add(200, h.v.euid, 1, h.file("txt", h.image), h.file("4", h.db))
					h.procs.Set(200, procfix.Alive(darwinTestStart(at)))
				}
				want[0].Starttime = darwinTestStart(at)
				if kind == "new-before" {
					reason, want = "process_inventory_unreadable", nil
				}
				if kind == "new-between" {
					want[0].Role, want[0].Action = "agent_director_mutator", "stop_and_restart_after_cutover"
				}
			case "bad-start":
				h.procs.Set(200, procfix.Alive("private-invalid-start"))
				want[0].Starttime = ""
			case "changed-start":
				h.processes[200].files = append(h.processes[200].files, h.file("4", h.db))
				h.afterListing = func() { h.procs.Set(200, procfix.Alive(procstarttimefix.DarwinProcStarttime)) }
			case "unreadable-first":
				h.procs.Set(200, procfix.Unreadable())
				want[0].Starttime = ""
			case "unreadable-second":
				h.processes[200].files = append(h.processes[200].files, h.file("4", h.db))
				h.afterListing = func() { h.procs.Set(200, procfix.Unreadable()) }
			case "mutator-precedence":
				h.processes[200].files = []lsofFile{{fd: "err"}, h.file("4", h.db)}
				want[0] = h.blocker(200, "agent_director_mutator")
			case "sorted":
				h.add(300, h.v.euid, 1, h.file("4", h.db))
				h.processes[200].files = nil
				want = append(want, h.blocker(300, "agent_director_mutator"))
			}
			h.check(context.Background(), reason, want...)
			if h.procs.EnvReads() != 0 {
				t.Fatal("process environment was read")
			}
			if kind == "new-before" {
				h.inListing = nil
				delete(h.processes, 200)
				h.procs.Set(200, procfix.Gone())
				h.check(context.Background(), "")
			}
		})
	}
}

func TestDarwinJournalHelperExclusions(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"descendants", "older-child", "helper-gone-at-start", "helper-pid-reused"} {
		t.Run(kind, func(t *testing.T) {
			h := newDarwinTestHost(t)
			h.processes[100].files = []lsofFile{h.file("4", h.db)}
			h.helperFiles = []lsofFile{h.file("4", h.db)}
			h.inListing = func() { h.add(102, h.v.euid, 101, h.file("4", h.db)); h.add(103, h.v.euid, 102, h.file("4", h.db)) }
			if kind == "older-child" {
				h.inListing = func() {
					h.add(102, h.v.euid, 101, h.file("4", h.db))
					h.procs.Set(102, procfix.Alive(procstarttimefix.DarwinProcStarttime))
				}
				// Selected first, so its earlier start isn't a reconciliation
				// failure; the helper's parent relation must not exclude it.
				h.add(102, h.v.euid, 101, h.file("4", h.db))
				h.procs.Set(102, procfix.Alive(procstarttimefix.DarwinProcStarttime))
			}
			if kind == "helper-gone-at-start" {
				h.v.listFiles = func(ctx context.Context, uid int, started func(int)) ([]byte, []byte, error) {
					started(999)
					return h.render(uid), nil, nil
				}
				h.check(context.Background(), "process_inventory_unreadable")
				return
			}
			if kind == "helper-pid-reused" {
				h.afterListing = func() { h.procs.Set(101, procfix.Alive(procstarttimefix.DarwinProcStarttime)) }
				h.check(context.Background(), "process_inventory_unreadable")
				return
			}
			if kind == "older-child" {
				want := h.blocker(102, "agent_director_mutator")
				want.Starttime = procstarttimefix.DarwinProcStarttime
				h.check(context.Background(), "old_or_unverified_mutators", want)
			} else {
				h.check(context.Background(), "")
			}
		})
	}
}

func TestDarwinJournalListingFailures(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"error", "warning", "malformed", "deadline"} {
		t.Run(kind, func(t *testing.T) {
			h := newDarwinTestHost(t)
			saved := h.v.listFiles
			switch kind {
			case "error":
				h.listingErr = errors.New("private-helper-command")
			case "warning":
				h.stderr = []byte("private-mount-warning")
			case "malformed":
				h.raw = []byte("f4\x00\n")
			case "deadline":
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				h.v.listFiles = func(ctx context.Context, _ int, _ func(int)) ([]byte, []byte, error) {
					cancel()
					<-ctx.Done()
					return nil, nil, ctx.Err()
				}
				h.check(ctx, "inventory_deadline")
			}
			if kind != "deadline" {
				h.check(context.Background(), "process_inventory_unreadable")
			}
			h.listingErr, h.stderr, h.raw = nil, nil, nil
			h.v.listFiles = saved
			h.check(context.Background(), "")
		})
	}
}

func TestDarwinJournalAccessControl(t *testing.T) {
	t.Parallel()
	for _, pathKind := range []string{"database", "root"} {
		for _, kind := range []string{"user", "group", "uuid", "malformed", "plus-empty", "error", "warning", "deny", "owner", "extended-attributes", "extended-allow-other", "deadline", "absent"} {
			t.Run(pathKind+"/"+kind, func(t *testing.T) {
				h := newDarwinTestHost(t)
				path := h.db
				if pathKind == "root" {
					path = h.root
				}
				header := "-rw-------+ 1 owner staff 0 Nov 6 12:00 private-name\n"
				reason := "legacy_root_access_unestablished"
				switch kind {
				case "user":
					h.acl[path] = []byte(header + " 0: user:other allow read\n")
				case "group":
					h.acl[path] = []byte(header + " 0: group:everyone inherited allow read\n")
				case "uuid":
					h.acl[path] = []byte(header + " 0: 01234567-89AB-CDEF-0123-456789ABCDEF allow read\n")
				case "malformed":
					h.acl[path] = []byte(header + "private-unparseable-entry\n")
				case "plus-empty":
					h.acl[path] = []byte(header)
				case "error":
					h.aclErr = errors.New("private-acl-diagnostic")
				case "warning":
					h.aclStderr = []byte("private-acl-warning")
				case "deny":
					h.acl[path] = []byte(header + " 0: group:everyone deny read\n")
					reason = ""
				case "owner":
					h.acl[path] = []byte(header + " 0: user:owner allow read,write\n")
					reason = ""
				case "extended-attributes":
					h.acl[path] = []byte(strings.Replace(header, "+", "@", 1) + " 0: user:owner allow read\n")
					reason = ""
				case "extended-allow-other":
					h.acl[path] = []byte(strings.Replace(header, "+", "@", 1) + " 0: user:other allow read\n")
				case "absent":
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					reason = ""
				case "deadline":
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					h.v.readACL = func(ctx context.Context, _ string, _ func(int)) ([]byte, []byte, error) {
						cancel()
						<-ctx.Done()
						return nil, nil, ctx.Err()
					}
					h.check(ctx, "inventory_deadline")
					return
				}
				h.check(context.Background(), reason)
				if kind == "absent" {
					for _, read := range h.aclCalls {
						if read == path {
							t.Fatal("read ACL of absent path")
						}
					}
				}
			})
		}
	}
}

func TestDarwinJournalBlockedLookupDeadline(t *testing.T) {
	t.Parallel()
	// WP-TR1: the initial path checks and ACL existence checks must refuse
	// before listing, without waiting for the blocked lookup or writing.
	for _, tc := range []struct {
		name, op, target string
		skip, aclReads   int // aclReads < 0 selects evidence after the listing.
	}{
		{"initial-db", "lstat", "db", 0, 0},
		{"initial-root", "lstat", "root", 0, 1},
		{"initial-ancestor", "lstat", "parent", 0, 0},
		{"initial-root-directory", "readdir", "root", 0, 1},
		{"initial-ancestor-directory", "readdir", "parent", 0, 0},
		{"acl-db", "lstat", "db", 1, 0},
		{"acl-root", "lstat", "root", 1, 1},
		// The first root ReadDir is now path validation; keep walk coverage.
		{"walk", "readdir", "root", 1, -1},
		{"kernel", "kernel", "root", 0, -1},
		{"ancestor", "lstat", "unlinked", 0, -1},
		{"open", "open", "image", 0, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newDarwinTestHost(t)
			fsys := &darwinTestFS{journalFS: osJournalFS{}}
			h.v.fs = fsys
			path := h.root
			switch tc.target {
			case "db":
				path = h.db
			case "parent":
				path = filepath.Dir(h.db)
			case "image":
				path = h.image
			case "unlinked":
				path = filepath.Join(h.root, "unlinked")
				h.write(path, []byte("secret"))
				h.processes[200].files = append(h.processes[200].files, h.file("4", path))
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(h.db)
			if err != nil {
				t.Fatal(err)
			}
			directories := map[string][]string{}
			for _, dir := range []string{filepath.Dir(h.db), h.root} {
				names, err := (osJournalFS{}).ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				directories[dir] = names
			}
			listed := make(chan struct{})
			h.inListing = func() { close(listed) }
			entered, _ := blockDarwinLookup(t, fsys, tc.op, path, tc.skip)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- h.v.verify(ctx, h.db, []string{h.root}) }()
			select {
			case <-entered:
				cancel()
			case err := <-result:
				t.Fatalf("verification returned before the blocked lookup: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("lookup never entered")
			}
			select {
			case err := <-result:
				assertDarwinRefusal(t, err, "inventory_deadline")
			case <-time.After(5 * time.Second):
				t.Fatal("deadline waited for blocked lookup")
			}
			if tc.aclReads >= 0 {
				select {
				case <-listed:
					t.Fatal("early lookup failure reached the listing")
				default:
				}
				var wantACL []string
				if tc.aclReads == 1 {
					wantACL = []string{h.db}
				}
				if !slices.Equal(h.aclCalls, wantACL) {
					t.Fatalf("ACL reads before refusal = %v; want %v", h.aclCalls, wantACL)
				}
			} else {
				select {
				case <-listed:
				default:
					t.Fatal("evidence lookup blocked before reaching the listing")
				}
			}
			// The seam is still blocked. No store bytes, lock, backup or root
			// entry may change before the refusal; cleanup releases and joins it.
			after, err := os.ReadFile(h.db)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("deadline changed store bytes: %v", err)
			}
			for dir, names := range directories {
				afterNames, err := (osJournalFS{}).ReadDir(dir)
				if err != nil || !slices.Equal(names, afterNames) {
					t.Fatalf("deadline changed directory %s: %v, %v; want %v", dir, afterNames, err, names)
				}
			}
		})
	}
}
