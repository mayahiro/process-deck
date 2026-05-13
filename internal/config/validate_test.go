package config

import (
	"strings"
	"testing"
)

func TestDecodeRejectsUnknownFields(t *testing.T) {
	input := strings.NewReader(`
version: 1
unknown: true
processes:
  app:
    cmd: "echo app"
`)
	if _, err := Decode(input); err == nil {
		t.Fatal("Decode() error = nil, want unknown field error")
	}
}

func TestDecodeAcceptsEnvFileScalarAndList(t *testing.T) {
	scalar := strings.NewReader(`
version: 1
processes:
  app:
    cmd: "echo app"
    env_file: .env
`)
	cfg, err := Decode(scalar)
	if err != nil {
		t.Fatalf("Decode() scalar error = %v, want nil", err)
	}
	if got := []string(cfg.Processes["app"].EnvFile); len(got) != 1 || got[0] != ".env" {
		t.Fatalf("scalar env_file = %#v, want [.env]", got)
	}

	list := strings.NewReader(`
version: 1
processes:
  app:
    cmd: "echo app"
    env_file:
      - .env
      - .env.local
`)
	cfg, err = Decode(list)
	if err != nil {
		t.Fatalf("Decode() list error = %v, want nil", err)
	}
	got := []string(cfg.Processes["app"].EnvFile)
	if len(got) != 2 || got[0] != ".env" || got[1] != ".env.local" {
		t.Fatalf("list env_file = %#v, want [.env .env.local]", got)
	}
}

func TestValidateAcceptsValidConfig(t *testing.T) {
	cfg := validConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestValidateRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name: "missing version",
			mutate: func(cfg *Config) {
				cfg.Version = 0
			},
			wantErr: "version is required",
		},
		{
			name: "unsupported version",
			mutate: func(cfg *Config) {
				cfg.Version = 2
			},
			wantErr: "unsupported version",
		},
		{
			name: "empty processes",
			mutate: func(cfg *Config) {
				cfg.Processes = nil
			},
			wantErr: "processes must define at least one process",
		},
		{
			name: "neither cmd nor exec",
			mutate: func(cfg *Config) {
				cfg.Processes["app"] = Process{}
			},
			wantErr: "must define exactly one of cmd or exec",
		},
		{
			name: "both cmd and exec",
			mutate: func(cfg *Config) {
				cfg.Processes["app"] = Process{
					Cmd:  "echo app",
					Exec: []string{"echo", "app"},
				}
			},
			wantErr: "must define exactly one of cmd or exec",
		},
		{
			name: "empty exec command",
			mutate: func(cfg *Config) {
				cfg.Processes["app"] = Process{
					Exec: []string{""},
				}
			},
			wantErr: "exec[0] must not be empty",
		},
		{
			name: "invalid default restart",
			mutate: func(cfg *Config) {
				cfg.Defaults.Restart = "sometimes"
			},
			wantErr: "defaults.restart must be one of",
		},
		{
			name: "invalid process restart",
			mutate: func(cfg *Config) {
				process := cfg.Processes["app"]
				process.Restart = "sometimes"
				cfg.Processes["app"] = process
			},
			wantErr: "processes.app.restart must be one of",
		},
		{
			name: "invalid default duration",
			mutate: func(cfg *Config) {
				cfg.Defaults.Backoff = "soon"
			},
			wantErr: "defaults.backoff must be a valid duration",
		},
		{
			name: "invalid default stop signal",
			mutate: func(cfg *Config) {
				cfg.Defaults.StopSignal = "USR1"
			},
			wantErr: "defaults.stop_signal must be one of",
		},
		{
			name: "invalid process duration",
			mutate: func(cfg *Config) {
				process := cfg.Processes["app"]
				process.StopTimeout = "later"
				cfg.Processes["app"] = process
			},
			wantErr: "processes.app.stop_timeout must be a valid duration",
		},
		{
			name: "invalid process stop signal",
			mutate: func(cfg *Config) {
				process := cfg.Processes["app"]
				process.StopSignal = "USR1"
				cfg.Processes["app"] = process
			},
			wantErr: "processes.app.stop_signal must be one of",
		},
		{
			name: "empty env key",
			mutate: func(cfg *Config) {
				process := cfg.Processes["app"]
				process.Env = map[string]string{"": "value"}
				cfg.Processes["app"] = process
			},
			wantErr: "processes.app.env must not contain an empty key",
		},
		{
			name: "env key containing equals",
			mutate: func(cfg *Config) {
				process := cfg.Processes["app"]
				process.Env = map[string]string{"BAD=KEY": "value"}
				cfg.Processes["app"] = process
			},
			wantErr: "processes.app.env key \"BAD=KEY\" must not contain =",
		},
		{
			name: "empty env file",
			mutate: func(cfg *Config) {
				process := cfg.Processes["app"]
				process.EnvFile = EnvFiles{""}
				cfg.Processes["app"] = process
			},
			wantErr: "processes.app.env_file[0] must not be empty",
		},
		{
			name: "negative log buffer",
			mutate: func(cfg *Config) {
				process := cfg.Processes["app"]
				process.LogBufferLines = -1
				cfg.Processes["app"] = process
			},
			wantErr: "processes.app.log_buffer_lines must be greater than or equal to 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.mutate(cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %q, want containing %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func validConfig() *Config {
	return &Config{
		Version: SchemaVersion,
		Defaults: Defaults{
			Restart:        "no",
			Backoff:        "1s",
			StopTimeout:    "10s",
			LogBufferLines: 1000,
		},
		Processes: map[string]Process{
			"app": {
				Cmd: "echo app",
			},
		},
	}
}
