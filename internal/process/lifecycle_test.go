package process

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunnerDrainsOutputAfterLeaderExits(t *testing.T) {
	runner := NewRunner(Spec{Cmd: "i=0; while [ $i -lt 2000 ]; do echo line-$i; echo line-$i >&2; i=$((i+1)); done"})
	run, err := runner.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Stop() })
	// Let both readers hit their bounded queue before consuming the output.
	time.Sleep(100 * time.Millisecond)
	counts := map[string]int{}
	for log := range run.Logs {
		want := fmt.Sprintf("line-%d", counts[log.Stream])
		if log.Line != want {
			t.Errorf("%s line = %q, want %q", log.Stream, log.Line, want)
		}
		counts[log.Stream]++
	}
	if result := <-run.Done; result.ExitCode != 0 || result.SupervisionErr != nil {
		t.Fatalf("result = %+v", result)
	}
	if counts["stdout"] != 2000 || counts["stderr"] != 2000 {
		t.Fatalf("captured line counts = %v", counts)
	}
}

func TestRunnerContinuesAfterMultiMegabyteLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output")
	longLine := strings.Repeat("x", 4*MaxLogLineBytes+1)
	writeTestFile(t, path, longLine+"\ntail\n")
	runner := NewRunner(Spec{Exec: []string{"/bin/cat", path}, StopTimeout: 100 * time.Millisecond})
	run, err := runner.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Stop() })
	var chunks []string
	for log := range run.Logs {
		chunks = append(chunks, log.Line)
	}
	if result := <-run.Done; result.ExitCode != 0 || result.SupervisionErr != nil {
		t.Fatalf("result = %+v", result)
	}
	if len(chunks) != 6 || chunks[5] != "tail" || strings.Join(chunks[:5], "") != longLine {
		t.Fatal("long line blocked or lost output")
	}
}

func TestRunnerCleansDescendantsAfterLeaderExit(t *testing.T) {
	for _, manualStop := range []bool{true, false} {
		for _, pty := range []bool{false, true} {
			t.Run(fmt.Sprintf("manual=%t/pty=%t", manualStop, pty), func(t *testing.T) {
				dir := t.TempDir()
				ending := "exit 0"
				if manualStop {
					ending = "wait"
				}
				runner := NewRunner(Spec{
					Cmd: "trap 'exit 0' TERM; /bin/sh -c 'trap \"\" TERM HUP; echo $$ > descendant.pid; exec sleep 30' >/dev/null 2>&1 & while [ ! -s descendant.pid ]; do sleep 0.01; done; echo ready; " + ending,
					CWD: dir, PTY: pty, StopTimeout: 150 * time.Millisecond,
				})
				run, err := runner.Start()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = killProcessGroup(run.PID) })
				ready := make(chan struct{})
				go func() {
					for log := range run.Logs {
						if log.Line == "ready" {
							close(ready)
						}
					}
				}()
				select {
				case <-ready:
				case <-time.After(5 * time.Second):
					t.Fatal("descendant did not start")
				}
				data, err := os.ReadFile(filepath.Join(dir, "descendant.pid"))
				if err != nil {
					t.Fatal(err)
				}
				pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
				if err != nil {
					t.Fatal(err)
				}
				if manualStop {
					if err := runner.Stop(); err != nil {
						t.Fatal(err)
					}
				}
				select {
				case result := <-run.Done:
					if result.SupervisionErr != nil {
						t.Fatal(result.SupervisionErr)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("group did not finish")
				}
				if err := syscall.Kill(pid, 0); !isProcessDone(err) {
					t.Fatalf("descendant %d remains after group cleanup: %v", pid, err)
				}
				if err := runner.Stop(); err != nil {
					t.Fatalf("repeated Stop() = %v", err)
				}
			})
		}
	}
}
