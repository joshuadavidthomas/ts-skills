package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if !alreadyReported(err) {
			fmt.Fprintf(os.Stderr, "ts-skills: %v\n", err)
		}
		os.Exit(1)
	}
}
