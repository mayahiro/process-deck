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
	writeTestFile(t, filepath.Join(dir, ".env"), "FOO=file\nexport BAR=file # comment\n")
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

func TestParseEnvFileSupportsExportPrefixAndCommentSuffix(t *testing.T) {
	input := strings.NewReader(`
export EXPORTED=one
export  SPACED=two # comment
` + "export\tTABBED=three\n" + `
UNQUOTED=hello world # comment
QUOTED="hello # world" # comment
SINGLE='literal # value'# comment
EMPTY= # comment
HASH=one#two
HASH_ONLY=#fragment
exported=value
export=value
`)
	got, err := parseEnvFile(input, "test.env")
	if err != nil {
		t.Fatalf("parseEnvFile() error = %v, want nil", err)
	}

	want := []string{
		"EXPORTED=one",
		"SPACED=two",
		"TABBED=three",
		"UNQUOTED=hello world",
		"QUOTED=hello # world",
		"SINGLE=literal # value",
		"EMPTY=",
		"HASH=one#two",
		"HASH_ONLY=#fragment",
		"exported=value",
		"export=value",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseEnvFile() = %#v, want %#v", got, want)
	}
}

func TestParseEnvFileRejectsInvalidAssignments(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{name: "empty key", input: "=value\n", wantErr: "key must not be empty"},
		{name: "empty exported key", input: "export =value\n", wantErr: "key must not be empty"},
		{name: "unclosed quote", input: "KEY=\"value\n", wantErr: "quoted value is missing a closing quote"},
		{name: "quoted trailing content", input: "KEY=\"value\" trailing\n", wantErr: "quoted value has unexpected trailing content"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseEnvFile(strings.NewReader(tt.input), "test.env")
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("parseEnvFile() error = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseEnvFileIgnoresNonAssignmentLines(t *testing.T) {
	got, err := parseEnvFile(strings.NewReader("source_up\nsource_up .env\nexport\nexport # comment\nexport # comment=ignored\nFOO=bar\n"), "test.env")
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
