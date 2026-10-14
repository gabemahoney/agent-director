//go:build linux

package cursorhost

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/gabemahoney/agent-director/internal/store"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"time"
)

type transcript struct {
	file    *os.File
	path    string
	binding store.CursorRuntime
	seq     uint64
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func openTranscript(home string, b store.CursorRuntime) (*transcript, error) {
	if !filepath.IsAbs(home) {
		return nil, fmt.Errorf("transcript home unavailable")
	}
	// Walk from / with NOFOLLOW at every component. No session id, input,
	// label or model-controlled path ever participates in a pathname.
	dir, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parts := append(splitPath(home), ".config", "agent-director", "cursor-transcripts")
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			unix.Close(dir)
			return nil, fmt.Errorf("unsafe transcript path")
		}
		next, e := unix.Openat(dir, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if e == unix.ENOENT && i >= len(parts)-3 {
			if e = unix.Mkdirat(dir, part, 0700); e == nil || e == unix.EEXIST {
				next, e = unix.Openat(dir, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			}
		}
		unix.Close(dir)
		if e != nil {
			return nil, fmt.Errorf("protected transcript directory unavailable")
		}
		dir = next
		if i >= len(parts)-2 {
			var st unix.Stat_t
			if e = unix.Fstat(dir, &st); e != nil || st.Uid != uint32(os.Getuid()) || (i == len(parts)-1 && st.Mode&0077 != 0) || (i < len(parts)-1 && st.Mode&0022 != 0) {
				unix.Close(dir)
				return nil, fmt.Errorf("transcript directory is not private")
			}
		}
	}
	defer unix.Close(dir)
	name, err := randomID()
	if err != nil {
		return nil, err
	}
	name += ".jsonl"
	fd, err := unix.Openat(dir, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, fmt.Errorf("protected transcript unavailable")
	}
	t := &transcript{file: os.NewFile(uintptr(fd), name), path: filepath.Join(home, ".config", "agent-director", "cursor-transcripts", name), binding: b}
	if err = t.record("launch", map[string]any{"agent": "cursor"}); err != nil {
		t.file.Close()
		return nil, err
	}
	return t, nil
}
func (t *transcript) record(kind string, data any) error {
	existing, err := openExistingTranscript(t.path)
	if err != nil {
		return err
	}
	expected, statErr := existing.Stat()
	closeErr := existing.Close()
	actual, actualErr := t.file.Stat()
	if statErr != nil || closeErr != nil || actualErr != nil || !os.SameFile(expected, actual) {
		return fmt.Errorf("Cursor transcript reference changed")
	}

	t.seq++
	record := map[string]any{"sequence": t.seq, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "kind": kind, "instance_id": t.binding.InstanceID, "life_number": t.binding.Life, "launch_token": t.binding.Token, "connection_id": t.binding.Connection, "session_id": t.binding.SessionID, "origin": "launch", "data": data}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	n, err := t.file.Write(raw)
	if err != nil || n != len(raw) {
		return fmt.Errorf("transcript write failed")
	}
	if err = t.file.Sync(); err != nil {
		return fmt.Errorf("transcript persistence failed")
	}
	return nil
}

// VerifyTranscriptPath rejects replaced/symlinked path components. Reports
// never turn an arbitrary path in a protocol/hook payload into a transcript.
func VerifyTranscriptPath(path string) error {
	f, err := openExistingTranscript(path)
	if err != nil {
		return err
	}
	return f.Close()
}
func openExistingTranscript(path string) (*os.File, error) {
	base := filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "cursor-transcripts")
	name := filepath.Base(path)
	if filepath.Dir(path) != base || len(name) != 38 || name[32:] != ".jsonl" {
		return nil, fmt.Errorf("invalid Cursor transcript reference")
	}
	if _, err := hex.DecodeString(name[:32]); err != nil {
		return nil, fmt.Errorf("invalid Cursor transcript reference")
	}
	dir, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("Cursor transcript unavailable")
	}
	for _, part := range splitPath(base) {
		if part == "" || part == "." || part == ".." {
			unix.Close(dir)
			return nil, fmt.Errorf("unsafe Cursor transcript path")
		}
		next, e := unix.Openat(dir, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(dir)
		if e != nil {
			return nil, fmt.Errorf("protected Cursor transcript unavailable")
		}
		dir = next
	}
	defer unix.Close(dir)
	var ds unix.Stat_t
	if err = unix.Fstat(dir, &ds); err != nil || ds.Uid != uint32(os.Getuid()) || ds.Mode&0077 != 0 {
		return nil, fmt.Errorf("Cursor transcript directory changed")
	}
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("Cursor transcript unavailable")
	}
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Getuid()) || st.Mode&0077 != 0 || st.Nlink != 1 {
		unix.Close(fd)
		return nil, fmt.Errorf("Cursor transcript is not protected")
	}
	return os.NewFile(uintptr(fd), name), nil
}
