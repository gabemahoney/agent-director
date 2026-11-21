package installversion

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	"github.com/gabemahoney/agent-director/internal/layout"
)

func (op *operation) stage(ctx context.Context, runtimeBinary, adminBinary string, facts Facts) (v InstalledVersion, err error) {
	parent := filepath.Join(op.root, layout.VersionsDir)
	staging, err := os.MkdirTemp(parent, ".staging-")
	if err != nil {
		return v, err
	}
	// staging is a private, newly created directory. After final-name rename
	// it no longer exists; no cleanup ever follows a final version path.
	defer func() { err = errors.Join(err, os.RemoveAll(staging)) }()
	files := make([]FileRecord, 0, 2)
	for i, source := range []string{runtimeBinary, adminBinary} {
		name := []string{layout.BinaryName, layout.AdminBinaryName}[i]
		if err := op.copyBinary(source, filepath.Join(staging, name), "stage."+name); err != nil {
			return v, err
		}
		file, err := fileRecord(filepath.Join(staging, name), name)
		if err != nil {
			return v, err
		}
		files = append(files, file)
	}
	// Ask only the copies whose bytes we retain. Querying the copied admin as
	// well binds its path to the running administrator, even if its invocation
	// was a mutable symlink that another publication changed after exec.
	for _, query := range []struct{ name, arg string }{
		{layout.BinaryName, RuntimeFactsArgument}, {layout.AdminBinaryName, "version-facts"},
	} {
		if err := op.boundary("before:stage.facts", filepath.Join(staging, query.name)); err != nil {
			return v, err
		}
		reported, err := queryFacts(ctx, filepath.Join(staging, query.name), query.arg)
		if err != nil {
			return v, err
		}
		if !reported.Equal(facts) {
			return v, problem{"version_conflict", "staged runtime and administrator do not report identical compiled facts"}
		}
		if err := op.boundary("after:stage.facts", filepath.Join(staging, query.name)); err != nil {
			return v, err
		}
	}
	anchor := contractFor(facts)
	if op.anchor != nil {
		anchor = *op.anchor
	}
	if err := compatible(facts, anchor, op.floor); err != nil {
		return v, err
	}
	// The agreed facts apply to both binaries. Also detect any modification
	// during the one-shot queries before naming or exposing this generation.
	for _, want := range files {
		got, err := fileRecord(filepath.Join(staging, want.Path), want.Path)
		if err != nil {
			return v, err
		}
		if got != want {
			return v, problem{"version_conflict", "staged executable changed during validation"}
		}
	}
	existing, err := listVersions(op.root, false)
	if err != nil {
		return v, err
	}
	sequence := uint64(1)
	for _, installed := range existing {
		if installed.Version == facts.Version {
			if !installed.Facts.Equal(facts) || !slices.Equal(installed.Files, files) {
				return v, problem{"version_conflict", "this version stamp already names different facts or executable bytes"}
			}
			op.result.Reused = true
			return installed, nil
		}
		if installed.InstalledSequence == math.MaxUint64 {
			return v, invalid("installed sequence exhausted")
		}
		if installed.InstalledSequence >= sequence {
			sequence = installed.InstalledSequence + 1
		}
	}
	record := VersionRecord{Facts: facts, InstalledSequence: sequence, Files: files}
	id := layout.VersionID(facts.Version)
	if err := record.validate(id); err != nil {
		return v, err
	}
	if err := op.writeAtomic(filepath.Join(staging, layout.VersionRecord), record, "stage.record", false); err != nil {
		return v, err
	}
	if err := op.syncDir(staging, "stage.directory-sync"); err != nil {
		return v, err
	}
	final := filepath.Join(parent, id)
	if _, err := os.Lstat(final); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return v, err
		}
		return v, problem{"version_conflict", "version directory already exists"}
	}
	if err := op.rename(staging, final, "stage.final-rename", nil); err != nil {
		return v, err
	}
	if err := op.syncDir(parent, "stage.final-parent-sync"); err != nil {
		return v, err
	}
	v, _, err = readVersion(op.root, id, false)
	return v, err
}

func (op *operation) copyBinary(source, destination, point string) error {
	// Candidate paths may be symlinks; resolve them once before opening. A
	// copied executable is never recopied after its semantic validation.
	source, err := filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	in, err := openRegular(source, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0100 == 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return invalid("candidate is not an ordinary executable")
	}
	out, err := openRegular(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Chmod(info.Mode().Perm())
	}
	if err == nil {
		err = op.syncFile(out, point+".file-sync")
	}
	return errors.Join(err, out.Close())
}

// limitedOutput bounds a malformed candidate's response without using a shell
// or an ordinary runtime command. Stderr is discarded (it can contain secrets).
type limitedOutput struct{ buffer bytes.Buffer }

func (b *limitedOutput) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > metadataLimit {
		return 0, problem{"version_conflict", "facts response exceeds limit"}
	}
	return b.buffer.Write(p)
}
func (b *limitedOutput) Bytes() []byte { return b.buffer.Bytes() }

func queryFacts(ctx context.Context, executable, argument string) (facts Facts, err error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, argument)
	// An older/incorrect candidate may not implement the handshake. Give it
	// no live-home configuration or inherited runtime targeting even on that
	// refusal path. The supported handshake itself never reads this home.
	home, err := os.MkdirTemp(filepath.Dir(executable), ".facts-home-")
	if err != nil {
		return Facts{}, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(home)) }()
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	var output limitedOutput
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	cmd.WaitDelay = time.Second
	// Launch and I/O failures do not establish an incompatible build. Only
	// a rejected handshake or invalid response is a permanent conflict.
	if err := cmd.Start(); err != nil {
		return facts, err
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return facts, ctx.Err()
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return facts, problem{"version_conflict", "staged executable rejected the compiled-facts handshake"}
		}
		return facts, err
	}
	if err := decode(output.Bytes(), &facts); err != nil {
		return facts, problem{"version_conflict", "staged executable returned malformed compiled facts"}
	}
	if err := facts.Validate(); err != nil {
		return facts, problem{"version_conflict", "staged executable returned invalid compiled facts"}
	}
	return facts, nil
}

func (op *operation) ensureReady(v InstalledVersion) error {
	if op.anchor == nil {
		return invalid("cannot expose a version without its permanent anchor")
	}
	verified, hash, err := readVersion(op.root, v.VersionID, false)
	if err != nil {
		return err
	}
	if err := compatible(verified.Facts, *op.anchor, op.floor); err != nil {
		return err
	}
	path := filepath.Join(verified.Directory, layout.VersionReady)
	if _, err := os.Lstat(path); err == nil {
		_, _, err = readVersion(op.root, v.VersionID, true)
		if err != nil {
			return err
		}
		return op.syncDir(verified.Directory, "ready.parent-sync")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// A previous process may have died between final-name rename and its
	// parent sync. Re-establish that durability before making it discoverable.
	if err := op.syncDir(verified.Directory, "ready.version-sync"); err != nil {
		return err
	}
	if err := op.syncDir(filepath.Dir(verified.Directory), "ready.final-parent-sync"); err != nil {
		return err
	}
	return op.writeBytesAtomic(path, []byte(hash+"\n"), "ready", false)
}
