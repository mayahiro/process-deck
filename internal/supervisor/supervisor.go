package supervisor

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/mayahiro/process-deck/internal/config"
	"github.com/mayahiro/process-deck/internal/logbuf"
	"github.com/mayahiro/process-deck/internal/process"
)

const (
	defaultRestart        = "no"
	defaultBackoff        = time.Second
	defaultStopSignal     = "TERM"
	defaultStopTimeout    = 10 * time.Second
	defaultLogBufferLines = 1000
)

// Options controls working-directory resolution and the supervisor lifetime.
type Options struct {
	BaseDir             string
	KeepRunningWhenDone bool
}

// Snapshot is a copy of a process's current lifecycle state.
type Snapshot struct {
	Name      string
	State     State
	PID       int
	Restarts  int
	ExitCode  *int
	StartedAt time.Time
	Command   string
}

// LogEntry is one retained output record, possibly a fragment of a long line.
type LogEntry struct {
	Stream string
	Line   string
	Time   time.Time
}

// LogCursor identifies the next retained log position for incremental reads.
type LogCursor uint64

// LogBatch contains retained log changes after a cursor.
type LogBatch struct {
	// Entries contains new retained log entries, or the complete retained
	// snapshot when Reset is true.
	Entries []LogEntry
	// Cursor is passed to the next LogsSince call.
	Cursor LogCursor
	// Retained is the current number of retained entries.
	Retained int
	// Reset reports that the caller must replace its existing cache.
	Reset bool
}

// Supervisor manages process groups, dependency ordering, restarts, and logs.
// Its command and snapshot methods may be called concurrently with Run.
type Supervisor struct {
	cfg     *config.Config
	options Options
	deps    map[string][]string

	opsMu        sync.Mutex // Serializes launches and manual lifecycle operations.
	mu           sync.Mutex
	runCtx       context.Context
	wg           sync.WaitGroup
	processes    map[string]*processRuntime
	events       chan Event
	eventsMu     sync.Mutex
	done         chan struct{}
	idle         bool
	eventsClosed bool
	stopping     bool
	hadFailed    bool
}

type processRuntime struct {
	name           string
	config         config.Process
	state          State
	pid            int
	restarts       int
	exitCode       *int
	startedAt      time.Time
	reachedRunning bool
	stopRequested  bool
	restarting     bool
	runner         *process.Runner
	generation     uint64
	restartCancel  context.CancelFunc
	finished       chan struct{}
	logs           *logbuf.Ring[LogEntry]
}

// New validates the dependency graph and prepares a supervisor without starting
// processes. The configuration must not be mutated after this call.
func New(cfg *config.Config, options Options) (*Supervisor, error) {
	deps := cfg.DependencyMap()
	if err := ValidateGraph(deps); err != nil {
		return nil, err
	}

	processes := make(map[string]*processRuntime, len(cfg.Processes))
	for _, name := range cfg.ProcessNames() {
		proc := cfg.Processes[name]
		processes[name] = &processRuntime{
			name:   name,
			config: proc,
			state:  StatePending,
			logs:   logbuf.New[LogEntry](resolveLogBufferLines(cfg.Defaults, proc)),
		}
	}

	return &Supervisor{
		cfg:       cfg,
		options:   options,
		deps:      deps,
		processes: processes,
		events:    make(chan Event, 1024),
		done:      make(chan struct{}),
	}, nil
}

// Events returns the event stream, which the caller must drain until it closes.
func (s *Supervisor) Events() <-chan Event {
	return s.events
}

