package supervisor

import "time"

type EventKind string

const (
	EventProcessStateChanged     EventKind = "process_state_changed"
	EventProcessStarted          EventKind = "process_started"
	EventProcessExited           EventKind = "process_exited"
	EventProcessRestartScheduled EventKind = "process_restart_scheduled"
	EventProcessLogLine          EventKind = "process_log_line"
	EventProcessSkipped          EventKind = "process_skipped"
	EventSupervisorError         EventKind = "supervisor_error"
	EventSupervisorStopped       EventKind = "supervisor_stopped"
)

type Event struct {
	Kind     EventKind
	Process  string
	Stream   string
	Line     string
	State    State
	PID      int
	Restarts int
	ExitCode *int
	Error    error
	Time     time.Time
}
