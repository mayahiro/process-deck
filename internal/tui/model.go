package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/mayahiro/process-deck/internal/supervisor"
)

const tickInterval = time.Second

type model struct {
	supervisor *supervisor.Supervisor
	cancel     context.CancelFunc

	table    table.Model
	logs     viewport.Model
	follow   bool
	quitting bool
	stopped  bool

	snapshots []supervisor.Snapshot
	logLines  map[string][]supervisor.LogEntry
	status    string
	width     int
	height    int
}

type supervisorEventMsg supervisor.Event
type supervisorEventsClosedMsg struct{}
type tickMsg time.Time

type commandDoneMsg struct {
	action  string
	process string
	err     error
}

func newModel(sup *supervisor.Supervisor, cancel context.CancelFunc) model {
	snapshots := sup.Snapshot()
	m := model{
		supervisor: sup,
		cancel:     cancel,
		table: table.New(
			table.WithColumns(defaultColumns(96)),
			table.WithRows(snapshotRows(snapshots)),
			table.WithFocused(true),
			table.WithHeight(8),
			table.WithWidth(96),
		),
		logs:      viewport.New(viewport.WithWidth(96), viewport.WithHeight(10)),
		follow:    true,
		snapshots: snapshots,
		logLines:  make(map[string][]supervisor.LogEntry),
		status:    "starting processes",
		width:     96,
		height:    24,
	}
	m.refreshLogPane()
	return m
}

func (m model) Init() tea.Cmd {
	return tea.Batch(waitForEvent(m.supervisor.Events()), tick())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case supervisorEventMsg:
		m.handleSupervisorEvent(supervisor.Event(msg))
		if m.stopped {
			return m, tea.Quit
		}
		return m, waitForEvent(m.supervisor.Events())
	case supervisorEventsClosedMsg:
		return m, tea.Quit
	case tickMsg:
		m.refreshSnapshots()
		return m, tick()
	case commandDoneMsg:
		m.status = commandStatus(msg)
		m.refreshSnapshots()
		return m, nil
	default:
		var cmd tea.Cmd
		if !m.follow {
			m.logs, cmd = m.logs.Update(msg)
		}
		return m, cmd
	}
}

func (m model) View() tea.View {
	view := tea.NewView(m.render())
	view.AltScreen = true
	return view
}

func (m *model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		if !m.quitting {
			m.quitting = true
			m.status = "stopping processes"
			m.cancel()
		}
		return *m, nil
	case "up", "k":
		m.table.MoveUp(1)
		m.refreshLogPane()
		return *m, nil
	case "down", "j":
		m.table.MoveDown(1)
		m.refreshLogPane()
		return *m, nil
	case "s":
		name := m.selectedProcess()
		if name == "" {
			return *m, nil
		}
		m.status = fmt.Sprintf("stopping %s", name)
		return *m, runCommand("stop", name, func() error {
			return m.supervisor.StopProcess(name)
		})
	case "a":
		name := m.selectedProcess()
		if name == "" {
			return *m, nil
		}
		m.status = fmt.Sprintf("starting %s", name)
		return *m, runCommand("start", name, func() error {
			return m.supervisor.StartProcess(name)
		})
	case "r":
		name := m.selectedProcess()
		if name == "" {
			return *m, nil
		}
		m.status = fmt.Sprintf("restarting %s", name)
		return *m, runCommand("restart", name, func() error {
			return m.supervisor.RestartProcess(name)
		})
	case "f":
		m.follow = !m.follow
		m.refreshLogPane()
		if m.follow {
			m.status = "log follow enabled"
		} else {
			m.status = "log follow disabled"
		}
		return *m, nil
	default:
		var cmd tea.Cmd
		if !m.follow {
			m.logs, cmd = m.logs.Update(msg)
		}
		return *m, cmd
	}
}

func (m *model) handleSupervisorEvent(event supervisor.Event) {
	switch event.Kind {
	case supervisor.EventProcessLogLine:
		m.logLines[event.Process] = append(m.logLines[event.Process], supervisor.LogEntry{
			Stream: event.Stream,
			Line:   event.Line,
			Time:   event.Time,
		})
		m.refreshLogPane()
	case supervisor.EventProcessRestartScheduled:
		m.status = fmt.Sprintf("%s restart scheduled", event.Process)
	case supervisor.EventProcessSkipped:
		m.status = fmt.Sprintf("%s skipped", event.Process)
	case supervisor.EventSupervisorError:
		if event.Error != nil {
			m.status = event.Error.Error()
		}
	case supervisor.EventSupervisorStopped:
		m.stopped = true
		m.status = "stopped"
	}
	m.refreshSnapshots()
	if !m.quitting && !m.stopped && m.allTerminal() {
		if m.anyFailed() {
			m.status = "one or more processes failed"
		} else {
			m.status = "all processes stopped"
		}
	}
}

func (m *model) refreshSnapshots() {
	m.snapshots = m.supervisor.Snapshot()
	cursor := m.table.Cursor()
	m.table.SetRows(snapshotRows(m.snapshots))
	if len(m.snapshots) == 0 {
		m.table.SetCursor(0)
		return
	}
	if cursor >= len(m.snapshots) {
		m.table.SetCursor(len(m.snapshots) - 1)
	}
}