// Run starts processes in dependency order and supervises them until cancellation
// or, unless KeepRunningWhenDone is set, until all processes have finished.
// The caller must drain Events concurrently. Run may only be called once.
func (s *Supervisor) Run(ctx context.Context) error {
	s.opsMu.Lock()
	s.mu.Lock()
	if s.runCtx != nil {
		s.mu.Unlock()
		s.opsMu.Unlock()
		return fmt.Errorf("runtime error: supervisor has already been run")
	}
	s.runCtx = ctx
	s.mu.Unlock()
	err := s.startInitial(ctx)
	s.opsMu.Unlock()
	if err != nil {
		s.closeEvents()
		return err
	}

	if s.options.KeepRunningWhenDone {
		<-ctx.Done()
		err = s.StopAll()
	} else {
		for {
			s.mu.Lock()
			done := s.done
			s.mu.Unlock()
			select {
			case <-ctx.Done():
				err = s.StopAll()
			case <-done:
				// A manual restart may have launched a new run since done closed.
				s.opsMu.Lock()
				s.mu.Lock()
				idle := s.idle
				if idle {
					s.stopping = true
					if s.hadFailed {
						err = fmt.Errorf("runtime error: one or more processes failed")
					}
				}
				s.mu.Unlock()
				s.opsMu.Unlock()
				if !idle {
					continue
				}
			}
			break
		}
	}

	s.wg.Wait()
	s.emitStopped()
	s.closeEvents()
	return err
}

// StartProcess starts a stopped process whose dependencies have reached running.
// A pending automatic restart is replaced by this immediate start.
func (s *Supervisor) StartProcess(name string) error {
	s.opsMu.Lock()
	defer s.opsMu.Unlock()
	return s.startManual(name)
}

func (s *Supervisor) startManual(name string) error {
	ctx, err := s.commandContext()
	if err != nil {
		return err
	}
	s.mu.Lock()
	runtime := s.processes[name]
	if runtime == nil {
		s.mu.Unlock()
		return fmt.Errorf("runtime error: unknown process %s", name)
	}
	if runtime.runner != nil || (!terminalState(runtime.state) && runtime.state != StatePending) {
		s.mu.Unlock()
		return fmt.Errorf("runtime error: process %s is already %s", name, runtime.state)
	}
	if !s.dependenciesReadyLocked(name) {
		s.mu.Unlock()
		return fmt.Errorf("runtime error: process %s dependencies are not ready", name)
	}
	s.cancelRestartLocked(runtime)
	runtime.reachedRunning = false
	finished := runtime.finished
	s.mu.Unlock()
	if finished != nil {
		<-finished // Publish the previous run's exit before a replacement starts.
	}
	if err := s.startProcess(ctx, name); err != nil {
		s.markStartFailed(name, err)
		return err
	}
	return nil
}

// StopProcess stops the named process and its transitive dependents, stopping
// dependents first. It also cancels their pending automatic restarts.
func (s *Supervisor) StopProcess(name string) error {
	s.opsMu.Lock()
	defer s.opsMu.Unlock()
	order, err := s.stopOrder(name)
	if err != nil {
		return err
	}
	return s.stopProcesses(order)
}

// RestartProcess stops the named process and its dependents, then starts the
// named process and previously active dependents in dependency order.
// Dependents that were already stopped are left stopped.
func (s *Supervisor) RestartProcess(name string) error {
	s.opsMu.Lock()
	defer s.opsMu.Unlock()
	if _, err := s.commandContext(); err != nil {
		return err
	}
	order, err := s.stopOrder(name)
	if err != nil {
		return err
	}
	resume := map[string]bool{name: true}
	s.mu.Lock()
	for _, dependent := range order {
		runtime := s.processes[dependent]
		if runtime.runner != nil || runtime.restarting || runtime.state == StatePending {
			resume[dependent] = true
		}
	}
	s.mu.Unlock()
	if err := s.stopProcesses(order); err != nil {
		return err
	}
	var startErrors []error
	for i := len(order) - 1; i >= 0; i-- {
		if resume[order[i]] {
			if err := s.startManual(order[i]); err != nil {
				startErrors = append(startErrors, err)
			}
		}
	}
	return errors.Join(startErrors...)
}

// StopAll prevents further launches and stops all process groups in reverse
// dependency order, waiting for their output to drain.
func (s *Supervisor) StopAll() error {
	s.opsMu.Lock()
	defer s.opsMu.Unlock()
	s.mu.Lock()
	s.stopping = true
	s.mu.Unlock()
	order, err := ReverseDependencyOrder(s.deps)
	if err != nil {
		return err
	}
	return s.stopProcesses(order)
}

