package tui

import (
	"context"

	nagitui "github.com/mayahiro/nagitui-go"

	"github.com/mayahiro/process-deck/internal/config"
	"github.com/mayahiro/process-deck/internal/session"
	"github.com/mayahiro/process-deck/internal/supervisor"
)

// Run starts an in-process supervisor and its TUI. The CLI uses RunSession to
// support recovery after a terminal client exits unexpectedly.
func Run(cfg *config.Config, baseDir string) error {
	sup, err := supervisor.New(cfg, supervisor.Options{
		BaseDir:             baseDir,
		KeepRunningWhenDone: true,
	})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() {
		runErr <- sup.Run(ctx)
	}()

	err = nagitui.RunTerminal[appMessage](newModel(sup, cancel), terminalOptions(), mapEvent)
	cancel()

	supervisorErr := <-runErr
	if err != nil {
		return err
	}
	return supervisorErr
}

// RunSession displays an independently managed session. Explicit quit or context
// cancellation stops all processes; terminal errors only detach the client.
func RunSession(ctx context.Context, client *session.Client) error {
	shutdownCtx, requestShutdown := context.WithCancel(ctx)
	defer requestShutdown()
	terminalDone := make(chan struct{})
	shutdownResult := make(chan error, 1)
	go func() {
		select {
		case <-shutdownCtx.Done():
			if err := client.Shutdown(); err != nil {
				_ = client.Close()
				shutdownResult <- err
				return
			}
		case <-terminalDone:
		}
		shutdownResult <- nil
	}()
	m := newModel(client, requestShutdown)
	if client.Reattached() {
		m.status = "reattached to running session"
	}
	err := nagitui.RunTerminal[appMessage](m, terminalOptions(), mapEvent)
	close(terminalDone)
	_ = client.Close()
	shutdownErr := <-shutdownResult
	if err != nil {
		return err
	}
	if shutdownErr != nil {
		return shutdownErr
	}
	return client.Wait()
}

func terminalOptions() nagitui.TerminalOptions {
	options := nagitui.DefaultTerminalOptions()
	options.MinimumFrameInterval = logRefreshInterval
	return options
}
