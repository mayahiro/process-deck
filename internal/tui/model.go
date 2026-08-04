package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	celltext "github.com/mayahiro/nagi-go/text"
	"github.com/mayahiro/nagi-go/vt"
	nagitui "github.com/mayahiro/nagitui-go"
	"github.com/mayahiro/nagitui-go/widget"

	"github.com/mayahiro/process-deck/internal/supervisor"
)

const (
	logRefreshInterval           = 50 * time.Millisecond
	maxSupervisorEventsPerUpdate = 256
	logHorizontalScrollStep      = 8
	uptimeInterval               = time.Second

	rootNodeID            nagitui.NodeID = "process-deck-root"
	processTableID        nagitui.NodeID = "process-table"
	processTableScrollID  nagitui.NodeID = "process-table-scroll"
	logVerticalScrollID   nagitui.NodeID = "log-vertical-scroll"
	logHorizontalScrollID nagitui.NodeID = "log-horizontal-scroll"

	supervisorEventsSubscriptionKey nagitui.SubscriptionKey = "supervisor-events"
	uptimeSubscriptionKey           nagitui.SubscriptionKey = "uptime"
)

type supervisorController interface {
	Events() <-chan supervisor.Event
	Snapshot() []supervisor.Snapshot
	LogsSince(string, supervisor.LogCursor) supervisor.LogBatch
	StopProcess(string) error
	StartProcess(string) error
	RestartProcess(string) error
}

type messageKind uint8

const (
	messageAction messageKind = iota
	messageProcessSelected
	messageSupervisorEvent
	messageSupervisorEventsClosed
	messageUptimeTick
	messageCommandDone
)

type userAction uint8

const (
	actionNone userAction = iota
	actionQuit
	actionSelectPrevious
	actionSelectNext
	actionStopProcess
	actionStartProcess
	actionRestartProcess
	actionToggleFollow
	actionToggleWrap
	actionLogPageUp
	actionLogPageDown
	actionLogHalfPageUp
	actionLogHalfPageDown
	actionLogTop
	actionLogBottom
	actionLogLeft
	actionLogRight
)

type appMessage struct {
	kind      messageKind
	action    userAction
	index     int
	timestamp time.Time
	event     supervisor.Event
	command   commandDone
}

type commandDone struct {
	action  string
	process string
	err     error
}

type cachedLogLine struct {
	content string
	visible string
	width   int
	height  int
	start   int
}

type model struct {
	supervisor supervisorController
	cancel     context.CancelFunc

	follow   bool
	logWrap  bool
	quitting bool
	stopped  bool

	snapshots []supervisor.Snapshot
	selected  int
	status    string
	now       time.Time

	width       int
	height      int
	tableHeight int
	logHeight   int
	logOffset   nagitui.ScrollOffset
	logMaximum  nagitui.ScrollOffset

	logProcess       string
	logCursor        supervisor.LogCursor
	logSyncNeeded    bool
	logLines         []cachedLogLine
	logNodes         []nagitui.Node[appMessage]
	logNextY         int
	logContentWidth  int
	logContentHeight int
	logWidthCounts   map[int]int
}

func newModel(sup supervisorController, cancel context.CancelFunc) *model {
	if cancel == nil {
		cancel = func() {}
	}
	m := &model{
		supervisor: sup,
		cancel:     cancel,
		follow:     true,
		logWrap:    true,
		status:     "starting processes",
		now:        time.Now(),
	}
	m.resize(nagitui.Size{Width: 96, Height: 24})
	m.refreshSnapshots()
	m.syncLogCache()
	return m
}

func (*model) Init() nagitui.Effect[appMessage] {
	return nagitui.FocusEffect[appMessage](rootNodeID)
}

func (m *model) Update(msg appMessage) nagitui.Effect[appMessage] {
	switch msg.kind {
	case messageAction:
		return m.handleAction(msg.action)
	case messageProcessSelected:
		return m.selectProcess(msg.index)
	case messageSupervisorEvent:
		return m.handleSupervisorEvent(msg.event)
	case messageSupervisorEventsClosed:
		m.stopped = true
		m.status = "stopped"
		return nagitui.ExitEffect[appMessage]()
	case messageUptimeTick:
		m.now = msg.timestamp
		return nagitui.NoneEffect[appMessage]()
	case messageCommandDone:
		m.status = commandStatus(msg.command)
		m.refreshSnapshots()
		return nagitui.NoneEffect[appMessage]()
	default:
		return nagitui.NoneEffect[appMessage]()
	}
}