// stopOrder returns the selected process and all transitive dependents in
// reverse dependency order. The dependency graph is immutable after New.
func (s *Supervisor) stopOrder(name string) ([]string, error) {
	if _, ok := s.deps[name]; !ok {
		return nil, fmt.Errorf("runtime error: unknown process %s", name)
	}
	selected := make(map[string]bool)
	var visit func(string)
	visit = func(name string) {
		if selected[name] {
			return
		}
		selected[name] = true
		for _, dependent := range DependentsOf(s.deps, name) {
			visit(dependent)
		}
	}
	visit(name)
	order, err := ReverseDependencyOrder(s.deps)
	if err != nil {
		return nil, err
	}
	filtered := order[:0]
	for _, name := range order {
		if selected[name] {
			filtered = append(filtered, name)
		}
	}
	return filtered, nil
}

// stopProcesses is called with opsMu held. Cancel every restart before waiting
// on the first group, so other affected processes cannot restart in the meantime.
func (s *Supervisor) stopProcesses(order []string) error {
	var events []Event
	s.mu.Lock()
	for _, name := range order {
		runtime := s.processes[name]
		runtime.stopRequested = true
		runtime.reachedRunning = false
		s.cancelRestartLocked(runtime)
		if runtime.runner == nil && !terminalState(runtime.state) {
			if event, ok := s.setStateLocked(runtime, StateExited); ok {
				events = append(events, event)
			}
		}
	}
	s.mu.Unlock()
	s.emitEvents(events)

	var stopErrors []error
	for _, name := range order {
		s.mu.Lock()
		runtime := s.processes[name]
		runner, finished := runtime.runner, runtime.finished
		event, changed := Event{}, false
		if runner != nil {
			event, changed = s.setStateLocked(runtime, StateStopping)
		}
		s.mu.Unlock()
		if changed {
			s.emit(event)
		}
		if runner != nil {
			if err := runner.Stop(); err != nil {
				err = fmt.Errorf("runtime error: failed to stop process %s: %w", name, err)
				stopErrors = append(stopErrors, err)
				s.emit(Event{Kind: EventSupervisorError, Process: name, Error: err, Time: time.Now()})
			} else {
				<-finished
			}
		}
	}
	s.mu.Lock()
	s.checkDoneLocked()
	s.mu.Unlock()
	return errors.Join(stopErrors...)
}

func (s *Supervisor) cancelRestartLocked(runtime *processRuntime) {
	if runtime.restartCancel != nil {
		runtime.restartCancel()
		runtime.restartCancel = nil
	}
	runtime.restarting = false
}

// Snapshot returns process states sorted by name.
func (s *Supervisor) Snapshot() []Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	names := sortedKeys(s.deps)
	snapshots := make([]Snapshot, 0, len(names))
	for _, name := range names {
		runtime := s.processes[name]
		snapshots = append(snapshots, Snapshot{
			Name:      name,
			State:     runtime.state,
			PID:       runtime.pid,
			Restarts:  runtime.restarts,
			ExitCode:  cloneExitCode(runtime.exitCode),
			StartedAt: runtime.startedAt,
			Command:   commandText(runtime.config),
		})
	}
	return snapshots
}

// Logs returns retained output in capture order, or nil for an unknown process.
func (s *Supervisor) Logs(name string) []LogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()

	runtime := s.processes[name]
	if runtime == nil {
		return nil
	}
	return runtime.logs.Items()
}

// LogsSince returns retained log changes after cursor.
func (s *Supervisor) LogsSince(name string, cursor LogCursor) LogBatch {
	s.mu.Lock()
	defer s.mu.Unlock()

	runtime := s.processes[name]
	if runtime == nil {
		return LogBatch{Reset: cursor != 0}
	}
	entries, next, retained, reset := runtime.logs.ItemsSince(uint64(cursor))
	return LogBatch{
		Entries:  entries,
		Cursor:   LogCursor(next),
		Retained: retained,
		Reset:    reset,
	}
}

func (s *Supervisor) startInitial(ctx context.Context) error {
	layers, err := StartupLayers(s.deps)
	if err != nil {
		return err
	}

	for _, layer := range layers {
		for _, name := range layer {
			if ctx.Err() != nil {
				return nil
			}
			if !s.dependenciesReady(name) {
				s.skipProcess(name)
				continue
			}
			if err := s.startProcess(ctx, name); err != nil {
				s.markStartFailed(name, err)
			}
		}
	}

	s.mu.Lock()
	s.checkDoneLocked()
	s.mu.Unlock()
	return nil
}

