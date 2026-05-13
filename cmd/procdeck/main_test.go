package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mayahiro/process-deck/internal/supervisor"
)

func TestRunVersionDoesNotRequireConfig(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if err := run([]string{"--version"}, &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v, want nil", err)
	}
	if got, want := stdout.String(), "procdeck dev\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr = %q, want empty", got)
	}
}

func TestRunDryRun(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	configPath := filepath.Join("..", "..", "examples", "process-deck.yaml")

	if err := run([]string{"--dry-run", "--config", configPath}, &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v, want nil", err)
	}
	if !strings.Contains(stdout.String(), "Startup layers:") {
		t.Fatalf("stdout = %q, want startup plan", stdout.String())
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr = %q, want empty", got)
	}
}

func TestPrintHeadlessEventShowsNonzeroExitCode(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := 7

	printHeadlessEvent(&stdout, &stderr, supervisor.Event{
		Kind:     supervisor.EventProcessExited,
		Process:  "api",
		ExitCode: &exitCode,
	})

	if got := stdout.String(); got != "" {
		t.Fatalf("stdout = %q, want empty", got)
	}
	if got, want := stderr.String(), "[api] exited with code 7\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}
