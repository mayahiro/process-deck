package process

import (
	"testing"
	"time"
)

func TestRunnerCapturesStdoutAndStderr(t *testing.T) {
	runner := NewRunner(Spec{
		Cmd: "printf 'out\\n'; printf 'err\\n' >&2",
	})

	run, err := runner.Start()
	if err != nil {
		t.Fatalf("Start() error = %v, want nil", err)
	}

	var logs []LogLine
	for line := range run.Logs {
		logs = append(logs, line)
	}
	result := <-run.Done
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", result.ExitCode)
	}

	if !hasLog(logs, "stdout", "out") {
		t.Fatalf("stdout log missing from %#v", logs)
	}
	if !hasLog(logs, "stderr", "err") {
		t.Fatalf("stderr log missing from %#v", logs)
	}
}

func TestRunnerStopTerminatesProcessGroup(t *testing.T) {
	runner := NewRunner(Spec{
		Cmd:         "trap 'exit 0' TERM; while true; do sleep 1; done",
		StopTimeout: time.Second,
	})

	run, err := runner.Start()
	if err != nil {
		t.Fatalf("Start() error = %v, want nil", err)
	}

	if err := runner.Stop(); err != nil {
		t.Fatalf("Stop() error = %v, want nil", err)
	}

	select {
	case <-run.Done:
	case <-time.After(2 * time.Second):
		t.Fatal("process did not exit after Stop()")
	}
}

func hasLog(logs []LogLine, stream string, line string) bool {
	for _, log := range logs {
		if log.Stream == stream && log.Line == line {
			return true
		}
	}
	return false
}