// startProcess requires opsMu, so shutdown and replacement launches cannot
// pass a command that is still being created.
func (s *Supervisor) startProcess(ctx context.Context, name string) error {
	var events []Event

	s.mu.Lock()
	if s.stopping || ctx.Err() != nil {
		s.mu.Unlock()
		return fmt.Errorf("runtime error: supervisor is stopping")
	}
	runtime := s.processes[name]
	s.cancelRestartLocked(runtime)
	runtime.generation++
	generation := runtime.generation
	runtime.stopRequested = false
	if s.idle {
		s.idle = false
		s.done = make(chan struct{})
	}
	runtime.exitCode = nil
	if event, ok := s.setStateLocked(runtime, StateStarting); ok {
		events = append(events, event)
	}
	spec, err := s.processSpec(runtime)
	if err != nil {
		s.mu.Unlock()
		s.emitEvents(events)
		return err
	}
	runner := process.NewRunner(spec)
	runtime.runner = runner
	runtime.finished = make(chan struct{})
	s.mu.Unlock()
	s.emitEvents(events)

	run, err := runner.Start()
	if err != nil {
		return err
	}

	events = nil

	s.mu.Lock()
	runtime.pid = run.PID
	runtime.startedAt = time.Now()
	runtime.reachedRunning = true
	if event, ok := s.setStateLocked(runtime, StateRunning); ok {
		events = append(events, event)
	}
	events = append(events, Event{
		Kind:    EventProcessStarted,
		Process: name,
		State:   StateRunning,
		PID:     run.PID,
		Time:    time.Now(),
	})
	s.mu.Unlock()
	s.emitEvents(events)

	logsDone := make(chan struct{})
	go func() {
		s.forwardLogs(name, run.Logs)
		close(logsDone)
	}()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.waitProcess(ctx, name, generation, run.Done, logsDone)
	}()
	return nil
}

func (s *Supervisor) waitProcess(ctx context.Context, name string, generation uint64, done <-chan process.Result, logsDone <-chan struct{}) {
	result := <-done
	<-logsDone
	var events []Event
	s.mu.Lock()
	runtime := s.processes[name]
	if runtime.generation != generation {
		s.mu.Unlock()
		return
	}
	runtime.pid = 0
	runtime.exitCode = &result.ExitCode
	runtime.runner = nil
	finished := runtime.finished

	state := StateExited
	if !runtime.stopRequested && (result.ExitCode != 0 || result.SupervisionErr != nil) {
		state = StateFailed
	}
	// Supervision failures require attention rather than launching a replacement
	// while an old group might still be alive.
	restart := !s.stopping && !runtime.stopRequested && result.SupervisionErr == nil && shouldRestart(resolveRestart(s.cfg.Defaults, runtime.config), result.ExitCode)
	if (state == StateFailed && !restart) || result.SupervisionErr != nil {
		s.hadFailed = true
	}
	if event, ok := s.setStateLocked(runtime, state); ok {
		events = append(events, event)
	}
	events = append(events, Event{
		Kind: EventProcessExited, Process: name, State: state,
		ExitCode: cloneExitCode(runtime.exitCode), Error: result.Err, Time: time.Now(),
	})
	if result.SupervisionErr != nil {
		events = append(events, Event{Kind: EventSupervisorError, Process: name, Error: result.SupervisionErr, Time: time.Now()})
	}
	if !restart {
		s.checkDoneLocked()
		s.mu.Unlock()
		s.emitEvents(events)
		close(finished)
		return
	}

	runtime.restarts++
	runtime.restarting = true
	runtime.exitCode = nil
	restartCtx, cancel := context.WithCancel(ctx)
	runtime.restartCancel = cancel
	defer cancel()
	if event, ok := s.setStateLocked(runtime, StatePending); ok {
		events = append(events, event)
	}
	backoff := resolveBackoff(s.cfg.Defaults, runtime.config)
	events = append(events, Event{
		Kind: EventProcessRestartScheduled, Process: name, State: StatePending,
		Restarts: runtime.restarts, Time: time.Now(),
	})
	s.mu.Unlock()
	s.emitEvents(events)
	close(finished)

	timer := time.NewTimer(backoff)
	defer timer.Stop()
	select {
	case <-restartCtx.Done():
		return // The cancelling operation owns the resulting state.
	case <-timer.C:
	}
	s.opsMu.Lock()
	defer s.opsMu.Unlock()
	s.mu.Lock()
	valid := restartCtx.Err() == nil && !s.stopping && runtime.generation == generation && runtime.restarting && !runtime.stopRequested
	s.mu.Unlock()
	if !valid {
		return
	}
	if err := s.startProcess(ctx, name); err != nil {
		s.markStartFailed(name, err)
	}
}

