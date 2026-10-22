package app

import (
	"strconv"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/procutil"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

// logEnded names on the execution's log, by pid and executable, each process that ending the execution ended outside
// the process group of its harness or program, which a signal to the group would have missed (FS-003 FR-005).
func (r *run) logEnded(ended []procutil.Ended) {
	for _, e := range ended {
		if !e.OutsideGroup {
			continue
		}
		pid := strconv.Itoa(e.Pid)
		r.event(domain.Event{Type: domain.EventProcessEnded,
			Payload: map[string]any{"pid": e.Pid, "executable": e.Executable}}.Says(domain.T("process_ended",
			"ended {{executable}} (pid {{pid}}), which ran outside the process group", "executable", e.Executable,
			"pid", pid)))
	}
}
