package process

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

type Spec struct {
	Name        string
	Cmd         string
	Exec        []string
	CWD         string
	EnvFiles    []string
	Env         map[string]string
	StopSignal  os.Signal
	StopTimeout time.Duration
}

type LogLine struct {
	Stream string
	Line   string
	Time   time.Time
}

type Result struct {
	ExitCode int
	Err      error
}

type Run struct {
	PID  int
	Logs <-chan LogLine
	Done <-chan Result
}

type Runner struct {
	spec Spec

	mu     sync.Mutex
	cmd    *exec.Cmd
	run    Run
	exited chan struct{}
}

func NewRunner(spec Spec) *Runner {
	return &Runner{spec: spec}
}

func (r *Runner) Start() (Run, error) {
	cmd, err := buildCommand(r.spec)
	if err != nil {
		return Run{}, err
	}
	setProcessGroup(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Run{}, fmt.Errorf("failed to open stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Run{}, fmt.Errorf("failed to open stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return Run{}, err
	}

	logs := make(chan LogLine, 128)
	done := make(chan Result, 1)
	exited := make(chan struct{})
	run := Run{
		PID:  cmd.Process.Pid,
		Logs: logs,
		Done: done,
	}

	r.mu.Lock()
	r.cmd = cmd
	r.run = run
	r.exited = exited
	r.mu.Unlock()

	var readers sync.WaitGroup
	readers.Add(2)
	go scanLines(&readers, stdout, "stdout", logs)
	go scanLines(&readers, stderr, "stderr", logs)

	go func() {
		err := cmd.Wait()
		readers.Wait()
		close(logs)
		done <- Result{
			ExitCode: exitCode(cmd, err),
			Err:      err,
		}
		close(done)
		close(exited)
	}()

	return run, nil
}

func (r *Runner) Stop() error {
	r.mu.Lock()
	cmd := r.cmd
	exited := r.exited
	r.mu.Unlock()

	if cmd == nil || cmd.Process == nil || exited == nil {
		return nil
	}

	sig := r.spec.StopSignal
	if sig == nil {
		sig = defaultStopSignal()
	}
	if err := signalProcessGroup(cmd.Process.Pid, sig); err != nil && !isProcessDone(err) {
		return err
	}

	timeout := r.spec.StopTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-exited:
		return nil
	case <-timer.C:
		if err := killProcessGroup(cmd.Process.Pid); err != nil && !isProcessDone(err) {
			return err
		}
		<-exited
		return nil
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

func scanLines(wg *sync.WaitGroup, r io.Reader, stream string, logs chan<- LogLine) {
	defer wg.Done()

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		logs <- LogLine{
			Stream: stream,
			Line:   scanner.Text(),
			Time:   time.Now(),
		}
	}
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