func (s *Supervisor) forwardLogs(name string, logs <-chan process.LogLine) {
	for line := range logs {
		entry := LogEntry{
			Stream: line.Stream,
			Line:   line.Line,
			Time:   line.Time,
		}
		var cursor LogCursor
		retained := 0
		s.mu.Lock()
		runtime := s.processes[name]
		if runtime != nil {
			runtime.logs.Add(entry)
			revision, count := runtime.logs.State()
			cursor = LogCursor(revision)
			retained = count
		}
		s.mu.Unlock()

		if runtime == nil {
			continue
		}
		s.emit(Event{
			Kind:          EventProcessLogLine,
			Process:       name,
			Stream:        entry.Stream,
			Line:          entry.Line,
			LogCursor:     cursor,
			LogRetained:   retained,
			LogStateValid: true,
			Time:          entry.Time,
		})
	}
}

func (s *Supervisor) dependenciesReady(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.dependenciesReadyLocked(name)
}

func (s *Supervisor) dependenciesReadyLocked(name string) bool {
	for _, dep := range s.deps[name] {
		runtime := s.processes[dep]
		if !runtime.reachedRunning || runtime.state == StateSkipped {
			return false
		}
	}
	return true
}

func (s *Supervisor) skipProcess(name string) {
	s.mu.Lock()

	runtime := s.processes[name]
	s.hadFailed = true
	events := make([]Event, 0, 2)
	if event, ok := s.setStateLocked(runtime, StateSkipped); ok {
		events = append(events, event)
	}
	events = append(events, Event{
		Kind:    EventProcessSkipped,
		Process: name,
		State:   StateSkipped,
		Time:    time.Now(),
	})
	s.mu.Unlock()
	s.emitEvents(events)
}

func (s *Supervisor) markStartFailed(name string, err error) {
	s.mu.Lock()

	runtime := s.processes[name]
	exitCode := 1
	runtime.exitCode = &exitCode
	runtime.pid = 0
	if runtime.runner != nil && runtime.finished != nil {
		close(runtime.finished)
	}
	runtime.runner = nil
	runtime.restarting = false
	runtime.stopRequested = false
	s.hadFailed = true
	events := make([]Event, 0, 2)
	if event, ok := s.setStateLocked(runtime, StateFailed); ok {
		events = append(events, event)
	}
	events = append(events, Event{
		Kind:    EventSupervisorError,
		Process: name,
		State:   StateFailed,
		Error:   fmt.Errorf("runtime error: failed to start process %s: %w", name, err),
		Time:    time.Now(),
	})
	s.checkDoneLocked()
	s.mu.Unlock()
	s.emitEvents(events)
}

func (s *Supervisor) processSpec(runtime *processRuntime) (process.Spec, error) {
	sig, err := process.ParseSignal(resolveStopSignal(s.cfg.Defaults, runtime.config))
	if err != nil {
		return process.Spec{}, fmt.Errorf("processes.%s.stop_signal: %w", runtime.name, err)
	}

	return process.Spec{
		Name:        runtime.name,
		Cmd:         runtime.config.Cmd,
		Exec:        append([]string(nil), runtime.config.Exec...),
		CWD:         resolveCWD(s.options.BaseDir, runtime.config.CWD),
		EnvFiles:    append([]string(nil), runtime.config.EnvFile...),
		Env:         cloneEnv(runtime.config.Env),
		StopSignal:  sig,
		StopTimeout: resolveStopTimeout(s.cfg.Defaults, runtime.config),
		PTY:         resolvePTY(s.cfg.Defaults, runtime.config),
	}, nil
}

