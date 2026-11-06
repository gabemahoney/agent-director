package adminapi

import (
	"bytes"
	"context"
	"debug/buildinfo"
	"debug/macho"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
)

// darwinTestHost is the must-use Darwin seam fixture for internal/adminapi:
// its process table, procfix.Checker, listing renderer, darwinTestFS wrappers
// and Mach-O builder are shared by verifier, containment and advice tests.
// Extend these fixtures instead of adding another host, FS wrapper or builder.
type darwinTestHost struct {
	t                                       *testing.T
	v                                       darwinVerifier
	procs                                   *procfix.Checker
	processes                               map[int]*darwinTestProcess
	db, root, image, start                  string
	selectedAt, listedAt                    time.Time
	afterSelection, inListing, afterListing func()
	selectionErr, listingErr                error
	raw, stderr                             []byte
	helperFiles                             []lsofFile
	acl                                     map[string][]byte
	aclCalls                                []string
	aclErr                                  error
	aclStderr                               []byte
}

type darwinTestProcess struct {
	uid, ppid int
	files     []lsofFile
}

func newDarwinTestHost(t *testing.T) *darwinTestHost {
	t.Helper()
	// Darwin's default temp directory can be reached through /var's link;
	// the verifier deliberately requires real paths all the way to /.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := &darwinTestHost{t: t, procs: procfix.New(), processes: map[int]*darwinTestProcess{}, acl: map[string][]byte{}}
	h.db, h.root = filepath.Join(dir, "store.db"), filepath.Join(dir, "root")
	if err := os.Mkdir(h.root, 0700); err != nil {
		t.Fatal(err)
	}
	h.write(h.db, []byte("private-store-content"))
	h.image = filepath.Join(dir, "ordinary-image")
	h.write(h.image, darwinThinImage(t, "other", macho.TypeExec))
	// The synthetic process starts after the real image's ctime. The clock
	// stays injected; no wait is needed to order real file writes and starts.
	start := time.Now().Add(time.Hour).Truncate(time.Microsecond)
	h.start = darwinTestStart(start)
	h.selectedAt, h.listedAt = start.Add(time.Second), start.Add(2*time.Second)
	h.v = darwinVerifier{procs: h.procs, ppids: h, fs: osJournalFS{}, selfPID: 100, euid: 501}
	h.add(100, h.v.euid, 1)
	h.add(200, h.v.euid, 1, h.file("txt", h.image))
	h.v.selectUID = func(uid int) ([]int, error) {
		if uid != h.v.euid {
			t.Fatalf("selection uid = %d", uid)
		}
		var pids []int
		for pid, p := range h.processes {
			if p.uid == uid {
				pids = append(pids, pid)
			}
		}
		sort.Ints(pids)
		if h.afterSelection != nil {
			h.afterSelection()
		}
		return pids, h.selectionErr
	}
	clockRead := 0
	h.v.now = func() time.Time {
		clockRead++
		if clockRead%2 == 1 {
			return h.selectedAt
		}
		return h.listedAt
	}
	h.v.listFiles = func(ctx context.Context, uid int, started func(int)) ([]byte, []byte, error) {
		if uid != h.v.euid {
			t.Fatalf("listing uid = %d", uid)
		}
		if h.inListing != nil {
			h.inListing()
		}
		// Model the helper appearing after selection, and remaining alive
		// with the identity read by started until the exclusions run.
		h.add(101, uid, h.v.selfPID, h.helperFiles...)
		started(101)
		out := h.render(uid)
		if h.raw != nil {
			out = h.raw
		}
		if h.afterListing != nil {
			h.afterListing()
		}
		return out, h.stderr, h.listingErr
	}
	h.v.readACL = func(ctx context.Context, path string, started func(int)) ([]byte, []byte, error) {
		h.aclCalls = append(h.aclCalls, path)
		out, ok := h.acl[path]
		if !ok {
			out = []byte("-rw------- 1 owner staff 0 Nov 6 12:00 private-name\n")
		}
		return out, h.aclStderr, h.aclErr
	}
	return h
}

func darwinTestStart(at time.Time) string {
	return fmt.Sprintf("%d.%d", at.Unix(), at.Nanosecond()/1000)
}

func (h *darwinTestHost) write(path string, data []byte) {
	h.t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		h.t.Fatal(err)
	}
}

func (h *darwinTestHost) add(pid, uid, ppid int, files ...lsofFile) {
	h.processes[pid] = &darwinTestProcess{uid: uid, ppid: ppid, files: files}
	h.procs.Set(pid, procfix.Alive(h.start))
}

func (h *darwinTestHost) PPID(pid int) (int, bool) {
	p, ok := h.processes[pid]
	if !ok {
		return 0, false
	}
	return p.ppid, true
}

func (h *darwinTestHost) file(fd, path string) lsofFile {
	h.t.Helper()
	st, err := (osJournalFS{}).Lstat(path)
	if err != nil {
		h.t.Fatal(err)
	}
	return lsofFile{fd: fd, typ: "REG", hasType: true, name: path, id: st.id, hasDev: true, hasIno: true}
}

