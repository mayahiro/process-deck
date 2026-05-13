package tui

import (
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/mayahiro/process-deck/internal/config"
	"github.com/mayahiro/process-deck/internal/supervisor"
)

func Run(cfg *config.Config, baseDir string) error {
	sup, err := supervisor.New(cfg, supervisor.Options{BaseDir: baseDir})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() {
		runErr <- sup.Run(ctx)
	}()

	program := tea.NewProgram(newModel(sup, cancel))
	_, err = program.Run()
	cancel()

	supervisorErr := <-runErr
	if err != nil && !errors.Is(err, tea.ErrProgramKilled) {
		return err
	}
	return supervisorErr
}
