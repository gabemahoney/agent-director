package contextfix

import (
	"bytes"
	"fmt"
	"testing"
)

// Padded adds synthetic JSON whitespace to a capture around a transcript size
// boundary, preserving every native record and its accounting/history meaning.
func Padded(t testing.TB, name, boundary string, offset int) Sample {
	t.Helper()
	s := Must(t, name)
	s.Name = fmt.Sprintf("synthetic-%s-%s-size%+d", name, boundary, offset)
	lines := bytes.Split(bytes.TrimSuffix(s.Transcript, []byte{'\n'}), []byte{'\n'})
	lineSize, fileSize := 4<<20, 64<<20
	switch boundary {
	case "line":
		lineSize += offset
		fileSize = len(s.Transcript) + lineSize - len(lines[0])
	case "file":
		fileSize += offset
	default:
		t.Fatalf("unknown transcript boundary %q", boundary)
	}
	remaining := fileSize - len(s.Transcript)
	var out bytes.Buffer
	out.Grow(fileSize)
	for _, line := range lines {
		out.Write(line)
		padding := min(remaining, lineSize-len(line))
		if padding < 0 {
			t.Fatal("capture record already exceeds requested boundary")
		}
		out.Write(bytes.Repeat([]byte{' '}, padding))
		out.WriteByte('\n')
		remaining -= padding
	}
	if remaining != 0 {
		t.Fatal("capture has too few records for requested file size")
	}
	s.Transcript = out.Bytes()
	return s
}
