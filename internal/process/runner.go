package process

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// Spec describes a command and the process group managed for its lifetime.
type Spec struct {
	Name        string
	Cmd         string
	Exec        []string
	CWD         string
	EnvFiles    []string
	Env         map[string]string
	StopSignal  os.Signal
	StopTimeout time.Duration
	PTY         bool
}

// LogLine is an output record, with long lines split at MaxLogLineBytes.
type LogLine struct {
	Stream string
	Line   string
	Time   time.Time
}

// Result reports the command's exit status after group cleanup and log drainage.
type Result struct {
	ExitCode int
	Err      error
	// SupervisionErr reports log capture or process group cleanup failures,
	// independently of the command's exit status.
	SupervisionErr error
}

// Run identifies a started command. Consume Logs until it closes before waiting
// on Done, and continue consuming Logs while calling Stop.
type Run struct {
	PID  int
	Logs <-chan LogLine
	Done <-chan Result
}

// Runner owns one command, its process group, and its output readers.
// Descendants must remain in the managed process group to be stopped with it.
type Runner struct {
	spec Spec

	mu       sync.Mutex
	cmd      *exec.Cmd
	exited   chan struct{}
	stopOnce sync.Once
	stopErr  error
}

// NewRunner prepares a command without starting it.
func NewRunner(spec Spec) *Runner {
	return &Runner{spec: spec}
}

// Start launches the command once, using pipes or a PTY according to Spec.
func (r *Runner) Start() (Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd != nil {
		return Run{}, fmt.Errorf("process has already been started")
	}
	if r.spec.PTY {
		return r.startPTY()
	}
	return r.startPipes()
}

type processOutput struct {
	file   *os.File
	stream string
}

func (r *Runner) startPipes() (Run, error) {
	cmd, err := buildCommand(r.spec)
	if err != nil {
		return Run{}, err
	}
	setPipeProcessGroup(cmd)

	// Own the read ends: Cmd.Wait must not close them before output is drained.
	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		return Run{}, fmt.Errorf("failed to open stdout pipe: %w", err)
	}
	defer stdoutWriter.Close()
	stderr, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdout.Close()
		return Run{}, fmt.Errorf("failed to open stderr pipe: %w", err)
	}
	defer stderrWriter.Close()
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter
	return r.start(cmd, []processOutput{{stdout, "stdout"}, {stderr, "stderr"}})
}

func (r *Runner) startPTY() (Run, error) {
	cmd, err := buildCommand(r.spec)
	if err != nil {
		return Run{}, err
	}
	master, slave, err := openPTY()
	if err != nil {
		return Run{}, err
	}
	defer slave.Close()
	setTTYProcessGroup(cmd)
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	return r.start(cmd, []processOutput{{master, "pty"}})
}

func (r *Runner) start(cmd *exec.Cmd, outputs []processOutput) (Run, error) {
	if err := cmd.Start(); err != nil {
		for _, output := range outputs {
			_ = output.file.Close()
		}
		return Run{}, err
	}
	logs := make(chan LogLine, 128)
	done := make(chan Result, 1)
	exited := make(chan struct{})
	r.cmd = cmd
	r.exited = exited

	readErrors := make(chan error, len(outputs))
	for _, output := range outputs {
		go func() {
			err := scanLines(output.file, output.stream, logs)
			_ = output.file.Close()
			if err != nil {
				err = fmt.Errorf("failed to read %s: %w", output.stream, err)
			}
			readErrors <- err
		}()
	}
	go func() {
		err := cmd.Wait()
		// The leader may exit before its children, even during an explicit stop.
		supervisionErr := r.stopGroup(cmd.Process.Pid)
		if supervisionErr != nil {
			for _, output := range outputs {
				_ = output.file.Close()
			}
		}
		for range outputs {
			supervisionErr = errors.Join(supervisionErr, <-readErrors)
		}
		close(logs)
		done <- Result{ExitCode: exitCode(cmd, err), Err: err, SupervisionErr: supervisionErr}
		close(done)
		close(exited)
	}()
	return Run{PID: cmd.Process.Pid, Logs: logs, Done: done}, nil
}

// Stop signals the whole process group, escalates to KILL after StopTimeout,
// and waits for output drainage. It is safe to call concurrently or repeatedly.
func (r *Runner) Stop() error {
	r.mu.Lock()
	cmd, exited := r.cmd, r.exited
	r.mu.Unlock()
	if cmd == nil {
		return nil
	}
	if err := r.stopGroup(cmd.Process.Pid); err != nil {
		return err
	}
	<-exited
	return nil
}

func (r *Runner) stopGroup(pid int) error {
	r.stopOnce.Do(func() {
		sig := r.spec.StopSignal
		if sig == nil {
			sig = defaultStopSignal()
		}
		if err := signalProcessGroup(pid, sig); err != nil && !errors.Is(err, os.ErrPermission) {
			if !isProcessDone(err) {
				r.stopErr = err
			}
			return
		}
		timeout := r.spec.StopTimeout
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		gone, err := waitForProcessGroup(pid, timeout)
		if err != nil || gone {
			r.stopErr = err
			return
		}
		killErr := killProcessGroup(pid)
		if killErr != nil && !isProcessDone(killErr) && !errors.Is(killErr, os.ErrPermission) {
			r.stopErr = killErr
			return
		}
		// Bound the wait even if the OS cannot immediately complete SIGKILL.
		gone, err = waitForProcessGroup(pid, 5*time.Second)
		if err != nil {
			r.stopErr = err
		} else if !gone {
			r.stopErr = errors.Join(fmt.Errorf("process group %d did not exit after KILL", pid), killErr)
		}
	})
	return r.stopErr
}

func waitForProcessGroup(pid int, timeout time.Duration) (bool, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		alive, err := processGroupAlive(pid)
		if err != nil || !alive {
			return !alive, err
		}
		select {
		case <-timer.C:
			return false, nil
		case <-ticker.C:
		}
	}
}

func buildCommand(spec Spec) (*exec.Cmd, error) {
	var cmd *exec.Cmd
	if strings.TrimSpace(spec.Cmd) != "" {
		cmd = exec.Command("/bin/sh", "-c", spec.Cmd)
	} else {
		if len(spec.Exec) == 0 || strings.TrimSpace(spec.Exec[0]) == "" {
			return nil, fmt.Errorf("missing executable")
		}
		cmd = exec.Command(spec.Exec[0], spec.Exec[1:]...)
	}
	cmd.Dir = spec.CWD
	env, err := buildEnv(spec.CWD, spec.EnvFiles, spec.Env)
	if err != nil {
		return nil, err
	}
	cmd.Env = env
	return cmd, nil
}

func buildEnv(cwd string, envFiles []string, env map[string]string) ([]string, error) {
	values := os.Environ()
	for _, path := range envFiles {
		entries, err := readEnvFile(resolveEnvFilePath(cwd, path))
		if err != nil {
			return nil, err
		}
		values = append(values, entries...)
	}
	if len(env) == 0 {
		return values, nil
	}

	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		values = append(values, key+"="+env[key])
	}
	return values, nil
}

func exitCode(cmd *exec.Cmd, err error) int {
	if cmd.ProcessState != nil {
		return cmd.ProcessState.ExitCode()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	if err != nil {
		return 1
	}
	return 0
}
