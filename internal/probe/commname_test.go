package probe

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"syscall"
	"testing"
)

// Every per-OS core must satisfy the command-name reader interface.
var (
	_ CommandNameReader = linuxCommandNameReader{}
	_ CommandNameReader = darwinCommandNameReader{}
	_ CommandNameReader = unsupportedCommandNameReader{}
)

// writeFakeComm writes <root>/<pid>/comm with the given raw content.
func writeFakeComm(t *testing.T, root string, pid int, content string) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(content), 0o644); err != nil {
		t.Fatalf("write comm: %v", err)
	}
}

// kinfoCommEntry builds one kinfoProcSize entry with raw planted at kinfoProcCommOffset.
func kinfoCommEntry(raw []byte) []byte {
	buf := make([]byte, kinfoProcSize)
	copy(buf[kinfoProcCommOffset:kinfoProcCommOffset+kinfoProcCommLen], raw)
	return buf
}

// TestLinuxCommandNameReader pins the Linux answer table over a fabricated proc root.
func TestLinuxCommandNameReader(t *testing.T) {
	const pid = 4242
	cases := []struct {
		name     string
		comm     *string // nil: no <pid>/comm file
		pid      int
		root     string // "" = the fabricated root
		wantName string
		wantOK   bool
	}{
		{name: "trailing_newline_trimmed", comm: commp("claude\n"), pid: pid, wantName: "claude", wantOK: true},
		{name: "no_trailing_newline", comm: commp("sh"), pid: pid, wantName: "sh", wantOK: true},
		{name: "only_one_newline_trimmed", comm: commp("dash\n\n"), pid: pid, wantName: "dash\n", wantOK: true},
		{name: "name_with_spaces_kept", comm: commp("tmux: server\n"), pid: pid, wantName: "tmux: server", wantOK: true},
		{name: "fifteen_byte_name", comm: commp("abcdefghijklmno\n"), pid: pid, wantName: "abcdefghijklmno", wantOK: true},
		{name: "empty_file", comm: commp(""), pid: pid},
		{name: "newline_only", comm: commp("\n"), pid: pid},
		{name: "missing_pid", pid: pid},
		{name: "missing_proc_root", comm: commp("claude\n"), pid: pid, root: "/nonexistent-proc-root"},
		{name: "pid_zero", comm: commp("claude\n"), pid: 0},
		{name: "pid_negative", comm: commp("claude\n"), pid: -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.comm != nil {
				// Plant under both the queried pid and pid, so the non-positive
				// cases prove the pid guard rather than a missing file.
				writeFakeComm(t, root, pid, *tc.comm)
				if tc.pid != pid {
					writeFakeComm(t, root, tc.pid, *tc.comm)
				}
			}
			if tc.root != "" {
				root = tc.root
			}

			got, ok := linuxCommandNameReader{procRoot: root}.CommandName(tc.pid)
			if got != tc.wantName || ok != tc.wantOK {
				t.Errorf("CommandName(%d) = (%q, %v); want (%q, %v)", tc.pid, got, ok, tc.wantName, tc.wantOK)
			}
		})
	}
}

// TestLinuxCommandNameReaderReadsOnlyComm: a pid dir with stat and environ but no comm answers ("", false).
func TestLinuxCommandNameReaderReadsOnlyComm(t *testing.T) {
	root := t.TempDir()
	writeFakeProc(t, root, 4242, 1, "12345", "some-instance")

	if got, ok := (linuxCommandNameReader{procRoot: root}).CommandName(4242); got != "" || ok {
		t.Errorf("CommandName = (%q, %v); want (\"\", false)", got, ok)
	}
}

// TestDarwinCommandNameReader pins the darwin answer table against fixed kinfo entries.
func TestDarwinCommandNameReader(t *testing.T) {
	const pid = 4242
	cases := []struct {
		name     string
		pid      int
		buf      []byte
		err      error
		wantName string
		wantOK   bool
	}{
		{name: "named", pid: pid, buf: kinfoCommEntry([]byte("claude\x00")), wantName: "claude", wantOK: true},
		{name: "sixteen_byte_name", pid: pid, buf: kinfoCommEntry([]byte("abcdefghijklmnop\x00")), wantName: "abcdefghijklmnop", wantOK: true},
		{name: "esrch", pid: pid, err: syscall.ESRCH},
		{name: "eperm", pid: pid, err: syscall.EPERM},
		{name: "empty_result", pid: pid, buf: []byte{}},
		{name: "short_entry", pid: pid, buf: kinfoCommEntry([]byte("claude\x00"))[:kinfoProcSize-1]},
		{name: "drift_empty_name", pid: pid, buf: kinfoCommEntry(nil)},
		{name: "drift_no_nul", pid: pid, buf: kinfoCommEntry([]byte("abcdefghijklmnopq"))},
		{name: "pid_zero", pid: 0},
		{name: "pid_negative", pid: -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls []int
			r := darwinCommandNameReader{fetchKinfo: func(p int) ([]byte, error) {
				calls = append(calls, p)
				return tc.buf, tc.err
			}}

			got, ok := r.CommandName(tc.pid)
			if got != tc.wantName || ok != tc.wantOK {
				t.Errorf("CommandName(%d) = (%q, %v); want (%q, %v)", tc.pid, got, ok, tc.wantName, tc.wantOK)
			}
			wantCalls := []int{tc.pid}
			if tc.pid <= 0 {
				wantCalls = nil
			}
			if !reflect.DeepEqual(calls, wantCalls) {
				t.Errorf("fetchKinfo calls = %v; want %v", calls, wantCalls)
			}
		})
	}
}