func (s *Supervisor) setStateLocked(runtime *processRuntime, state State) (Event, bool) {
	if runtime.state == state {
		return Event{}, false
	}
	runtime.state = state
	return Event{
		Kind:     EventProcessStateChanged,
		Process:  runtime.name,
		State:    state,
		PID:      runtime.pid,
		Restarts: runtime.restarts,
		ExitCode: cloneExitCode(runtime.exitCode),
		Time:     time.Now(),
	}, true
}

func (s *Supervisor) checkDoneLocked() {
	for _, runtime := range s.processes {
		if runtime.restarting || !terminalState(runtime.state) {
			return
		}
	}
	if !s.idle {
		s.idle = true
		close(s.done)
	}
}

func (s *Supervisor) emitStopped() {
	s.emit(Event{
		Kind: EventSupervisorStopped,
		Time: time.Now(),
	})
}

func (s *Supervisor) emit(event Event) {
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	if s.eventsClosed {
		return
	}
	s.events <- event
}

func (s *Supervisor) emitEvents(events []Event) {
	for _, event := range events {
		s.emit(event)
	}
}

func (s *Supervisor) closeEvents() {
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	if s.eventsClosed {
		return
	}
	close(s.events)
	s.eventsClosed = true
}

func (s *Supervisor) commandContext() (context.Context, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.runCtx == nil {
		return nil, fmt.Errorf("runtime error: supervisor is not running")
	}
	if s.stopping {
		return nil, fmt.Errorf("runtime error: supervisor is stopping")
	}
	if err := s.runCtx.Err(); err != nil {
		return nil, err
	}
	return s.runCtx, nil
}

func resolveCWD(baseDir string, cwd string) string {
	if cwd == "" {
		return baseDir
	}
	if filepath.IsAbs(cwd) || baseDir == "" {
		return cwd
	}
	return filepath.Join(baseDir, cwd)
}

func resolveRestart(defaults config.Defaults, proc config.Process) string {
	if proc.Restart != "" {
		return proc.Restart
	}
	if defaults.Restart != "" {
		return defaults.Restart
	}
	return defaultRestart
}

func resolveBackoff(defaults config.Defaults, proc config.Process) time.Duration {
	return resolveDuration(proc.Backoff, defaults.Backoff, defaultBackoff)
}

func resolveStopSignal(defaults config.Defaults, proc config.Process) string {
	if proc.StopSignal != "" {
		return proc.StopSignal
	}
	if defaults.StopSignal != "" {
		return defaults.StopSignal
	}
	return defaultStopSignal
}

func resolveStopTimeout(defaults config.Defaults, proc config.Process) time.Duration {
	return resolveDuration(proc.StopTimeout, defaults.StopTimeout, defaultStopTimeout)
}

func resolveLogBufferLines(defaults config.Defaults, proc config.Process) int {
	if proc.LogBufferLines != nil {
		return *proc.LogBufferLines
	}
	if defaults.LogBufferLines != nil {
		return *defaults.LogBufferLines
	}
	return defaultLogBufferLines
}

func resolvePTY(defaults config.Defaults, proc config.Process) bool {
	if proc.PTY != nil {
		return *proc.PTY
	}
	if defaults.PTY != nil {
		return *defaults.PTY
	}
	return false
}

func resolveDuration(values ...any) time.Duration {
	fallback := time.Duration(0)
	for _, value := range values {
		switch v := value.(type) {
		case string:
			if v == "" {
				continue
			}
			d, err := time.ParseDuration(v)
			if err == nil {
				return d
			}
		case time.Duration:
			fallback = v
		}
	}
	return fallback
}

func shouldRestart(policy string, exitCode int) bool {
	switch policy {
	case "always":
		return true
	case "on-failure":
		return exitCode != 0
	default:
		return false
	}
}

func cloneEnv(env map[string]string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]string, len(env))
	for key, value := range env {
		out[key] = value
	}
	return out
}

func cloneExitCode(exitCode *int) *int {
	if exitCode == nil {
		return nil
	}
	value := *exitCode
	return &value
}

func commandText(proc config.Process) string {
	if proc.Cmd != "" {
		return proc.Cmd
	}
	if len(proc.Exec) == 0 {
		return ""
	}
	return proc.Exec[0]
}
