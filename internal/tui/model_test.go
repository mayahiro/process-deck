package tui

import (
	"reflect"
	"testing"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

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
	want := []string{"10:20:30 [stderr] warning"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("logViewLines() = %#v, want %#v", got, want)
	}
}

func TestLogSourceLabelFallsBackForEmptyStream(t *testing.T) {
	if got, want := logSourceLabel(""), "[log]"; got != want {
		t.Fatalf("logSourceLabel() = %q, want %q", got, want)
	}
}

func TestToggleLogWrap(t *testing.T) {
	m := model{
		logs:    viewport.New(viewport.WithWidth(8), viewport.WithHeight(3)),
		logWrap: true,
	}
	m.logs.SoftWrap = true

	m.handleKey(keyPress('w', "w"))
	if m.logWrap {
		t.Fatal("logWrap = true, want false")
	}
	if m.logs.SoftWrap {
		t.Fatal("logs.SoftWrap = true, want false")
	}
	if got, want := m.status, "log wrap disabled"; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}

	m.logs.SetContentLines([]string{"01234567890123456789"})
	m.logs.ScrollRight(8)
	if got := m.logs.XOffset(); got == 0 {
		t.Fatalf("XOffset = %d, want scrolled", got)
	}

	m.handleKey(keyPress('w', "w"))
	if !m.logWrap {
		t.Fatal("logWrap = false, want true")
	}
	if !m.logs.SoftWrap {
		t.Fatal("logs.SoftWrap = false, want true")
	}
	if got := m.logs.XOffset(); got != 0 {
		t.Fatalf("XOffset = %d, want 0", got)
	}
}

func TestLogScrollDisablesFollow(t *testing.T) {
	m := model{
		logs:   viewport.New(viewport.WithWidth(20), viewport.WithHeight(2)),
		follow: true,
	}
	m.logs.SetContentLines([]string{"one", "two", "three", "four", "five"})
	m.logs.GotoBottom()

	m.handleKey(specialKey(tea.KeyPgUp))
	if m.follow {
		t.Fatal("follow = true, want false")
	}
	if m.logs.AtBottom() {
		t.Fatal("logs.AtBottom() = true, want false")
	}
	if got, want := m.status, "log follow disabled"; got != want {
		t.Fatalf("status = %q, want %q", got, want)
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

func keyPress(code rune, text string) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Text: text})
}

func specialKey(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code})
}