// TestDarwinCommandNameReaderHasNoEnvSeam: the kinfo fetch is the reader's only seam (no KERN_PROCARGS2).
func TestDarwinCommandNameReaderHasNoEnvSeam(t *testing.T) {
	typ := reflect.TypeOf(darwinCommandNameReader{})
	var fields []string
	for i := 0; i < typ.NumField(); i++ {
		fields = append(fields, typ.Field(i).Name)
	}
	if !reflect.DeepEqual(fields, []string{"fetchKinfo"}) {
		t.Errorf("darwinCommandNameReader fields = %v; want only [fetchKinfo]", fields)
	}
}

// TestKinfoCommandNameParse pins parseKinfoComm's names and drift refusals, entry-granular.
func TestKinfoCommandNameParse(t *testing.T) {
	cases := []struct {
		name     string
		raw      []byte
		wantName string
		wantErr  bool
	}{
		{name: "plain", raw: []byte("zsh\x00"), wantName: "zsh"},
		{name: "stops_at_first_nul", raw: []byte("sh\x00garbage\x00"), wantName: "sh"},
		{name: "sixteen_bytes_then_nul", raw: []byte("abcdefghijklmnop\x00"), wantName: "abcdefghijklmnop"},
		{name: "utf8_kept", raw: []byte("caf\xc3\xa9\x00"), wantName: "café"},
		{name: "truncated_rune_kept", raw: []byte("caf\xc3\x00"), wantName: "caf\xc3"},
		{name: "space_and_tilde_kept", raw: []byte("a b~\x00"), wantName: "a b~"},
		{name: "empty", raw: []byte("\x00claude"), wantErr: true},
		{name: "no_nul_in_field", raw: []byte("abcdefghijklmnopq"), wantErr: true},
		{name: "tab_control_byte", raw: []byte("a\tb\x00"), wantErr: true},
		{name: "newline_control_byte", raw: []byte("ab\n\x00"), wantErr: true},
		{name: "soh_control_byte", raw: []byte("\x01ab\x00"), wantErr: true},
		{name: "del_control_byte", raw: []byte("ab\x7f\x00"), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseKinfoComm(kinfoCommEntry(tc.raw), 0)
			if tc.wantErr {
				if !errors.Is(err, ErrKinfoLayoutDrift) || got != "" {
					t.Fatalf("parseKinfoComm = (%q, %v); want (\"\", ErrKinfoLayoutDrift)", got, err)
				}
				return
			}
			if err != nil || got != tc.wantName {
				t.Fatalf("parseKinfoComm = (%q, %v); want (%q, nil)", got, err, tc.wantName)
			}
		})
	}
}

// TestKinfoCommandNameParseEntryBounds: the parser reads the entry at off, and refuses one that does not fit.
func TestKinfoCommandNameParseEntryBounds(t *testing.T) {
	buf := append(kinfoCommEntry([]byte("decoy\x00")), kinfoCommEntry([]byte("target\x00"))...)

	if got, err := parseKinfoComm(buf, kinfoProcSize); err != nil || got != "target" {
		t.Errorf("entry 1 = (%q, %v); want (\"target\", nil)", got, err)
	}
	for _, off := range []int{-1, kinfoProcSize + 1, 2 * kinfoProcSize} {
		if _, err := parseKinfoComm(buf, off); !errors.Is(err, ErrKinfoLayoutDrift) {
			t.Errorf("off %d: err = %v; want ErrKinfoLayoutDrift", off, err)
		}
	}
}

// TestUnsupportedCommandNameReader: every pid answers ("", false).
func TestUnsupportedCommandNameReader(t *testing.T) {
	for _, pid := range []int{-1, 0, 1, os.Getpid()} {
		if got, ok := (unsupportedCommandNameReader{}).CommandName(pid); got != "" || ok {
			t.Errorf("CommandName(%d) = (%q, %v); want (\"\", false)", pid, got, ok)
		}
	}
}

func commp(s string) *string { return &s }
