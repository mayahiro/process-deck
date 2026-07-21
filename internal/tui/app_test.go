package tui

import "testing"

func TestTerminalOptionsPreserveApplicationFrameInterval(t *testing.T) {
	if got := terminalOptions().MinimumFrameInterval; got != logRefreshInterval {
		t.Fatalf("MinimumFrameInterval = %s, want %s", got, logRefreshInterval)
	}
}
