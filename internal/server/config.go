package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"tailscale.com/ipn"
	"tailscale.com/types/persist"
)

const defaultHostname = "ts-skillsd"

type Config struct {
	StateDir string
	Hostname string
	AuthKey  string
	Tag      string
	Verbose  bool
}

// DevConfig serves the registry over plain HTTP on a loopback address with a
// fixed dev actor. It exists only for local development; it never opens a
// Tailnet node and must never be reachable from other machines.
type DevConfig struct {
	StateDir string
	Listen   string
	// EphemeralFallback retries on port 0 when Listen is busy. It is set only
	// when the listen address was defaulted, so an explicit address that is
	// busy still fails loudly.
	EphemeralFallback bool
	Started           func(net.Addr)
}

func devConfigFromEnv() (DevConfig, error) {
	listen := os.Getenv("TS_SKILLSD_DEV_LISTEN")
	ephemeralFallback := listen == ""
	if listen == "" {
		listen = "127.0.0.1:8080"
	}
	if os.Getenv("TS_SKILLSD_AUTHKEY_FILE") != "" {
		return DevConfig{}, fmt.Errorf("dev mode does not enroll a Tailnet node; unset TS_SKILLSD_AUTHKEY_FILE")
	}
	if os.Getenv("TS_AUTHKEY") != "" {
		return DevConfig{}, fmt.Errorf("dev mode does not enroll a Tailnet node; unset TS_AUTHKEY")
	}
	stateDir := os.Getenv("TS_SKILLSD_STATE_DIR")
	if stateDir == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return DevConfig{}, fmt.Errorf("resolve default dev state directory: %w", err)
		}
		stateDir = filepath.Join(cache, "ts-skillsd-dev")
	}
	return normalizeDevConfig(DevConfig{StateDir: stateDir, Listen: listen, EphemeralFallback: ephemeralFallback})
}

func normalizeDevConfig(config DevConfig) (DevConfig, error) {
	if strings.TrimSpace(config.StateDir) == "" {
		return DevConfig{}, fmt.Errorf("dev state directory is required")
	}
	absolute, err := filepath.Abs(config.StateDir)
	if err != nil {
		return DevConfig{}, fmt.Errorf("resolve dev state directory: %w", err)
	}
	config.StateDir = absolute

	host, port, err := net.SplitHostPort(config.Listen)
	if err != nil {
		return DevConfig{}, fmt.Errorf("dev listen address must be host:port: %w", err)
	}
	if port == "" {
		return DevConfig{}, fmt.Errorf("dev listen address %q must include a port", config.Listen)
	}
	if !isLoopbackHost(host) {
		return DevConfig{}, fmt.Errorf("dev listen address %q must use a loopback host", config.Listen)
	}
	return config, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

// configFromEnv reads server configuration. For first enrollment, it resolves
// TS_SKILLSD_AUTHKEY_FILE before TS_AUTHKEY. Later starts need neither value
// when the tsnet state file already contains an enrolled node key.
func configFromEnv() (Config, error) {
	config := Config{
		StateDir: os.Getenv("TS_SKILLSD_STATE_DIR"),
		Hostname: os.Getenv("TS_SKILLSD_HOSTNAME"),
		Tag:      os.Getenv("TS_SKILLSD_TAG"),
	}
	if config.Hostname == "" {
		config.Hostname = defaultHostname
	}
	if value := os.Getenv("TS_SKILLSD_VERBOSE"); value != "" {
		verbose, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("TS_SKILLSD_VERBOSE must be a boolean value such as 1 or true")
		}
		config.Verbose = verbose
	}
	var err error
	config, err = normalizeConfig(config)
	if err != nil {
		return Config{}, err
	}
	config.AuthKey, err = authKeyFromEnv()
	if err != nil {
		return Config{}, err
	}
	if config.AuthKey != "" {
		return normalizeConfig(config)
	}

	hasState, err := tsnetStateHasNodeKey(filepath.Join(config.StateDir, "tsnet", "tailscaled.state"))
	if err != nil {
		return Config{}, err
	}
	if hasState {
		return config, nil
	}
	if variable := unsupportedTsnetCredential(); variable != "" {
		return Config{}, fmt.Errorf("%s is not supported for ts-skillsd enrollment; use TS_SKILLSD_AUTHKEY_FILE or TS_AUTHKEY", variable)
	}
	return Config{}, fmt.Errorf("first enrollment requires TS_SKILLSD_AUTHKEY_FILE or TS_AUTHKEY; no node key exists in %q", filepath.Join(config.StateDir, "tsnet", "tailscaled.state"))
}