func (m *model) Subscriptions() nagitui.Subscription[appMessage] {
	if m.supervisor == nil || m.stopped {
		return nagitui.NoneSubscription[appMessage]()
	}
	events := nagitui.StreamSubscription(
		supervisorEventsSubscriptionKey,
		nagitui.BatchDelivery(maxSupervisorEventsPerUpdate, logRefreshInterval),
		supervisorEventStream(m.supervisor.Events()),
	)
	// Supervisor events are lossless and cover every Snapshot mutation. Uptime
	// is the only state that needs a periodic message while a process is running.
	if !hasVisibleUptime(m.snapshots) {
		return events
	}
	return nagitui.BatchSubscriptions(
		events,
		nagitui.EverySubscription(
			uptimeSubscriptionKey,
			uptimeInterval,
			nagitui.LatestDelivery(),
			func() appMessage {
				return appMessage{kind: messageUptimeTick, timestamp: time.Now()}
			},
		),
	)
}

func hasVisibleUptime(snapshots []supervisor.Snapshot) bool {
	for _, snapshot := range snapshots {
		if snapshot.PID != 0 && !snapshot.StartedAt.IsZero() {
			return true
		}
	}
	return false
}

func (m *model) View(context nagitui.ViewContext) nagitui.Node[appMessage] {
	m.resize(context.Size)
	rows := snapshotRows(m.snapshots, m.now)
	tableBodyHeight := maxInt(0, m.tableHeight-1)
	processTable := widget.NewTable(
		processTableID,
		defaultColumns(m.width),
		rows,
		m.selected,
		func(index int) appMessage {
			return appMessage{kind: messageProcessSelected, index: index}
		},
	).Viewport(processTableScrollID, nagitui.Fixed(uint32(tableBodyHeight))).Node()

	m.updateLogMetrics()
	logViewport := m.logViewportNode()

	selected := m.selectedProcess()
	if selected == "" {
		selected = "-"
	}

	root := nagitui.Column(
		paddedText("Process Deck", nagitui.Style{Bold: true}).WithLength(nagitui.Fixed(1)),
		processTable.WithLength(nagitui.Fixed(uint32(m.tableHeight))),
		paddedText(fmt.Sprintf("Logs: %s", selected), nagitui.Style{Bold: true}).WithLength(nagitui.Fixed(1)),
		logViewport,
		paddedText(m.status, nagitui.Style{}).WithLength(nagitui.Fixed(1)),
		paddedText(m.footerText(), nagitui.Style{}).WithLength(nagitui.Fixed(1)),
	).Focusable(rootNodeID).OnEvent(rootNodeID, handleRootEvent)
	return root
}

func supervisorEventStream(events <-chan supervisor.Event) nagitui.SubscriptionStream[appMessage] {
	return func(ctx context.Context, sink nagitui.SubscriptionSink[appMessage]) {
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-events:
				if !ok {
					sink.Send(appMessage{kind: messageSupervisorEventsClosed})
					return
				}
				if !sink.Send(appMessage{kind: messageSupervisorEvent, event: event}) {
					return
				}
			}
		}
	}
}

func handleRootEvent(event vt.Event) nagitui.EventResult[appMessage] {
	msg, ok := messageForEvent(event)
	if !ok {
		return nagitui.IgnoreResult[appMessage]()
	}
	return nagitui.MessageResult(msg)
}

func mapEvent(event vt.Event) nagitui.EventAction[appMessage] {
	msg, ok := messageForEvent(event)
	if !ok {
		return nagitui.IgnoreAction[appMessage]()
	}
	return nagitui.MessageAction(msg)
}

