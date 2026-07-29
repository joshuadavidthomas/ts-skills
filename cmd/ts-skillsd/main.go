package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/joshuadavidthomas/ts-skills/internal/server"
	"github.com/joshuadavidthomas/ts-skills/internal/version"
	"tailscale.com/hostinfo"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "ts-skillsd: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 && (args[0] == "version" || args[0] == "--version") {
		fmt.Printf("ts-skillsd %s\n", version.Version)
		return nil
	}

	hostinfo.SetApp("ts-skillsd")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dev, err := devModeFromEnv()
	if err != nil {
		return err
	}
	if !dev {
		return server.Run(ctx, server.Config{})
	}

	stateDir, err := devStateDirForLog()
	if err != nil {
		return err
	}
	return server.RunDev(ctx, server.DevConfig{Started: func(address net.Addr) {
		fmt.Fprintf(os.Stderr, "ts-skillsd: dev mode treats every local connection as dev@localhost; never expose this listener\n")
		fmt.Fprintf(os.Stderr, "ts-skillsd: dev mode: http://%s (state: %s)\n", address, stateDir)
	}})
}

func devModeFromEnv() (bool, error) {
	value := os.Getenv("TS_SKILLSD_DEV")
	if value == "" {
		return false, nil
	}
	enabled, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("TS_SKILLSD_DEV must be a boolean value such as 1 or true")
	}
	return enabled, nil
}

func devStateDirForLog() (string, error) {
	stateDir := os.Getenv("TS_SKILLSD_STATE_DIR")
	if stateDir == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("resolve default dev state directory: %w", err)
		}
		stateDir = filepath.Join(cache, "ts-skillsd-dev")
	}
	absolute, err := filepath.Abs(stateDir)
	if err != nil {
		return "", fmt.Errorf("resolve dev state directory: %w", err)
	}
	return absolute, nil
}
