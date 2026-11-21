//go:build linux || darwin

package installversion

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// durableImage models loss of unsynced data and directory entries. Real Sync
// runs first; a directory sync commits names, a regular-file sync commits bytes.
// This is a conservative storage-fault model, not hardware power-loss evidence.
type durableImage struct {
	t     testing.TB
	base  string
	root  *durableNode
	nodes map[string]*durableNode
}

type durableNode struct {
	mode     os.FileMode
	data     []byte
	children map[string]*durableNode
}

func newDurableImage(t testing.TB, base string) *durableImage {
	t.Helper()
	d := &durableImage{t: t, base: base, nodes: map[string]*durableNode{}}
	d.root = d.remember(base, true)
	return d
}

func (d *durableImage) remember(path string, initial bool) *durableNode {
	info, err := os.Lstat(path)
	must(d.t, err)
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		must(d.t, err)
		return &durableNode{mode: info.Mode(), data: []byte(target)}
	}
	stat := info.Sys().(*syscall.Stat_t)
	key := fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
	if n := d.nodes[key]; n != nil {
		return n
	}
	n := &durableNode{mode: info.Mode(), children: map[string]*durableNode{}}
	d.nodes[key] = n
	// Hold observed inodes open so a removed temporary cannot reuse an inode
	// still referenced by the modeled durable namespace.
	f, err := os.Open(path)
	must(d.t, err)
	d.t.Cleanup(func() { _ = f.Close() })
	if initial {
		d.commit(path, n, true)
	}
	return n
}

func (d *durableImage) commit(path string, n *durableNode, initial bool) {
	info, err := os.Lstat(path)
	must(d.t, err)
	n.mode = info.Mode()
	if !info.IsDir() {
		n.data = append([]byte(nil), bytesAt(d.t, path)...)
		return
	}
	entries, err := os.ReadDir(path)
	must(d.t, err)
	n.children = map[string]*durableNode{}
	for _, entry := range entries {
		n.children[entry.Name()] = d.remember(filepath.Join(path, entry.Name()), initial)
	}
}

func (d *durableImage) sync(file *os.File) error {
	if err := file.Sync(); err != nil {
		return err
	}
	path := file.Name()
	rel, err := filepath.Rel(d.base, path)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil
	}
	d.commit(path, d.remember(path, false), false)
	return nil
}

func (d *durableImage) restore(base string) {
	var write func(string, *durableNode)
	write = func(path string, n *durableNode) {
		switch {
		case n.mode.IsDir():
			must(d.t, os.MkdirAll(path, 0700))
			for name, child := range n.children {
				write(filepath.Join(path, name), child)
			}
			must(d.t, os.Chmod(path, n.mode.Perm()))
		case n.mode&os.ModeSymlink != 0:
			must(d.t, os.Symlink(string(n.data), path))
		default:
			must(d.t, os.WriteFile(path, n.data, n.mode.Perm()))
		}
	}
	write(base, d.root)
}
