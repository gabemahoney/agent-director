// Command check-lcuuid reports whether each Mach-O file has an LC_UUID load
// command, which macOS 26's dyld requires before it loads a binary (b.jh2).
//
// It prints one line per file:
//
//	PASS <path> LC_UUID=<hex> minos=<x.y.z>
//	FAIL <path> LC_UUID=none minos=<x.y.z>
//	ERROR <path>: <reason>
//
// minos is the LC_BUILD_VERSION minimum macOS, or "none" without one.
//
// Usage:
//
//	go run ./tools/check-lcuuid <mach-o file>...
//
// Exit status: 0 if every file has LC_UUID, 1 if any lacks it and all could
// be read, 2 on a usage error or a file that is not a readable thin Mach-O.
package main

import (
	"debug/macho"
	"encoding/hex"
	"fmt"
	"os"
)

const (
	lcUUID         = 0x1b
	lcBuildVersion = 0x32

	// uuid_command: cmd, cmdsize, then the 16-byte uuid.
	uuidEnd = 24
	// build_version_command: cmd, cmdsize, platform, then minos.
	minosEnd = 16
)

const (
	exitMissing = 1
	exitError   = 2
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: check-lcuuid <mach-o file>...")
		os.Exit(exitError)
	}
	status := 0
	for _, path := range os.Args[1:] {
		uuid, minos, err := inspect(path)
		switch {
		case err != nil:
			fmt.Printf("ERROR %s: %v\n", path, err)
			status = exitError
		case uuid == "":
			fmt.Printf("FAIL %s LC_UUID=none minos=%s\n", path, minos)
			if status == 0 {
				status = exitMissing
			}
		default:
			fmt.Printf("PASS %s LC_UUID=%s minos=%s\n", path, uuid, minos)
		}
	}
	os.Exit(status)
}

// inspect returns path's LC_UUID as hex ("" without one) and its minimum
// macOS ("none" without LC_BUILD_VERSION).
func inspect(path string) (uuid, minos string, err error) {
	f, err := macho.Open(path)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	minos = "none"
	for _, l := range f.Loads {
		raw := l.Raw()
		switch f.ByteOrder.Uint32(raw[0:4]) {
		case lcUUID:
			if len(raw) < uuidEnd {
				return "", "", fmt.Errorf("LC_UUID is %d bytes, want %d", len(raw), uuidEnd)
			}
			uuid = hex.EncodeToString(raw[8:uuidEnd])
		case lcBuildVersion:
			if len(raw) < minosEnd {
				return "", "", fmt.Errorf("LC_BUILD_VERSION is %d bytes, want at least %d", len(raw), minosEnd)
			}
			v := f.ByteOrder.Uint32(raw[12:minosEnd])
			minos = fmt.Sprintf("%d.%d.%d", v>>16, (v>>8)&0xff, v&0xff)
		}
	}
	return uuid, minos, nil
}
