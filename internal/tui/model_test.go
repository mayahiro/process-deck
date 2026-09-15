package tui

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mayahiro/nagi-go/vt"
	nagitui "github.com/mayahiro/nagitui-go"
	"github.com/mayahiro/nagitui-go/surface"
	"github.com/mayahiro/nagitui-go/tuitest"

	"github.com/mayahiro/process-deck/internal/supervisor"
)

var benchmarkViewNode nagitui.Node[appMessage]
var benchmarkFrame *nagitui.Frame

func TestSnapshotRows(t *testing.T) {
	exitCode := 2
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	rows := snapshotRows([]supervisor.Snapshot{
		{
			Name:     "api",
			State:    supervisor.StateFailed,
			PID:      123,
			Restarts: 4,
			ExitCode: &exitCode,
			Command:  "npm run dev",
		},
	}, now)

	want := []string{"api", "failed", "123", "4", "-", "2", "npm run dev"}
	if got := rows[0].Cells; !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshotRows() = %#v, want %#v", got, want)
	}
}

func TestUptimeTextUsesTickPrecision(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		started time.Time
		want    string
	}{
		{name: "seconds", started: now.Add(-12*time.Second - 900*time.Millisecond), want: "12s"},
		{name: "minutes and seconds", started: now.Add(-23*time.Minute - 45*time.Second), want: "23m45s"},
		{name: "hours minutes and seconds", started: now.Add(-time.Hour - 23*time.Minute - 45*time.Second), want: "1h23m45s"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := supervisor.Snapshot{PID: 1, StartedAt: test.started}
			if got := uptimeTextAt(snapshot, now); got != test.want {
				t.Fatalf("uptimeTextAt() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestHasVisibleUptimeRequiresRunningProcess(t *testing.T) {
	tests := []struct {
		name      string
		snapshots []supervisor.Snapshot
		want      bool
	}{
		{name: "no running process"},
		{
			name:      "running process",
			snapshots: []supervisor.Snapshot{{PID: 1, StartedAt: time.Unix(1, 0)}},
			want:      true,
		},
		{
			name:      "missing pid",
			snapshots: []supervisor.Snapshot{{StartedAt: time.Unix(1, 0)}},
		},
		{
			name:      "missing start time",
			snapshots: []supervisor.Snapshot{{PID: 1}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := hasVisibleUptime(test.snapshots); got != test.want {
				t.Fatalf("hasVisibleUptime() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestUptimeTickUpdatesModelTime(t *testing.T) {
	m := newModel(nil, func() {})
	want := time.Date(2026, 7, 20, 12, 0, 1, 0, time.UTC)
	m.Update(appMessage{kind: messageUptimeTick, timestamp: want})
	if !m.now.Equal(want) {
		t.Fatalf("model time = %s, want %s", m.now, want)
	}
}

func TestDefaultColumnsFitSupportedWidths(t *testing.T) {
	for _, width := range []int{60, 74, 75, 80, 96, 130} {
		columns := defaultColumns(width)
		total := 2 + (len(columns)-1)*3
		for _, column := range columns {
			columnWidth, ok := column.Width.Value()
			if !ok {
				t.Fatalf("width %d column %q is not fixed", width, column.Title)
			}
			total += int(columnWidth)
		}
		if total > width {
			t.Fatalf("defaultColumns(%d) uses %d cells", width, total)
		}
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

func TestLogViewLinesEmpty(t *testing.T) {
	got := logViewLines(nil)
	want := []string{"no logs"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("logViewLines() = %#v, want %#v", got, want)
	}
}

func TestVisibleLogTextRemovesTerminalControls(t *testing.T) {
	input := "plain\x1b[31;1mred\x1b[0m\x1b]8;;https://invalid.example\a link\x1b[2J"
	if got, want := visibleLogText(input), "plainred link"; got != want {
		t.Fatalf("visibleLogText() = %q, want %q", got, want)
	}
}

func TestNagiTUIHarnessTogglesWrapAndScrollsHorizontally(t *testing.T) {
	m, harness := newTestHarness(t, []supervisor.Snapshot{{Name: "api", State: supervisor.StateRunning}}, map[string][]supervisor.LogEntry{
		"api": {{Stream: "stdout", Line: strings.Repeat("x", 180), Time: time.Unix(0, 0)}},
	})

	if err := harness.Input([]byte("w")); err != nil {
		t.Fatal(err)
	}
	if m.logWrap {
		t.Fatal("logWrap = true, want false")
	}
	if got, want := m.status, "log wrap disabled"; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
	state, ok := harness.ScrollState(logHorizontalScrollID)
	if !ok || state.Maximum.X < logHorizontalScrollStep {
		t.Fatalf("horizontal scroll state = %+v, %t", state, ok)
	}

	if err := harness.Input([]byte("\x1b[C")); err != nil {
		t.Fatal(err)
	}
	state, ok = harness.ScrollState(logHorizontalScrollID)
	if !ok || state.Offset.X != logHorizontalScrollStep {
		t.Fatalf("horizontal offset = %+v, %t, want %d", state.Offset, ok, logHorizontalScrollStep)
	}

	if err := harness.Input([]byte("w")); err != nil {
		t.Fatal(err)
	}
	if !m.logWrap {
		t.Fatal("logWrap = false, want true")
	}
	if m.logOffset.X != 0 {
		t.Fatalf("logOffset.X = %d, want 0", m.logOffset.X)
	}
	if _, ok := harness.ScrollState(logHorizontalScrollID); ok {
		t.Fatal("horizontal scroll state remains after wrapping was enabled")
	}
}

func TestNagiTUIHarnessLogScrollDisablesFollow(t *testing.T) {
	logs := make([]supervisor.LogEntry, 40)
	for index := range logs {
		logs[index] = supervisor.LogEntry{
			Stream: "stdout",
			Line:   "line",
			Time:   time.Unix(int64(index), 0),
		}
	}
	m, harness := newTestHarness(t, []supervisor.Snapshot{{Name: "api", State: supervisor.StateRunning}}, map[string][]supervisor.LogEntry{"api": logs})

	before, ok := harness.ScrollState(logVerticalScrollID)
	if !ok || !before.AtEnd || before.Maximum.Y == 0 {
		t.Fatalf("initial vertical scroll state = %+v, %t", before, ok)
	}
	if err := harness.Input([]byte("\x1b[5~")); err != nil {
		t.Fatal(err)
	}
	if m.follow {
		t.Fatal("follow = true, want false")
	}
	if got, want := m.status, "log follow disabled"; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
	after, ok := harness.ScrollState(logVerticalScrollID)
	if !ok || after.Offset.Y >= before.Offset.Y {
		t.Fatalf("vertical offset after PageUp = %+v, want less than %+v", after, before)
	}
}

func TestNagiTUIHarnessFollowsGrowingLogs(t *testing.T) {
	fake := newFakeSupervisor([]supervisor.Snapshot{{Name: "api", State: supervisor.StateRunning}}, map[string][]supervisor.LogEntry{
		"api": logEntries(20),
	})
	_, harness := newHarness(t, fake, func() {})
	before, ok := harness.ScrollState(logVerticalScrollID)
	if !ok || !before.AtEnd {
		t.Fatalf("initial vertical scroll state = %+v, %t", before, ok)
	}

	fake.logs["api"] = logEntries(30)
	if err := harness.Send(appMessage{
		kind: messageSupervisorEvent,
		event: supervisor.Event{
			Kind: supervisor.EventProcessLogLine, Process: "api",
		},
	}); err != nil {
		t.Fatal(err)
	}
	after, ok := harness.ScrollState(logVerticalScrollID)
	if !ok || !after.AtEnd || after.Offset.Y != after.Maximum.Y || after.Maximum.Y <= before.Maximum.Y {
		t.Fatalf("vertical scroll state after growth = %+v, want end beyond %+v", after, before)
	}
}

func TestNagiTUILogCacheUpdatesFromEventsWithoutReadingHistory(t *testing.T) {
	fake := newFakeSupervisor([]supervisor.Snapshot{{Name: "api", State: supervisor.StateRunning}}, map[string][]supervisor.LogEntry{
		"api": logEntries(20),
	})
	m, harness := newHarness(t, fake, func() {})
	if got, want := fake.logEntriesReturned, 20; got != want {
		t.Fatalf("initial returned log entries = %d, want %d", got, want)
	}
	initialCalls := fake.logsSinceCalls
	initialFrames := len(harness.Frames())

	if err := harness.Advance(time.Hour); err != nil {
		t.Fatal(err)
	}
	if got, want := fake.logEntriesReturned, 20; got != want {
		t.Fatalf("returned log entries while idle = %d, want %d", got, want)
	}
	if got := fake.logsSinceCalls; got != initialCalls {
		t.Fatalf("LogsSince calls while idle = %d, want %d", got, initialCalls)
	}
	if got := len(harness.Frames()); got != initialFrames {
		t.Fatalf("frames while idle = %d, want %d", got, initialFrames)
	}

	entry := supervisor.LogEntry{Stream: "stdout", Line: "new", Time: time.Unix(20, 0)}
	fake.logs["api"] = append(logEntries(20), entry)
	fake.logRevisions["api"] = 21
	if err := harness.Send(appMessage{
		kind: messageSupervisorEvent,
		event: supervisor.Event{
			Kind:          supervisor.EventProcessLogLine,
			Process:       "api",
			Stream:        entry.Stream,
			Line:          entry.Line,
			Time:          entry.Time,
			LogCursor:     21,
			LogRetained:   21,
			LogStateValid: true,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if got, want := fake.logEntriesReturned, 20; got != want {
		t.Fatalf("returned log entries after append = %d, want %d", got, want)
	}
	if got := fake.logsSinceCalls; got != initialCalls {
		t.Fatalf("LogsSince calls after event = %d, want %d", got, initialCalls)
	}
	if got, want := len(m.logLines), 21; got != want {
		t.Fatalf("cached log lines = %d, want %d", got, want)
	}
	if got := m.logLines[20].content; !strings.Contains(got, "new") {
		t.Fatalf("newest cached log line = %q, want new entry", got)
	}
}

func TestNagiTUIViewDoesNotReadSupervisorLogs(t *testing.T) {
	fake := newFakeSupervisor([]supervisor.Snapshot{{Name: "api", State: supervisor.StateRunning}}, map[string][]supervisor.LogEntry{
		"api": logEntries(20),
	})
	m := newModel(fake, func() {})
	initialCalls := fake.logsSinceCalls

	m.View(nagitui.ViewContext{Size: nagitui.Size{Width: 96, Height: 24}})
	if got := fake.logsSinceCalls; got != initialCalls {
		t.Fatalf("LogsSince calls from View = %d, want %d", got, initialCalls)
	}
}

func TestNagiTUIHarnessRefreshesSnapshotFromSupervisorEvent(t *testing.T) {
	fake := newFakeSupervisor([]supervisor.Snapshot{{Name: "api", State: supervisor.StateRunning}}, nil)
	m, harness := newHarness(t, fake, func() {})
	exitCode := 1
	fake.snapshots = []supervisor.Snapshot{{
		Name:     "api",
		State:    supervisor.StateFailed,
		ExitCode: &exitCode,
	}}

	if err := harness.Send(appMessage{
		kind: messageSupervisorEvent,
		event: supervisor.Event{
			Kind:    supervisor.EventProcessStateChanged,
			Process: "api",
			State:   supervisor.StateFailed,
		},
	}); err != nil {
		t.Fatal(err)
	}

	if got := m.snapshots[0].State; got != supervisor.StateFailed {
		t.Fatalf("snapshot state = %s, want %s", got, supervisor.StateFailed)
	}
	if rendered := surfaceText(harness.LatestSurface()); !strings.Contains(rendered, "failed") {
		t.Fatalf("rendered screen does not contain failed state:\n%s", rendered)
	}
}

func TestNagiTUIHarnessRefreshesRunningUptimeEachSecond(t *testing.T) {
	fake := newFakeSupervisor([]supervisor.Snapshot{{
		Name:      "api",
		State:     supervisor.StateRunning,
		PID:       42,
		StartedAt: time.Now().Add(-time.Hour),
	}}, nil)
	_, harness := newHarness(t, fake, func() {})
	if got, want := harness.ActiveSubscriptions(), 2; got != want {
		t.Fatalf("active subscriptions = %d, want %d", got, want)
	}
	initialFrames := len(harness.Frames())

	if err := harness.Advance(time.Second - time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if got := len(harness.Frames()); got != initialFrames {
		t.Fatalf("frames before second boundary = %d, want %d", got, initialFrames)
	}
	if err := harness.Advance(time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if got, want := len(harness.Frames()), initialFrames+1; got != want {
		t.Fatalf("frames at first second boundary = %d, want %d", got, want)
	}
	if err := harness.Advance(time.Second); err != nil {
		t.Fatal(err)
	}
	if got, want := len(harness.Frames()), initialFrames+2; got != want {
		t.Fatalf("frames at second second boundary = %d, want %d", got, want)
	}

	fake.snapshots = []supervisor.Snapshot{{Name: "api", State: supervisor.StateExited}}
	if err := harness.Send(appMessage{
		kind:  messageSupervisorEvent,
		event: supervisor.Event{Kind: supervisor.EventProcessExited, Process: "api"},
	}); err != nil {
		t.Fatal(err)
	}
	if got, want := harness.ActiveSubscriptions(), 1; got != want {
		t.Fatalf("active subscriptions after exit = %d, want %d", got, want)
	}
	framesAfterExit := len(harness.Frames())
	if err := harness.Advance(time.Hour); err != nil {
		t.Fatal(err)
	}
	if got := len(harness.Frames()); got != framesAfterExit {
		t.Fatalf("frames while stopped = %d, want %d", got, framesAfterExit)
	}
}

func TestNagiTUILogCacheDropsRolledOverEntries(t *testing.T) {
	fake := newFakeSupervisor([]supervisor.Snapshot{{Name: "api", State: supervisor.StateRunning}}, map[string][]supervisor.LogEntry{
		"api": {
			{Stream: "stdout", Line: strings.Repeat("x", 180), Time: time.Unix(0, 0)},
			{Stream: "stdout", Line: "line", Time: time.Unix(1, 0)},
			{Stream: "stdout", Line: "line", Time: time.Unix(2, 0)},
		},
	})
	m, harness := newHarness(t, fake, func() {})
	if err := harness.Input([]byte("w")); err != nil {
		t.Fatal(err)
	}
	beforeWidth := m.logContentWidth

	fake.setRetainedLogs("api", []supervisor.LogEntry{
		{Stream: "stdout", Line: "line", Time: time.Unix(1, 0)},
		{Stream: "stdout", Line: "line", Time: time.Unix(2, 0)},
		{Stream: "stdout", Line: "new", Time: time.Unix(3, 0)},
	}, 4)
	if err := harness.Send(appMessage{
		kind: messageSupervisorEvent,
		event: supervisor.Event{
			Kind:          supervisor.EventProcessLogLine,
			Process:       "api",
			Stream:        "stdout",
			Line:          "new",
			Time:          time.Unix(3, 0),
			LogCursor:     4,
			LogRetained:   3,
			LogStateValid: true,
		},
	}); err != nil {
		t.Fatal(err)
	}

	if got, want := len(m.logLines), 3; got != want {
		t.Fatalf("cached log lines = %d, want %d", got, want)
	}
	if strings.Contains(m.logLines[0].content, "00:00:00") {
		t.Fatalf("oldest cached log line was not dropped: %q", m.logLines[0].content)
	}
	if got := m.logLines[2].content; !strings.Contains(got, "new") {
		t.Fatalf("newest cached log line = %q, want new entry", got)
	}
	if got := m.logContentWidth; got >= beforeWidth {
		t.Fatalf("log content width after rollover = %d, want less than %d", got, beforeWidth)
	}
	state, ok := harness.ScrollState(logHorizontalScrollID)
	if !ok || state.Maximum.X != 0 {
		t.Fatalf("horizontal scroll state after rollover = %+v, %t", state, ok)
	}
}

func TestNagiTUILogCachePreservesScrollExtent(t *testing.T) {
	m, harness := newTestHarness(t, []supervisor.Snapshot{{Name: "api", State: supervisor.StateRunning}}, map[string][]supervisor.LogEntry{
		"api": logEntries(130),
	})

	state, ok := harness.ScrollState(logVerticalScrollID)
	if !ok {
		t.Fatal("vertical scroll state is missing")
	}
	wantMaximum := uint32(maxInt(0, m.logContentHeight-m.logHeight))
	if got := state.Maximum.Y; got != wantMaximum {
		t.Fatalf("vertical maximum = %d, want %d", got, wantMaximum)
	}
}

func TestNagiTUILogVirtualRangeIsBoundedToVisibleLines(t *testing.T) {
	m := newModel(nil, func() {})
	for _, entry := range logEntries(1000) {
		m.appendCachedLogLine(entry)
	}

	start, end, origin := m.visibleLogRange(500, 10)
	if start != 500 || end != 510 || origin != 500 {
		t.Fatalf("visibleLogRange() = %d, %d, %d, want 500, 510, 500", start, end, origin)
	}
}

func TestNagiTUILogVirtualRangeIncludesIntersectingWrappedLines(t *testing.T) {
	m := newModel(nil, func() {})
	m.width = 24
	m.logWrap = true
	for index := range 3 {
		m.appendCachedLogLine(supervisor.LogEntry{
			Stream: "stdout",
			Line:   strings.Repeat(fmt.Sprintf("line-%d ", index), 12),
			Time:   time.Unix(int64(index), 0),
		})
	}

	firstHeight := m.logLines[0].height
	secondHeight := m.logLines[1].height
	if firstHeight <= 1 || secondHeight <= 1 {
		t.Fatalf("wrapped heights = %d, %d, want both greater than 1", firstHeight, secondHeight)
	}
	start, end, origin := m.visibleLogRange(firstHeight+secondHeight-1, 2)
	if start != 1 || end != 3 || origin != firstHeight {
		t.Fatalf("visibleLogRange() = %d, %d, %d, want 1, 3, %d", start, end, origin, firstHeight)
	}
}

func TestNagiTUILogVirtualScrollRendersRequestedRange(t *testing.T) {
	logs := make([]supervisor.LogEntry, 100)
	for index := range logs {
		logs[index] = supervisor.LogEntry{
			Stream: "stdout",
			Line:   fmt.Sprintf("line-%03d", index),
			Time:   time.Unix(int64(index), 0),
		}
	}
	_, harness := newTestHarness(t, []supervisor.Snapshot{{Name: "api", State: supervisor.StateRunning}}, map[string][]supervisor.LogEntry{
		"api": logs,
	})
	if !harness.SetScrollOffset(logVerticalScrollID, nagitui.ScrollOffset{Y: 50}) {
		t.Fatal("SetScrollOffset() = false")
	}
	if err := harness.Step(); err != nil {
		t.Fatal(err)
	}

	rendered := surfaceText(harness.LatestSurface())
	if !strings.Contains(rendered, "line-050") {
		t.Fatalf("requested virtual range was not rendered:\n%s", rendered)
	}
	if strings.Contains(rendered, "line-000") || strings.Contains(rendered, "line-099") {
		t.Fatalf("logs outside the requested virtual range were rendered:\n%s", rendered)
	}
}

func TestNagiTUILogCacheRecalculatesWrapOnResize(t *testing.T) {
	m, harness := newTestHarness(t, []supervisor.Snapshot{{Name: "api", State: supervisor.StateRunning}}, map[string][]supervisor.LogEntry{
		"api": {{Stream: "stdout", Line: strings.Repeat("x", 500), Time: time.Unix(0, 0)}},
	})
	before := m.logContentHeight

	if err := harness.Resize(nagitui.Size{Width: 40, Height: 24}); err != nil {
		t.Fatal(err)
	}
	if got := m.logContentHeight; got <= before {
		t.Fatalf("wrapped log height after resize = %d, want greater than %d", got, before)
	}
}

func TestNagiTUIHarnessMovesProcessSelection(t *testing.T) {
	m, harness := newTestHarness(t, []supervisor.Snapshot{
		{Name: "api", State: supervisor.StateRunning},
		{Name: "worker", State: supervisor.StateRunning},
	}, map[string][]supervisor.LogEntry{
		"api":    {{Stream: "stdout", Line: "api-only", Time: time.Unix(0, 0)}},
		"worker": {{Stream: "stdout", Line: "worker-only", Time: time.Unix(0, 0)}},
	})

	if err := harness.Input([]byte("j")); err != nil {
		t.Fatal(err)
	}
	if got, want := m.selectedProcess(), "worker"; got != want {
		t.Fatalf("selected process = %q, want %q", got, want)
	}
	if rendered := surfaceText(harness.LatestSurface()); !strings.Contains(rendered, "worker-only") || strings.Contains(rendered, "api-only") {
		t.Fatalf("worker log cache was not selected:\n%s", rendered)
	}
	if err := harness.Input([]byte("\x1b[A")); err != nil {
		t.Fatal(err)
	}
	if got, want := m.selectedProcess(), "api"; got != want {
		t.Fatalf("selected process = %q, want %q", got, want)
	}
	if rendered := surfaceText(harness.LatestSurface()); !strings.Contains(rendered, "api-only") || strings.Contains(rendered, "worker-only") {
		t.Fatalf("api log cache was not restored:\n%s", rendered)
	}
}

func TestNagiTUIHarnessRendersScreenRegions(t *testing.T) {
	_, harness := newTestHarness(t, []supervisor.Snapshot{{
		Name: "api", State: supervisor.StateRunning, PID: 42, Command: "go run ./cmd/api",
	}}, map[string][]supervisor.LogEntry{
		"api": {{Stream: "stdout", Line: "ready", Time: time.Unix(0, 0)}},
	})

	rendered := surfaceText(harness.LatestSurface())
	for _, expected := range []string{
		"Process Deck",
		"api",
		"go run ./cmd/api",
		"Logs: api",
		"ready",
		"starting processes",
		"q quit",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered screen does not contain %q:\n%s", expected, rendered)
		}
	}
}

func TestNagiTUIHarnessRunsProcessCommand(t *testing.T) {
	fake := newFakeSupervisor([]supervisor.Snapshot{{Name: "api", State: supervisor.StateRunning}}, nil)
	fake.stopErr = errors.New("stop failed")
	m, harness := newHarness(t, fake, func() {})

	if err := harness.Input([]byte("s")); err != nil {
		t.Fatal(err)
	}
	select {
	case name := <-fake.stopCalls:
		if name != "api" {
			t.Fatalf("stopped process = %q, want api", name)
		}
	case <-time.After(time.Second):
		t.Fatal("StopProcess was not called")
	}
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline) && m.status != "stop failed"; {
		if err := harness.Step(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	if got, want := m.status, "stop failed"; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
}

func TestNagiTUIHarnessQuitWaitsForSupervisorStop(t *testing.T) {
	cancelled := false
	fake := newFakeSupervisor([]supervisor.Snapshot{{Name: "api", State: supervisor.StateRunning}}, nil)
	m, harness := newHarness(t, fake, func() { cancelled = true })

	if err := harness.Input([]byte("q")); err != nil {
		t.Fatal(err)
	}
	if !cancelled || !m.quitting {
		t.Fatalf("cancelled = %t, quitting = %t", cancelled, m.quitting)
	}
	if harness.ExitRequested() {
		t.Fatal("exit requested before supervisor stopped")
	}
	if err := harness.Send(appMessage{
		kind:  messageSupervisorEvent,
		event: supervisor.Event{Kind: supervisor.EventSupervisorStopped},
	}); err != nil {
		t.Fatal(err)
	}
	if !harness.ExitRequested() {
		t.Fatal("exit not requested after supervisor stopped")
	}
}

func TestMessageForEventMapsControlKeys(t *testing.T) {
	tests := []struct {
		name   string
		event  vt.Event
		action userAction
	}{
		{
			name: "control c",
			event: vt.Event{Kind: vt.EventKey, Key: vt.KeyEvent{
				Code: vt.KeyCharacter, Character: 'c', Modifiers: vt.Modifiers{Control: true},
			}},
			action: actionQuit,
		},
		{
			name: "control u",
			event: vt.Event{Kind: vt.EventKey, Key: vt.KeyEvent{
				Code: vt.KeyCharacter, Character: 'u', Modifiers: vt.Modifiers{Control: true},
			}},
			action: actionLogHalfPageUp,
		},
		{
			name: "control d",
			event: vt.Event{Kind: vt.EventKey, Key: vt.KeyEvent{
				Code: vt.KeyCharacter, Character: 'd', Modifiers: vt.Modifiers{Control: true},
			}},
			action: actionLogHalfPageDown,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			msg, ok := messageForEvent(test.event)
			if !ok || msg.kind != messageAction || msg.action != test.action {
				t.Fatalf("messageForEvent() = %+v, %t, want action %d", msg, ok, test.action)
			}
		})
	}
}

func TestMessageForEventMapsKittyKeys(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		action userAction
	}{
		{name: "quit", input: "\x1b[113u", action: actionQuit},
		{name: "stop", input: "\x1b[115u", action: actionStopProcess},
		{name: "start", input: "\x1b[97u", action: actionStartProcess},
		{name: "restart", input: "\x1b[114u", action: actionRestartProcess},
		{name: "control c", input: "\x1b[99;5u", action: actionQuit},
		{name: "control u", input: "\x1b[117;5u", action: actionLogHalfPageUp},
		{name: "control d", input: "\x1b[100;5u", action: actionLogHalfPageDown},
		{name: "down", input: "\x1b[1;1:1B", action: actionSelectNext},
		{name: "repeated navigation", input: "\x1b[106;1:2u", action: actionSelectNext},
		{name: "caps lock", input: "\x1b[113;65u", action: actionQuit},
		{name: "num lock", input: "\x1b[106;129u", action: actionSelectNext},
		{name: "control with locks", input: "\x1b[117;197u", action: actionLogHalfPageUp},
		{name: "released quit", input: "\x1b[113;1:3u"},
		{name: "released restart", input: "\x1b[114;1:3u"},
		{name: "alt quit", input: "\x1b[113;3u"},
		{name: "meta quit", input: "\x1b[113;33u"},
		{name: "super quit", input: "\x1b[113;9u"},
		{name: "hyper quit", input: "\x1b[113;17u"},
		{name: "super stop", input: "\x1b[115;9u"},
		{name: "hyper stop", input: "\x1b[115;17u"},
		{name: "super start", input: "\x1b[97;9u"},
		{name: "hyper start", input: "\x1b[97;17u"},
		{name: "super restart", input: "\x1b[114;9u"},
		{name: "hyper restart", input: "\x1b[114;17u"},
		{name: "super control c", input: "\x1b[99;13u"},
		{name: "hyper control c", input: "\x1b[99;21u"},
		{name: "super control u", input: "\x1b[117;13u"},
		{name: "hyper control d", input: "\x1b[100;21u"},
		{name: "super down", input: "\x1b[1;9:1B"},
		{name: "hyper down", input: "\x1b[1;17:1B"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := vt.NewDecoder().Feed([]byte(test.input))
			if len(events) != 1 || events[0].Kind != vt.EventKey || events[0].Key.Protocol != vt.KeyProtocolKitty {
				t.Fatalf("decoded Kitty input = %+v, want one key event", events)
			}
			msg, ok := messageForEvent(events[0])
			if test.action == actionNone {
				if ok {
					t.Fatalf("messageForEvent() = %+v, want ignored key", msg)
				}
				return
			}
			if !ok || msg.kind != messageAction || msg.action != test.action {
				t.Fatalf("messageForEvent() = %+v, %t, want action %d", msg, ok, test.action)
			}
		})
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

func BenchmarkModelViewWithCachedLogs(b *testing.B) {
	tests := []struct {
		name  string
		count int
	}{
		{name: "10-lines", count: 10},
		{name: "1000-lines", count: 1000},
	}
	for _, test := range tests {
		b.Run(test.name, func(b *testing.B) {
			fake := newFakeSupervisor([]supervisor.Snapshot{{Name: "api", State: supervisor.StateRunning}}, map[string][]supervisor.LogEntry{
				"api": logEntries(test.count),
			})
			m := newModel(fake, func() {})
			context := nagitui.ViewContext{Size: nagitui.Size{Width: 96, Height: 24}}
			benchmarkViewNode = m.View(context)

			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				benchmarkViewNode = m.View(context)
			}
		})
	}
}

func BenchmarkModelRenderWithCachedLogs(b *testing.B) {
	tests := []struct {
		name  string
		count int
	}{
		{name: "10-lines", count: 10},
		{name: "1000-lines", count: 1000},
	}
	for _, test := range tests {
		b.Run(test.name, func(b *testing.B) {
			fake := newFakeSupervisor([]supervisor.Snapshot{{Name: "api", State: supervisor.StateRunning}}, map[string][]supervisor.LogEntry{
				"api": logEntries(test.count),
			})
			m := newModel(fake, func() {})
			runtime, err := nagitui.NewRuntime[appMessage](m, nagitui.Size{Width: 96, Height: 24})
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(runtime.Close)
			if _, err := runtime.RenderIfDirty(); err != nil {
				b.Fatal(err)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				runtime.RequestFrame()
				benchmarkFrame, err = runtime.RenderIfDirty()
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkModelAppendLogEventAtCapacity(b *testing.B) {
	tests := []struct {
		name  string
		count int
	}{
		{name: "10-lines", count: 10},
		{name: "1000-lines", count: 1000},
	}
	for _, test := range tests {
		b.Run(test.name, func(b *testing.B) {
			m := newModel(nil, func() {})
			m.snapshots = []supervisor.Snapshot{{Name: "api", State: supervisor.StateRunning}}
			m.logProcess = "api"
			m.logCursor = supervisor.LogCursor(test.count)
			m.logSyncNeeded = false
			for _, entry := range logEntries(test.count) {
				m.appendCachedLogLine(entry)
			}
			benchmarkViewNode = m.logViewportNode()
			event := supervisor.Event{
				Kind:          supervisor.EventProcessLogLine,
				Process:       "api",
				Stream:        "stdout",
				Line:          "line",
				Time:          time.Unix(int64(test.count), 0),
				LogCursor:     supervisor.LogCursor(test.count),
				LogRetained:   test.count,
				LogStateValid: true,
			}

			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				event.LogCursor++
				m.applyLogEvent(event)
				benchmarkViewNode = m.logViewportNode()
			}
		})
	}
}

func newTestHarness(
	t *testing.T,
	snapshots []supervisor.Snapshot,
	logs map[string][]supervisor.LogEntry,
) (*model, *tuitest.Harness[appMessage]) {
	t.Helper()
	return newHarness(t, newFakeSupervisor(snapshots, logs), func() {})
}

func newHarness(
	t *testing.T,
	fake *fakeSupervisor,
	cancel func(),
) (*model, *tuitest.Harness[appMessage]) {
	t.Helper()
	m := newModel(fake, cancel)
	harness, err := tuitest.New(
		m,
		nagitui.Size{Width: 96, Height: 24},
		mapEvent,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(harness.Close)
	wantSubscriptions := 1
	if hasVisibleUptime(m.snapshots) {
		wantSubscriptions++
	}
	if got := harness.ActiveSubscriptions(); got != wantSubscriptions {
		t.Fatalf("active subscriptions = %d, want %d", got, wantSubscriptions)
	}
	return m, harness
}

type fakeSupervisor struct {
	events       chan supervisor.Event
	snapshots    []supervisor.Snapshot
	logs         map[string][]supervisor.LogEntry
	stopCalls    chan string
	startCalls   chan string
	restartCalls chan string
	logStarts    map[string]supervisor.LogCursor
	logRevisions map[string]supervisor.LogCursor

	logEntriesReturned int
	logsSinceCalls     int
	stopErr            error
	startErr           error
	restartErr         error
}

func newFakeSupervisor(snapshots []supervisor.Snapshot, logs map[string][]supervisor.LogEntry) *fakeSupervisor {
	fake := &fakeSupervisor{
		events:       make(chan supervisor.Event),
		snapshots:    append([]supervisor.Snapshot(nil), snapshots...),
		logs:         logs,
		stopCalls:    make(chan string, 1),
		startCalls:   make(chan string, 1),
		restartCalls: make(chan string, 1),
		logStarts:    make(map[string]supervisor.LogCursor),
		logRevisions: make(map[string]supervisor.LogCursor),
	}
	for name, entries := range logs {
		fake.logRevisions[name] = supervisor.LogCursor(len(entries))
	}
	return fake
}

func logEntries(count int) []supervisor.LogEntry {
	entries := make([]supervisor.LogEntry, count)
	for index := range entries {
		entries[index] = supervisor.LogEntry{
			Stream: "stdout",
			Line:   "line",
			Time:   time.Unix(int64(index), 0),
		}
	}
	return entries
}

func (f *fakeSupervisor) Events() <-chan supervisor.Event {
	return f.events
}

func (f *fakeSupervisor) Snapshot() []supervisor.Snapshot {
	return append([]supervisor.Snapshot(nil), f.snapshots...)
}

func (f *fakeSupervisor) LogsSince(name string, cursor supervisor.LogCursor) supervisor.LogBatch {
	f.logsSinceCalls++
	entries := f.logs[name]
	start := f.logStarts[name]
	revision := f.logRevisions[name]
	minimumRevision := start + supervisor.LogCursor(len(entries))
	if revision < minimumRevision {
		revision = minimumRevision
		f.logRevisions[name] = revision
	}
	reset := cursor < start || cursor > revision
	if reset {
		cursor = start
	}
	offset := int(cursor - start)
	delta := append([]supervisor.LogEntry(nil), entries[offset:]...)
	f.logEntriesReturned += len(delta)
	return supervisor.LogBatch{
		Entries:  delta,
		Cursor:   revision,
		Retained: len(entries),
		Reset:    reset,
	}
}

func (f *fakeSupervisor) setRetainedLogs(name string, entries []supervisor.LogEntry, revision supervisor.LogCursor) {
	f.logs[name] = append([]supervisor.LogEntry(nil), entries...)
	f.logRevisions[name] = revision
	f.logStarts[name] = revision - supervisor.LogCursor(len(entries))
}

func (f *fakeSupervisor) StopProcess(name string) error {
	f.stopCalls <- name
	return f.stopErr
}

func (f *fakeSupervisor) StartProcess(name string) error {
	f.startCalls <- name
	return f.startErr
}

func (f *fakeSupervisor) RestartProcess(name string) error {
	f.restartCalls <- name
	return f.restartErr
}

func surfaceText(source *surface.Surface) string {
	var output strings.Builder
	for y := uint32(0); y < source.Height(); y++ {
		for x := uint32(0); x < source.Width(); x++ {
			cell, _ := source.Cell(int32(x), int32(y))
			if cell.Continuation() {
				continue
			}
			content := cell.Content()
			if content == "" {
				content = " "
			}
			output.WriteString(content)
		}
		output.WriteByte('\n')
	}
	return output.String()
}
