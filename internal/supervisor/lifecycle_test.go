package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/mayahiro/process-deck/internal/config"
)

func TestManualOperationsCancelPendingRestart(t *testing.T) {
	for _, operation := range []string{"stop", "start", "restart"} {
		t.Run(operation, func(t *testing.T) {
			cfg := testConfig(map[string]config.Process{"worker": {
				Cmd: "if [ ! -f first ]; then touch first; exit 1; fi; echo $$ >> starts; exec sleep 30",
				CWD: t.TempDir(), Restart: "on-failure", Backoff: "150ms", StopTimeout: "100ms",
			}})
			sup, journal := startControlledSupervisor(t, cfg)
			awaitCondition(t, func() bool {
				return hasEventKind(journal.snapshot(), EventProcessRestartScheduled, "worker")
			})
			var err error
			switch operation {
			case "stop":
				err = sup.StopProcess("worker")
			case "start":
				err = sup.StartProcess("worker")
			case "restart":
				err = sup.RestartProcess("worker")
			}
			if err != nil {
				t.Fatal(err)
			}
			if operation != "stop" {
				awaitCondition(t, func() bool {
					return fileExists(filepath.Join(cfg.Processes["worker"].CWD, "starts"))
				})
			}
			// Observe beyond the original timer, including a second launch's output.
			time.Sleep(250 * time.Millisecond)
			wantStarts := 1
			wantState := StateExited
			if operation != "stop" {
				wantStarts, wantState = 2, StateRunning
				data, err := os.ReadFile(filepath.Join(cfg.Processes["worker"].CWD, "starts"))
				if err != nil || len(strings.Fields(string(data))) != 1 {
					t.Fatalf("spawned PIDs = %q, error = %v", data, err)
				}
			}
			if got := countEventKind(journal.snapshot(), EventProcessStarted, "worker"); got != wantStarts {
				t.Fatalf("start events = %d, want %d", got, wantStarts)
			}
			if snapshot := sup.Snapshot()[0]; snapshot.State != wantState {
				t.Fatalf("snapshot = %+v", snapshot)
			}
		})
	}
}

func TestStopCascadesToTransitiveDependents(t *testing.T) {
	sup, journal := startControlledSupervisor(t, dependencyTestConfig())
	awaitRunning(t, sup, 5)
	unrelatedPID := snapshotByName(sup, "unrelated").PID
	if err := sup.StopProcess("api"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"api", "worker", "leaf", "inactive"} {
		if snapshot := snapshotByName(sup, name); snapshot.State != StateExited || snapshot.PID != 0 {
			t.Fatalf("dependent left running: %+v", snapshot)
		}
	}
	if got := snapshotByName(sup, "unrelated"); got.PID != unrelatedPID || got.State != StateRunning {
		t.Fatalf("unrelated process changed: %+v", got)
	}
	assertEventOrder(t, journal.snapshot(), EventProcessStateChanged, StateStopping, []string{"leaf", "worker", "inactive", "api"})
	if err := sup.StartProcess("worker"); err == nil {
		t.Fatal("dependent started while its dependency was manually stopped")
	}
}

func TestRestartRestoresOnlyPreviouslyActiveDependents(t *testing.T) {
	sup, journal := startControlledSupervisor(t, dependencyTestConfig())
	awaitRunning(t, sup, 5)
	if err := sup.StopProcess("inactive"); err != nil {
		t.Fatal(err)
	}
	awaitCondition(t, func() bool { return hasEventKind(journal.snapshot(), EventProcessExited, "inactive") })
	before := sup.Snapshot()
	offset := len(journal.snapshot())
	if err := sup.RestartProcess("api"); err != nil {
		t.Fatal(err)
	}
	for _, previous := range before {
		current := snapshotByName(sup, previous.Name)
		switch previous.Name {
		case "inactive", "unrelated":
			if current.PID != previous.PID || current.State != previous.State {
				t.Fatalf("unexpectedly changed %s: %+v", previous.Name, current)
			}
		default:
			if current.State != StateRunning || current.PID == previous.PID {
				t.Fatalf("process did not restart: %+v", current)
			}
			if err := syscall.Kill(previous.PID, 0); err != syscall.ESRCH {
				t.Fatalf("old PID %d remains: %v", previous.PID, err)
			}
		}
	}
	awaitCondition(t, func() bool { return countEventKind(journal.snapshot(), EventProcessStarted, "leaf") == 2 })
	events := journal.snapshot()[offset:]
	assertEventOrder(t, events, EventProcessStateChanged, StateStopping, []string{"leaf", "worker", "api"})
	assertEventOrder(t, events, EventProcessStarted, StateRunning, []string{"api", "worker", "leaf"})
}

