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
	BaseDir string
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

	mu        sync.Mutex
	processes map[string]*processRuntime
	events    chan Event
	done      chan struct{}
	doneOnce  sync.Once
	stopping  bool
	hadFailed bool
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
	defer close(s.events)

	if err := s.startInitial(ctx); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		if err := s.StopAll(); err != nil {
			return err
		}
		<-s.done
		s.emitStopped()
		return nil
	case <-s.done:
		if s.failed() {
			return fmt.Errorf("runtime error: one or more processes failed")
		}
		s.emitStopped()
		return nil
	}
}

func (s *Supervisor) StopAll() error {
	s.mu.Lock()
	s.stopping = true
	for _, runtime := range s.processes {
		runtime.stopRequested = true
		if runtime.restarting || runtime.state == StatePending || runtime.state == StateStarting {
			runtime.restarting = false
			s.setStateLocked(runtime, StateExited)
		}
	}
	s.mu.Unlock()

	order, err := ReverseDependencyOrder(s.deps)
	if err != nil {
		return err
	}

	for _, name := range order {
		s.mu.Lock()
		runtime := s.processes[name]
		runner := runtime.runner
		if runner != nil && runtime.state == StateRunning {
			s.setStateLocked(runtime, StateStopping)
		}
		s.mu.Unlock()

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
	s.mu.Lock()
	if s.stopping || ctx.Err() != nil {
		s.mu.Unlock()
		return nil
	}
	runtime := s.processes[name]
	runtime.stopRequested = false
	runtime.restarting = false
	runtime.exitCode = nil
	s.setStateLocked(runtime, StateStarting)
	spec, err := s.processSpec(runtime)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	runner := process.NewRunner(spec)
	runtime.runner = runner
	s.mu.Unlock()

	run, err := runner.Start()
	if err != nil {
		return err
	}

	s.mu.Lock()
	runtime.pid = run.PID
	runtime.startedAt = time.Now()
	runtime.reachedRunning = true
	s.setStateLocked(runtime, StateRunning)
	s.emitLocked(Event{
		Kind:    EventProcessStarted,
		Process: name,
		State:   StateRunning,
		PID:     run.PID,
		Time:    time.Now(),
	})
	s.mu.Unlock()

	logsDone := make(chan struct{})
	go func() {
		s.forwardLogs(name, run.Logs)
		close(logsDone)
	}()
	go s.waitProcess(ctx, name, run.Done, logsDone)
	return nil
}

func (s *Supervisor) waitProcess(ctx context.Context, name string, done <-chan process.Result, logsDone <-chan struct{}) {
	result := <-done
	<-logsDone

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
	s.setStateLocked(runtime, state)
	s.emitLocked(Event{
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
		s.setStateLocked(runtime, StatePending)
		backoff := resolveBackoff(s.cfg.Defaults, runtime.config)
		restarts := runtime.restarts
		s.emitLocked(Event{
			Kind:     EventProcessRestartScheduled,
			Process:  name,
			State:    StatePending,
			Restarts: restarts,
			Time:     time.Now(),
		})
		s.mu.Unlock()

		timer := time.NewTimer(backoff)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			s.mu.Lock()
			runtime := s.processes[name]
			runtime.restarting = false
			if !terminalState(runtime.state) {
				s.setStateLocked(runtime, StateExited)
			}
			s.checkDoneLocked()
			s.mu.Unlock()
		case <-timer.C:
			if ctx.Err() != nil {
				s.mu.Lock()
				runtime := s.processes[name]
				runtime.restarting = false
				if !terminalState(runtime.state) {
					s.setStateLocked(runtime, StateExited)
				}
				s.checkDoneLocked()
				s.mu.Unlock()
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
		runtime.logs.Add(entry)
		s.emitLocked(Event{
			Kind:    EventProcessLogLine,
			Process: name,
			Stream:  entry.Stream,
			Line:    entry.Line,
			Time:    entry.Time,
		})
		s.mu.Unlock()
	}
}

func (s *Supervisor) dependenciesReady(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

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
	defer s.mu.Unlock()

	runtime := s.processes[name]
	s.hadFailed = true
	s.setStateLocked(runtime, StateSkipped)
	s.emitLocked(Event{
		Kind:    EventProcessSkipped,
		Process: name,
		State:   StateSkipped,
		Time:    time.Now(),
	})
}

func (s *Supervisor) markStartFailed(name string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	runtime := s.processes[name]
	exitCode := 1
	runtime.exitCode = &exitCode
	runtime.pid = 0
	runtime.runner = nil
	runtime.restarting = false
	runtime.stopRequested = false
	s.hadFailed = true
	s.setStateLocked(runtime, StateFailed)
	s.emitLocked(Event{
		Kind:    EventSupervisorError,
		Process: name,
		State:   StateFailed,
		Error:   fmt.Errorf("runtime error: failed to start process %s: %w", name, err),
		Time:    time.Now(),
	})
	s.checkDoneLocked()
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
		Env:         cloneEnv(runtime.config.Env),
		StopSignal:  sig,
		StopTimeout: resolveStopTimeout(s.cfg.Defaults, runtime.config),
	}, nil
}

func (s *Supervisor) setStateLocked(runtime *processRuntime, state State) {
	if runtime.state == state {
		return
	}
	runtime.state = state
	s.emitLocked(Event{
		Kind:     EventProcessStateChanged,
		Process:  runtime.name,
		State:    state,
		PID:      runtime.pid,
		Restarts: runtime.restarts,
		ExitCode: cloneExitCode(runtime.exitCode),
		Time:     time.Now(),
	})
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
	s.events <- event
}

func (s *Supervisor) emitLocked(event Event) {
	s.events <- event
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
	if proc.LogBufferLines != 0 {
		return proc.LogBufferLines
	}
	if defaults.LogBufferLines != 0 {
		return defaults.LogBufferLines
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
