package supervisor

import (
	"context"
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

type Options struct {
	BaseDir             string
	KeepRunningWhenDone bool
}

type Snapshot struct {
	Name      string
	State     State
	PID       int
	Restarts  int
	ExitCode  *int
	StartedAt time.Time
	Command   string
}

type LogEntry struct {
	Stream string
	Line   string
	Time   time.Time
}

type Supervisor struct {
	cfg     *config.Config
	options Options
	deps    map[string][]string

	mu           sync.Mutex
	runCtx       context.Context
	wg           sync.WaitGroup
	processes    map[string]*processRuntime
	events       chan Event
	eventsMu     sync.Mutex
	done         chan struct{}
	doneOnce     sync.Once
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
	logs           *logbuf.Ring[LogEntry]
}

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

func (s *Supervisor) Events() <-chan Event {
	return s.events
}

func (s *Supervisor) Run(ctx context.Context) error {
	s.mu.Lock()
	s.runCtx = ctx
	s.mu.Unlock()

	if err := s.startInitial(ctx); err != nil {
		s.closeEvents()
		return err
	}

	var err error
	select {
	case <-ctx.Done():
		err = s.StopAll()
		<-s.done
	case <-s.done:
		if s.options.KeepRunningWhenDone {
			<-ctx.Done()
			err = s.StopAll()
			<-s.done
		} else if s.failed() {
			err = fmt.Errorf("runtime error: one or more processes failed")
		}
	}

	s.wg.Wait()
	s.emitStopped()
	s.closeEvents()
	return err
}

func (s *Supervisor) StartProcess(name string) error {
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
	if !terminalState(runtime.state) && runtime.state != StatePending {
		s.mu.Unlock()
		return fmt.Errorf("runtime error: process %s is already %s", name, runtime.state)
	}
	if !s.dependenciesReadyLocked(name) {
		s.mu.Unlock()
		return fmt.Errorf("runtime error: process %s dependencies are not ready", name)
	}
	runtime.reachedRunning = false
	s.mu.Unlock()

	if err := s.startProcess(ctx, name); err != nil {
		s.markStartFailed(name, err)
		return err
	}
	return nil
}

func (s *Supervisor) StopProcess(name string) error {
	var events []Event

	s.mu.Lock()
	runtime := s.processes[name]
	if runtime == nil {
		s.mu.Unlock()
		return fmt.Errorf("runtime error: unknown process %s", name)
	}
	runtime.stopRequested = true
	runtime.restarting = false
	runner := runtime.runner
	if runner != nil && runtime.state == StateRunning {
		if event, ok := s.setStateLocked(runtime, StateStopping); ok {
			events = append(events, event)
		}
	}
	if runner == nil && (runtime.state == StatePending || runtime.state == StateStarting) {
		if event, ok := s.setStateLocked(runtime, StateExited); ok {
			events = append(events, event)
		}
		s.checkDoneLocked()
	}
	s.mu.Unlock()
	s.emitEvents(events)

	if runner != nil {
		if err := runner.Stop(); err != nil {
			return err
		}
		s.waitForTerminal(name)
	}
	return nil
}

func (s *Supervisor) RestartProcess(name string) error {
	if err := s.StopProcess(name); err != nil {
		return err
	}
	return s.StartProcess(name)
}

func (s *Supervisor) StopAll() error {
	var events []Event

	s.mu.Lock()
	s.stopping = true
	for _, runtime := range s.processes {
		runtime.stopRequested = true
		if runtime.restarting || runtime.state == StatePending || runtime.state == StateStarting {
			runtime.restarting = false
			if event, ok := s.setStateLocked(runtime, StateExited); ok {
				events = append(events, event)
			}
		}
	}
	s.mu.Unlock()
	s.emitEvents(events)

	order, err := ReverseDependencyOrder(s.deps)
	if err != nil {
		return err
	}

	for _, name := range order {
		events = nil

		s.mu.Lock()
		runtime := s.processes[name]
		runner := runtime.runner
		if runner != nil && runtime.state == StateRunning {
			if event, ok := s.setStateLocked(runtime, StateStopping); ok {
				events = append(events, event)
			}
		}
		s.mu.Unlock()
		s.emitEvents(events)

		if runner != nil {
			if err := runner.Stop(); err != nil {
				s.emit(Event{
					Kind:    EventSupervisorError,
					Process: name,
					Error:   err,
					Time:    time.Now(),
				})
			}
		}
	}

	s.mu.Lock()
	s.checkDoneLocked()
	s.mu.Unlock()
	return nil
}

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

func (s *Supervisor) Logs(name string) []LogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()

	runtime := s.processes[name]
	if runtime == nil {
		return nil
	}
	return runtime.logs.Items()
}

