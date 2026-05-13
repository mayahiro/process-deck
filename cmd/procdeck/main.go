package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mayahiro/process-deck/internal/config"
	"github.com/mayahiro/process-deck/internal/supervisor"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
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

	cfg, err := config.LoadFile(configPath)
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	deps := cfg.DependencyMap()
	if err := supervisor.ValidateGraph(deps); err != nil {
		return err
	}

	if dryRun {
		return printDryRun(stdout, configPath, cfg, deps)
	}
	if noTUI {
		return fmt.Errorf("runtime error: --no-tui is not implemented in phase 1; use --dry-run")
	}
	return fmt.Errorf("runtime error: TUI mode is not implemented in phase 1; use --dry-run")
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