func (h *darwinTestHost) render(uid int) []byte {
	var out bytes.Buffer
	var pids []int
	for pid, p := range h.processes {
		if p.uid == uid {
			pids = append(pids, pid)
		}
	}
	sort.Ints(pids)
	for _, pid := range pids {
		fmt.Fprintf(&out, "p%d\x00\n", pid)
		for _, f := range h.processes[pid].files {
			fmt.Fprintf(&out, "f%s\x00", f.fd)
			if f.hasType {
				fmt.Fprintf(&out, "t%s\x00", f.typ)
			}
			if f.hasDev {
				fmt.Fprintf(&out, "D0x%x\x00", f.id.dev)
			}
			if f.hasIno {
				fmt.Fprintf(&out, "i%d\x00", f.id.ino)
			}
			fmt.Fprintf(&out, "n%s\x00\n", f.name)
		}
	}
	return out.Bytes()
}

func (h *darwinTestHost) check(ctx context.Context, reason string, processes ...JournalProcess) *JournalRefusal {
	h.t.Helper()
	err := h.v.verify(ctx, h.db, []string{h.root})
	return assertDarwinRefusal(h.t, err, reason, processes...)
}

func (h *darwinTestHost) blocker(pid int, role string) JournalProcess {
	action := "stop_or_make_inventory_readable"
	if role == "agent_director_mutator" {
		action = "stop_and_restart_after_cutover"
	}
	return JournalProcess{PID: pid, Starttime: h.start, Role: role, Action: action}
}

func assertDarwinRefusal(t *testing.T, err error, reason string, processes ...JournalProcess) *JournalRefusal {
	t.Helper()
	if reason == "" {
		if err != nil {
			t.Fatalf("unexpected refusal: %v", err)
		}
		return nil
	}
	var refusal *JournalRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("want %s, got %v", reason, err)
	}
	if processes == nil {
		processes = []JournalProcess{}
	}
	if refusal.Reason != reason || !reflect.DeepEqual(refusal.Processes, processes) {
		t.Fatalf("refusal = %#v, want %s %#v", refusal, reason, processes)
	}
	// Exact wire keys/values guard all failures against leaking names,
	// file contents, helper diagnostics or arbitrary OS error text.
	wire, err := json.Marshal(refusal)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(struct {
		Reason    string           `json:"reason"`
		Processes []JournalProcess `json:"processes"`
		Action    string           `json:"action"`
	}{reason, processes, journalActions[reason]})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(wire, want) {
		t.Fatalf("wire = %s, want %s", wire, want)
	}
	return refusal
}

// darwinTestFS wraps real temporary files. fold simulates APFS case folding;
// aliases/kernel simulate kernel path forms; before injects refusal/blocking.
// Every call is recorded, including calls abandoned at the attempt deadline.
type darwinTestFS struct {
	journalFS
	fold            bool
	aliases, kernel map[string]string
	before          func(op, path string) error
	mu              sync.Mutex
	calls           []string
}

func (f *darwinTestFS) lookup(op, path string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, op+":"+path)
	f.mu.Unlock()
	if f.before != nil {
		if err := f.before(op, path); err != nil {
			return "", &fs.PathError{Op: op, Path: path, Err: err}
		}
	}
	for alias, real := range f.aliases {
		if path == alias || strings.HasPrefix(path, alias+string(filepath.Separator)) {
			path = real + strings.TrimPrefix(path, alias)
			break
		}
	}
	if !f.fold {
		return path, nil
	}
	current := string(filepath.Separator)
	clean := filepath.Clean(path)
	if clean == current {
		return current, nil
	}
	for _, name := range strings.Split(strings.TrimPrefix(clean, current), current) {
		entries, err := f.journalFS.ReadDir(current)
		if err != nil {
			return "", err
		}
		found := false
		for _, entry := range entries {
			if strings.EqualFold(entry, name) {
				current = filepath.Join(current, entry)
				found = true
				break
			}
		}
		if !found {
			return "", &fs.PathError{Op: op, Path: path, Err: fs.ErrNotExist}
		}
	}
	return current, nil
}

func (f *darwinTestFS) Lstat(path string) (journalStat, error) {
	p, err := f.lookup("lstat", path)
	if err != nil {
		return journalStat{}, err
	}
	return f.journalFS.Lstat(p)
}
func (f *darwinTestFS) ReadDir(path string) ([]string, error) {
	p, err := f.lookup("readdir", path)
	if err != nil {
		return nil, err
	}
	return f.journalFS.ReadDir(p)
}
func (f *darwinTestFS) Open(path string) (journalImageFile, journalStat, error) {
	p, err := f.lookup("open", path)
	if err != nil {
		return nil, journalStat{}, err
	}
	return f.journalFS.Open(p)
}
func (f *darwinTestFS) EvalSymlinks(path string) (string, error) {
	p, err := f.lookup("eval", path)
	if err != nil {
		return "", err
	}
	return f.journalFS.EvalSymlinks(p)
}
func (f *darwinTestFS) KernelPath(path string) ([]string, error) {
	p, err := f.lookup("kernel", path)
	if err != nil {
		return nil, err
	}
	if alias, ok := f.kernel[p]; ok {
		return []string{alias}, nil
	}
	return f.journalFS.KernelPath(p)
}
func (f *darwinTestFS) saw(op, path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.calls {
		if call == op+":"+path {
			return true
		}
	}
	return false
}