func (m *model) refreshLogPane() {
	name := m.selectedProcess()
	lines := logViewLines(m.logLines[name])
	m.logs.SetContentLines(lines)
	if m.follow {
		m.logs.GotoBottom()
	}
}

func (m *model) selectedProcess() string {
	if len(m.snapshots) == 0 {
		return ""
	}
	cursor := m.table.Cursor()
	if cursor < 0 || cursor >= len(m.snapshots) {
		return ""
	}
	return m.snapshots[cursor].Name
}

func (m *model) resize(width int, height int) {
	if width <= 0 || height <= 0 {
		return
	}
	m.width = width
	m.height = height

	tableHeight := minInt(maxInt(6, height/2), maxInt(6, height-8))
	logHeight := maxInt(3, height-tableHeight-6)

	m.table.SetWidth(width)
	m.table.SetHeight(tableHeight)
	m.table.SetColumns(defaultColumns(width))
	m.logs.SetWidth(width)
	m.logs.SetHeight(logHeight)
	m.refreshLogPane()
}

func (m model) render() string {
	width := maxInt(m.width, 60)
	header := headerStyle(width).Render("Process Deck")
	tableView := m.table.View()
	selected := m.selectedProcess()
	if selected == "" {
		selected = "-"
	}
	logTitle := sectionStyle(width).Render(fmt.Sprintf("Logs: %s", selected))
	footer := footerStyle(width).Render(m.footerText())
	status := statusStyle(width).Render(m.status)

	return lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		tableView,
		logTitle,
		m.logs.View(),
		status,
		footer,
	)
}

func (m model) footerText() string {
	follow := "off"
	if m.follow {
		follow = "on"
	}
	return fmt.Sprintf("↑/k ↓/j select   s stop   a start   r restart   f follow:%s   q quit", follow)
}

func (m model) allTerminal() bool {
	if len(m.snapshots) == 0 {
		return false
	}
	for _, snapshot := range m.snapshots {
		switch snapshot.State {
		case supervisor.StateExited, supervisor.StateFailed, supervisor.StateSkipped:
		default:
			return false
		}
	}
	return true
}

func (m model) anyFailed() bool {
	for _, snapshot := range m.snapshots {
		if snapshot.State == supervisor.StateFailed || snapshot.State == supervisor.StateSkipped {
			return true
		}
	}
	return false
}

func waitForEvent(events <-chan supervisor.Event) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-events
		if !ok {
			return supervisorEventsClosedMsg{}
		}
		return supervisorEventMsg(event)
	}
}

func tick() tea.Cmd {
	return tea.Tick(tickInterval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func runCommand(action string, processName string, fn func() error) tea.Cmd {
	return func() tea.Msg {
		return commandDoneMsg{
			action:  action,
			process: processName,
			err:     fn(),
		}
	}
}

func commandStatus(msg commandDoneMsg) string {
	if msg.err != nil {
		return msg.err.Error()
	}
	return fmt.Sprintf("%s %s", msg.action, msg.process)
}

func snapshotRows(snapshots []supervisor.Snapshot) []table.Row {
	rows := make([]table.Row, 0, len(snapshots))
	for _, snapshot := range snapshots {
		rows = append(rows, table.Row{
			snapshot.Name,
			string(snapshot.State),
			pidText(snapshot.PID),
			fmt.Sprintf("%d", snapshot.Restarts),
			uptimeText(snapshot),
			exitText(snapshot.ExitCode),
			snapshot.Command,
		})
	}
	return rows
}

func defaultColumns(width int) []table.Column {
	commandWidth := maxInt(16, width-66)
	return []table.Column{
		{Title: "name", Width: 16},
		{Title: "state", Width: 10},
		{Title: "pid", Width: 7},
		{Title: "restarts", Width: 8},
		{Title: "uptime", Width: 8},
		{Title: "exit", Width: 5},
		{Title: "command", Width: commandWidth},
	}
}

func logViewLines(entries []supervisor.LogEntry) []string {
	if len(entries) == 0 {
		return []string{"no logs"}
	}
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		lines = append(lines, fmt.Sprintf("%s %-6s %s", entry.Time.Format("15:04:05"), entry.Stream, entry.Line))
	}
	return lines
}

func pidText(pid int) string {
	if pid == 0 {
		return "-"
	}
	return fmt.Sprintf("%d", pid)
}

func exitText(exitCode *int) string {
	if exitCode == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *exitCode)
}

func uptimeText(snapshot supervisor.Snapshot) string {
	if snapshot.PID == 0 || snapshot.StartedAt.IsZero() {
		return "-"
	}
	elapsed := time.Since(snapshot.StartedAt).Round(time.Second)
	if elapsed < time.Second {
		return "0s"
	}
	if elapsed < time.Minute {
		return elapsed.String()
	}
	return strings.TrimSuffix(elapsed.String(), "0s")
}

func headerStyle(width int) lipgloss.Style {
	return lipgloss.NewStyle().
		Bold(true).
		Width(width).
		Padding(0, 1)
}

func sectionStyle(width int) lipgloss.Style {
	return lipgloss.NewStyle().
		Bold(true).
		Width(width).
		Padding(0, 1)
}

func statusStyle(width int) lipgloss.Style {
	return lipgloss.NewStyle().
		Width(width).
		Padding(0, 1)
}

func footerStyle(width int) lipgloss.Style {
	return lipgloss.NewStyle().
		Width(width).
		Padding(0, 1)
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}
