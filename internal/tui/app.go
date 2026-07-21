package tui

import (
	"context"

	nagitui "github.com/mayahiro/nagitui-go"

	"github.com/mayahiro/process-deck/internal/config"
	"github.com/mayahiro/process-deck/internal/supervisor"
)

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

func terminalOptions() nagitui.TerminalOptions {
	options := nagitui.DefaultTerminalOptions()
	options.MinimumFrameInterval = logRefreshInterval
	return options
}