func messageForEvent(event vt.Event) (appMessage, bool) {
	if event.Kind == vt.EventText {
		return messageForText(event.Text)
	}
	if event.Kind != vt.EventKey || event.Key.Action == vt.KeyRelease {
		return appMessage{}, false
	}

	modifiers := event.Key.Modifiers
	if event.Key.Code == vt.KeyCharacter && modifiers.Control && !modifiers.Alt && !modifiers.Meta {
		switch event.Key.Character {
		case 'c':
			return actionMessage(actionQuit), true
		case 'u':
			return actionMessage(actionLogHalfPageUp), true
		case 'd':
			return actionMessage(actionLogHalfPageDown), true
		}
	}
	if modifiers.Alt || modifiers.Control || modifiers.Meta {
		return appMessage{}, false
	}
	if event.Key.Code == vt.KeyCharacter {
		return messageForText(string(event.Key.Character))
	}

	action := actionNone
	switch event.Key.Code {
	case vt.KeyUp:
		action = actionSelectPrevious
	case vt.KeyDown:
		action = actionSelectNext
	case vt.KeyPageUp:
		action = actionLogPageUp
	case vt.KeyPageDown:
		action = actionLogPageDown
	case vt.KeyHome:
		action = actionLogTop
	case vt.KeyEnd:
		action = actionLogBottom
	case vt.KeyLeft:
		action = actionLogLeft
	case vt.KeyRight:
		action = actionLogRight
	default:
		return appMessage{}, false
	}
	return actionMessage(action), true
}

func messageForText(text string) (appMessage, bool) {
	action := actionNone
	switch text {
	case "q":
		action = actionQuit
	case "k":
		action = actionSelectPrevious
	case "j":
		action = actionSelectNext
	case "s":
		action = actionStopProcess
	case "a":
		action = actionStartProcess
	case "r":
		action = actionRestartProcess
	case "f":
		action = actionToggleFollow
	case "w":
		action = actionToggleWrap
	case "h":
		action = actionLogLeft
	case "l":
		action = actionLogRight
	default:
		return appMessage{}, false
	}
	return actionMessage(action), true
}

func actionMessage(action userAction) appMessage {
	return appMessage{kind: messageAction, action: action}
}

func (m *model) handleAction(action userAction) nagitui.Effect[appMessage] {
	switch action {
	case actionQuit:
		if !m.quitting {
			m.quitting = true
			m.status = "stopping processes"
			m.cancel()
		}
		return nagitui.NoneEffect[appMessage]()
	case actionSelectPrevious:
		return m.moveSelection(-1)
	case actionSelectNext:
		return m.moveSelection(1)
	case actionStopProcess:
		if m.supervisor == nil {
			return nagitui.NoneEffect[appMessage]()
		}
		return m.runProcessCommand("stop", m.supervisor.StopProcess)
	case actionStartProcess:
		if m.supervisor == nil {
			return nagitui.NoneEffect[appMessage]()
		}
		return m.runProcessCommand("start", m.supervisor.StartProcess)
	case actionRestartProcess:
		if m.supervisor == nil {
			return nagitui.NoneEffect[appMessage]()
		}
		return m.runProcessCommand("restart", m.supervisor.RestartProcess)
	case actionToggleFollow:
		return m.toggleFollow()
	case actionToggleWrap:
		return m.toggleLogWrap()
	case actionLogPageUp, actionLogPageDown, actionLogHalfPageUp, actionLogHalfPageDown, actionLogTop, actionLogBottom:
		return m.scrollLogVertically(action)
	case actionLogLeft, actionLogRight:
		return m.scrollLogHorizontally(action)
	default:
		return nagitui.NoneEffect[appMessage]()
	}
}

func (m *model) moveSelection(delta int) nagitui.Effect[appMessage] {
	if len(m.snapshots) == 0 {
		return nagitui.NoneEffect[appMessage]()
	}
	return m.selectProcess(minInt(maxInt(m.selected+delta, 0), len(m.snapshots)-1))
}

func (m *model) selectProcess(index int) nagitui.Effect[appMessage] {
	if len(m.snapshots) == 0 {
		m.selected = 0
		return nagitui.NoneEffect[appMessage]()
	}
	index = minInt(maxInt(index, 0), len(m.snapshots)-1)
	if index == m.selected {
		return nagitui.NoneEffect[appMessage]()
	}
	m.selected = index
	m.syncLogCache()
	if !m.follow {
		return nagitui.NoneEffect[appMessage]()
	}
	m.logOffset.Y = ^uint32(0)
	return nagitui.ScrollToEffect[appMessage](logVerticalScrollID, m.logOffset)
}

func (m *model) runProcessCommand(action string, operation func(string) error) nagitui.Effect[appMessage] {
	name := m.selectedProcess()
	if name == "" || operation == nil {
		return nagitui.NoneEffect[appMessage]()
	}
	progress := action + "ing"
	if action == "stop" {
		progress = "stopping"
	}
	m.status = fmt.Sprintf("%s %s", progress, name)
	return nagitui.RunEffect(func(context.Context) appMessage {
		return appMessage{
			kind: messageCommandDone,
			command: commandDone{
				action:  action,
				process: name,
				err:     operation(name),
			},
		}
	})
}

