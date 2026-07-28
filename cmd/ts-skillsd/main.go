package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/joshuadavidthomas/ts-skills/internal/daemon"
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

	dev, err := daemon.DevModeFromEnv()
	if err != nil {
		return err
	}
	if !dev {
		config, err := daemon.ConfigFromEnv()
		if err != nil {
			return err
		}
		return daemon.Run(ctx, config)
	}

	config, err := daemon.DevConfigFromEnv()
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "ts-skillsd: dev mode treats every local connection as dev@localhost; never expose this listener\n")
	config.Started = func(address net.Addr) {
		fmt.Fprintf(os.Stderr, "ts-skillsd: dev mode: http://%s (state: %s)\n", address, config.StateDir)
	}
	return daemon.RunDev(ctx, config)
}