// blockDarwinLookup releases and joins the blocked seam at cleanup. The
// verifier must return before release, proving the kernel call is abandoned.
// skipMatches optionally lets earlier checks of the same path complete.
func blockDarwinLookup(t *testing.T, f *darwinTestFS, op, path string, skipMatches ...int) (<-chan struct{}, func()) {
	t.Helper()
	entered, released, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	skip := 0
	if len(skipMatches) > 0 {
		skip = skipMatches[0]
	}
	f.before = func(gotOp, gotPath string) error {
		if gotOp != op || gotPath != path {
			return nil
		}
		if skip > 0 {
			skip--
			return nil
		}
		enterOnce.Do(func() { close(entered) })
		<-released
		close(done)
		return errors.New("blocked lookup released")
	}
	release := func() { releaseOnce.Do(func() { close(released) }); <-done }
	t.Cleanup(func() {
		select {
		case <-entered:
			release()
		default:
			releaseOnce.Do(func() { close(released) })
		}
	})
	return entered, release
}

// darwinThinImage emits a minimal 64-bit Mach-O with Go's inline buildinfo;
// module "test-binary" is the metadata a newer Go gives this module's test
// binaries. Validation uses the standard readers; it is fixture setup, not a
// meta-test.
func darwinThinImage(t *testing.T, module string, kind macho.Type) []byte {
	t.Helper()
	info := make([]byte, 32)
	copy(info, "\xff Go buildinf:")
	info[14], info[15] = 8, 2
	text := "path\texample.test/program\nmod\texample.test/other\tv1.0.0\n"
	switch module {
	case "main":
		text = "path\texample.test/program\nmod\t" + journalModulePath + "\tv1.0.0\n"
	case "dependency":
		text += "dep\t" + journalModulePath + "\tv1.0.0\n"
	case "test-binary":
		text = "path\t" + journalModulePath + "/internal/adminapi.test\nmod\t" + journalModulePath + "\t(devel)\n"
	}
	for _, s := range []string{"go1.22.10", strings.Repeat("x", 16) + text + strings.Repeat("y", 16)} {
		info = binary.AppendUvarint(info, uint64(len(s)))
		info = append(info, s...)
	}
	if module == "none" {
		info = make([]byte, len(info))
	}
	const offset = 256
	data := make([]byte, offset+len(info))
	put := func(off int, n uint32) { binary.LittleEndian.PutUint32(data[off:], n) }
	put64 := func(off int, n uint64) { binary.LittleEndian.PutUint64(data[off:], n) }
	put(0, macho.Magic64)
	put(4, uint32(macho.CpuArm64))
	put(8, 0)
	put(12, uint32(kind))
	put(16, 1)
	put(20, 152)
	put(32, uint32(macho.LoadCmdSegment64))
	put(36, 152)
	copy(data[40:], "__DATA")
	put64(56, 0x1000)
	put64(64, uint64(len(info)))
	put64(72, offset)
	put64(80, uint64(len(info)))
	put(88, 3)
	put(92, 3)
	put(96, 1)
	copy(data[104:], "__go_buildinfo")
	copy(data[120:], "__DATA")
	put64(136, 0x1000)
	put64(144, uint64(len(info)))
	put(152, offset)
	copy(data[offset:], info)
	if _, err := macho.NewFile(bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if module != "none" {
		if _, err := buildinfo.Read(bytes.NewReader(data)); err != nil {
			t.Fatalf("Mach-O fixture metadata: %v", err)
		}
	}
	return data
}

func darwinUniversalImage(t *testing.T, modules ...string) []byte {
	t.Helper()
	data := make([]byte, 8+20*len(modules))
	binary.BigEndian.PutUint32(data, macho.MagicFat)
	binary.BigEndian.PutUint32(data[4:], uint32(len(modules)))
	for i, module := range modules {
		kind := macho.TypeExec
		if module == "non-executable" {
			kind = macho.TypeObj
		}
		thin := darwinThinImage(t, module, kind)
		cpu := uint32(macho.CpuArm64)
		if i == 0 {
			cpu = uint32(macho.CpuAmd64)
		}
		binary.LittleEndian.PutUint32(thin[4:], cpu)
		for len(data)%8 != 0 {
			data = append(data, 0)
		}
		at := 8 + 20*i
		for j, n := range []uint32{cpu, 0, uint32(len(data)), uint32(len(thin)), 3} {
			binary.BigEndian.PutUint32(data[at+j*4:], n)
		}
		data = append(data, thin...)
	}
	if _, err := macho.NewFatFile(bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	return data
}