func (m *model) toggleFollow() nagitui.Effect[appMessage] {
	m.follow = !m.follow
	if !m.follow {
		m.logOffset.Y = m.logMaximum.Y
		m.status = "log follow disabled"
		return nagitui.NoneEffect[appMessage]()
	}
	m.logOffset.Y = ^uint32(0)
	m.status = "log follow enabled"
	return nagitui.ScrollToEffect[appMessage](logVerticalScrollID, m.logOffset)
}

func (m *model) toggleLogWrap() nagitui.Effect[appMessage] {
	m.logWrap = !m.logWrap
	m.logOffset.X = 0
	m.rebuildCachedLogPresentation()
	if m.logWrap {
		m.status = "log wrap enabled"
		return nagitui.NoneEffect[appMessage]()
	}
	m.status = "log wrap disabled"
	return nagitui.ScrollToEffect[appMessage](logHorizontalScrollID, m.logOffset)
}

func (m *model) scrollLogVertically(action userAction) nagitui.Effect[appMessage] {
	if m.follow {
		m.follow = false
		m.logOffset.Y = m.logMaximum.Y
		m.status = "log follow disabled"
	}

	page := uint32(maxInt(m.logHeight, 1))
	halfPage := uint32(maxInt(m.logHeight/2, 1))
	switch action {
	case actionLogPageUp:
		m.logOffset.Y = subtractUint32(m.logOffset.Y, page)
	case actionLogPageDown:
		m.logOffset.Y = minUint32(m.logMaximum.Y, addUint32(m.logOffset.Y, page))
	case actionLogHalfPageUp:
		m.logOffset.Y = subtractUint32(m.logOffset.Y, halfPage)
	case actionLogHalfPageDown:
		m.logOffset.Y = minUint32(m.logMaximum.Y, addUint32(m.logOffset.Y, halfPage))
	case actionLogTop:
		m.logOffset.Y = 0
	case actionLogBottom:
		m.logOffset.Y = m.logMaximum.Y
	}
	return nagitui.ScrollToEffect[appMessage](logVerticalScrollID, m.logOffset)
}

func (m *model) scrollLogHorizontally(action userAction) nagitui.Effect[appMessage] {
	if m.logWrap {
		return nagitui.NoneEffect[appMessage]()
	}
	step := uint32(logHorizontalScrollStep)
	if action == actionLogLeft {
		m.logOffset.X = subtractUint32(m.logOffset.X, step)
	} else {
		m.logOffset.X = minUint32(m.logMaximum.X, addUint32(m.logOffset.X, step))
	}
	return nagitui.ScrollToEffect[appMessage](logHorizontalScrollID, m.logOffset)
}

