package config

import (
	"path/filepath"
	"sort"
	"strings"
)

const SchemaVersion = 1

var DefaultConfigFilenames = []string{
	"process-deck.yaml",
	"process-deck.yml",
	"procdeck.yaml",
	"procdeck.yml",
}

// Config is the versioned Process Deck YAML schema.
type Config struct {
	Version   int                `yaml:"version"`
	Project   string             `yaml:"project"`
	Defaults  Defaults           `yaml:"defaults"`
	Processes map[string]Process `yaml:"processes"`
}

// Defaults contains process options applied when a process omits a value.
type Defaults struct {
	Restart        string `yaml:"restart"`
	Backoff        string `yaml:"backoff"`
	StopSignal     string `yaml:"stop_signal"`
	StopTimeout    string `yaml:"stop_timeout"`
	LogBufferLines int    `yaml:"log_buffer_lines"`
}

// Process describes one managed process.
type Process struct {
	Cmd            string            `yaml:"cmd"`
	Exec           []string          `yaml:"exec"`
	CWD            string            `yaml:"cwd"`
	Env            map[string]string `yaml:"env"`
	DependsOn      []string          `yaml:"depends_on"`
	Restart        string            `yaml:"restart"`
	Backoff        string            `yaml:"backoff"`
	StopSignal     string            `yaml:"stop_signal"`
	StopTimeout    string            `yaml:"stop_timeout"`
	LogBufferLines int               `yaml:"log_buffer_lines"`
}

func (c *Config) ProcessNames() []string {
	names := make([]string, 0, len(c.Processes))
	for name := range c.Processes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (c *Config) DependencyMap() map[string][]string {
	deps := make(map[string][]string, len(c.Processes))
	for name, process := range c.Processes {
		deps[name] = append([]string(nil), process.DependsOn...)
	}
	return deps
}

func (c *Config) ProjectName(fallbackPath string) string {
	if strings.TrimSpace(c.Project) != "" {
		return c.Project
	}

	base := filepath.Base(fallbackPath)
	if base == "." || base == string(filepath.Separator) {
		return "process-deck"
	}
	return base
}
