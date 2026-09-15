package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/mayahiro/process-deck/internal/supervisor"
)

func TestMain(m *testing.M) {
	if IsWorker(os.Args[1:]) {
		if err := RunWorker(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) == 5 && os.Args[1] == "--session-test-client" {
		client, err := openAt(os.Args[2], os.Args[3], os.Args[4], true)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		for event := range client.Events() {
			if event.Kind == supervisor.EventProcessStarted {
				fmt.Println(event.PID)
			}
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestReattachAfterClientCrash(t *testing.T) {
	for _, pty := range []bool{false, true} {
		t.Run(fmt.Sprintf("pty=%t", pty), func(t *testing.T) {
			dir, socket := testLocation(t)
			configPath := writeConfig(t, dir, fmt.Sprintf(`version: 1
processes:
  worker:
    cmd: "echo start >> starts; trap 'exit 0' TERM; i=0; while :; do echo line-$i; i=$((i+1)); sleep 0.02; done"
    pty: %t
    log_buffer_lines: 20
    stop_timeout: 200ms
`, pty))
			command := exec.Command(os.Args[0], "--session-test-client", socket, configPath, dir)
			output, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			command.Stderr = os.Stderr
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			t.Cleanup(func() {
				if !waited {
					_ = command.Process.Kill()
					_ = command.Wait()
				}
			})
			pidLine := make(chan string, 1)
			go func() {
				line, _ := bufio.NewReader(output).ReadString('\n')
				pidLine <- strings.TrimSpace(line)
			}()
			var pid int
			select {
			case line := <-pidLine:
				pid, err = strconv.Atoi(line)
				if err != nil || pid <= 0 {
					t.Fatalf("child PID = %q, error = %v", line, err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("client did not start its process")
			}
			if err := command.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = command.Wait()
			waited = true
			// Reattachment must work even after the config has become invalid.
			writeConfig(t, dir, "invalid: [\n")
			client := reconnect(t, socket, configPath, dir)
			if !client.Reattached() {
				t.Fatal("expected to reattach to the original session")
			}
			if got := client.Snapshot()[0].PID; got != pid {
				t.Fatalf("PID after client crash = %d, want original %d", got, pid)
			}
			event := awaitEvent(t, client, func(event supervisor.Event) bool {
				return event.Kind == supervisor.EventProcessLogLine && strings.HasPrefix(event.Line, "line-")
			})
			wantStream := "stdout"
			if pty {
				wantStream = "pty"
			}
			if event.Stream != wantStream {
				t.Fatalf("stream = %s, want %s", event.Stream, wantStream)
			}
			if batch := client.LogsSince("worker", 0); len(batch.Entries) == 0 {
				t.Fatal("retained logs were not recovered")
			}
			starts, err := os.ReadFile(filepath.Join(dir, "starts"))
			if err != nil || string(starts) != "start\n" {
				t.Fatalf("process starts = %q, error = %v", starts, err)
			}
			if err := client.StopProcess("worker"); err != nil {
				t.Fatal(err)
			}
			awaitSnapshot(t, client, func(s supervisor.Snapshot) bool { return s.PID == 0 })
			if err := client.StartProcess("worker"); err != nil {
				t.Fatal(err)
			}
			started := awaitSnapshot(t, client, func(s supervisor.Snapshot) bool { return s.PID != 0 })
			if started.PID == pid {
				t.Fatal("manual start reused the stopped process")
			}
			if err := client.RestartProcess("worker"); err != nil {
				t.Fatal(err)
			}
			restarted := awaitSnapshot(t, client, func(s supervisor.Snapshot) bool {
				return s.PID != 0 && s.PID != started.PID
			})
			shutdown(t, client)
			if err := syscall.Kill(restarted.PID, 0); err != syscall.ESRCH {
				t.Fatalf("process still exists after shutdown: %v", err)
			}
		})
	}
}

func TestDetachedSessionDrainsOutputAndRetainsBoundedHistory(t *testing.T) {
	dir, socket := testLocation(t)
	path := writeConfig(t, dir, `version: 1
processes:
  worker:
    cmd: "while [ ! -f emit ]; do sleep 0.01; done; i=0; while [ $i -lt 5000 ]; do echo line-$i; i=$((i+1)); done; touch emitted; exec sleep 30"
    log_buffer_lines: 4
    stop_timeout: 200ms
`)
	client := openTestClient(t, socket, path, dir, true)
	pid := awaitSnapshot(t, client, func(s supervisor.Snapshot) bool { return s.PID != 0 }).PID
	_ = client.Close()
	_ = client.Wait()
	if err := os.WriteFile(filepath.Join(dir, "emit"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool {
		_, err := os.Stat(filepath.Join(dir, "emitted"))
		return err == nil
	})
	client = reconnect(t, socket, path, dir)
	if got := client.Snapshot()[0].PID; got != pid {
		t.Fatalf("PID = %d, want %d", got, pid)
	}
	await(t, func() bool { return client.LogsSince("worker", 0).Cursor == 5000 })
	batch := client.LogsSince("worker", 0)
	if len(batch.Entries) != 4 || batch.Entries[3].Line != "line-4999" {
		t.Fatalf("retained history = %+v", batch)
	}
	if len(client.History()) != 4 {
		t.Fatalf("attachment history length = %d, want 4", len(client.History()))
	}
	shutdown(t, client)
}

func TestConcurrentLaunchDoesNotDuplicateProcesses(t *testing.T) {
	dir, socket := testLocation(t)
	path := writeConfig(t, dir, `version: 1
processes:
  worker:
    cmd: "echo start >> starts; exec sleep 30"
    stop_timeout: 200ms
`)
	var clients [2]*Client
	var errs [2]error
	var group sync.WaitGroup
	start := make(chan struct{})
	for index := range clients {
		group.Go(func() {
			<-start
			clients[index], errs[index] = openAt(socket, path, dir, true)
		})
	}
	close(start)
	group.Wait()
	var client *Client
	for index, candidate := range clients {
		if candidate != nil {
			t.Cleanup(func() { _ = candidate.Close() })
			if client != nil {
				t.Fatal("both clients attached")
			}
			client = candidate
		} else if errs[index] == nil || !strings.Contains(errs[index].Error(), "already attached") {
			t.Fatalf("second launch error = %v", errs[index])
		}
	}
	if client == nil {
		t.Fatalf("no client attached: %v", errs)
	}
	await(t, func() bool {
		data, _ := os.ReadFile(filepath.Join(dir, "starts"))
		return len(data) > 0
	})
	data, err := os.ReadFile(filepath.Join(dir, "starts"))
	if err != nil || string(data) != "start\n" {
		t.Fatalf("starts = %q, error = %v", data, err)
	}
	shutdown(t, client)
}

func TestAttachedLogBurstDoesNotLoseOutput(t *testing.T) {
	dir, socket := testLocation(t)
	path := writeConfig(t, dir, `version: 1
processes:
  worker:
    cmd: "i=0; while [ $i -lt 5000 ]; do echo line-$i; i=$((i+1)); done; exec sleep 30"
    log_buffer_lines: 0
    stop_timeout: 200ms
`)
	client := openTestClient(t, socket, path, dir, true)
	// A busy UI can take longer than one frame to consume its next batch.
	// This must apply backpressure rather than drop its connection or logs.
	time.Sleep(100 * time.Millisecond)
	for index := 0; index < 5000; index++ {
		event := awaitEvent(t, client, func(event supervisor.Event) bool {
			return event.Kind == supervisor.EventProcessLogLine
		})
		if want := fmt.Sprintf("line-%d", index); event.Line != want {
			t.Fatalf("line = %q, want %q", event.Line, want)
		}
	}
	shutdown(t, client)
}

func TestQuietSessionCanReconnectAfterDisconnect(t *testing.T) {
	dir, socket := testLocation(t)
	path := writeConfig(t, dir, "version: 1\nprocesses:\n  worker:\n    exec: [sleep, '30']\n")
	client := openTestClient(t, socket, path, dir, true)
	pid := awaitSnapshot(t, client, func(s supervisor.Snapshot) bool { return s.PID != 0 }).PID
	_ = client.Close()
	_ = client.Wait()
	client = reconnect(t, socket, path, dir)
	if got := client.Snapshot()[0].PID; got != pid {
		t.Fatalf("PID = %d, want %d", got, pid)
	}
	shutdown(t, client)
}

func TestHeadlessShortProcessPreservesLogsAndFailure(t *testing.T) {
	dir, socket := testLocation(t)
	path := writeConfig(t, dir, `version: 1
processes:
  worker:
    cmd: "echo before-exit; echo failure >&2; exit 7"
    log_buffer_lines: 0
`)
	client := openTestClient(t, socket, path, dir, false)
	logs := make(map[string]string)
	for event := range client.Events() {
		if event.Kind == supervisor.EventProcessLogLine {
			logs[event.Stream] = event.Line
		}
	}
	if err := client.Wait(); err == nil || !strings.Contains(err.Error(), "one or more processes failed") {
		t.Fatalf("Wait() = %v, want process failure", err)
	}
	if logs["stdout"] != "before-exit" || logs["stderr"] != "failure" {
		t.Fatalf("logs = %v", logs)
	}
}

func TestStaleSocketDoesNotStartNewProcesses(t *testing.T) {
	dir, socket := testLocation(t)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	_ = listener.Close()
	if _, err := openAt(socket, filepath.Join(dir, "missing.yaml"), dir, true); err == nil || !strings.Contains(err.Error(), "stale socket") {
		t.Fatalf("Open() = %v, want stale session error", err)
	}
	if _, err := os.Lstat(socket); err != nil {
		t.Fatalf("stale socket was removed: %v", err)
	}
}

func TestPrivateDirectoryRejectsSymlinksAndSharedPermissions(t *testing.T) {
	dir := t.TempDir()
	shared := filepath.Join(dir, "shared")
	if err := os.Mkdir(shared, 0755); err != nil {
		t.Fatal(err)
	}
	if err := privateDirectory(shared); err == nil {
		t.Fatal("shared directory was accepted")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := privateDirectory(link); err == nil {
		t.Fatal("symlink directory was accepted")
	}
}

func TestProtocolMismatchIsRejected(t *testing.T) {
	dir, socket := testLocation(t)
	path := writeConfig(t, dir, "version: 1\nprocesses:\n  worker:\n    exec: [sleep, '30']\n")
	client := openTestClient(t, socket, path, dir, true)
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(request{Version: protocolVersion + 1, Method: "shutdown"}); err != nil {
		t.Fatal(err)
	}
	var reply response
	if err := json.NewDecoder(conn).Decode(&reply); err != nil || !strings.Contains(reply.Error, "incompatible") {
		t.Fatalf("response = %+v, error = %v", reply, err)
	}
	awaitSnapshot(t, client, func(s supervisor.Snapshot) bool { return s.PID != 0 })
	shutdown(t, client)
}

func TestShutdownAcknowledgesWhileManualStopIsPending(t *testing.T) {
	dir, socket := testLocation(t)
	path := writeConfig(t, dir, `version: 1
processes:
  worker:
    cmd: "trap '' TERM; echo ready; while :; do sleep 1; done"
    stop_timeout: 6s
`)
	client := openTestClient(t, socket, path, dir, true)
	awaitEvent(t, client, func(event supervisor.Event) bool {
		return event.Kind == supervisor.EventProcessLogLine && event.Line == "ready"
	})
	stopResult := make(chan error, 1)
	go func() { stopResult <- client.StopProcess("worker") }()
	awaitSnapshot(t, client, func(snapshot supervisor.Snapshot) bool { return snapshot.State == supervisor.StateStopping })
	started := time.Now()
	if err := client.Shutdown(); err != nil {
		t.Fatalf("shutdown acknowledgment: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("shutdown acknowledgment waited for a pending stop: %s", elapsed)
	}
	for range client.Events() {
	}
	if err := client.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := <-stopResult; err != nil {
		t.Fatalf("manual stop: %v", err)
	}
	if elapsed := time.Since(started); elapsed < 5*time.Second {
		t.Fatalf("session ended before the configured stop timeout: %s", elapsed)
	}
}

func testLocation(t *testing.T) (string, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pd-session-test-")
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "session.sock")
	t.Cleanup(func() {
		// Shut down even when an assertion failed after detaching or before
		// a client was returned. Only this test's private session is touched.
		if conn, err := net.DialTimeout("unix", socket, time.Second); err == nil {
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			_ = json.NewEncoder(conn).Encode(request{Version: protocolVersion, Method: "shutdown"})
			var reply response
			_ = json.NewDecoder(conn).Decode(&reply)
			_ = conn.Close()
		}
		lock, err := os.OpenFile(socket+".lock", os.O_RDWR, 0600)
		if err == nil {
			defer lock.Close()
			await(t, func() bool { return syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil })
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	return dir, socket
}

func writeConfig(t *testing.T, dir, contents string) string {
	t.Helper()
	path := filepath.Join(dir, "process-deck.yaml")
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func openTestClient(t *testing.T, socket, path, dir string, keep bool) *Client {
	t.Helper()
	client, err := openAt(socket, path, dir, keep)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func reconnect(t *testing.T, socket, path, dir string) *Client {
	t.Helper()
	var client *Client
	await(t, func() bool {
		var err error
		client, err = openAt(socket, path, dir, true)
		if err != nil && !strings.Contains(err.Error(), "already attached") {
			t.Fatal(err)
		}
		return err == nil
	})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func shutdown(t *testing.T, client *Client) {
	t.Helper()
	if err := client.Shutdown(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		for range client.Events() {
		}
		finished <- client.Wait()
	}()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("session did not shut down")
	}
}

func awaitSnapshot(t *testing.T, client *Client, predicate func(supervisor.Snapshot) bool) supervisor.Snapshot {
	t.Helper()
	var snapshot supervisor.Snapshot
	await(t, func() bool {
		snapshots := client.Snapshot()
		if len(snapshots) == 0 {
			return false
		}
		snapshot = snapshots[0]
		return predicate(snapshot)
	})
	return snapshot
}

func awaitEvent(t *testing.T, client *Client, predicate func(supervisor.Event) bool) supervisor.Event {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-client.Events():
			if !ok {
				t.Fatalf("session ended: %v", client.Wait())
			}
			if predicate(event) {
				return event
			}
		case <-timer.C:
			t.Fatal("timed out waiting for session event")
		}
	}
}

func await(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !predicate() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for session state")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