func (m *model) handleSupervisorEvent(event supervisor.Event) nagitui.Effect[appMessage] {
	if event.Kind == supervisor.EventProcessLogLine {
		m.applyLogEvent(event)
		return nagitui.NoneEffect[appMessage]()
	}

	switch event.Kind {
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
	m.updateTerminalStatus()
	if m.stopped {
		return nagitui.ExitEffect[appMessage]()
	}
	return nagitui.NoneEffect[appMessage]()
}

func (m *model) applyLogEvent(event supervisor.Event) {
	if event.Process != m.selectedProcess() {
		return
	}
	if !event.LogStateValid {
		m.logSyncNeeded = true
		m.syncLogCache()
		return
	}
	if event.LogCursor < m.logCursor {
		return
	}
	if event.LogCursor == m.logCursor {
		if event.LogRetained != len(m.logLines) {
			m.logSyncNeeded = true
			m.syncLogCache()
		}
		return
	}
	if event.LogCursor != m.logCursor+1 {
		m.logSyncNeeded = true
		m.syncLogCache()
		return
	}
	if event.LogRetained == 0 {
		m.clearCachedLogLines()
		m.logCursor = event.LogCursor
		m.logSyncNeeded = false
		return
	}

	expectedExisting := event.LogRetained - 1
	if expectedExisting > len(m.logLines) {
		m.logSyncNeeded = true
		m.syncLogCache()
		return
	}
	if drop := len(m.logLines) - expectedExisting; drop > 0 {
		m.dropCachedLogLines(drop)
	}
	m.appendCachedLogLine(supervisor.LogEntry{
		Stream: event.Stream,
		Line:   event.Line,
		Time:   event.Time,
	})
	if excess := len(m.logLines) - event.LogRetained; excess > 0 {
		m.dropCachedLogLines(excess)
	}
	m.logCursor = event.LogCursor
	m.logSyncNeeded = false
}

func (m *model) updateTerminalStatus() {
	if !m.quitting && !m.stopped && m.allTerminal() {
		if m.anyFailed() {
			m.status = "one or more processes failed"
		} else {
			m.status = "all processes stopped"
		}
	}
}

func (m *model) refreshSnapshots() {
	if m.supervisor == nil {
		m.snapshots = nil
		m.selected = 0
		return
	}
	selected := m.selectedProcess()
	m.snapshots = m.supervisor.Snapshot()
	if len(m.snapshots) == 0 {
		m.selected = 0
		return
	}
	if selected != "" {
		for index, snapshot := range m.snapshots {
			if snapshot.Name == selected {
				m.selected = index
				return
			}
		}
	}
	m.selected = minInt(maxInt(m.selected, 0), len(m.snapshots)-1)
}

func (m *model) selectedProcess() string {
	if m.selected < 0 || m.selected >= len(m.snapshots) {
		return ""
	}
	return m.snapshots[m.selected].Name
}

func (m *model) resize(size nagitui.Size) {
	if size.Width == 0 || size.Height == 0 {
		return
	}
	widthChanged := m.width != int(size.Width)
	m.width = int(size.Width)
	m.height = int(size.Height)
	m.tableHeight = minInt(maxInt(6, m.height/2), maxInt(6, m.height-8))
	m.logHeight = maxInt(3, m.height-m.tableHeight-6)
	if widthChanged {
		m.recalculateCachedLogHeights()
	}
}

func (m *model) syncLogCache() {
	name := m.selectedProcess()
	if name != m.logProcess {
		m.resetLogCache(name)
	}
	if name == "" || m.supervisor == nil || !m.logSyncNeeded {
		return
	}

	batch := m.supervisor.LogsSince(name, m.logCursor)
	expectedExisting := batch.Retained - len(batch.Entries)
	if !batch.Reset && expectedExisting > len(m.logLines) {
		batch = m.supervisor.LogsSince(name, 0)
	}
	m.applyLogBatch(batch)
	m.logSyncNeeded = false
}

func (m *model) applyLogBatch(batch supervisor.LogBatch) {
	if batch.Reset {
		m.clearCachedLogLines()
	}

	existing := batch.Retained - len(batch.Entries)
	if existing < 0 {
		existing = 0
	}
	if drop := len(m.logLines) - existing; drop > 0 {
		m.dropCachedLogLines(drop)
	}
	for _, entry := range batch.Entries {
		m.appendCachedLogLine(entry)
	}
	if excess := len(m.logLines) - batch.Retained; excess > 0 {
		m.dropCachedLogLines(excess)
	}
	m.logCursor = batch.Cursor
}

func (m *model) resetLogCache(process string) {
	m.logProcess = process
	m.logCursor = 0
	m.logSyncNeeded = true
	m.clearCachedLogLines()
}

func (m *model) clearCachedLogLines() {
	for index := range m.logLines {
		m.logLines[index] = cachedLogLine{}
	}
	for index := range m.logNodes {
		m.logNodes[index] = nagitui.Node[appMessage]{}
	}
	m.logLines = nil
	m.logNodes = nil
	m.logNextY = 0
	m.logContentWidth = 0
	m.logContentHeight = 0
	m.logWidthCounts = make(map[int]int)
}

func (m *model) appendCachedLogLine(entry supervisor.LogEntry) {
	if m.logWidthCounts == nil {
		m.logWidthCounts = make(map[int]int)
	}
	content := formatLogEntry(entry)
	visible := visibleLogText(content)
	line := cachedLogLine{
		content: content,
		visible: visible,
		width:   celltext.Width(visible, celltext.ModernWidth()),
		height:  m.logLineHeight(visible),
		start:   m.logNextY,
	}
	m.logLines = append(m.logLines, line)
	m.logNodes = append(m.logNodes, logLineNode(content, m.logWrap))
	m.logNextY += line.height
	m.logContentHeight += line.height
	m.logWidthCounts[line.width]++
	m.logContentWidth = maxInt(m.logContentWidth, line.width)
}

func (m *model) dropCachedLogLines(count int) {
	count = minInt(maxInt(count, 0), len(m.logLines))
	if count == 0 {
		return
	}
	for index := range count {
		m.logNodes[index] = nagitui.Node[appMessage]{}
	}
	m.logNodes = m.logNodes[count:]
	maximumRemoved := false
	for index := range count {
		line := m.logLines[index]
		m.logContentHeight -= line.height
		m.logWidthCounts[line.width]--
		if m.logWidthCounts[line.width] == 0 {
			delete(m.logWidthCounts, line.width)
			maximumRemoved = maximumRemoved || line.width == m.logContentWidth
		}
		m.logLines[index] = cachedLogLine{}
	}
	m.logLines = m.logLines[count:]
	if len(m.logLines) == 0 {
		m.logNextY = 0
	}
	if maximumRemoved {
		m.logContentWidth = 0
		for width := range m.logWidthCounts {
			m.logContentWidth = maxInt(m.logContentWidth, width)
		}
	}
	m.logContentHeight = maxInt(m.logContentHeight, 0)
}

func (m *model) rebuildCachedLogPresentation() {
	m.logContentHeight = 0
	m.logNextY = 0
	for index := range m.logNodes {
		m.logNodes[index] = nagitui.Node[appMessage]{}
	}
	m.logNodes = nil
	for index := range m.logLines {
		line := &m.logLines[index]
		line.start = m.logNextY
		line.height = m.logLineHeight(line.visible)
		m.logNodes = append(m.logNodes, logLineNode(line.content, m.logWrap))
		m.logNextY += line.height
		m.logContentHeight += line.height
	}
}

func (m *model) recalculateCachedLogHeights() {
	m.logContentHeight = 0
	m.logNextY = 0
	for index := range m.logLines {
		line := &m.logLines[index]
		line.start = m.logNextY
		line.height = m.logLineHeight(line.visible)
		m.logNextY += line.height
		m.logContentHeight += line.height
	}
}

func (m *model) logLineHeight(visible string) int {
	if !m.logWrap {
		return 1
	}
	lines := celltext.IterateWrappedLines(visible, maxInt(m.width, 1), celltext.ModernWidth())
	height := 0
	for {
		if _, ok := lines.Next(); !ok {
			break
		}
		height++
	}
	return maxInt(1, height)
}

func (m *model) updateLogMetrics() {
	contentHeight := m.logContentHeight
	contentWidth := m.logContentWidth
	if len(m.logLines) == 0 {
		contentHeight = 1
		contentWidth = celltext.Width("no logs", celltext.ModernWidth())
	}
	m.logMaximum = nagitui.ScrollOffset{
		X: uint32(maxInt(0, contentWidth-m.width)),
		Y: uint32(maxInt(0, contentHeight-m.logHeight)),
	}
	if m.logWrap {
		m.logOffset.X = 0
	} else {
		m.logOffset.X = minUint32(m.logOffset.X, m.logMaximum.X)
	}
	if m.follow {
		m.logOffset.Y = m.logMaximum.Y
	} else {
		m.logOffset.Y = minUint32(m.logOffset.Y, m.logMaximum.Y)
	}
}

func (m *model) logViewportNode() nagitui.Node[appMessage] {
	contentSize := m.logVirtualContentSize()
	vertical := nagitui.VirtualScrollViewportWithOptions(
		logVerticalScrollID,
		contentSize,
		nagitui.ScrollViewportOptions[appMessage]{
			Axis:       nagitui.ScrollAxisVertical,
			StickToEnd: m.follow,
		},
		m.virtualLogFragment,
	).TabStop(false)
	if m.logWrap {
		return vertical.WithLength(nagitui.Fixed(uint32(m.logHeight)))
	}
	return nagitui.ScrollViewportWithOptions(
		logHorizontalScrollID,
		vertical,
		nagitui.ScrollViewportOptions[appMessage]{Axis: nagitui.ScrollAxisHorizontal},
	).TabStop(false).WithLength(nagitui.Fixed(uint32(m.logHeight)))
}

func (m *model) logVirtualContentSize() nagitui.Size {
	width := m.logContentWidth
	height := m.logContentHeight
	if len(m.logLines) == 0 {
		width = celltext.Width("no logs", celltext.ModernWidth())
		height = 1
	}
	if m.logWrap {
		width = maxInt(m.width, 1)
	}
	return nagitui.Size{
		Width:  uint32(maxInt(width, 1)),
		Height: uint32(maxInt(height, 1)),
	}
}

func (m *model) virtualLogFragment(viewport nagitui.VirtualViewport) nagitui.VirtualFragment[appMessage] {
	if len(m.logLines) == 0 {
		return nagitui.NewVirtualFragment(
			nagitui.ScrollOffset{},
			nagitui.Column(logLineNode("no logs", m.logWrap)),
		)
	}
	start, end, origin := m.visibleLogRange(int(viewport.Offset.Y), int(viewport.Size.Height))
	nodes := append([]nagitui.Node[appMessage](nil), m.logNodes[start:end]...)
	return nagitui.NewVirtualFragment(
		nagitui.ScrollOffset{Y: uint32(origin)},
		nagitui.Column(nodes...),
	)
}

func (m *model) visibleLogRange(offset, height int) (start, end, origin int) {
	if len(m.logLines) == 0 {
		return 0, 0, 0
	}
	base := m.logLines[0].start
	visibleStart := base + maxInt(offset, 0)
	visibleEnd := visibleStart + maxInt(height, 1)
	start = sort.Search(len(m.logLines), func(index int) bool {
		line := m.logLines[index]
		return line.start+line.height > visibleStart
	})
	if start == len(m.logLines) {
		start = len(m.logLines) - 1
	}
	end = start
	for end < len(m.logLines) && m.logLines[end].start < visibleEnd {
		end++
	}
	if end == start {
		end++
	}
	return start, end, m.logLines[start].start - base
}
func logLineNode(content string, wrap bool) nagitui.Node[appMessage] {
	mode := nagitui.WrapHard
	if !wrap {
		mode = nagitui.WrapNone
	}
	return nagitui.ANSIText[appMessage](content, nagitui.ANSITextOptions{
		Paragraph: nagitui.ParagraphOptions{Wrap: mode, Alignment: nagitui.AlignStart},
	})
}

func (m *model) footerText() string {
	follow := "off"
	if m.follow {
		follow = "on"
	}
	wrap := "off"
	if m.logWrap {
		wrap = "on"
	}
	if m.width < 90 {
		return fmt.Sprintf("up/k down/j select   pg log   w:%s f:%s   q quit", wrap, follow)
	}
	if m.width < 130 {
		return fmt.Sprintf("up/k down/j select  pg log  left/right xscroll  w:%s f:%s  s/a/r proc  q quit", wrap, follow)
	}
	return fmt.Sprintf("up/k down/j select   pgup/pgdn log   left/right xscroll   w wrap:%s   f follow:%s   s/a/r proc   q quit", wrap, follow)
}

func (m *model) allTerminal() bool {
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

func (m *model) anyFailed() bool {
	for _, snapshot := range m.snapshots {
		if snapshot.State == supervisor.StateFailed || snapshot.State == supervisor.StateSkipped {
			return true
		}
	}
	return false
}

func snapshotRows(snapshots []supervisor.Snapshot, now time.Time) []widget.TableRow {
	rows := make([]widget.TableRow, 0, len(snapshots))
	for _, snapshot := range snapshots {
		rows = append(rows, widget.NewTableRow(
			nagitui.NewNodeID("process-row-"+snapshot.Name),
			[]string{
				snapshot.Name,
				string(snapshot.State),
				pidText(snapshot.PID),
				fmt.Sprintf("%d", snapshot.Restarts),
				uptimeTextAt(snapshot, now),
				exitText(snapshot.ExitCode),
				snapshot.Command,
			},
		))
	}
	return rows
}

func defaultColumns(width int) []widget.TableColumn {
	columnWidths := []int{10, 8, 5, 4, 5, 4}
	targetWidths := []int{16, 10, 7, 8, 8, 5}
	available := maxInt(1, width-20)
	used := 0
	for _, columnWidth := range columnWidths {
		used += columnWidth
	}
	commandMinimum := minInt(16, maxInt(1, available-used))
	expansion := maxInt(0, available-used-commandMinimum)
	for index := range columnWidths {
		growth := minInt(targetWidths[index]-columnWidths[index], expansion)
		columnWidths[index] += growth
		expansion -= growth
		used += growth
	}
	commandWidth := maxInt(1, available-used)
	return []widget.TableColumn{
		widget.NewTableColumn("name", nagitui.Fixed(uint32(columnWidths[0]))),
		widget.NewTableColumn("state", nagitui.Fixed(uint32(columnWidths[1]))),
		widget.NewTableColumn("pid", nagitui.Fixed(uint32(columnWidths[2]))),
		widget.NewTableColumn("restarts", nagitui.Fixed(uint32(columnWidths[3]))),
		widget.NewTableColumn("uptime", nagitui.Fixed(uint32(columnWidths[4]))),
		widget.NewTableColumn("exit", nagitui.Fixed(uint32(columnWidths[5]))),
		widget.NewTableColumn("command", nagitui.Fixed(uint32(commandWidth))),
	}
}

func logViewLines(entries []supervisor.LogEntry) []string {
	if len(entries) == 0 {
		return []string{"no logs"}
	}
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		lines = append(lines, formatLogEntry(entry))
	}
	return lines
}

func formatLogEntry(entry supervisor.LogEntry) string {
	return fmt.Sprintf("%s %-8s %s", entry.Time.Format("15:04:05"), logSourceLabel(entry.Stream), entry.Line)
}

func logSourceLabel(stream string) string {
	if stream == "" {
		return "[log]"
	}
	return "[" + stream + "]"
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

func uptimeTextAt(snapshot supervisor.Snapshot, now time.Time) string {
	if snapshot.PID == 0 || snapshot.StartedAt.IsZero() {
		return "-"
	}
	elapsed := now.Sub(snapshot.StartedAt).Truncate(time.Second)
	if elapsed < 0 {
		elapsed = 0
	}
	if elapsed < time.Second {
		return "0s"
	}
	return strings.TrimSuffix(elapsed.String(), "0s")
}

func commandStatus(msg commandDone) string {
	if msg.err != nil {
		return msg.err.Error()
	}
	return fmt.Sprintf("%s %s", msg.action, msg.process)
}

func paddedText(content string, style nagitui.Style) nagitui.Node[appMessage] {
	return nagitui.Padding(
		nagitui.StyledText[appMessage](content, style),
		nagitui.Insets{Left: 1, Right: 1},
	)
}

func visibleLogText(input string) string {
	var output strings.Builder
	for index := 0; index < len(input); {
		character, width := utf8.DecodeRuneInString(input[index:])
		next := index + width
		switch character {
		case '\x1b':
			index = skipEscapeSequence(input, next)
		case '\u009b':
			index = skipControlSequence(input, next)
		case '\u009d':
			index = skipControlString(input, next, true)
		case '\u0090', '\u0098', '\u009e', '\u009f':
			index = skipControlString(input, next, false)
		default:
			if character == '\r' || character == '\n' || !unicode.IsControl(character) {
				output.WriteRune(character)
			}
			index = next
		}
	}
	return output.String()
}

func skipEscapeSequence(input string, start int) int {
	if start >= len(input) {
		return start
	}
	introducer, width := utf8.DecodeRuneInString(input[start:])
	next := start + width
	switch introducer {
	case '[':
		return skipControlSequence(input, next)
	case ']':
		return skipControlString(input, next, true)
	case 'P', 'X', '^', '_':
		return skipControlString(input, next, false)
	}
	for index := start; index < len(input); index++ {
		value := input[index]
		if value >= 0x30 && value <= 0x7e {
			return index + 1
		}
		if value < 0x20 || value > 0x2f {
			return index
		}
	}
	return len(input)
}

func skipControlSequence(input string, start int) int {
	for index := start; index < len(input); index++ {
		value := input[index]
		if value >= 0x40 && value <= 0x7e {
			return index + 1
		}
		if value == '\r' || value == '\n' || value == '\x1b' {
			return index
		}
	}
	return len(input)
}

func skipControlString(input string, start int, bellTerminated bool) int {
	for index := start; index < len(input); {
		character, width := utf8.DecodeRuneInString(input[index:])
		next := index + width
		if character == '\u009c' || bellTerminated && character == '\a' {
			return next
		}
		if character == '\x1b' && next < len(input) {
			terminator, terminatorWidth := utf8.DecodeRuneInString(input[next:])
			if terminator == '\\' {
				return next + terminatorWidth
			}
		}
		index = next
	}
	return len(input)
}

func subtractUint32(value, amount uint32) uint32 {
	if amount > value {
		return 0
	}
	return value - amount
}

func addUint32(value, amount uint32) uint32 {
	if ^uint32(0)-value < amount {
		return ^uint32(0)
	}
	return value + amount
}

func minUint32(a, b uint32) uint32 {
	if a < b {
		return a
	}
	return b
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
