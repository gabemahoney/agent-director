package adminapi

import (
	"bytes"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// lsofFile is one file set of an `lsof -F0pftDin` listing: its FD, type,
// device, inode and name, each only when the listing gave it.
type lsofFile struct {
	fd, typ, name  string
	hasType        bool
	hasDev, hasIno bool
	id             journalFileID
}

// errLsofUnparseable is a listing the parser cannot read as process and file
// sets; the verifier refuses it as process_inventory_unreadable.
var errLsofUnparseable = errors.New("the lsof listing cannot be parsed")

// parseLsofListing reads `lsof -F0pftDin` output: with 0, every field ends in
// NUL and every process or file set ends in NL. The first byte of a field
// names it: p (pid; starts a process set), f (FD; starts a file set), t
// (type), D (device, 0x-hex), i (inode, decimal) and n (name); other letters
// are ignored. A file field before any p, a bad pid or a repeated pid is
// unparseable; an unparseable D or i counts as missing. lsof escapes
// non-printable bytes in a name, so a name is used only for lookups whose
// result is checked by device and inode.
func parseLsofListing(out []byte) (map[int][]lsofFile, error) {
	listing := map[int][]lsofFile{}
	pid := 0
	var file *lsofFile
	endFile := func() {
		if file != nil {
			listing[pid] = append(listing[pid], *file)
			file = nil
		}
	}
	for _, token := range bytes.Split(out, []byte{0}) {
		field := string(bytes.TrimPrefix(token, []byte{'\n'}))
		if field == "" {
			continue
		}
		value := field[1:]
		switch field[0] {
		case 'p':
			endFile()
			n, err := strconv.Atoi(value)
			if err != nil || n <= 0 {
				return nil, errLsofUnparseable
			}
			if _, seen := listing[n]; seen {
				return nil, errLsofUnparseable
			}
			pid = n
			listing[pid] = nil
		case 'f':
			if pid == 0 {
				return nil, errLsofUnparseable
			}
			endFile()
			file = &lsofFile{fd: strings.Trim(value, " ")}
		case 't', 'D', 'i', 'n':
			if file == nil {
				return nil, errLsofUnparseable
			}
			switch field[0] {
			case 't':
				file.typ, file.hasType = value, true
			case 'D':
				hex, found := strings.CutPrefix(value, "0x")
				dev, err := strconv.ParseUint(hex, 16, 32)
				file.id.dev, file.hasDev = dev, found && err == nil
			case 'i':
				ino, err := strconv.ParseUint(value, 10, 64)
				file.id.ino, file.hasIno = ino, err == nil
			case 'n':
				file.name = value
			}
		}
	}
	endFile()
	return listing, nil
}

// lsofFDClass is what an FD value says about its entry, in Apple's Darwin
// dialect (apple-oss-distributions/lsof dialects/darwin/libproc/dproc.c).
type lsofFDClass int

const (
	// lsofFDDirectory: cwd, rtd and twd (a thread's working directory), never
	// store or root evidence.
	lsofFDDirectory lsofFDClass = iota
	// lsofFDText: txt, program text and mapped files; the first is the image.
	lsofFDText
	// lsofFDInfoError: err, an entry lsof could not read, such as "FD info
	// error" or "FILEPORT info error".
	lsofFDInfoError
	// lsofFDDescriptor: a decimal descriptor number, or fp. (a fileport, which
	// can carry a vnode).
	lsofFDDescriptor
	// lsofFDUnknown: any other FD value.
	lsofFDUnknown
)

var lsofDescriptorFD = regexp.MustCompile(`^([0-9]+|fp\..*)$`)

func lsofFDClassOf(fd string) lsofFDClass {
	switch {
	case fd == "cwd", fd == "rtd", fd == "twd":
		return lsofFDDirectory
	case fd == "txt":
		return lsofFDText
	case fd == "err":
		return lsofFDInfoError
	case lsofDescriptorFD.MatchString(fd):
		return lsofFDDescriptor
	}
	return lsofFDUnknown
}

// lsofVnodeTypes are the TYPE values of file-system entries in lsof's Darwin
// dialect (dfile.c enter_vnode_info); every other vnode type is printed as a
// 4-digit octal string, which lsofOctalVnodeType matches. A vnode entry
// without a device or inode is tested through its gated, identity-confirmed
// path instead.
var lsofVnodeTypes = []string{"REG", "DIR", "CHR", "BLK", "FIFO", "LINK"}

var lsofOctalVnodeType = regexp.MustCompile(`^[0-7]{4}$`)

// lsofNonVnodeTypes are the TYPE values that are neither store nor root
// evidence (dsock.c, dproc.c): sockets of every family (sock keeps an unknown
// family), pipes, kqueues, POSIX semaphores and shared memory, file-system
// event descriptors (FSEVENTS is Apple's 4.91 spelling, FSEVENT upstream's),
// network policies and channels.
var lsofNonVnodeTypes = []string{
	"sock", "IPv4", "IPv6", "unix", "rte", "ndrv", "key", "systm", "ppp", "vsock", "vsockp",
	"PIPE", "KQUEUE", "PSXSEM", "PSXSHM", "FSEVENTS", "FSEVENT", "NPOLICY", "ATALK", "CHAN", "NEXUS",
}

// lsofKind is an entry's descriptor kind.
type lsofKind int

const (
	lsofKindVnode lsofKind = iota
	lsofKindNonVnode
	// lsofKindUnknown: no type (Apple's lsof gives none for a revoked vnode, an
	// EPERM vnode, a region info error or an unknown descriptor kind), or a
	// type in neither table.
	lsofKindUnknown
)

func lsofKindOf(f lsofFile) lsofKind {
	switch {
	case !f.hasType:
		return lsofKindUnknown
	case slices.Contains(lsofVnodeTypes, f.typ), lsofOctalVnodeType.MatchString(f.typ):
		return lsofKindVnode
	case slices.Contains(lsofNonVnodeTypes, f.typ):
		return lsofKindNonVnode
	}
	return lsofKindUnknown
}