func authKeyFromEnv() (string, error) {
	if authKeyFile := os.Getenv("TS_SKILLSD_AUTHKEY_FILE"); authKeyFile != "" {
		contents, err := os.ReadFile(authKeyFile)
		if err != nil {
			return "", fmt.Errorf("read TS_SKILLSD_AUTHKEY_FILE %q: %w", authKeyFile, err)
		}
		authKey := strings.TrimSpace(string(contents))
		if authKey == "" {
			return "", fmt.Errorf("TS_SKILLSD_AUTHKEY_FILE %q is empty", authKeyFile)
		}
		return authKey, nil
	}
	return strings.TrimSpace(os.Getenv("TS_AUTHKEY")), nil
}

func tsnetStateHasNodeKey(path string) (bool, error) {
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read tsnet state %q: %w", path, err)
	}
	var state map[string][]byte
	if err := json.Unmarshal(contents, &state); err != nil {
		return false, fmt.Errorf("parse tsnet state %q: %w", path, err)
	}
	profileKey := ipn.StateKey(state[string(ipn.CurrentProfileKey(""))])
	if profileKey == "" {
		profileKey = ipn.LegacyGlobalDaemonStateKey
	}
	profile, found := state[string(profileKey)]
	if !found || len(profile) == 0 {
		return false, nil
	}
	prefs := ipn.NewPrefs()
	prefs.Persist = &persist.Persist{}
	if err := ipn.PrefsFromBytes(profile, prefs); err != nil {
		return false, fmt.Errorf("parse tsnet profile %q in %q: %w", profileKey, path, err)
	}
	return !prefs.Persist.PrivateNodeKey.IsZero(), nil
}

var unsupportedTsnetCredentials = []string{
	"TS_AUTH_KEY",
	"TS_CLIENT_SECRET",
	"TS_CLIENT_ID",
	"TS_ID_TOKEN",
	"TS_AUDIENCE",
	"TSNET_FORCE_LOGIN",
	"TS_CONTROL_URL",
}

func unsupportedTsnetCredential() string {
	for _, variable := range unsupportedTsnetCredentials {
		if os.Getenv(variable) != "" {
			return variable
		}
	}
	return ""
}

func clearTsnetCredentialEnvironment() {
	for _, variable := range append([]string{"TS_AUTHKEY"}, unsupportedTsnetCredentials...) {
		_ = os.Unsetenv(variable)
	}
}

func normalizeConfig(config Config) (Config, error) {
	if strings.TrimSpace(config.StateDir) == "" {
		return Config{}, fmt.Errorf("TS_SKILLSD_STATE_DIR is required")
	}
	absolute, err := filepath.Abs(config.StateDir)
	if err != nil {
		return Config{}, fmt.Errorf("resolve server state directory: %w", err)
	}
	config.StateDir = absolute

	if !validHostname(config.Hostname) {
		return Config{}, fmt.Errorf("TS_SKILLSD_HOSTNAME must be a DNS label of at most 63 characters")
	}
	if config.Tag != "" && !validTag(config.Tag) {
		return Config{}, fmt.Errorf("TS_SKILLSD_TAG must have the form tag:name using letters, numbers, and hyphens")
	}
	if strings.IndexFunc(config.AuthKey, unicode.IsControl) >= 0 {
		return Config{}, fmt.Errorf("tailnet auth key contains a control character")
	}
	return config, nil
}

func validHostname(hostname string) bool {
	if len(hostname) == 0 || len(hostname) > 63 || hostname[0] == '-' || hostname[len(hostname)-1] == '-' {
		return false
	}
	for _, char := range hostname {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' {
			continue
		}
		return false
	}
	return true
}

func validTag(tag string) bool {
	name, found := strings.CutPrefix(tag, "tag:")
	if !found || name == "" || !isASCIILetter(name[0]) {
		return false
	}
	for index := range len(name) {
		char := name[index]
		if isASCIILetter(char) || (char >= '0' && char <= '9') || char == '-' {
			continue
		}
		return false
	}
	return name[len(name)-1] != '-'
}

func isASCIILetter(char byte) bool {
	return (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')
}
