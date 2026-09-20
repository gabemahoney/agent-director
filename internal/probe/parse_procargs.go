package probe

import "encoding/binary"

// envFromProcArgs2 parses a KERN_PROCARGS2 blob and returns just the env
// section (NUL-separated KEY=VAL entries). Returns (nil, false) on a too-short
// blob. The KERN_PROCARGS2 layout (per the XNU sources) is:
//
//	uint32  argc                            // 4 bytes, native byte order
//	string  exec_path '\0'                  // null-terminated
//	<pad>                                   // pad to word alignment
//	string  argv[0..argc-1], each '\0'-terminated
//	string  envp[0..], each '\0'-terminated, list ends at empty string
//
// It skips past argc + exec_path + argv to reach envp. This parser is
// build-tag-free (pure byte handling, no sysctl) so both the darwin Prober and
// the darwin LivenessChecker verdict logic are unit-testable off-darwin against
// synthetic blobs.
func envFromProcArgs2(blob []byte) ([]byte, bool) {
	if len(blob) < 4 {
		return nil, false
	}
	argc := int(binary.LittleEndian.Uint32(blob[:4]))
	if argc < 0 {
		return nil, false
	}

	// Skip past the 4-byte argc, the exec_path (null-terminated), any
	// padding (the exec_path is followed by enough NULs to align to
	// argv start, but in practice the parser just hops to the next
	// non-NUL byte), and argc argv entries.
	i := 4

	// Walk the exec_path until the first NUL. Then skip any additional
	// NULs (alignment padding before argv[0]).
	for i < len(blob) && blob[i] != 0 {
		i++
	}
	for i < len(blob) && blob[i] == 0 {
		i++
	}

	// Skip argc argv strings.
	for j := 0; j < argc && i < len(blob); j++ {
		for i < len(blob) && blob[i] != 0 {
			i++
		}
		// consume the NUL
		if i < len(blob) {
			i++
		}
	}
	if i >= len(blob) {
		return nil, false
	}
	return blob[i:], true
}
