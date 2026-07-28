package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/joshuadavidthomas/ts-skills/internal/client"
	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	Registry client.Origin
}

type document struct {
	Registry *string `toml:"registry"`
}

func DefaultPath() (string, error) {
	directory, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user configuration directory: %w", err)
	}
	if directory == "" {
		return "", fmt.Errorf("user configuration directory is empty")
	}
	return filepath.Join(directory, "ts-skills", "config.toml"), nil
}

func Load(path string) (Config, error) {
	if path == "" {
		return Config{}, fmt.Errorf("config path must be provided")
	}
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config %q: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	var raw document
	decoder := toml.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if raw.Registry == nil || *raw.Registry == "" {
		return Config{}, fmt.Errorf("decode config %q: registry is required", path)
	}
	origin, err := client.ParseOrigin(*raw.Registry)
	if err != nil {
		return Config{}, fmt.Errorf("decode config %q registry: %w", path, err)
	}
	return Config{Registry: origin}, nil
}
