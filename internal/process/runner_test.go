package process

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRunnerCapturesStdoutAndStderr(t *testing.T) {
	runner := NewRunner(Spec{
		Cmd: "printf 'out\\n'; printf 'err\\n' >&2",
	})

	run, err := runner.Start()
	if err != nil {
		t.Fatalf("Start() error = %v, want nil", err)
	}

	var logs []LogLine
	for line := range run.Logs {
		logs = append(logs, line)
	}
	result := <-run.Done
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", result.ExitCode)
	}

	if !hasLog(logs, "stdout", "out") {
		t.Fatalf("stdout log missing from %#v", logs)
	}
	if !hasLog(logs, "stderr", "err") {
		t.Fatalf("stderr log missing from %#v", logs)
	}
}

func TestRunnerCapturesPTYOutput(t *testing.T) {
	runner := NewRunner(Spec{
		Cmd: "if [ -t 1 ]; then printf 'tty\\n'; else printf 'pipe\\n'; fi; printf 'err\\n' >&2",
		PTY: true,
	})

	run, err := runner.Start()
	if err != nil {
		t.Fatalf("Start() error = %v, want nil", err)
	}

	var logs []LogLine
	for line := range run.Logs {
		logs = append(logs, line)
	}
	result := <-run.Done
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", result.ExitCode)
	}

	if !hasLog(logs, "pty", "tty") {
		t.Fatalf("pty tty log missing from %#v", logs)
	}
	if !hasLog(logs, "pty", "err") {
		t.Fatalf("pty stderr log missing from %#v", logs)
	}
}

func TestRunnerStopTerminatesProcessGroup(t *testing.T) {
	runner := NewRunner(Spec{
		Cmd:         "trap 'exit 0' TERM; while true; do sleep 1; done",
		StopTimeout: time.Second,
	})

	run, err := runner.Start()
	if err != nil {
		t.Fatalf("Start() error = %v, want nil", err)
	}

	if err := runner.Stop(); err != nil {
		t.Fatalf("Stop() error = %v, want nil", err)
	}

	select {
	case <-run.Done:
	case <-time.After(2 * time.Second):
		t.Fatal("process did not exit after Stop()")
	}
}

func TestRunnerLoadsEnvFilesFromCWD(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, ".env"), "FOO=file\nBAR=file\n")
	writeTestFile(t, filepath.Join(dir, ".env.local"), "FOO=local\n")

	runner := NewRunner(Spec{
		Cmd:      "printf '%s\\n%s\\n' \"$FOO\" \"$BAR\"",
		CWD:      dir,
		EnvFiles: []string{".env", ".env.local"},
		Env: map[string]string{
			"FOO": "env",
		},
	})

	run, err := runner.Start()
	if err != nil {
		t.Fatalf("Start() error = %v, want nil", err)
	}

	var logs []LogLine
	for line := range run.Logs {
		logs = append(logs, line)
	}
	result := <-run.Done
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", result.ExitCode)
	}

	if !hasLog(logs, "stdout", "env") {
		t.Fatalf("FOO log missing from %#v", logs)
	}
	if !hasLog(logs, "stdout", "file") {
		t.Fatalf("BAR log missing from %#v", logs)
	}
}

func TestRunnerRejectsMissingEnvFile(t *testing.T) {
	runner := NewRunner(Spec{
		Cmd:      "true",
		CWD:      t.TempDir(),
		EnvFiles: []string{".missing"},
	})

	if _, err := runner.Start(); err == nil {
		t.Fatal("Start() error = nil, want missing env_file error")
	}
}

func TestParseEnvFile(t *testing.T) {
	input := strings.NewReader(`
# comment
FOO=bar
source_up
source_up .env
EMPTY=
QUOTED="hello world"
SINGLE='literal $VALUE'
INLINE=one # comment
HASH=one#two
`)
	got, err := parseEnvFile(input, "test.env")
	if err != nil {
		t.Fatalf("parseEnvFile() error = %v, want nil", err)
	}

	want := []string{
		"FOO=bar",
		"EMPTY=",
		"QUOTED=hello world",
		"SINGLE=literal $VALUE",
		"INLINE=one",
		"HASH=one#two",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseEnvFile() = %#v, want %#v", got, want)
	}
}

func TestParseEnvFileIgnoresNonAssignmentLines(t *testing.T) {
	got, err := parseEnvFile(strings.NewReader("source_up\nsource_up .env\nFOO=bar\n"), "test.env")
	if err != nil {
		t.Fatalf("parseEnvFile() error = %v, want nil", err)
	}

	want := []string{"FOO=bar"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseEnvFile() = %#v, want %#v", got, want)
	}
}

func hasLog(logs []LogLine, stream string, line string) bool {
	for _, log := range logs {
		if log.Stream == stream && log.Line == line {
			return true
		}
	}
	return false
}

func writeTestFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v, want nil", err)
	}
}
