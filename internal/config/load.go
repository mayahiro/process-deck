package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v4"
)

func Decode(r io.Reader) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func LoadFile(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config error: failed to open %s: %w", path, err)
	}
	defer f.Close()

	cfg, err := Decode(f)
	if err != nil {
		return nil, fmt.Errorf("config error: failed to parse %s: %w", path, err)
	}
	return cfg, nil
}

func DiscoverPath(dir string) (string, error) {
	for _, name := range DefaultConfigFilenames {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() {
			return path, nil
		}
		if err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("config error: failed to inspect %s: %w", path, err)
		}
	}

	return "", fmt.Errorf("config error: no config file found; searched %s", strings.Join(DefaultConfigFilenames, ", "))
}
