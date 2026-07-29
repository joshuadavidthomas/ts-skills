package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

type config struct {
	registry origin
}

type document struct {
	Registry *string `toml:"registry"`
}

func defaultPath() (string, error) {
	directory, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user configuration directory: %w", err)
	}
	if directory == "" {
		return "", fmt.Errorf("user configuration directory is empty")
	}
	return filepath.Join(directory, "ts-skills", "config.toml"), nil
}

func load(path string) (config, error) {
	if path == "" {
		return config{}, fmt.Errorf("config path must be provided")
	}
	file, err := os.Open(path)
	if err != nil {
		return config{}, fmt.Errorf("open config %q: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	var raw document
	decoder := toml.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if raw.Registry == nil || *raw.Registry == "" {
		return config{}, fmt.Errorf("decode config %q: registry is required", path)
	}
	origin, err := parseOrigin(*raw.Registry)
	if err != nil {
		return config{}, fmt.Errorf("decode config %q registry: %w", path, err)
	}
	return config{registry: origin}, nil
}
