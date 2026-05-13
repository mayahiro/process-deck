package tui

import (
	"reflect"
	"testing"
	"time"

	"github.com/mayahiro/process-deck/internal/supervisor"
)

func TestSnapshotRows(t *testing.T) {
	exitCode := 2
	rows := snapshotRows([]supervisor.Snapshot{
		{
			Name:     "api",
			State:    supervisor.StateFailed,
			PID:      123,
			Restarts: 4,
			ExitCode: &exitCode,
			Command:  "npm run dev",
		},
	})

	want := []string{"api", "failed", "123", "4", "-", "2", "npm run dev"}
	if got := []string(rows[0]); !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshotRows() = %#v, want %#v", got, want)
	}
}

func TestLogViewLines(t *testing.T) {
	entries := []supervisor.LogEntry{
		{
			Stream: "stderr",
			Line:   "warning",
			Time:   time.Date(2026, 5, 14, 10, 20, 30, 0, time.UTC),
		},
	}

	got := logViewLines(entries)
	want := []string{"10:20:30 stderr warning"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("logViewLines() = %#v, want %#v", got, want)
	}
}

func TestLogViewLinesEmpty(t *testing.T) {
	got := logViewLines(nil)
	want := []string{"no logs"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("logViewLines() = %#v, want %#v", got, want)
	}
}

func TestTerminalStatusHelpers(t *testing.T) {
	m := model{
		snapshots: []supervisor.Snapshot{
			{Name: "api", State: supervisor.StateExited},
			{Name: "worker", State: supervisor.StateFailed},
		},
	}

	if !m.allTerminal() {
		t.Fatal("allTerminal() = false, want true")
	}
	if !m.anyFailed() {
		t.Fatal("anyFailed() = false, want true")
	}
}
