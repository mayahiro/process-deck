package supervisor

type State string

const (
	StatePending  State = "pending"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateExited   State = "exited"
	StateFailed   State = "failed"
	StateSkipped  State = "skipped"
)

func terminalState(state State) bool {
	switch state {
	case StateExited, StateFailed, StateSkipped:
		return true
	default:
		return false
	}
}
