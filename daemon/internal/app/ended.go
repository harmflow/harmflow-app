package app

import (
	"strconv"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/procutil"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

// logEnded names on the execution's log, by pid and executable, each process that ending the attempt ended and that the
// harness or program left running when it exited on its own, and each one it ended outside the process group of its
// harness or program, which a signal to the group would have missed.
func (r *run) logEnded(ended []procutil.Ended) {
	for _, e := range ended {
		pid := strconv.Itoa(e.Pid)
		var text domain.Text
		switch {
		case e.LeftRunning:
			text = domain.T("process_left_ended", "ended {{executable}} (pid {{pid}}), which the harness or program left running",
				"executable", e.Executable, "pid", pid)
		case e.OutsideGroup:
			text = domain.T("process_ended", "ended {{executable}} (pid {{pid}}), which ran outside the process group",
				"executable", e.Executable, "pid", pid)
		default:
			continue
		}
		r.event(domain.Event{Type: domain.EventProcessEnded,
			Payload: map[string]any{"pid": e.Pid, "executable": e.Executable}}.Says(text))
	}
}
