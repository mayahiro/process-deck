package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/mayahiro/process-deck/internal/config"
	"github.com/mayahiro/process-deck/internal/session"
	"github.com/mayahiro/process-deck/internal/supervisor"
	"github.com/mayahiro/process-deck/internal/tui"
)

var version = "dev"

func main() {
	var err error
	if session.IsWorker(os.Args[1:]) {
		err = session.RunWorker()
	} else {
		err = run(os.Args[1:], os.Stdout, os.Stderr)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer, stderr io.Writer) error {
	var configPath string
	var noTUI bool
	var dryRun bool
	var showVersion bool

	flags := flag.NewFlagSet("procdeck", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&configPath, "config", "", "config file path")
	flags.BoolVar(&noTUI, "no-tui", false, "run without the TUI")
	flags.BoolVar(&dryRun, "dry-run", false, "validate config and print the startup plan")
	flags.BoolVar(&showVersion, "version", false, "print version information")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: procdeck [flags]")
		fmt.Fprintln(flags.Output())
		fmt.Fprintln(flags.Output(), "Flags:")
		flags.PrintDefaults()
	}

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("usage error: unexpected argument %q", flags.Arg(0))
	}
	if showVersion {
		fmt.Fprintf(stdout, "procdeck %s\n", version)
		return nil
	}

	if configPath == "" {
		path, err := config.DiscoverPath(".")
		if err != nil {
			return err
		}
		configPath = path
	}

	if dryRun {
		cfg, err := config.LoadFile(configPath)
		if err != nil {
			return err
		}
		if err := cfg.Validate(); err != nil {
			return err
		}
		deps := cfg.DependencyMap()
		return printDryRun(stdout, configPath, cfg, deps)
	}
	baseDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("runtime error: failed to get working directory: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client, err := session.Open(configPath, baseDir, !noTUI)
	if err != nil {
		return err
	}
	defer client.Close()
	if client.Reattached() {
		fmt.Fprintln(stderr, "Reattached to the existing session (using its original configuration and environment)")
	}
	if noTUI {
		return runHeadless(ctx, stdout, stderr, client)
	}
	return tui.RunSession(ctx, client)
}

func printDryRun(w io.Writer, configPath string, cfg *config.Config, deps map[string][]string) error {
	layers, err := supervisor.StartupLayers(deps)
	if err != nil {
		return err
	}

	projectPath, err := os.Getwd()
	if err != nil {
		projectPath = "."
	}

	fmt.Fprintf(w, "Project: %s\n", cfg.ProjectName(projectPath))
	fmt.Fprintf(w, "Config: %s\n", displayPath(configPath))
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Processes:")
	for _, name := range flattenLayers(layers) {
		processDeps := supervisor.DependenciesOf(deps, name)
		if len(processDeps) == 0 {
			fmt.Fprintf(w, "- %s\n", name)
			continue
		}
		fmt.Fprintf(w, "- %s depends_on [%s]\n", name, strings.Join(processDeps, ", "))
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Startup layers:")
	for i, layer := range layers {
		fmt.Fprintf(w, "%d. %s\n", i+1, strings.Join(layer, ", "))
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "OK")
	return nil
}

func displayPath(path string) string {
	if rel, err := filepath.Rel(".", path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}

func flattenLayers(layers [][]string) []string {
	names := make([]string, 0)
	for _, layer := range layers {
		names = append(names, layer...)
	}
	return names
}

func runHeadless(ctx context.Context, stdout io.Writer, stderr io.Writer, client *session.Client) error {
	cursors := make(map[string]supervisor.LogCursor)
	for _, event := range client.History() {
		printHeadlessEvent(stdout, stderr, event)
		cursors[event.Process] = max(cursors[event.Process], event.LogCursor)
	}
	stopping := ctx.Done()
	for {
		select {
		case <-stopping:
			stopping = nil
			if err := client.Shutdown(); err != nil {
				return err
			}
		case event, ok := <-client.Events():
			if !ok {
				return client.Wait()
			}
			if event.Kind == supervisor.EventProcessLogLine && event.LogStateValid && event.LogCursor != 0 {
				if event.LogCursor <= cursors[event.Process] {
					continue
				}
				cursors[event.Process] = event.LogCursor
			}
			printHeadlessEvent(stdout, stderr, event)
		}
	}
}

func printHeadlessEvent(stdout io.Writer, stderr io.Writer, event supervisor.Event) {
	switch event.Kind {
	case supervisor.EventProcessLogLine:
		fmt.Fprintf(stdout, "[%s %s] %s\n", event.Process, event.Stream, event.Line)
	case supervisor.EventProcessRestartScheduled:
		fmt.Fprintf(stderr, "[%s] restart scheduled\n", event.Process)
	case supervisor.EventProcessExited:
		if event.ExitCode != nil && *event.ExitCode != 0 {
			fmt.Fprintf(stderr, "[%s] exited with code %d\n", event.Process, *event.ExitCode)
		}
	case supervisor.EventProcessSkipped:
		fmt.Fprintf(stderr, "[%s] skipped\n", event.Process)
	case supervisor.EventSupervisorError:
		if event.Error != nil {
			fmt.Fprintln(stderr, event.Error)
		}
	}
}
