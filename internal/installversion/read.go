package installversion

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/gabemahoney/agent-director/internal/layout"
)

const metadataLimit = 1024 * 1024

func decode(data []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return invalid("malformed installation metadata")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return invalid("trailing installation metadata")
	}
	return nil
}

func readBytes(path string) ([]byte, error) {
	f, err := openRegular(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(f, metadataLimit+1))
	err = errors.Join(err, f.Close())
	if err != nil {
		return nil, err
	}
	if len(b) > metadataLimit {
		return nil, invalid("installation metadata exceeds its limit")
	}
	return b, nil
}

func readJSON(path string, value any) error {
	b, err := readBytes(path)
	if err != nil {
		return err
	}
	return decode(b, value)
}

func digest(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

func fileRecord(path, name string) (FileRecord, error) {
	f, err := openRegular(path, os.O_RDONLY, 0)
	if err != nil {
		return FileRecord{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return FileRecord{}, err
	}
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return FileRecord{}, err
	}
	if size != info.Size() {
		return FileRecord{}, invalid("retained resource changed during observation")
	}
	return FileRecord{name, fmt.Sprintf("%x", h.Sum(nil)), size, uint32(info.Mode().Perm())}, nil
}

// canonicalRoot resolves caller-supplied ancestors once. Every managed child
// is checked without following symlinks; no reader creates a missing root.
func canonicalRoot(root string) (string, error) {
	if root == "" {
		return "", invalid("installation root is empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	if err := checkDir(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

func readVersion(root, id string, ready bool) (InstalledVersion, string, error) {
	var v InstalledVersion
	if !validDigest(id) {
		return v, "", invalid("invalid version directory name")
	}
	dir := filepath.Join(root, layout.VersionsDir, id)
	if err := checkDir(filepath.Join(root, layout.VersionsDir)); err != nil {
		return v, "", err
	}
	if err := checkDir(dir); err != nil {
		return v, "", err
	}
	b, err := readBytes(filepath.Join(dir, layout.VersionRecord))
	if err != nil {
		return v, "", err
	}
	if err := decode(b, &v.VersionRecord); err != nil {
		return v, "", err
	}
	if err := v.VersionRecord.validate(id); err != nil {
		return v, "", err
	}
	hash := digest(b)
	if ready {
		marker, err := readBytes(filepath.Join(dir, layout.VersionReady))
		if err != nil {
			return v, "", err
		}
		if string(marker) != hash+"\n" {
			return v, "", invalid("ready marker does not match the version record")
		}
	}
	for _, want := range v.Files {
		got, err := fileRecord(filepath.Join(dir, want.Path), want.Path)
		if err != nil {
			return v, "", err
		}
		if got != want {
			return v, "", invalid("retained resource does not match its inventory")
		}
	}
	v.VersionID, v.Directory, v.RuntimeExecutable = id, dir, filepath.Join(dir, layout.BinaryName)
	return v, hash, nil
}

// ListInstalled validates ready generations without inspecting current, the
// anchor, history, a database, or an installation lock. Missing roots and
// unfinished staging are empty discovery, never a request to initialize.
func ListInstalled(root string) ([]InstalledVersion, error) {
	root, err := canonicalRoot(root)
	if errors.Is(err, os.ErrNotExist) {
		return []InstalledVersion{}, nil
	}
	if err != nil {
		return nil, err
	}
	return listVersions(root, true)
}

func listVersions(root string, readyOnly bool) ([]InstalledVersion, error) {
	dir := filepath.Join(root, layout.VersionsDir)
	if err := checkDir(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []InstalledVersion{}, nil
		}
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	versions := make([]InstalledVersion, 0, len(entries))
	sequences := make(map[uint64]bool)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".staging-") {
			continue
		}
		if !validDigest(entry.Name()) || !entry.IsDir() {
			return nil, invalid("unexpected retained-version entry")
		}
		_, markerErr := os.Lstat(filepath.Join(dir, entry.Name(), layout.VersionReady))
		ready := markerErr == nil
		if errors.Is(markerErr, os.ErrNotExist) {
			if readyOnly {
				continue
			}
		} else if markerErr != nil {
			return nil, markerErr
		}
		v, _, err := readVersion(root, entry.Name(), ready)
		if err != nil {
			return nil, err
		}
		if sequences[v.InstalledSequence] {
			return nil, invalid("duplicate installed sequence")
		}
		sequences[v.InstalledSequence] = true
		versions = append(versions, v)
	}
	slices.SortFunc(versions, func(a, b InstalledVersion) int {
		if a.InstalledSequence < b.InstalledSequence {
			return -1
		}
		if a.InstalledSequence > b.InstalledSequence {
			return 1
		}
		return 0
	})
	return versions, nil
}

// ReadPublished observes a single current link separately from discovery. A
// concurrent publisher can change it immediately afterwards. Nil means absent.
func ReadPublished(root string) (*InstalledVersion, error) {
	root, err := canonicalRoot(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	id, err := readCurrentID(root)
	if err != nil || id == "" {
		return nil, err
	}
	v, _, err := readVersion(root, id, true)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func readCurrentID(root string) (string, error) {
	path := filepath.Join(root, layout.CurrentVersion)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink == 0 || !owned(info) {
		return "", invalid("current is not an owned version link")
	}
	target, err := os.Readlink(path)
	if err != nil {
		return "", err
	}
	id := strings.TrimPrefix(target, layout.VersionsDir+"/")
	if !validDigest(id) || target != layout.VersionsDir+"/"+id {
		return "", invalid("current escapes the retained layout")
	}
	return id, nil
}

// ReadContract and ReadInitialization parse evidence without repairing it.
func ReadContract(root string) (Contract, error) {
	var c Contract
	root, err := canonicalRoot(root)
	if err != nil {
		return c, err
	}
	if err := readJSON(filepath.Join(root, layout.InstallContract), &c); err != nil {
		return c, err
	}
	return c, c.validate()
}

func ReadInitialization(root string) (Initialization, error) {
	var i Initialization
	root, err := canonicalRoot(root)
	if err != nil {
		return i, err
	}
	if err := readJSON(filepath.Join(root, layout.InstallInitialization), &i); err != nil {
		return i, err
	}
	return i, i.validate()
}