func TestRestartFailureLeavesDependentsStopped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "environment")
	if err := os.WriteFile(path, []byte("READY=1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(map[string]config.Process{
		"api":    {Exec: []string{"sleep", "30"}, EnvFile: []string{path}},
		"worker": {Exec: []string{"sleep", "30"}, DependsOn: []string{"api"}},
	})
	sup, _ := startControlledSupervisor(t, cfg)
	awaitRunning(t, sup, 2)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := sup.RestartProcess("api"); err == nil {
		t.Fatal("expected missing env_file error")
	}
	if got := snapshotByName(sup, "worker"); got.PID != 0 || got.State != StateExited {
		t.Fatalf("dependent restarted despite dependency failure: %+v", got)
	}
}

func TestConcurrentStartOnlyLaunchesOneProcess(t *testing.T) {
	sup, journal := startControlledSupervisor(t, testConfig(map[string]config.Process{"worker": {Exec: []string{"sleep", "30"}}}))
	awaitRunning(t, sup, 1)
	if err := sup.StopProcess("worker"); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 8)
	for range cap(results) {
		go func() { results <- sup.StartProcess("worker") }()
	}
	successes := 0
	for range cap(results) {
		if err := <-results; err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful starts = %d, want 1", successes)
	}
	awaitCondition(t, func() bool { return countEventKind(journal.snapshot(), EventProcessStarted, "worker") == 2 })
}

func TestHeadlessSupervisorRemainsControllableAfterManualRestart(t *testing.T) {
	sup, journal := startSupervisorWithOptions(t, testConfig(map[string]config.Process{
		"worker": {Exec: []string{"sleep", "30"}},
	}), Options{})
	awaitRunning(t, sup, 1)
	if err := sup.RestartProcess("worker"); err != nil {
		t.Fatal(err)
	}
	// Restart briefly leaves every process terminal. A headless supervisor must
	// still service cancellation and drain the replacement run during cleanup.
	awaitCondition(t, func() bool { return countEventKind(journal.snapshot(), EventProcessStarted, "worker") == 2 })
	if got := sup.Snapshot()[0]; got.State != StateRunning || got.PID == 0 {
		t.Fatalf("replacement not running: %+v", got)
	}
}

func dependencyTestConfig() *config.Config {
	return testConfig(map[string]config.Process{
		"api":       {Exec: []string{"sleep", "30"}},
		"worker":    {Exec: []string{"sleep", "30"}, DependsOn: []string{"api"}},
		"leaf":      {Exec: []string{"sleep", "30"}, DependsOn: []string{"worker"}},
		"inactive":  {Exec: []string{"sleep", "30"}, DependsOn: []string{"api"}},
		"unrelated": {Exec: []string{"sleep", "30"}},
	})
}

type eventJournal struct {
	mu     sync.Mutex
	events []Event
}

func (j *eventJournal) snapshot() []Event {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]Event(nil), j.events...)
}

func startControlledSupervisor(t *testing.T, cfg *config.Config) (*Supervisor, *eventJournal) {
	t.Helper()
	return startSupervisorWithOptions(t, cfg, Options{KeepRunningWhenDone: true})
}

func startSupervisorWithOptions(t *testing.T, cfg *config.Config, options Options) (*Supervisor, *eventJournal) {
	t.Helper()
	sup, err := New(cfg, options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	journal := &eventJournal{}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for event := range sup.Events() {
			journal.mu.Lock()
			journal.events = append(journal.events, event)
			journal.mu.Unlock()
		}
	}()
	finished := make(chan error, 1)
	go func() { finished <- sup.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-finished:
			if err != nil {
				t.Error(err)
			}
			<-drained
		case <-time.After(10 * time.Second):
			t.Error("supervisor did not stop")
			for _, snapshot := range sup.Snapshot() {
				if snapshot.PID > 0 {
					_ = syscall.Kill(-snapshot.PID, syscall.SIGKILL)
				}
			}
		}
	})
	return sup, journal
}

func awaitCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not met")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func awaitRunning(t *testing.T, sup *Supervisor, count int) {
	t.Helper()
	awaitCondition(t, func() bool {
		found := 0
		for _, snapshot := range sup.Snapshot() {
			if snapshot.State == StateRunning {
				found++
			}
		}
		return found == count
	})
}

func snapshotByName(sup *Supervisor, name string) Snapshot {
	for _, snapshot := range sup.Snapshot() {
		if snapshot.Name == name {
			return snapshot
		}
	}
	return Snapshot{}
}

func assertEventOrder(t *testing.T, events []Event, kind EventKind, state State, want []string) {
	t.Helper()
	var got []string
	for _, event := range events {
		if event.Kind == kind && event.State == state {
			got = append(got, event.Process)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s order = %v, want %v", kind, got, want)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
