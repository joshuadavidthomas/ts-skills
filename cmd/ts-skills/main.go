package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/joshuadavidthomas/ts-skills/internal/client"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := client.Run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		os.Exit(1)
	}
}
