//go:build linux

package cursorhost

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
)

func procStatus(pid int, key string) (int, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 && parts[0] == key+":" {
			return strconv.Atoi(parts[1])
		}
	}
	return 0, fmt.Errorf("process evidence unavailable")
}
func pipeAt(pid, fd int) (string, error) {
	link, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%d", pid, fd))
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(link, "pipe:[") {
		return "", fmt.Errorf("ACP connection is not a pipe")
	}
	return link, nil
}

// VerifyRuntime independently reads start time, immediate parent, tracer and
// both ends of the registered pipe pair. A supplied connection claim alone
// never grants reporting authority. Terminated means positive process-gone
// evidence, not EOF or an unreadable probe.
func VerifyRuntime(b store.CursorRuntime, terminated bool) error {
	pc := probe.NewProcChecker()
	start, alive, known := pc.StartTime(b.GatewayPID)
	if !known || !alive || start != b.GatewayStart {
		return fmt.Errorf("gateway identity unavailable")
	}
	in, err := pipeAt(b.GatewayPID, b.InputFD)
	if err != nil || in != b.InputPipe {
		return fmt.Errorf("ACP input binding changed")
	}
	out, err := pipeAt(b.GatewayPID, b.OutputFD)
	if err != nil || out != b.OutputPipe {
		return fmt.Errorf("ACP output binding changed")
	}
	start, alive, known = pc.StartTime(b.RuntimePID)
	if terminated {
		if !known || (alive && start == b.RuntimeStart) {
			return fmt.Errorf("Cursor termination is not proven")
		}
		return nil
	}
	if !known || !alive || start != b.RuntimeStart {
		return fmt.Errorf("Cursor runtime identity unavailable")
	}
	parent, ok := probe.NewParentPIDReader().PPID(b.RuntimePID)
	if !ok || parent != b.GatewayPID {
		return fmt.Errorf("Cursor runtime parent changed")
	}
	tracer, err := procStatus(b.RuntimePID, "TracerPid")
	if err != nil || tracer <= 0 {
		return fmt.Errorf("Cursor lifetime is unprotected")
	}
	tgid, err := procStatus(tracer, "Tgid")
	if err != nil || tgid != b.GatewayPID {
		return fmt.Errorf("Cursor lifetime protector changed")
	}
	in, err = pipeAt(b.RuntimePID, 0)
	if err != nil || in != b.InputPipe {
		return fmt.Errorf("Cursor input owner changed")
	}
	out, err = pipeAt(b.RuntimePID, 1)
	if err != nil || out != b.OutputPipe {
		return fmt.Errorf("Cursor output owner changed")
	}
	return nil
}

func processPipe(pid, fd int) (string, error) { return pipeAt(pid, fd) }
