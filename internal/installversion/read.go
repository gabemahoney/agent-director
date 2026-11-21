package installversion

import (
	"bytes"
	"context"
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
	return readBytesContext(context.Background(), path)
}

// contextReader checks cancellation between bounded reads. In particular it
// hides os.File's WriterTo so hashing cannot bypass these checks in io.Copy.
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) > 32*1024 {
		p = p[:32*1024]
	}
	n, err := r.r.Read(p)
	if ctxErr := r.ctx.Err(); ctxErr != nil {
		return n, ctxErr
	}
	return n, err
}

func readBytesContext(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := openRegular(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(contextReader{ctx, f}, metadataLimit+1))
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
	return fileRecordContext(context.Background(), path, name)
}

func fileRecordContext(ctx context.Context, path, name string) (FileRecord, error) {
	if err := ctx.Err(); err != nil {
		return FileRecord{}, err
	}
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
	size, err := io.Copy(h, contextReader{ctx, f})
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
	return readVersionContext(context.Background(), root, id, ready)
}

func readVersionContext(ctx context.Context, root, id string, ready bool) (InstalledVersion, string, error) {
	var v InstalledVersion
	if err := ctx.Err(); err != nil {
		return v, "", err
	}
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
	b, err := readBytesContext(ctx, filepath.Join(dir, layout.VersionRecord))
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
		marker, err := readBytesContext(ctx, filepath.Join(dir, layout.VersionReady))
		if err != nil {
			return v, "", err
		}
		if string(marker) != hash+"\n" {
			return v, "", invalid("ready marker does not match the version record")
		}
	}
	for _, want := range v.Files {
		got, err := fileRecordContext(ctx, filepath.Join(dir, want.Path), want.Path)
		if err != nil {
			return v, "", err
		}
		if got != want {
			return v, "", invalid("retained resource does not match its inventory")
		}
	}
	v.VersionID, v.Directory, v.RuntimeExecutable = id, dir, filepath.Join(dir, layout.BinaryName)
	return v, hash, ctx.Err()
}

// ListInstalled validates ready generations without inspecting current, the
// anchor, history, a database, or an installation lock. Missing roots and
// unfinished staging are empty discovery, never a request to initialize.
func ListInstalled(root string) ([]InstalledVersion, error) {
	versions, err := ListInstalledContext(context.Background(), root)
	sortVersions(versions)
	return versions, err
}

// ListInstalledContext performs the same lock-free validation, checking the
// caller's context between directory batches, versions and resource reads.
// Results are unordered: deadline-bound callers select by installed_sequence
// directly, without spending an uninterruptible sort on an unlimited history.
func ListInstalledContext(ctx context.Context, root string) ([]InstalledVersion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := canonicalRoot(root)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if errors.Is(err, os.ErrNotExist) {
		return []InstalledVersion{}, nil
	}
	if err != nil {
		return nil, err
	}
	return listVersionsContext(ctx, root, true)
}

func listVersions(root string, readyOnly bool) ([]InstalledVersion, error) {
	versions, err := listVersionsContext(context.Background(), root, readyOnly)
	sortVersions(versions)
	return versions, err
}

func listVersionsContext(ctx context.Context, root string, readyOnly bool) ([]InstalledVersion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir := filepath.Join(root, layout.VersionsDir)
	if err := checkDir(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []InstalledVersion{}, nil
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := openDir(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	versions := make([]InstalledVersion, 0)
	sequences := make(map[uint64]bool)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries, readErr := f.ReadDir(64)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, readErr
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
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
			v, _, err := readVersionContext(ctx, root, entry.Name(), ready)
			if err != nil {
				return nil, err
			}
			if sequences[v.InstalledSequence] {
				return nil, invalid("duplicate installed sequence")
			}
			sequences[v.InstalledSequence] = true
			versions = append(versions, v)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	return versions, ctx.Err()
}

func sortVersions(versions []InstalledVersion) {
	slices.SortFunc(versions, func(a, b InstalledVersion) int {
		if a.InstalledSequence < b.InstalledSequence {
			return -1
		}
		if a.InstalledSequence > b.InstalledSequence {
			return 1
		}
		return 0
	})
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
