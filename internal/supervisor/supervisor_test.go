package supervisor

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mayahiro/process-deck/internal/config"
)

func TestRunExitsSuccessfully(t *testing.T) {
	cfg := testConfig(map[string]config.Process{
		"one": {
			Cmd: "echo one",
		},
		"two": {
			Cmd:       "echo two",
			DependsOn: []string{"one"},
		},
	})

	events, err := runSupervisor(t, cfg, 0)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	if !hasEventLog(events, "one", "stdout", "one") {
		t.Fatalf("missing one stdout log in %#v", events)
	}
	if !hasEventLog(events, "two", "stdout", "two") {
		t.Fatalf("missing two stdout log in %#v", events)
	}
}

func TestRunReturnsErrorWhenProcessFails(t *testing.T) {
	cfg := testConfig(map[string]config.Process{
		"failer": {
			Cmd: "exit 7",
		},
	})

	_, err := runSupervisor(t, cfg, 0)
	if err == nil {
		t.Fatal("Run() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "one or more processes failed") {
		t.Fatalf("Run() error = %q, want process failure", err.Error())
	}
}

func TestRunSkipsDependentWhenDependencyFailsBeforeRunning(t *testing.T) {
	cfg := testConfig(map[string]config.Process{
		"missing": {
			Exec: []string{"/process-deck-missing-executable"},
		},
		"dependent": {
			Cmd:       "echo dependent",
			DependsOn: []string{"missing"},
		},
	})

	events, err := runSupervisor(t, cfg, 0)
	if err == nil {
		t.Fatal("Run() error = nil, want error")
	}
	if !hasEventKind(events, EventProcessSkipped, "dependent") {
		t.Fatalf("missing skipped event in %#v", events)
	}
}

func TestRunRestartsOnFailureUntilStopped(t *testing.T) {
	cfg := testConfig(map[string]config.Process{
		"failer": {
			Cmd:     "echo fail; exit 1",
			Restart: "on-failure",
			Backoff: "10ms",
		},
	})

	events, err := runSupervisor(t, cfg, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("Run() error = %v, want nil after context cancellation", err)
	}
	if !hasEventKind(events, EventProcessRestartScheduled, "failer") {
		t.Fatalf("missing restart scheduled event in %#v", events)
	}
	if countEventKind(events, EventProcessStarted, "failer") < 2 {
		t.Fatalf("process was not restarted; events = %#v", events)
	}
}

func TestStopProcessSuppressesRestart(t *testing.T) {
	cfg := testConfig(map[string]config.Process{
		"server": {
			Cmd:         "trap 'exit 0' TERM; while true; do sleep 1; done",
			Restart:     "always",
			StopTimeout: "1s",
		},
	})

	sup, err := New(cfg, Options{})
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}

	ctx := context.Background()
	errCh := make(chan error, 1)
	go func() {
		errCh <- sup.Run(ctx)
	}()

	var events []Event
	stopped := false
	for event := range sup.Events() {
		events = append(events, event)
		if event.Kind == EventProcessStarted && !stopped {
			stopped = true
			if err := sup.StopProcess("server"); err != nil {
				t.Fatalf("StopProcess() error = %v, want nil", err)
			}
		}
	}

	if err := <-errCh; err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if hasEventKind(events, EventProcessRestartScheduled, "server") {
		t.Fatalf("manual stop triggered restart; events = %#v", events)
	}
}

func TestRunCanStayOpenAfterAllProcessesExit(t *testing.T) {
	cfg := testConfig(map[string]config.Process{
		"one": {
			Cmd: "echo one",
		},
	})

	sup, err := New(cfg, Options{KeepRunningWhenDone: true})
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- sup.Run(ctx)
	}()

	var events []Event
	for event := range sup.Events() {
		events = append(events, event)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if !hasEventKind(events, EventProcessExited, "one") {
		t.Fatalf("missing process exited event in %#v", events)
	}
	if !hasEventKind(events, EventSupervisorStopped, "") {
		t.Fatalf("missing supervisor stopped event in %#v", events)
	}
}

func TestLogsRespectLogBufferLines(t *testing.T) {
	cfg := testConfig(map[string]config.Process{
		"spam": {
			Cmd:            "printf 'one\\ntwo\\nthree\\nfour\\n'",
			LogBufferLines: intPtr(2),
		},
	})

	sup, err := New(cfg, Options{})
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- sup.Run(context.Background())
	}()

	for range sup.Events() {
	}
	if err := <-errCh; err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	logs := sup.Logs("spam")
	got := make([]string, 0, len(logs))
	for _, entry := range logs {
		got = append(got, entry.Line)
	}
	want := []string{"three", "four"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Logs() lines = %#v, want %#v", got, want)
	}
}

func TestLogsCanBeDisabledWithZeroLogBufferLines(t *testing.T) {
	cfg := testConfig(map[string]config.Process{
		"spam": {
			Cmd:            "printf 'one\\ntwo\\n'",
			LogBufferLines: intPtr(0),
		},
	})

	sup, err := New(cfg, Options{})
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- sup.Run(context.Background())
	}()

	for range sup.Events() {
	}
	if err := <-errCh; err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if got := sup.Logs("spam"); got != nil {
		t.Fatalf("Logs() = %#v, want nil", got)
	}
}

func runSupervisor(t *testing.T, cfg *config.Config, timeout time.Duration) ([]Event, error) {
	t.Helper()

	sup, err := New(cfg, Options{})
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}

	ctx := context.Background()
	cancel := func() {}
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- sup.Run(ctx)
	}()

	var events []Event
	for event := range sup.Events() {
		events = append(events, event)
	}
	return events, <-errCh
}

func testConfig(processes map[string]config.Process) *config.Config {
	return &config.Config{
		Version: config.SchemaVersion,
		Defaults: config.Defaults{
			Restart:        "no",
			Backoff:        "1s",
			StopSignal:     "TERM",
			StopTimeout:    "1s",
			LogBufferLines: intPtr(100),
		},
		Processes: processes,
	}
}

func intPtr(v int) *int {
	return &v
}

func hasEventLog(events []Event, process string, stream string, line string) bool {
	for _, event := range events {
		if event.Kind == EventProcessLogLine && event.Process == process && event.Stream == stream && event.Line == line {
			return true
		}
	}
	return false
}

func hasEventKind(events []Event, kind EventKind, process string) bool {
	return countEventKind(events, kind, process) > 0
}

func countEventKind(events []Event, kind EventKind, process string) int {
	count := 0
	for _, event := range events {
		if event.Kind == kind && event.Process == process {
			count++
		}
	}
	return count
}
