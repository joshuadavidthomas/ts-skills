package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	Registry *url.URL
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
	parsed, err := url.Parse(*raw.Registry)
	if err != nil {
		return Config{}, fmt.Errorf("decode config %q registry: %w", path, err)
	}
	origin, err := validateOrigin(parsed)
	if err != nil {
		return Config{}, fmt.Errorf("decode config %q registry: %w", path, err)
	}
	return Config{Registry: origin}, nil
}

func validateOrigin(source *url.URL) (*url.URL, error) {
	if source == nil || (source.Scheme != "https" && source.Scheme != "http") {
		return nil, fmt.Errorf("URL scheme must be HTTPS or loopback HTTP")
	}
	if source.Host == "" || source.Hostname() == "" || source.Opaque != "" {
		return nil, fmt.Errorf("URL must have an origin host")
	}
	if source.User != nil || source.RawQuery != "" || source.ForceQuery || source.Fragment != "" {
		return nil, fmt.Errorf("URL must not contain user info, a query, or a fragment")
	}
	if (source.Path != "" && source.Path != "/") || (source.RawPath != "" && source.RawPath != "/") {
		return nil, fmt.Errorf("URL must not contain a path")
	}
	if source.Scheme == "http" && !isLoopbackHost(source.Hostname()) {
		return nil, fmt.Errorf("cleartext HTTP is allowed only for a loopback host")
	}
	clone := *source
	clone.Path = ""
	clone.RawPath = ""
	return &clone, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