func (s *Supervisor) startInitial(ctx context.Context) error {
	layers, err := StartupLayers(s.deps)
	if err != nil {
		return err
	}

	for _, layer := range layers {
		for _, name := range layer {
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

func (s *Supervisor) startProcess(ctx context.Context, name string) error {
	var events []Event

	s.mu.Lock()
	if s.stopping || ctx.Err() != nil {
		s.mu.Unlock()
		return nil
	}
	runtime := s.processes[name]
	runtime.stopRequested = false
	runtime.restarting = false
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
		s.waitProcess(ctx, name, run.Done, logsDone)
	}()
	return nil
}

func (s *Supervisor) waitProcess(ctx context.Context, name string, done <-chan process.Result, logsDone <-chan struct{}) {
	result := <-done
	<-logsDone

	var events []Event

	s.mu.Lock()
	runtime := s.processes[name]
	runtime.pid = 0
	runtime.exitCode = &result.ExitCode
	runtime.runner = nil

	state := StateExited
	if !runtime.stopRequested && result.ExitCode != 0 {
		state = StateFailed
	}
	restart := !s.stopping && !runtime.stopRequested && shouldRestart(resolveRestart(s.cfg.Defaults, runtime.config), result.ExitCode)
	if state == StateFailed && !restart {
		s.hadFailed = true
	}
	if event, ok := s.setStateLocked(runtime, state); ok {
		events = append(events, event)
	}
	events = append(events, Event{
		Kind:     EventProcessExited,
		Process:  name,
		State:    state,
		ExitCode: cloneExitCode(runtime.exitCode),
		Error:    result.Err,
		Time:     time.Now(),
	})

	if restart {
		runtime.restarts++
		runtime.restarting = true
		runtime.exitCode = nil
		if event, ok := s.setStateLocked(runtime, StatePending); ok {
			events = append(events, event)
		}
		backoff := resolveBackoff(s.cfg.Defaults, runtime.config)
		restarts := runtime.restarts
		events = append(events, Event{
			Kind:     EventProcessRestartScheduled,
			Process:  name,
			State:    StatePending,
			Restarts: restarts,
			Time:     time.Now(),
		})
		s.mu.Unlock()
		s.emitEvents(events)

		timer := time.NewTimer(backoff)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			events = nil

			s.mu.Lock()
			runtime := s.processes[name]
			runtime.restarting = false
			if !terminalState(runtime.state) {
				if event, ok := s.setStateLocked(runtime, StateExited); ok {
					events = append(events, event)
				}
			}
			s.checkDoneLocked()
			s.mu.Unlock()
			s.emitEvents(events)
		case <-timer.C:
			if ctx.Err() != nil {
				events = nil

				s.mu.Lock()
				runtime := s.processes[name]
				runtime.restarting = false
				if !terminalState(runtime.state) {
					if event, ok := s.setStateLocked(runtime, StateExited); ok {
						events = append(events, event)
					}
				}
				s.checkDoneLocked()
				s.mu.Unlock()
				s.emitEvents(events)
				return
			}
			if err := s.startProcess(ctx, name); err != nil {
				s.markStartFailed(name, err)
			}
		}
		return
	}

	s.checkDoneLocked()
	s.mu.Unlock()
	s.emitEvents(events)
}

func (s *Supervisor) forwardLogs(name string, logs <-chan process.LogLine) {
	for line := range logs {
		entry := LogEntry{
			Stream: line.Stream,
			Line:   line.Line,
			Time:   line.Time,
		}
		s.mu.Lock()
		runtime := s.processes[name]
		if runtime != nil {
			runtime.logs.Add(entry)
		}
		s.mu.Unlock()

		if runtime == nil {
			continue
		}
		s.emit(Event{
			Kind:    EventProcessLogLine,
			Process: name,
			Stream:  entry.Stream,
			Line:    entry.Line,
			Time:    entry.Time,
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
		if runtime.state == StateSkipped || (!runtime.reachedRunning && runtime.state == StateFailed) {
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
	s.doneOnce.Do(func() {
		close(s.done)
	})
}

func (s *Supervisor) failed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hadFailed
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
	if err := s.runCtx.Err(); err != nil {
		return nil, err
	}
	return s.runCtx, nil
}

func (s *Supervisor) waitForTerminal(name string) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		s.mu.Lock()
		runtime := s.processes[name]
		done := runtime == nil || (runtime.runner == nil && terminalState(runtime.state))
		s.mu.Unlock()
		if done {
			return
		}
	}
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
