package config

import (
	"fmt"
	"strings"
	"time"
)

func (c *Config) Validate() error {
	if c.Version == 0 {
		return fmt.Errorf("config error: version is required")
	}
	if c.Version != SchemaVersion {
		return fmt.Errorf("config error: unsupported version %d", c.Version)
	}
	if len(c.Processes) == 0 {
		return fmt.Errorf("config error: processes must define at least one process")
	}

	if err := validateDefaults(c.Defaults); err != nil {
		return err
	}

	for _, name := range c.ProcessNames() {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("config error: process name must not be empty")
		}
		if err := validateProcess(name, c.Processes[name]); err != nil {
			return err
		}
	}

	return nil
}

func validateDefaults(defaults Defaults) error {
	if err := validateRestart("defaults.restart", defaults.Restart); err != nil {
		return err
	}
	if err := validateDuration("defaults.backoff", defaults.Backoff); err != nil {
		return err
	}
	if err := validateDuration("defaults.stop_timeout", defaults.StopTimeout); err != nil {
		return err
	}
	if err := validateStopSignal("defaults.stop_signal", defaults.StopSignal); err != nil {
		return err
	}
	if defaults.LogBufferLines < 0 {
		return fmt.Errorf("config error: defaults.log_buffer_lines must be greater than or equal to 0")
	}
	return nil
}

func validateProcess(name string, process Process) error {
	hasCmd := strings.TrimSpace(process.Cmd) != ""
	hasExec := len(process.Exec) > 0
	if hasCmd == hasExec {
		return fmt.Errorf("config error: processes.%s must define exactly one of cmd or exec", name)
	}
	if len(process.Exec) > 0 && strings.TrimSpace(process.Exec[0]) == "" {
		return fmt.Errorf("config error: processes.%s.exec[0] must not be empty", name)
	}
	for key := range process.Env {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("config error: processes.%s.env must not contain an empty key", name)
		}
		if strings.Contains(key, "=") {
			return fmt.Errorf("config error: processes.%s.env key %q must not contain =", name, key)
		}
	}
	if err := validateRestart("processes."+name+".restart", process.Restart); err != nil {
		return err
	}
	if err := validateDuration("processes."+name+".backoff", process.Backoff); err != nil {
		return err
	}
	if err := validateDuration("processes."+name+".stop_timeout", process.StopTimeout); err != nil {
		return err
	}
	if err := validateStopSignal("processes."+name+".stop_signal", process.StopSignal); err != nil {
		return err
	}
	if process.LogBufferLines < 0 {
		return fmt.Errorf("config error: processes.%s.log_buffer_lines must be greater than or equal to 0", name)
	}
	return nil
}

func validateRestart(path string, value string) error {
	if value == "" {
		return nil
	}
	switch value {
	case "no", "on-failure", "always":
		return nil
	default:
		return fmt.Errorf("config error: %s must be one of no, on-failure, always", path)
	}
}

func validateDuration(path string, value string) error {
	if value == "" {
		return nil
	}
	if _, err := time.ParseDuration(value); err != nil {
		return fmt.Errorf("config error: %s must be a valid duration: %q", path, value)
	}
	return nil
}

func validateStopSignal(path string, value string) error {
	if value == "" {
		return nil
	}
	name := strings.ToUpper(strings.TrimSpace(value))
	name = strings.TrimPrefix(name, "SIG")
	switch name {
	case "TERM", "INT", "KILL", "HUP", "QUIT":
		return nil
	default:
		return fmt.Errorf("config error: %s must be one of TERM, INT, KILL, HUP, QUIT", path)
	}
}
