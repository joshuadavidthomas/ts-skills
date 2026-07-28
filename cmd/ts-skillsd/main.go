package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/joshuadavidthomas/ts-skills/internal/daemon"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dev, err := daemon.DevModeFromEnv()
	if err == nil {
		if dev {
			var config daemon.DevConfig
			config, err = daemon.DevConfigFromEnv()
			if err == nil {
				fmt.Fprintf(os.Stderr, "ts-skillsd: dev mode treats every local connection as dev@localhost; never expose this listener\n")
				config.Started = func(address net.Addr) {
					fmt.Fprintf(os.Stderr, "ts-skillsd: dev mode: http://%s (state: %s)\n", address, config.StateDir)
				}
				err = daemon.RunDev(ctx, config)
			}
		} else {
			var config daemon.Config
			config, err = daemon.ConfigFromEnv()
			if err == nil {
				err = daemon.Run(ctx, config)
			}
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "ts-skillsd: %v\n", err)
		os.Exit(1)
	}
}
